package harness

import (
	"strings"
	"testing"
)

func plgIssue(issues []string, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i, substr) {
			return true
		}
	}
	return false
}

const cleanPlugin = `{
  "$schema": "https://json.schemastore.org/claude-code-plugin-manifest.json",
  "name": "yagura",
  "displayName": "Yagura",
  "version": "0.35.0",
  "description": "Portfolio orchestrator MCP server.",
  "author": {"name": "shizukutanaka", "url": "https://github.com/shizukutanaka/yagura"},
  "license": "MIT",
  "skills": ["./.claude/skills/"],
  "agents": ["./.claude/agents/yagura-reviewer.md"],
  "mcpServers": {"yagura": {"type": "http", "url": "http://127.0.0.1:8090/mcp"}}
}`

func TestAuditPlugin_Clean(t *testing.T) {
	r := AuditPluginManifest(cleanPlugin)
	if r.Kind != "plugin" || !r.ValidJSON || !r.NameValid {
		t.Fatalf("expected valid plugin, got %+v", r)
	}
	if r.Score != 100 {
		t.Errorf("clean plugin should score 100, got %d (issues %v)", r.Score, r.Issues)
	}
	// declared components recorded.
	want := map[string]bool{"skills": true, "agents": true, "mcpServers": true}
	for _, c := range r.Components {
		delete(want, c)
	}
	if len(want) != 0 {
		t.Errorf("missing components in %v", r.Components)
	}
}

func TestAuditPlugin_InvalidJSON(t *testing.T) {
	r := AuditPluginManifest(`{ "name": }`)
	if r.ValidJSON || r.Score != 0 {
		t.Errorf("invalid JSON → score 0, got %+v", r)
	}
}

func TestAuditPlugin_NameRules(t *testing.T) {
	missing := AuditPluginManifest(`{"description":"x"}`)
	if missing.NameValid || !plgIssue(missing.Issues, "missing 'name'") {
		t.Errorf("expected missing-name issue, got %+v", missing)
	}
	bad := AuditPluginManifest(`{"name":"My_Plugin"}`)
	if bad.NameValid || !plgIssue(bad.Issues, "kebab-case") {
		t.Errorf("expected kebab-case issue, got %+v", bad)
	}
}

func TestAuditPlugin_PathRules(t *testing.T) {
	notRel := AuditPluginManifest(`{"name":"p","agents":["agents/x.md"]}`)
	if !plgIssue(notRel.Issues, "start with") {
		t.Errorf("expected ./ path issue, got %v", notRel.Issues)
	}
	traversal := AuditPluginManifest(`{"name":"p","skills":["./../escape/"]}`)
	if !plgIssue(traversal.Issues, "traversal") {
		t.Errorf("expected traversal issue, got %v", traversal.Issues)
	}
}

func TestAuditPlugin_MCPServerNeedsCommandOrURL(t *testing.T) {
	r := AuditPluginManifest(`{"name":"p","mcpServers":{"x":{"args":["a"]}}}`)
	if !plgIssue(r.Issues, "command") {
		t.Errorf("expected mcp command/url issue, got %v", r.Issues)
	}
	ok := AuditPluginManifest(`{"name":"p","mcpServers":{"x":{"command":"./bin/x"}}}`)
	if plgIssue(ok.Issues, "command") {
		t.Errorf("command-based server should be fine, got %v", ok.Issues)
	}
}

const cleanMarketplace = `{
  "name": "yagura",
  "owner": {"name": "shizukutanaka"},
  "description": "Yagura marketplace.",
  "plugins": [
    {"name": "yagura", "source": "./", "description": "Portfolio orchestrator."}
  ]
}`

func TestAuditMarketplace_Clean(t *testing.T) {
	r := AuditPluginManifest(cleanMarketplace)
	if r.Kind != "marketplace" {
		t.Fatalf("should detect marketplace, got %q", r.Kind)
	}
	if r.Score != 100 || !r.NameValid {
		t.Errorf("clean marketplace should score 100, got %d (issues %v)", r.Score, r.Issues)
	}
}

