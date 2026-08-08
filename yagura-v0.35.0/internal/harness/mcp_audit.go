// mcp_audit.go: MCP サーバー設定(.mcp.json)/ ツール定義の tool-poisoning & 設定リスク監査。
//
// 着想: MCP は agent↔tool の de-facto 標準だが、ツール記述(description)が「agent が読む
// 信頼境界」になっており、poisoned description で agent を誘導する攻撃が現実化している
// (arXiv 2508.14925 MCPTox は実サーバで攻撃成功率>60%、arXiv 2603.22489 MCP threat
// modeling、arXiv 2603.18063 MCP-38 threat taxonomy、CSA unicode-instruction-injection
// research 2026)。Yagura は MCP server かつ .claude/ 監査ツールなので、MCP の設定・
// ツール記述を構造ベースで lint するのは security 中核。secretscan(credential)を補完する。
//
// content から自動判定:
//   - mcpServers を持つ → server 設定監査(pipe-to-shell / 未 pin npx / 平文 http /
//     env・headers の secret 直書き)
//   - tools[] を持つ → tool description の poisoning 監査(v0.110.0 で 2026 taxonomy に
//     整合): injection 上書き文 / data-exfil 指示 / zero-width 等の隠し文字 /
//     HTML・markdown コメント smuggling / base64 隠しペイロード /
//     cross-tool shadowing(同名 tool 上書き)
//
// AuditSkill 等と同じ shape(Score + Issues + Suggestions)。stdlib のみ(ADR-0001)。
package harness

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// MCPAuditResult は .mcp.json / tools 定義の評価結果。
type MCPAuditResult struct {
	Score       int      `json:"score"`
	Kind        string   `json:"kind"` // "mcp-config" | "mcp-tools"
	ValidJSON   bool     `json:"valid_json"`
	ServerCount int      `json:"server_count,omitempty"`
	ToolCount   int      `json:"tool_count,omitempty"`
	Issues      []string `json:"issues,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

var (
	// server command/args: pipe-to-shell や curl|sh 系(remote code exec)。
	reShellFetch = regexp.MustCompile(`(?i)\b(curl|wget)\b.*\|\s*(sh|bash|zsh|python)`)
	// npx/uvx/pip 等で version 未 pin(@x.y.z や ==ver が無い)= supply chain。
	reUnpinnedRunner = regexp.MustCompile(`(?i)^(npx|uvx|pnpm dlx|bunx)$`)
	// tool description の injection/上書き文(poisoning)。tight に高シグナルのみ。
	reInjection = regexp.MustCompile(`(?i)(ignore\s+(all\s+|the\s+)?(previous|prior|above)\s+instruction|disregard\s+(previous|the|all)|do\s+not\s+(tell|inform|mention|reveal|notify)\s+(the\s+)?user|before\s+(using|calling)\s+(any\s+)?other\s+tool|you\s+must\s+(always|first|secretly)|instead\s+of\s+(the|your|calling)|<\s*(system|important|secret|admin)\s*>|system\s+prompt)`)
	// data-exfil 指示。
	reExfil = regexp.MustCompile(`(?i)(read|cat|send|exfiltrat|upload|post|leak|transmit)\b.{0,40}(\.env|\.ssh|id_rsa|credential|api[_\- ]?key|secret|password|token|\.aws)`)
	// zero-width / bidi control / BOM(隠し文字による poisoning 隠蔽)。
	reHiddenChar = regexp.MustCompile(`[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}\x{FEFF}]`)
	// env/header の secret 直書き(${VAR} placeholder は除外)。
	reSecretVal = regexp.MustCompile(`(?i)^(sk-|ghp_|gho_|xai-|aiza|pk-|eyj)`)
	// HTML/markdown コメントに隠した指示(rendered markdown では人間に不可視、
	// モデルはトークン化する。CSA / policylayer 2026 research)。
	reHTMLComment = regexp.MustCompile(`<!--[\s\S]*?-->`)
	// description 内の長い base64 run(復号して injection/exfil なら flag)。
	reBase64Run = regexp.MustCompile(`[A-Za-z0-9+/]{24,}={0,2}`)
)

// AuditMCPConfig は content を .mcp.json / tools 定義と判定して評価する。
func AuditMCPConfig(content string) MCPAuditResult {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &top); err != nil {
		return MCPAuditResult{Score: 0, ValidJSON: false, Kind: "mcp-config",
			Issues:      []string{"invalid JSON — cannot parse MCP config"},
			Suggestions: []string{".mcp.json / tools list must be strict JSON."}}
	}
	if _, ok := top["tools"]; ok {
		return auditMCPTools(top)
	}
	return auditMCPServers(top)
}

func auditMCPServers(top map[string]json.RawMessage) MCPAuditResult {
	r := MCPAuditResult{Score: 100, Kind: "mcp-config", ValidJSON: true}
	var servers map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		URL     string            `json:"url"`
		Type    string            `json:"type"`
		Env     map[string]string `json:"env"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(top["mcpServers"], &servers); err != nil {
		// 値はあるが object でない(string/array 等)→ 壊れた config。
		// 「見つからない」ではなく malformed として報告する。
		r.Score -= 10
		r.Issues = append(r.Issues, "mcpServers is malformed — must be an object of {name: server} entries")
		return r
	}
	if len(servers) == 0 {
		r.Score -= 10
		r.Issues = append(r.Issues, "no mcpServers found (not an MCP server config?)")
		return r
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	r.ServerCount = len(names)
	for _, n := range names {
		s := servers[n]
		cmdline := s.Command + " " + strings.Join(s.Args, " ")
		if reShellFetch.MatchString(cmdline) {
			r.Score -= 25
			r.Issues = append(r.Issues, fmt.Sprintf("server %q runs a fetch-piped-to-shell command — remote code execution risk", n))
		}
		if reUnpinnedRunner.MatchString(strings.TrimSpace(s.Command)) && !hasPinnedPackage(s.Args) {
			r.Score -= 10
			r.Issues = append(r.Issues, fmt.Sprintf("server %q launches via %s without a pinned package version — supply-chain risk", n, s.Command))
			r.Suggestions = append(r.Suggestions, "pin the package (e.g. pkg@1.2.3) so a hijacked latest can't be pulled.")
		}
		if u := strings.ToLower(strings.TrimSpace(s.URL)); strings.HasPrefix(u, "http://") && !isLoopbackURL(u) {
			r.Score -= 12
			r.Issues = append(r.Issues, fmt.Sprintf("server %q connects over cleartext http:// to a non-loopback host", n))
		}
		for k, v := range mergeStringMaps(s.Env, s.Headers) {
			if looksLikeRealSecret(v) {
				r.Score -= 20
				r.Issues = append(r.Issues, fmt.Sprintf("server %q has a hardcoded secret in %q — use ${ENV_VAR} instead", n, k))
				break
			}
		}
	}
	if r.Score < 0 {
		r.Score = 0
	}
	return r
}

