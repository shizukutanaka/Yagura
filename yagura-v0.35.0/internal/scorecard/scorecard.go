// Package scorecard は api.scorecard.dev への問合せクライアント。
//
// OpenSSF Scorecard は OSS プロジェクトの 18 個のセキュリティ checks を
// 0-10 スコアで採点する。Scorecard 自体は GitHub Actions で動作するが、
// 公開済みプロジェクトについては api.scorecard.dev/projects/... から
// 既存スコアを取得できる(rate-limited だが Yagura のポートフォリオ規模で十分)。
//
// 設計判断(security spec S1.1):
//   - Read-only API call
//   - 結果は MCP tool として返すのみ、Yagura は scorecard.yml を生成しない
//   - 失敗時(unscored repo、API 障害)は graceful degradation
//   - ゼロ依存(標準ライブラリのみ)
package scorecard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.scorecard.dev"
	defaultTimeout = 30 * time.Second
	maxResponseLen = 2 << 20 // 2 MB
)

// Score は単一プロジェクトの Scorecard 結果。
type Score struct {
	Repo             string    `json:"repo"`   // "github.com/owner/repo"
	Commit           string    `json:"commit"` // 解析対象 commit SHA
	Score            float64   `json:"score"`  // 0.0 - 10.0(集約スコア)
	Date             time.Time `json:"date"`   // スコア計測日
	Checks           []Check   `json:"checks"` // 各 check の結果
	ScorecardVersion string    `json:"scorecard_version"`
}

// Check は単一 check の結果。
type Check struct {
	Name          string   `json:"name"`          // 例: "Branch-Protection"
	Score         int      `json:"score"`         // -1 (N/A) または 0-10
	Reason        string   `json:"reason"`        // 短い説明
	Details       []string `json:"details"`       // 詳細(N>0 件まで)
	Documentation string   `json:"documentation"` // check のドキュメント URL
}

// Client は Scorecard API クライアント。
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// Option は Client 生成オプション。
type Option func(*Client)

// WithBaseURL は問合せ先 URL を上書きする(主にテスト用)。
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithHTTPClient は HTTP クライアントを上書きする。
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.httpClient = h } }

// New は Client を生成する。
func New(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		baseURL:    defaultBaseURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Fetch は GitHub repo (例: "owner/repo" もしくは "github.com/owner/repo")の
// Scorecard 結果を取得する。
//
// Scorecard 解析が未実施の repo は ErrNotScored を返す。
// このエラーは MCP tool で「サポート外プロジェクト」として表示できる。
func (c *Client) Fetch(ctx context.Context, repo string) (*Score, error) {
	repo = normalizeRepo(repo)
	if repo == "" {
		return nil, errors.New("scorecard: repo is required")
	}

	endpoint := fmt.Sprintf("%s/projects/%s", c.baseURL, url.PathEscape(repo))
	// PathEscape は "/" を %2F に変換するが、API は "/" を期待するので戻す
	endpoint = strings.ReplaceAll(endpoint, "%2F", "/")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "yagura/0.3 (+https://github.com/shizukutanaka/yagura)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotScored
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("scorecard: status %d: %s", resp.StatusCode,
			strings.TrimSpace(string(snippet)))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseLen))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var wire wireScore
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return wire.toScore(), nil
}

// ErrNotScored は Scorecard が当該 repo を未解析であることを示す。
var ErrNotScored = errors.New("scorecard: repo not scored yet")

// PriorityChecks は Scorecard の 18 check のうち重要度が高い 7 つを返す。
//
// Yagura では「全 check の数値羅列」よりも「優先 check の合否」を
// 強調表示する。これは Apple 流取捨選択(必要なものだけ表示)に従う。
func PriorityChecks() []string {
	return []string{
		"Branch-Protection",
		"Code-Review",
		"Dangerous-Workflow",
		"Dependency-Update-Tool",
		"Pinned-Dependencies",
		"Token-Permissions",
		"Vulnerabilities",
	}
}

// HealthCategory は score を 4 段階のカテゴリに分類する。
// API consumer (MCP tool 等) がカラー表示などに使えるよう正規化する。
func HealthCategory(score float64) string {
	switch {
	case score >= 8.0:
		return "excellent"
	case score >= 6.0:
		return "good"
	case score >= 4.0:
		return "fair"
	case score >= 0:
		return "poor"
	}
	return "unknown"
}

// ─── internal ────────────────────────────────────────────────

func normalizeRepo(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "github.com/")
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return ""
	}
	// "owner/repo" → "github.com/owner/repo"
	return "github.com/" + parts[0] + "/" + parts[1]
}

// wireScore は api.scorecard.dev のレスポンス形式。
type wireScore struct {
	Date      string      `json:"date"`
	Repo      wireRepo    `json:"repo"`
	Scorecard wireMeta    `json:"scorecard"`
	Score     float64     `json:"score"`
	Checks    []wireCheck `json:"checks"`
}

type wireRepo struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

type wireMeta struct {
	Version string `json:"version"`
}

type wireCheck struct {
	Name          string   `json:"name"`
	Score         int      `json:"score"`
	Reason        string   `json:"reason"`
	Details       []string `json:"details"`
	Documentation struct {
		URL string `json:"url"`
	} `json:"documentation"`
}

func (w wireScore) toScore() *Score {
	parsedDate, _ := time.Parse("2006-01-02", w.Date)
	checks := make([]Check, 0, len(w.Checks))
	for _, c := range w.Checks {
		checks = append(checks, Check{
			Name:          c.Name,
			Score:         c.Score,
			Reason:        c.Reason,
			Details:       c.Details,
			Documentation: c.Documentation.URL,
		})
	}
	// Sort by name for stable presentation
	sort.SliceStable(checks, func(i, j int) bool {
		return checks[i].Name < checks[j].Name
	})
	return &Score{
		Repo:             w.Repo.Name,
		Commit:           w.Repo.Commit,
		Score:            w.Score,
		Date:             parsedDate,
		Checks:           checks,
		ScorecardVersion: w.Scorecard.Version,
	}
}
