// Package project は Yagura が管理するプロジェクトの定義を提供する。
//
// 1 プロジェクト = 1 GitHub リポジトリ(+ 任意のローカルパス + メタデータ)。
// 状態は registry パッケージで永続化する。
package project

import (
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Stage はプロジェクトのライフサイクル状態。
type Stage string

const (
	// StageActive は活発に開発中のプロジェクト。
	StageActive Stage = "active"
	// StageMaintenance は新機能開発を止め保守のみ行うプロジェクト。
	StageMaintenance Stage = "maintenance"
	// StagePaused は一時的に開発を止めているプロジェクト。
	StagePaused Stage = "paused"
	// StageArchived は終了し参照のみのプロジェクト。
	StageArchived Stage = "archived"
)

var validStages = map[Stage]bool{
	StageActive: true, StageMaintenance: true, StagePaused: true, StageArchived: true,
}

// CIStatus は GitHub Actions / CI の最新状態。
type CIStatus string

const (
	// CIStatusPassing は最新 CI が成功している状態。
	CIStatusPassing CIStatus = "passing"
	// CIStatusFailing は最新 CI が失敗している状態。
	CIStatusFailing CIStatus = "failing"
	// CIStatusUnknown は CI 状態が未取得・不明な状態。
	CIStatusUnknown CIStatus = "unknown"
)

// SprintPhase は gstack 7 フェーズ(Think→Plan→Build→Review→Test→Ship→Reflect)。
type SprintPhase string

const (
	// PhaseThink は課題理解・調査フェーズ。
	PhaseThink SprintPhase = "think"
	// PhasePlan は設計・計画フェーズ。
	PhasePlan SprintPhase = "plan"
	// PhaseBuild は実装フェーズ。
	PhaseBuild SprintPhase = "build"
	// PhaseReview はコードレビューフェーズ。
	PhaseReview SprintPhase = "review"
	// PhaseTest は検証・テストフェーズ。
	PhaseTest SprintPhase = "test"
	// PhaseShip はリリースフェーズ。
	PhaseShip SprintPhase = "ship"
	// PhaseReflect は振り返りフェーズ。
	PhaseReflect SprintPhase = "reflect"
)

var validPhases = map[SprintPhase]bool{
	PhaseThink: true, PhasePlan: true, PhaseBuild: true,
	PhaseReview: true, PhaseTest: true, PhaseShip: true, PhaseReflect: true,
}

// AllPhasesInOrder は gstack の正規順序。
var AllPhasesInOrder = []SprintPhase{
	PhaseThink, PhasePlan, PhaseBuild, PhaseReview,
	PhaseTest, PhaseShip, PhaseReflect,
}

// NextPhase は現在 phase の次を返す。Reflect の次は Think に戻る(循環)。
// 未知の phase が渡された場合は Think を返す(リカバリ)。
func NextPhase(p SprintPhase) SprintPhase {
	for i, cur := range AllPhasesInOrder {
		if cur == p {
			return AllPhasesInOrder[(i+1)%len(AllPhasesInOrder)]
		}
	}
	return PhaseThink
}

// Milestone は Sprint 内の個別到達点。
type Milestone struct {
	Title     string     `json:"title"`
	Done      bool       `json:"done"`
	CreatedAt time.Time  `json:"created_at"`
	DoneAt    *time.Time `json:"done_at,omitempty"`
}

// Sprint は gstack スプリントの実行状態。
type Sprint struct {
	Phase      SprintPhase `json:"phase"`
	StartedAt  time.Time   `json:"started_at"`
	Goal       string      `json:"goal"`
	Milestones []Milestone `json:"milestones,omitempty"`
}

// Project は Yagura の中核データ構造。
type Project struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Repository  string `json:"repository"`
	LocalPath   string `json:"local_path,omitempty"`

	Language  string   `json:"language,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`

	LatestVersion  string    `json:"latest_version,omitempty"`
	LatestActivity time.Time `json:"latest_activity,omitempty"`
	OpenPRs        int       `json:"open_prs"`
	OpenIssues     int       `json:"open_issues"`
	CIStatus       CIStatus  `json:"ci_status,omitempty"`
	TestCoverage   float64   `json:"test_coverage,omitempty"`
	StarCount      int       `json:"star_count,omitempty"`
	// RepoPublic は scanner が観測した repository の公開状態(sensor、scanner 専用)。
	// true = GitHub 上で Public。omitempty なので private/未観測(false)では出力されない。
	// trust base: MCP tool (yagura_update) からは設定不可。
	RepoPublic bool `json:"repo_public,omitempty"`

	// Security health (S1.1 + S1.2). scanner が 24h 周期で更新する。
	// 取得失敗時はゼロ値のまま放置(graceful degradation)。
	ScorecardScore float64   `json:"scorecard_score,omitempty"` // 0.0-10.0
	ScorecardAt    time.Time `json:"scorecard_at,omitempty"`    // 最終取得日時
	VulnCritical   int       `json:"vuln_critical,omitempty"`   // CRITICAL 件数
	VulnHigh       int       `json:"vuln_high,omitempty"`       // HIGH 件数
	VulnMedium     int       `json:"vuln_medium,omitempty"`     // MEDIUM 件数
	VulnLow        int       `json:"vuln_low,omitempty"`        // LOW 件数
	VulnScanAt     time.Time `json:"vuln_scan_at,omitempty"`    // 最終スキャン日時

	Stage    Stage   `json:"stage"`
	Priority int     `json:"priority"`
	Notes    string  `json:"notes,omitempty"`
	Sprint   *Sprint `json:"sprint,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	MihariEndpoint string `json:"mihari_endpoint,omitempty"`
}

// TotalVulns は CVSS の全 severity 合計件数を返す。
func (p *Project) TotalVulns() int {
	return p.VulnCritical + p.VulnHigh + p.VulnMedium + p.VulnLow
}

// HasCriticalSecurityIssue は対応必須レベルの security 問題があるかを返す。
// CRITICAL/HIGH vuln または Scorecard < 5.0 を「対応必須」とみなす。
func (p *Project) HasCriticalSecurityIssue() bool {
	if p.VulnCritical > 0 || p.VulnHigh > 0 {
		return true
	}
	if !p.ScorecardAt.IsZero() && p.ScorecardScore < 5.0 {
		return true
	}
	return false
}

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,49}$`)
var repoPattern = regexp.MustCompile(`^(github\.com/)?[a-zA-Z0-9][a-zA-Z0-9._-]*/[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Validate は新規/更新時の妥当性検証。
func (p *Project) Validate() error {
	var errs []error
	if !slugPattern.MatchString(p.Slug) {
		errs = append(errs, errors.New(
			"slug must be lowercase alphanumeric + hyphens, 1-50 chars, starting with alphanumeric"))
	}
	if p.DisplayName == "" {
		errs = append(errs, errors.New("display_name is required"))
	}
	if p.Repository == "" {
		errs = append(errs, errors.New("repository is required"))
	} else if !repoPattern.MatchString(p.Repository) {
		errs = append(errs, errors.New(
			"repository must be 'owner/repo' or 'github.com/owner/repo'"))
	}
	if p.Stage == "" {
		p.Stage = StageActive
	} else if !validStages[p.Stage] {
		errs = append(errs, errors.New(
			"stage must be one of: active, maintenance, paused, archived"))
	}
	if p.Priority < 0 || p.Priority > 5 {
		errs = append(errs, errors.New("priority must be 0-5"))
	}
	if p.Sprint != nil && !validPhases[p.Sprint.Phase] {
		errs = append(errs, errors.New(
			"sprint.phase must be one of: think/plan/build/review/test/ship/reflect"))
	}
	if p.LocalPath != "" && strings.Contains(p.LocalPath, "..") {
		errs = append(errs, errors.New("local_path must not contain '..'"))
	}
	return errors.Join(errs...)
}

// OwnerRepo は Repository から (owner, repo) を抽出する。
func (p *Project) OwnerRepo() (owner, repo string) {
	s := strings.TrimPrefix(p.Repository, "github.com/")
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// IsActive は yagura_today / scanner ターゲット判定。
func (p *Project) IsActive() bool { return p.Stage == StageActive }

// IsScannable は scanner の対象判定(active + maintenance)。
func (p *Project) IsScannable() bool {
	return p.Stage == StageActive || p.Stage == StageMaintenance
}

// StaleAge は LatestActivity からの経過時間。
func (p *Project) StaleAge(now time.Time) time.Duration {
	if p.LatestActivity.IsZero() {
		return 0
	}
	return now.Sub(p.LatestActivity)
}

// HasTag は tag を含むか判定(case-insensitive)。
func (p *Project) HasTag(tag string) bool {
	target := strings.ToLower(tag)
	for _, t := range p.Tags {
		if strings.ToLower(t) == target {
			return true
		}
	}
	return false
}

// MatchesQuery は q(case-insensitive 部分一致)が slug / display_name / notes /
// repository / tags のいずれかにマッチするか判定する。q が空なら常に true。
// MCP search tool と CLI search が共有する(以前は両者に重複実装されていた)。
func (p *Project) MatchesQuery(q string) bool {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(p.Slug), q) ||
		strings.Contains(strings.ToLower(p.DisplayName), q) ||
		strings.Contains(strings.ToLower(p.Notes), q) ||
		strings.Contains(strings.ToLower(p.Repository), q) {
		return true
	}
	for _, t := range p.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			return true
		}
	}
	return false
}

// DaysSince は t から now までの経過日数(整数)を返す。t が zero なら -1
// (=「データなし」)。list/search/today の "days since commit" 表示で共有する。
func DaysSince(t, now time.Time) int {
	if t.IsZero() {
		return -1
	}
	return int(now.Sub(t).Hours() / 24)
}

// SortBySlug は slug 昇順ソート。
func SortBySlug(ps []*Project) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Slug < ps[j].Slug })
}

// SortByActivity は最新活動降順ソート(同時刻は slug 昇順で安定化)。
func SortByActivity(ps []*Project) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].LatestActivity.Equal(ps[j].LatestActivity) {
			return ps[i].Slug < ps[j].Slug
		}
		return ps[i].LatestActivity.After(ps[j].LatestActivity)
	})
}
