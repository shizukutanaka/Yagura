package opsrisk

import (
	"encoding/json"
	"testing"
)

func ptr(b bool) *bool { return &b }

func TestClassify_CapabilityBase(t *testing.T) {
	cases := map[string]Tier{
		"read":     TierAuto,
		"network":  TierLog,
		"write":    TierReview,
		"exec":     TierReview,
		"delete":   TierHuman,
		"auth":     TierHuman,
		"billing":  TierHuman,
		"data":     TierHuman,
		"external": TierHuman,
	}
	for cap, want := range cases {
		if got := Classify(Op{Name: "x", Capability: cap}).Tier; got != want {
			t.Errorf("capability %q → %s, want %s", cap, got, want)
		}
	}
}

func TestClassify_UnknownIsReview(t *testing.T) {
	d := Classify(Op{Name: "x", Capability: "mystery"})
	if d.Tier != TierReview {
		t.Errorf("unknown capability should default to review, got %s", d.Tier)
	}
}

func TestClassify_IrreversibleBumps(t *testing.T) {
	// write(review) + irreversible → human
	d := Classify(Op{Name: "x", Capability: "write", Reversible: ptr(false)})
	if d.Tier != TierHuman {
		t.Errorf("irreversible write should be human, got %s", d.Tier)
	}
	// read(auto) + irreversible → at least review
	d = Classify(Op{Name: "x", Capability: "read", Reversible: ptr(false)})
	if d.Tier != TierReview {
		t.Errorf("irreversible read should floor at review, got %s", d.Tier)
	}
	// reversible write stays review
	if got := Classify(Op{Name: "x", Capability: "write", Reversible: ptr(true)}).Tier; got != TierReview {
		t.Errorf("reversible write should stay review, got %s", got)
	}
}

func TestClassify_BlastRadius(t *testing.T) {
	// write + portfolio → human
	if got := Classify(Op{Name: "x", Capability: "write", BlastRadius: "portfolio"}).Tier; got != TierHuman {
		t.Errorf("portfolio write should be human, got %s", got)
	}
	// network(log) + project → review
	if got := Classify(Op{Name: "x", Capability: "network", BlastRadius: "project"}).Tier; got != TierReview {
		t.Errorf("project network should be review, got %s", got)
	}
	// read + single → auto
	if got := Classify(Op{Name: "x", Capability: "read", BlastRadius: "single"}).Tier; got != TierAuto {
		t.Errorf("single read should be auto, got %s", got)
	}
}

func TestClassify_ConsentGate(t *testing.T) {
	// write + gate → review downgrades to log
	if got := Classify(Op{Name: "x", Capability: "write", HasGate: true}).Tier; got != TierLog {
		t.Errorf("gated write should downgrade to log, got %s", got)
	}
	// delete + gate → stays human (destructive not downgradable)
	if got := Classify(Op{Name: "x", Capability: "delete", HasGate: true}).Tier; got != TierHuman {
		t.Errorf("gated delete must stay human, got %s", got)
	}
}

func TestClassify_Controls(t *testing.T) {
	human := Classify(Op{Name: "x", Capability: "billing"})
	joined := join(human.Controls)
	if !contains(joined, "human_approval") || !contains(joined, "alert") || !contains(joined, "audit_log") {
		t.Errorf("human tier controls incomplete: %v", human.Controls)
	}
	auto := Classify(Op{Name: "x", Capability: "read"})
	if join(auto.Controls) != "proceed" {
		t.Errorf("auto tier should just proceed, got %v", auto.Controls)
	}
}

func TestClassifyAll_WorstAndDeterminism(t *testing.T) {
	ops := []Op{
		{Name: "z_read", Capability: "read"},
		{Name: "a_delete", Capability: "delete"},
		{Name: "m_write", Capability: "write"},
	}
	r1 := ClassifyAll(ops)
	r2 := ClassifyAll(ops)
	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	if string(b1) != string(b2) {
		t.Error("ClassifyAll must be deterministic")
	}
	if r1.Worst != TierHuman {
		t.Errorf("worst should be human (delete present), got %s", r1.Worst)
	}
	// sorted by name
	if r1.Decisions[0].Name != "a_delete" || r1.Decisions[2].Name != "z_read" {
		t.Errorf("decisions not sorted by name: %+v", r1.Decisions)
	}
	if r1.ByTier["human"] != 1 || r1.ByTier["review"] != 1 || r1.ByTier["auto"] != 1 {
		t.Errorf("by_tier counts off: %+v", r1.ByTier)
	}
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestClassify_UnknownBlastRadius_SecureByDefault closes a fail-open: a typo'd
// or unrecognized non-empty blast radius (e.g. "portfollio") previously fell
// through the switch with no escalation, leaving the op at its capability base
// — less oversight than the caller signaled. Mirrors the unknown-capability
// rule (→ review, secure by default).
func TestClassify_UnknownBlastRadius_SecureByDefault(t *testing.T) {
	got := Classify(Op{Name: "x", Capability: "read", BlastRadius: "portfollio"}).Tier
	if rank(got) < rank(TierReview) {
		t.Errorf("unknown blast radius should escalate to >= review, got %s", got)
	}
}

// TestClassify_EmptyBlastRadius_NoEscalation guards against over-escalation:
// an unspecified blast radius carries no signal and must not raise the tier.
func TestClassify_EmptyBlastRadius_NoEscalation(t *testing.T) {
	if got := Classify(Op{Name: "x", Capability: "read"}).Tier; got != TierAuto {
		t.Errorf("empty blast radius on read should stay auto, got %s", got)
	}
}
