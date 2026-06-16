// Package riskreason は脆弱性の「複合判断」を rule-based / deterministic に行う
// Cyber Risk Reasoning Layer。
//
// 着想は「AIセキュリティは検知ロジックから複合判断へ」— CVSS 単体では本当の
// 修正優先度は決まらず、(1) 脆弱性深刻度、(2) 資産の業務重要度、(3) 到達可能性
// (公開/認証/WAF)、(4) 攻撃可能性(KEV/公開エクスプロイト)、(5) 横展開の
// blast radius(依存元数)、(6) パッチの業務影響を合わせて初めて優先度が出る、
// という考え方を構造化したもの。
//
// この層の設計要件(記事準拠)を rule-based で満たす:
//   - 根拠提示: Score がどの factor をどれだけ加点/減点したか(Factors)を返す
//   - 再現性 : 同じ入力 → 同じ判断。LLM を使わない決定論(Yagura の trust base)
//   - 文脈ギャップの明示: 評価できなかった要因(公開状況/エクスプロイト等)を
//     Unknowns として出す ——「AI が複合判断できる文脈を組織が持っているか」が
//     次の勝負、という記事の主張をそのまま機械可読にする
//   - リスクと対応の分離: パッチの業務影響は risk score を下げず、Recommendation
//     (今すぐ遮断 / 監視強化 / パッチ / 例外承認 / 経営報告)側に効く
//
// Yagura は判断を「自動実行」しない。本層は人間(SOC/CSIRT/脆弱性管理/経営)が
// 検証できる根拠付きの優先度を返す判断補助エンジンであり、audit log と併せて
// Human-in-the-Loop を前提とする。
package riskreason

import (
	"fmt"
	"sort"
	"strings"
)

// Priority は修正優先度バンド。
type Priority string

const (
	// PriorityNow は今すぐ対応(遮断/隔離も検討)。
	PriorityNow Priority = "NOW"
	// PrioritySoon は次の保守ウィンドウで対応。
	PrioritySoon Priority = "SOON"
	// PriorityScheduled は計画的に対応。
	PriorityScheduled Priority = "SCHEDULED"
	// PriorityMonitor は監視継続・通常サイクル。
	PriorityMonitor Priority = "MONITOR"
	// PriorityDefer は後回し(低重要度/archived)。
	PriorityDefer Priority = "DEFER"
)

// Input は 1 脆弱性 × 1 資産の複合エビデンス。
// *bool は「不明(nil)」と「false」を区別する — 不明は Unknowns に積み、
// 組織が整えるべき文脈ギャップとして可視化する。
type Input struct {
	CVE      string  `json:"cve"`
	CVSS     float64 `json:"cvss"`     // 0-10 base score(0 = severity 文字列で判定)
	Severity string  `json:"severity"` // CVSS が無い時の代替("critical"/"high"/...)

	// 資産・業務文脈(registry 由来)。
	AssetPriority int      `json:"asset_priority"` // 0-5(project.Priority)
	Stage         string   `json:"stage"`          // active/maintenance/paused/archived
	Tags          []string `json:"tags"`           // production / pii / external 等

	// 到達可能性。
	InternetExposed *bool `json:"internet_exposed"`
	AuthRequired    *bool `json:"auth_required"`
	WAFProtected    *bool `json:"waf_protected"`

	// 攻撃可能性(脅威インテリジェンス)。
	KnownExploited *bool   `json:"known_exploited"` // CISA KEV 等、実環境で悪用中
	PublicExploit  *bool   `json:"public_exploit"`  // エクスプロイトコード公開
	EPSS           float64 `json:"epss"`            // Exploit Prediction Scoring System 確率 0-1(0=不明)
	Automatable    *bool   `json:"automatable"`     // SSVC: 攻撃が自動化/wormable か

	// 横展開の blast radius(projectgraph の Impact 由来 = 依存元数)。
	Dependents int `json:"dependents"`

	// パッチの業務影響(true = すぐ当てられない)。risk score ではなく対応に効く。
	PatchBlocksBusiness *bool `json:"patch_blocks_business"`
}

// Factor は score を動かした 1 要因(根拠提示)。
type Factor struct {
	Name   string `json:"name"`
	Delta  int    `json:"delta"`
	Detail string `json:"detail"`
}

// Result は複合判断の出力。
type Result struct {
	CVE            string   `json:"cve"`
	Score          int      `json:"score"`              // 0-100 の複合リスク
	Priority       Priority `json:"priority"`           // 修正優先度バンド(Yagura 独自)
	SSVC           SSVC     `json:"ssvc"`               // CISA SSVC 整合の決定木出力(Act/Attend/...)
	Factors        []Factor `json:"factors"`            // 根拠(加点/減点の内訳)
	Unknowns       []string `json:"unknowns,omitempty"` // 評価できなかった文脈ギャップ
	Recommendation string   `json:"recommendation"`
}

