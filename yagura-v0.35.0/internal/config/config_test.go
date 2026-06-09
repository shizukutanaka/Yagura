package config

import (
	"errors"
	"strings"
	"testing"
)

func clearAll(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"YAGURA_ADDR", "YAGURA_STATE_DIR", "YAGURA_GITHUB_TOKEN",
		"YAGURA_MCP_TOKEN", "YAGURA_LOG_LEVEL", "YAGURA_SCAN_INTERVAL",
		"YAGURA_ENABLE_PPROF",
	} {
		t.Setenv(k, "")
	}
}

func TestLoad_Success(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_abc1234567890")
	t.Setenv("YAGURA_STATE_DIR", "/tmp/yagura-test")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != "127.0.0.1:8090" {
		t.Errorf("default addr wrong: %s", c.Addr)
	}
	if c.ScanInterval.Minutes() != 5 {
		t.Errorf("default interval wrong: %s", c.ScanInterval)
	}
}

func TestLoad_MissingToken(t *testing.T) {
	clearAll(t)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "YAGURA_GITHUB_TOKEN") {
		t.Errorf("expected token error, got %v", err)
	}
}

func TestLoad_InvalidTokenFormat(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "not-a-token")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ghp_") {
		t.Errorf("expected format hint, got %v", err)
	}
}

func TestLoad_MultipleErrors(t *testing.T) {
	clearAll(t)
	// Token も log level も間違える
	t.Setenv("YAGURA_GITHUB_TOKEN", "")
	t.Setenv("YAGURA_LOG_LEVEL", "trace")
	t.Setenv("YAGURA_ADDR", ":not-numeric")
	_, err := Load()
	if err == nil {
		t.Fatal("expected combined errors")
	}
	for _, want := range []string{"YAGURA_GITHUB_TOKEN", "YAGURA_LOG_LEVEL", "YAGURA_ADDR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in error: %s", want, err)
		}
	}
}

func TestLoad_PublicBindWithoutToken_Refused(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_xxx")
	t.Setenv("YAGURA_ADDR", "0.0.0.0:8090")
	// MCP_TOKEN intentionally empty
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "public interface") {
		t.Errorf("expected public-bind safety error, got %v", err)
	}
}

func TestLoad_PublicBindWithToken_OK(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_xxx")
	t.Setenv("YAGURA_ADDR", "0.0.0.0:8090")
	t.Setenv("YAGURA_MCP_TOKEN", "some-strong-token")
	t.Setenv("YAGURA_STATE_DIR", "/tmp/y")
	_, err := Load()
	if err != nil {
		t.Errorf("public bind with token should be allowed, got %v", err)
	}
}

func TestLoad_ScanIntervalTooShort(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_x")
	t.Setenv("YAGURA_SCAN_INTERVAL", "1s")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "30s") {
		t.Errorf("expected min interval error, got %v", err)
	}
}

func TestLoad_InvalidScanInterval(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_x")
	t.Setenv("YAGURA_SCAN_INTERVAL", "garbage")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "duration") {
		t.Errorf("expected duration parse error, got %v", err)
	}
}

func TestValidateAddr(t *testing.T) {
	cases := []struct {
		input string
		valid bool
	}{
		{"127.0.0.1:8090", true},
		{":8090", true},
		{"[::]:8090", true},
		{"localhost:80", true},

		{"", false},
		{"8090", false},
		{":", false},
		{":abc", false},
		{":0", false},
		{":65536", false},
	}
	for _, c := range cases {
		err := validateAddr(c.input)
		if c.valid && err != nil {
			t.Errorf("%q should be valid: %v", c.input, err)
		}
		if !c.valid && err == nil {
			t.Errorf("%q should be invalid", c.input)
		}
	}
}

func TestIsPublicBind(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8090": false,
		"localhost:8090": false,
		"[::1]:8090":     false,
		"0.0.0.0:8090":   true,
		"[::]:8090":      true,
		":8090":          true, // empty host = bind all
	}
	for addr, want := range cases {
		if got := isPublicBind(addr); got != want {
			t.Errorf("isPublicBind(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestConfig_String_RedactsSecrets(t *testing.T) {
	c := &Config{
		Addr:        "127.0.0.1:8090",
		StateDir:    "/x",
		GitHubToken: "ghp_supersecrettoken123456789",
		MCPToken:    "mcp_secret_token_456",
		LogLevel:    "info",
	}
	s := c.String()
	for _, leak := range []string{"ghp_supersecret", "mcp_secret_token"} {
		if strings.Contains(s, leak) {
			t.Errorf("secret leaked in String(): %s", s)
		}
	}
	if !strings.Contains(s, "redacted") {
		t.Error("String() should indicate redaction")
	}
}

func TestLoad_ReturnsJoinedErrors(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_LOG_LEVEL", "garbage")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error")
	}
	// errors.Is は work する(Join 経由でも)
	if !errors.Is(err, err) {
		t.Error("errors.Is sanity check failed")
	}
}

