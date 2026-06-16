// plugin_audit.go: Claude Code プラグイン / マーケットプレイス manifest の heuristic 評価。
//
// Claude Code のプラグインは skills / commands / hooks / agents / MCP server を 1 つに
// まとめて配布する仕組みで、`.claude-plugin/plugin.json`(plugin)と
// `.claude-plugin/marketplace.json`(marketplace)が manifest。公式ドキュメント
// (code.claude.com/docs/en/plugins-reference, plugin-marketplaces)の構造ルールを
// 構造ベースの lint rule にする:
//
//	plugin.json:
//	  - name 必須 + kebab-case(^[a-z0-9-]+$)。namespacing /<plugin>:<command> の鍵
//	  - component path 群(skills/commands/agents/hooks/mcpServers/...)は "./" 始まりの
//	    相対 path、`../`(path traversal)禁止
//	  - mcpServers の inline entry は command か url を持つ
//	  - version は semver 形式(任意)
//	marketplace.json:
//	  - name(kebab)/ owner.name / plugins[] 必須
//	  - plugin name は kebab かつ unique、source は相対 path("./" 始まり)か object
//
// AuditSkill / AuditSubagent / AuditWorkflow / AuditSettings / AuditAgentConfig と同じ
// shape(Score + Issues + Suggestions)。stdlib encoding/json のみ(ADR-0001)。
package harness

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// PluginAuditResult は plugin.json / marketplace.json 評価結果。
type PluginAuditResult struct {
	Score       int      `json:"score"`
	Kind        string   `json:"kind"` // "plugin" | "marketplace"
	ValidJSON   bool     `json:"valid_json"`
	Name        string   `json:"name,omitempty"`
	NameValid   bool     `json:"name_valid"`
	Components  []string `json:"components,omitempty"` // plugin: 宣言された component 種別 / marketplace: plugin 名
	Issues      []string `json:"issues,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

var (
	reKebab  = regexp.MustCompile(`^[a-z0-9-]+$`)
	reSemver = regexp.MustCompile(`^\d+\.\d+\.\d+([-+].*)?$`)
)

// AuditPluginManifest は content を plugin / marketplace のどちらかと判定して評価する。
// marketplace は top-level に "plugins"(配列)と "owner" を持つことで判別する。
func AuditPluginManifest(content string) PluginAuditResult {
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &top); err != nil {
		return PluginAuditResult{Score: 0, ValidJSON: false, Kind: "plugin",
			Issues:      []string{"invalid JSON — Claude Code cannot load this manifest at all"},
			Suggestions: []string{"plugin.json / marketplace.json must be strict JSON."}}
	}
	if _, hasPlugins := top["plugins"]; hasPlugins {
		if _, hasOwner := top["owner"]; hasOwner {
			return auditMarketplace(top)
		}
	}
	return auditPlugin(top)
}

func auditPlugin(top map[string]json.RawMessage) PluginAuditResult {
	r := PluginAuditResult{Score: 100, Kind: "plugin", ValidJSON: true}

	r.Name = jsonString(top["name"])
	if r.Name == "" {
		r.Score -= 30
		r.Issues = append(r.Issues, "missing 'name' — required, and the namespacing key for /<plugin>:<command>")
	} else if !reKebab.MatchString(r.Name) {
		r.Score -= 20
		r.Issues = append(r.Issues, fmt.Sprintf("name %q is not kebab-case (^[a-z0-9-]+$) — no spaces/uppercase/underscore", r.Name))
	} else {
		r.NameValid = true
	}

	if v := jsonString(top["version"]); v != "" && !reSemver.MatchString(v) {
		r.Score -= 6
		r.Issues = append(r.Issues, fmt.Sprintf("version %q is not semantic (MAJOR.MINOR.PATCH)", v))
	}
	if jsonString(top["description"]) == "" {
		r.Score -= 3
		r.Suggestions = append(r.Suggestions, "add a 'description' — shown in the plugin manager.")
	}
	// author は object(.name)であるべき。bare string は誤り。
	if a, ok := top["author"]; ok && len(a) > 0 && a[0] == '"' {
		r.Score -= 4
		r.Suggestions = append(r.Suggestions, "author should be an object {name, email?, url?}, not a bare string.")
	}

	// component path 群(string | []string | inline object)。
	for _, field := range []string{"skills", "commands", "agents", "hooks", "mcpServers", "lspServers", "outputStyles"} {
		raw, ok := top[field]
		if !ok || len(raw) == 0 {
			continue
		}
		r.Components = append(r.Components, field)
		checkComponentPaths(field, raw, &r)
	}
	sort.Strings(r.Components)
	// mcpServers が inline object なら各 server に command/url が要る。
	if raw, ok := top["mcpServers"]; ok && len(raw) > 0 && raw[0] == '{' {
		checkMCPServers(raw, &r)
	}

	if r.Score < 0 {
		r.Score = 0
	}
	return r
}

func auditMarketplace(top map[string]json.RawMessage) PluginAuditResult {
	r := PluginAuditResult{Score: 100, Kind: "marketplace", ValidJSON: true}

	r.Name = jsonString(top["name"])
	if r.Name == "" {
		r.Score -= 25
		r.Issues = append(r.Issues, "missing marketplace 'name' (required)")
	} else if !reKebab.MatchString(r.Name) {
		r.Score -= 15
		r.Issues = append(r.Issues, fmt.Sprintf("marketplace name %q is not kebab-case", r.Name))
	} else {
		r.NameValid = true
	}

	// owner.name 必須。owner が object でない(string 等)場合は malformed として
	// "required" とは区別する — 誤診断(値はあるのに「欠落」と言う)を避ける。
	ownerName := ""
	if raw, ok := top["owner"]; ok {
		var o struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			r.Score -= 15
			r.Issues = append(r.Issues, "owner is malformed — must be an object {name, email?, url?}")
		} else {
			ownerName = o.Name
		}
	}
	if ownerName == "" && !plgIssueHas(r.Issues, "owner is malformed") {
		r.Score -= 15
		r.Issues = append(r.Issues, "owner.name is required")
	}

	// plugins は配列必須。配列でない(string/object 等)場合は malformed として
	// "empty" とは区別する — 壊れている manifest を「空」と誤報しない。
	var plugins []struct {
		Name        string          `json:"name"`
		Source      json.RawMessage `json:"source"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(top["plugins"], &plugins); err != nil {
		r.Score -= 20
		r.Issues = append(r.Issues, "plugins is malformed — must be a JSON array of plugin entries")
	} else if len(plugins) == 0 {
		r.Score -= 20
		r.Issues = append(r.Issues, "plugins[] is empty — a marketplace lists at least one plugin")
	}
	seen := map[string]bool{}
	for i, p := range plugins {
		label := p.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}
		r.Components = append(r.Components, label)
		if p.Name == "" {
			r.Score -= 10
			r.Issues = append(r.Issues, fmt.Sprintf("plugin %s: missing name", label))
		} else if !reKebab.MatchString(p.Name) {
			r.Score -= 8
			r.Issues = append(r.Issues, fmt.Sprintf("plugin %q name is not kebab-case", p.Name))
		}
		if p.Name != "" {
			if seen[p.Name] {
				r.Score -= 10
				r.Issues = append(r.Issues, fmt.Sprintf("duplicate plugin name %q in plugins[]", p.Name))
			}
			seen[p.Name] = true
		}
		checkMarketplaceSource(label, p.Source, &r)
	}
	sort.Strings(r.Components)

	if r.Score < 0 {
		r.Score = 0
	}
	return r
}

