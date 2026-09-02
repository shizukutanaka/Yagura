package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// firstRelativeCandidate returns the first PATH-resolvable (non-absolute)
// chromium candidate for this OS, or "" if all are absolute (e.g. darwin).
func firstRelativeCandidate() string {
	for _, c := range chromiumCandidates() {
		if !filepath.IsAbs(c) {
			return c
		}
	}
	return ""
}

func TestFindChromium_NoneWhenPathEmpty(t *testing.T) {
	if firstRelativeCandidate() == "" {
		t.Skip("this OS uses absolute candidate paths; PATH manipulation is moot")
	}
	// With an empty PATH, no PATH-relative candidate resolves. (On platforms whose
	// candidates are all relative names, this guarantees "".)
	t.Setenv("PATH", t.TempDir())
	if got := findChromium(); got != "" {
		t.Errorf("expected no chromium with an empty PATH, got %q", got)
	}
}

func TestFindChromium_FindsOnPath(t *testing.T) {
	name := firstRelativeCandidate()
	if name == "" {
		t.Skip("no PATH-relative candidate on this OS")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake browser: %v", err)
	}
	t.Setenv("PATH", dir)
	got := findChromium()
	if got != bin {
		t.Errorf("findChromium = %q, want %q", got, bin)
	}
}

// openApp should launch the discovered chromium with the --app window flags.
// The fake browser records its argv so we can assert the app-window path was taken.
func TestOpenApp_LaunchesChromiumWithAppArg(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake browser is a POSIX shell script")
	}
	name := firstRelativeCandidate()
	if name == "" {
		t.Skip("no PATH-relative candidate on this OS")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "argv.txt")
	// The fake browser writes its arguments, then exits 0 (so cmd.Start succeeds).
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake browser: %v", err)
	}
	t.Setenv("PATH", dir)

	openApp("http://127.0.0.1:8090/dashboard")

	// openApp uses Start (async); poll briefly for the fake browser to write argv.
	var data []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(argsFile); err == nil && len(b) > 0 {
			data = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatal("fake chromium was not launched (no argv recorded)")
	}
	got := string(data)
	if !strings.Contains(got, "--app=http://127.0.0.1:8090/dashboard") {
		t.Errorf("expected --app=URL in launch args, got:\n%s", got)
	}
}