// Weights は複合判断の各 factor の重み(rule-based・決定論)。すべて整数の加点/減点で、
// DefaultWeights() が現行のチューニング値。運用側が ScoreWith に渡すことで、組織の
// リスク許容度に合わせて調整できる(custom rule loading の基盤)。
// json タグ付きなので、部分的な override JSON を DefaultWeights() の上に Unmarshal
// するだけで「指定した factor だけ差し替え」が効く(未指定 field は既定値のまま)。
type Weights struct {
	// (1) 深刻度ベース
	SevCritical int `json:"sev_critical"` // CVSS >= 9.0
	SevHigh     int `json:"sev_high"`     // 7.0-8.9
	SevMedium   int `json:"sev_medium"`   // 4.0-6.9
	SevLow      int `json:"sev_low"`      // < 4.0
	// (2) 資産の業務重要度
	AssetPriorityPerLevel int `json:"asset_priority_per_level"` // AssetPriority(0-5)に乗算
	StageArchived         int `json:"stage_archived"`
	StagePaused           int `json:"stage_paused"`
	StageMaintenance      int `json:"stage_maintenance"`
	TagExposure           int `json:"tag_exposure"`  // production/external tag
	TagSensitive          int `json:"tag_sensitive"` // pii/secret tag
	TagLowValue           int `json:"tag_low_value"` // dev/test tag(他 tag が無いときのみ適用)
	// (3) 到達可能性
	Exposed      int `json:"exposed"`       // internet-exposed == true
	NotExposed   int `json:"not_exposed"`   // == false
	NoAuth       int `json:"no_auth"`       // AuthRequired == false(認証不要 = 危険)
	AuthRequired int `json:"auth_required"` // == true
	NoWAF        int `json:"no_waf"`        // WAFProtected == false
	WAFProtected int `json:"waf_protected"` // == true
	// (4) 攻撃可能性
	KnownExploited int `json:"known_exploited"` // KEV
	PublicExploit  int `json:"public_exploit"`
	EPSSHigh       int `json:"epss_high"`   // EPSS >= 0.5
	EPSSMedium     int `json:"epss_medium"` // EPSS >= 0.1 (CISA "act" 閾値)
	// (5) 横展開
	BlastRadiusPerDep int `json:"blast_radius_per_dep"` // 依存元 1 件あたり
	BlastRadiusCap    int `json:"blast_radius_cap"`     // 上限
	// 優先度バンド閾値(score >= 値)
	BandNow       int `json:"band_now"`
	BandSoon      int `json:"band_soon"`
	BandScheduled int `json:"band_scheduled"`
	BandMonitor   int `json:"band_monitor"`
}

// DefaultWeights は現行のチューニング値(これまでの定数/マジックナンバーを集約)。
func DefaultWeights() Weights {
	return Weights{
		SevCritical: 45, SevHigh: 32, SevMedium: 18, SevLow: 7,
		AssetPriorityPerLevel: 4,
		StageArchived:         -20, StagePaused: -12, StageMaintenance: -3,
		TagExposure: 8, TagSensitive: 8, TagLowValue: -6,
		Exposed: 15, NotExposed: -10,
		NoAuth: 12, AuthRequired: -6,
		NoWAF: 4, WAFProtected: -8,
		KnownExploited: 28, PublicExploit: 12,
		EPSSHigh: 20, EPSSMedium: 10,
		BlastRadiusPerDep: 3, BlastRadiusCap: 18,
		BandNow: 75, BandSoon: 55, BandScheduled: 35, BandMonitor: 15,
	}
}

// Score は DefaultWeights() で複合判断する(後方互換の既定 API)。
func Score(in Input) Result { return ScoreWith(in, DefaultWeights()) }

