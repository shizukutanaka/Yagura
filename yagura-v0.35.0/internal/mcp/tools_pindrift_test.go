// tools_pindrift_test.go — tests for the yagura_pin_drift handler (was 4.5% coverage).
// Uses a fake PinDriftChecker so no network is touched; covers the unavailable,
// bad-json, empty-files, no-pins note, serial/parallel dispatch, and summary_only paths.
package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shizukutanaka/yagura/internal/pindrift"
)

// fakePinDrift records which method was used and returns one OK result per pin.
type fakePinDrift struct {
	serialCalls   int
	parallelCalls int
	lastConc      int
}

func (f *fakePinDrift) results(pins []pindrift.Pin) []pindrift.Result {
	out := make([]pindrift.Result, 0, len(pins))
	for _, p := range pins {
		out = append(out, pindrift.Result{Pin: p, Status: pindrift.StatusOK, Detail: "ok"})
	}
	return out
}

func (f *fakePinDrift) CheckPins(_ context.Context, pins []pindrift.Pin) []pindrift.Result {
	f.serialCalls++
	return f.results(pins)
}

func (f *fakePinDrift) CheckPinsParallel(_ context.Context, pins []pindrift.Pin, conc int) []pindrift.Result {
	f.parallelCalls++
	f.lastConc = conc
	return f.results(pins)
}

const pinnedWorkflow = "jobs:\n  build:\n    steps:\n" +
	"      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2\n"

func depsWithPinDrift(t *testing.T, f *fakePinDrift) Deps {
	t.Helper()
	d := newDeps(t)
	d.PinDrift = f
	return d
}

func TestPinDrift_Unavailable_WhenNotConfigured(t *testing.T) {
	tool := buildPinDriftTool(newDeps(t)) // d.PinDrift is nil
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"files":{"a.yml":"x"}}`))
	if !IsCode(err, "unavailable") {
		t.Errorf("expected unavailable, got %v", err)
	}
}

func TestPinDrift_BadJSON(t *testing.T) {
	tool := buildPinDriftTool(depsWithPinDrift(t, &fakePinDrift{}))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{`))
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestPinDrift_EmptyFiles(t *testing.T) {
	tool := buildPinDriftTool(depsWithPinDrift(t, &fakePinDrift{}))
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"files":{}}`))
	if !IsCode(err, "invalid_input") {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestPinDrift_NoPinsReturnsNote(t *testing.T) {
	f := &fakePinDrift{}
	tool := buildPinDriftTool(depsWithPinDrift(t, f))
	// File with no SHA-pinned uses: → note path, no checker call.
	res := mustCall(t, tool, map[string]any{"files": map[string]string{"a.yml": "name: ci\n"}}).(map[string]any)
	if res["note"] == nil {
		t.Error("expected a note about no SHA-pinned uses")
	}
	if f.serialCalls != 0 || f.parallelCalls != 0 {
		t.Error("checker must not be called when there are no pins")
	}
}

func TestPinDrift_DefaultConcurrencyUsesParallel4(t *testing.T) {
	f := &fakePinDrift{}
	tool := buildPinDriftTool(depsWithPinDrift(t, f))
	res := mustCall(t, tool, map[string]any{"files": map[string]string{"ci.yml": pinnedWorkflow}}).(map[string]any)
	if f.parallelCalls != 1 || f.lastConc != 4 {
		t.Errorf("expected one parallel call at concurrency 4, got calls=%d conc=%d", f.parallelCalls, f.lastConc)
	}
	if res["results"] == nil || res["summary"] == nil {
		t.Error("expected results and summary in response")
	}
}

func TestPinDrift_ExplicitConcurrencyParallel(t *testing.T) {
	f := &fakePinDrift{}
	tool := buildPinDriftTool(depsWithPinDrift(t, f))
	mustCall(t, tool, map[string]any{"files": map[string]string{"ci.yml": pinnedWorkflow}, "concurrency": 8})
	if f.parallelCalls != 1 || f.lastConc != 8 {
		t.Errorf("expected parallel call at concurrency 8, got calls=%d conc=%d", f.parallelCalls, f.lastConc)
	}
}

func TestPinDrift_NegativeConcurrencySerial(t *testing.T) {
	f := &fakePinDrift{}
	tool := buildPinDriftTool(depsWithPinDrift(t, f))
	mustCall(t, tool, map[string]any{"files": map[string]string{"ci.yml": pinnedWorkflow}, "concurrency": -1})
	if f.serialCalls != 1 || f.parallelCalls != 0 {
		t.Errorf("negative concurrency must use serial path, got serial=%d parallel=%d", f.serialCalls, f.parallelCalls)
	}
}

func TestPinDrift_SummaryOnly(t *testing.T) {
	f := &fakePinDrift{}
	tool := buildPinDriftTool(depsWithPinDrift(t, f))
	res := mustCall(t, tool, map[string]any{
		"files":        map[string]string{"ci.yml": pinnedWorkflow},
		"summary_only": true,
	})
	// summary_only returns a pindrift.Summary, not the results map.
	if _, ok := res.(pindrift.Summary); !ok {
		t.Errorf("expected pindrift.Summary, got %T", res)
	}
}