func TestAuditMarketplace_Rules(t *testing.T) {
	noOwner := AuditPluginManifest(`{"name":"m","plugins":[{"name":"p","source":"./p"}],"owner":{}}`)
	if !plgIssue(noOwner.Issues, "owner.name") {
		t.Errorf("expected owner.name issue, got %v", noOwner.Issues)
	}
	dup := AuditPluginManifest(`{"name":"m","owner":{"name":"o"},"plugins":[{"name":"p","source":"./a"},{"name":"p","source":"./b"}]}`)
	if !plgIssue(dup.Issues, "duplicate") {
		t.Errorf("expected duplicate issue, got %v", dup.Issues)
	}
	badSrc := AuditPluginManifest(`{"name":"m","owner":{"name":"o"},"plugins":[{"name":"p","source":"plugins/p"}]}`)
	if !plgIssue(badSrc.Issues, "must start with") {
		t.Errorf("expected source ./ issue, got %v", badSrc.Issues)
	}
	githubOK := AuditPluginManifest(`{"name":"m","owner":{"name":"o"},"plugins":[{"name":"p","source":{"source":"github","repo":"o/r"}}]}`)
	if githubOK.Score != 100 {
		t.Errorf("github source with repo should be clean, got %d: %v", githubOK.Score, githubOK.Issues)
	}
}

// TestAuditMarketplace_MalformedPluginsNotReportedAsEmpty pins the fix for a
// misleading diagnosis: when "plugins" is present but is not a JSON array
// (e.g. a string or object), the unmarshal used to be silently ignored and the
// audit reported "plugins[] is empty" — which is wrong. A malformed plugins
// value is a different (worse) problem than an empty array, so the issue text
// must say it is malformed and must NOT claim the list is empty.
func TestAuditMarketplace_MalformedPluginsNotReportedAsEmpty(t *testing.T) {
	r := AuditPluginManifest(`{"name":"m","owner":{"name":"o"},"plugins":"oops"}`)
	if r.Kind != "marketplace" {
		t.Fatalf("should still detect marketplace, got %q", r.Kind)
	}
	if plgIssue(r.Issues, "empty") {
		t.Errorf("malformed plugins must not be reported as empty: %v", r.Issues)
	}
	if !plgIssue(r.Issues, "malformed") {
		t.Errorf("expected a 'malformed' issue for non-array plugins, got %v", r.Issues)
	}
}

// TestAuditMarketplace_MalformedOwnerDistinctFromMissing pins that an owner
// value that is present but not an object (e.g. a bare string) is reported as
// malformed rather than the generic "owner.name is required".
func TestAuditMarketplace_MalformedOwnerDistinctFromMissing(t *testing.T) {
	r := AuditPluginManifest(`{"name":"m","owner":"someone","plugins":[{"name":"p","source":"./p"}]}`)
	if !plgIssue(r.Issues, "owner") {
		t.Fatalf("expected an owner-related issue, got %v", r.Issues)
	}
	if !plgIssue(r.Issues, "malformed") {
		t.Errorf("expected 'malformed' owner issue (owner is a string, not an object), got %v", r.Issues)
	}
}

// TestAuditMarketplace_MalformedSourceObjectReported pins that a plugin whose
// source is an object but is malformed JSON-shaped (wrong types) is flagged
// rather than silently passing.
func TestAuditMarketplace_MalformedSourceObjectReported(t *testing.T) {
	// source is an object but "repo" has the wrong type (number) under a github
	// source — the object unmarshal fails and must be surfaced, not ignored.
	r := AuditPluginManifest(`{"name":"m","owner":{"name":"o"},"plugins":[{"name":"p","source":{"source":"github","repo":12345}}]}`)
	if !plgIssue(r.Issues, "source") {
		t.Errorf("expected a source issue for malformed source object, got %v", r.Issues)
	}
}

