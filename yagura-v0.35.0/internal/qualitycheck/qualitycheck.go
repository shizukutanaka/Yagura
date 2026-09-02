// Package qualitycheck は「逸脱を物理的に潰す」品質ゲートを実装する。
//
// 動機 (v0.19.0, cortex/aircloset 2026/05 翻訳):
//
//	cortex の harness の中心は「コードの正しさを 100% 担保する」ことで、
//	その手段が「`as any` / TODO / `eslint-disable` を 0 件強制」。これにより
//	AI が間違ったコードを書いても merge されない設計が成立している。
//
//	yagura は portfolio 23+ プロジェクトを対象に同等の検査を提供する。
//	各プロジェクトの CI 内で `yagura_quality_check` を呼び出して 0 件強制すれば、
//	m の sovereign computing stack 全体が同じ品質ゲートで保護される。
//
// 設計判断:
//   - ゼロ依存 (ADR-0001, regex のみ stdlib)
//   - 多言語対応: TypeScript / JavaScript / Go / Python / Rust
//   - 3 段階 Severity: prohibited (0 件強制) / warning / info
//   - 偽陽性を抑える: 文字列リテラル / コメントの中だけ検出する rule もあるが、
//     v0.19 は line-based regex のみ(より精度の高い AST ベースは v0.20+ 候補)
//   - secretscan と類似 architecture(統一感重視)
package qualitycheck

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Severity は finding の重大度。
type Severity string

const (
	// SevProhibited: 0 件強制。CI で fail させるべき。
	SevProhibited Severity = "prohibited"
	// SevWarning: 注意が必要。レビューで確認するか、reason コメントを要求。
	SevWarning Severity = "warning"
	// SevInfo: 参考情報。
	SevInfo Severity = "info"
)

// Rule は 1 つの検査ルール。
type Rule struct {
	ID          string   `json:"id"`
	Severity    Severity `json:"severity"`
	Languages   []string `json:"languages"` // "ts" "js" "go" "py" "rust" or "any"
	Description string   `json:"description"`
	Suggestion  string   `json:"suggestion,omitempty"`

	// pattern は compiled regex。Build 時に regex.MustCompile される。
	pattern *regexp.Regexp
}

// Finding は検出された 1 件。
type Finding struct {
	RuleID      string   `json:"rule_id"`
	Severity    Severity `json:"severity"`
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Column      int      `json:"column"`
	Excerpt     string   `json:"excerpt"` // 該当 line の trimmed snippet (最大 120 char)
	Description string   `json:"description"`
	Suggestion  string   `json:"suggestion,omitempty"`
}

// Result はファイル全体スキャンの集計。
type Result struct {
	FilesScanned int              `json:"files_scanned"`
	TotalLines   int              `json:"total_lines"`
	Findings     []Finding        `json:"findings"`
	BySeverity   map[Severity]int `json:"by_severity"`
	ByRule       map[string]int   `json:"by_rule"`
	ByFile       map[string]int   `json:"by_file,omitempty"`
	// v0.23.0: cache 統計(ScanFilesCached 使用時のみ非ゼロ)
	CacheHits   int `json:"cache_hits,omitempty"`
	CacheMisses int `json:"cache_misses,omitempty"`
}

// HasProhibited は prohibited 件が 1 件以上か。
//
// CI gate での fail/pass 判定に使う。
func (r *Result) HasProhibited() bool {
	return r.BySeverity[SevProhibited] > 0
}

// DefaultRules は yagura が標準で提供するルールセット。
//
// cortex の文脈に合わせて選定:
//   - TS/JS: as any / : any / @ts-ignore / @ts-nocheck / eslint-disable / oxlint-disable
//   - Go: 過剰な interface{} は info 留め(正当な使用が多い)
//   - 全言語: TODO / FIXME / HACK / XXX
//
// 偽陽性を避けるため、自動生成ファイル等の除外は呼出側の責任とする
// (path フィルタは ScanFiles で受け付ける)。
// compiledDefaultRules は正規表現を一度だけコンパイルして再利用する
// (DefaultRules() 呼び出し毎の 14 個の regexp.MustCompile を回避)。
var compiledDefaultRules = sync.OnceValue(buildDefaultRules)

// DefaultRules の返り値は呼び出し毎に独立した slice。
func DefaultRules() []Rule {
	rs := compiledDefaultRules()
	out := make([]Rule, len(rs))
	copy(out, rs)
	return out
}

