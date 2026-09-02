package agentmd

import (
	"strings"
	"testing"
	"time"
)

func basicFacts() ProjectFacts {
	return ProjectFacts{
		Slug:        "breeze",
		DisplayName: "Breeze",
		Repository:  "shizukutanaka/breeze",
		Language:    "javascript",
		Stage:       "production",
		LocalPath:   "/home/m/breeze",
		Description: "Serverless P2P E2E encrypted messenger.",
		Scope:       "- IN: messaging, presence, file transfer\n- OUT: phone calls",
		Phases:      []string{"v11 hardening", "v12 mobile UX"},
		DoD:         []string{"All 2,150 tests green", "No critical CVEs"},
		DependsOn:   []string{"cotton", "tessera"},
		HasCI:       true,
		HasTests:    true,
		CIStatus:    "passing",
		Tags:        []string{"messaging", "encryption"},
		GeneratedAt: time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
		GeneratedBy: "yagura 0.32.0",
	}
}

func TestGenerate_IncludesTitle(t *testing.T) {
	out := Generate(basicFacts())
	if !strings.Contains(out, "# AGENTS.md — Breeze") {
		t.Errorf("missing title heading:\n%s", out[:200])
	}
}

func TestGenerate_FallsBackToSlugWhenNoDisplayName(t *testing.T) {
	f := basicFacts()
	f.DisplayName = ""
	out := Generate(f)
	if !strings.Contains(out, "# AGENTS.md — breeze") {
		t.Errorf("should use slug: %s", out[:200])
	}
}

func TestGenerate_FallsBackToProjectWhenNoSlugOrName(t *testing.T) {
	f := basicFacts()
	f.Slug = ""
	f.DisplayName = ""
	out := Generate(f)
	if !strings.Contains(out, "# AGENTS.md — Project") {
		t.Errorf("missing fallback title")
	}
}

func TestGenerate_PurposeOmittedWhenEmpty(t *testing.T) {
	f := basicFacts()
	f.Description = ""
	out := Generate(f)
	if strings.Contains(out, "## Purpose") {
		t.Errorf("Purpose section should be omitted")
	}
}

func TestGenerate_QuickFactsContainsRepository(t *testing.T) {
	out := Generate(basicFacts())
	if !strings.Contains(out, "shizukutanaka/breeze") {
		t.Error("missing repository")
	}
	if !strings.Contains(out, "**Repository:**") {
		t.Error("missing repository label")
	}
}

func TestGenerate_QuickFactsOmittedWhenAllEmpty(t *testing.T) {
	f := ProjectFacts{Slug: "x"}
	out := Generate(f)
	if strings.Contains(out, "## Quick facts") {
		t.Errorf("Quick facts should be skipped")
	}
}

func TestGenerate_PhasesAsList(t *testing.T) {
	out := Generate(basicFacts())
	if !strings.Contains(out, "## Phases") {
		t.Error("missing Phases header")
	}
	if !strings.Contains(out, "- v11 hardening") {
		t.Errorf("missing phase item")
	}
}

func TestGenerate_DependsOnSorted(t *testing.T) {
	f := basicFacts()
	f.DependsOn = []string{"zfoo", "alpha", "mu"}
	out := Generate(f)
	posA := strings.Index(out, "`alpha`")
	posM := strings.Index(out, "`mu`")
	posZ := strings.Index(out, "`zfoo`")
	if !(posA < posM && posM < posZ) {
		t.Errorf("DependsOn not sorted alphabetically:\n%s", out)
	}
}

func TestGenerate_DefaultRulesUsedWhenNoneSupplied(t *testing.T) {
	out := Generate(basicFacts())
	if !strings.Contains(out, "### Testing") {
		t.Error("default Testing rule missing")
	}
	if !strings.Contains(out, "Reproducibility") {
		t.Error("default Reproducibility rule missing")
	}
	if !strings.Contains(out, "AI-generated code") {
		t.Error("default AI code rule missing")
	}
}