// ─── checkMarketplaceSource branch coverage ───────────────────

// helper: build a minimal marketplace JSON with one plugin having the given source value.
// Both "owner" and "plugins" are required for AuditPluginManifest to route to auditMarketplace.
func mktplaceWithSource(src string) string {
	return `{"name":"m","owner":{"name":"o"},"plugins":[{"name":"p","source":` + src + `}]}`
}

func TestAuditMarketplace_SourceStringTraversal(t *testing.T) {
	r := AuditPluginManifest(mktplaceWithSource(`"./../../etc/passwd"`))
	if !plgIssue(r.Issues, "traversal") {
		t.Errorf("expected path traversal issue for ../ in string source, got %v", r.Issues)
	}
}

func TestAuditMarketplace_SourceGitHubMissingRepo(t *testing.T) {
	r := AuditPluginManifest(mktplaceWithSource(`{"source":"github"}`))
	if !plgIssue(r.Issues, "repo") {
		t.Errorf("expected 'repo' issue for github source without repo, got %v", r.Issues)
	}
}

func TestAuditMarketplace_SourceGitSubdirMissingFields(t *testing.T) {
	// only url provided, path missing → issue
	r := AuditPluginManifest(mktplaceWithSource(`{"source":"git-subdir","url":"https://github.com/x/y"}`))
	if !plgIssue(r.Issues, "url") && !plgIssue(r.Issues, "path") {
		t.Errorf("expected url/path issue for incomplete git-subdir source, got %v", r.Issues)
	}
}

func TestAuditMarketplace_SourceGitSubdirOK(t *testing.T) {
	r := AuditPluginManifest(mktplaceWithSource(`{"source":"git-subdir","url":"https://github.com/x/y","path":"./plugins/p"}`))
	if plgIssue(r.Issues, "git-subdir") {
		t.Errorf("complete git-subdir source should not be flagged, got %v", r.Issues)
	}
}

func TestAuditMarketplace_SourceNpmOK(t *testing.T) {
	r := AuditPluginManifest(mktplaceWithSource(`{"source":"npm","package":"my-plugin@1.0.0"}`))
	if plgIssue(r.Issues, "needs") {
		t.Errorf("npm source should not require extra fields, got %v", r.Issues)
	}
}

func TestAuditMarketplace_SourceDefault_NotStringOrObject(t *testing.T) {
	// source is a number (not string or object) → default branch → flagged
	r := AuditPluginManifest(mktplaceWithSource(`42`))
	if !plgIssue(r.Issues, "must be a relative path string or an object") {
		t.Errorf("numeric source should hit default branch and be flagged, got %v", r.Issues)
	}
}

func TestAuditMarketplace_NoOwnerName(t *testing.T) {
	// owner present but name field absent → "owner.name is required"
	// AuditPluginManifest requires both "plugins" AND "owner" to route to auditMarketplace.
	r := AuditPluginManifest(`{"name":"m","owner":{},"plugins":[{"name":"p","source":"./p"}]}`)
	if r.Kind != "marketplace" {
		t.Fatalf("expected marketplace kind, got %q", r.Kind)
	}
	if !plgIssue(r.Issues, "owner.name") {
		t.Errorf("empty owner object should trigger owner.name issue, got %v", r.Issues)
	}
}

func TestAuditMarketplace_PluginsMissingField(t *testing.T) {
	// plugins is present but empty → "plugins[] is empty" issue
	// (JSON must have both "plugins" and "owner" to route to auditMarketplace)
	r := AuditPluginManifest(`{"name":"m","owner":{"name":"o"},"plugins":[]}`)
	if r.Kind != "marketplace" {
		t.Fatalf("expected marketplace, got %q", r.Kind)
	}
	if r.Score == 100 {
		t.Errorf("empty plugins[] should reduce score, got %d (issues %v)", r.Score, r.Issues)
	}
}

// ─── jsonString unmarshal-error branch ───────────────────────

