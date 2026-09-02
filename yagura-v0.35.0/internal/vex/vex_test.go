package vex

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

func TestBuild_Defaults(t *testing.T) {
	d := Build("", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}},
	})
	if d.Author != "yagura" {
		t.Errorf("author default = %q, want yagura", d.Author)
	}
	if d.Context != contextURL {
		t.Errorf("context = %q, want %q", d.Context, contextURL)
	}
	if d.Version != 1 {
		t.Errorf("version = %d, want 1", d.Version)
	}
	if d.Timestamp != "2026-06-06T12:00:00Z" {
		t.Errorf("timestamp = %q", d.Timestamp)
	}
	if got := d.Statements[0].Status; got != StatusUnderInvestigation {
		t.Errorf("default status = %q, want under_investigation", got)
	}
	if !strings.HasPrefix(d.ID, "urn:yagura:vex:") {
		t.Errorf("@id = %q, want urn:yagura:vex: prefix", d.ID)
	}
}

func TestBuild_Deterministic(t *testing.T) {
	stmts := []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0002"}, Status: StatusFixed},
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusAffected, ActionStatement: "upgrade"},
	}
	a := Build("acme", fixedNow, stmts)
	b := Build("acme", fixedNow, stmts)
	if a.ID != b.ID {
		t.Errorf("@id not deterministic: %q vs %q", a.ID, b.ID)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Error("documents not byte-identical for identical input")
	}
}

func TestBuild_Sorted(t *testing.T) {
	d := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0009"}, Status: StatusFixed},
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusFixed},
		{Vulnerability: Vuln{Name: "CVE-2025-0005"}, Status: StatusFixed},
	})
	want := []string{"CVE-2025-0001", "CVE-2025-0005", "CVE-2025-0009"}
	for i, w := range want {
		if d.Statements[i].Vulnerability.Name != w {
			t.Errorf("statement[%d] = %q, want %q", i, d.Statements[i].Vulnerability.Name, w)
		}
	}
}

func TestBuild_SortByProductTieBreak(t *testing.T) {
	d := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Products: []Product{{ID: "pkg:zeta"}}, Status: StatusFixed},
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Products: []Product{{ID: "pkg:alpha"}}, Status: StatusFixed},
	})
	if d.Statements[0].Products[0].ID != "pkg:alpha" {
		t.Errorf("tie-break by product failed: got %q first", d.Statements[0].Products[0].ID)
	}
}

func TestBuild_TrimsVulnName(t *testing.T) {
	d := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "  CVE-2025-0001  "}, Status: StatusFixed},
	})
	if d.Statements[0].Vulnerability.Name != "CVE-2025-0001" {
		t.Errorf("name not trimmed: %q", d.Statements[0].Vulnerability.Name)
	}
}

func TestValidate_OK(t *testing.T) {
	d := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusNotAffected, Justification: JustComponentNotPresent},
		{Vulnerability: Vuln{Name: "CVE-2025-0002"}, Status: StatusAffected, ActionStatement: "upgrade to 2.0"},
		{Vulnerability: Vuln{Name: "CVE-2025-0003"}, Status: StatusFixed},
	})
	if issues := Validate(d); len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
}

func TestValidate_NotAffectedNeedsJustification(t *testing.T) {
	d := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusNotAffected},
	})
	issues := Validate(d)
	if !hasIssue(issues, "requires a justification") {
		t.Errorf("expected justification issue, got %v", issues)
	}
}

func TestValidate_NotAffectedImpactStatementOK(t *testing.T) {
	d := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusNotAffected, ImpactStatement: "endpoint unreachable"},
	})
	if issues := Validate(d); len(issues) != 0 {
		t.Errorf("impact_statement should satisfy not_affected, got %v", issues)
	}
}

func TestValidate_InvalidJustification(t *testing.T) {
	d := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusNotAffected, Justification: "made_up"},
	})
	if !hasIssue(Validate(d), "invalid justification") {
		t.Error("expected invalid justification issue")
	}
}

func TestValidate_InvalidStatus(t *testing.T) {
	d := Document{
		Context:    contextURL,
		Statements: []Statement{{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: "broken"}},
	}
	if !hasIssue(Validate(d), "invalid status") {
		t.Error("expected invalid status issue")
	}
}

func TestValidate_AffectedNeedsAction(t *testing.T) {
	d := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusAffected},
	})
	if !hasIssue(Validate(d), "action_statement") {
		t.Error("expected action_statement issue for affected")
	}
}

func TestValidate_MissingName(t *testing.T) {
	d := Document{
		Context:    contextURL,
		Statements: []Statement{{Status: StatusFixed}},
	}
	if !hasIssue(Validate(d), "vulnerability.name is required") {
		t.Error("expected missing name issue")
	}
}

func TestValidate_EmptyDocument(t *testing.T) {
	issues := Validate(Document{})
	if !hasIssue(issues, "missing @context") || !hasIssue(issues, "no statements") {
		t.Errorf("expected context+statements issues, got %v", issues)
	}
}

