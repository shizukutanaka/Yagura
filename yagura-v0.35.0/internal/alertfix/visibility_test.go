package alertfix

import (
	"testing"
	"time"
)

// Spec for the repository-visibility-mismatch rule (visibility literacy):
// a project whose human-declared sensitivity tag says "internal/confidential/
// private/secret" but whose repo is observed Public should raise an alert.
// Declared intent (tag, manual metadata) vs observed reality (RepoPublic,
// scanner-only sensor) — the mismatch is the signal.

func visibilitySnap(public bool, tags ...string) ProjectSnapshot {
	return ProjectSnapshot{
		Slug:           "secretproj",
		CIStatus:       "passing",
		LatestActivity: fixedNow().Add(-1 * 24 * time.Hour), // recent → no stale alert
		RepoPublic:     public,
		Tags:           tags,
	}
}

func findSource(as []Alert, src Source) (Alert, bool) {
	for _, a := range as {
		if a.Source == src {
			return a, true
		}
	}
	return Alert{}, false
}

func TestEvaluate_PublicRepoWithSensitiveTag_Alerts(t *testing.T) {
	for _, tag := range []string{"internal", "confidential", "private", "secret"} {
		t.Run(tag, func(t *testing.T) {
			as := Evaluate(visibilitySnap(true, tag), thresholds())
			a, ok := findSource(as, SourceVisibility)
			if !ok {
				t.Fatalf("expected a visibility alert for tag %q, got %v", tag, as)
			}
			if a.Severity != SevHigh {
				t.Errorf("visibility alert should be HIGH, got %s", a.Severity)
			}
			if a.Project != "secretproj" {
				t.Errorf("alert project = %q", a.Project)
			}
		})
	}
}

func TestEvaluate_PublicRepoSensitiveTag_CaseInsensitive(t *testing.T) {
	as := Evaluate(visibilitySnap(true, "Confidential"), thresholds())
	if _, ok := findSource(as, SourceVisibility); !ok {
		t.Errorf("tag match should be case-insensitive; got %v", as)
	}
}

func TestEvaluate_PublicRepoWithoutSensitiveTag_NoAlert(t *testing.T) {
	as := Evaluate(visibilitySnap(true, "docs", "go"), thresholds())
	if _, ok := findSource(as, SourceVisibility); ok {
		t.Errorf("public repo without a sensitivity tag must not alert; got %v", as)
	}
}

func TestEvaluate_PrivateRepoWithSensitiveTag_NoAlert(t *testing.T) {
	as := Evaluate(visibilitySnap(false, "internal"), thresholds())
	if _, ok := findSource(as, SourceVisibility); ok {
		t.Errorf("private repo is the correct state; must not alert; got %v", as)
	}
}
