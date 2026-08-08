package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/logging"
)

// ─── runSafely: background-task panic recovery (v0.107.0) ─────────

// TestRunSafely_RecoversPanicWithoutPropagating verifies a panicking task
// does not crash the caller — background tickers (audit prune, cache GC,
// rate-limiter GC, gauge updates) run for the daemon's whole lifetime;
// an unrecovered panic in any one of them currently takes the entire
// process down with it, unlike MCP tool calls which already recover
// (internal/mcp/server.go).
func TestRunSafely_RecoversPanicWithoutPropagating(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New("info", "test", "test", &buf)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped runSafely: %v", r)
		}
	}()
	runSafely(logger, "test-task", func() {
		panic("boom")
	})

	if !strings.Contains(buf.String(), "test-task") {
		t.Errorf("expected the task name in the log output, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("expected the panic value in the log output, got: %s", buf.String())
	}
}

// TestRunSafely_NonPanickingTaskRunsNormally is a regression guard: a task
// that doesn't panic must still execute exactly as if called directly.
func TestRunSafely_NonPanickingTaskRunsNormally(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New("info", "test", "test", &buf)

	called := false
	runSafely(logger, "test-task", func() {
		called = true
	})

	if !called {
		t.Error("expected the task function to run")
	}
	if strings.Contains(buf.String(), "panic") {
		t.Errorf("no panic occurred, but log mentions one: %s", buf.String())
	}
}