func buildDefaultRules() []Rule {
	rules := []Rule{
		// ─── TypeScript / JavaScript ────────────────────────────
		{
			ID:          "ts-as-any",
			Severity:    SevProhibited,
			Languages:   []string{"ts", "tsx", "js", "jsx"},
			Description: "`as any` cast escapes the type system",
			Suggestion:  "Define a proper type or use `unknown` then narrow",
			pattern:     regexp.MustCompile(`\bas\s+any\b`),
		},
		{
			ID:          "ts-any-type",
			Severity:    SevProhibited,
			Languages:   []string{"ts", "tsx"},
			Description: "`: any` type annotation defeats type checking",
			Suggestion:  "Use a specific type, `unknown`, or generic parameter",
			pattern:     regexp.MustCompile(`:\s*any\b(?:[^a-zA-Z_])`),
		},
		{
			ID:          "ts-ignore",
			Severity:    SevProhibited,
			Languages:   []string{"ts", "tsx", "js", "jsx"},
			Description: "`@ts-ignore` silences type errors silently",
			Suggestion:  "Use `@ts-expect-error` with a reason, or fix the actual type",
			pattern:     regexp.MustCompile(`@ts-ignore\b`),
		},
		{
			ID:          "ts-nocheck",
			Severity:    SevProhibited,
			Languages:   []string{"ts", "tsx", "js", "jsx"},
			Description: "`@ts-nocheck` disables type checking for the entire file",
			pattern:     regexp.MustCompile(`@ts-nocheck\b`),
		},
		{
			ID:          "eslint-disable",
			Severity:    SevProhibited,
			Languages:   []string{"ts", "tsx", "js", "jsx"},
			Description: "`eslint-disable` bypasses lint rules",
			Suggestion:  "Fix the underlying issue or update the lint rule",
			pattern:     regexp.MustCompile(`eslint-disable(?:-next-line|-line)?\b`),
		},
		{
			ID:          "oxlint-disable",
			Severity:    SevProhibited,
			Languages:   []string{"ts", "tsx", "js", "jsx"},
			Description: "`oxlint-disable` bypasses lint rules",
			pattern:     regexp.MustCompile(`oxlint-disable(?:-next-line|-line)?\b`),
		},
		// ─── Go ─────────────────────────────────────────────────
		{
			ID:          "go-nolint",
			Severity:    SevWarning,
			Languages:   []string{"go"},
			Description: "`//nolint:` should always include a reason after a colon",
			Suggestion:  "Use `//nolint:rulename // reason explaining why`",
			pattern:     regexp.MustCompile(`//\s*nolint\b`),
		},
		{
			ID:          "go-panic-prod",
			Severity:    SevWarning,
			Languages:   []string{"go"},
			Description: "`panic()` in production code should be exceptional",
			Suggestion:  "Return an error instead; reserve panic for truly unrecoverable states",
			pattern:     regexp.MustCompile(`\bpanic\s*\(`),
		},
		// ─── Python ─────────────────────────────────────────────
		{
			ID:          "py-type-ignore",
			Severity:    SevWarning,
			Languages:   []string{"py"},
			Description: "`# type: ignore` should always include the specific error code",
			Suggestion:  "Use `# type: ignore[error-code]` with a reason",
			pattern:     regexp.MustCompile(`#\s*type:\s*ignore\b`),
		},
		{
			ID:          "py-noqa-naked",
			Severity:    SevInfo,
			Languages:   []string{"py"},
			Description: "`# noqa` without a code disables all rules on that line",
			Suggestion:  "Use `# noqa: E501` (specific code) instead",
			pattern:     regexp.MustCompile(`#\s*noqa\s*(?:[^:]|$)`),
		},
		// ─── Universal (any language) ───────────────────────────
		{
			ID:          "todo",
			Severity:    SevWarning,
			Languages:   []string{"any"},
			Description: "`TODO` marker indicates unfinished work",
			Suggestion:  "Replace with `// TODO(issue-#123): description` or remove",
			pattern:     regexp.MustCompile(`\bTODO\b`),
		},
		{
			ID:          "fixme",
			Severity:    SevWarning,
			Languages:   []string{"any"},
			Description: "`FIXME` marker indicates a known bug",
			Suggestion:  "File an issue and link it, or fix it now",
			pattern:     regexp.MustCompile(`\bFIXME\b`),
		},
		{
			ID:          "hack",
			Severity:    SevWarning,
			Languages:   []string{"any"},
			Description: "`HACK` marker indicates a workaround",
			pattern:     regexp.MustCompile(`\bHACK\b`),
		},
		{
			ID:          "xxx",
			Severity:    SevWarning,
			Languages:   []string{"any"},
			Description: "`XXX` marker indicates something requires attention",
			pattern:     regexp.MustCompile(`\bXXX\b`),
		},
	}
	return rules
}

// RuleSpec はユーザ定義 lint ルールの外部仕様(JSON 可)。Rule.pattern は unexported なので、
// 外部からは pattern を文字列で渡し CompileRules でコンパイルする。プロジェクト固有の
// 「逸脱を物理的に潰す」ルール(例: console.log 禁止)を再コンパイルなしで足せる。
type RuleSpec struct {
	ID          string   `json:"id"`
	Pattern     string   `json:"pattern"`
	Severity    Severity `json:"severity"`
	Languages   []string `json:"languages,omitempty"`
	Description string   `json:"description,omitempty"`
	Suggestion  string   `json:"suggestion,omitempty"`
}

