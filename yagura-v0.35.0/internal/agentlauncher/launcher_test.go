package agentlauncher

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// mockSpawner はテスト用 Spawner。
type mockSpawner struct {
	called   bool
	cmd      string
	args     []string
	err      error
}

func (m *mockSpawner) Start(ctx context.Context, cmd string, args ...string) error {
	m.called = true
	m.cmd = cmd
	m.args = append([]string(nil), args...)
	return m.err
}

// ─── LaunchWindsurf: OS 別コマンド ────────────────────────────

func TestLaunchWindsurf_macOS(t *testing.T) {
	mock := &mockSpawner{}
	l := &Launcher{Spawner: mock, GOOSOverride: "darwin"}
	if err := l.LaunchWindsurf(context.Background(), "/home/m/yagura"); err != nil {
		t.Fatal(err)
	}
	if mock.cmd != "open" {
		t.Errorf("cmd: got %s, want open", mock.cmd)
	}
	if len(mock.args) < 3 || mock.args[0] != "-a" || mock.args[1] != "Windsurf" {
		t.Errorf("args: got %v, want [-a Windsurf <path>]", mock.args)
	}
}

func TestLaunchWindsurf_Linux(t *testing.T) {
	mock := &mockSpawner{}
	l := &Launcher{Spawner: mock, GOOSOverride: "linux"}
	if err := l.LaunchWindsurf(context.Background(), "/home/m/yagura"); err != nil {
		t.Fatal(err)
	}
	if mock.cmd != "windsurf" {
		t.Errorf("cmd: got %s, want windsurf", mock.cmd)
	}
}

func TestLaunchWindsurf_Windows(t *testing.T) {
	mock := &mockSpawner{}
	l := &Launcher{Spawner: mock, GOOSOverride: "windows"}
	if err := l.LaunchWindsurf(context.Background(), `C:\yagura`); err != nil {
		t.Fatal(err)
	}
	if mock.cmd != "cmd" {
		t.Errorf("cmd: got %s, want cmd", mock.cmd)
	}
	if len(mock.args) < 3 || mock.args[0] != "/c" || mock.args[1] != "start" {
		t.Errorf("args: got %v, want [/c start ...]", mock.args)
	}
}

// ─── LaunchClaudeCode ─────────────────────────────────────────

func TestLaunchClaudeCode_macOS(t *testing.T) {
	mock := &mockSpawner{}
	l := &Launcher{Spawner: mock, GOOSOverride: "darwin"}
	if err := l.LaunchClaudeCode(context.Background(), "/home/m/yagura"); err != nil {
		t.Fatal(err)
	}
	if mock.cmd != "claude" || mock.args[0] != "code" {
		t.Errorf("expected claude code <path>, got %s %v", mock.cmd, mock.args)
	}
}

func TestLaunchClaudeCode_Linux(t *testing.T) {
	mock := &mockSpawner{}
	l := &Launcher{Spawner: mock, GOOSOverride: "linux"}
	_ = l.LaunchClaudeCode(context.Background(), "/x")
	if mock.cmd != "claude" {
		t.Errorf("cmd: got %s, want claude", mock.cmd)
	}
}

// ─── DryRun ──────────────────────────────────────────────────

func TestDryRun_DoesNotSpawn(t *testing.T) {
	mock := &mockSpawner{}
	l := &Launcher{Spawner: mock, DryRun: true, GOOSOverride: "linux"}
	if err := l.LaunchWindsurf(context.Background(), "/x"); err != nil {
		t.Fatal(err)
	}
	if mock.called {
		t.Error("spawner called despite DryRun")
	}
	// LastCommand は記録されているはず
	cmd, args := l.LastCommand()
	if cmd != "windsurf" || len(args) == 0 {
		t.Errorf("LastCommand not recorded: %s %v", cmd, args)
	}
}

// ─── Validation ──────────────────────────────────────────────

func TestLaunchWindsurf_EmptyPathError(t *testing.T) {
	l := New()
	if err := l.LaunchWindsurf(context.Background(), ""); err == nil {
		t.Error("expected error for empty path")
	}
}

func TestLaunchClaudeCode_EmptyPathError(t *testing.T) {
	l := New()
	if err := l.LaunchClaudeCode(context.Background(), ""); err == nil {
		t.Error("expected error for empty path")
	}
}

// ─── Spawner error propagation ──────────────────────────────

