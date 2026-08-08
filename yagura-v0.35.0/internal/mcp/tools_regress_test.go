package mcp

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/regress"
)

// TestRegress_ThresholdsOverrideAppliesCalibratedGate verifies the optional
// `thresholds` param on yagura_regress (v0.104.0) lets a client flip Crossed
// using a calibrated value, mirroring the CLI's .yagura/thresholds.json
// auto-detection (MCP tools take explicit params instead of doing their own
// filesystem detection, per the established content-based contract).
func TestRegress_ThresholdsOverrideAppliesCalibratedGate(t *testing.T) {
	d := newDeps(t)
	tool := buildRegressTool(d)
	r := mustCall(t, tool, map[string]any{
		"old": map[string]string{"x.go": "package p\nfunc F(a int) {}\n"},
		"new": map[string]string{"x.go": "package p\nfunc F(a, b int) {}\n"},
		"thresholds": map[string]any{
			"params": 1,
		},
	})
	rep, ok := r.(regress.Report)
	if !ok {
		t.Fatalf("expected regress.Report, got %T", r)
	}
	if rep.Crossed != 1 {
		t.Errorf("expected calibrated threshold to flip Crossed to 1, got %d", rep.Crossed)
	}
}

func TestRegress_OmittedThresholdsMatchesPriorBehavior(t *testing.T) {
	d := newDeps(t)
	tool := buildRegressTool(d)
	r := mustCall(t, tool, map[string]any{
		"old": map[string]string{"x.go": "package p\nfunc F(a int) {}\n"},
		"new": map[string]string{"x.go": "package p\nfunc F(a, b int) {}\n"},
	})
	rep, ok := r.(regress.Report)
	if !ok {
		t.Fatalf("expected regress.Report, got %T", r)
	}
	if rep.Crossed != 0 {
		t.Errorf("without thresholds override, 2 params stays under the conventional gate (5): got Crossed=%d", rep.Crossed)
	}
}