// TestAuditPlugin_NameAsNumber covers `return ""` in jsonString when the raw
// message is valid JSON but not a string (here, a number). In auditPlugin,
// jsonString(top["name"]) returns "" → "missing 'name'" issue.
func TestAuditPlugin_NameAsNumber(t *testing.T) {
	r := AuditPluginManifest(`{"name":42}`)
	if r.NameValid {
		t.Error("numeric name should not be valid")
	}
	if !plgIssue(r.Issues, "missing 'name'") {
		t.Errorf("numeric name decoded as empty string → missing-name issue expected, got %v", r.Issues)
	}
}

// ─── auditPlugin additional branch coverage ──────────────────

// TestAuditPlugin_NonSemverVersion covers the `r.Score -= 6` for a version that
// is present but does not match semver MAJOR.MINOR.PATCH.
func TestAuditPlugin_NonSemverVersion(t *testing.T) {
	r := AuditPluginManifest(`{"name":"p","version":"1.0"}`)
	if !plgIssue(r.Issues, "not semantic") {
		t.Errorf("version '1.0' should flag non-semver issue, got %v", r.Issues)
	}
}

// TestAuditPlugin_BareStringAuthor covers the `r.Score -= 4` for an "author" that
// is a bare JSON string rather than an object {name, email?, url?}.
func TestAuditPlugin_BareStringAuthor(t *testing.T) {
	r := AuditPluginManifest(`{"name":"p","author":"Alice"}`)
	found := false
	for _, s := range r.Suggestions {
		if strings.Contains(s, "author should be an object") {
			found = true
		}
	}
	if !found {
		t.Errorf("bare-string author should suggest object form, got %v", r.Suggestions)
	}
}

// TestAuditPlugin_SkillsAsSingleString covers the `case '"'` path in
// checkComponentPaths when the component value is a single string (not an array).
func TestAuditPlugin_SkillsAsSingleString(t *testing.T) {
	// "skill.md" does not start with "./" → badRel issue.
	r := AuditPluginManifest(`{"name":"p","skills":"skill.md"}`)
	if !plgIssue(r.Issues, "start with") {
		t.Errorf("single-string skill not starting with ./ should flag, got %v", r.Issues)
	}
}

// TestAuditPlugin_EmptyPathSkipped covers the `if p == "" { continue }` branch
// inside checkComponentPaths when an array contains an empty-string element.
func TestAuditPlugin_EmptyPathSkipped(t *testing.T) {
	// First element is ""; second is valid → no bad-rel issue (empty is skipped).
	r := AuditPluginManifest(`{"name":"p","agents":["","./agents/x.md"]}`)
	// Empty string is skipped, "./agents/x.md" starts with "./" → no issue.
	if plgIssue(r.Issues, "start with") {
		t.Errorf("empty path should be skipped, not flagged, got %v", r.Issues)
	}
}

// TestAuditPlugin_ScoreClamped covers the `r.Score = 0` clamp in auditPlugin.
// Missing name (−30) + 10 mcpServers each missing command/url (10×−10=−100) +
// no description (−3) = raw score −33 → clamped to 0.
func TestAuditPlugin_ScoreClamped(t *testing.T) {
	// No "name" field → -30; 10 inline mcpServers with no command/url → -100.
	servers := `{"a":{},"b":{},"c":{},"d":{},"e":{},"f":{},"g":{},"h":{},"i":{},"j":{}}`
	r := AuditPluginManifest(`{"mcpServers":` + servers + `}`)
	if r.Score != 0 {
		t.Errorf("massively-penalised plugin should clamp to score 0, got %d (issues %v)", r.Score, r.Issues)
	}
}

// ─── auditMarketplace additional branch coverage ─────────────

// TestAuditMarketplace_NoTopLevelName covers the `r.Score -= 25` for a marketplace
// that has "plugins" and "owner" (required for routing) but no "name" key.
func TestAuditMarketplace_NoTopLevelName(t *testing.T) {
	r := AuditPluginManifest(`{"owner":{"name":"o"},"plugins":[{"name":"p","source":"./p"}]}`)
	if r.Kind != "marketplace" {
		t.Fatalf("expected marketplace, got %q", r.Kind)
	}
	if !plgIssue(r.Issues, "missing marketplace") {
		t.Errorf("expected missing-marketplace-name issue, got %v", r.Issues)
	}
}

