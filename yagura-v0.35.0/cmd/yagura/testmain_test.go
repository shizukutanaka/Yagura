// testmain_test.go: このパッケージのテストが **実 $HOME に触れられないようにする**(v1.2.0)。
//
// なぜ必要か:
//
//	`YAGURA_STATE_DIR` を設定し忘れたコードパスは、既定の `$HOME/.yagura/state` に
//	落ちる。v1.2.0 以前は GitHub token が必須だったため daemon がテスト中に起動できず、
//	その書き込みは **たまたま** 起きていなかった。token を任意にした途端、テスト実行が
//	開発者の本物のホームに audit レコードを書き始め、別のテスト
//	(`TestVerifyAudit_NoStateDirEnv`、既定 state dir が空であることを前提にしていた)が
//	落ちた。
//
//	つまり「テストが実 HOME に依存・書き込みしている」という欠陥は元から在り、
//	必須 token がそれを隠していただけだった。個別のテストを直すのではなく、
//	**パッケージ全体で構造的に不可能にする** ——一箇所で塞げば将来の追加分も守られる。
//
// 注意: t.Setenv は各テスト終了時に環境を復元するので、テスト内で HOME を temp に
// 向けても「テスト外」で走るコード(遅延した goroutine 等)は素の HOME を見る。
// TestMain でプロセス全体に効かせるのはそのため。
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// HOME を差し替える **前** に Go のキャッシュ位置を明示的に固定する。
	//
	// GOCACHE の既定は $HOME 配下なので、HOME だけ temp に向けると子プロセスの
	// `go build`(TestRecovery_AfterSIGKILL が実バイナリを作る)がキャッシュを
	// 見失い、毎回フルビルドになる。実測で 5.9 秒 → 19.6 秒に悪化した
	// ——隔離のために cycle time を捨てるのは v0.130.0 の逆行なので、両立させる。
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if os.Getenv(key) != "" {
			continue
		}
		out, err := exec.Command("go", "env", key).Output()
		if err != nil {
			continue
		}
		if v := strings.TrimSpace(string(out)); v != "" {
			os.Setenv(key, v)
		}
	}

	home, err := os.MkdirTemp("", "yagura-test-home-")
	if err != nil {
		panic("cannot create isolated test HOME: " + err.Error())
	}
	// HOME / USERPROFILE の両方(os.UserHomeDir は OS で参照先が違う)。
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)

	code := m.Run()

	os.RemoveAll(home)
	os.Exit(code)
}

// TestMain の効果そのものを固定する。これが無いと、誰かが TestMain を消しても
// 「たまたま今は誰も実 HOME に書かない」状態では気づけない。
func TestTestMain_HomeIsIsolated(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if home == "" {
		t.Fatal("HOME is empty during tests")
	}
	if filepath.Base(filepath.Dir(home)) == "" {
		t.Fatalf("unexpected home path %q", home)
	}
	// 実ホーム直下の .yagura をテストが作らないこと自体は TestMain が保証するが、
	// 少なくとも temp 配下に居ることは確認できる。
	if tmp := os.TempDir(); len(home) < len(tmp) || home[:len(tmp)] != tmp {
		t.Errorf("tests must run with HOME inside %s, got %q — TestMain isolation is not in effect", tmp, home)
	}
}
