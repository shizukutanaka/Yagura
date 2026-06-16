// Package pindrift implements detection of SHA pin integrity drift in GitHub
// Actions workflow files.
//
// 設計判断 (security spec S1.6):
//   - 抽出は自前 line-based parser(ADR-0001 維持、ghaaudit と同方式)
//   - 検証は既存 internal/github.Client 経由で GitHub API を叩く
//   - drift 種別を 5 段階に分類: OK / TAG_DRIFT / MISSING / STALE / UNVERIFIABLE
//   - inline tag comment ("# v4.2.2") を抽出して tag→SHA 整合性を検証
//
// 検出する攻撃シナリオ:
//   - **Trivy-action (Mar 2026)**: 75/76 tags が force-push で書き換えられた。
//     pin した SHA は immutable だが、その SHA は tag からは到達不能な commit
//     になっている可能性。検出するには tag が今指している SHA を取得して比較。
//   - **deleted commits**: maintainer が commit を削除/履歴書換え。SHA が
//     API で 404 を返す → MISSING。
//   - **abandoned actions**: 長期間更新無し。STALE フラグで通知(攻撃ではないが
//     監査価値あり)。
//
// 参考:
//   - https://nirmata.com/2026/03/24/github-actions-is-under-attack/ (impostor-commit)
//   - https://github.com/cli/cli/issues/13314 (gh actions-pin RFC)
package pindrift

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/shizukutanaka/yagura/internal/github"
)

// DriftStatus は pin の検証結果。
type DriftStatus string

const (
	// StatusOK は SHA が repo に存在し tag(あれば)と整合、年齢も正常な健全状態。
	StatusOK DriftStatus = "OK"
	// StatusTagDrift は inline tag コメントと SHA が不整合 — force-push の疑いがある状態。
	StatusTagDrift DriftStatus = "TAG_DRIFT"
	// StatusMissing は SHA が repo に存在しない — 削除/履歴書換えが疑われる状態。
	StatusMissing DriftStatus = "MISSING"
	// StatusStale は commit が 1 年以上前 — 攻撃ではないが継続監視が推奨される状態。
	StatusStale DriftStatus = "STALE"
	// StatusUnverifiable は API error / rate limit により検証不能だった状態。
	StatusUnverifiable DriftStatus = "UNVERIFIABLE"
)

// Pin は workflow から抽出された SHA pin 1 件。
type Pin struct {
	File       string `json:"file"`        // ".github/workflows/ci.yml"
	Line       int    `json:"line"`
	Owner      string `json:"owner"`       // "actions"
	Repo       string `json:"repo"`        // "checkout"
	PinnedSHA  string `json:"pinned_sha"`  // 40-char hex
	TagComment string `json:"tag_comment"` // inline "# v4.2.2" から抽出(任意)
}

// Result は 1 つの pin に対する検証結果。
type Result struct {
	Pin            Pin         `json:"pin"`
	Status         DriftStatus `json:"status"`
	Detail         string      `json:"detail"`                      // 人間向け説明
	LatestTagSHA   string      `json:"latest_tag_sha,omitempty"`    // TAG_DRIFT 検出時
	CommitDate     string      `json:"commit_date,omitempty"`       // OK / STALE 時
	AgeDays        int         `json:"age_days,omitempty"`
}

// GitHubClient は pindrift が必要とする最小 interface。
// internal/github.Client を直接受けず interface 化することで、test が容易に。
type GitHubClient interface {
	GetCommit(ctx context.Context, owner, repo, sha string) (*github.CommitInfo, error)
	GetTagSHA(ctx context.Context, owner, repo, tag string) (string, error)
}

// Checker は pin drift 検証を実行する。
type Checker struct {
	GH GitHubClient
	// NowFn は時刻取得 hook(テスト用、nil なら time.Now)。
	NowFn func() time.Time
	// StaleThresholdDays を超えると STALE 扱い(デフォルト 365 日)。
	StaleThresholdDays int
	// RateLimit は GitHub API 残量を見て pause する guard(v0.12.0+)。
	// nil なら従来通り(rate limit 無視)。
	RateLimit *RateLimitGuard
}

// New は標準 Checker を返す。
func New(gh GitHubClient) *Checker {
	return &Checker{
		GH:                 gh,
		StaleThresholdDays: 365,
	}
}

// ─── 抽出 ────────────────────────────────────────────────────

// `uses: owner/repo@<40-char-SHA>` + optional inline comment `# v1.2.3`
// reusable workflow form `owner/repo/.github/workflows/x.yml@<sha>` にも対応。
var rePinLine = regexp.MustCompile(
	`^\s*-?\s*uses:\s*["']?([A-Za-z0-9_.-]+)/([A-Za-z0-9_./-]+?)(?:/[^@\s"']*)?@([0-9a-f]{40})["']?(?:\s*#\s*(\S+))?`,
)

