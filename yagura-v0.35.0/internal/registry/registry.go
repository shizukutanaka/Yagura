// Package registry はプロジェクト群の CRUD と永続化を担う。
//
// 設計判断:
//   - in-memory map + JSON ファイル(slug ごと 1 ファイル)
//   - 書込みは atomic(一時 file → fsync → rename)で部分書込みを防ぐ
//   - 23 プロジェクト規模では十分な性能(全件 in-memory)
//   - 100+ プロジェクトになったら ADR を切って SQLite 移行検討
//   - ファイル名は slug。slug は validation で `^[a-z0-9][a-z0-9-]{0,49}$` に
//     制限されているため、path traversal は構造的に不可能。
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shizukutanaka/yagura/internal/atomicfile"
	"github.com/shizukutanaka/yagura/internal/project"
)

var (
	// ErrNotFound は指定 slug のプロジェクトが registry に存在しない場合に返る。
	// errors.Is で判定できるよう sentinel として公開する。
	ErrNotFound = errors.New("project not found")
	// ErrAlreadyExists は Add 時に同名 slug が既に登録済みの場合に返る。
	ErrAlreadyExists = errors.New("project already exists")
)

// Registry はプロジェクト群の in-memory インデックスと JSON 永続化を束ねる。
// 全メソッドは goroutine-safe(内部 RWMutex で保護)。ゼロ値は使えないので
// 必ず New で生成すること。
type Registry struct {
	dir string

	mu       sync.RWMutex
	projects map[string]*project.Project
}

// New は dir をストレージ root とする Registry を生成し、既存の *.json を読み込む。
// dir が空ならエラー。dir は無ければ作成する。読込中に壊れた project ファイルが
// あっても Registry 自体は返し、その読込エラーを併せて返す(部分起動を許容)。
func New(dir string) (*Registry, error) {
	if dir == "" {
		return nil, errors.New("registry dir is required")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create registry dir: %w", err)
	}
	r := &Registry{
		dir:      dir,
		projects: make(map[string]*project.Project),
	}
	loadErr := r.loadAll()
	return r, loadErr
}

func (r *Registry) loadAll() error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return fmt.Errorf("read registry dir: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(r.dir, e.Name())
		p, lerr := loadProjectFile(path)
		if lerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), lerr))
			continue
		}
		r.projects[p.Slug] = p
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func loadProjectFile(path string) (*project.Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p project.Project
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("validate: %w", err)
	}
	return &p, nil
}

// Add は新規プロジェクトを登録する。p を Validate し、slug が未使用なら
// CreatedAt/UpdatedAt を now で設定して永続化する。既存 slug なら ErrAlreadyExists。
func (r *Registry) Add(p *project.Project) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.projects[p.Slug]; exists {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, p.Slug)
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now
	if err := r.persist(p); err != nil {
		return err
	}
	r.projects[p.Slug] = p
	return nil
}

// Update は既存プロジェクトを置き換える。p を Validate し、CreatedAt は既存値を
// 保持したまま UpdatedAt を now に更新して永続化する。未登録 slug なら ErrNotFound。
func (r *Registry) Update(p *project.Project) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("validate: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.projects[p.Slug]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, p.Slug)
	}
	p.CreatedAt = existing.CreatedAt
	p.UpdatedAt = time.Now().UTC()
	if err := r.persist(p); err != nil {
		return err
	}
	r.projects[p.Slug] = p
	return nil
}

// Delete は slug のプロジェクトを registry と disk から削除する。未登録なら
// ErrNotFound。ファイルが既に無い場合は in-memory 側のみ削除して成功扱い。
func (r *Registry) Delete(slug string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.projects[slug]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	if err := os.Remove(r.fileFor(slug)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove file: %w", err)
	}
	delete(r.projects, slug)
	return nil
}

// Get は slug のプロジェクトの clone を返す(内部状態の取り違えを防ぐため deep copy)。
// 未登録なら ErrNotFound。
func (r *Registry) Get(slug string) (*project.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.projects[slug]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, slug)
	}
	return cloneProject(p), nil
}

// List は全プロジェクトの clone を slug 昇順で返す(決定論的)。
func (r *Registry) List() []*project.Project {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*project.Project, 0, len(r.projects))
	for _, p := range r.projects {
		out = append(out, cloneProject(p))
	}
	project.SortBySlug(out)
	return out
}

// Count は stage 別のプロジェクト数を返す。全 stage を 0 で初期化するので、
// 0 件の stage も key として必ず含まれる。
func (r *Registry) Count() map[project.Stage]int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	counts := map[project.Stage]int{
		project.StageActive: 0, project.StageMaintenance: 0,
		project.StagePaused: 0, project.StageArchived: 0,
	}
	for _, p := range r.projects {
		counts[p.Stage]++
	}
	return counts
}

// Filter は predicate を満たすプロジェクトの clone を slug 昇順で返す(決定論的)。
func (r *Registry) Filter(predicate func(*project.Project) bool) []*project.Project {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*project.Project, 0)
	for _, p := range r.projects {
		if predicate(p) {
			out = append(out, cloneProject(p))
		}
	}
	project.SortBySlug(out)
	return out
}

func (r *Registry) persist(p *project.Project) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := atomicfile.Write(r.fileFor(p.Slug), data, 0o600); err != nil {
		return fmt.Errorf("persist %s: %w", p.Slug, err)
	}
	return nil
}

func (r *Registry) fileFor(slug string) string {
	return filepath.Join(r.dir, slug+".json")
}

func cloneProject(p *project.Project) *project.Project {
	c := *p
	if p.Tags != nil {
		c.Tags = append([]string(nil), p.Tags...)
	}
	if p.DependsOn != nil {
		c.DependsOn = append([]string(nil), p.DependsOn...)
	}
	if p.Sprint != nil {
		s := *p.Sprint
		if p.Sprint.Milestones != nil {
			s.Milestones = append([]project.Milestone(nil), p.Sprint.Milestones...)
		}
		c.Sprint = &s
	}
	return &c
}
