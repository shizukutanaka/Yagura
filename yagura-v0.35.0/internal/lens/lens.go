// Package lens は Yagura の **構造レンズ 29 種の単一の情報源**(v0.129.0)。
//
// なぜ在るか — 削除のための部品:
//
//	v0.128.0 時点で MCP は 29 個の「1 レンズ 1 tool」を公開し、その全部が
//	`files`(ファイル名→内容の map)を **必須** で受け取っていた。つまり複雑度を
//	1 つ測るために、呼び出し側はソース全体を LLM の context に流し込む必要があった。
//	一方 CLI は同じ 29 レンズを **ディスクから読んで** 実行していた——同じ機能の
//	実装が 2 本あり、しかも MCP 側だけが利用者にトークンを課金していた。
//
//	v0.118.0 はこの矛盾を `yagura_portfolio_quality` **1 つだけ** で解消し、
//	CHANGELOG に「daemon がディスクを読むのでソース内容が LLM context を
//	1 バイトも通らない(token 経済の矛盾解消)」と書いた。その修正は残る 29 個へ
//	波及しなかった。本パッケージはその波及であり、**29 tool を 1 つに畳んで削除する**
//	ための表である。
//
//	First principles で言えば: レンズの *種類* は情報量を持つ(lensoverlap の実測でも
//	cognit↔complexity の Jaccard 0.38 以外はほぼ直交していた)。情報量を持たないのは
//	**tool として 29 個並んでいること** の方で、そこだけを削る。能力は 1 つも落とさない。
//
// 設計:
//
//	レンズは `Scan(files, ...)` の純関数で、IO を持たない(ファイルの読み出しは
//	呼び出し側が `internal/srcfiles` の単一 seam で行う)。本パッケージはその
//	純関数群への **決定論的なディスパッチ表** 以上のことをしない。
//
// zero-dep(ADR-0001): stdlib + 各レンズ package のみ。
package lens

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shizukutanaka/yagura/internal/apidoc"
	"github.com/shizukutanaka/yagura/internal/assertcheck"
	"github.com/shizukutanaka/yagura/internal/astcheck"
	"github.com/shizukutanaka/yagura/internal/calibrate"
	"github.com/shizukutanaka/yagura/internal/cognit"
	"github.com/shizukutanaka/yagura/internal/complexity"
	"github.com/shizukutanaka/yagura/internal/coupling"
	"github.com/shizukutanaka/yagura/internal/ctxcheck"
	"github.com/shizukutanaka/yagura/internal/deadcode"
	"github.com/shizukutanaka/yagura/internal/deprank"
	"github.com/shizukutanaka/yagura/internal/errdiscard"
	"github.com/shizukutanaka/yagura/internal/errpolicy"
	"github.com/shizukutanaka/yagura/internal/errwrap"
	"github.com/shizukutanaka/yagura/internal/flagarg"
	"github.com/shizukutanaka/yagura/internal/globalcheck"
	"github.com/shizukutanaka/yagura/internal/hotspot"
	"github.com/shizukutanaka/yagura/internal/ifacebloat"
	"github.com/shizukutanaka/yagura/internal/lensoverlap"
	"github.com/shizukutanaka/yagura/internal/nakedret"
	"github.com/shizukutanaka/yagura/internal/namecheck"
	"github.com/shizukutanaka/yagura/internal/nestdepth"
	"github.com/shizukutanaka/yagura/internal/paramcheck"
	"github.com/shizukutanaka/yagura/internal/prealloc"
	"github.com/shizukutanaka/yagura/internal/predeclared"
	"github.com/shizukutanaka/yagura/internal/recvcheck"
	"github.com/shizukutanaka/yagura/internal/returncheck"
	"github.com/shizukutanaka/yagura/internal/synccheck"
	"github.com/shizukutanaka/yagura/internal/thelper"
	"github.com/shizukutanaka/yagura/internal/typeassert"
)

// Options は全レンズ共通の入力。レンズごとに意味が違うので、使わないレンズは無視する。
type Options struct {
	// Threshold はしきい値を持つレンズ(complexity/cognit/nest_depth/…)の閾値。
	// 0 は「そのレンズの既定値」を意味する。
	Threshold int `json:"threshold"`
	// Module は import を解決するモジュールパス(coupling / dep_rank)。
	Module string `json:"module"`
	// Ignore は許容する識別子(predeclared)。
	Ignore []string `json:"ignore"`
	// MinLenses は収束とみなすレンズ数(hotspot)。0 は既定。
	MinLenses int `json:"min_lenses"`
}

