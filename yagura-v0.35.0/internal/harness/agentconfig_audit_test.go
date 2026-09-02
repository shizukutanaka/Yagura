package harness

import (
	"strings"
	"testing"
)

func acIssue(issues []string, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i, substr) {
			return true
		}
	}
	return false
}

// cleanAgentConfig: 参照解決済み・secure・context 整合の理想 config。
const cleanAgentConfig = `{
  "gateway": {
    "mode": "local",
    "bind": "loopback",
    "auth": {"mode": "token", "token": "f3a9c1e7b245d8901aa6"},
    "controlUi": {"allowedOrigins": ["http://127.0.0.1:18789"]}
  },
  "agents": {
    "defaults": {
      "model": {"primary": "vllm/qwen3.6-27b"},
      "models": {"vllm/qwen3.6-27b": {"alias": "Qwen"}},
      "compaction": {"mode": "safeguard", "reserveTokensFloor": 50000}
    }
  },
  "models": {
    "providers": {
      "vllm": {
        "baseUrl": "http://192.168.11.100:8888/v1",
        "apiKey": "EMPTY",
        "models": [
          {"id": "qwen3.6-27b", "name": "Qwen", "input": ["text", "image"], "contextWindow": 262144, "maxTokens": 32768}
        ]
      }
    }
  }
}`

func TestAuditAgentConfig_Clean(t *testing.T) {
	r := AuditAgentConfig(cleanAgentConfig)
	if !r.ValidJSON {
		t.Fatal("should be valid JSON")
	}
	if !r.PrimaryResolves {
		t.Errorf("primary should resolve, got %+v", r)
	}
	if r.DeviceAuthDisabled || r.BrowserNoSandbox || len(r.HardcodedKeys) != 0 || !r.CompactionSafe {
		t.Errorf("clean config should have no security/compaction flags: %+v", r)
	}
	if r.Score < 90 {
		t.Errorf("clean config should score >=90, got %d (issues %v)", r.Score, r.Issues)
	}
}

func TestAuditAgentConfig_InvalidJSON(t *testing.T) {
	r := AuditAgentConfig(`{ "gateway": , }`)
	if r.ValidJSON || r.Score != 0 {
		t.Errorf("invalid JSON → score 0, got %+v", r)
	}
	if !acIssue(r.Issues, "invalid JSON") {
		t.Errorf("expected invalid-JSON issue, got %v", r.Issues)
	}
}

func TestAuditAgentConfig_DanglingPrimary(t *testing.T) {
	src := `{
      "agents": {"defaults": {"model": {"primary": "vllm/not-declared"}}},
      "models": {"providers": {"vllm": {"apiKey": "EMPTY", "models": [{"id": "qwen3.6-27b", "contextWindow": 262144, "maxTokens": 32768}]}}}
    }`
	r := AuditAgentConfig(src)
	if r.PrimaryResolves {
		t.Error("primary should not resolve")
	}
	if !acIssue(r.Issues, "does not resolve") {
		t.Errorf("expected dangling-primary issue, got %v", r.Issues)
	}
}

func TestAuditAgentConfig_ContextMisconfig(t *testing.T) {
	src := `{
      "agents": {"defaults": {"model": {"primary": "p/m"}}},
      "models": {"providers": {"p": {"apiKey": "EMPTY", "models": [{"id": "m", "contextWindow": 8000, "maxTokens": 8000}]}}}
    }`
	r := AuditAgentConfig(src)
	if !acIssue(r.Issues, "maxTokens") {
		t.Errorf("expected maxTokens>=contextWindow issue, got %v", r.Issues)
	}
}

func TestAuditAgentConfig_HardcodedKey(t *testing.T) {
	src := `{
      "agents": {"defaults": {"model": {"primary": "cloud/m"}}},
      "models": {"providers": {"cloud": {"apiKey": "sk-proj-abcdef0123456789", "models": [{"id": "m", "contextWindow": 1000000, "maxTokens": 32768}]}}}
    }`
	r := AuditAgentConfig(src)
	if len(r.HardcodedKeys) != 1 || r.HardcodedKeys[0] != "cloud" {
		t.Errorf("expected hardcoded key for 'cloud', got %+v", r.HardcodedKeys)
	}
	if !acIssue(r.Issues, "hardcoded API key") {
		t.Errorf("expected hardcoded-key issue, got %v", r.Issues)
	}
}

