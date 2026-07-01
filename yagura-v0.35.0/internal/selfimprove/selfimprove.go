// Package selfimprove は「再帰的自己改善(recursive self-improvement, RSI)」を
// Yagura の設計思想に沿って安全な形で取り込む決定論的カーネルである。
//
// 文献の整理(調査):
//   - STOP(arXiv 2310.02304): モデルを固定したまま「足場(scaffold/harness)」を
//     最適化対象とみなす。= 自己改善は重みではなく harness に宿る。Yagura は
//     まさにその harness なので、RSI の対象は Yagura が観測する harness 自身。
//   - Darwin Gödel Machine(arXiv 2505.22954): Gödel machine の「改善を*証明*して
//     から採用」を「*経験的検証*(produce → trial → select)」に緩める。良かった
//     ものは archive に残す。→ 盲目採用せず、計測で良し悪しを判定して gate する。
//   - "Your Agent May Misevolve"(arXiv 2509.26354)/ ICLR 2026 RSI workshop:
//     自己進化エージェントには misevolution の創発リスクがあり、「feedback loop を
//     計装し、報酬をリアルタイムに記録し、変更を guardrail 内に収め、記憶を監査可能に
//     保つ」ときだけ安全に自己改善できる。
//
// Yagura のスタンス(control-plane thesis = "kernel not brain"):
//
//	Yagura は **自分のコードを書き換えない**(それは ADR-0001 に反し、misevolution の
//	危険そのもの)。代わりに、エージェントの harness レベル自己改善ループを
//	**計測可能・gate 可能・監査可能**にする決定論的 substrate になる。
//	本パッケージは「観測した自己メトリクスから、優先度つきの改善提案を rule-based に
//	生成し(STOP の方向)、前回との差分から後退を検出して採用/巻き戻しを助言する
//	(Darwin Gödel の経験的検証)」。提案はあくまで助言で、実行は人間/エージェント側。
//	LLM は呼ばない。決定論的で、出力は audit log に残せる。
package selfimprove

import (
	"fmt"
	"sort"
)

// しきい値(named const = テスト可能 & 仕様として明示)。
const (
	// reliability: この回数以上呼ばれ、エラー率がこれ以上なら「要修繕」。
	minCallsForReliability = 5
	highErrorRate          = 0.20 // 20%+ は high
	mediumErrorRate        = 0.05 // 5%+ は medium
	// token economy: 平均応答がこのバイト数以上 かつ よく呼ばれる tool は圧縮余地。
	largeResponseBytes = 4096
	chattyCallShare    = 0.10 // セッション総 call の 10%+ を占める
	// retire: このスコア未満の skill は retire 候補(MUSE-Autoskill の self-cleaning)。
	lowSkillScore = 40
	// fitness: エラー率がこの絶対値以上「悪化」したら regression として high。
	regressionDelta = 0.10
)

// ToolStat は 1 tool の観測値(yagura_token_stats 由来を想定)。
type ToolStat struct {
	Name         string `json:"name"`
	Calls        uint64 `json:"calls"`
	Errors       uint64 `json:"errors"`
	AvgRespBytes int    `json:"avg_resp_bytes"`
}

// SkillScore は 1 skill の監査結果(yagura_skill_audit 由来を想定)。
type SkillScore struct {
	Path   string `json:"path"`
	Score  int    `json:"score"`
	Retire bool   `json:"retire"`
}

// Snapshot は harness 自身の観測スナップショット。
// PrevTools を渡すと前回窓との fitness(後退検出)も行う(Darwin Gödel の経験的検証)。
type Snapshot struct {
	Tools        []ToolStat   `json:"tools"`
	PrevTools    []ToolStat   `json:"prev_tools,omitempty"`
	Skills       []SkillScore `json:"skills,omitempty"`
	CoverageGaps []string     `json:"coverage_gaps,omitempty"` // 例: Fowler 行列で sensor/guide が欠ける象限
	SessionCalls uint64       `json:"session_calls,omitempty"` // 窓内の総 call 数(比率しきい値用)
}