func TestSpawnerError_Propagated(t *testing.T) {
	mock := &mockSpawner{err: errors.New("simulated spawn failure")}
	l := &Launcher{Spawner: mock, GOOSOverride: "linux"}
	err := l.LaunchWindsurf(context.Background(), "/x")
	if err == nil {
		t.Error("expected error to be propagated")
	}
}

// ─── Deeplink ────────────────────────────────────────────────

func TestWindsurfDeeplink(t *testing.T) {
	cases := map[string]string{
		"/home/m/yagura": "windsurf://file/home/m/yagura", // 絶対パス: 先頭 / を path として共有
		"relative/path":  "windsurf://file/relative/path", // 相対: / を補完
	}
	for in, want := range cases {
		got := WindsurfDeeplink(in)
		if got != want {
			t.Errorf("WindsurfDeeplink(%q) = %q, want %q", in, got, want)
		}
		if !strings.HasPrefix(got, "windsurf://") {
			t.Errorf("missing windsurf:// prefix: %q", got)
		}
	}
}

// ─── New defaults ────────────────────────────────────────────

func TestNew_HasSpawner(t *testing.T) {
	l := New()
	if l.Spawner == nil {
		t.Error("Spawner is nil")
	}
}

// ─── Windows claude code command ─────────────────────────────

func TestLaunchClaudeCode_Windows(t *testing.T) {
	mock := &mockSpawner{}
	l := &Launcher{Spawner: mock, GOOSOverride: "windows"}
	if err := l.LaunchClaudeCode(context.Background(), `C:\project`); err != nil {
		t.Fatal(err)
	}
	if mock.cmd != "cmd" {
		t.Errorf("cmd: got %s, want cmd", mock.cmd)
	}
	if len(mock.args) < 2 || mock.args[0] != "/c" {
		t.Errorf("args: got %v, want [/c claude code ...]", mock.args)
	}
}

// ─── goos() fallback to runtime.GOOS ─────────────────────────

func TestGOOS_DefaultFallback(t *testing.T) {
	// no GOOSOverride → goos() returns runtime.GOOS
	l := &Launcher{DryRun: true}
	// Just launch to exercise goos() with no override; no spawner needed for DryRun.
	_ = l.LaunchWindsurf(context.Background(), "/tmp/workspace")
	cmd, _ := l.LastCommand()
	if cmd == "" {
		t.Error("DryRun should still record the command")
	}
}

// ─── OSSpawner.Start ──────────────────────────────────────────

func TestOSSpawner_Start_Succeeds(t *testing.T) {
	s := &OSSpawner{}
	// "true" is a standard POSIX command that exits 0 immediately.
	if err := s.Start(context.Background(), "true"); err != nil {
		t.Errorf("OSSpawner.Start(true): %v", err)
	}
}

func TestOSSpawner_Start_FailsForNonexistentCmd(t *testing.T) {
	s := &OSSpawner{}
	err := s.Start(context.Background(), "this-cmd-does-not-exist-yagura-test")
	if err == nil {
		t.Error("expected error for nonexistent command")
	}
}

// TestRun_LazyDefaultSpawner covers the `if l.Spawner == nil` branch in run():
// a non-DryRun Launcher with a nil Spawner must lazily construct an OSSpawner.
// We use a GOOSOverride + nonexistent command so the real OSSpawner.Start fails
// fast without launching any actual editor process.
func TestRun_LazyDefaultSpawner(t *testing.T) {
	l := &Launcher{
		DryRun:       false, // force the real spawn path
		Spawner:      nil,   // must be lazily initialized inside run()
		GOOSOverride: "linux",
	}
	// LaunchWindsurf on linux runs the bare "windsurf" binary, which is absent in
	// CI → OSSpawner.Start returns a start error. The point is the nil-Spawner
	// branch executes and l.Spawner is populated afterwards.
	err := l.LaunchWindsurf(context.Background(), t.TempDir())
	if err == nil {
		t.Skip("windsurf binary unexpectedly present in PATH; lazy-init branch still ran")
	}
	if l.Spawner == nil {
		t.Error("run() should have lazily set l.Spawner to a default OSSpawner")
	}
	if _, ok := l.Spawner.(*OSSpawner); !ok {
		t.Errorf("expected lazily-set Spawner to be *OSSpawner, got %T", l.Spawner)
	}
}