// ScoreWith は与えられた Weights で複合エビデンスから修正優先度を導く。完全に決定論的。
func ScoreWith(in Input, w Weights) Result {
	r := Result{CVE: in.CVE}
	score := 0
	add := func(name string, delta int, detail string) {
		if delta != 0 {
			r.Factors = append(r.Factors, Factor{Name: name, Delta: delta, Detail: detail})
			score += delta
		}
	}

	// ── (1) 深刻度ベース ──
	switch sev := severityBucket(in.CVSS, in.Severity); sev {
	case "critical":
		add("severity", w.SevCritical, "CVSS critical (>=9.0)")
	case "high":
		add("severity", w.SevHigh, "CVSS high (7.0-8.9)")
	case "medium":
		add("severity", w.SevMedium, "CVSS medium (4.0-6.9)")
	case "low":
		add("severity", w.SevLow, "CVSS low (<4.0)")
	default:
		// 深刻度なし。severity 文字列が提供されたが未認識なのか、そもそも
		// 未指定なのかを区別する(後者と混同すると operator が typo に気づけない)。
		if strings.TrimSpace(in.Severity) != "" {
			r.Unknowns = append(r.Unknowns, fmt.Sprintf("severity %q not recognized (no score applied)", in.Severity))
		} else {
			r.Unknowns = append(r.Unknowns, "severity (no CVSS or severity string provided)")
		}
	}

	// ── (2) 資産の業務重要度 ──
	if in.AssetPriority > 0 {
		d := in.AssetPriority * w.AssetPriorityPerLevel
		add("asset_priority", d, fmt.Sprintf("business criticality (priority %d/5)", in.AssetPriority))
	}
	switch strings.ToLower(strings.TrimSpace(in.Stage)) {
	case "archived":
		add("stage", w.StageArchived, "asset archived (out of service)")
	case "paused":
		add("stage", w.StagePaused, "asset paused")
	case "maintenance":
		add("stage", w.StageMaintenance, "asset in maintenance")
	}
	add(tagSignal(in.Tags, w))

	// ── (3) 到達可能性 ──
	tri(in.InternetExposed, &r, &score,
		"reachability", w.Exposed, "internet-exposed (reachable by external attackers)",
		w.NotExposed, "not internet-exposed (internal only)",
		"internet exposure")
	tri(in.AuthRequired, &r, &score,
		"auth", w.AuthRequired, "authentication required to reach",
		w.NoAuth, "reachable without authentication",
		"authentication requirement")
	tri(in.WAFProtected, &r, &score,
		"waf", w.WAFProtected, "WAF/edge filtering in front",
		w.NoWAF, "no WAF in front",
		"WAF coverage")

	// ── (4) 攻撃可能性 ──
	triHi(in.KnownExploited, &r, &score, "known_exploited", w.KnownExploited,
		"actively exploited in the wild (e.g. CISA KEV)", "known-exploited status (CISA KEV)")
	triHi(in.PublicExploit, &r, &score, "public_exploit", w.PublicExploit,
		"public exploit code available", "public exploit availability")
	// EPSS(exploit 予測確率)。>=0.5 高 / >=0.1 中(CISA "act" 閾値)。0 は不明。
	switch {
	case in.EPSS >= 0.5:
		add("epss", w.EPSSHigh, fmt.Sprintf("EPSS %.2f — high predicted exploitation probability", in.EPSS))
	case in.EPSS >= 0.1:
		add("epss", w.EPSSMedium, fmt.Sprintf("EPSS %.2f — elevated exploitation probability (>=0.1)", in.EPSS))
	case in.EPSS <= 0:
		r.Unknowns = append(r.Unknowns, "exploit probability (EPSS)")
	}

	// ── (5) 横展開 blast radius ──
	if in.Dependents > 0 {
		d := in.Dependents * w.BlastRadiusPerDep
		if d > w.BlastRadiusCap {
			d = w.BlastRadiusCap
		}
		add("blast_radius", d, fmt.Sprintf("%d asset(s) depend on this (lateral blast radius)", in.Dependents))
	}

	// パッチ業務影響は score ではなく recommendation に効くが、不明(nil)なら他の *bool と
	// 同様に文脈ギャップとして surface する(recommend は nil を「影響なし」とみなすため)。
	if in.PatchBlocksBusiness == nil {
		r.Unknowns = append(r.Unknowns, "patch business impact")
	}

	if score < 0 {
		score = 0
	}
	if score > 100 {
		score = 100
	}
	r.Score = score
	r.Priority = band(score, w)
	r.SSVC = evalSSVC(in)
	r.Recommendation = recommend(r.Priority, in, r.Unknowns)
	return r
}

// ScoreAll は DefaultWeights() で複数 finding を score し、score 降順で返す。
func ScoreAll(inputs []Input) []Result { return ScoreAllWith(inputs, DefaultWeights()) }

// ScoreAllWith は与えられた Weights で複数 finding を score し、score 降順
// (tie は CVE 昇順)で返す。
func ScoreAllWith(inputs []Input, w Weights) []Result {
	out := make([]Result, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, ScoreWith(in, w))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].CVE < out[j].CVE
	})
	return out
}

// ─── helpers ───

