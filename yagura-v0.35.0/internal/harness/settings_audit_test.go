package harness

import (
	"strings"
	"testing"
)

// settingsIssue は Issues のいずれかが substr を含むか(workflow_audit_test の
// hasIssueContaining と同じ用途だが、test バイナリ内で名前衝突しないよう別名)。
func settingsIssue(issues []string, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i, substr) {
			return true
		}
	}
	return false
}

// pristineSettings は yagura 自身の .claude/settings.json 相当(deny + hooks)。
const pristineSettings = `{
  "permissions": {
    "deny": ["Bash(rm -rf*)", "Bash(git push --force*)"],
    "ask": ["Write", "Edit", "Bash"]
  },
  "hooks": {
    "PostToolUse": [
      {"matcher": "Edit|Write", "hooks": [{"type": "command", "command": "gofmt -w x"}]}
    ]
  }
}`

func TestAuditSettings_Pristine(t *testing.T) {
	r := AuditSettings(pristineSettings)
	if !r.ValidJSON {
		t.Fatal("should be valid JSON")
	}
	if !r.HasPermissions || !r.HasDenyList || !r.GuardsDestructive || !r.HasHooks {
		t.Errorf("pristine settings should have permissions+deny+destructive-guard+hooks: %+v", r)
	}
	if r.HasUnrestrictedAllow {
		t.Error("pristine settings has no unrestricted allow")
	}
	if r.Score < 90 {
		t.Errorf("pristine settings should score >=90, got %d (issues: %v)", r.Score, r.Issues)
	}
}

func TestAuditSettings_InvalidJSON(t *testing.T) {
	r := AuditSettings("{ not valid json,, }")
	if r.ValidJSON {
		t.Error("should detect invalid JSON")
	}
	if r.Score != 0 {
		t.Errorf("invalid JSON should score 0, got %d", r.Score)
	}
	if !settingsIssue(r.Issues, "invalid JSON") {
		t.Errorf("expected invalid-JSON issue, got %v", r.Issues)
	}
}

func TestAuditSettings_NoPermissions(t *testing.T) {
	r := AuditSettings(`{"hooks": {"Stop": []}}`)
	if r.HasPermissions {
		t.Error("should report no permissions block")
	}
	if !settingsIssue(r.Issues, "no 'permissions' block") {
		t.Errorf("expected no-permissions issue, got %v", r.Issues)
	}
}

func TestAuditSettings_EmptyDeny(t *testing.T) {
	r := AuditSettings(`{"permissions": {"allow": ["Bash(go test *)"]}, "hooks": {"Stop": []}}`)
	if r.HasDenyList {
		t.Error("should report empty deny list")
	}
	if !settingsIssue(r.Issues, "empty deny list") {
		t.Errorf("expected empty-deny issue, got %v", r.Issues)
	}
}

func TestAuditSettings_DenyWithoutRmGuard(t *testing.T) {
	r := AuditSettings(`{"permissions": {"deny": ["Bash(git push --force*)"]}, "hooks": {"Stop": []}}`)
	if !r.HasDenyList {
		t.Error("should detect a deny list")
	}
	if r.GuardsDestructive {
		t.Error("deny without rm -rf should not count as guarding destructive")
	}
	if !settingsIssue(r.Issues, "rm -rf") {
		t.Errorf("expected rm -rf guard issue, got %v", r.Issues)
	}
}

func TestAuditSettings_UnrestrictedAllow(t *testing.T) {
	r := AuditSettings(`{"permissions": {"allow": ["Bash(*)"], "deny": ["Bash(rm -rf *)"]}, "hooks": {"Stop": []}}`)
	if !r.HasUnrestrictedAllow {
		t.Error("should detect unrestricted Bash allow")
	}
	if !settingsIssue(r.Issues, "unrestricted Bash allow") {
		t.Errorf("expected unrestricted-allow issue, got %v", r.Issues)
	}
}

func TestAuditSettings_ScopedAllowOK(t *testing.T) {
	// Edit(/docs/**) や Bash(go test *) のような scope 付き allow は flag しない。
	r := AuditSettings(`{"permissions": {"allow": ["Edit(/docs/**)", "Bash(go test *)"], "deny": ["Bash(rm -rf *)"]}, "hooks": {"Stop": []}}`)
	if r.HasUnrestrictedAllow {
		t.Errorf("scoped allow rules should not be flagged: %+v", r)
	}
}

func TestAuditSettings_NoHooksSuggestion(t *testing.T) {
	r := AuditSettings(`{"permissions": {"deny": ["Bash(rm -rf *)"]}}`)
	if r.HasHooks {
		t.Error("should report no hooks")
	}
	if !settingsIssue(r.Suggestions, "format-on-edit hook") {
		t.Errorf("expected hooks suggestion, got %v", r.Suggestions)
	}
}

func TestAuditSettings_DefaultAgent(t *testing.T) {
	r := AuditSettings(`{"agent": "yagura-reviewer", "permissions": {"deny": ["Bash(rm -rf *)"]}, "hooks": {"Stop": []}}`)
	if !r.HasDefaultAgent {
		t.Error("should detect default agent field")
	}
}
