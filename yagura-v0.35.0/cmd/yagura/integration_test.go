// integration_test.go: daemon の HTTP smoke test。
//
// 実 daemon プロセスを起動する代わりに、main package で組み上げた構成を
// in-process で起動し、エンドポイントを叩いて返答を検証する。
//
// 短時間 (<5 sec) で完了するよう scanner interval を大きくし、context cancel
// で即時停止する。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// freePort は利用可能な TCP port を返す。
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	port := addr.Port
	_ = l.Close()
	return "127.0.0.1:" + itoa(port)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// startDaemon は daemon を別 goroutine で起動し、ready になったら return する。
// shutdown チャネルを close すると daemon が止まる(t.Cleanup で自動)。
func startDaemon(t *testing.T) (addr string) {
	t.Helper()
	addr = freePort(t)
	t.Setenv("YAGURA_ADDR", addr)
	t.Setenv("YAGURA_STATE_DIR", t.TempDir())
	t.Setenv("YAGURA_GITHUB_TOKEN", "ghp_integration_test_dummy_token")
	t.Setenv("YAGURA_SCAN_INTERVAL", "1h")
	t.Setenv("YAGURA_SECURITY_SCAN_INTERVAL", "24h")
	t.Setenv("YAGURA_LOG_LEVEL", "error") // テスト中のログ抑制

	errCh := make(chan error, 1)
	go func() {
		errCh <- run()
	}()

	// readyz が 200 になるまで待つ(最大 3 秒)
	deadline := time.Now().Add(3 * time.Second)
	url := "http://" + addr + "/healthz"
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Cleanup(func() {
		// SIGTERM 相当: グローバル shutdown 機構を呼ぶことは困難なので、HTTP server を
		// テスト終了直後に止めるには process kill か signal が必要。
		// 代替: daemon は run() 内で SIGTERM/SIGINT を待つので、テストプロセスに
		// 自己 SIGTERM を送る代わり、ここでは context cancellation を別途仕込んでも良いが
		// 今は最小実装として errCh を読み捨てる。
		select {
		case <-errCh:
		case <-time.After(100 * time.Millisecond):
		}
	})

	return addr
}

// ─── tests ───────────────────────────────────────────────────

// TestIntegration_Healthz は /healthz が 200 を返すことを確認する。
//
// このテストだけは daemon の起動と /healthz チェックで完結するので
// shutdown が綺麗でなくても test は成功する。後続テストとは別 Run。
func TestIntegration_Healthz(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	addr := startDaemon(t)
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("healthz status: %d", resp.StatusCode)
	}
}

