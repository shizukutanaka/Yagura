// Package opsrisk は「ある操作にどれだけの自律性を与えてよいか」を決定論的に
// 分類する control-plane プリミティブである。
//
// 出典(Zenn からの調査・2026):
//   - "AIエージェント時代のガバナンス設計 — 禁止から段階的運用への5つの判断軸"
//     (zenn.dev/miyan): 権限境界 / 監査証跡 / **段階的自律性** / コスト統制 /
//     コンプライアンス。低リスク=自動+ログ、高リスク=人間承認+監査+アラート。
//   - "AIエージェントの暴走を防ぐ設計原則 — 「判断はコード、提案はLLM」"
//     (zenn.dev/imudak): 判断ロジックは決定論的コードに置き、LLM は提案に留める。
//   - OWASP LLM08:2025 Excessive Agency: 過剰な機能/権限/自律性を避け、最小権限と
//     edit/delete への consent を課す。
//
// Yagura のスタンス(kernel not brain): path-policy が「どのパスを触ってよいか」を
// 統治するのに対し、opsrisk は「その操作にどれだけ自律実行を許すか」(段階的自律性)を
// capability・可逆性・影響範囲から rule-based に分類する。LLM は呼ばない。
// 出力(tier と必要 control)は audit log に残せる。
package opsrisk

import (
	"fmt"
	"sort"
	"strings"
)

// Tier は許容する自律レベル。低い順に auto < log < review < human。
type Tier string

const (
	// TierAuto: operation may proceed autonomously.
	TierAuto Tier = "auto"
	// TierLog: operation may proceed but must be written to the audit log.
	TierLog Tier = "log"
	// TierReview: operation requires reviewer approval before proceeding.
	TierReview Tier = "review"
	// TierHuman: operation must halt until a human explicitly approves (+ alert raised).
	TierHuman Tier = "human"
)

func rank(t Tier) int {
	switch t {
	case TierAuto:
		return 0
	case TierLog:
		return 1
	case TierReview:
		return 2
	case TierHuman:
		return 3
	}
	return 1
}

func tierOf(r int) Tier {
	switch {
	case r <= 0:
		return TierAuto
	case r == 1:
		return TierLog
	case r == 2:
		return TierReview
	default:
		return TierHuman
	}
}

// capBase は capability ごとの基準 tier(権限境界の軸)。
// 未知 capability は secure-by-default で review。
var capBase = map[string]Tier{
	"read":     TierAuto,
	"network":  TierLog, // 外向き read。ログは残す
	"write":    TierReview,
	"exec":     TierReview,
	"delete":   TierHuman, // 破壊的
	"data":     TierHuman, // ユーザーデータ操作(PII 文脈)
	"auth":     TierHuman,
	"billing":  TierHuman, // コスト統制の軸
	"external": TierHuman, // 外部 API への副作用
}

// Op は 1 操作の記述。
type Op struct {
	Name        string `json:"name"`
	Capability  string `json:"capability"`             // read|write|delete|exec|network|external|auth|billing|data
	Reversible  *bool  `json:"reversible,omitempty"`   // 取り消せるか(nil=不明)
	BlastRadius string `json:"blast_radius,omitempty"` // single|project|portfolio|external
	HasGate     bool   `json:"has_gate,omitempty"`     // 事前 consent/確認ゲートがあるか
}

// Decision は 1 操作の分類結果。
type Decision struct {
	Name       string   `json:"name"`
	Capability string   `json:"capability"`
	Tier       Tier     `json:"tier"`
	Controls   []string `json:"controls"`
	Rationale  string   `json:"rationale"`
}

// Result は複数操作の分類結果。
type Result struct {
	Decisions []Decision     `json:"decisions"`
	ByTier    map[string]int `json:"by_tier"`
	Worst     Tier           `json:"worst"` // 全体の gate 結果
}

// Classify は 1 操作の自律 tier を決定論的に分類する。
func Classify(op Op) Decision {
	capLower := strings.ToLower(strings.TrimSpace(op.Capability))
	var reasons []string

	base, known := capBase[capLower]
	if !known {
		base = TierReview
		reasons = append(reasons, fmt.Sprintf("unknown capability %q → review (secure by default)", op.Capability))
	} else {
		reasons = append(reasons, fmt.Sprintf("capability %q", capLower))
	}
	r := rank(base)

	// 可逆性: 取り消せない操作は 1 段引き上げ、最低でも review。
	if op.Reversible != nil && !*op.Reversible {
		r++
		if r < rank(TierReview) {
			r = rank(TierReview)
		}
		reasons = append(reasons, "irreversible")
	}

	// 影響範囲(blast radius)。
	switch strings.ToLower(strings.TrimSpace(op.BlastRadius)) {
	case "", "single":
		// 未指定 or 最小範囲: シグナルなし、escalation しない。
	case "portfolio", "external":
		if r < rank(TierHuman) {
			r = rank(TierHuman)
		}
		reasons = append(reasons, "blast radius "+strings.ToLower(op.BlastRadius))
	case "project":
		if r < rank(TierReview) {
			r = rank(TierReview)
		}
		reasons = append(reasons, "blast radius project")
	default:
		// 未知の blast radius は secure-by-default で review まで引き上げる
		// (未知 capability と同じ規約)。typo が黙って under-classify するのを防ぐ。
		if r < rank(TierReview) {
			r = rank(TierReview)
		}
		reasons = append(reasons, fmt.Sprintf("unknown blast radius %q → review (secure by default)", op.BlastRadius))
	}

	// consent ゲート: write/exec に確認があれば 1 段緩和(最低 log は残す)。
	// delete/auth/billing/data/external は緩和しない(OWASP: edit/delete consent は
	// 必須だが、破壊的・特権操作の人間承認を consent で代替させない)。
	if op.HasGate && (capLower == "write" || capLower == "exec") && r > rank(TierLog) {
		r--
		reasons = append(reasons, "consent gate present (−1)")
	}

	if r > rank(TierHuman) {
		r = rank(TierHuman)
	}
	tier := tierOf(r)
	return Decision{
		Name:       op.Name,
		Capability: capLower,
		Tier:       tier,
		Controls:   controlsFor(tier),
		Rationale:  strings.Join(reasons, "; "),
	}
}

// controlsFor は tier ごとに必要な control を返す
// (低リスク=自動+ログ、高リスク=人間承認+監査+アラート)。
func controlsFor(t Tier) []string {
	switch t {
	case TierAuto:
		return []string{"proceed"}
	case TierLog:
		return []string{"proceed", "audit_log"}
	case TierReview:
		return []string{"reviewer_approval", "audit_log"}
	default:
		return []string{"human_approval", "audit_log", "alert"}
	}
}

// ClassifyAll は複数操作を分類し、最も厳しい tier を gate 結果として返す。
// 出力は決定論的(Decisions は Name 昇順)。
func ClassifyAll(ops []Op) Result {
	res := Result{ByTier: map[string]int{}, Worst: TierAuto}
	ds := make([]Decision, 0, len(ops))
	for _, op := range ops {
		ds = append(ds, Classify(op))
	}
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Name < ds[j].Name })
	for _, d := range ds {
		res.ByTier[string(d.Tier)]++
		if rank(d.Tier) > rank(res.Worst) {
			res.Worst = d.Tier
		}
	}
	res.Decisions = ds
	return res
}