// Proposal は 1 件の改善提案。
type Proposal struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"` // reliability | token_economy | retire | coverage | fitness
	Target    string         `json:"target"`
	Severity  string         `json:"severity"` // high | medium | low
	Rationale string         `json:"rationale"`
	Action    string         `json:"action"`
	Evidence  map[string]any `json:"evidence,omitempty"`
}

// Report は自己改善カーネルの出力。
type Report struct {
	Proposals  []Proposal     `json:"proposals"`
	ByKind     map[string]int `json:"by_kind"`
	BySeverity map[string]int `json:"by_severity"`
	Summary    string         `json:"summary"`
}

// Analyze は snapshot を rule-based に評価し、優先度つき改善提案を返す。
// 出力は決定論的(severity 降順 → kind → target で安定整列)。LLM は使わない。
func Analyze(snap Snapshot) Report {
	var ps []Proposal
	ps = append(ps, reliabilityProposals(snap.Tools)...)
	ps = append(ps, tokenEconomyProposals(snap.Tools, snap.SessionCalls)...)
	ps = append(ps, retireProposals(snap.Skills)...)
	ps = append(ps, coverageProposals(snap.CoverageGaps)...)
	ps = append(ps, fitnessProposals(snap.Tools, snap.PrevTools)...)

	sortProposals(ps)
	rep := Report{Proposals: ps, ByKind: map[string]int{}, BySeverity: map[string]int{}}
	for _, p := range ps {
		rep.ByKind[p.Kind]++
		rep.BySeverity[p.Severity]++
	}
	rep.Summary = summarize(rep)
	return rep
}

// reliabilityProposals は① 高エラー率の tool を schema 見直し / repair_args 対象として提案する。
func reliabilityProposals(tools []ToolStat) []Proposal {
	ps := make([]Proposal, 0, len(tools))
	for _, t := range tools {
		if t.Calls < minCallsForReliability || t.Errors == 0 {
			continue
		}
		rate := float64(t.Errors) / float64(t.Calls)
		sev := ""
		switch {
		case rate >= highErrorRate:
			sev = "high"
		case rate >= mediumErrorRate:
			sev = "medium"
		}
		if sev == "" {
			continue
		}
		ps = append(ps, Proposal{
			ID:       "reliability:" + t.Name,
			Kind:     "reliability",
			Target:   t.Name,
			Severity: sev,
			Rationale: fmt.Sprintf("%s fails %.0f%% of calls (%d/%d) — likely a schema/usage mismatch the harness can fix",
				t.Name, rate*100, t.Errors, t.Calls),
			Action:   "review the tool's input schema and description; tighten arg validation or add a usage example",
			Evidence: map[string]any{"calls": t.Calls, "errors": t.Errors, "error_rate": round2(rate)},
		})
	}
	return ps
}

// tokenEconomyProposals は② 大きい応答をよく呼ぶ tool を圧縮余地(input token 削減)として提案する。
func tokenEconomyProposals(tools []ToolStat, sessionCalls uint64) []Proposal {
	ps := make([]Proposal, 0, len(tools))
	for _, t := range tools {
		if t.AvgRespBytes < largeResponseBytes {
			continue
		}
		share := 0.0
		if sessionCalls > 0 {
			share = float64(t.Calls) / float64(sessionCalls)
		}
		if sessionCalls > 0 && share < chattyCallShare {
			continue // 大きいが滅多に呼ばれない → 後回し
		}
		sev := "low"
		if share >= chattyCallShare {
			sev = "medium"
		}
		ps = append(ps, Proposal{
			ID:       "token_economy:" + t.Name,
			Kind:     "token_economy",
			Target:   t.Name,
			Severity: sev,
			Rationale: fmt.Sprintf("%s returns ~%d bytes/call and is %.0f%% of session calls — every byte is agent input token",
				t.Name, t.AvgRespBytes, share*100),
			Action:   "add/use summary_only or a limit arg, or enable YAGURA_MCP_COMPACT, to shrink the response",
			Evidence: map[string]any{"avg_resp_bytes": t.AvgRespBytes, "calls": t.Calls, "call_share": round2(share)},
		})
	}
	return ps
}

