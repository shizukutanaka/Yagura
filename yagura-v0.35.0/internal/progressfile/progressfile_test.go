package progressfile

import (
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
}

func basic() Snapshot {
	return Snapshot{
		Project:         "breeze",
		GeneratedAt:     fixedNow(),
		GeneratedBy:     "yagura 0.33.0",
		TotalFeatures:   10,
		DoneFeatures:    3,
		PendingFeatures: []string{"v2 mobile UX", "v3 search", "v4 sync", "v5 backup", "v6 i18n", "v7 stretch"},
		PlanProgressPct: 30,
		CurrentPhase:    "v11 hardening",
		HookSessions:    7,
		ToolErrorCount:  2,
		TopTools: []ToolUse{
			{"Bash", 42},
			{"Edit", 30},
			{"Read", 18},
		},
		ActiveAlerts: []Alert{
			{ID: "a1", Severity: "high", Source: "vulns", Summary: "lodash CVE-2026-xxx"},
			{ID: "a2", Severity: "critical", Source: "secretscan", Summary: "AWS key leak"},
		},
		Note: "Yesterday I refactored the auth flow but didn't finish tests for it.",
	}
}

// ─── basic shape ─────────────────────────────────────────

func TestGenerate_IncludesHeader(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "claude-progress.txt — breeze") {
		t.Errorf("header missing:\n%s", out[:200])
	}
	if !strings.Contains(out, "2026-05-16T12:00:00Z") {
		t.Error("timestamp missing")
	}
	if !strings.Contains(out, "by yagura 0.33.0") {
		t.Error("GeneratedBy missing")
	}
}

func TestGenerate_FallsBackToUnknownProject(t *testing.T) {
	s := basic()
	s.Project = ""
	out := Generate(s)
	if !strings.Contains(out, "(unknown project)") {
		t.Error("project fallback missing")
	}
}

func TestGenerate_NowFallback(t *testing.T) {
	s := basic()
	s.GeneratedAt = time.Time{}
	out := Generate(s)
	// At least year should appear
	if !strings.Contains(out, "20") {
		t.Error("auto-now should still emit a year")
	}
}

// ─── progress section ──────────────────────────────────

func TestGenerate_FeaturesProgress(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "3 of 10 done (30%)") {
		t.Errorf("features progress incorrect:\n%s", out)
	}
}

func TestGenerate_ProgressSectionOmittedWhenEmpty(t *testing.T) {
	out := Generate(Snapshot{Project: "x", GeneratedAt: fixedNow()})
	if strings.Contains(out, "## Where you are") {
		t.Error("progress section should be omitted")
	}
}

func TestGenerate_ZeroTotalNoDiv0(t *testing.T) {
	s := basic()
	s.TotalFeatures = 0
	s.DoneFeatures = 0
	out := Generate(s)
	if strings.Contains(out, "Features:") {
		t.Error("zero total should hide features line")
	}
	// No NaN, no Inf, no panic
}

// ─── next section ──────────────────────────────────────

func TestGenerate_NextLimitedToFive(t *testing.T) {
	out := Generate(basic())
	for i := 1; i <= 5; i++ {
		if !strings.Contains(out, "v") {
			t.Errorf("feature %d missing", i)
		}
	}
	// We have 6 pending, so the "more pending" note should appear
	if !strings.Contains(out, "1 more pending") {
		t.Errorf("should mention 1 more pending:\n%s", out)
	}
}

func TestGenerate_NextOmittedWhenNone(t *testing.T) {
	s := basic()
	s.PendingFeatures = nil
	out := Generate(s)
	if strings.Contains(out, "## What's next") {
		t.Error("Next section should be omitted")
	}
}

func TestGenerate_NextNoMoreNoteWhenAllShown(t *testing.T) {
	s := basic()
	s.PendingFeatures = []string{"only-one"}
	out := Generate(s)
	if strings.Contains(out, "more pending") {
		t.Error("should not mention 'more pending' when all shown")
	}
}

// ─── activity section ──────────────────────────────────

func TestGenerate_ToolsSortedByCountDesc(t *testing.T) {
	out := Generate(basic())
	posBash := strings.Index(out, "Bash (42)")
	posEdit := strings.Index(out, "Edit (30)")
	posRead := strings.Index(out, "Read (18)")
	if !(posBash < posEdit && posEdit < posRead) {
		t.Errorf("tools not sorted by count desc:\n%s", out)
	}
}