// TestIntegration_MCPToolsList は /mcp の tools/list が 12 ツール返すことを検証。
func TestIntegration_MCPToolsList(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	addr := startDaemon(t)
	body := bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	resp, err := http.Post("http://"+addr+"/mcp", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var r struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	// 必要な tool が登録されているか確認
	// expectedTools が source of truth。tool 追加時はここに名前を追記するだけで OK。
	expectedTools := []string{
		"yagura_list", "yagura_get", "yagura_search", "yagura_today",
		"yagura_register", "yagura_unregister", "yagura_update", "yagura_stats",
		"yagura_vulns", "yagura_scorecard", "yagura_health", "yagura_secretscan",
		"yagura_sbom", "yagura_gha_audit", "yagura_pin_drift",
		// v0.13.0+: agent handoff
		"yagura_quota_report", "yagura_agent_status",
		"yagura_session_save", "yagura_session_load", "yagura_handoff",
		"yagura_heartbeat", "yagura_quota_forecast", "yagura_usage_summary",
		// v0.17+
		"yagura_token_stats",
		// v0.18+ — graph
		"yagura_graph_neighbors", "yagura_graph_impact", "yagura_graph_stats",
		// v0.19+ — harness scaffolding + quality
		"yagura_harness_recommend", "yagura_skill_audit", "yagura_subagent_audit",
		"yagura_quality_check",
		// v0.22-23+ — meta + dedupe
		"yagura_tools_catalog", "yagura_dedupe_stats",
		// v0.24+ — Plan.md + release radar
		"yagura_plan_status", "yagura_release_radar",
		// v0.25-26 — AI verify + test audit
		"yagura_ai_verify", "yagura_test_audit",
		// v0.36 — Go AST structural audit (Roadmap #6)
		"yagura_ast_check",
		// v0.36 — test assertion density (hollow test detection)
		"yagura_assert_check",
		// v0.36 — error-context discipline (wrap ratio + blank-discard)
		"yagura_err_policy",
		// v0.36 — cyclomatic complexity (testability precondition)
		"yagura_complexity",
		// v0.36 — package import coupling (architecture / SDP)
		"yagura_coupling",
		// v0.36 — exported-API doc discipline (public contract)
		"yagura_api_doc",
		// v0.27 — cortex flywheel ④ Alert-Fix
		"yagura_alert_fix", "yagura_alert_resolve", "yagura_agents_md", "yagura_feature_list", "yagura_harness_coverage",
		"yagura_hook_timeline", "yagura_hook_stats", "yagura_progress_file", "yagura_init_sh",
		// v0.35 — 複数 AI を使った処理の並列化 + Cyber Risk Reasoning Layer
		"yagura_parallel_plan", "yagura_risk_triage",
		// v0.35 — .claude/ artifact 監査(content-based, MCP surface)
		"yagura_workflow_audit", "yagura_settings_audit", "yagura_agent_config_audit",
		"yagura_plugin_audit", "yagura_publicity_scan", "yagura_mcp_audit",
		// v0.35 — self-healing orchestration の recovery 判断
		"yagura_recovery_decide",
		// v0.35 — OpenVEX v0.2.0 文書生成 + lint
		"yagura_vex",
		// v0.35 — harness レベル再帰的自己改善(RSI)カーネル
		"yagura_self_improve",
		// v0.35 — 変更パスの policy gate
		"yagura_path_policy",
		// v0.35 — 操作の autonomy tier 分類
		"yagura_ops_risk",
		// v0.35 — 間接プロンプトインジェクション検出
		"yagura_inject_scan",
		// v0.35 — 任意エージェントの event 正規化(OTel GenAI semconv)
		"yagura_agent_event",
		// v0.35 — エージェントセッションの構造化サマリ
		"yagura_session_summary",
	}
	if len(r.Result.Tools) != len(expectedTools) {
		t.Errorf("expected %d tools, got %d", len(expectedTools), len(r.Result.Tools))
	}

	names := map[string]bool{}
	for _, tool := range r.Result.Tools {
		if n, ok := tool["name"].(string); ok {
			names[n] = true
		}
	}
	for _, n := range expectedTools {
		if !names[n] {
			t.Errorf("missing tool: %s", n)
		}
	}
}

// TestIntegration_Dashboard は /dashboard が、登録ありで table を含む HTML を返すことを検証。
//
// empty portfolio では table 自体描画されない仕様のため、まずプロジェクトを登録する。
func TestIntegration_Dashboard(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	addr := startDaemon(t)

	// 1 件登録 — table がレンダリングされるため
	registerReq := `{
		"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"yagura_register","arguments":{
			"slug":"dashp",
			"display_name":"Dashboard test",
			"repository":"example/dashp"
		}}
	}`
	resp1, err := http.Post("http://"+addr+"/mcp", "application/json", strings.NewReader(registerReq))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp1.Body.Close()

	resp, err := http.Get("http://" + addr + "/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"Yagura", "<table", "Security", "Stage", "dashp"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

// TestIntegration_RegisterAndScan は登録 → secret scan の往復を検証。
func TestIntegration_RegisterAndScan(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	addr := startDaemon(t)

	// 1. プロジェクト登録(notes に GitHub PAT らしき文字列を埋める)
	registerReq := `{
		"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"yagura_register","arguments":{
			"slug":"intptest",
			"display_name":"Integration test project",
			"repository":"example/intptest",
			"notes":"TODO: rotate token: ghp_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ"
		}}
	}`
	post := func(body string) (int, []byte) {
		resp, err := http.Post("http://"+addr+"/mcp", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	code, _ := post(registerReq)
	if code != 200 {
		t.Fatalf("register status: %d", code)
	}

	// 2. secret scan を呼んで finding がある
	scanReq := `{
		"jsonrpc":"2.0","id":2,"method":"tools/call",
		"params":{"name":"yagura_secretscan","arguments":{"slug":"intptest"}}
	}`
	code, body := post(scanReq)
	if code != 200 {
		t.Fatalf("scan status: %d", code)
	}
	if !strings.Contains(string(body), "github-personal-token") {
		t.Errorf("expected to detect github-personal-token, got: %s", string(body))
	}
	if !strings.Contains(string(body), "REDACTED") {
		t.Errorf("secret should be redacted in finding")
	}
}

// TestIntegration_AuditChainAcrossRestart は daemon shutdown 後の audit log が
// 整合性を保つことを確認する(verify subcommand)。
func TestIntegration_VerifySubcommand(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	dir := t.TempDir()
	t.Setenv("YAGURA_STATE_DIR", dir)

	// verify は state が空でも success(audit ディレクトリなし or 空)
	var out, errBuf bytes.Buffer
	code := dispatch([]string{"verify"}, &out, &errBuf)
	if code != 0 {
		t.Errorf("verify empty state: code=%d err=%s", code, errBuf.String())
	}
}

// _ = errors を使う(unused import suppression)
var _ = errors.New
var _ context.Context
var _ = os.Stdin

// ─── v0.28: New tool smoke tests ─────────────────────────────────
//
// v0.24+ で追加された 5 tools (plan_status / release_radar / ai_verify /
// test_audit / alert_fix) の MCP 経由 smoke。各 tool が JSON-RPC 経由で
// expected 構造を返すことを network 層から検証する。

func TestIntegration_AIVerify_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	addr := startDaemon(t)

	// AI risk pattern を含む code を投げる
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"yagura_ai_verify","arguments":{"text":"// AI generated\nconst hash = md5(password);","path":"x.js","summary_only":true}}}`
	resp := mcpCall(t, addr, payload)
	if !strings.Contains(resp, "risk_score") {
		t.Errorf("ai_verify response missing risk_score: %s", resp)
	}
	if !strings.Contains(resp, "by_severity") {
		t.Errorf("ai_verify response missing by_severity: %s", resp)
	}
}

func TestIntegration_TestAudit_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	addr := startDaemon(t)

	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"yagura_test_audit","arguments":{"files":{"a.go":"package x","a_test.go":"package x","b.go":"package x"}}}}`
	resp := mcpCall(t, addr, payload)
	if !strings.Contains(resp, "coverage_ratio") {
		t.Errorf("test_audit response missing coverage_ratio: %s", resp)
	}
	if !strings.Contains(resp, "untested_files") {
		t.Errorf("test_audit response missing untested_files: %s", resp)
	}
}

func TestIntegration_AlertFix_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	addr := startDaemon(t)

	// portfolio 空でも alert_fix が動くことを確認(0 alerts を返す)
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"yagura_alert_fix","arguments":{}}}`
	resp := mcpCall(t, addr, payload)
	if !strings.Contains(resp, "by_severity") {
		t.Errorf("alert_fix response missing by_severity: %s", resp)
	}
	if !strings.Contains(resp, "projects_scanned") {
		t.Errorf("alert_fix response missing projects_scanned: %s", resp)
	}
}

func TestIntegration_PlanStatus_NotFoundError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	addr := startDaemon(t)

	// 存在しない slug → not_found error
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"yagura_plan_status","arguments":{"slug":"does-not-exist"}}}`
	resp := mcpCall(t, addr, payload)
	if !strings.Contains(resp, "not_found") && !strings.Contains(resp, "error") {
		t.Errorf("plan_status missing slug should error: %s", resp)
	}
}

func TestIntegration_ReleaseRadar_EmptyPortfolio(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	addr := startDaemon(t)

	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"yagura_release_radar","arguments":{}}}`
	resp := mcpCall(t, addr, payload)
	if !strings.Contains(resp, "total_projects") {
		t.Errorf("release_radar response missing total_projects: %s", resp)
	}
}

// mcpCall は MCP JSON-RPC POST + body 文字列を返す(test helper)。
// startDaemon は token を設定していないので Authorization header は不要。
func mcpCall(t *testing.T, addr, payload string) string {
	t.Helper()
	req, err := http.NewRequest("POST", "http://"+addr+"/mcp", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}
