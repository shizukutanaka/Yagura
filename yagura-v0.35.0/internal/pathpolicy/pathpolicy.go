// Package pathpolicy は「どのパスへの変更を許す/レビュー必須/拒否するか」を
// glob ルールで決定論的に判定する control-plane プリミティブである。
//
// 動機(kernel not brain): エージェントは自由にファイルを編集できてしまうが、
// 変更してよい範囲は project ごとに決まっている(例: ADR-0001 を守るため go.mod は
// 触らせない、監査の中核 internal/audit/** は人間レビュー必須)。Yagura は LLM を
// 呼ばず、変更パス集合をルールに照らして deny / review / allow を根拠つきで返す
// deterministic な guardrail になる。エージェントは編集前に問い合わせ、CI は PR の
// 変更ファイル一覧を gate にかけられる。判断は audit log に残せる。
//
// マッチングは slash 区切りの glob:
//   - `*`  … 1 セグメント内の任意(path.Match 準拠、`?` `[...]` も可)
//   - `**` … 0 個以上のセグメント(doublestar)
//   - それ以外は完全一致
//
// 例: `internal/audit/**`, `go.mod`, `**/*_test.go`, `cmd/*/main.go`
//
// 判定セマンティクス: あるパスに複数ルールがマッチしたら **最も厳しい action が勝つ**
// (deny > review > allow)。順序に依存せず、deny ルールが他に shadow されない安全側。
// どのルールにもマッチしなければ Policy.Default(既定 allow)。
package pathpolicy

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Action は判定結果の種別。厳しい順に deny > review > allow。
type Action string

const (
	// ActionDeny は変更パスを即時拒否する最も厳格な判定。
	ActionDeny Action = "deny"
	// ActionReview は人間レビューを要求する中間的な判定。
	ActionReview Action = "review"
	// ActionAllow は変更パスを承認する判定。
	ActionAllow Action = "allow"
)

func severity(a Action) int {
	switch a {
	case ActionDeny:
		return 3
	case ActionReview:
		return 2
	case ActionAllow:
		return 1
	}
	return 0
}

// Rule は 1 つの glob ルール。
type Rule struct {
	Path   string `json:"path"`
	Action Action `json:"action"`
	Reason string `json:"reason,omitempty"`
}

// Policy はルール集合と、未マッチ時の既定 action。
type Policy struct {
	Rules   []Rule `json:"rules"`
	Default Action `json:"default,omitempty"`
}

// Decision は 1 パスに対する判定。
type Decision struct {
	Path   string `json:"path"`
	Action Action `json:"action"`
	Rule   string `json:"rule,omitempty"` // マッチした glob("" = 既定)
	Reason string `json:"reason,omitempty"`
}

// Result は全パスの判定結果。
type Result struct {
	Decisions []Decision `json:"decisions"`
	Denied    []string   `json:"denied,omitempty"`
	Review    []string   `json:"review,omitempty"`
	Allowed   int        `json:"allowed"`
	Worst     Action     `json:"worst"` // 全体の gate 結果(deny > review > allow)
}

// validAction は a が既知の action か。
func validAction(a Action) bool {
	return a == ActionDeny || a == ActionReview || a == ActionAllow
}

// validateGlob は glob pattern が path.Match 的に妥当か検証する。
// セグメント単位で評価し、不正な文字クラス等(ErrBadPattern)を検出する。
// `**`(複数セグメントワイルドカード)は本 package 独自トークンなので許容。
func validateGlob(pattern string) error {
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return err // path.ErrBadPattern
		}
	}
	return nil
}

// Validate は policy をロード時に検証する。
//
// deny ルールの glob が壊れている(ErrBadPattern)、または action がタイポ
// (severity()==0 で allow 既定を上書きできない)場合、そのルールは Evaluate で
// **サイレントに不発**となり、本来 deny されるべきパスが allow に落ちる
// (security guardrail の fail-open)。これを load 時の error として顕在化させる。
func (p Policy) Validate() error {
	if p.Default != "" && !validAction(p.Default) {
		return fmt.Errorf("pathpolicy: invalid default action %q (deny/review/allow)", p.Default)
	}
	for i, r := range p.Rules {
		if r.Path == "" {
			return fmt.Errorf("pathpolicy: rule #%d: path is required", i)
		}
		if r.Action != "" && !validAction(r.Action) {
			return fmt.Errorf("pathpolicy: rule #%d (%q): invalid action %q (deny/review/allow)", i, r.Path, r.Action)
		}
		if err := validateGlob(normalize(r.Path)); err != nil {
			return fmt.Errorf("pathpolicy: rule #%d (%q): invalid glob: %w", i, r.Path, err)
		}
	}
	return nil
}

// Evaluate は changed の各パスを policy で判定する。出力は決定論的
// (Decisions は入力パスを昇順整列、Denied/Review も昇順)。
func Evaluate(p Policy, changed []string) Result {
	def := p.Default
	if def == "" {
		def = ActionAllow
	}
	paths := append([]string(nil), changed...)
	sort.Strings(paths)

	res := Result{Worst: ActionAllow}
	for _, raw := range paths {
		cp := normalize(raw)
		if cp == "" {
			continue
		}
		// 最も厳しいマッチを採用。
		best := Decision{Path: raw, Action: def}
		for _, r := range p.Rules {
			if r.Action == "" {
				continue
			}
			if matchGlob(normalize(r.Path), cp) {
				if best.Rule == "" || severity(r.Action) > severity(best.Action) {
					best.Action = r.Action
					best.Rule = r.Path
					best.Reason = r.Reason
				}
			}
		}
		res.Decisions = append(res.Decisions, best)
		switch best.Action {
		case ActionDeny:
			res.Denied = append(res.Denied, raw)
		case ActionReview:
			res.Review = append(res.Review, raw)
		default:
			res.Allowed++
		}
		if severity(best.Action) > severity(res.Worst) {
			res.Worst = best.Action
		}
	}
	return res
}

// normalize は path を slash 区切りに整え、先頭 "./" と前後 "/" を除く。
func normalize(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	p = strings.TrimPrefix(p, "./")
	p = strings.Trim(p, "/")
	return p
}

// matchGlob は slash 区切り glob(`*` は 1 セグメント内、`**` は 0+ セグメント)で
// name にマッチするかを返す。
func matchGlob(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	pseg := strings.Split(pattern, "/")
	nseg := strings.Split(name, "/")
	return matchSegments(pseg, nseg)
}

func matchSegments(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// 連続する ** は 1 つに畳む。
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true // 末尾 ** は残り全部にマッチ(0 個含む)
			}
			// ** が 0+ セグメントを消費: 各位置で残りパターンを試す。
			for i := 0; i <= len(name); i++ {
				if matchSegments(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], name[0])
		if err != nil || !ok {
			return false
		}
		pat = pat[1:]
		name = name[1:]
	}
	return len(name) == 0
}
