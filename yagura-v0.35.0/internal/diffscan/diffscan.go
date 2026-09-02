// Package diffscan は unified diff から *追加された行* を抽出する(新視点 v0.36)。
//
// 動機(ソクラテス的):
//
//	既存 scanner は全てファイル/内容の **スナップショット** を採点する。だが
//	AI エージェントのレビューで本当に問うべきは「この **変更** が何を新しく
//	持ち込んだか」。既存の負債(古い TODO 等)で PR を落とすのではなく、diff が
//	*追加* した行だけを対象にするのが正しい粒度。これは snapshot ではなく delta の
//	視点。本 package はその純粋プリミティブ(追加行抽出)を提供し、呼出側が
//	secretscan 等と合成して「この変更は秘密を新たに混入したか?」を判定できる。
//
// stdlib のみ(ADR-0001)。git 不要 — `git diff` 等の unified diff テキストを受ける。
package diffscan

import (
	"regexp"
	"strings"
)

// AddedLine は diff で追加された 1 行(新ファイル側の行番号つき)。
type AddedLine struct {
	Path string `json:"path"`
	Line int    `json:"line"` // 新ファイル側の 1-based 行番号
	Text string `json:"text"` // 先頭の '+' を除いた内容
}

// AddedLines は unified diff を走査し、追加行を出現順に返す。
//
// 各ハンクの `@@ -a,b +c,d @@` から新ファイル側開始行 c を取り、context 行と
// 追加行で行番号を進め、削除行では進めない(新ファイル側の行番号を正しく保つ)。
func AddedLines(unifiedDiff string) []AddedLine {
	var out []AddedLine
	path := ""
	newLine := 0
	inHunk := false

	for _, line := range strings.Split(unifiedDiff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			path = parseNewPath(line)
			inHunk = false
		case strings.HasPrefix(line, "--- "):
			// old-file header; ignore (path comes from +++).
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			newLine = parseHunkNewStart(line)
			inHunk = true
		case strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "new file") || strings.HasPrefix(line, "deleted file") ||
			strings.HasPrefix(line, "rename ") || strings.HasPrefix(line, "similarity ") ||
			strings.HasPrefix(line, "\\ "):
			// metadata / "\ No newline at end of file" — ignore.
		case !inHunk:
			// outside a hunk; ignore.
		case strings.HasPrefix(line, "+"):
			out = append(out, AddedLine{Path: path, Line: newLine, Text: line[1:]})
			newLine++
		case strings.HasPrefix(line, "-"):
			// removed line: does not advance new-file counter.
		default:
			// context line (starts with space, or empty): advances new-file counter.
			newLine++
		}
	}
	return out
}

// parseNewPath は `+++ b/foo.go` 形式から論理パスを返す(a/ b/ prefix と
// 末尾タブ以降のタイムスタンプを除去。/dev/null はそのまま)。
func parseNewPath(header string) string {
	s := strings.TrimSpace(header[len("+++ "):])
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	if s == "/dev/null" {
		return s
	}
	if strings.HasPrefix(s, "a/") || strings.HasPrefix(s, "b/") {
		s = s[2:]
	}
	return s
}

// parseOldPath は `--- a/foo.go` 形式から論理パスを返す。
func parseOldPath(header string) string {
	s := strings.TrimSpace(header[len("--- "):])
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	if s == "/dev/null" {
		return s
	}
	if strings.HasPrefix(s, "a/") || strings.HasPrefix(s, "b/") {
		s = s[2:]
	}
	return s
}

// RemovedLine は diff で削除された 1 行(旧ファイル側の行番号つき)。
type RemovedLine struct {
	Path string `json:"path"`
	Line int    `json:"line"` // 旧ファイル側の 1-based 行番号
	Text string `json:"text"` // 先頭の '-' を除いた内容
}

// RemovedLines は unified diff を走査し、削除行を出現順に返す。
// 削除行と context 行で旧ファイル側行番号を進め、追加行では進めない。
func RemovedLines(unifiedDiff string) []RemovedLine {
	var out []RemovedLine
	path := ""
	oldLine := 0
	inHunk := false

	for _, line := range strings.Split(unifiedDiff, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			path = parseOldPath(line)
			inHunk = false
		case strings.HasPrefix(line, "+++ "):
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			oldLine = parseHunkOldStart(line)
			inHunk = true
		case strings.HasPrefix(line, "diff ") || strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "new file") || strings.HasPrefix(line, "deleted file") ||
			strings.HasPrefix(line, "rename ") || strings.HasPrefix(line, "similarity ") ||
			strings.HasPrefix(line, "\\ "):
		case !inHunk:
		case strings.HasPrefix(line, "-"):
			out = append(out, RemovedLine{Path: path, Line: oldLine, Text: line[1:]})
			oldLine++
		case strings.HasPrefix(line, "+"):
			// added: does not advance old-file counter.
		default:
			oldLine++
		}
	}
	return out
}

// parseHunkOldStart は `@@ -a,b +c,d @@` から旧ファイル側開始行 a を返す。
func parseHunkOldStart(hunk string) int {
	minus := strings.IndexByte(hunk, '-')
	if minus < 0 {
		return 1
	}
	rest := hunk[minus+1:]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 1
	}
	n := 0
	for _, c := range rest[:end] {
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		return 1
	}
	return n
}

// GuardRemoval は削除された安全装置 1 件。
type GuardRemoval struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// guardPatterns は「削除されると危険」な高シグナルの構文。
var guardPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	// panic 回復の削除(順序重視: recover を error-check より先に判定)。
	{"recover-removed", regexp.MustCompile(`\brecover\s*\(`)},
	// エラーチェックの削除。
	{"error-check-removed", regexp.MustCompile(`\bif\b[^\n]*\berr\b[^\n]*!=\s*nil`)},
	// 後始末・ロック解放の削除。
	{"cleanup-removed", regexp.MustCompile(`\bdefer\b[^\n]*\.(Close|Unlock|RUnlock|Done|Stop)\s*\(`)},
}

// RemovedGuards は削除行のうち、安全装置に該当するものを返す。
// 追加ではなく *削除* のみが対象(保護が剥がされた兆候)。1 行 1 kind(最初に
// 該当したパターン)で報告する。
func RemovedGuards(unifiedDiff string) []GuardRemoval {
	var out []GuardRemoval
	for _, rl := range RemovedLines(unifiedDiff) {
		for _, gp := range guardPatterns {
			if gp.re.MatchString(rl.Text) {
				out = append(out, GuardRemoval{Path: rl.Path, Line: rl.Line, Kind: gp.kind, Text: strings.TrimSpace(rl.Text)})
				break
			}
		}
	}
	return out
}
func parseHunkNewStart(hunk string) int {
	plus := strings.IndexByte(hunk, '+')
	if plus < 0 {
		return 1
	}
	rest := hunk[plus+1:]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 1
	}
	n := 0
	for _, c := range rest[:end] {
		n = n*10 + int(c-'0')
	}
	if n == 0 {
		// "@@ -0,0 +0,0 @@" 等。追加開始は 1 行目扱い。
		return 1
	}
	return n
}
