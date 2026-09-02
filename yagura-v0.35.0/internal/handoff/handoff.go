// Package handoff persists session context for cross-agent handoff (v0.13.0).
//
// 動機:
//
//	Claude Code が quota 切れになって Windsurf に切替えるとき、Cascade が
//	どこから作業を引き継ぐべきか分かるよう、現在の作業 context を yagura に
//	保存しておく必要がある。
//
// 保存内容(Context):
//   - workspace: 現在の git リポジトリ root
//   - branch: 現在の git ブランチ
//   - active_files: 編集中のファイル一覧
//   - plan_md_step: Plan.md の現在進行中のフェーズ
//   - open_todos: 未解決の TODO / FIXME / NOTE
//   - last_commit: 最後の commit SHA(再開前 base 状態)
//   - free_notes: 任意のメモ(agent 間引き継ぎメッセージ)
//
// 永続化:
//   - state_dir/handoff.json に書く(atomic: write→fsync→rename)
//   - スキーマは forward-compatible(unknown fields は無視)
//   - 単一 file で OK(複数 workspace を扱う場合は workspace_id key 追加可)
package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shizukutanaka/yagura/internal/atomicfile"
)

// Context は handoff session の全データ。
type Context struct {
	Version     int       `json:"version"` // スキーマバージョン(現在 1)
	SavedAt     time.Time `json:"saved_at"`
	SavedBy     string    `json:"saved_by"`  // "claude_code" | "windsurf" | 手動 etc.
	Workspace   string    `json:"workspace"` // 絶対パス
	Branch      string    `json:"branch,omitempty"`
	LastCommit  string    `json:"last_commit,omitempty"` // SHA
	ActiveFiles []string  `json:"active_files,omitempty"`
	PlanMdStep  string    `json:"plan_md_step,omitempty"` // Plan.md の現在進行中フェーズ
	OpenTodos   []Todo    `json:"open_todos,omitempty"`
	FreeNotes   string    `json:"free_notes,omitempty"` // 自由記述メモ
}

// Todo は単一 TODO/FIXME/NOTE エントリ。
type Todo struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Kind string `json:"kind"` // "TODO" | "FIXME" | "NOTE" | "XXX"
	Text string `json:"text"`
}

// Store は handoff context を永続化する。並行安全。
type Store struct {
	path string

	mu sync.Mutex
}

// New は state_dir/handoff.json を扱う Store を生成する。
//
// stateDir は yagura の state directory(audit log と同じ場所)。
// 親 dir が存在しない場合は MkdirAll で作成。
func New(stateDir string) (*Store, error) {
	if stateDir == "" {
		return nil, errors.New("stateDir is required")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir state dir: %w", err)
	}
	return &Store{
		path: filepath.Join(stateDir, "handoff.json"),
	}, nil
}

// Save は context を atomic に書き出す。
//
// 既存 context は上書き(merge ではなく replace)。
// saved_at は自動で現在時刻に上書きされる。
// 書き込みは write→fsync→rename パターンで crash-safe。
func (s *Store) Save(ctx *Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// 最低限の validation
	if ctx.Workspace == "" {
		return errors.New("workspace is required")
	}
	if ctx.Version == 0 {
		ctx.Version = 1
	}
	if ctx.SavedAt.IsZero() {
		ctx.SavedAt = time.Now().UTC()
	} else {
		ctx.SavedAt = ctx.SavedAt.UTC()
	}

	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	// atomic write: temp → fsync → rename
	if err := atomicfile.Write(s.path, data, 0o600); err != nil {
		return fmt.Errorf("save handoff: %w", err)
	}
	return nil
}

// Load は最後に保存された context を返す。
//
// 未保存(ファイル未存在)の場合は (nil, ErrNotSaved)。
func (s *Store) Load() (*Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotSaved
		}
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	var ctx Context
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &ctx, nil
}

// Clear は保存済み context を削除する(handoff 完了後の cleanup 用)。
//
// ファイルが既に存在しない場合は nil を返す(idempotent)。
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove: %w", err)
	}
	return nil
}

// Path は handoff.json の絶対パスを返す(debug/CLI 用)。
func (s *Store) Path() string {
	return s.path
}

// ErrNotSaved は Load() で context が未保存の場合に返される。
var ErrNotSaved = errors.New("no handoff context saved")