func auditMCPTools(top map[string]json.RawMessage) MCPAuditResult {
	r := MCPAuditResult{Score: 100, Kind: "mcp-tools", ValidJSON: true}
	var tools []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(top["tools"], &tools); err != nil {
		// 値はあるが array でない → 壊れた tools 宣言。empty と区別する。
		r.Score -= 5
		r.Issues = append(r.Issues, "tools is malformed — must be a JSON array of {name, description}")
		return r
	}
	r.ToolCount = len(tools)
	if r.ToolCount == 0 {
		r.Score -= 5
		r.Issues = append(r.Issues, "tools[] is empty")
		return r
	}
	// cross-tool shadowing: 同名 tool は信頼された tool を上書きする poisoning vector。
	seen := make(map[string]int, len(tools))
	for _, t := range tools {
		if t.Name != "" {
			seen[t.Name]++
		}
	}
	dupNames := make([]string, 0)
	for name, n := range seen {
		if n >= 2 {
			dupNames = append(dupNames, name)
		}
	}
	sort.Strings(dupNames)
	for _, name := range dupNames {
		r.Score -= 20
		r.Issues = append(r.Issues, fmt.Sprintf("duplicate tool name %q — cross-tool shadowing risk", name))
	}
	for _, t := range tools {
		label := t.Name
		if label == "" {
			label = "(unnamed)"
		}
		d := t.Description
		if reInjection.MatchString(d) {
			r.Score -= 25
			r.Issues = append(r.Issues, fmt.Sprintf("tool %q description contains instruction-override text — classic tool-poisoning (MCPTox)", label))
		}
		if reExfil.MatchString(d) {
			r.Score -= 25
			r.Issues = append(r.Issues, fmt.Sprintf("tool %q description hints at reading/exfiltrating secrets/files", label))
		}
		if reHiddenChar.MatchString(d) {
			r.Score -= 15
			r.Issues = append(r.Issues, fmt.Sprintf("tool %q description contains zero-width/bidi hidden characters — concealment", label))
		}
		if reHTMLComment.MatchString(d) {
			r.Score -= 15
			r.Issues = append(r.Issues, fmt.Sprintf("tool %q description hides text in an HTML/markdown comment — concealment", label))
		}
		if hasEncodedPayload(d) {
			r.Score -= 20
			r.Issues = append(r.Issues, fmt.Sprintf("tool %q description hides an encoded instruction payload (base64) — evasion", label))
		}
		if len(d) > 2000 {
			r.Score -= 5
			r.Suggestions = append(r.Suggestions, fmt.Sprintf("tool %q has an unusually long description (%d chars) — review for hidden instructions", label, len(d)))
		}
	}
	if r.Score < 0 {
		r.Score = 0
	}
	return r
}

// ─── helpers ───

func hasPinnedPackage(args []string) bool {
	for _, a := range args {
		// pkg@1.2.3 / pkg==1.2.3 / @scope/pkg@1.2.3 を pin とみなす(先頭 @scope は除外)。
		s := strings.TrimSpace(a)
		if strings.HasPrefix(s, "-") {
			continue
		}
		if strings.Contains(strings.TrimPrefix(s, "@"), "@") || strings.Contains(s, "==") {
			return true
		}
	}
	return false
}

func isLoopbackURL(u string) bool {
	return strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost") || strings.Contains(u, "[::1]")
}

func mergeStringMaps(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// hasEncodedPayload は description 内の長い base64 run を復号し、復号後の平文が
// injection/exfil パターンに一致するかを見る(injectscan の decode-then-rescan
// と同じ gate: 無害な base64 token は flag しない)。
func hasEncodedPayload(d string) bool {
	for _, m := range reBase64Run.FindAllString(d, -1) {
		dec, err := base64.StdEncoding.DecodeString(m)
		if err != nil {
			continue
		}
		p := string(dec)
		if reInjection.MatchString(p) || reExfil.MatchString(p) {
			return true
		}
	}
	return false
}

func looksLikeRealSecret(v string) bool {
	t := strings.TrimSpace(v)
	if t == "" || strings.Contains(t, "${") || strings.HasPrefix(t, "$") {
		return false // ${VAR} / $VAR placeholder
	}
	if reSecretVal.MatchString(t) {
		return true
	}
	return len(t) >= 24 && !strings.ContainsAny(t, " /\\") // 長い不透明文字列
}
