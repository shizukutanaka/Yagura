package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNew_EmitsJSONWithServiceAndVersion(t *testing.T) {
	var buf bytes.Buffer
	l := New("info", "yagura", "v0.1.0", &buf)
	l.Info("hello", "k", "v")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["service"] != "yagura" || rec["version"] != "v0.1.0" {
		t.Errorf("service/version missing: %v", rec)
	}
	if rec["msg"] != "hello" || rec["k"] != "v" {
		t.Errorf("payload missing: %v", rec)
	}
}

func TestNew_RespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	l := New("warn", "yagura", "v0", &buf)
	l.Debug("hidden")
	l.Info("hidden too")
	l.Warn("shown")
	out := buf.String()
	if strings.Contains(out, "hidden") {
		t.Errorf("debug/info should be suppressed: %s", out)
	}
	if !strings.Contains(out, "shown") {
		t.Errorf("warn should be emitted: %s", out)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo,
		"garbage": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDiscard_NeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Discard() panicked: %v", r)
		}
	}()
	l := Discard()
	l.Info("test")
	l.Error("test")
}
