// Package github は Yagura が必要とする最小限の GitHub REST API クライアントを提供する。
//
// Mihari の internal/github と異なり、Yagura は read-only かつ簡素な情報のみ必要:
//   - リポジトリの最終 push 時刻
//   - open PR / Issue 数
//   - 最新 release tag
//   - default branch の最新 CI run 結果
//
// 設計判断:
//   - zero-dep: net/http のみ
//   - 認証は PAT/fine-grained token を Authorization ヘッダで
//   - GitHub App は将来対応(Yagura は単一ユーザ用途なので PAT で十分)
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	// ErrNotFound は GitHub API が 404 を返した場合の sentinel。
	ErrNotFound = errors.New("not found")
	// ErrUnauthorized は 401(トークン無効/未設定)の sentinel。
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden は 403(権限不足、rate limit 以外)の sentinel。
	ErrForbidden = errors.New("forbidden")
	// ErrRateLimited は 403/429 のうち rate limit 超過を示す sentinel。
	ErrRateLimited = errors.New("rate limited")
)

// Client は GitHub REST API クライアント。並行安全。
//
// 認証は TokenStore 経由(S0.1 per-owner credential separation)。
// 後方互換: Config.Token のみ指定された場合は single-token fallback として動作。
type Client struct {
	tokens  *TokenStore
	baseURL string
	http    *http.Client

	mu        sync.RWMutex
	rateLimit RateLimit
}

// Config は Client の初期化パラメータ。
//
// 認証は次の優先順:
//  1. Tokens (*TokenStore) — multi-owner credential separation(S0.1 推奨)
//  2. Token (string)         — 単一 fallback token(後方互換)
type Config struct {
	Tokens  *TokenStore // owner-specific tokens(S0.1)
	Token   string      // single fallback token(後方互換)
	BaseURL string
	Timeout time.Duration
}

// NewClient は Client を生成する。
//
// Tokens が指定されていれば最優先で使用。指定されていない場合は Token を
// fallback として持つ単一 store を内部生成する(後方互換)。
func NewClient(c Config) *Client {
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.BaseURL == "" {
		c.BaseURL = "https://api.github.com"
	}
	store := c.Tokens
	if store == nil {
		store = NewTokenStore(c.Token)
	}
	return &Client{
		tokens:  store,
		baseURL: strings.TrimRight(c.BaseURL, "/"),
		http: &http.Client{
			Timeout: c.Timeout,
		},
	}
}

// RateLimit は最新の rate limit 状態。
type RateLimit struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

// LastRateLimit は最後に観測した rate limit を返す(API 呼出なし)。
func (c *Client) LastRateLimit() RateLimit {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rateLimit
}

