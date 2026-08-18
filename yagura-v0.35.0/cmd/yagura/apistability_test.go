// apistability_test.go: v1.0.0 の **後方互換の約束を機械で守る**。
//
// なぜ散文ではなく test なのか:
//
//	v1.0.0 は「MCP tool 名とその入力を壊さない」という約束である。約束を README に
//	書くだけなら、次のリファクタで静かに破れる——このリポジトリは v0.129.0 で実際に
//	29 個の tool を消しており、それが許されたのは 1.0 **前** だったからに過ぎない。
//	以後は消せない。だから golden list を置き、消失と改名を CI で落とす。
//
//	**追加は許す**(集合の増加は互換を壊さない)。落ちるのは削除と改名だけ。
//	意図的に破壊的変更を行う場合は major を上げ、このリストを更新すること——
//	更新という手作業が「今あなたは約束を破っている」という自覚を強制する。
package main

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/mcp"
	"github.com/shizukutanaka/yagura/internal/registry"
)

// v1ToolNames は v1.0.0 が公開した MCP tool の全名称。**削ってはならない。**
var v1ToolNames = []string{
	"yagura_agent_config_audit",
	"yagura_agent_event",
	"yagura_agent_status",
	"yagura_agents_md",
	"yagura_ai_verify",
	"yagura_alert_fix",
	"yagura_alert_resolve",
	"yagura_alert_snapshot",
	"yagura_cc_security",
	"yagura_change_coupling",
	"yagura_churn_risk",
	"yagura_claudemd_audit",
	"yagura_code_health",
	"yagura_coverage",
	"yagura_dedupe_stats",
	"yagura_defect_dataset",
	"yagura_diff_scan",
	"yagura_feature_list",
	"yagura_flow_risk",
	"yagura_get",
	"yagura_gha_audit",
	"yagura_graph_impact",
	"yagura_graph_neighbors",
	"yagura_graph_stats",
	"yagura_handoff",
	"yagura_harness_coverage",
	"yagura_harness_recommend",
	"yagura_health",
	"yagura_heartbeat",
	"yagura_hook_stats",
	"yagura_hook_timeline",
	"yagura_init_sh",
	"yagura_inject_scan",
	"yagura_lens",
	"yagura_list",
	"yagura_mcp_audit",
	"yagura_ops_risk",
	"yagura_ownership",
	"yagura_parallel_plan",
	"yagura_path_policy",
	"yagura_pin_drift",
	"yagura_plan_status",
	"yagura_plugin_audit",
	"yagura_portfolio_quality",
	"yagura_process_risk",
	"yagura_progress_file",
	"yagura_publicity_scan",
	"yagura_quality_check",
	"yagura_quota_forecast",
	"yagura_quota_report",
	"yagura_recovery_decide",
	"yagura_register",
	"yagura_regress",
	"yagura_release_radar",
	"yagura_review_gate",
	"yagura_risk_triage",
	"yagura_sbom",
	"yagura_scorecard",
	"yagura_search",
	"yagura_secretscan",
	"yagura_self_improve",
	"yagura_self_improve_history",
	"yagura_session_load",
	"yagura_session_save",
	"yagura_session_summary",
	"yagura_settings_audit",
	"yagura_skill_audit",
	"yagura_stats",
	"yagura_subagent_audit",
	"yagura_test_audit",
	"yagura_today",
	"yagura_token_stats",
	"yagura_tools_catalog",
	"yagura_unregister",
	"yagura_update",
	"yagura_usage_summary",
	"yagura_vex",
	"yagura_vulns",
	"yagura_workflow_audit",
}

func TestAPIStability_V1ToolsAreAllStillRegistered(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := mcp.New("", nil)
	mcp.RegisterDefaultTools(s, mcp.Deps{Registry: reg, Now: func() time.Time { return time.Unix(0, 0) }})

	live := map[string]bool{}
	for _, n := range s.ToolNames() {
		live[n] = true
	}
	var missing []string
	for _, n := range v1ToolNames {
		if !live[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d tool(s) promised by v1.0.0 are no longer registered: %s\n"+
			"Removing or renaming a tool breaks every v1 client. If this is deliberate, it is a "+
			"MAJOR version change: bump the major version and update v1ToolNames in the same commit.",
			len(missing), strings.Join(missing, ", "))
	}
}

// 追加は互換を壊さないので許す。ただし **黙って増える** のも避けたいので、
// 増分は報告だけする(fail させない)。
func TestAPIStability_AdditionsAreAllowedAndVisible(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := mcp.New("", nil)
	mcp.RegisterDefaultTools(s, mcp.Deps{Registry: reg, Now: func() time.Time { return time.Unix(0, 0) }})

	promised := map[string]bool{}
	for _, n := range v1ToolNames {
		promised[n] = true
	}
	var added []string
	for _, n := range s.ToolNames() {
		if !promised[n] {
			added = append(added, n)
		}
	}
	if len(added) > 0 {
		sort.Strings(added)
		t.Logf("%d tool(s) added since v1.0.0 (allowed, additive): %s", len(added), strings.Join(added, ", "))
	}
}