// ─── envInt + AuditKeepDays ────────────────────────────────

func TestEnvInt_Default(t *testing.T) {
	t.Setenv("UNSET_VAR", "")
	if got := envInt("UNSET_VAR", 42); got != 42 {
		t.Errorf("empty env should return default: got %d", got)
	}
}

func TestEnvInt_Valid(t *testing.T) {
	t.Setenv("INT_VAR", "123")
	if got := envInt("INT_VAR", 0); got != 123 {
		t.Errorf("got %d, expected 123", got)
	}
}

func TestEnvInt_InvalidFallsBack(t *testing.T) {
	t.Setenv("INT_VAR", "not-a-number")
	if got := envInt("INT_VAR", 7); got != 7 {
		t.Errorf("invalid should return default: got %d", got)
	}
}

func TestEnvInt_Negative(t *testing.T) {
	t.Setenv("INT_VAR", "-5")
	if got := envInt("INT_VAR", 0); got != -5 {
		t.Errorf("negative should be allowed: got %d", got)
	}
}

func TestAuditKeepDays_Default(t *testing.T) {
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_dummy_for_test")
	// Don't set YAGURA_AUDIT_KEEP_DAYS
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditKeepDays != 90 {
		t.Errorf("default should be 90, got %d", cfg.AuditKeepDays)
	}
}

func TestAuditKeepDays_Override(t *testing.T) {
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_dummy_for_test")
	t.Setenv("YAGURA_AUDIT_KEEP_DAYS", "30")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditKeepDays != 30 {
		t.Errorf("override should be 30, got %d", cfg.AuditKeepDays)
	}
}

// ─── path helper functions ────────────────────────────────────

func TestProjectsDirFor(t *testing.T) {
	got := ProjectsDirFor("/state")
	if got != "/state/projects" {
		t.Errorf("ProjectsDirFor = %q, want /state/projects", got)
	}
}

func TestAuditDirFor(t *testing.T) {
	got := AuditDirFor("/state")
	if got != "/state/audit" {
		t.Errorf("AuditDirFor = %q, want /state/audit", got)
	}
}

func TestSecretsDirFor(t *testing.T) {
	got := SecretsDirFor("/state")
	if got != "/state/secrets" {
		t.Errorf("SecretsDirFor = %q, want /state/secrets", got)
	}
}

func TestConfig_PathMethods(t *testing.T) {
	c := &Config{StateDir: "/mystate"}
	if got := c.ProjectsDir(); got != "/mystate/projects" {
		t.Errorf("ProjectsDir = %q", got)
	}
	if got := c.AuditPath(); got != "/mystate/audit" {
		t.Errorf("AuditPath = %q", got)
	}
	if got := c.DraftsDir(); got != "/mystate/drafts" {
		t.Errorf("DraftsDir = %q", got)
	}
	if got := c.SecretsPath(); got != "/mystate/secrets" {
		t.Errorf("SecretsPath = %q", got)
	}
}

func TestConfig_GoString(t *testing.T) {
	c := &Config{StateDir: "/s", GitHubToken: "ghp_secret"}
	s := c.GoString()
	if s == "" {
		t.Error("GoString should return non-empty string")
	}
	if strings.Contains(s, "ghp_secret") {
		t.Error("GoString must redact token")
	}
}

func TestResolveStateDir_EnvOverride(t *testing.T) {
	t.Setenv("YAGURA_STATE_DIR", "/custom/state")
	got, err := ResolveStateDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/custom/state" {
		t.Errorf("ResolveStateDir = %q, want /custom/state", got)
	}
}

func TestEnvBool_TrueValues(t *testing.T) {
	for _, val := range []string{"1", "true", "TRUE", "yes"} {
		t.Setenv("YAGURA_ENABLE_PPROF", val)
		if !envBool("YAGURA_ENABLE_PPROF") {
			t.Errorf("envBool(%q) = false, want true", val)
		}
	}
}

func TestEnvBool_FalseValues(t *testing.T) {
	for _, val := range []string{"0", "false", "", "no"} {
		t.Setenv("YAGURA_ENABLE_PPROF", val)
		if envBool("YAGURA_ENABLE_PPROF") {
			t.Errorf("envBool(%q) = true, want false", val)
		}
	}
}
