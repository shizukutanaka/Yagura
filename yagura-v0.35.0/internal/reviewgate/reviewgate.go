// Package reviewgate は cortex flywheel ② Review の scanner 群の結果を、
// 決定論的に 1 つのゲート判定(allow / review / block)へ束ねる(新視点 v0.36)。
//
// 動機(ソクラテス的):
//
//	既存 scanner は各々独立した数値(aiverify risk_score / secretscan findings /
//	qualitycheck prohibited / astcheck high …)を返すが、変更を加えた agent/人間が
//	本当に知りたいのは「で、merge してよいのか?」という 1 つの答え。opsrisk が
//	*操作* を、pathpolicy が *パス* を tier 化するのに対し、本 package は ② Review
//	の合成判定を提供する(その ② 版の対)。
//
// 方針は secure-by-default: hard signal(秘密・禁止 lint・CRITICAL AI risk・
// high AST)はいずれも即 block。それ以外は AI risk score を閾値判定して review/allow。
// これは本リポジトリの一連の hardening(不正/危険を loud に)と同じ思想。
package reviewgate

import "fmt"

// Tier はゲート判定。厳しい順に block > review > allow。
type Tier string

const (
	// TierAllow はスキャン結果が全て安全圏で自動進行を許可するゲート判定。
	TierAllow Tier = "allow"
	// TierReview はリスクシグナルが閾値超かつ hard signal なしでレビュー推奨のゲート判定。
	TierReview Tier = "review"
	// TierBlock は critical secret・critical vuln 等の hard signal によるブロックのゲート判定。
	TierBlock Tier = "block"
)

// reviewRiskThreshold は block すべき hard signal が無い場合に、aiverify の
// risk_score がこれ以上なら review に落とす閾値。
const reviewRiskThreshold = 40

// Signals は ② Review scanner 群から算出済みのサマリ値(集約入力)。
type Signals struct {
	SecretFindings int `json:"secret_findings"` // secretscan total（任意 severity）
	AIRiskScore    int `json:"ai_risk_score"`   // aiverify 0-100
	AICritical     int `json:"ai_critical"`     // aiverify CRITICAL findings
	LintProhibited int `json:"lint_prohibited"` // qualitycheck prohibited count
	ASTHigh        int `json:"ast_high"`        // astcheck high-severity findings
}

// Decision は合成ゲート判定。Reasons は常に非空。
type Decision struct {
	Tier     Tier     `json:"tier"`
	Blockers []string `json:"blockers,omitempty"`
	Reasons  []string `json:"reasons"`
}

// Evaluate は Signals を決定論的に 1 つの Tier へ束ねる。
func Evaluate(s Signals) Decision {
	d := Decision{}
	block := func(b string) { d.Blockers = append(d.Blockers, b) }

	if s.SecretFindings > 0 {
		block(fmt.Sprintf("%d secret finding(s)", s.SecretFindings))
	}
	if s.LintProhibited > 0 {
		block(fmt.Sprintf("%d prohibited lint finding(s)", s.LintProhibited))
	}
	if s.AICritical > 0 {
		block(fmt.Sprintf("%d critical AI-risk finding(s)", s.AICritical))
	}
	if s.ASTHigh > 0 {
		block(fmt.Sprintf("%d high-severity AST finding(s)", s.ASTHigh))
	}

	if len(d.Blockers) > 0 {
		d.Tier = TierBlock
		d.Reasons = append(d.Reasons, "hard signals present → block (secure by default)")
		return d
	}
	if s.AIRiskScore >= reviewRiskThreshold {
		d.Tier = TierReview
		d.Reasons = append(d.Reasons, fmt.Sprintf("AI risk score %d ≥ %d → human review", s.AIRiskScore, reviewRiskThreshold))
		return d
	}
	d.Tier = TierAllow
	d.Reasons = append(d.Reasons, fmt.Sprintf("no hard signals; AI risk score %d < %d → allow", s.AIRiskScore, reviewRiskThreshold))
	return d
}
