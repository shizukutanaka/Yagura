package aiverify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ─── LoadUserConfig ───────────────────────────────────────────

func TestLoadUserConfig_HappyPath(t *testing.T) {
	cfg := &UserConfig{
		Rules: []UserRule{
			{ID: "custom-1", Pattern: `panic\(`, Category: "external", Risk: "HIGH", Message: "explicit panic"},
		},
		Disable: []string{"billing-stripe-uncaught"},
	}
	raw, _ := json.Marshal(cfg)
	f := filepath.Join(t.TempDir(), "aiverify.json")
	_ = os.WriteFile(f, raw, 0o644)

	got, err := LoadUserConfig(f)
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if len(got.Rules) != 1 {
		t.Errorf("expected 1 custom rule, got %d", len(got.Rules))
	}
	if len(got.Disable) != 1 || got.Disable[0] != "billing-stripe-uncaught" {
		t.Errorf("disable list: %v", got.Disable)
	}
}

// TestLoadUserConfig_MissingFile distinguishes ErrNotExist from an I/O error:
// a missing file returns a descriptive error, not a nil config.
func TestLoadUserConfig_MissingFile(t *testing.T) {
	_, err := LoadUserConfig(filepath.Join(t.TempDir(), "nofile.json"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadUserConfig_MalformedJSON(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(f, []byte("{not json"), 0o644)
	_, err := LoadUserConfig(f)
	if err == nil {
		t.Error("expected error for malformed JSON")
	}
}

func TestLoadUserConfig_EmptyFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "empty.json")
	_ = os.WriteFile(f, []byte(`{}`), 0o644)
	got, err := LoadUserConfig(f)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Rules) != 0 || len(got.Disable) != 0 {
		t.Error("empty config should have zero rules and disables")
	}
}

// ─── UserConfig.Apply ─────────────────────────────────────────

func TestApply_AddsCustomRule(t *testing.T) {
	cfg := &UserConfig{
		Rules: []UserRule{
			{ID: "my-panic", Pattern: `panic\(`, Category: "external", Risk: "HIGH", Message: "explicit panic"},
		},
	}
	merged, err := cfg.Apply(DefaultRules())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	var found bool
	for _, r := range merged {
		if r.ID == "my-panic" {
			found = true
		}
	}
	if !found {
		t.Error("custom rule 'my-panic' missing after Apply")
	}
	if len(merged) <= len(DefaultRules()) {
		t.Error("merged set should be larger than defaults")
	}
}

func TestApply_DisablesDefaultRule(t *testing.T) {
	cfg := &UserConfig{
		Disable: []string{"billing-stripe-uncaught"},
	}
	merged, err := cfg.Apply(DefaultRules())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, r := range merged {
		if r.ID == "billing-stripe-uncaught" {
			t.Error("disabled rule 'billing-stripe-uncaught' still present after Apply")
		}
	}
}

func TestApply_BadPattern(t *testing.T) {
	cfg := &UserConfig{
		Rules: []UserRule{
			{ID: "bad", Pattern: `[invalid`, Category: "external", Risk: "HIGH", Message: "bad regex"},
		},
	}
	_, err := cfg.Apply(DefaultRules())
	if err == nil {
		t.Error("expected error for invalid regex pattern")
	}
}

func TestApply_UnknownCategory_Preserved(t *testing.T) {
	// Unknown category strings pass through (forward-compatible).
	cfg := &UserConfig{
		Rules: []UserRule{
			{ID: "custom-new-cat", Pattern: `mypattern`, Category: "custom", Risk: "LOW", Message: "test"},
		},
	}
	merged, err := cfg.Apply(DefaultRules())
	if err != nil {
		t.Fatalf("Apply with unknown category: %v", err)
	}
	var found bool
	for _, r := range merged {
		if r.ID == "custom-new-cat" {
			found = true
			if r.Category != "custom" {
				t.Errorf("category not preserved: %q", r.Category)
			}
		}
	}
	if !found {
		t.Error("custom rule not in merged set")
	}
}

func TestApply_EmptyConfig_ReturnsCopyOfDefaults(t *testing.T) {
	cfg := &UserConfig{}
	merged, err := cfg.Apply(DefaultRules())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(merged) != len(DefaultRules()) {
		t.Errorf("empty config should preserve all defaults: want %d, got %d",
			len(DefaultRules()), len(merged))
	}
}

func TestApply_MissingID_Error(t *testing.T) {
	cfg := &UserConfig{
		Rules: []UserRule{
			{ID: "", Pattern: `foo`, Category: "external", Risk: "LOW", Message: "no id"},
		},
	}
	_, err := cfg.Apply(DefaultRules())
	if err == nil {
		t.Error("expected error for empty rule ID")
	}
}

func TestApply_MissingPattern_Error(t *testing.T) {
	cfg := &UserConfig{
		Rules: []UserRule{
			{ID: "ok-id", Pattern: "", Category: "external", Risk: "LOW", Message: "no pattern"},
		},
	}
	_, err := cfg.Apply(DefaultRules())
	if err == nil {
		t.Error("expected error for empty rule pattern")
	}
}

// TestApply_CustomRuleDetects verifies a user rule actually fires on scan.
func TestApply_CustomRuleDetects(t *testing.T) {
	cfg := &UserConfig{
		Rules: []UserRule{
			{ID: "custom-todo-strict", Pattern: `STRICT_TODO`, Category: "data", Risk: "MEDIUM", Message: "strict todo"},
		},
	}
	rules, err := cfg.Apply(DefaultRules())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	findings := ScanText("x.go", "// STRICT_TODO: fix this", rules)
	var found bool
	for _, f := range findings {
		if f.RuleID == "custom-todo-strict" {
			found = true
		}
	}
	if !found {
		t.Error("custom rule 'custom-todo-strict' did not fire on matching content")
	}
}
