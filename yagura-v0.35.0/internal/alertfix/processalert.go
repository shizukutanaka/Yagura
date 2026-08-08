package alertfix

import (
	"fmt"
	"sort"
	"strings"
)

// SourceProcessRisk はプロセス指標(churn × ownership)から発火した alert の source。
// v0.122.0。外部センサー(vulns/ci/scorecard…)しか見ていなかった注意配分レイヤに、
// 初めて *リポジトリ内部の* プロセス指標を供給する。
const SourceProcessRisk Source = "process_risk"

// MaxProcessAlertsPerProject は 1 プロジェクトあたりの process alert 上限。
//
// なぜ上限を設けるか(研究的根拠):
//
//	Sadowski et al., "Lessons from Building Static Analysis Tools at Google",
//	CACM 2018。Google の初期の試みは「解析結果を自動で bug として起票する」方式で、
//	**起票された bug の 84% は修正されなかった**。同論文は "effective false positive"
//	を「開発者が結果を見た後に *何の行動も取らなかった* もの」と定義する——たとえ
//	指摘が技術的に正しくても、理解されず行動につながらなければ偽陽性と同じ害を持つ。
//	さらに code review に出すチェックは effective false positive 率 10% 未満が要件で、
//	「偽陽性率を決めるのはツール作者ではなく開発者」と述べている。
//
//	risk スコアの高いファイルを全件 alert 化すれば、まさにこの失敗を再現する
//	(1 リポジトリで数十件 = 誰も読まない)。よって **上位数件に絞る**。
//	`TestProcessRisk_VolumeIsCapped` がこの上限を固定する。
const MaxProcessAlertsPerProject = 3

// 発火条件は **絶対値のある研究由来シグナル** で決める(v0.122.0)。
//
// なぜ合成スコアの閾値ではないか:
//
//	processrisk.Score はリポジトリ内 percentile の平均なので、値の意味が
//	リポジトリごとに変わる。実際 dogfood では最上位ファイルでも 0.695 で、
//	当初置いた 0.85 という固定閾値は「絶対に発火しない」死んだ条件だった
//	(発火しない閾値は、常に発火する閾値と同じくらい無価値)。
//	欠陥データセットで較正していない以上、合成スコアに絶対的な「危険ライン」は
//	引けない——引けるふりをしない。
//
//	そこで発火判定は解釈可能な絶対条件に置き、合成スコアは **並び順** にだけ使う。
const (
	// ProcessRiskChurnMin: 相対 churn が 1.0 以上 = そのファイルは現在の行数より
	// 多くの行が書き換えられている(実質作り直され続けている)。Nagappan & Ball の
	// M1(churned/total LOC)がファイル単位で 1 を超える状態に相当する。
	ProcessRiskChurnMin = 1.0
	// ProcessRiskOwnershipMax: 最大所有者が半分未満 = 明確な持ち主が居ない。
	// Bird et al. の「所有権が低いほど fault が多い」に対応する。
	ProcessRiskOwnershipMax = 0.5
)

// ProcessRiskFile は alert 化のために必要な最小限のビュー。
// alertfix が processrisk を import しないのは、dashboard が sessionsummary を
// import しないのと同じ流儀(表示/判断層を薄く保つ)。
type ProcessRiskFile struct {
	Path          string
	Score         float64 // 並び順にのみ使う(発火判定には使わない)
	RelativeChurn float64
	Ownership     float64
	HasOwnership  bool
	Reasons       []string
}

// alerts は「研究由来の絶対条件」を満たすか。
func (f ProcessRiskFile) alerts() bool {
	if f.RelativeChurn >= ProcessRiskChurnMin {
		return true
	}
	return f.HasOwnership && f.Ownership < ProcessRiskOwnershipMax
}

