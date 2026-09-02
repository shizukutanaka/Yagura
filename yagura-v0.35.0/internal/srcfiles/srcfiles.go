// Package srcfiles is the single capped, fail-open-aware source-tree walker.
//
// なぜ独立パッケージなのか(v0.118.0):
//
//	この walker は元々 cmd/yagura/cli.go の readFilesLimited にあり、その doc comment
//	自身が「新しいスキャナはこの 1 本を predicate 付きで再利用すること(独自 WalkDir を
//	書くと caps と fail-open 警告が失われる)」と要求していた。しかし cmd/yagura は
//	internal/* から import できない(依存方向が逆)ため、internal/mcp の tool 群は
//	この規約に構造的に従えず、結果として「client が files を丸ごと送る」content-based
//	契約しか選べなかった。internal/srcfiles へ引き上げることで CLI と MCP の双方が
//	同じ 1 本を使える——v0.109.0 の runSafely のように複製するのではなく、単一 seam に
//	統合する(CLAUDE.md の単一 seam 原則)。
//
// 不完全スキャン(cap 到達 / 読取失敗)は Result のフラグで必ず伝播する。これを無視して
// 「findings なし」と報告すると部分スキャンを完全スキャンと取り違える fail-open に
// なるため、呼出側は Incomplete() を必ず確認すること。
//
// zero-dep(ADR-0001): stdlib のみ。
package srcfiles

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DefaultMaxFiles / DefaultMaxBytes は既定の走査上限。
const (
	DefaultMaxFiles = 1000
	DefaultMaxBytes = 50 * 1024 * 1024
)

// Result は 1 回の走査結果。
type Result struct {
	Files     map[string]string // relpath → content
	Truncated bool              // 上限に達して打ち切った
	// Matched は accept を通ったファイルの **総数**。上限で打ち切った後も
	// 数え続けるので、len(Files)/Matched が「どれだけ読めたか」を与える。
	//
	// なぜ真偽値では足りないか: kubernetes(17,878 Go ファイル)を測ったとき、
	// 応答は `incomplete: true` の 1 語だった。実際に読めたのは 5.6% だが、
	// 利用者には 99% なのか 5% なのか判別できない。「不完全である」は
	// 「どれだけ信用してよいか」を答えない。
	Matched    int
	Unreadable []string // ツリー内に在るが読めなかったソース
}

// Incomplete はスキャンが完全でなかったか(cap 到達 or 読取失敗)を返す。
// true の場合、findings が空でも「クリーン」と結論してはならない。
func (r Result) Incomplete() bool { return r.Truncated || len(r.Unreadable) > 0 }

// ReadGo は dir 配下の *.go を既定上限で読む。
func ReadGo(dir string) (Result, error) {
	return ReadLimited(dir, DefaultMaxFiles, DefaultMaxBytes, func(name string) bool {
		return strings.HasSuffix(name, ".go")
	})
}

// ReadGoTest は dir 配下の *_test.go を既定上限で読む。
func ReadGoTest(dir string) (Result, error) {
	return ReadLimited(dir, DefaultMaxFiles, DefaultMaxBytes, func(name string) bool {
		return strings.HasSuffix(name, "_test.go")
	})
}

// ReadSource は dir 配下のサポート言語ソースを既定上限で読む。
func ReadSource(dir string) (Result, error) {
	return ReadLimited(dir, DefaultMaxFiles, DefaultMaxBytes, IsSourceFile)
}

// IsSourceFile は name がサポート言語(Go/TS/JS/Python/Rust/Java)のソースかを返す。
func IsSourceFile(name string) bool {
	for _, ext := range []string{".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".java"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// ReadLimited は dir を再帰的に走査し、accept(filename)==true のファイルを
// {relpath: content} で読む共通 walker。vendor/node_modules/.git/.yagura を skip し、
// maxFiles / maxTotalBytes の上限と、不完全スキャン(truncated / unreadable)の
// シグナルを Result に記録する。新しいスキャナはこの 1 本を predicate 付きで
// 再利用すること(独自 WalkDir を書くと caps と fail-open 警告が失われる)。
func ReadLimited(dir string, maxFiles int, maxTotalBytes int64, accept func(name string) bool) (Result, error) {
	res := Result{Files: make(map[string]string)}
	var totalBytes int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		name := d.Name()
		if d.IsDir() {
			if name == "vendor" || name == "node_modules" || name == ".git" || name == ".yagura" {
				return filepath.SkipDir
			}
			return nil
		}
		if !accept(name) {
			return nil
		}
		res.Matched++
		if len(res.Files) >= maxFiles {
			res.Truncated = true
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			// 存在するソースが読めない = 検出から漏れる。fail-open を避けるため
			// 黙殺せず記録する(深いツリー walk を 1 ファイルで止めるのは過剰なので
			// skip+report)。
			res.Unreadable = append(res.Unreadable, relOrPath(dir, path))
			return nil
		}
		if totalBytes+int64(len(data)) > maxTotalBytes {
			res.Truncated = true
			return nil
		}
		totalBytes += int64(len(data))
		res.Files[relOrPath(dir, path)] = string(data)
		return nil
	})
	return res, err
}

// relOrPath は dir 起点の相対パスを返す(取れなければ絶対パスのまま)。
func relOrPath(dir, path string) string {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return path
	}
	return rel
}
