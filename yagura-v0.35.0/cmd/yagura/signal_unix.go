//go:build !windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals returns the signals that should trigger graceful shutdown
// on this platform. Unix: SIGINT (Ctrl+C) and SIGTERM (kill, systemd stop).
func shutdownSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}