const maxPatternLen = 1000

// CompileRules は RuleSpec 群を Rule へコンパイルする。決定論的(入力順を保つ)。
//   - id / pattern は必須。空ならエラー。
//   - severity 未指定は warning。不正値はエラー。
//   - languages 未指定は ["any"]。
//   - pattern は Go の regexp(RE2、線形時間=ReDoS なし)で Compile。不正 regex はエラー。
//   - pattern 長は maxPatternLen まで(暴走防止)。
func CompileRules(specs []RuleSpec) ([]Rule, error) {
	out := make([]Rule, 0, len(specs))
	for i, s := range specs {
		if strings.TrimSpace(s.ID) == "" {
			return nil, fmt.Errorf("custom rule #%d: id is required", i)
		}
		if strings.TrimSpace(s.Pattern) == "" {
			return nil, fmt.Errorf("custom rule %q: pattern is required", s.ID)
		}
		if len(s.Pattern) > maxPatternLen {
			return nil, fmt.Errorf("custom rule %q: pattern too long (>%d)", s.ID, maxPatternLen)
		}
		sev := s.Severity
		if sev == "" {
			sev = SevWarning
		}
		if sev != SevProhibited && sev != SevWarning && sev != SevInfo {
			return nil, fmt.Errorf("custom rule %q: invalid severity %q (prohibited/warning/info)", s.ID, sev)
		}
		re, err := regexp.Compile(s.Pattern)
		if err != nil {
			return nil, fmt.Errorf("custom rule %q: invalid pattern: %w", s.ID, err)
		}
		langs := s.Languages
		if len(langs) == 0 {
			langs = []string{"any"}
		}
		out = append(out, Rule{
			ID:          s.ID,
			Severity:    sev,
			Languages:   langs,
			Description: s.Description,
			Suggestion:  s.Suggestion,
			pattern:     re,
		})
	}
	return out, nil
}

// ScanText は 1 つのコンテンツに対してルールを適用する。
//
// 引数:
//
//	path     — 結果に含める file 名(空でも可、表示用)
//	content  — スキャン対象テキスト
//	language — 言語ヒント("ts" "go" "py" "any" 等)。"" / "any" なら言語非依存ルールのみ
//	rules    — 適用するルール
//
// 戻り値: Finding 配列(line/column 順に sort されない、呼出側で必要なら sort)
func ScanText(path, content, language string, rules []Rule) []Finding {
	var findings []Finding
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024) // 1MB max line for long minified files
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		for _, r := range rules {
			if !ruleApplies(r, language) {
				continue
			}
			locs := r.pattern.FindAllStringIndex(line, -1)
			for _, loc := range locs {
				findings = append(findings, Finding{
					RuleID:      r.ID,
					Severity:    r.Severity,
					File:        path,
					Line:        lineNum,
					Column:      loc[0] + 1, // 1-indexed
					Excerpt:     truncateExcerpt(line, 120),
					Description: r.Description,
					Suggestion:  r.Suggestion,
				})
			}
		}
	}
	return findings
}

// ScanFiles は複数ファイル一括スキャン。各 path から language を推定する。
//
// 戻り値: 集計 Result。Findings は file/line 順に sort 済み。
// ScanFiles は複数ファイル一括スキャン。各 path から language を推定する。
//
// 戻り値: 集計 Result。Findings は file/line 順に sort 済み。
//
// 既存 caller の互換性を保つ shim。新規 caller は ScanFilesCached を使うと
// content-hash based cache で重複読込を回避できる。
func ScanFiles(files map[string]string, rules []Rule) Result {
	return scanFilesImpl(files, rules, nil)
}

// ScanFilesCached は cache 統合版 ScanFiles (v0.23.0)。
//
// 各ファイル content の SHA-256 を key として cache を参照し、同じ content の
// 再 scan を skip する。AI agent が CI で同じファイルを再投入する典型ケースで
// CPU と response token を削減。
//
// cache が nil なら通常 scan(後方互換)。
//
// Result に CacheHits / CacheMisses が含まれる。
func ScanFilesCached(files map[string]string, rules []Rule, cache CacheLike) Result {
	return scanFilesImpl(files, rules, cache)
}

// CacheLike は dedupe.Cache を依存しないための minimal interface。
//
// 循環 import を避けるため interface 経由で受ける。
type CacheLike interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte)
}

