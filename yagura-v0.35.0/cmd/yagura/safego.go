package main

import (
	"fmt"
	"log/slog"
	"runtime/debug"
)

// runSafely invokes fn and recovers any panic, logging it instead of
// letting it crash the whole daemon (v0.107.0). Several long-running
// background tickers — portfolio gauge updates, audit-log pruning,
// disk-cache pruning, rate-limiter GC — run for the entire life of the
// process; an unrecovered panic in any one of them currently takes down
// MCP/dashboard/HTTP API along with it, a blast radius wildly out of
// proportion to a bug in a low-value maintenance task. Mirrors the panic
// recovery already applied to MCP tool calls (internal/mcp/server.go).
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