func TestAuditAgentConfig_PlaceholderKeyOK(t *testing.T) {
	// "API-KEY" and "EMPTY" are placeholders — must not be flagged.
	src := `{
      "agents": {"defaults": {"model": {"primary": "a/m"}}},
      "models": {"providers": {
        "a": {"apiKey": "EMPTY", "models": [{"id": "m", "contextWindow": 100, "maxTokens": 50}]},
        "b": {"apiKey": "API-KEY", "models": [{"id": "n", "contextWindow": 100, "maxTokens": 50}]}
      }}
    }`
	r := AuditAgentConfig(src)
	if len(r.HardcodedKeys) != 0 {
		t.Errorf("placeholder keys must not be flagged, got %+v", r.HardcodedKeys)
	}
}

func TestAuditAgentConfig_CompactionFloorTooSmall(t *testing.T) {
	src := `{
      "agents": {"defaults": {"model": {"primary": "a/m"}, "compaction": {"mode": "safeguard", "reserveTokensFloor": 5000}}},
      "models": {"providers": {"a": {"apiKey": "EMPTY", "models": [{"id": "m", "contextWindow": 100000, "maxTokens": 8000}]}}}
    }`
	r := AuditAgentConfig(src)
	if r.CompactionSafe {
		t.Error("reserveTokensFloor 5000 should be flagged unsafe")
	}
	if !acIssue(r.Issues, "reserveTokensFloor") {
		t.Errorf("expected compaction floor issue, got %v", r.Issues)
	}
}

func TestAuditAgentConfig_SecurityFlags(t *testing.T) {
	src := `{
      "gateway": {"auth": {"mode": "token", "token": "openclaw-local-token-123"}, "controlUi": {"dangerouslyDisableDeviceAuth": true}},
      "browser": {"enabled": true, "noSandbox": true},
      "agents": {"defaults": {"model": {"primary": "a/m"}}},
      "models": {"providers": {"a": {"apiKey": "EMPTY", "models": [{"id": "m", "contextWindow": 100000, "maxTokens": 8000}]}}}
    }`
	r := AuditAgentConfig(src)
	if !r.DeviceAuthDisabled || !r.BrowserNoSandbox {
		t.Errorf("expected device-auth + noSandbox flags, got %+v", r)
	}
	if !acIssue(r.Issues, "dangerouslyDisableDeviceAuth") || !acIssue(r.Issues, "noSandbox") {
		t.Errorf("expected security issues, got %v", r.Issues)
	}
	if !acIssue(r.Issues, "weak/example") {
		t.Errorf("expected weak-token issue, got %v", r.Issues)
	}
}

// ─── remaining branch coverage ───────────────────────────────

func TestAuditAgentConfig_NoProviders(t *testing.T) {
	r := AuditAgentConfig(`{"models":{"providers":{}}}`)
	if r.ProviderCount != 0 {
		t.Fatalf("ProviderCount = %d, want 0", r.ProviderCount)
	}
	if !acIssue(r.Issues, "no model providers") {
		t.Errorf("expected no-providers issue, got %v", r.Issues)
	}
}

func TestAuditAgentConfig_PrimaryUnset(t *testing.T) {
	src := `{"models":{"providers":{"vllm":{"apiKey":"EMPTY","models":[{"id":"m","contextWindow":1000,"maxTokens":100}]}}}}`
	r := AuditAgentConfig(src)
	if r.PrimaryResolves {
		t.Error("unset primary must not resolve")
	}
	if !acIssue(r.Issues, "primary is unset") {
		t.Errorf("expected unset-primary issue, got %v", r.Issues)
	}
}

func TestAuditAgentConfig_DanglingMenuModels(t *testing.T) {
	// agents.defaults.models lists a ref that no provider declares.
	src := `{
	  "agents": {"defaults": {
	    "model": {"primary": "vllm/m"},
	    "models": {"vllm/m": {}, "ghost/nope": {}}
	  }},
	  "models": {"providers": {"vllm": {"apiKey": "EMPTY", "models": [{"id": "m", "contextWindow": 1000, "maxTokens": 100}]}}}
	}`
	r := AuditAgentConfig(src)
	if !acIssue(r.Issues, "unresolved model ref") {
		t.Errorf("expected dangling menu-model issue, got %v", r.Issues)
	}
	if !acIssue(r.Issues, "ghost/nope") {
		t.Errorf("issue should name the dangling ref, got %v", r.Issues)
	}
}

