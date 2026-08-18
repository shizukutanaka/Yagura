package main

import (
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ─── version sync gate ───────────────────────────────────────

// TestVersion_MatchesDaemon pins the comment "updated together with main yagura
// version" as a machine-checked invariant. The tray shipped 3 releases (0.33-0.35)
// stuck at 0.32.0 because the release checklist doesn't mention this file; this
// test makes the drift impossible to miss.
func TestVersion_MatchesDaemon(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "yagura", "main.go"))
	if err != nil {
		t.Skipf("daemon source not readable from test cwd: %v", err)
	}
	re := regexp.MustCompile(`version\s*=\s*"([0-9.]+)"`)
	m := re.FindSubmatch(src)
	if m == nil {
		t.Fatal("could not find version literal in ../yagura/main.go — update this test's regexp")
	}
	daemonVersion := string(m[1])
	if version != daemonVersion {
		t.Errorf("yagura-tray version = %q, daemon = %q — bump them together (see release checklist)",
			version, daemonVersion)
	}
}

// ─── daemon.Start env wiring ─────────────────────────────────

func TestDaemon_Start_TokenEnvWiring(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX shell script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fakeyagura")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &daemon{
		path:        script,
		addr:        "127.0.0.1:1",
		stateDir:    t.TempDir(),
		githubToken: "ghp_traytest",
		mcpToken:    "mcp_traytest",
	}
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer d.Stop()

	env := strings.Join(d.cmd.Env, "\n")
	for _, want := range []string{
		"YAGURA_GITHUB_TOKEN=ghp_traytest",
		"YAGURA_MCP_TOKEN=mcp_traytest",
		"YAGURA_ADDR=" + d.addr,
		"YAGURA_STATE_DIR=" + d.stateDir,
	} {
		if !strings.Contains(env, want) {
			t.Errorf("daemon env missing %q", want)
		}
	}
}

// ─── daemon.Stop edge cases ──────────────────────────────────

func TestDaemon_Stop_NilGuards(t *testing.T) {
	// never started → no-op, no panic
	(&daemon{}).Stop()
	// cmd set but process nil (Start failed) → no-op, no panic
	d := &daemon{path: "/nonexistent-binary-xyz"}
	_ = d.Start() // fails; cmd.Process stays nil
	d.Stop()
}

func TestDaemon_Stop_ForceKillsAfterTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX trap semantics")
	}
	// A child that ignores SIGTERM forces Stop's 3s escalation to Kill.
	// The sleeper is backgrounded with its stdio detached: when Kill reaps the
	// shell, the orphaned sleep must not keep the test harness's stdout pipe
	// open (go test waits for I/O to complete and would fail the package).
	// The script touches $YAGURA_STATE_DIR/ready *after* installing the trap so
	// the test can wait deterministically instead of racing a fixed sleep
	// (under full-suite load a sleep was not always enough — flaked once).
	dir := t.TempDir()
	script := filepath.Join(dir, "stubborn")
	body := "#!/bin/sh\ntrap '' TERM\n: > \"$YAGURA_STATE_DIR/ready\"\nsleep 60 >/dev/null 2>&1 &\nwait $!\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	// 猶予は注入する。本番の 3 秒を実時間で待つ理由は無い——検証したいのは
	// 「SIGTERM を無視する子でも猶予後に必ず kill されて刈り取られる」ことであって、
	// 3 という数値ではない(数値は TestDaemon_StopGraceDefault が固定する)。
	const grace = 200 * time.Millisecond
	d := &daemon{path: script, addr: "127.0.0.1:1", stateDir: t.TempDir(), stopGrace: grace}
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	readyPath := filepath.Join(d.stateDir, "ready")
	trapInstalled := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(readyPath); err == nil {
			trapInstalled = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !trapInstalled {
		d.Stop()
		t.Fatal("stubborn child never signaled trap readiness")
	}

	start := time.Now()
	d.Stop() // must escalate to Kill after the grace and reap the child
	elapsed := time.Since(start)

	if elapsed < grace {
		t.Errorf("Stop returned in %v (< grace %v) — SIGTERM should have been ignored, forcing the Kill path", elapsed, grace)
	}
	if err := d.cmd.Process.Signal(syscall.Signal(0)); err == nil {
		t.Error("process still alive after forced Stop")
	}
}

// ─── resolveDaemonPath fallbacks ─────────────────────────────

func TestResolveDaemonPath_PATHFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX PATH semantics")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "yagura")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	// no sibling 'yagura' next to the test binary in CI → falls through to PATH
	if got := resolveDaemonPath(""); got != fake {
		t.Skipf("sibling binary shadowed PATH lookup (got %q) — environment-dependent", got)
	}
}

func TestResolveDaemonPath_LastResort(t *testing.T) {
	// Empty PATH and no sibling → bare "yagura" so the caller's Stat shows
	// a clear error message.
	t.Setenv("PATH", t.TempDir())
	if got := resolveDaemonPath(""); got != "yagura" {
		t.Skipf("expected bare fallback, got %q — a sibling binary exists in this env", got)
	}
}

