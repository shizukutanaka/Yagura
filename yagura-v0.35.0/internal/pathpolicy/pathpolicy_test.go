package pathpolicy

import (
	"encoding/json"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"go.mod", "go.mod", true},
		{"go.mod", "go.sum", false},
		{"*.go", "main.go", true},
		{"*.go", "cmd/main.go", false}, // * is single-segment
		{"**/*_test.go", "internal/audit/audit_test.go", true},
		{"**/*_test.go", "audit_test.go", true}, // ** matches zero segments
		{"internal/audit/**", "internal/audit/audit.go", true},
		{"internal/audit/**", "internal/audit/sub/x.go", true},
		{"internal/audit/**", "internal/audit", true}, // ** matches zero
		{"internal/audit/**", "internal/mcp/x.go", false},
		{"cmd/*/main.go", "cmd/yagura/main.go", true},
		{"cmd/*/main.go", "cmd/yagura/sub/main.go", false},
		{"**", "anything/at/all.go", true},
		{"docs/**/*.md", "docs/adr/0001.md", true},
		{"docs/**/*.md", "docs/x.md", true},
		{"docs/**/*.md", "docs/x.txt", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pat, c.name); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pat, c.name, got, c.want)
		}
	}
}

func TestEvaluate_StrictestWins(t *testing.T) {
	p := Policy{Rules: []Rule{
		{Path: "**/*.go", Action: ActionAllow},
		{Path: "internal/audit/**", Action: ActionReview, Reason: "trust base"},
		{Path: "go.mod", Action: ActionDeny, Reason: "ADR-0001"},
	}}
	r := Evaluate(p, []string{"internal/audit/audit.go", "cmd/yagura/main.go", "go.mod"})

	byPath := map[string]Decision{}
	for _, d := range r.Decisions {
		byPath[d.Path] = d
	}
	if byPath["internal/audit/audit.go"].Action != ActionReview {
		t.Errorf("audit.go: want review (strictest of allow+review), got %v", byPath["internal/audit/audit.go"].Action)
	}
	if byPath["cmd/yagura/main.go"].Action != ActionAllow {
		t.Errorf("main.go: want allow, got %v", byPath["cmd/yagura/main.go"].Action)
	}
	if byPath["go.mod"].Action != ActionDeny || byPath["go.mod"].Reason != "ADR-0001" {
		t.Errorf("go.mod: want deny+reason, got %+v", byPath["go.mod"])
	}
	if r.Worst != ActionDeny {
		t.Errorf("worst should be deny, got %v", r.Worst)
	}
	if len(r.Denied) != 1 || len(r.Review) != 1 || r.Allowed != 1 {
		t.Errorf("counts off: denied=%v review=%v allowed=%d", r.Denied, r.Review, r.Allowed)
	}
}

func TestEvaluate_DefaultAction(t *testing.T) {
	// no rule matches → default allow
	r := Evaluate(Policy{Rules: []Rule{{Path: "go.mod", Action: ActionDeny}}}, []string{"README.md"})
	if r.Decisions[0].Action != ActionAllow || r.Decisions[0].Rule != "" {
		t.Errorf("unmatched should be default allow, got %+v", r.Decisions[0])
	}
	// explicit default review
	r = Evaluate(Policy{Default: ActionReview}, []string{"README.md"})
	if r.Decisions[0].Action != ActionReview {
		t.Errorf("want default review, got %v", r.Decisions[0].Action)
	}
	if r.Worst != ActionReview {
		t.Errorf("worst should be review, got %v", r.Worst)
	}
}

func TestEvaluate_Deterministic(t *testing.T) {
	p := Policy{Rules: []Rule{{Path: "go.mod", Action: ActionDeny}}}
	in := []string{"z.go", "go.mod", "a.go"}
	r1 := Evaluate(p, in)
	r2 := Evaluate(p, in)
	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	if string(b1) != string(b2) {
		t.Error("Evaluate must be deterministic")
	}
	// decisions sorted by path
	if r1.Decisions[0].Path != "a.go" || r1.Decisions[1].Path != "go.mod" || r1.Decisions[2].Path != "z.go" {
		t.Errorf("decisions not sorted by path: %+v", r1.Decisions)
	}
}

