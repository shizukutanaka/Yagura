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

// v1.2.0 で **仕様を意図的に変えた**: token が無いことは error ではなくなった。
// 旧 TestLoad_MissingToken(「無ければ error」を固定していた)はここで置き換わる。
// 実装に合わせてテストを書き換えたのではなく、要求そのものを削除した結果である
// ——理由は config.go の該当箇所と CHANGELOG v1.2.0 に書いてある。
func TestLoad_MissingTokenStartsInLocalOnlyMode(t *testing.T) {
	clearAll(t)
	c, err := Load()
	if err != nil {
		t.Fatalf("a missing token must not prevent startup: %v", err)
	}
	if c.GitHubEnabled() {
		t.Error("GitHubEnabled() must be false without a token")
	}
	if len(c.DisabledCapabilities()) == 0 {
		t.Error("must name the capabilities that are unavailable")
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
	// token の「形が壊れている」+ log level + addr の 3 つを同時に間違える
	// (「token が無い」はもう error ではないので、壊れた token を使う)
	t.Setenv("YAGURA_GITHUB_TOKEN", "not-a-token")
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

// ─── isPublicBind ────────────────────────────────────────────

func TestIsPublicBind_AllCases(t *testing.T) {
	cases := []struct {
		addr   string
		public bool
	}{
		{"127.0.0.1:8080", false},
		{"localhost:8080", false},
		{"[::1]:8080", false},
		{":8080", true},
		{"0.0.0.0:8080", true},
		{"[::]:8080", true},
		{"192.168.1.1:8080", true},  // LAN IP → public
		{"not-valid", false},        // SplitHostPort error → false
	}
	for _, tc := range cases {
		got := isPublicBind(tc.addr)
		if got != tc.public {
			t.Errorf("isPublicBind(%q) = %v, want %v", tc.addr, got, tc.public)
		}
	}
}

// ─── validateAddr whitespace host ────────────────────────────

func TestValidateAddr_WhitespaceHost(t *testing.T) {
	err := validateAddr("127.0.0 1:8080")
	if err == nil {
		t.Error("host with space should return error")
	}
}

// ─── isPublicBind: custom loopback IP ────────────────────────

func TestIsPublicBind_CustomLoopback(t *testing.T) {
	// 127.0.0.2 is in 127.0.0.0/8 loopback range; not in the literal switch,
	// so it hits net.ParseIP + IsLoopback() path and must return false.
	if got := isPublicBind("127.0.0.2:8090"); got {
		t.Error("127.0.0.2 is loopback → isPublicBind should return false")
	}
}

// ─── Load: ScanTimeout validation ────────────────────────────

func TestLoad_ScanTimeoutTooShort(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_testtesttesttest")
	t.Setenv("YAGURA_STATE_DIR", "/tmp/x")
	t.Setenv("YAGURA_SCAN_TIMEOUT", "500ms") // < 1s
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "too short") {
		t.Errorf("expected too-short timeout error, got %v", err)
	}
}

func TestLoad_ScanTimeoutInvalid(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_testtesttesttest")
	t.Setenv("YAGURA_STATE_DIR", "/tmp/x")
	t.Setenv("YAGURA_SCAN_TIMEOUT", "not-a-duration")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "YAGURA_SCAN_TIMEOUT") {
		t.Errorf("expected scan-timeout parse error, got %v", err)
	}
}

// ─── Load: SecurityScanInterval validation ───────────────────

func TestLoad_SecurityScanIntervalTooShort(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_testtesttesttest")
	t.Setenv("YAGURA_STATE_DIR", "/tmp/x")
	t.Setenv("YAGURA_SECURITY_SCAN_INTERVAL", "30m") // < 1h
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "1h") {
		t.Errorf("expected security-interval too-short error, got %v", err)
	}
}

