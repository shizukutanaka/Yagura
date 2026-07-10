package scanner

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// runSafely invokes fn and recovers any panic, logging it instead of
// letting it crash the whole daemon (v0.109.0). Scanner.run and
// SecurityScanner.run are long-running goroutines started once at daemon
// boot; ScanAll/runOnce make real GitHub/OSV/Scorecard API calls and parse
// external responses per project — an unrecovered panic here takes down
// MCP/dashboard/HTTP API along with it, exactly the failure mode
// cmd/yagura/safego.go's runSafely already closed for the daemon's other
// background tickers (gauge updates, cache/audit pruning, rate-limiter
// GC). Duplicated here rather than imported because cmd/yagura depends on
// internal/scanner, not the other way around.
func runSafely(logger *slog.Logger, task string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("background task panic",
				"task", task,
				"panic", fmt.Sprint(r),
				"stack", string(debug.Stack()))
		}
	}()
	fn()
}