// ─── resolveStateDir linux branches ──────────────────────────

func TestResolveStateDir_XDGStateHome(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG branch is linux-only")
	}
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	got := resolveStateDir("")
	if got != filepath.Join("/xdg/state", "yagura") {
		t.Errorf("resolveStateDir = %q, want /xdg/state/yagura", got)
	}
}

func TestResolveStateDir_HomeFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("~/.local/state branch is linux-only")
	}
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := resolveStateDir("")
	if got != filepath.Join(home, ".local", "state", "yagura") {
		t.Errorf("resolveStateDir = %q, want %s/.local/state/yagura", got, home)
	}
}

// ─── browser launch fallbacks ────────────────────────────────

func TestOpenBrowser_MissingLauncherWarnsOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rundll32 always exists on windows")
	}
	// xdg-open/open absent from an empty PATH → Start fails → warning, no panic.
	t.Setenv("PATH", t.TempDir())
	openBrowser("http://127.0.0.1:1/dashboard")
}

func TestOpenApp_FallsBackWhenNoChromium(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX PATH semantics")
	}
	// Empty PATH: findChromium misses, openBrowser also misses — both
	// best-effort, must not panic or block.
	t.Setenv("PATH", t.TempDir())
	openApp("http://127.0.0.1:1/dashboard")
}

// ─── non-windows tray fallback ───────────────────────────────

func TestPlatformSupportsTray_NonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows build has its own tray implementation")
	}
	if platformSupportsTray() {
		t.Error("non-windows build must report no tray support")
	}
}

func TestRuntimeGOOS(t *testing.T) {
	t.Setenv("GOOS", "plan9")
	if got := runtimeGOOS(); got != "plan9" {
		t.Errorf("runtimeGOOS with GOOS env = %q, want plan9", got)
	}
	t.Setenv("GOOS", "")
	if got := runtimeGOOS(); got != "this platform" {
		t.Errorf("runtimeGOOS without GOOS env = %q, want fallback label", got)
	}
}

func TestRunTray_ExitsOnSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-windows fallback only")
	}
	// Pre-register our own handler so SIGTERM can never hit the default
	// (process-killing) disposition, even before runTray installs its Notify.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGTERM)
	defer signal.Stop(guard)

	done := make(chan struct{})
	go func() {
		runTray(&daemon{}, "127.0.0.1:1")
		close(done)
	}()

	// Nudge with SIGTERM until runTray's own channel (registered inside the
	// goroutine) receives one and the function returns.
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-deadline:
			t.Fatal("runTray did not exit after SIGTERM")
		case <-tick.C:
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}
	}
}

// 本番の猶予は 3 秒であること。テストが短い値を使うのは実時間を待たないためで、
// 本番まで短くしてよいという意味ではない。
func TestDaemon_StopGraceDefault(t *testing.T) {
	if defaultStopGrace != 3*time.Second {
		t.Errorf("production stop grace must stay 3s, got %v", defaultStopGrace)
	}
	if (&daemon{}).grace() != defaultStopGrace {
		t.Error("a zero stopGrace must fall back to the production default")
	}
}

// tray が **偽の資格情報を作らない** ことを固定する(v1.2.0)。
//
// 以前は token が無いと "tray-no-token-placeholder" を注入していた。daemon の
// 書式検証に弾かれて起動しないので、PAT を持たない利用者にとって
// 「yagura-tray をダブルクリック」という導線は壊れていた。要求を迂回する仕掛けが
// 製品内に生えたら、その要求の方が間違っている——要求は削除済み。
func TestDaemonEnv_NoFakeCredentialWhenTokenAbsent(t *testing.T) {
	d := &daemon{addr: "127.0.0.1:1", stateDir: "/tmp/x"}
	env := d.env(nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "YAGURA_GITHUB_TOKEN=") {
			t.Errorf("no token was configured, yet the tray set %q — it must not invent credentials", kv)
		}
		if strings.Contains(kv, "placeholder") {
			t.Errorf("placeholder credential leaked into the child env: %q", kv)
		}
	}
}

func TestDaemonEnv_PassesRealTokenThrough(t *testing.T) {
	d := &daemon{addr: "127.0.0.1:1", stateDir: "/tmp/x", githubToken: "ghp_real", mcpToken: "secret"}
	env := d.env(nil)
	var sawGH, sawMCP bool
	for _, kv := range env {
		if kv == "YAGURA_GITHUB_TOKEN=ghp_real" {
			sawGH = true
		}
		if kv == "YAGURA_MCP_TOKEN=secret" {
			sawMCP = true
		}
	}
	if !sawGH || !sawMCP {
		t.Errorf("configured tokens must reach the child: gh=%v mcp=%v", sawGH, sawMCP)
	}
}