// Repository は最小限の repository 情報。
type Repository struct {
	Owner           string    `json:"-"`
	Name            string    `json:"-"`
	FullName        string    `json:"full_name"`
	DefaultBranch   string    `json:"default_branch"`
	PushedAt        time.Time `json:"pushed_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	StargazersCount int       `json:"stargazers_count"`
	Description     string    `json:"description"`
	Language        string    `json:"language"`
	Archived        bool      `json:"archived"`
	Private         bool      `json:"private"`
}

// GetRepository は GET /repos/{owner}/{repo} を呼ぶ。
func (c *Client) GetRepository(ctx context.Context, owner, repo string) (*Repository, error) {
	var r Repository
	path := fmt.Sprintf("/repos/%s/%s", owner, repo)
	if err := c.doJSON(ctx, http.MethodGet, path, &r); err != nil {
		return nil, err
	}
	r.Owner = owner
	r.Name = repo
	return &r, nil
}

// CountOpenItems は open な PR / Issue の総数を search API で取得する。
// 注意: Search API は別 rate limit(30/min)。
func (c *Client) CountOpenItems(ctx context.Context, owner, repo string) (openPRs, openIssues int, err error) {
	prPath := fmt.Sprintf("/search/issues?q=repo:%s/%s+is:open+is:pr&per_page=1", owner, repo)
	var prResult searchResult
	if err := c.doJSON(ctx, http.MethodGet, prPath, &prResult); err != nil {
		return 0, 0, fmt.Errorf("count prs: %w", err)
	}

	issuePath := fmt.Sprintf("/search/issues?q=repo:%s/%s+is:open+is:issue&per_page=1", owner, repo)
	var issueResult searchResult
	if err := c.doJSON(ctx, http.MethodGet, issuePath, &issueResult); err != nil {
		return prResult.TotalCount, 0, fmt.Errorf("count issues: %w", err)
	}

	return prResult.TotalCount, issueResult.TotalCount, nil
}

type searchResult struct {
	TotalCount int `json:"total_count"`
}

// LatestRelease は最新 release tag を返す。release が無ければ "" を返す。
func (c *Client) LatestRelease(ctx context.Context, owner, repo string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/releases/latest", owner, repo)
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, &rel); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return rel.TagName, nil
}

// LatestCIStatus は default branch の最新 workflow run の conclusion を返す。
// Actions 未設定 (404) は "" を返す(エラーにしない)。
func (c *Client) LatestCIStatus(ctx context.Context, owner, repo, branch string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/actions/runs?branch=%s&per_page=1&status=completed",
		owner, repo, branch)
	var result struct {
		WorkflowRuns []struct {
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, &result); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	if len(result.WorkflowRuns) == 0 {
		return "", nil
	}
	return result.WorkflowRuns[0].Conclusion, nil
}

// doJSON は HTTP リクエスト発行 + JSON デコード。
func (c *Client) doJSON(ctx context.Context, method, path string, out any) error {
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "yagura-portfolio-orchestrator/0.1.0")
	// S0.1: path から owner を抽出し、owner-specific token を使う(無ければ fallback)
	if token := c.tokens.TokenForPath(path); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	c.updateRateLimit(resp.Header)

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %s", ErrUnauthorized, path)
	case http.StatusForbidden:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return fmt.Errorf("%w: %s", ErrRateLimited, path)
		}
		return fmt.Errorf("%w: %s", ErrForbidden, path)
	default:
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("unexpected status %d: %s: %s", resp.StatusCode, path,
			strings.TrimSpace(string(preview)))
	}

	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode json: %w", err)
	}
	return nil
}

func (c *Client) updateRateLimit(h http.Header) {
	limit := parseIntHeader(h, "X-RateLimit-Limit")
	remaining := parseIntHeader(h, "X-RateLimit-Remaining")
	reset := parseIntHeader(h, "X-RateLimit-Reset")
	if limit == 0 && remaining == 0 && reset == 0 {
		return
	}
	c.mu.Lock()
	c.rateLimit = RateLimit{
		Limit:     limit,
		Remaining: remaining,
		Reset:     time.Unix(int64(reset), 0),
	}
	c.mu.Unlock()
}

func parseIntHeader(h http.Header, key string) int {
	v := h.Get(key)
	if v == "" {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(v, "%d", &n)
	return n
}

// CommitInfo は GitHub API /repos/{owner}/{repo}/commits/{sha} の最小サブセット。
// pin drift 検証 (S1.6) に必要なフィールドのみ。
type CommitInfo struct {
	SHA    string `json:"sha"` // 40-char hex(検証用)
	Commit struct {
		Committer struct {
			Date string `json:"date"` // RFC3339
		} `json:"committer"`
	} `json:"commit"`
}

// GetCommit は指定 SHA の commit メタを取得する。
// SHA が repo に存在しない場合は ErrNotFound を返す(= MISSING 状態)。
// SHA が repo の network 内(fork)に存在するが canonical repo にない場合も
// GitHub API は 404 を返す(impostor commit pattern)。
func (c *Client) GetCommit(ctx context.Context, owner, repo, sha string) (*CommitInfo, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s", owner, repo, sha)
	var info CommitInfo
	if err := c.doJSON(ctx, http.MethodGet, path, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// TagInfo は GitHub API /repos/{owner}/{repo}/git/ref/tags/{tag} の最小サブセット。
type TagInfo struct {
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"` // "commit" or "tag"
	} `json:"object"`
}

// GetTagSHA は指定 tag が現在指している commit SHA を返す。
//
// annotated tag (object.type == "tag") の場合は、tag object を更に dereference
// して underlying commit SHA を取得する。
func (c *Client) GetTagSHA(ctx context.Context, owner, repo, tag string) (string, error) {
	path := fmt.Sprintf("/repos/%s/%s/git/ref/tags/%s", owner, repo, tag)
	var t TagInfo
	if err := c.doJSON(ctx, http.MethodGet, path, &t); err != nil {
		return "", err
	}
	if t.Object.Type == "commit" {
		return t.Object.SHA, nil
	}
	// annotated tag: dereference
	derefPath := fmt.Sprintf("/repos/%s/%s/git/tags/%s", owner, repo, t.Object.SHA)
	var deref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.doJSON(ctx, http.MethodGet, derefPath, &deref); err != nil {
		return "", err
	}
	return deref.Object.SHA, nil
}
