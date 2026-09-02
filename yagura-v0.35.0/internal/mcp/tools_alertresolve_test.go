package mcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/shizukutanaka/yagura/internal/alertfix"
)

func newAlertStore(t *testing.T) *alertfix.Store {
	t.Helper()
	store, err := alertfix.NewStore(filepath.Join(t.TempDir(), "alert_state.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func callAlert(t *testing.T, store *alertfix.Store, args map[string]any) (map[string]any, error) {
	t.Helper()
	tool := buildAlertResolveTool(store)
	b, _ := json.Marshal(args)
	out, err := tool.Handler(context.Background(), b)
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

func TestAlertResolve_Resolve(t *testing.T) {
	store := newAlertStore(t)
	r, err := callAlert(t, store, map[string]any{"alert_id": "a1", "action": "resolve", "note": "fixed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r["action"] != "resolve" {
		t.Errorf("action = %v", r["action"])
	}
	st := r["current_state"].(*alertfix.CurrentState)
	if st.Status != alertfix.StatusResolved {
		t.Errorf("status = %q, want resolved", st.Status)
	}
}

func TestAlertResolve_SnoozeDefaultsToSevenDays(t *testing.T) {
	store := newAlertStore(t)
	r, err := callAlert(t, store, map[string]any{"alert_id": "a2", "action": "snooze"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	st := r["current_state"].(*alertfix.CurrentState)
	if st.Status != alertfix.StatusSnoozed {
		t.Errorf("status = %q, want snoozed", st.Status)
	}
	if st.SnoozeUntil == nil {
		t.Error("snooze should set a snooze_until deadline (default 7 days)")
	}
}

func TestAlertResolve_Reopen(t *testing.T) {
	store := newAlertStore(t)
	// resolve, then reopen → back to active
	if _, err := callAlert(t, store, map[string]any{"alert_id": "a3", "action": "resolve"}); err != nil {
		t.Fatal(err)
	}
	r, err := callAlert(t, store, map[string]any{"alert_id": "a3", "action": "reopen"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	st := r["current_state"].(*alertfix.CurrentState)
	if st.Status != alertfix.StatusActive {
		t.Errorf("status after reopen = %q, want active", st.Status)
	}
}

func TestAlertResolve_NilStoreUnavailable(t *testing.T) {
	tool := buildAlertResolveTool(nil)
	b, _ := json.Marshal(map[string]any{"alert_id": "x", "action": "resolve"})
	_, err := tool.Handler(context.Background(), b)
	if !IsCode(err, "unavailable") {
		t.Errorf("expected unavailable when store is nil, got %v", err)
	}
}

func TestAlertResolve_InvalidInputs(t *testing.T) {
	store := newAlertStore(t)
	tool := buildAlertResolveTool(store)
	cases := []struct {
		name string
		args string
	}{
		{"malformed json", `{`},
		{"missing alert_id", `{"action":"resolve"}`},
		{"unknown action", `{"alert_id":"a","action":"frobnicate"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := tool.Handler(context.Background(), json.RawMessage(c.args))
			if !IsCode(err, "invalid_input") {
				t.Errorf("expected invalid_input, got %v", err)
			}
		})
	}
}
