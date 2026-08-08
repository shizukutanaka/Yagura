// Package injectscan は untrusted content(web ページ / issue 本文 / tool 出力 /
// ファイル等、エージェントが実行時に取り込むテキスト)に潜む間接プロンプト
// インジェクションのシグナルを決定論的に検出する。
//
// 多言語調査(2026)の合意:
//   - プロンプトインジェクションは OWASP の AI 脅威 #1。モデル層では解決不能で、
//     現実解は「LLM の外の決定論的 policy による defense-in-depth」。
//   - 多層防御: 入力(Pre)→ 実行 → 出力(Post-filter)→ 行動分析。検証ゲートは
//     「LLM と独立したサーバ層」に置く(ko)。Yagura はまさにその層。
//   - 高シグナルなヒューリスティック: override 語彙(「ignore previous instructions」
//     等、多言語)、外部ドメインへの送信、.env/.ssh/credential の exfil 指示、
//     zero-width/bidi の隠し文字、encoding トリック、instruction/data 混同マーカー。
//
// mcp_audit が MCP の tool 定義の poisoning を見るのに対し、injectscan はエージェントが
// fetch/read した content 自体を見る(content provenance)。Yagura は LLM を呼ばず、
// tight な高シグナル正規表現で検出する(正当な文章を誤検出しない)。出力は audit 可能。
package injectscan

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Severity は検出された injection の深刻度。
type Severity string

// Category は injection 手口の分類。
type Category string

const (
	// SevCritical は最重大(直接の実行/漏洩につながる)。
	SevCritical Severity = "critical"
	// SevHigh は高(明確な操作意図)。
	SevHigh Severity = "high"
	// SevMedium は中(疑わしいが文脈依存)。
	SevMedium Severity = "medium"
	// SevLow は低(弱いシグナル)。
	SevLow Severity = "low"
)

const (
	// CatOverride は既存指示の上書き(instruction override)。
	CatOverride Category = "instruction_override"
	// CatExfiltration は機密/データの持ち出し誘導。
	CatExfiltration Category = "exfiltration"
	// CatToolManip は tool 呼出の不正操作。
	CatToolManip Category = "tool_manipulation"
	// CatHiddenText は不可視/隠しテキストによる注入。
	CatHiddenText Category = "hidden_text"
	// CatEncoding はエンコーディングによる検出回避。
	CatEncoding Category = "encoding"
	// CatDataConfuse はデータ/指示の境界混同。
	CatDataConfuse Category = "data_confusion"
)

// Finding は 1 件の検出。
type Finding struct {
	Category Category `json:"category"`
	Severity Severity `json:"severity"`
	Line     int      `json:"line"`
	Rule     string   `json:"rule"`
	Snippet  string   `json:"snippet"` // redact 済み(制御文字は · 置換、長さ切り詰め)
}

// Result はスキャン結果。Score は 0-100(高いほど clean)。
type Result struct {
	Findings    []Finding      `json:"findings"`
	BySeverity  map[string]int `json:"by_severity"`
	ByCategory  map[string]int `json:"by_category"`
	Score       int            `json:"score"`
	HasCritical bool           `json:"has_critical"`
	Summary     string         `json:"summary"`
}

type pattern struct {
	cat  Category
	sev  Severity
	rule string
	re   *regexp.Regexp
}

// 高シグナルなパターン群(tight = 正当文章の誤検出を避ける)。多言語(en/ja/zh/ko)。
var patterns = []pattern{
	// instruction override
	{CatOverride, SevHigh, "ignore-previous", regexp.MustCompile(`(?i)\b(ignore|disregard|forget)\b[^.\n]{0,30}\b(previous|above|prior|earlier|all|the)\b[^.\n]{0,30}\b(instruction|instructions|prompt|prompts|message|messages|rule|rules|context)\b`)},
	{CatOverride, SevHigh, "you-are-now", regexp.MustCompile(`(?i)\byou are now\b|\bfrom now on,? you\b`)},
	{CatOverride, SevHigh, "new-instructions", regexp.MustCompile(`(?i)\bnew\b[^.\n]{0,20}\b(instructions?|rules?|system prompt)\b\s*:`)},
	{CatOverride, SevMedium, "system-prompt-ref", regexp.MustCompile(`(?i)\b(system prompt|developer message)\b`)},
	{CatOverride, SevHigh, "act-as-privileged", regexp.MustCompile(`(?i)\bact as\b[^.\n]{0,24}\b(system|admin|administrator|root|developer mode|DAN|jailbreak)\b`)},
	{CatOverride, SevHigh, "override-ja", regexp.MustCompile(`(これまで|以前|上記|前)の(指示|命令|指令)を(無視|忘れ)|今からあなたは|システムプロンプト`)},
	{CatOverride, SevHigh, "override-zh", regexp.MustCompile(`忽略(之前|上述|前面|以上)[^。\n]{0,6}(指令|指示|提示|规则)|你现在是|系统提示词`)},
	{CatOverride, SevHigh, "override-ko", regexp.MustCompile(`이전[^.\n]{0,6}지시[^.\n]{0,6}무시|위[^.\n]{0,4}지시를?\s*무시|시스템\s*프롬프트|이제부터\s*너는`)},

	// exfiltration
	{CatExfiltration, SevCritical, "read-send-secret", regexp.MustCompile(`(?i)\b(read|cat|open|send|upload|post|exfiltrate|leak|email)\b[^.\n]{0,40}(\.env|\.ssh|id_rsa|/etc/passwd|credential|secret|api[ _-]?key|password|access[ _-]?token|private key)`)},
	// copy-secret is SevMedium because `copy .env` appears in setup documentation ("cp .env.example .env")
	// and alone cannot exfiltrate data — the companion send-to-url rule catches the full attack chain.
	{CatExfiltration, SevMedium, "copy-secret", regexp.MustCompile(`(?i)\bcopy\b[^.\n]{0,40}(\.env|\.ssh|id_rsa|/etc/passwd|credential|secret|api[ _-]?key|password|access[ _-]?token|private key)`)},
	{CatExfiltration, SevCritical, "send-to-url", regexp.MustCompile(`(?i)\b(send|post|upload|forward|exfiltrate)\b[^.\n]{0,40}\bto\b[^.\n]{0,20}https?://`)},
	{CatExfiltration, SevHigh, "curl-exfil", regexp.MustCompile(`(?i)\bcurl\b[^\n]{0,80}https?://[^\s]*\?[^\s]*=`)},

	// tool / agent manipulation
	{CatToolManip, SevMedium, "invoke-tool", regexp.MustCompile(`(?i)\b(call|invoke|run|execute|trigger)\b[^.\n]{0,20}\b(the\s+)?\w+\s+(tool|function|command|mcp\b)`)},
	{CatToolManip, SevMedium, "use-your-tools", regexp.MustCompile(`(?i)\buse your\b[^.\n]{0,20}\b(tools?|capabilities|functions?|permissions?)\b`)},

	// instruction/data 混同マーカーが untrusted content に埋め込まれている
	{CatDataConfuse, SevMedium, "role-marker", regexp.MustCompile(`(?i)<\s*/?\s*system\s*>|\[/?INST\]|<\|im_start\|>\s*system|(?m)^\s*#{2,3}\s*system\s*:`)},
}

