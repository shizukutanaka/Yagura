//go:build !windows
// +build !windows

// Non-Windows fallback: no tray, just run the daemon in foreground.
//
// macOS would use NSStatusItem (Cocoa, requires cgo or systray) and Linux
// requires AppIndicator (libappindicator C dep). Both violate ADR-0001's
// zero-dep policy. On these platforms, yagura-tray simply launches the
// daemon, opens the browser, and waits for Ctrl-C.
//
// Users on macOS/Linux can run the daemon directly (cmd/yagura) which is
// what they typically prefer anyway (systemd / launchd integration).
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// platformSupportsTray returns false on non-Windows.
func platformSupportsTray() bool { return false }

// runTray is a no-op fallback: blocks on signals until interrupted.
func runTray(d *daemon, addr string) {
	fmt.Printf("yagura-tray running in foreground mode (no tray on %s).\n", runtimeGOOS())
	fmt.Println("Press Ctrl-C to stop.")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

func runtimeGOOS() string {
	// Lazy import: avoid pulling runtime here directly; use known field.
	// (Actually we do need runtime in main.go, just reuse via os.Getenv hack)
	if v := os.Getenv("GOOS"); v != "" {
		return v
	}
	return "this platform"
}
