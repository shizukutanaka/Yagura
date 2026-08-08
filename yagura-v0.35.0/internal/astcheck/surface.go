// surface.go — capability surface 分析(Roadmap #6 / 新視点 v0.36)。
//
// ソクラテス的動機:
//
//	既存 scanner は「コードの *どこが間違っているか*」(defect)を問う。本機能は
//	「コードは *何ができるのか*(何に触れるのか)」という least-privilege /
//	attack-surface の視点を加える。opsrisk が *操作* を capability(exec/network/…)
//	で tier 分類するのに対し、本機能は *コード* の capability を import から
//	静的にプロファイルする(その静的な対)。
//
// 判定は import パスのみ(go/parser ImportsOnly、型不要・zero-dep)。import で
// 一意に capability が決まるものに限定し、`os` のような多義 import は扱わない
// (os.WriteFile / os.Getenv 等の call-level 判定は今後の増分)。
package astcheck

import (
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// SurfaceResult は capability ごとに該当ファイル(昇順)を持つ。
type SurfaceResult struct {
	FilesScanned int                 `json:"files_scanned"`
	ByCapability map[string][]string `json:"by_capability"`
}

// importCapability は import パスを capability に対応づける("" = 該当なし)。
func importCapability(path string) string {
	switch path {
	case "os/exec", "syscall":
		return "exec"
	case "net", "net/http", "net/rpc", "net/smtp":
		return "network"
	case "unsafe":
		return "unsafe"
	case "reflect":
		return "reflect"
	}
	if strings.HasPrefix(path, "crypto/") || path == "crypto" {
		return "crypto"
	}
	return ""
}

// Surface は files の Go ファイルの import を走査し capability profile を返す。
func Surface(files map[string]string) SurfaceResult {
	res := SurfaceResult{ByCapability: map[string][]string{}}
	fset := token.NewFileSet()
	seen := map[string]map[string]bool{} // capability -> set(file)
	for path, src := range files {
		if !IsGoFile(path) {
			continue
		}
		res.FilesScanned++
		f, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
		if f == nil {
			_ = err
			continue
		}
		for _, imp := range f.Imports {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				continue
			}
			capName := importCapability(p)
			if capName == "" {
				continue
			}
			if seen[capName] == nil {
				seen[capName] = map[string]bool{}
			}
			seen[capName][path] = true
		}
	}
	for capName, fileset := range seen {
		list := make([]string, 0, len(fileset))
		for fp := range fileset {
			list = append(list, fp)
		}
		sort.Strings(list)
		res.ByCapability[capName] = list
	}
	return res
}
