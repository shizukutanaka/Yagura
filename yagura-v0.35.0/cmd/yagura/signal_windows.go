//go:build windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals returns the signals that should trigger graceful shutdown
// on this platform.
//
// Windows: SIGINT (Ctrl+C), SIGTERM (taskkill /T graceful), and SIGBREAK
// (Ctrl+Break, sent by service-manager stop and by console close).
//
// SIGBREAK is the canonical "stop" signal for Windows console apps; without
// it, services managed by NSSM or sc.exe close ungracefully on shutdown.
func shutdownSignals() []os.Signal {
	return []os.Signal{
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.Signal(0x15), // SIGBREAK (windows.SIGBREAK alias)
	}
}