func scanFilesImpl(files map[string]string, rules []Rule, cache CacheLike) Result {
	res := Result{
		BySeverity: map[Severity]int{},
		ByRule:     map[string]int{},
		ByFile:     map[string]int{},
	}
	for path, content := range files {
		res.FilesScanned++
		res.TotalLines += countLines(content)
		lang := detectLanguage(path)

		var fs []Finding
		if cache != nil {
			// content hash + path + lang をキーに(同じ content が違うファイル名で
			// あれば別 finding になるので path も含める)
			cacheKey := cacheKeyFor(path, content, lang)
			if cached, ok := cache.Get(cacheKey); ok {
				if parsed, err := unmarshalFindings(cached); err == nil {
					fs = parsed
					res.CacheHits++
				}
			}
			if fs == nil {
				fs = ScanText(path, content, lang, rules)
				res.CacheMisses++
				if data, err := marshalFindings(fs); err == nil {
					cache.Set(cacheKey, data)
				}
			}
		} else {
			fs = ScanText(path, content, lang, rules)
		}
		for _, f := range fs {
			res.Findings = append(res.Findings, f)
			res.BySeverity[f.Severity]++
			res.ByRule[f.RuleID]++
			res.ByFile[f.File]++
		}
	}
	// deterministic 順序
	sortFindings(res.Findings)
	return res
}

// sortFindings は findings を決定論的な全順序で並べる。
//
// 入力は files map を range して作られる(= 走査順が非決定的)。sort.Slice は
// unstable なので、比較キーが全順序でないと「等しい」要素の相対順が走査順に依存し
// 出力が run ごとにブレる。同一 (File,Line,Column) に複数ルールがヒットしても
// RuleID で tie-break して順序を一意に確定させる。
func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		return a.RuleID < b.RuleID
	})
}

// cacheKeyFor は (path, content, language) から決定論的 cache key を生成する。
//
// content の SHA-256 を含めることで、同 path でも内容が変われば cache miss する。
// rules セットも本来は key に含めるべきだが、現状 DefaultRules() のみ使用するので省略。
// 将来 custom rules を受ける際は ruleHash を追加する。
func cacheKeyFor(path, content, language string) string {
	contentHash := sha256Hex([]byte(content))
	return "qc:" + path + ":" + language + ":" + contentHash
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func marshalFindings(fs []Finding) ([]byte, error) {
	return json.Marshal(fs)
}

func unmarshalFindings(data []byte) ([]Finding, error) {
	var fs []Finding
	err := json.Unmarshal(data, &fs)
	return fs, err
}

// ruleApplies は rule が language に対して有効か。
//
// "any" を含むルールは全言語適用。
// path 拡張子から language が確定できない場合は universal ルールのみ。
func ruleApplies(r Rule, language string) bool {
	for _, l := range r.Languages {
		if l == "any" {
			return true
		}
		if l == language {
			return true
		}
	}
	return false
}

// detectLanguage は path 拡張子から言語を推定する。
func detectLanguage(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".ts") || strings.HasSuffix(lower, ".mts") || strings.HasSuffix(lower, ".cts"):
		return "ts"
	case strings.HasSuffix(lower, ".tsx"):
		return "tsx"
	case strings.HasSuffix(lower, ".js") || strings.HasSuffix(lower, ".mjs") || strings.HasSuffix(lower, ".cjs"):
		return "js"
	case strings.HasSuffix(lower, ".jsx"):
		return "jsx"
	case strings.HasSuffix(lower, ".go"):
		return "go"
	case strings.HasSuffix(lower, ".py"):
		return "py"
	case strings.HasSuffix(lower, ".rs"):
		return "rust"
	default:
		return ""
	}
}

// truncateExcerpt は line を最大 maxLen char に切り詰める。
// 前後の空白を trim してから処理する。
func truncateExcerpt(line string, maxLen int) string {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) <= maxLen {
		return trimmed
	}
	// maxLen < 3 has no room for the "..." suffix; truncate plainly instead of
	// computing trimmed[:maxLen-3] (a negative index that would panic).
	if maxLen <= 3 {
		if maxLen <= 0 {
			return ""
		}
		return trimmed[:maxLen]
	}
	return trimmed[:maxLen-3] + "..."
}

// countLines は content の行数を返す。
func countLines(content string) int {
	if content == "" {
		return 0
	}
	n := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		n++
	}
	return n
}

// Summary は人間可読な要約文字列を返す。
func (r *Result) Summary() string {
	if len(r.Findings) == 0 {
		return fmt.Sprintf("clean: %d files / %d lines scanned, 0 findings", r.FilesScanned, r.TotalLines)
	}
	return fmt.Sprintf("scan: %d files / %d lines / %d findings (prohibited=%d, warning=%d, info=%d)",
		r.FilesScanned, r.TotalLines, len(r.Findings),
		r.BySeverity[SevProhibited], r.BySeverity[SevWarning], r.BySeverity[SevInfo])
}
