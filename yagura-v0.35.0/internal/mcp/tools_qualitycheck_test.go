package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shizukutanaka/yagura/internal/qualitycheck"
)

// ─── yagura_quality_check: custom_rules (v0.35) ──────────────

func TestQualityCheck_CustomRuleFlagsPattern(t *testing.T) {
	d := newDeps(t)
	tool := buildQualityCheckTool(d, nil)
	r := mustCall(t, tool, map[string]any{
		"text":     `console.log("debug");`,
		"language": "ts",
		"custom_rules": []qualitycheck.RuleSpec{{
			ID:       "no-console-log",
			Pattern:  `console\.log`,
			Severity: qualitycheck.SevProhibited,
		}},
	}).(map[string]any)

	byRule := r["by_rule"].(map[string]int)
	if byRule["no-console-log"] != 1 {
		t.Errorf("custom rule should flag 1 console.log, got %d (by_rule=%v)", byRule["no-console-log"], byRule)
	}
	if !r["has_prohibited"].(bool) {
		t.Error("expected has_prohibited=true for a prohibited custom rule")
	}
}

func TestQualityCheck_DefaultRulesStillApplyWithCustom(t *testing.T) {
	d := newDeps(t)
	tool := buildQualityCheckTool(d, nil)
	r := mustCall(t, tool, map[string]any{
		"text":     "const x = y as any; // TODO\nconsole.log(1);",
		"language": "ts",
		"custom_rules": []qualitycheck.RuleSpec{{
			ID:      "no-console-log",
			Pattern: `console\.log`,
		}},
	}).(map[string]any)

	byRule := r["by_rule"].(map[string]int)
	// built-in ts-as-any + todo must still fire alongside the custom rule
	if byRule["ts-as-any"] != 1 || byRule["todo"] != 1 || byRule["no-console-log"] != 1 {
		t.Errorf("expected built-in and custom rules together, got %v", byRule)
	}
}

func TestQualityCheck_InvalidCustomRuleRejected(t *testing.T) {
	d := newDeps(t)
	tool := buildQualityCheckTool(d, nil)
	b, _ := json.Marshal(map[string]any{
		"text": "x",
		"custom_rules": []qualitycheck.RuleSpec{{
			ID:      "bad",
			Pattern: `[unclosed`,
		}},
	})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input error for bad regex, got %v", err)
	}
}

func TestQualityCheck_TooManyCustomRulesRejected(t *testing.T) {
	d := newDeps(t)
	tool := buildQualityCheckTool(d, nil)
	specs := make([]qualitycheck.RuleSpec, 201)
	for i := range specs {
		specs[i] = qualitycheck.RuleSpec{ID: "r", Pattern: "x"}
	}
	b, _ := json.Marshal(map[string]any{"text": "x", "custom_rules": specs})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input for >200 custom_rules, got %v", err)
	}
}