// EvaluateProcessRisk はプロセス指標のリスク上位ファイルを alert に変換する。
//
// 設計上の制約はすべて Sadowski et al. の知見から来ている:
//   - 件数を絞る(MaxProcessAlertsPerProject)
//   - 条件を満たさなければ黙る(ProcessRiskChurnMin / ProcessRiskOwnershipMax)
//   - 全 alert が *なぜ* 出たか(Description)と *次に何をするか*(Recommendation +
//     SuggestedTool)を持つ——理解も行動もできない alert は effective false positive。
func EvaluateProcessRisk(project string, files []ProcessRiskFile, th Thresholds) []Alert {
	if len(files) == 0 {
		return nil
	}
	// 絶対条件を満たすものだけを対象にする
	cand := make([]ProcessRiskFile, 0, len(files))
	for _, f := range files {
		if f.alerts() {
			cand = append(cand, f)
		}
	}
	if len(cand) == 0 {
		return nil
	}
	// risk 降順、同点は path 昇順(決定論的)
	sort.SliceStable(cand, func(i, j int) bool {
		if cand[i].Score != cand[j].Score {
			return cand[i].Score > cand[j].Score
		}
		return cand[i].Path < cand[j].Path
	})
	if len(cand) > MaxProcessAlertsPerProject {
		cand = cand[:MaxProcessAlertsPerProject]
	}

	// 選抜は score 順(合成リスク上位を採る)だが、**提示は severity 順**にする
	// ——alertfix の他 source(rankAlerts)と並び規約を揃えるため。
	// MEDIUM が LOW の下に出ると読み手が優先度を誤る。
	sevRank := map[Severity]int{SevCritical: 0, SevHigh: 1, SevMedium: 2, SevLow: 3}
	sort.SliceStable(cand, func(i, j int) bool {
		si, sj := sevRank[processSeverity(cand[i])], sevRank[processSeverity(cand[j])]
		if si != sj {
			return si < sj
		}
		if cand[i].Score != cand[j].Score {
			return cand[i].Score > cand[j].Score
		}
		return cand[i].Path < cand[j].Path
	})

	out := make([]Alert, 0, len(cand))
	for _, f := range cand {
		out = append(out, Alert{
			// ID は path を qualifier にして安定させる(resolve/snooze が効くように)
			ID:       buildID(project, SourceProcessRisk, f.Path),
			Project:  project,
			Source:   SourceProcessRisk,
			Severity: processSeverity(f),
			Title:    fmt.Sprintf("High process risk: %s", f.Path),
			// なぜ出たかを必ず添える(数値だけ返さない)
			Description: describeProcessRisk(f),
			Recommendation: "Review this file before adding features to it: high churn with weak " +
				"ownership is the pattern most associated with defects in the literature. " +
				"Consider splitting it, adding tests around the churning region, or assigning a clear owner.",
			SuggestedTool: "yagura_process_risk",
			SuggestedArgs: map[string]any{"slug": project, "limit": MaxProcessAlertsPerProject},
			MetricFloat:   f.Score,
		})
	}
	return out
}

// describeProcessRisk は alert の根拠文を組み立てる。reasons が空でも
// 最低限「何が起きたか」を必ず書く(理解できない alert を出さない)。
func describeProcessRisk(f ProcessRiskFile) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Process-metric risk (rank %.2f within repo; churn and ownership signals, complexity not scored). "+
		"Relative churn %.2f", f.Score, f.RelativeChurn)
	if f.HasOwnership {
		fmt.Fprintf(&b, ", top-owner share %.0f%%", f.Ownership*100)
	}
	b.WriteString(".")
	if len(f.Reasons) > 0 {
		b.WriteString(" Evidence: ")
		b.WriteString(strings.Join(f.Reasons, "; "))
		b.WriteString(".")
	} else {
		b.WriteString(" Driven by relative churn and change count in the analyzed window.")
	}
	return b.String()
}

// processSeverity は score を severity に写す。critical は使わない——
// 「そのうち壊れるかもしれない」という予兆であって、既知脆弱性のような
// 即時対応事象ではないため(severity のインフレを避ける)。
func processSeverity(f ProcessRiskFile) Severity {
	// 所有者不在 + 高 churn の重なりがいちばん危ない(両論文の交差)
	noOwner := f.HasOwnership && f.Ownership < ProcessRiskOwnershipMax
	switch {
	case f.RelativeChurn >= 3*ProcessRiskChurnMin && noOwner:
		return SevHigh
	case f.RelativeChurn >= 3*ProcessRiskChurnMin || noOwner:
		return SevMedium
	default:
		return SevLow
	}
}