// ─── helpers ───

// plgIssueHas は既出 issue に substr を含むものがあるかを返す
// (同じ欠陥を二重報告しないためのガード)。
func plgIssueHas(issues []string, substr string) bool {
	for _, s := range issues {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

// jsonString は RawMessage を string として読む(失敗時 "")。
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// checkComponentPaths は string / []string の path を検証する(object は path 検査対象外)。
func checkComponentPaths(field string, raw json.RawMessage, r *PluginAuditResult) {
	var paths []string
	switch raw[0] {
	case '"':
		paths = []string{jsonString(raw)}
	case '[':
		json.Unmarshal(raw, &paths)
	case '{':
		return // inline object(hooks/mcpServers 等)は path ではない
	default:
		return
	}
	badRel, traversal := false, false
	for _, p := range paths {
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "./") {
			badRel = true
		}
		if strings.Contains(p, "../") {
			traversal = true
		}
	}
	if badRel {
		r.Score -= 10
		r.Issues = append(r.Issues, fmt.Sprintf("%s: component paths must be relative and start with \"./\"", field))
	}
	if traversal {
		r.Score -= 15
		r.Issues = append(r.Issues, fmt.Sprintf("%s: path traversal (\"../\") is not allowed", field))
	}
}

// checkMCPServers は inline mcpServers の各 entry に command か url があるか検証する。
func checkMCPServers(raw json.RawMessage, r *PluginAuditResult) {
	var servers map[string]struct {
		Command string `json:"command"`
		URL     string `json:"url"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(raw, &servers) != nil {
		return
	}
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := servers[n]
		if s.Command == "" && s.URL == "" {
			r.Score -= 10
			r.Issues = append(r.Issues, fmt.Sprintf("mcp server %q: needs a 'command' (stdio) or a 'url' (http/sse)", n))
		}
	}
}

// checkMarketplaceSource は plugin source(相対 path string か object)を検証する。
func checkMarketplaceSource(label string, raw json.RawMessage, r *PluginAuditResult) {
	if len(raw) == 0 {
		r.Score -= 10
		r.Issues = append(r.Issues, fmt.Sprintf("plugin %s: missing 'source'", label))
		return
	}
	switch raw[0] {
	case '"':
		s := jsonString(raw)
		if !strings.HasPrefix(s, "./") {
			r.Score -= 8
			r.Issues = append(r.Issues, fmt.Sprintf("plugin %s: relative source %q must start with \"./\"", label, s))
		}
		if strings.Contains(s, "../") {
			r.Score -= 12
			r.Issues = append(r.Issues, fmt.Sprintf("plugin %s: source has path traversal (\"../\")", label))
		}
	case '{':
		var o struct {
			Source string `json:"source"`
			Repo   string `json:"repo"`
			URL    string `json:"url"`
			Path   string `json:"path"`
		}
		json.Unmarshal(raw, &o)
		switch o.Source {
		case "github":
			if o.Repo == "" {
				r.Score -= 8
				r.Issues = append(r.Issues, fmt.Sprintf("plugin %s: github source needs 'repo' (owner/repo)", label))
			}
		case "git-subdir":
			if o.URL == "" || o.Path == "" {
				r.Score -= 8
				r.Issues = append(r.Issues, fmt.Sprintf("plugin %s: git-subdir source needs both 'url' and 'path'", label))
			}
		case "url", "npm", "":
			// url/npm は最低限の field を持つ前提(ここでは緩く許容)
		}
	default:
		r.Score -= 8
		r.Issues = append(r.Issues, fmt.Sprintf("plugin %s: source must be a relative path string or an object", label))
	}
}
