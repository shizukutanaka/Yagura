// yagura-tray: 1-click Windows tray launcher for yagura.
//
// What it does:
//  1. Embeds the yagura daemon binary (or finds it next to itself).
//  2. Starts the daemon as a child process.
//  3. Opens the dashboard in the default browser.
//  4. Adds a system tray icon with right-click menu (Open/Stop/Quit).
//
// Why a separate binary:
//
//	The daemon (cmd/yagura) is a pure HTTP server with no GUI. yagura-tray
//	is a thin wrapper that adds Windows-specific UX (tray icon, browser
//	launch). This keeps the daemon portable and reproducible while the
//	tray binary handles GUI concerns.
//
// Zero deps (ADR-0001):
//   - Tray UI uses syscall + user32.dll/shell32.dll directly (Windows).
//   - On non-Windows, the binary falls back to plain daemon + browser launch.
//   - No external Go modules.
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

var (
	version = "0.99.1" // updated together with main yagura version
)

func main() {
	var (
		daemonPath  = flag.String("daemon", "", "Path to yagura daemon binary (default: yagura next to this exe)")
		addr        = flag.String("addr", "127.0.0.1:18190", "Listen address for daemon")
		stateDir    = flag.String("state-dir", "", "State directory (default: per-OS user data dir)")
		githubToken = flag.String("github-token", "", "GitHub PAT (or set YAGURA_GITHUB_TOKEN env)")
		mcpToken    = flag.String("mcp-token", "", "MCP Bearer token (optional, empty = no auth on loopback)")
		noTray      = flag.Bool("no-tray", false, "Skip system tray (foreground daemon + browser only)")
		noBrowser   = flag.Bool("no-browser", false, "Skip auto-opening browser")
		showVersion = flag.Bool("version", false, "Print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("yagura-tray %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	// daemon path resolution
	dp := resolveDaemonPath(*daemonPath)
	if _, err := os.Stat(dp); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: yagura daemon not found at %s\n", dp)
		fmt.Fprintf(os.Stderr, "       Place yagura(.exe) next to yagura-tray, or use -daemon flag.\n")
		os.Exit(1)
	}

	// state dir resolution (OS-specific)
	sd := resolveStateDir(*stateDir)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cannot create state dir %s: %v\n", sd, err)
		os.Exit(1)
	}

	// GitHub token: -github-token flag overrides env. If neither set,
	// supply a placeholder so the daemon's config validation doesn't reject
	// startup (read-only ops will still work locally; scanner/vulns won't).
	gh := *githubToken
	if gh == "" {
		gh = os.Getenv("YAGURA_GITHUB_TOKEN")
	}
	if gh == "" {
		fmt.Fprintln(os.Stderr, "(no -github-token / YAGURA_GITHUB_TOKEN — scanner/vulns will not refresh)")
		gh = "tray-no-token-placeholder"
	}

	// daemon launch
	d := &daemon{
		path:        dp,
		addr:        *addr,
		stateDir:    sd,
		githubToken: gh,
		mcpToken:    *mcpToken,
	}
	if err := d.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: failed to start daemon: %v\n", err)
		os.Exit(1)
	}
	defer d.Stop()

	// wait readiness (max 5s) then open browser
	if waitForReady(*addr, 5*time.Second) {
		fmt.Printf("yagura daemon ready on http://%s\n", *addr)
		if !*noBrowser {
			openApp("http://" + *addr + "/dashboard")
		}
	} else {
		fmt.Fprintf(os.Stderr, "WARN: daemon not ready after 5s; continuing anyway.\n")
	}

	// signal handling — ensure daemon stops when tray exits
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	if *noTray || !platformSupportsTray() {
		fmt.Println("(running without tray — Ctrl-C to stop)")
		<-sigCh
		return
	}

	// platform-specific tray loop (blocks until tray exit)
	runTray(d, *addr)
}

// daemon manages the spawned yagura subprocess lifecycle.
type daemon struct {
	path        string
	addr        string
	stateDir    string
	githubToken string
	mcpToken    string
	cmd         *exec.Cmd
}

func (d *daemon) Start() error {
	d.cmd = exec.Command(d.path)
	env := append(os.Environ(),
		"YAGURA_ADDR="+d.addr,
		"YAGURA_STATE_DIR="+d.stateDir,
	)
	if d.githubToken != "" {
		env = append(env, "YAGURA_GITHUB_TOKEN="+d.githubToken)
	}
	if d.mcpToken != "" {
		env = append(env, "YAGURA_MCP_TOKEN="+d.mcpToken)
	}
	d.cmd.Env = env
	d.cmd.Stdout = os.Stdout
	d.cmd.Stderr = os.Stderr
	return d.cmd.Start()
}

func (d *daemon) Stop() {
	if d.cmd == nil || d.cmd.Process == nil {
		return
	}
	// graceful first (SIGTERM on Unix, Process.Kill on Windows since no SIGTERM)
	d.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- d.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		d.cmd.Process.Kill()
		<-done
	}
}

// resolveDaemonPath finds the yagura daemon binary.
//
// Search order:
//  1. -daemon flag
//  2. Same directory as the tray executable (most common)
//  3. PATH lookup
func resolveDaemonPath(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	exePath, err := os.Executable()
	if err == nil {
		dir := filepath.Dir(exePath)
		name := "yagura"
		if runtime.GOOS == "windows" {
			name = "yagura.exe"
		}
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// fallback: PATH
	if p, err := exec.LookPath("yagura"); err == nil {
		return p
	}
	// last resort: assume same-dir, let the caller see the error
	return "yagura"
}

// resolveStateDir picks an OS-appropriate state directory.
//
// Windows: %APPDATA%\yagura
// macOS:   ~/Library/Application Support/yagura
// Linux:   $XDG_STATE_HOME/yagura or ~/.local/state/yagura
func resolveStateDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	switch runtime.GOOS {
	case "windows":
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, "yagura")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "yagura")
		}
	default: // linux & others
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			return filepath.Join(xdg, "yagura")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "state", "yagura")
		}
	}
	return ".yagura-state"
}

// waitForReady polls the daemon's TCP listener.
//
// Uses TCP dial rather than HTTP /readyz to avoid HTTP-level dependencies
// during startup and to keep this loop tiny.
func waitForReady(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// openApp opens the dashboard as a chromeless "app window" via a Chromium-based
// browser (--app=URL) so it looks like a native desktop app to non-CLI users.
// Falls back to the default browser when no Chromium browser is found.
//
// Zero deps (ADR-0001): just os/exec. The PWA manifest the dashboard serves
// also lets users "Install" it as a standalone desktop app from any browser.
func openApp(url string) {
	if bin := findChromium(); bin != "" {
		cmd := exec.Command(bin, appArgs(url)...)
		if err := cmd.Start(); err == nil {
			return
		}
	}
	openBrowser(url)
}

// appArgs are the flags that open URL as a standalone chromeless window.
func appArgs(url string) []string {
	return []string{"--app=" + url, "--window-size=1200,840", "--no-first-run", "--no-default-browser-check"}
}

// chromiumCandidates lists Chromium-based browser executables to try, per OS.
func chromiumCandidates() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{"chrome", "msedge", "brave", "chromium"}
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	default:
		return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"}
	}
}

// findChromium returns the first available Chromium-based browser, or "".
func findChromium() string {
	for _, c := range chromiumCandidates() {
		if filepath.IsAbs(c) {
			if _, err := os.Stat(c); err == nil {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// openBrowser launches the default browser at url.
//
// Best-effort: failure is logged but doesn't abort the tray launch.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "WARN: could not open browser: %v\n", err)
	}
}
