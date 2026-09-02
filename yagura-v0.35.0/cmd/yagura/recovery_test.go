// recovery_test.go: 災害復旧シナリオ。daemon が SIGKILL (kill -9) で強制終了されたあと、
// 再起動して state directory の consistency が保たれていることを検証する。
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestRecovery_AfterSIGKILL は次のシナリオをテストする:
//  1. daemon を子プロセスで起動
//  2. プロジェクトを 1 つ登録(disk へ永続化)
//  3. daemon を SIGKILL で強制終了(graceful shutdown を回避)
//  4. 同じ state dir で再起動
//  5. yagura_list で先に登録したプロジェクトが残っていることを確認
//  6. yagura verify で audit log の hash chain が無事なことを確認
//
// SIGKILL は OS-level な強制終了で、daemon は cleanup hook を実行できない。
// ファイル書込み中に殺された場合のデータ損失リスクがあるため、registry の
// atomic write (write→fsync→rename) や audit の append-only fsync が
// 正しく動いていることの間接的検証になる。
func TestRecovery_AfterSIGKILL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery test in -short mode")
	}
	// Windows では SIGKILL の挙動が異なるためスキップ
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL semantics differ on Windows")
	}

	stateDir := t.TempDir()
	addr1, addr2 := freePort(t), freePort(t)

	// ─── 1. 子プロセスで daemon 起動 ───
	bin := buildBinaryForTest(t)
	proc1 := startBinary(t, bin, stateDir, addr1)
	defer cleanupProc(proc1)
	waitForHealthz(t, addr1, 3*time.Second)

	// ─── 2. プロジェクト登録 ───
	mustPost(t, "http://"+addr1+"/mcp", `{
		"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"yagura_register","arguments":{
			"slug":"survivor","display_name":"Survivor","repository":"x/survivor"
		}}
	}`)

	// 登録できたことを確認(disk へ永続化されている前提)
	if !listContains(t, addr1, "survivor") {
		t.Fatal("project not listed after register")
	}

	// ─── 3. SIGKILL ───
	if err := proc1.Process.Signal(os.Kill); err != nil {
		t.Fatalf("SIGKILL: %v", err)
	}
	// プロセス終了を待つ
	_, _ = proc1.Process.Wait()

	// ─── 4. 同じ state dir で再起動(別 port で) ───
	proc2 := startBinary(t, bin, stateDir, addr2)
	defer cleanupProc(proc2)
	waitForHealthz(t, addr2, 3*time.Second)

	// ─── 5. 永続化された project が読み込まれているか ───
	if !listContains(t, addr2, "survivor") {
		t.Errorf("project 'survivor' not present after SIGKILL+restart")
	}

	// ─── 6. audit log が integrity 保持 ───
	stop2 := stopBinary(t, proc2)
	_ = stop2
	verifyAuditLogIntegrity(t, bin, stateDir)
}

// ─── helpers ───

func buildBinaryForTest(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "yagura-test")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build: %v", err)
	}
	return out
}

func startBinary(t *testing.T, bin, stateDir, addr string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"YAGURA_GITHUB_TOKEN=ghp_recovery_test_dummy",
		"YAGURA_STATE_DIR="+stateDir,
		"YAGURA_ADDR="+addr,
		"YAGURA_SCAN_INTERVAL=1h",
		"YAGURA_SECURITY_SCAN_INTERVAL=24h",
		"YAGURA_LOG_LEVEL=error",
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func cleanupProc(c *exec.Cmd) {
	if c.Process != nil {
		_ = c.Process.Kill()
		_, _ = c.Process.Wait()
	}
}

func stopBinary(t *testing.T, c *exec.Cmd) error {
	t.Helper()
	if c.Process == nil {
		return nil
	}
	if err := c.Process.Signal(os.Interrupt); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { _, err := c.Process.Wait(); done <- err }()
	select {
	case <-done:
		return nil
	case <-time.After(10 * time.Second):
		_ = c.Process.Kill()
		return nil
	}
}

func waitForHealthz(t *testing.T, addr string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon at %s did not become healthy within %v", addr, deadline)
}

func mustPost(t *testing.T, url, body string) []byte {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("post %s status=%d body=%s", url, resp.StatusCode, b)
	}
	return b
}

func listContains(t *testing.T, addr, slug string) bool {
	t.Helper()
	body := mustPost(t, "http://"+addr+"/mcp", `{
		"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"yagura_list","arguments":{}}
	}`)
	var r struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	for _, c := range r.Result.Content {
		if strings.Contains(c.Text, slug) {
			return true
		}
	}
	return false
}

func verifyAuditLogIntegrity(t *testing.T, bin, stateDir string) {
	t.Helper()
	cmd := exec.Command(bin, "verify")
	cmd.Env = append(os.Environ(), "YAGURA_STATE_DIR="+stateDir)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		t.Errorf("verify after SIGKILL recovery failed: %v\nstdout=%s\nstderr=%s",
			err, out.String(), errBuf.String())
	}
	t.Logf("verify output: %s", out.String())
}