func TestEvaluate_EmptyAndNormalize(t *testing.T) {
	r := Evaluate(Policy{Rules: []Rule{{Path: "go.mod", Action: ActionDeny}}}, []string{"./go.mod", "", "  "})
	// "./go.mod" normalizes to "go.mod" → deny; blanks skipped
	if len(r.Decisions) != 1 || r.Decisions[0].Action != ActionDeny {
		t.Errorf("normalize/skip failed: %+v", r.Decisions)
	}
}

// TestSeverity_UnknownAction covers the default return 0 in severity.
func TestSeverity_UnknownAction(t *testing.T) {
	if got := severity("bogus"); got != 0 {
		t.Errorf("unknown action: got %d, want 0", got)
	}
}

// TestMatchGlob_EmptyPattern covers the early-return branch in matchGlob when
// pattern is "": returns true only if name is also empty.
func TestMatchGlob_EmptyPattern(t *testing.T) {
	if !matchGlob("", "") {
		t.Error("matchGlob('','') should return true")
	}
	if matchGlob("", "nonempty") {
		t.Error("matchGlob('','nonempty') should return false")
	}
}

// TestMatchSegments_ConsecutiveDoubleStar covers the pat = pat[1:] collapse loop
// inside matchSegments when two consecutive "**" appear in the pattern.
func TestMatchSegments_ConsecutiveDoubleStar(t *testing.T) {
	// **/** is equivalent to ** — should match any path (including empty).
	if !matchGlob("**/**", "a/b/c") {
		t.Error("**/** should match a/b/c")
	}
	if !matchGlob("**/**", "x") {
		t.Error("**/** should match x (single segment)")
	}
}

// TestEvaluate_RuleWithEmptyAction covers the "if r.Action == """ continue branch
// inside Evaluate's inner rule loop.
func TestEvaluate_RuleWithEmptyAction(t *testing.T) {
	p := Policy{
		Rules: []Rule{
			{Path: "go.mod", Action: ""},         // empty action → skipped by continue
			{Path: "go.mod", Action: ActionDeny}, // this applies
		},
	}
	r := Evaluate(p, []string{"go.mod"})
	if len(r.Decisions) != 1 || r.Decisions[0].Action != ActionDeny {
		t.Errorf("empty-action rule should be skipped: %+v", r.Decisions)
	}
}

// ─── Policy.Validate — reject silently-inert rules (fail-open guard) ──────
//
// A deny rule with a malformed glob (path.Match ErrBadPattern) or a typo'd
// action (severity()==0, never overrides the allow default) silently never
// fires, so a path that should be denied falls through to allow. Validate
// turns those typos into load-time errors instead of a disabled guardrail.

func TestValidate_OK(t *testing.T) {
	p := Policy{
		Default: ActionReview,
		Rules: []Rule{
			{Path: "go.mod", Action: ActionDeny, Reason: "ADR-0001"},
			{Path: "docs/**", Action: ActionAllow},
			{Path: "internal/*/secret_*.go", Action: ActionReview},
		},
	}
	if err := p.Validate(); err != nil {
		t.Errorf("valid policy rejected: %v", err)
	}
}

func TestValidate_MalformedGlob(t *testing.T) {
	p := Policy{Rules: []Rule{{Path: "secrets/[", Action: ActionDeny}}}
	if err := p.Validate(); err == nil {
		t.Error("expected error for malformed glob in a deny rule (would silently never match)")
	}
}

func TestValidate_BadAction(t *testing.T) {
	p := Policy{Rules: []Rule{{Path: "go.mod", Action: "denyy"}}}
	if err := p.Validate(); err == nil {
		t.Error("expected error for unknown action (severity 0 silently never overrides allow)")
	}
}

func TestValidate_BadDefault(t *testing.T) {
	p := Policy{Default: "opem", Rules: []Rule{{Path: "x", Action: ActionDeny}}}
	if err := p.Validate(); err == nil {
		t.Error("expected error for unknown default action")
	}
}

func TestValidate_EmptyPath(t *testing.T) {
	p := Policy{Rules: []Rule{{Path: "", Action: ActionDeny}}}
	if err := p.Validate(); err == nil {
		t.Error("expected error for empty rule path")
	}
}

func TestValidate_EmptyActionAllowed(t *testing.T) {
	// empty action is an explicit skip in Evaluate; keep that semantics.
	p := Policy{Rules: []Rule{{Path: "x", Action: ""}}}
	if err := p.Validate(); err != nil {
		t.Errorf("empty action should be allowed (explicit skip), got %v", err)
	}
}