// Result は 1 レンズの実行結果。
//
// RunAll では Report を **省く**(全レンズの本文を返すなら 29 tool のままと
// 変わらず、統合した意味が消える)。
type Result struct {
	Lens     string `json:"lens"`
	Summary  string `json:"summary"`
	Findings int    `json:"findings"`
	Report   any    `json:"report,omitempty"`
}

// spec は 1 レンズの定義。run は (完全な report, 指摘件数) を返す。
type spec struct {
	name    string
	summary string
	run     func(files map[string]string, o Options) (any, int)
}

// specs は **唯一の** レンズ表。ここに無いものは MCP からも CLI からも見えない。
//
// 並びは name 昇順(Names() の決定論性はこの表に依存しない——Run 時に sort する)。
var specs = []spec{
	{"api_doc", "exported API doc coverage (godoc discipline)", func(f map[string]string, o Options) (any, int) {
		r := apidoc.Scan(f)
		return r, len(r.Findings)
	}},
	{"assert_check", "assertion density; hollow tests prove nothing", func(f map[string]string, o Options) (any, int) {
		r := assertcheck.Scan(f)
		return r, r.HollowFiles
	}},
	{"ast_check", "structural checks regex cannot do (os.Exit in library, empty nil branch)", func(f map[string]string, o Options) (any, int) {
		r := astcheck.ScanFiles(f)
		return r, len(r.Findings)
	}},
	{"calibrate", "threshold calibration from the corpus itself (distributions + outliers)", func(f map[string]string, o Options) (any, int) {
		r := calibrate.Scan(f)
		return r, len(r.Outliers)
	}},
	{"cognit", "cognitive complexity (nesting-weighted human reading cost)", func(f map[string]string, o Options) (any, int) {
		r := cognit.Scan(f, o.Threshold)
		return r, len(r.Findings)
	}},
	{"complexity", "cyclomatic complexity (McCabe, gocyclo-compatible)", func(f map[string]string, o Options) (any, int) {
		r := complexity.Scan(f, o.Threshold)
		return r, len(r.Findings)
	}},
	{"coupling", "package import coupling (fan-in/fan-out/instability)", func(f map[string]string, o Options) (any, int) {
		r := coupling.Scan(f, o.Module)
		return r, len(r.Findings)
	}},
	{"ctx_check", "context.Context discipline (first arg, not in structs)", func(f map[string]string, o Options) (any, int) {
		r := ctxcheck.Scan(f)
		return r, len(r.Findings)
	}},
	{"dead_code", "unreferenced unexported declarations", func(f map[string]string, o Options) (any, int) {
		r := deadcode.Scan(f)
		return r, len(r.Findings)
	}},
	{"dep_rank", "package in-degree (blast radius of a change)", func(f map[string]string, o Options) (any, int) {
		r := deprank.Scan(f, o.Module, o.Threshold)
		return r, len(r.Findings)
	}},
	{"err_discard", "errors dropped at the call site", func(f map[string]string, o Options) (any, int) {
		r := errdiscard.Scan(f)
		return r, len(r.Findings)
	}},
	{"err_policy", "error diagnosability (wrap rate, blank discards)", func(f map[string]string, o Options) (any, int) {
		r := errpolicy.Scan(f)
		return r, len(r.Findings)
	}},
	{"err_wrap", "Go 1.13 error wrapping (%w, errors.Is/As)", func(f map[string]string, o Options) (any, int) {
		r := errwrap.Scan(f)
		return r, len(r.Findings)
	}},
	{"flag_arg", "boolean flag arguments (Fowler flag-argument smell)", func(f map[string]string, o Options) (any, int) {
		r := flagarg.Scan(f, o.Threshold)
		return r, len(r.Findings)
	}},
	{"global_check", "mutable package-level globals", func(f map[string]string, o Options) (any, int) {
		r := globalcheck.Scan(f)
		return r, len(r.Findings)
	}},
	{"hotspot", "functions independently flagged by several lenses (convergence)", func(f map[string]string, o Options) (any, int) {
		r := hotspot.Scan(f, o.MinLenses)
		return r, len(r.Hotspots)
	}},
	{"ifacebloat", "interface method count (the bigger the interface, the weaker the abstraction)", func(f map[string]string, o Options) (any, int) {
		r := ifacebloat.Scan(f, o.Threshold)
		return r, len(r.Findings)
	}},
	{"lens_overlap", "Jaccard overlap between lenses (evidence for retiring one)", func(f map[string]string, o Options) (any, int) {
		r := lensoverlap.Scan(f)
		return r, r.HighOverlap + r.MediumOverlap
	}},
	{"naked_ret", "naked returns in long functions with named results", func(f map[string]string, o Options) (any, int) {
		r := nakedret.Scan(f, o.Threshold)
		return r, len(r.Findings)
	}},
	{"name_check", "name/behaviour agreement (is/has predicates, error naming)", func(f map[string]string, o Options) (any, int) {
		r := namecheck.Scan(f)
		return r, len(r.Findings)
	}},
	{"nest_depth", "maximum control-flow nesting depth", func(f map[string]string, o Options) (any, int) {
		r := nestdepth.Scan(f, o.Threshold)
		return r, len(r.Findings)
	}},
	{"param_check", "parameter count (Fowler long-parameter-list smell)", func(f map[string]string, o Options) (any, int) {
		r := paramcheck.Scan(f, o.Threshold)
		return r, len(r.Findings)
	}},
	{"prealloc", "slices grown by append in a range loop without preallocation", func(f map[string]string, o Options) (any, int) {
		r := prealloc.Scan(f)
		return r, len(r.Findings)
	}},
	{"predeclared", "declarations shadowing Go builtins (len/cap/min/max/…)", func(f map[string]string, o Options) (any, int) {
		r := predeclared.Scan(f, o.Ignore)
		return r, len(r.Findings)
	}},
	{"recv_check", "method receiver consistency (name and value/pointer mixing)", func(f map[string]string, o Options) (any, int) {
		r := recvcheck.Scan(f)
		return r, len(r.Findings)
	}},
	{"return_check", "return-value count (how wide the exit is)", func(f map[string]string, o Options) (any, int) {
		r := returncheck.Scan(f, o.Threshold)
		return r, len(r.Findings)
	}},
	{"sync_check", "copying types that contain sync locks", func(f map[string]string, o Options) (any, int) {
		r := synccheck.Scan(f)
		return r, len(r.Findings)
	}},
	{"thelper", "test helpers that never call t.Helper()", func(f map[string]string, o Options) (any, int) {
		r := thelper.Scan(f)
		return r, len(r.Findings)
	}},
	{"type_assert", "single-value type assertions that can panic", func(f map[string]string, o Options) (any, int) {
		r := typeassert.Scan(f)
		return r, len(r.Findings)
	}},
}

