// tagname_test.go: リリースタグ名の検証を **実際に起きた事故から** 固定する。
//
// 事故:
//
//	このリポジトリのタグ 3 本のうち 2 本(ｖ1.78.0 / ｖ1.79.0)は先頭が ASCII の
//	'v' (U+0076) ではなく **全角の 'ｖ' (U+FF56)** だった。release.yml の trigger は
//	`tags: ['v*']` なので、その 2 本は **release workflow を一度も起動していない**。
//	バイナリも SBOM も SLSA provenance も生成されないまま「タグは付いている」状態。
//
//	この種の失敗は「何も起きない」という形で現れるので、誰も気づかない。だから
//	テストで固定する——見た目で区別できない文字を人間のレビューに頼らせない。
//
// ここでは scripts/tag.sh の --check を **実際に実行して** 検証する
// (検証ロジックを Go 側に写経すると、写経した方だけが正しくなりうる)。
package main

import (
	"os/exec"
	"strings"
	"testing"
)

func runTagCheck(t *testing.T, name string) error {
	t.Helper()
	cmd := exec.Command("bash", "../../scripts/tag.sh", "--check", name)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "ERROR") {
		t.Fatalf("tag.sh --check %q failed unexpectedly: %v\n%s", name, err, out)
	}
	return err
}

func TestTagName_RejectsTheFullWidthVThatBrokeTwoReleases(t *testing.T) {
	// 実際にこのリポジトリに付いてしまった 2 本。見た目は "v1.78.0" と区別できない。
	for _, bad := range []string{"ｖ" + "1.78.0", "ｖ" + "1.79.0"} {
		if err := runTagCheck(t, bad); err == nil {
			t.Errorf("tag %q (full-width v, U+FF56) must be rejected: it does not match "+
				"release.yml's `tags: ['v*']`, so the release workflow would silently never run", bad)
		}
	}
}

func TestTagName_AcceptsWellFormedAndRejectsTheRest(t *testing.T) {
	good := []string{"v1.0.0", "v0.6.0", "v1.79.0", "v10.20.30"}
	for _, g := range good {
		if err := runTagCheck(t, g); err != nil {
			t.Errorf("tag %q should be accepted, got error", g)
		}
	}
	bad := map[string]string{
		"1.0.0":     "missing the v prefix",
		"V1.0.0":    "uppercase V does not match the lowercase glob",
		"v1.0":      "not X.Y.Z",
		"v1.0.0.0":  "too many components",
		"v1.0.0-rc": "suffix would produce a surprising release name",
		"v 1.0.0":   "whitespace",
		"":          "empty",
	}
	for b, why := range bad {
		if err := runTagCheck(t, b); err == nil {
			t.Errorf("tag %q should be rejected (%s)", b, why)
		}
	}
}

// 検証が **落ちうる** ことの確認。常に通る検証器は何も検証しない。
func TestTagName_CheckerCanFail(t *testing.T) {
	if err := runTagCheck(t, "definitely-not-a-tag"); err == nil {
		t.Fatal("the tag checker accepted obvious garbage — it is not actually checking")
	}
}