func TestLoad_SecurityScanIntervalInvalid(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_testtesttesttest")
	t.Setenv("YAGURA_STATE_DIR", "/tmp/x")
	t.Setenv("YAGURA_SECURITY_SCAN_INTERVAL", "garbage")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "YAGURA_SECURITY_SCAN_INTERVAL") {
		t.Errorf("expected security-interval parse error, got %v", err)
	}
}

// ─── Load: per-owner token validation failure ────────────────

func TestLoad_PerOwnerTokenInvalidFormat(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_testtesttesttest")
	t.Setenv("YAGURA_STATE_DIR", "/tmp/x")
	// Per-owner token with bad format → Load should fail
	t.Setenv("YAGURA_GITHUB_TOKEN_MYORG", "not-a-token-format")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "YAGURA_GITHUB_TOKEN_MYORG") {
		t.Errorf("expected per-owner token format error, got %v", err)
	}
}

// ─── Load: GitHubBase validation ─────────────────────────────

func TestLoad_GitHubBaseInvalidScheme(t *testing.T) {
	clearAll(t)
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_testtesttesttest")
	t.Setenv("YAGURA_STATE_DIR", "/tmp/x")
	t.Setenv("YAGURA_GITHUB_BASE", "ftp://api.github.com") // not http or https
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "YAGURA_GITHUB_BASE") {
		t.Errorf("expected github-base scheme error, got %v", err)
	}
}

// GitHub token は **任意** であること(v1.2.0)。
//
// なぜ変えたか:
//
//	v1.1.0 まで、token が無いと daemon は起動を拒否していた。しかし 79 tool のうち
//	GitHub / ネットワークを要するのは vulns・scorecard・scanner だけで、29 レンズも
//	registry も graph も plan も harness 監査も、まったく必要としない。
//	「ローカル優先」を掲げる製品が、ローカル作業のために PAT の発行を強制していた。
//
//	決定的な証拠は製品自身の中にあった: cmd/yagura-tray は token が無いとき
//	"tray-no-token-placeholder" という **偽の資格情報を注入** して、この検証を
//	すり抜けようとしていた。しかもその偽 token は looksLikeGitHubToken に弾かれるので
//	daemon は結局起動せず、**README が薦める「端末不要」の導線が壊れていた**。
//	要求を回避する仕掛けが製品内に生えたら、その要求の方が間違っている。
func TestLoad_GitHubTokenIsOptional(t *testing.T) {
	t.Setenv("YAGURA_GITHUB_TOKEN", "")
	t.Setenv("YAGURA_STATE_DIR", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("a missing GitHub token must not prevent startup: %v", err)
	}
	if cfg.GitHubToken != "" {
		t.Errorf("want empty token, got %q", cfg.GitHubToken)
	}
	if cfg.GitHubEnabled() {
		t.Error("GitHubEnabled() must be false without a token")
	}
	dis := cfg.DisabledCapabilities()
	if len(dis) == 0 {
		t.Error("the daemon must state which capabilities are disabled, not fail silently")
	}
}

// 一方、**間違った** token は今も error。無いのは選択だが、壊れているのは事故。
func TestLoad_MalformedGitHubTokenIsStillAnError(t *testing.T) {
	t.Setenv("YAGURA_GITHUB_TOKEN", "tray-no-token-placeholder")
	t.Setenv("YAGURA_STATE_DIR", t.TempDir())
	if _, err := Load(); err == nil {
		t.Error("a malformed token must still be reported — it is a mistake, not a choice")
	}
}

func TestLoad_ValidGitHubTokenEnablesCapabilities(t *testing.T) {
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_0123456789abcdef0123456789abcdef0123")
	t.Setenv("YAGURA_STATE_DIR", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GitHubEnabled() {
		t.Error("GitHubEnabled() must be true with a well-formed token")
	}
	if len(cfg.DisabledCapabilities()) != 0 {
		t.Errorf("nothing should be disabled, got %v", cfg.DisabledCapabilities())
	}
}