// retireProposals は③ 低品質 skill を retire 候補として提案する
// (ライブラリが育つほど retrieval noise を増やすため)。
func retireProposals(skills []SkillScore) []Proposal {
	ps := make([]Proposal, 0, len(skills))
	for _, s := range skills {
		if !s.Retire && s.Score >= lowSkillScore {
			continue
		}
		ps = append(ps, Proposal{
			ID:       "retire:" + s.Path,
			Kind:     "retire",
			Target:   s.Path,
			Severity: "low",
			Rationale: fmt.Sprintf("%s scores %d (retire=%v) — low-value skills dilute retrieval as the library grows",
				s.Path, s.Score, s.Retire),
			Action:   "retire or rewrite this skill (MUSE-Autoskill self-cleaning); never auto-delete — human decides",
			Evidence: map[string]any{"score": s.Score, "retire": s.Retire},
		})
	}
	return ps
}

// coverageProposals は④ Fowler 行列で欠けている feedforward/feedback を補う提案。
func coverageProposals(gaps []string) []Proposal {
	ps := make([]Proposal, 0, len(gaps))
	for _, q := range gaps {
		ps = append(ps, Proposal{
			ID:        "coverage:" + q,
			Kind:      "coverage",
			Target:    q,
			Severity:  "medium",
			Rationale: fmt.Sprintf("harness has no control in %q — a blind spot in the feedforward/feedback matrix", q),
			Action:    "add a guide ([G]) or sensor ([S]) covering this quadrant",
			Evidence:  map[string]any{"quadrant": q},
		})
	}
	return ps
}

// fitnessProposals は⑤ 前回窓との比較で「後退」を検出する
// (Darwin Gödel の produce→trial→select)。良くなった/悪くなったを助言し、
// 盲目的な採用を避ける(misevolution 対策)。
func fitnessProposals(tools, prevTools []ToolStat) []Proposal {
	if len(prevTools) == 0 {
		return nil
	}
	prev := make(map[string]ToolStat, len(prevTools))
	for _, t := range prevTools {
		prev[t.Name] = t
	}
	var ps []Proposal
	for _, t := range tools {
		p, ok := prev[t.Name]
		if !ok || t.Calls < minCallsForReliability || p.Calls == 0 {
			continue
		}
		now := float64(t.Errors) / float64(t.Calls)
		before := float64(p.Errors) / float64(p.Calls)
		delta := now - before
		if delta >= regressionDelta {
			ps = append(ps, Proposal{
				ID:       "fitness:" + t.Name,
				Kind:     "fitness",
				Target:   t.Name,
				Severity: "high",
				Rationale: fmt.Sprintf("%s error rate rose %.0f%%→%.0f%% since the last window — a recent change may have regressed it",
					t.Name, before*100, now*100),
				Action:   "treat the last harness change to this tool as unvalidated: revert or fix before keeping it",
				Evidence: map[string]any{"prev_rate": round2(before), "cur_rate": round2(now), "delta": round2(delta)},
			})
		}
	}
	return ps
}

var severityRank = map[string]int{"high": 0, "medium": 1, "low": 2}
var kindRank = map[string]int{"fitness": 0, "reliability": 1, "coverage": 2, "token_economy": 3, "retire": 4}

func sortProposals(ps []Proposal) {
	sort.SliceStable(ps, func(i, j int) bool {
		if severityRank[ps[i].Severity] != severityRank[ps[j].Severity] {
			return severityRank[ps[i].Severity] < severityRank[ps[j].Severity]
		}
		if kindRank[ps[i].Kind] != kindRank[ps[j].Kind] {
			return kindRank[ps[i].Kind] < kindRank[ps[j].Kind]
		}
		return ps[i].Target < ps[j].Target
	})
}

func summarize(r Report) string {
	if len(r.Proposals) == 0 {
		return "no improvement proposals — harness metrics are within thresholds"
	}
	return fmt.Sprintf("%d proposal(s): %d high / %d medium / %d low",
		len(r.Proposals), r.BySeverity["high"], r.BySeverity["medium"], r.BySeverity["low"])
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}
