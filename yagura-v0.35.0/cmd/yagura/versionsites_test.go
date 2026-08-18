// versionsites_test.go: リリース時に版番号を書き換える箇所を **集合として固定** する。
//
// なぜ必要か:
//
//	版番号は 4 つの Go/HTML 箇所に散っており、リリース手順は毎回それを手で書き換えて
//	いた。1 箇所忘れても大半のテストは緑のまま通る(dashboard の footer が古い版を
//	表示するだけ、tray の --version が食い違うだけ)。実際 v0.113→v0.130 の間、
//	この手順は 18 回すべて人手で実行されている。
//
//	`make release VERSION=x.y.z` がこの書き換えを自動化した(v0.131.0)。自動化は
//	**対象の集合が固定されている限りにおいてのみ** 正しいので、5 つ目の版番号が
//	どこかに増えた瞬間にこのテストが落ちる。自動化の前提そのものを守るガード。
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// versionSites はリリーススクリプトが書き換える **すべての** ファイル(repo root 相対)。
// ここを増やすときは scripts/release.sh の SITES も必ず同時に増やすこと。
var versionSites = []string{
	"cmd/yagura-tray/main.go",
	"cmd/yagura/main.go",
	"cmd/yagura/main_test.go",
	"internal/dashboard/dashboard.go",
}

// docSites は版番号を含む散文(手で直す。文脈があるので機械置換に任せない)。
var docSites = []string{"CLAUDE.md", "README.md"}

func TestVersionSites_AreExactlyTheKnownSet(t *testing.T) {
	root := "../.."
	cur := version // cmd/yagura/main.go の実際の版
	if cur == "" || cur == "dev" {
		t.Skipf("version is %q (ldflags-injected build); nothing to scan", cur)
	}
	verRe := regexp.MustCompile(regexp.QuoteMeta(cur))

	var found []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 読めない枝は無視(走査対象の網羅性はここでは主張しない)
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "bin", "vendor", "node_modules", ".yagura":
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".go" && ext != ".md" {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		// CHANGELOG は全リリースの版を含むので当然対象外。
		if rel == "CHANGELOG.md" {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		body := string(b)
		// Go ファイルは **コメントを除いてから** 探す。
		// このリポジトリは新しいファイルの doc コメントに「(v0.131.0)」のように
		// 導入リリースを書く習慣があり、それは書き換えるべき「版番号の宣言」ではなく
		// 単なる由来の記録。区別しないと、新ファイルを足したリリースは必ず誤検出で落ちる
		// (このテスト自身が最初の犠牲者だった)。
		if ext == ".go" {
			body = stripGoComments(body)
		}
		if verRe.MatchString(body) {
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	want := append(append([]string{}, versionSites...), docSites...)
	sort.Strings(want)
	sort.Strings(found)
	if strings.Join(found, ",") != strings.Join(want, ",") {
		t.Errorf("files carrying version %s changed.\n got: %v\nwant: %v\n"+
			"If you added a new place that states the version, add it to versionSites/docSites "+
			"here AND to SITES in scripts/release.sh — otherwise `make release` will leave it stale.",
			cur, found, want)
	}
}

// stripGoComments は行コメントとブロックコメントを取り除く(文字列リテラル内の
// "//" は残す必要があるので、素朴な除去ではなく最小限の状態機械で走る)。
func stripGoComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	const (
		code = iota
		lineComment
		blockComment
		str
		raw
	)
	state := code
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case code:
			switch {
			case c == '/' && i+1 < len(src) && src[i+1] == '/':
				state, i = lineComment, i+1
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state, i = blockComment, i+1
			case c == '"':
				state = str
				out.WriteByte(c)
			case c == '`':
				state = raw
				out.WriteByte(c)
			default:
				out.WriteByte(c)
			}
		case lineComment:
			if c == '\n' {
				state = code
				out.WriteByte(c)
			}
		case blockComment:
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				state, i = code, i+1
			}
		case str:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(src) {
				i++
				out.WriteByte(src[i])
			} else if c == '"' {
				state = code
			}
		case raw:
			out.WriteByte(c)
			if c == '`' {
				state = code
			}
		}
	}
	return out.String()
}

func TestStripGoComments_KeepsStringsDropsComments(t *testing.T) {
	got := stripGoComments("// note 1.2.3\nx := \"1.2.3\" // trailing 1.2.3\n/* block 1.2.3 */\ny := `raw 1.2.3`\n")
	if strings.Count(got, "1.2.3") != 2 {
		t.Errorf("want the two string literals kept and the three comments dropped, got %q", got)
	}
}