func TestMerge_AddsNewPreservesVerdicts(t *testing.T) {
	base := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusNotAffected, Justification: JustComponentNotPresent},
		{Vulnerability: Vuln{Name: "CVE-2025-0002"}, Status: StatusFixed},
	})
	later := fixedNow.Add(time.Hour)
	merged := Merge(base, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0002"}}, // already present → must NOT downgrade
		{Vulnerability: Vuln{Name: "CVE-2025-0003"}}, // new → under_investigation
	}, later)

	if len(merged.Statements) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(merged.Statements))
	}
	if merged.Version != base.Version+1 {
		t.Errorf("version = %d, want %d", merged.Version, base.Version+1)
	}
	byName := map[string]Statement{}
	for _, s := range merged.Statements {
		byName[s.Vulnerability.Name] = s
	}
	if byName["CVE-2025-0002"].Status != StatusFixed {
		t.Errorf("existing verdict clobbered: CVE-2025-0002 = %q, want fixed", byName["CVE-2025-0002"].Status)
	}
	if byName["CVE-2025-0001"].Status != StatusNotAffected {
		t.Errorf("existing verdict clobbered: CVE-2025-0001 = %q", byName["CVE-2025-0001"].Status)
	}
	if byName["CVE-2025-0003"].Status != StatusUnderInvestigation {
		t.Errorf("new statement status = %q, want under_investigation", byName["CVE-2025-0003"].Status)
	}
}

func TestMerge_NoNewIsNoOp(t *testing.T) {
	base := Build("acme", fixedNow, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusFixed},
	})
	later := fixedNow.Add(time.Hour)
	merged := Merge(base, []Statement{
		{Vulnerability: Vuln{Name: "CVE-2025-0001"}}, // duplicate only
		{Vulnerability: Vuln{Name: "   "}},           // blank ignored
	}, later)
	if merged.ID != base.ID || merged.Version != base.Version || merged.Timestamp != base.Timestamp {
		t.Errorf("no-op merge must return base unchanged: id %q/%q ver %d/%d", merged.ID, base.ID, merged.Version, base.Version)
	}
}

func TestMerge_Deterministic(t *testing.T) {
	base := Build("acme", fixedNow, []Statement{{Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusFixed}})
	later := fixedNow.Add(time.Hour)
	adds := []Statement{{Vulnerability: Vuln{Name: "CVE-2025-0009"}}}
	a := Merge(base, adds, later)
	b := Merge(base, adds, later)
	if a.ID != b.ID {
		t.Errorf("merge @id not deterministic: %q vs %q", a.ID, b.ID)
	}
	// re-sorted: 0001 before 0009
	if a.Statements[0].Vulnerability.Name != "CVE-2025-0001" {
		t.Errorf("merge not re-sorted: first = %q", a.Statements[0].Vulnerability.Name)
	}
}

func TestMerge_EmptyBaseSetsContext(t *testing.T) {
	merged := Merge(Document{}, []Statement{{Vulnerability: Vuln{Name: "CVE-2025-0001"}}}, fixedNow)
	if merged.Context != contextURL {
		t.Errorf("context = %q, want %q", merged.Context, contextURL)
	}
	if merged.Author != "yagura" {
		t.Errorf("author default = %q", merged.Author)
	}
	if merged.Version != 2 {
		t.Errorf("version = %d, want 2 (1->+1)", merged.Version)
	}
}

func TestParseAndValidate_OK(t *testing.T) {
	data := []byte(`{
		"@context":"https://openvex.dev/ns/v0.2.0",
		"@id":"urn:test:1","author":"acme","timestamp":"2026-06-06T12:00:00Z","version":1,
		"tooling":"openvex-cli",
		"statements":[{
			"vulnerability":{"@id":"https://nvd.nist.gov/vuln/detail/CVE-2025-0001","name":"CVE-2025-0001"},
			"products":[{"@id":"pkg:github/acme/x@1.0.0","subcomponents":[{"@id":"pkg:golang/net/http"}]}],
			"status":"not_affected","justification":"vulnerable_code_not_in_execute_path"
		}]
	}`)
	d, issues, err := ParseAndValidate(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues, got %v", issues)
	}
	// richer fields round-trip
	if d.Tooling != "openvex-cli" {
		t.Errorf("tooling = %q", d.Tooling)
	}
	if d.Statements[0].Vulnerability.ID == "" {
		t.Error("vulnerability @id dropped")
	}
	if len(d.Statements[0].Products[0].Subcomponents) != 1 {
		t.Error("subcomponents dropped")
	}
}

func TestParseAndValidate_MalformedJSON(t *testing.T) {
	if _, _, err := ParseAndValidate([]byte(`{ not json`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestParseAndValidate_SurfacesStructuralIssues(t *testing.T) {
	data := []byte(`{"@context":"https://openvex.dev/ns/v0.2.0","version":1,
		"statements":[{"vulnerability":{"name":"CVE-2025-0001"},"status":"bogus"}]}`)
	_, issues, err := ParseAndValidate(data)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !hasIssue(issues, "invalid status") {
		t.Errorf("expected invalid status issue, got %v", issues)
	}
}

func TestValidate_ProductMissingID(t *testing.T) {
	d := Document{
		Context: contextURL,
		Statements: []Statement{{
			Vulnerability: Vuln{Name: "CVE-2025-0001"}, Status: StatusFixed,
			Products: []Product{{ID: ""}},
		}},
	}
	if !hasIssue(Validate(d), "missing an @id") {
		t.Error("expected product @id issue")
	}
}

func hasIssue(issues []string, substr string) bool {
	for _, s := range issues {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

func TestFirstProduct_EmptyProducts(t *testing.T) {
	s := Statement{Products: nil}
	if got := firstProduct(s); got != "" {
		t.Errorf("firstProduct with no products = %q, want empty", got)
	}
}