func TestGenerate_CustomRulesOverrideDefaults(t *testing.T) {
	f := basicFacts()
	f.HarnessRules = []HarnessRule{
		{Topic: "Custom", Rule: "Only ship on Tuesdays."},
	}
	out := Generate(f)
	if !strings.Contains(out, "Only ship on Tuesdays.") {
		t.Error("custom rule missing")
	}
	if strings.Contains(out, "Testing") && !strings.Contains(out, "Only ship") {
		t.Error("custom rules should override defaults entirely")
	}
}

func TestGenerate_RulesGroupedByTopic(t *testing.T) {
	f := basicFacts()
	f.HarnessRules = []HarnessRule{
		{Topic: "A", Rule: "rule a1"},
		{Topic: "B", Rule: "rule b1"},
		{Topic: "A", Rule: "rule a2"},
	}
	out := Generate(f)
	// rule a1 and a2 should both be under ### A
	posA := strings.Index(out, "### A")
	posB := strings.Index(out, "### B")
	a1 := strings.Index(out, "rule a1")
	a2 := strings.Index(out, "rule a2")
	b1 := strings.Index(out, "rule b1")
	if !(posA < a1 && a1 < a2 && a2 < posB && posB < b1) {
		t.Errorf("topics not grouped correctly:\nA at %d, a1 at %d, a2 at %d, B at %d, b1 at %d",
			posA, a1, a2, posB, b1)
	}
}

func TestGenerate_SensorsAppearOnlyWhenNonZero(t *testing.T) {
	f := basicFacts()
	out := Generate(f)
	if strings.Contains(out, "## Current sensor state") {
		t.Errorf("should be omitted when all sensors zero")
	}
	f.VulnCritical = 2
	out2 := Generate(f)
	if !strings.Contains(out2, "## Current sensor state") {
		t.Errorf("should appear when vuln_critical > 0")
	}
	if !strings.Contains(out2, "CRITICAL vulnerabilities:** 2") {
		t.Errorf("wrong critical count: %s", out2)
	}
}

func TestGenerate_ProvenanceFooterAlwaysIncluded(t *testing.T) {
	out := Generate(basicFacts())
	if !strings.Contains(out, "## Provenance") {
		t.Error("Provenance footer missing")
	}
	if !strings.Contains(out, "yagura 0.32.0") {
		t.Error("GeneratedBy missing")
	}
	if !strings.Contains(out, "2026-05-13T12:00:00Z") {
		t.Errorf("GeneratedAt missing: %s", out)
	}
}

func TestGenerate_Deterministic(t *testing.T) {
	a := Generate(basicFacts())
	b := Generate(basicFacts())
	if a != b {
		t.Error("Generate should be deterministic for the same input")
	}
}

func TestGenerate_EmptyInputProducesValidStub(t *testing.T) {
	out := Generate(ProjectFacts{})
	if !strings.Contains(out, "# AGENTS.md") {
		t.Error("title missing")
	}
	if !strings.Contains(out, "## House rules") {
		t.Error("house rules should still appear")
	}
	if !strings.Contains(out, "## Provenance") {
		t.Error("Provenance should still appear")
	}
}

func TestGenerate_NoFillerForMissingSections(t *testing.T) {
	out := Generate(ProjectFacts{Slug: "x"})
	// We don't want placeholders like "TBD" or "TODO".
	if strings.Contains(out, "TBD") || strings.Contains(out, "TODO") {
		t.Errorf("output should not contain TBD/TODO placeholders:\n%s", out)
	}
}

func TestGenerate_AgentReaderHintInHeader(t *testing.T) {
	out := Generate(basicFacts())
	if !strings.Contains(out, "If you are an agent") {
		t.Error("agent-reader hint missing from header")
	}
}

func TestGenerate_TagsRendered(t *testing.T) {
	out := Generate(basicFacts())
	if !strings.Contains(out, "messaging, encryption") {
		t.Error("tags not rendered")
	}
}
