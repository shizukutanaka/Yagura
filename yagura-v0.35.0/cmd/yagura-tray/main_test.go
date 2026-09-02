package main

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestResolveDaemonPath_FlagWins(t *testing.T) {
	got := resolveDaemonPath("/custom/path/yagura")
	if got != "/custom/path/yagura" {
		t.Errorf("flag should win: got %s", got)
	}
}

func TestResolveDaemonPath_SiblingFallback(t *testing.T) {
	// Create a dummy 'yagura' file next to the test binary
	exe, err := os.Executable()
	if err != nil {
		t.Skip("os.Executable not available")
	}
	dir := filepath.Dir(exe)
	name := "yagura"
	if runtime.GOOS == "windows" {
		name = "yagura.exe"
	}
	dummy := filepath.Join(dir, name)
	// Only run if we can create the file (might fail in some CI)
	if err := os.WriteFile(dummy, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Skipf("cannot write to test exe dir: %v", err)
	}
	defer os.Remove(dummy)

	got := resolveDaemonPath("")
	if got != dummy {
		t.Errorf("sibling lookup failed: got %s, want %s", got, dummy)
	}
}

func TestResolveStateDir_FlagWins(t *testing.T) {
	got := resolveStateDir("/explicit/state")
	if got != "/explicit/state" {
		t.Errorf("flag should win: got %s", got)
	}
}

func TestResolveStateDir_OSSpecificDefault(t *testing.T) {
	// Just make sure we get *some* path, not empty
	got := resolveStateDir("")
	if got == "" {
		t.Error("state dir should not be empty")
	}
}

func TestWaitForReady_TimesOut(t *testing.T) {
	// Use a closed port that's likely unbound (high random port)
	if waitForReady("127.0.0.1:1", 500*time.Millisecond) {
		t.Error("should have timed out")
	}
}

func TestWaitForReady_Succeeds(t *testing.T) {
	// Bring up an actual listener
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	if !waitForReady(addr, 1*time.Second) {
		t.Errorf("should have succeeded against %s", addr)
	}
}

func TestDaemon_StartStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix shell-style command")
	}
	// Use a script that sleeps; Stop() should SIGTERM it.
	dir := t.TempDir()
	script := filepath.Join(dir, "fakeyagura")
	_ = os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	d := &daemon{path: script, addr: "127.0.0.1:1", stateDir: t.TempDir()}
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if d.cmd == nil || d.cmd.Process == nil {
		t.Fatal("process not started")
	}
	pid := d.cmd.Process.Pid

	// Confirm the PID is alive before Stop()
	if err := d.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("process %d not alive after Start: %v", pid, err)
	}

	// Stop blocks until process is reaped (Wait completes)
	d.Stop()

	// After Stop returns, sending signal 0 should fail with ESRCH / process exited.
	if err := d.cmd.Process.Signal(syscall.Signal(0)); err == nil {
		t.Errorf("process %d still alive after Stop", pid)
	}
}

func TestAppArgs(t *testing.T) {
	args := appArgs("http://127.0.0.1:8090/dashboard")
	if len(args) == 0 || args[0] != "--app=http://127.0.0.1:8090/dashboard" {
		t.Errorf("appArgs first should be --app=URL, got %v", args)
	}
	var hasNoFirstRun bool
	for _, a := range args {
		if a == "--no-first-run" {
			hasNoFirstRun = true
		}
	}
	if !hasNoFirstRun {
		t.Error("expected --no-first-run for an unobtrusive app window")
	}
}

func TestChromiumCandidates_NonEmpty(t *testing.T) {
	if len(chromiumCandidates()) == 0 {
		t.Error("expected at least one chromium candidate for this OS")
	}
}
