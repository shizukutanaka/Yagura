package reviewgate

import "testing"

func TestEvaluate_CleanAllows(t *testing.T) {
	d := Evaluate(Signals{AIRiskScore: 10})
	if d.Tier != TierAllow {
		t.Errorf("clean signals should allow, got %s (%v)", d.Tier, d.Reasons)
	}
	if len(d.Reasons) == 0 {
		t.Error("decision must always carry a rationale")
	}
}

func TestEvaluate_SecretsBlock(t *testing.T) {
	d := Evaluate(Signals{SecretFindings: 1})
	if d.Tier != TierBlock {
		t.Errorf("a secret finding must block, got %s", d.Tier)
	}
	if len(d.Blockers) == 0 {
		t.Error("block tier must list blockers")
	}
}

func TestEvaluate_ProhibitedLintBlocks(t *testing.T) {
	// qualitycheck's core contract: prohibited count > 0 fails the gate.
	if d := Evaluate(Signals{LintProhibited: 2}); d.Tier != TierBlock {
		t.Errorf("prohibited lint must block, got %s", d.Tier)
	}
}

func TestEvaluate_AICriticalBlocks(t *testing.T) {
	if d := Evaluate(Signals{AICritical: 1, AIRiskScore: 25}); d.Tier != TierBlock {
		t.Errorf("a critical AI-risk finding must block, got %s", d.Tier)
	}
}

func TestEvaluate_ASTHighBlocks(t *testing.T) {
	if d := Evaluate(Signals{ASTHigh: 1}); d.Tier != TierBlock {
		t.Errorf("a high-severity AST finding must block, got %s", d.Tier)
	}
}

func TestEvaluate_RiskScoreReviews(t *testing.T) {
	d := Evaluate(Signals{AIRiskScore: 40})
	if d.Tier != TierReview {
		t.Errorf("risk score at threshold should review, got %s", d.Tier)
	}
}

func TestEvaluate_BelowThresholdAllows(t *testing.T) {
	if d := Evaluate(Signals{AIRiskScore: 39}); d.Tier != TierAllow {
		t.Errorf("risk score below threshold should allow, got %s", d.Tier)
	}
}

func TestEvaluate_MultipleBlockersAllListed(t *testing.T) {
	d := Evaluate(Signals{SecretFindings: 1, LintProhibited: 1, ASTHigh: 1})
	if d.Tier != TierBlock {
		t.Fatalf("expected block, got %s", d.Tier)
	}
	if len(d.Blockers) != 3 {
		t.Errorf("all three blockers should be listed, got %d: %v", len(d.Blockers), d.Blockers)
	}
}