// TestAuditMarketplace_NonKebabTopLevelName covers `r.Score -= 15` for a
// marketplace whose name is present but not kebab-case.
func TestAuditMarketplace_NonKebabTopLevelName(t *testing.T) {
	r := AuditPluginManifest(`{"name":"My Marketplace","owner":{"name":"o"},"plugins":[{"name":"p","source":"./p"}]}`)
	if r.NameValid {
		t.Error("non-kebab marketplace name should not be valid")
	}
	if !plgIssue(r.Issues, "kebab-case") {
		t.Errorf("expected kebab-case issue for marketplace name, got %v", r.Issues)
	}
}

// TestAuditMarketplace_PluginEmptyNameAndMissingSource covers several branches:
//   - label = fmt.Sprintf("#%d", i) when plugin.Name == ""
//   - r.Score -= 10 for missing plugin name
//   - checkMarketplaceSource "missing 'source'" (len(raw)==0)
//   - r.Score = 0 clamp (missing marketplace name −25 + 5 plugins ×20 = −125)
func TestAuditMarketplace_PluginEmptyNameAndMissingSource(t *testing.T) {
	// No top-level name (−25) + 5 plugins with no name (5×−10) + no source (5×−10) = −125 → 0.
	r := AuditPluginManifest(`{"owner":{"name":"o"},"plugins":[{},{},{},{},{}]}`)
	if r.Kind != "marketplace" {
		t.Fatalf("expected marketplace, got %q", r.Kind)
	}
	if !plgIssue(r.Issues, "missing") {
		t.Errorf("expected missing-name issues, got %v", r.Issues)
	}
	if r.Score != 0 {
		t.Errorf("heavily penalised marketplace should clamp to 0, got %d", r.Score)
	}
}

// TestAuditMarketplace_PluginNonKebabName covers `r.Score -= 8` for a plugin
// whose name is present but not kebab-case.
func TestAuditMarketplace_PluginNonKebabName(t *testing.T) {
	r := AuditPluginManifest(`{"name":"m","owner":{"name":"o"},"plugins":[{"name":"My Plugin","source":"./p"}]}`)
	if !plgIssue(r.Issues, "kebab-case") {
		t.Errorf("non-kebab plugin name should flag, got %v", r.Issues)
	}
}

// ─── checkMCPServers unmarshal-error branch ──────────────────

// TestAuditPlugin_MCPServersUnmarshalError covers the `return` in checkMCPServers
// when json.Unmarshal fails (e.g. command is a JSON number, not a string).
func TestAuditPlugin_MCPServersUnmarshalError(t *testing.T) {
	// "command":42 causes json.Unmarshal into struct{Command string} to fail.
	r := AuditPluginManifest(`{"name":"p","mcpServers":{"x":{"command":42}}}`)
	// The error path returns silently — no "needs a command/url" issue should appear.
	if plgIssue(r.Issues, "needs a 'command'") {
		t.Errorf("unmarshal error should cause silent return, not a false command issue, got %v", r.Issues)
	}
}

// ─── checkComponentPaths default branch ──────────────────────

// TestAuditPlugin_ComponentNullValue covers the `default: return` in
// checkComponentPaths when the raw JSON value starts with 'n' (null).
func TestAuditPlugin_ComponentNullValue(t *testing.T) {
	// skills:null — raw[0]='n' → hits default: return (no path check).
	r := AuditPluginManifest(`{"name":"p","skills":null}`)
	// null skills: checkComponentPaths returns immediately, no path issue.
	if plgIssue(r.Issues, "start with") {
		t.Errorf("null skills should not flag a path issue, got %v", r.Issues)
	}
}
