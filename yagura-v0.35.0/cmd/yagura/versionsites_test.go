// versionsites_test.go: リリース時に版番号を書き換え **忘れた** 箇所を検出する。
//
// なぜ必要か:
//
//	版番号は 4 つの箇所に散っており、1 箇所忘れても大半のテストは緑のまま通る
//	(dashboard の footer が古い版を表示するだけ、tray の --version が食い違うだけ)。
//	`make release` はその書き換えを自動化したが、**自動化は対象の集合が正しい限りに
//	おいてのみ正しい**。5 つ目の版番号がどこかに増えたら、スクリプトは黙ってそれを
//	古いまま残す。
//
// 設計をひとつ間違えて直した記録(v1.0.0):
//
//	最初の版は「**現在の**版番号を含むファイル集合」を固定していた。これは 1.0.0 で
//	破綻した——"1.0.0" は SBOM・VEX・plugin manifest のテスト fixture に普通に現れる
//	ありふれた文字列で、9 個の無関係なファイルが誤検出された。
//
//	正しい問いは「現在の版はどこに在るか」ではなく **「古い版が残っていないか」**。
//	リリースで防ぎたい失敗はまさにそれ(書き換え漏れ)であり、しかも古い版番号
//	(例 0.131.0)は fixture にまず現れない。よって前版を探す形に直した。
//
//	Go のコメントは除外する。このリポジトリは「★ v0.118.0」のように **由来** を
//	コメントに書く習慣があり、それは書き換えるべき宣言ではない。
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// versionSites はリリーススクリプトが書き換えるファイル(repo root 相対)。
// ここを増やすときは scripts/release.sh の SITES も必ず同時に増やすこと。
var versionSites = []string{
	"cmd/yagura-tray/main.go",
	"cmd/yagura/main.go",
	"cmd/yagura/main_test.go",
	"internal/dashboard/dashboard.go",
}

var changelogHeading = regexp.MustCompile(`(?m)^## \[v(\d+\.\d+\.\d+)\]`)

// TestVersionSites_KnownSitesCarryCurrentVersion は既知の 4 箇所が現在の版を
// 持っていることを確かめる(書き換えが実際に効いたか)。
func TestVersionSites_KnownSitesCarryCurrentVersion(t *testing.T) {
	if version == "" || version == "dev" {
		t.Skipf("version is %q (ldflags-injected build)", version)
	}
	for _, rel := range versionSites {
		b, err := os.ReadFile(filepath.Join("../..", rel))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if !strings.Contains(string(b), version) {
			t.Errorf("%s does not carry the current version %s — `make release` missed it", rel, version)
		}
	}
}

// TestVersionSites_NoStalePreviousVersionRemains は **前版の残骸** を探す。
// これがリリース自動化が実際に犯しうる失敗であり、5 つ目の版番号箇所が増えたことも
// 同時に検出できる(その箇所だけ古いまま残るため)。
func TestVersionSites_NoStalePreviousVersionRemains(t *testing.T) {
	if version == "" || version == "dev" {
		t.Skipf("version is %q (ldflags-injected build)", version)
	}
	root := "../.."
	prev := previousVersion(t, root)
	if prev == "" {
		t.Skip("no previous release recorded in CHANGELOG.md")
	}
	prevRe := regexp.MustCompile(regexp.QuoteMeta(prev))

	var stale []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "bin", "vendor", "node_modules", ".yagura", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		// コメントは由来の記録であって宣言ではないので除外する。
		if prevRe.MatchString(stripGoComments(string(b))) {
			stale = append(stale, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("%d file(s) still state the previous version %s in code (current is %s): %v\n"+
			"If one of these is a version site, add it to versionSites here AND to SITES in "+
			"scripts/release.sh, or `make release` will keep leaving it stale.",
			len(stale), prev, version, stale)
	}
}

// previousVersion は CHANGELOG の 2 番目の見出しから前版を読む。
func previousVersion(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("read CHANGELOG: %v", err)
	}
	m := changelogHeading.FindAllStringSubmatch(string(b), 2)
	if len(m) < 2 {
		return ""
	}
	if m[0][1] != version {
		t.Fatalf("CHANGELOG's newest entry is v%s but the binary reports %s — write the entry "+
			"for this release (scripts/release.sh enforces this too)", m[0][1], version)
	}
	return m[1][1]
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
