package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shizukutanaka/yagura/internal/audit"
)

func TestSelfImproveTool(t *testing.T) {
	tool := buildSelfImproveTool(Deps{}, nil, nil)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{
		"session_calls": 100,
		"tools": [{"name":"yagura_x","calls":20,"errors":8,"avg_resp_bytes":200}],
		"skills": [{"path":"a/SKILL.md","score":20,"retire":true}],
		"coverage_gaps": ["feedback:runtime"]
	}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	b, _ := json.Marshal(out)
	var r struct {
		Proposals []struct {
			Kind     string `json:"kind"`
			Severity string `json:"severity"`
		} `json:"proposals"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(r.Proposals) < 3 {
		t.Errorf("expected reliability+retire+coverage proposals, got %d: %s", len(r.Proposals), b)
	}
	if r.Proposals[0].Severity != "high" {
		t.Errorf("highest-severity proposal should rank first, got %s", r.Proposals[0].Severity)
	}
	if r.Summary == "" {
		t.Error("summary should be set")
	}
}

func TestSelfImproveTool_Empty(t *testing.T) {
	tool := buildSelfImproveTool(Deps{}, nil, nil)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	b, _ := json.Marshal(out)
	if !json.Valid(b) {
		t.Fatal("output not valid json")
	}
}

func TestSelfImproveTool_BadInput(t *testing.T) {
	tool := buildSelfImproveTool(Deps{}, nil, nil)
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{`)); err == nil {
		t.Error("expected error for malformed JSON")
	}
}

// 'tools' 省略時に live stats を自己収集し、それを評価することを確認する
// (RSI ループが実際に閉じていること)。
func TestSelfImproveTool_SelfCollectsLiveStats(t *testing.T) {
	stats := func() []ToolStats {
		return []ToolStats{
			{Name: "yagura_flaky", Calls: 20, ResponseBytes: 4000, ErrorCount: 6}, // 30% err → high reliability
			{Name: "yagura_ok", Calls: 50, ResponseBytes: 5000, ErrorCount: 0},
		}
	}
	tool := buildSelfImproveTool(Deps{}, stats, nil)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	m := out.(map[string]any)
	if m["self_collected"] != true {
		t.Errorf("expected self_collected=true, got %v", m["self_collected"])
	}
	b, _ := json.Marshal(m)
	var r struct {
		Proposals []struct {
			ID string `json:"id"`
		} `json:"proposals"`
	}
	_ = json.Unmarshal(b, &r)
	found := false
	for _, p := range r.Proposals {
		if p.ID == "reliability:yagura_flaky" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a reliability proposal for the flaky live tool, got %s", b)
	}

	// caller-supplied tools must override self-collection.
	out2, _ := tool.Handler(context.Background(), json.RawMessage(`{"tools":[{"name":"x","calls":1,"errors":0}]}`))
	if out2.(map[string]any)["self_collected"] != false {
		t.Error("explicit tools should disable self-collection")
	}
}

// record=true で自己評価が audit sink に 1 record 残ること、false/未指定では残さないこと
// (misevolution 対策の「memories auditable」)を確認する。
func TestSelfImproveTool_RecordsToAudit(t *testing.T) {
	var captured []audit.Record
	emit := func(r audit.Record) { captured = append(captured, r) }
	tool := buildSelfImproveTool(Deps{}, nil, emit)

	// record なし → 監査に残さない
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"tools":[{"name":"a","calls":10,"errors":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["recorded"] != false {
		t.Error("recorded should be false when record is absent")
	}
	if len(captured) != 0 {
		t.Errorf("no audit record expected, got %d", len(captured))
	}

	// record=true → 監査に 1 record(Kind=self_improve, Fields に by_severity / proposals)
	out, err = tool.Handler(context.Background(), json.RawMessage(`{"record":true,"tools":[{"name":"a","calls":10,"errors":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["recorded"] != true {
		t.Error("recorded should be true when record=true and a sink is set")
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 audit record, got %d", len(captured))
	}
	rec := captured[0]
	if rec.Kind != "self_improve" || rec.Actor != "mcp" || rec.Target != "harness" {
		t.Errorf("unexpected audit record envelope: %+v", rec)
	}
	if _, ok := rec.Fields["by_severity"]; !ok {
		t.Errorf("audit record missing by_severity: %+v", rec.Fields)
	}
	if _, ok := rec.Fields["proposals"]; !ok {
		t.Errorf("audit record missing proposals: %+v", rec.Fields)
	}
}

// emit が nil でも record=true で panic しないこと。
func TestSelfImproveTool_RecordNilSinkSafe(t *testing.T) {
	tool := buildSelfImproveTool(Deps{}, nil, nil)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"record":true,"tools":[{"name":"a","calls":10,"errors":3}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["recorded"] != false {
		t.Error("recorded should be false when no sink is configured")
	}
}