func severityBucket(cvss float64, sev string) string {
	if cvss > 0 {
		switch {
		case cvss >= 9.0:
			return "critical"
		case cvss >= 7.0:
			return "high"
		case cvss >= 4.0:
			return "medium"
		default:
			return "low"
		}
	}
	switch strings.ToLower(strings.TrimSpace(sev)) {
	case "critical":
		return "critical"
	case "high", "important":
		return "high"
	case "medium", "moderate":
		return "medium"
	case "low":
		return "low"
	}
	return ""
}

// tagSignal は tags から 1 つの集約 factor を返す(production/data 機微 → 加点、
// dev/test → 減点。多重加点を避けるため上限つき)。
func tagSignal(tags []string, w Weights) (string, int, string) {
	exposure, sensitive, lowval := false, false, false
	for _, t := range tags {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "production", "prod", "external", "public", "internet-facing":
			exposure = true
		case "pii", "secret", "financial", "payment", "audit", "confidential":
			sensitive = true
		case "internal", "dev", "development", "test", "staging", "sandbox", "experimental":
			lowval = true
		}
	}
	delta := 0
	parts := []string{}
	if exposure {
		delta += w.TagExposure
		parts = append(parts, "production/external")
	}
	if sensitive {
		delta += w.TagSensitive
		parts = append(parts, "sensitive-data")
	}
	if lowval && delta == 0 {
		delta += w.TagLowValue
		parts = append(parts, "non-production")
	}
	if delta == 0 {
		return "tags", 0, ""
	}
	return "tags", delta, "asset tags: " + strings.Join(parts, ", ")
}

// tri は *bool(不明/真/偽)を factor に落とす(真と偽で別 detail、nil は Unknowns)。
func tri(v *bool, r *Result, score *int, name string, dTrue int, detailTrue string, dFalse int, detailFalse string, unknownLabel string) {
	if v == nil {
		r.Unknowns = append(r.Unknowns, unknownLabel)
		return
	}
	if *v {
		if dTrue != 0 {
			r.Factors = append(r.Factors, Factor{Name: name, Delta: dTrue, Detail: detailTrue})
			*score += dTrue
		}
	} else if dFalse != 0 {
		r.Factors = append(r.Factors, Factor{Name: name, Delta: dFalse, Detail: detailFalse})
		*score += dFalse
	}
}

// triHi は「真の時だけ加点、偽は無印、nil は Unknowns」の脅威インテル型 factor。
func triHi(v *bool, r *Result, score *int, name string, dTrue int, detailTrue, unknownLabel string) {
	if v == nil {
		r.Unknowns = append(r.Unknowns, unknownLabel)
		return
	}
	if *v {
		r.Factors = append(r.Factors, Factor{Name: name, Delta: dTrue, Detail: detailTrue})
		*score += dTrue
	}
}

func band(score int, w Weights) Priority {
	switch {
	case score >= w.BandNow:
		return PriorityNow
	case score >= w.BandSoon:
		return PrioritySoon
	case score >= w.BandScheduled:
		return PriorityScheduled
	case score >= w.BandMonitor:
		return PriorityMonitor
	default:
		return PriorityDefer
	}
}

func isTrue(b *bool) bool { return b != nil && *b }

func recommend(p Priority, in Input, unknowns []string) string {
	var sb strings.Builder
	switch p {
	case PriorityNow:
		if isTrue(in.KnownExploited) {
			sb.WriteString("Actively exploited — ")
		}
		if isTrue(in.PatchBlocksBusiness) {
			sb.WriteString("fix now, but patching has business impact: apply compensating controls (tighten access / WAF / raise monitoring), record a risk-acceptance exception, and escalate to management")
		} else {
			sb.WriteString("patch immediately and raise monitoring until patched")
		}
		if isTrue(in.InternetExposed) {
			sb.WriteString("; restrict external reachability in the meantime")
		}
	case PrioritySoon:
		sb.WriteString("patch in the next maintenance window; raise monitoring meanwhile")
		if isTrue(in.PatchBlocksBusiness) {
			sb.WriteString(" and record the deferral as a risk-acceptance exception")
		}
	case PriorityScheduled:
		sb.WriteString("schedule a patch on the normal cadence")
	case PriorityMonitor:
		sb.WriteString("low active risk — keep monitoring; patch on the normal cadence")
	case PriorityDefer:
		sb.WriteString("defer (low criticality / archived asset); revisit if exposure or exploit status changes")
	}
	if len(unknowns) > 0 {
		sb.WriteString(fmt.Sprintf(". %d risk factor(s) are unknown (%s) — wire that context in to sharpen this judgment",
			len(unknowns), strings.Join(unknowns, ", ")))
	}
	return sb.String()
}