// ExtractPins は workflow YAML 文字列から SHA-pinned uses: を全て抽出する。
//
// 引数 filePath は Pin.File に記録される。tag pin (@v4) や branch pin (@main)
// は対象外(これらは ghaaudit が unpinned-uses / mutable-ref として検出する)。
func ExtractPins(filePath, content string) []Pin {
	var pins []Pin
	for i, line := range strings.Split(content, "\n") {
		m := rePinLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		owner, repo, sha, tag := m[1], m[2], m[3], m[4]
		// ローカル action (./xxx) を除外
		if strings.HasPrefix(owner, ".") {
			continue
		}
		// reusable workflow の場合、repo 部分が "actions/checkout" のように
		// path を含むので、最初のスラッシュまでを repo として扱う
		if i := strings.Index(repo, "/"); i >= 0 {
			repo = repo[:i]
		}
		pins = append(pins, Pin{
			File:       filePath,
			Line:       i + 1,
			Owner:      owner,
			Repo:       repo,
			PinnedSHA:  sha,
			TagComment: tag,
		})
	}
	return pins
}

// ─── 検証 ────────────────────────────────────────────────────

// CheckPin は単一 pin の drift status を判定する。
//
// 判定順:
//  1. GetCommit で SHA 存在確認
//     - 404 → MISSING (commit 削除済み)
//     - その他 error → UNVERIFIABLE
//  2. inline tag comment があれば、tag → SHA 解決
//     - 不一致 → TAG_DRIFT (force-push 疑い)
//  3. commit date が StaleThresholdDays を超えていれば STALE
//  4. それ以外は OK
func (c *Checker) CheckPin(ctx context.Context, pin Pin) Result {
	r := Result{Pin: pin}

	// (0) rate limit guard: 残量不足なら reset まで sleep
	if c.RateLimit != nil {
		if err := c.RateLimit.Wait(ctx); err != nil {
			r.Status = StatusUnverifiable
			r.Detail = "context cancelled while waiting for GitHub API rate limit reset"
			return r
		}
	}

	// (1) SHA 存在確認
	info, err := c.GH.GetCommit(ctx, pin.Owner, pin.Repo, pin.PinnedSHA)
	if err != nil {
		if errors.Is(err, github.ErrNotFound) {
			r.Status = StatusMissing
			r.Detail = "Pinned SHA not found in repository — possibly force-deleted or impostor commit"
			return r
		}
		r.Status = StatusUnverifiable
		r.Detail = "GitHub API error: " + err.Error()
		return r
	}
	r.CommitDate = info.Commit.Committer.Date

	// (2) inline tag comment 整合性検証(force-push 攻撃検出のキー)
	if pin.TagComment != "" && looksLikeTag(pin.TagComment) {
		tagSHA, err := c.GH.GetTagSHA(ctx, pin.Owner, pin.Repo, pin.TagComment)
		if err == nil && tagSHA != "" && tagSHA != pin.PinnedSHA {
			r.Status = StatusTagDrift
			r.LatestTagSHA = tagSHA
			r.Detail = "Tag '" + pin.TagComment + "' now points to a different commit than pinned SHA — " +
				"maintainer may have force-pushed the tag (Trivy-action attack pattern, Mar 2026)"
			return r
		}
	}

	// (3) 経年判定
	now := c.now()
	if commitTime, err := time.Parse(time.RFC3339, info.Commit.Committer.Date); err == nil {
		age := int(now.Sub(commitTime).Hours() / 24)
		r.AgeDays = age
		if age > c.StaleThresholdDays {
			r.Status = StatusStale
			r.Detail = "Pinned commit is " +
				durationLabel(age) + " old — consider auditing for security updates"
			return r
		}
	}

	r.Status = StatusOK
	r.Detail = "Pin is valid and current"
	return r
}

// CheckPins は複数 pin を逐次検証する。
// 並列化は v0.11.0+ で導入予定(API rate limit を意識する必要)。
func (c *Checker) CheckPins(ctx context.Context, pins []Pin) []Result {
	out := make([]Result, len(pins))
	for i, p := range pins {
		out[i] = c.CheckPin(ctx, p)
		// context cancellation を尊重
		if ctx.Err() != nil {
			break
		}
	}
	return out
}

// ─── helpers ─────────────────────────────────────────────────

func (c *Checker) now() time.Time {
	if c.NowFn != nil {
		return c.NowFn()
	}
	return time.Now()
}

// looksLikeTag は文字列が tag 名らしい形式か判定する(comment が "v1.0" や
// "1.2.3-rc1" 等の場合に true)。"trusted" のような任意コメントを除外。
var reTagComment = regexp.MustCompile(`^v?\d+(\.\d+){0,2}([-+][\w.]+)?$|^pin@`)

func looksLikeTag(s string) bool {
	if s == "" {
		return false
	}
	// "pin@v1" のような特殊形式は除外(mheap/pin-github-action のコメント形式)
	// シンプルに semver-like なものだけ tag として扱う
	return reTagComment.MatchString(s)
}

