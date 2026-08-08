package alertfix

import (
	"fmt"
	"strings"
	"testing"
)

func riskyFiles(n int) []ProcessRiskFile {
	out := make([]ProcessRiskFile, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ProcessRiskFile{
			Path:          fmt.Sprintf("pkg/file%03d.go", i),
			Score:         0.99,
			RelativeChurn: 5.0, // above ProcessRiskChurnMin → fires
			Ownership:     1.0,
			HasOwnership:  true,
			Reasons:       []string{"relative churn in the top 1% of this repo (5.00)", "changed 40 times"},
		})
	}
	return out
}

// TestProcessRisk_VolumeIsCapped is the central research-grounded guard.
// Sadowski et al. (CACM 2018, "Lessons from Building Static Analysis Tools at
// Google") report that an automated bug-filing approach failed with 84% of
// filed bugs never fixed, and that an issue counts as an "effective false
// positive" whenever the developer takes no action. Emitting one alert per
// risky file would recreate exactly that failure, so the source is capped.
func TestProcessRisk_VolumeIsCapped(t *testing.T) {
	alerts := EvaluateProcessRisk("proj", riskyFiles(100), DefaultThresholds())
	if len(alerts) > MaxProcessAlertsPerProject {
		t.Errorf("emitted %d alerts for 100 risky files; cap is %d (alert fatigue guard)",
			len(alerts), MaxProcessAlertsPerProject)
	}
	if len(alerts) == 0 {
		t.Fatal("100 high-risk files should still produce at least one alert")
	}
}

// TestProcessRisk_EveryAlertIsActionable: Sadowski et al. stress that an alert
// a developer cannot act on is an effective false positive even when correct.
// Every emitted alert must therefore carry a concrete recommendation and an
// explanation of why it fired.
func TestProcessRisk_EveryAlertIsActionable(t *testing.T) {
	alerts := EvaluateProcessRisk("proj", riskyFiles(5), DefaultThresholds())
	for _, a := range alerts {
		if strings.TrimSpace(a.Recommendation) == "" {
			t.Errorf("alert %s has no recommendation (not actionable)", a.ID)
		}
		if strings.TrimSpace(a.Description) == "" {
			t.Errorf("alert %s has no description (not understandable)", a.ID)
		}
		// the description must surface the evidence, not just a score
		if !strings.Contains(a.Description, "churn") {
			t.Errorf("alert %s description lacks the evidence that triggered it: %q", a.ID, a.Description)
		}
		if a.SuggestedTool == "" {
			t.Errorf("alert %s suggests no tool to investigate with", a.ID)
		}
	}
}

// TestProcessRisk_BelowThresholdIsSilent: a calm repository must produce no
// process alerts at all. Crying wolf on healthy code is the fastest way to
// train developers to ignore the source.
func TestProcessRisk_BelowThresholdIsSilent(t *testing.T) {
	calm := []ProcessRiskFile{
		{Path: "a.go", Score: 0.10, RelativeChurn: 0.05, Ownership: 1.0, HasOwnership: true},
		{Path: "b.go", Score: 0.42, RelativeChurn: 0.30, Ownership: 0.9, HasOwnership: true},
	}
	if got := EvaluateProcessRisk("proj", calm, DefaultThresholds()); len(got) != 0 {
		t.Errorf("calm files must not alert, got %d: %+v", len(got), got)
	}
}

// TestProcessRisk_HighestRiskFirst keeps the cap meaningful: if only N alerts
// survive, they must be the N riskiest.
func TestProcessRisk_HighestRiskFirst(t *testing.T) {
	files := []ProcessRiskFile{
		{Path: "low.go", Score: 0.86, RelativeChurn: 1.2, Reasons: []string{"churn"}},
		{Path: "worst.go", Score: 0.99, RelativeChurn: 4.0, Reasons: []string{"churn"}},
		{Path: "mid.go", Score: 0.92, RelativeChurn: 2.0, Reasons: []string{"churn"}},
	}
	alerts := EvaluateProcessRisk("proj", files, DefaultThresholds())
	if len(alerts) == 0 {
		t.Fatal("expected alerts")
	}
	if !strings.Contains(alerts[0].Title, "worst.go") {
		t.Errorf("highest-risk file must come first, got %q", alerts[0].Title)
	}
}

// TestProcessRisk_StableIDs is required for the resolve/snooze lifecycle: the
// same finding across two runs must carry the same ID or it would be nagged
// again after being dismissed.
func TestProcessRisk_StableIDs(t *testing.T) {
	files := riskyFiles(3)
	a := EvaluateProcessRisk("proj", files, DefaultThresholds())
	b := EvaluateProcessRisk("proj", files, DefaultThresholds())
	if len(a) != len(b) {
		t.Fatalf("nondeterministic alert count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Errorf("unstable ID at %d: %q vs %q", i, a[i].ID, b[i].ID)
		}
	}
	// distinct files must not collide onto one ID
	seen := map[string]bool{}
	for _, x := range a {
		if seen[x.ID] {
			t.Errorf("duplicate alert ID %q", x.ID)
		}
		seen[x.ID] = true
	}
}

// TestProcessRisk_SourceIsDistinct keeps process risk separable from the
// external-sensor sources so a caller can filter it.
func TestProcessRisk_SourceIsDistinct(t *testing.T) {
	alerts := EvaluateProcessRisk("proj", riskyFiles(2), DefaultThresholds())
	for _, a := range alerts {
		if a.Source != SourceProcessRisk {
			t.Errorf("source = %q, want %q", a.Source, SourceProcessRisk)
		}
		if a.Project != "proj" {
			t.Errorf("project = %q", a.Project)
		}
	}
}

func TestProcessRisk_EmptyInput(t *testing.T) {
	if got := EvaluateProcessRisk("proj", nil, DefaultThresholds()); len(got) != 0 {
		t.Errorf("nil input must give no alerts, got %+v", got)
	}
}

// TestProcessRisk_FiresOnRealisticScores is the regression guard for the defect
// dogfooding exposed: the original design gated on the composite score (>=0.85),
// but that score is a mean of within-repo percentiles and topped out at 0.695 on
// a real repository, so the source could never fire. Firing now depends on
// interpretable absolute conditions, and this test pins that a realistic
// score with high churn still alerts.
func TestProcessRisk_FiresOnRealisticScores(t *testing.T) {
	realistic := []ProcessRiskFile{
		{Path: "internal/mcp/tools.go", Score: 0.693, RelativeChurn: 6.34,
			Ownership: 1.0, HasOwnership: true, Reasons: []string{"relative churn in the top 1% of this repo (6.34)"}},
	}
	alerts := EvaluateProcessRisk("proj", realistic, DefaultThresholds())
	if len(alerts) != 1 {
		t.Fatalf("a file rewritten 6x its own size must alert even at score 0.693; got %d", len(alerts))
	}
}

// TestProcessRisk_OwnershipAloneCanFire covers the Bird et al. condition
// independently of churn.
func TestProcessRisk_OwnershipAloneCanFire(t *testing.T) {
	shared := []ProcessRiskFile{
		{Path: "shared.go", Score: 0.5, RelativeChurn: 0.1, Ownership: 0.2, HasOwnership: true},
	}
	if got := EvaluateProcessRisk("proj", shared, DefaultThresholds()); len(got) != 1 {
		t.Fatalf("a file with no clear owner must alert on the Bird condition; got %d", len(got))
	}
}
