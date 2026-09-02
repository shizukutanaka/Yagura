package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepoTracked_EverythingNeededToBuildIsInGit は「このリポジトリを clone すれば
// ビルドできる」を **落ちるテスト** にする。
//
// なぜ要るか(v1.86.0 の発見):
//
//	`.gitignore` に `/yagura-v*/` が在り、ソースツリー全体が git から除外されていた。
//	実ファイル 428 のうち追跡されていたのは 237 で、欠けていた 191 に以下が含まれる:
//
//	  - go.mod              → **clone してもビルドできない**
//	  - _test.go 100 本      → 「約束を機械的に守る」テスト群そのもの
//	  - production .go 36 本 → プログラムの一部が存在しない
//	  - LICENSE / NOTICE     → 法的に何なのか分からない
//	  - .github/workflows/*  → GitHub 上の登録 workflow は **0 個**、CI は一度も走っていなかった
//
//	除外の理由として `.gitignore` に書かれていたのは「fake-secret fixture が
//	push protection に引っかかる」だったが、トークン形の文字列を含むのは 7 ファイルだけ。
//	**7 件の懸念のために 191 件を除外し、その代償を誰も測っていなかった。**
//
// このリポジトリの規約は「テストで enforce されていない約束は intention」。
// 「clone すれば動く」は intention ですらなく、単に偽だった。ここで固定する。
func TestRepoTracked_EverythingNeededToBuildIsInGit(t *testing.T) {
	root := moduleRoot(t)
	tracked := trackedFiles(t, root)

	// go.mod が無ければ他の何が在ってもビルドできない。最初に単独で見る。
	if !tracked["go.mod"] {
		t.Error("go.mod is not tracked by git: a clone of this repository cannot build")
	}

	var untracked []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "vendor", "node_modules", ".yagura":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !tracked[rel] {
			untracked = append(untracked, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(untracked) > 0 {
		t.Errorf("%d Go source file(s) exist but are not tracked by git — a clone would be "+
			"missing them:\n  %s", len(untracked), strings.Join(untracked, "\n  "))
	}
}

// TestRepoTracked_WorkflowsAreAtTheRepositoryRoot は CI が **実在する** ことを固定する。
//
// GitHub は `.github/workflows/` を **リポジトリのルートでしか** 読まない。
// v1.85.0 まで workflow はソースツリー側(`yagura-v0.35.0/.github/workflows/`)にあり、
// しかもそこは gitignore されていた。結果:
//
//   - Actions API の `list_workflows` は total_count 0 を返す = **CI は一度も走っていない**
//   - `release.yml`(SBOM / Sigstore / SLSA-3)も登録されていないので、
//     **タグを push しても何も起こらない**
//
// つまり「公開が止まっているのは tag push の 403 のせい」という長らく公開してきた
// 説明は誤りだった——403 は本当だが律速ではない。トリガーする対象が存在しなかった。
// このガードが固定するのは **配置** であって追跡状態ではない。理由は下記。
func TestRepoTracked_WorkflowsAreAtTheRepositoryRoot(t *testing.T) {
	repo := repoRoot(t)

	// ① ルートに実在すること。ここに無ければ GitHub は永久に登録しない。
	for _, w := range []string{"ci.yml", "codeql.yml", "release.yml", "scorecard.yml"} {
		p := filepath.Join(repo, ".github", "workflows", w)
		if _, err := os.Stat(p); err != nil {
			t.Errorf(".github/workflows/%s is missing from the repository root: GitHub "+
				"registers workflows only from the root, so CI would not exist", w)
		}
	}

	// ② モジュール側に複製が無いこと。**元のバグはここに置いてあったこと** で、
	//    2 つ持つと「CI 定義を名乗るファイルが 2 つあり、片方は絶対に動かない」
	//    という同じ失敗を静かな形で再発させる。
	dup := filepath.Join(moduleRoot(t), ".github", "workflows")
	if _, err := os.Stat(dup); err == nil {
		t.Errorf("%s exists: workflows must have exactly one home, at the repository root — "+
			"a copy beside the module can never run and will drift", dup)
	}

	// **追跡状態はここでは検査しない。** そうしたいが、できない:
	// このセッションの GitHub App には `workflows` 権限が無く、
	// `.github/workflows/*` を push しようとすると remote に拒否される
	// (`refusing to allow a GitHub App to create or update workflow ...`)。
	// 追跡を要求すると、権限が付与されるまで **恒久的に赤いテスト** になる。
	//
	// 赤いまま出荷するのも、静かに緑にするのも誤り。だから
	// **守れる約束(配置)だけをテストにし、守れない部分は
	// docs/PRODUCT_ASSESSMENT.md に未解決のブロッカーとして名前つきで残す。**
	// 権限が付いて workflow が push された時点で、ここに追跡検査を足すこと。
}

// moduleRoot は go.mod を含むディレクトリ(= このモジュールのルート)を返す。
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir = filepath.Dir(dir) // cmd/yagura -> cmd -> module root
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Skipf("module root not found from the test working directory: %v", err)
	}
	return dir
}

// repoRoot は git のトップレベルを返す。git 管理外なら skip する
// (tarball だけ展開して走らせても壊れないため)。
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "-C", moduleRoot(t), "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skip("not inside a git repository; this guard only applies to the repository itself")
	}
	return strings.TrimSpace(string(out))
}

// trackedFiles は base から見た相対パスで「git が追跡しているファイル」集合を返す。
func trackedFiles(t *testing.T, base string) map[string]bool {
	t.Helper()
	out, err := exec.Command("git", "-C", base, "ls-files").Output()
	if err != nil {
		t.Skip("git ls-files unavailable; this guard only applies to the repository itself")
	}
	set := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			set[line] = true
		}
	}
	if len(set) == 0 {
		t.Skip("no tracked files reported; not a usable git checkout")
	}
	return set
}