// byName は名前引きの索引(表は 1 度だけ走査する)。
var byName = func() map[string]spec {
	m := make(map[string]spec, len(specs))
	for _, s := range specs {
		m[s.name] = s
	}
	return m
}()

// Names は登録済みレンズ名を昇順で返す。
func Names() []string {
	out := make([]string, 0, len(specs))
	for _, s := range specs {
		out = append(out, s.name)
	}
	sort.Strings(out)
	return out
}

// Summaries は名前 → 一行説明。tool description に埋めて「何が選べるか」を示すため。
func Summaries() map[string]string {
	m := make(map[string]string, len(specs))
	for _, s := range specs {
		m[s.name] = s.summary
	}
	return m
}

// Run は 1 レンズを実行し、完全な report つきの Result を返す。
//
// 未知の名前は **選べる名前を挙げて** 失敗する(黙って空を返すと、呼び出し側は
// 「指摘なし=健全」と読んでしまう)。
func Run(name string, files map[string]string, o Options) (Result, error) {
	s, ok := byName[name]
	if !ok {
		return Result{}, fmt.Errorf("unknown lens %q; valid lenses: %s", name, strings.Join(Names(), ", "))
	}
	rep, n := s.run(files, o)
	return Result{Lens: s.name, Summary: s.summary, Findings: n, Report: rep}, nil
}

// RunAll は全レンズを走らせ、**件数だけ** を名前昇順で返す(Report は省く)。
// 「まずどこを見るべきか」を 1 往復で決めるための入口。
func RunAll(files map[string]string, o Options) []Result {
	names := Names()
	out := make([]Result, 0, len(names))
	for _, n := range names {
		s := byName[n]
		_, cnt := s.run(files, o)
		out = append(out, Result{Lens: s.name, Summary: s.summary, Findings: cnt})
	}
	return out
}
