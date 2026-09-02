package churn

import (
	"os/exec"
	"testing"
)

// TestIsPartialClone は「blob を持たないクローンか」の判定を固定する。
//
// 発見の経緯: prometheus を `--filter=blob:none` で clone して測ろうとしたところ、
// `git log --numstat` が 60 秒の上限を超えた。製品はそれを
// "repository history too large; lower max_commits" と診断したが、これは **誤診** で、
// max_commits を 2000→800 に下げても直らなかった。真因は partial clone で、
// --numstat が差分のたびに blob を 1 つずつネットワーク取得していたこと
// (n=50 で 16 秒)。通常の clone なら同じ 2000 コミットが 2.9 秒で終わる。
//
// 誤った診断は、利用者を「履歴を削る」という効かない対処に誘導する。
func TestIsPartialClone(t *testing.T) {
	full := t.TempDir()
	mustGit(t, full, "init")
	if isPartialClone(full) {
		t.Error("an ordinary repository must not be reported as a partial clone")
	}

	partial := t.TempDir()
	mustGit(t, partial, "init")
	// promisor remote = blob を遅延取得する設定。実際の `--filter` clone が書くもの。
	mustGit(t, partial, "config", "remote.origin.promisor", "true")
	if !isPartialClone(partial) {
		t.Error("a promisor remote must be reported as a partial clone")
	}

	// git 管理外は判定不能 = false(誤った助言をするより黙る)。
	if isPartialClone(t.TempDir()) {
		t.Error("a non-repository must not be reported as a partial clone")
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
