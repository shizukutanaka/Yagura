package scanner

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/logging"
)

// ─── runSafely: background-scan panic recovery (v0.109.0) ─────────

// TestRunSafely_RecoversPanicWithoutPropagating verifies a panicking scan
// cycle doesn't crash the caller. Scanner.run/SecurityScanner.run are
// long-running goroutines started once at daemon boot (go s.run(ctx));
// ScanAll/runOnce make real GitHub/OSV/Scorecard API calls and parse
// external responses per project, so an unrecovered panic here is more
// likely to fire in practice than in the simpler background tasks
// cmd/yagura/safego.go already protects (gauge updates, cache/audit
// pruning) — and would take the whole daemon down with it.
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