func TestAuditAgentConfig_UnknownInputModality(t *testing.T) {
	src := `{
	  "agents": {"defaults": {"model": {"primary": "p/m"}}},
	  "models": {"providers": {"p": {"apiKey": "EMPTY", "models": [
	    {"id": "m", "input": ["text", "smell"], "contextWindow": 1000, "maxTokens": 100}
	  ]}}}
	}`
	r := AuditAgentConfig(src)
	found := false
	for _, s := range r.Suggestions {
		if strings.Contains(s, "smell") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected suggestion about unrecognized modality, got %v", r.Suggestions)
	}
}

func TestAuditAgentConfig_CompactionFloorUnset(t *testing.T) {
	// mode set but reserveTokensFloor missing → distinct issue from too-small.
	src := `{
	  "agents": {"defaults": {
	    "model": {"primary": "p/m"},
	    "compaction": {"mode": "safeguard"}
	  }},
	  "models": {"providers": {"p": {"apiKey": "EMPTY", "models": [{"id": "m", "contextWindow": 1000, "maxTokens": 100}]}}}
	}`
	r := AuditAgentConfig(src)
	if r.CompactionSafe {
		t.Error("mode without floor should clear CompactionSafe")
	}
	if !acIssue(r.Issues, "reserveTokensFloor is unset") {
		t.Errorf("expected floor-unset issue, got %v", r.Issues)
	}
}

func TestAuditAgentConfig_WeakGatewayToken(t *testing.T) {
	src := `{
	  "gateway": {"auth": {"mode": "token", "token": "test123"}},
	  "agents": {"defaults": {"model": {"primary": "p/m"}}},
	  "models": {"providers": {"p": {"apiKey": "EMPTY", "models": [{"id": "m", "contextWindow": 1000, "maxTokens": 100}]}}}
	}`
	r := AuditAgentConfig(src)
	if !acIssue(r.Issues, "looks weak") {
		t.Errorf("expected weak-token issue, got %v", r.Issues)
	}
}

// ─── isRealAPIKey / isWeakToken edges ────────────────────────

func TestIsRealAPIKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"", false},
		{"EMPTY", false},
		{"changeme", false},        // placeholder (case-insensitive)
		{"sk-short", true},         // known prefix beats length
		{"xai-k", true},            // known prefix
		{"abcdefghijklmnop", true}, // exactly 16 chars, no prefix
		{"shortrandom", false},     // < 16, no known prefix
	}
	for _, c := range cases {
		if got := isRealAPIKey(c.key); got != c.want {
			t.Errorf("isRealAPIKey(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

func TestIsWeakToken(t *testing.T) {
	cases := []struct {
		tok  string
		want bool
	}{
		{"", false},                     // empty is a different check's concern
		{"local-token-123", true},       // example marker
		{"PASSWORD-abcdef", true},       // weak word
		{"short", true},                 // < 12 chars
		{"f3a9c1e7b245d8901aa6", false}, // long random
	}
	for _, c := range cases {
		if got := isWeakToken(c.tok); got != c.want {
			t.Errorf("isWeakToken(%q) = %v, want %v", c.tok, got, c.want)
		}
	}
}

// TestAuditAgentConfig_ContextWindowZero covers the `m.ContextWindow <= 0` branch
// (r.Score -= 8 for "contextWindow unset/zero").
func TestAuditAgentConfig_ContextWindowZero(t *testing.T) {
	src := `{
	  "agents": {"defaults": {"model": {"primary": "p/m"}}},
	  "models": {"providers": {"p": {"apiKey": "EMPTY", "models": [{"id": "m", "contextWindow": 0, "maxTokens": 100}]}}}
	}`
	r := AuditAgentConfig(src)
	if !acIssue(r.Issues, "contextWindow unset/zero") {
		t.Errorf("contextWindow=0 should flag contextWindow issue, got %v", r.Issues)
	}
}