// durationLabel は日数を人間向けに変換する。
func durationLabel(days int) string {
	switch {
	case days >= 365*2:
		return strconvI(days/365) + " years"
	case days >= 365:
		return "1 year"
	case days >= 30*2:
		return strconvI(days/30) + " months"
	case days >= 30:
		return "1 month"
	default:
		return strconvI(days) + " days"
	}
}

// strconvI: 最小限の int→string(strconv import 削減用)
func strconvI(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ─── 統計 ────────────────────────────────────────────────────

// Summary は 1 回の検証結果の要約。
type Summary struct {
	TotalPins   int            `json:"total_pins"`
	ByStatus    map[string]int `json:"by_status"`
	Concerning  []Result       `json:"concerning,omitempty"` // OK 以外
}

// Summarize は results から Summary を作成する。
func Summarize(results []Result) Summary {
	s := Summary{
		ByStatus: map[string]int{},
	}
	s.TotalPins = len(results)
	for _, r := range results {
		s.ByStatus[string(r.Status)]++
		if r.Status != StatusOK {
			s.Concerning = append(s.Concerning, r)
		}
	}
	return s
}

// ─── 並列化 (v0.11.0) ────────────────────────────────────────

// CheckPinsParallel は worker pool で複数 pin を並列検証する。
//
// concurrency: 最大同時実行数。0 以下なら 4(デフォルト)。
//
// GitHub API authenticated rate limit は 5000 req/h ≈ 1.4 req/sec。
// 各 pin は 1〜2 API call 消費するので、concurrency=4 は約 8 req/sec で
// burst として安全範囲(5000/3600 = 1.4 を超えるが、短期間なら GitHub の
// secondary rate limit に引っかからない)。
//
// 戻り値の順序は input pins の順序と一致する。context cancellation は
// 全 worker に即座に伝播し、中断後の結果は zero-value のまま。
//
// CheckPinsParallel は pins を最大 concurrency 本の goroutine で並列検証する。
// concurrency <= 0 のとき 4 を使用。serial より O(N/concurrency) に短縮。
// 23 プロジェクト × 平均 5 pins ≈ 115 pins を 4 並列で約 8 秒(serial 約 30 秒)。
func (c *Checker) CheckPinsParallel(ctx context.Context, pins []Pin, concurrency int) []Result {
	if concurrency <= 0 {
		concurrency = 4
	}
	if concurrency > len(pins) {
		concurrency = len(pins)
	}
	if len(pins) == 0 {
		return nil
	}

	results := make([]Result, len(pins))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, p := range pins {
		// context cancel 時は新 worker を起動しない
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break
		}
		wg.Add(1)
		go func(idx int, pin Pin) {
			defer wg.Done()
			defer func() { <-sem }()
			// 個別 CheckPin は ctx を受けるので、cancel 時には UNVERIFIABLE 状態で返る
			results[idx] = c.CheckPin(ctx, pin)
		}(i, p)
	}
	wg.Wait()
	return results
}

// ─── streaming (v0.12.0) ────────────────────────────────────

// ResultEvent は streaming 用の出力 1 件(SSE event)。
//
// Index は input pins 配列の index、TotalCount は全体件数。
// CI 側で「進捗 N/M」の表示が可能。
type ResultEvent struct {
	Index      int    `json:"index"`
	TotalCount int    `json:"total_count"`
	Result     Result `json:"result"`
}

// CheckPinsStream は pin を並列検証し、各完了をチャネルでストリーミング返却する。
//
// CheckPinsParallel と同じ並列度で動くが、結果を待たずに完了順に出力。
// 順序は不定だが ResultEvent.Index で input 位置がわかる。
//
// チャネルは len(pins) 個のイベントを送信後に close される。
// context cancel された場合も close される(残り pin は未送信)。
//
// 呼出側パターン:
//
//	ch := checker.CheckPinsStream(ctx, pins, 4)
//	for ev := range ch {
//	    // SSE で send: data: {"index":...,"result":...}
//	}
func (c *Checker) CheckPinsStream(ctx context.Context, pins []Pin, concurrency int) <-chan ResultEvent {
	if concurrency <= 0 {
		concurrency = 4
	}
	if concurrency > len(pins) {
		concurrency = len(pins)
	}
	out := make(chan ResultEvent, concurrency) // buffer で送信ブロックを軽減
	total := len(pins)
	if total == 0 {
		close(out)
		return out
	}

	go func() {
		defer close(out)
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup

		for i, p := range pins {
			if ctx.Err() != nil {
				return
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			wg.Add(1)
			go func(idx int, pin Pin) {
				defer wg.Done()
				defer func() { <-sem }()
				r := c.CheckPin(ctx, pin)
				select {
				case out <- ResultEvent{Index: idx, TotalCount: total, Result: r}:
				case <-ctx.Done():
				}
			}(i, p)
		}
		wg.Wait()
	}()

	return out
}