// hidden text: zero-width / bidi 制御文字。
var hiddenRe = regexp.MustCompile(`[\x{200b}-\x{200f}\x{202a}-\x{202e}\x{2060}-\x{2064}\x{2066}-\x{2069}\x{feff}]`)

// base64 候補(長い run)。decode して suspicious なら encoding finding。
var b64Re = regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`)
var b64SuspectRe = regexp.MustCompile(`(?i)ignore|system prompt|api[ _-]?key|password|\.env|\.ssh|secret|credential|exfiltrat`)

var sevWeight = map[Severity]int{SevCritical: 40, SevHigh: 20, SevMedium: 10, SevLow: 3}

// Scan は content を 1 回走査し、検出を返す。決定論的(line→category→rule 順に整列)。
func Scan(content string) Result {
	var fs []Finding
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		ln := i + 1
		for _, p := range patterns {
			if loc := p.re.FindStringIndex(line); loc != nil {
				fs = append(fs, Finding{
					Category: p.cat, Severity: p.sev, Line: ln, Rule: p.rule,
					Snippet: redact(line[loc[0]:min(loc[1], loc[0]+120)]),
				})
			}
		}
		if hiddenRe.MatchString(line) {
			fs = append(fs, Finding{
				Category: CatHiddenText, Severity: SevHigh, Line: ln, Rule: "hidden-control-char",
				Snippet: redact(line),
			})
		}
		for _, b := range b64Re.FindAllString(line, -1) {
			if dec, err := base64.StdEncoding.DecodeString(strings.TrimRight(b, "=") + pad(b)); err == nil {
				// 復号ペイロードを suspect キーワード *および* 本体パターン集合の
				// 両方で再走査する。後者が無いと、既知パターンに合致する injection を
				// base64 で包むだけで検出を回避できた(encoding evasion / fail-open)。
				if b64SuspectRe.Match(dec) || matchesAnyPattern(string(dec)) {
					fs = append(fs, Finding{
						Category: CatEncoding, Severity: SevMedium, Line: ln, Rule: "base64-hidden-payload",
						Snippet: redact(string(dec)),
					})
				}
			}
		}
	}

	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		if fs[i].Category != fs[j].Category {
			return fs[i].Category < fs[j].Category
		}
		return fs[i].Rule < fs[j].Rule
	})

	res := Result{Findings: fs, BySeverity: map[string]int{}, ByCategory: map[string]int{}, Score: 100}
	for _, f := range fs {
		res.BySeverity[string(f.Severity)]++
		res.ByCategory[string(f.Category)]++
		res.Score -= sevWeight[f.Severity]
		if f.Severity == SevCritical {
			res.HasCritical = true
		}
	}
	if res.Score < 0 {
		res.Score = 0
	}
	res.Summary = summarize(res)
	return res
}

// matchesAnyPattern は s が既知の injection パターンのいずれかに合致するか返す。
// base64 復号ペイロードの再走査に使う(plaintext と同じ検出力を encoded にも適用)。
func matchesAnyPattern(s string) bool {
	for _, p := range patterns {
		if p.re.MatchString(s) {
			return true
		}
	}
	return false
}

func summarize(r Result) string {
	if len(r.Findings) == 0 {
		return "no prompt-injection signals detected"
	}
	return fmt.Sprintf("%d signal(s), score %d/100 (%d critical / %d high / %d medium / %d low)",
		len(r.Findings), r.Score, r.BySeverity["critical"], r.BySeverity["high"],
		r.BySeverity["medium"], r.BySeverity["low"])
}

// redact は制御文字を · に置換し、長すぎる snippet を切り詰める(出力を安全に)。
func redact(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= 120 {
			b.WriteString("…")
			break
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || hiddenRe.MatchString(string(r)) {
			b.WriteRune('·')
		} else {
			b.WriteRune(r)
		}
		n++
	}
	return strings.TrimSpace(b.String())
}

func pad(s string) string {
	if m := len(strings.TrimRight(s, "=")) % 4; m != 0 {
		return strings.Repeat("=", 4-m)
	}
	return ""
}