func TestGenerate_ToolErrorWarning(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "Tool errors: 2") {
		t.Error("missing error count")
	}
	if !strings.Contains(out, "investigate before adding new work") {
		t.Error("missing investigation hint")
	}
}

func TestGenerate_ActivityOmittedWhenEmpty(t *testing.T) {
	s := basic()
	s.HookSessions = 0
	s.ToolErrorCount = 0
	s.TopTools = nil
	out := Generate(s)
	if strings.Contains(out, "## Recent activity") {
		t.Error("activity section should be omitted")
	}
}

// ─── alerts section ────────────────────────────────────

func TestGenerate_AlertsSortedBySeverity(t *testing.T) {
	out := Generate(basic())
	posCritical := strings.Index(out, "AWS key leak")
	posHigh := strings.Index(out, "lodash")
	if posCritical < 0 || posHigh < 0 {
		t.Errorf("alerts missing:\n%s", out)
	}
	if posCritical > posHigh {
		t.Errorf("CRITICAL should appear before HIGH:\n%s", out)
	}
}

func TestGenerate_AlertSeverityUppercased(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "[CRITICAL]") || !strings.Contains(out, "[HIGH]") {
		t.Error("severity label not uppercased")
	}
}

func TestGenerate_AlertsOmittedWhenEmpty(t *testing.T) {
	s := basic()
	s.ActiveAlerts = nil
	out := Generate(s)
	if strings.Contains(out, "## Active alerts") {
		t.Error("alerts section should be omitted")
	}
}

func TestSeverityRank_KnownAndUnknown(t *testing.T) {
	if severityRank("critical") >= severityRank("high") {
		t.Error("critical should rank lower (higher priority)")
	}
	if severityRank("UNKNOWN") <= severityRank("info") {
		t.Error("unknown should rank after info")
	}
	if severityRank("  CRITICAL  ") != severityRank("critical") {
		t.Error("severity rank should be trim+case insensitive")
	}
}

// ─── note + footer ─────────────────────────────────────

func TestGenerate_NoteRendered(t *testing.T) {
	out := Generate(basic())
	if !strings.Contains(out, "Yesterday I refactored the auth flow") {
		t.Error("note missing")
	}
}

func TestGenerate_NoteOmittedWhenWhitespaceOnly(t *testing.T) {
	s := basic()
	s.Note = "   \n  "
	out := Generate(s)
	if strings.Contains(out, "## Note from previous session") {
		t.Error("note section should be omitted for whitespace-only")
	}
}

func TestGenerate_FooterAlwaysPresent(t *testing.T) {
	out := Generate(Snapshot{})
	if !strings.Contains(out, "regenerated by `yagura_progress_file`") {
		t.Error("footer must always appear")
	}
	if !strings.Contains(out, "trust git history") {
		t.Error("footer authoritativeness hint missing")
	}
}

// ─── determinism ──────────────────────────────────────

func TestGenerate_Deterministic(t *testing.T) {
	a := Generate(basic())
	b := Generate(basic())
	if a != b {
		t.Error("Generate must be deterministic for the same input")
	}
}

func TestGenerate_DeterministicAlertOrder(t *testing.T) {
	s := basic()
	// Same severity → tie-break by id
	s.ActiveAlerts = []Alert{
		{ID: "z", Severity: "high", Summary: "z-alert"},
		{ID: "a", Severity: "high", Summary: "a-alert"},
	}
	out := Generate(s)
	posA := strings.Index(out, "a-alert")
	posZ := strings.Index(out, "z-alert")
	if posA > posZ {
		t.Errorf("alerts of same severity should be sorted by id:\n%s", out)
	}
}

// ─── filler hygiene ───────────────────────────────────

func TestGenerate_NoTBDPlaceholders(t *testing.T) {
	out := Generate(Snapshot{Project: "x", GeneratedAt: fixedNow()})
	if strings.Contains(out, "TBD") || strings.Contains(out, "TODO") {
		t.Errorf("empty input produced filler placeholders:\n%s", out)
	}
}

func TestPercentSafe_ZeroTotal(t *testing.T) {
	if got := percentSafe(5, 0); got != 0 {
		t.Errorf("percentSafe(5, 0) = %d, want 0 (divide-by-zero guard)", got)
	}
}
