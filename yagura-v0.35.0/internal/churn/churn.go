// Package churn は yagura に *時間軸* を持ち込む(v0.119.0)。
//
// なぜ必要か:
//
//	v0.118.0 までの ~24 個の lens はすべて **スナップショット**解析だった
//	(`grep -rl "git log\|churn" internal/` が 0 件)。既存の `internal/hotspot` は
//	名前に反して「複数 lens が同じ関数を指摘したか」という収束シグナルであり、
//	業界・研究で言う hotspot(複雑度 × 変更頻度)ではない。つまり「どのコードが
//	頻繁に触られているか」という次元が丸ごと欠けていた。
//
// 研究的根拠:
//
//   - Nagappan & Ball, "Use of Relative Code Churn Measures to Predict System
//     Defect Density", ICSE 2005 (Microsoft Research)。Windows Server 2003 を対象に、
//     **絶対的な** churn は defect density の予測子として貧弱である一方、
//     サイズや時間幅で正規化した **相対的な** churn 群(M1-M8)は高い予測力を持ち、
//     fault-prone / not fault-prone binary を **89.0%** の精度で判別できると報告。
//     本パッケージはその M1-M8 をそのまま実装し、**ランキングは相対 churn で行う**
//     (絶対 churn で並べないことが論文の中心的な知見)。
//
//   - Adam Tornhill の behavioral code analysis(hotspot = 複雑度 × 変更頻度)。
//     静的解析はスナップショットを見るが、behavioral analysis は時間的発展を見る。
//     上位 hotspot はコード全体のごく一部でありながら報告される欠陥の 25-70% を
//     占めると報告されている。本パッケージの RiskScore はこの規則
//     (「頻繁に変わる複雑なコードが本当に危険」)を実装する。
//
// 設計:
//
//	Parse は `git log --numstat` の出力を受け取る **純関数** で、git の実行を伴わない
//	(テストは実リポジトリ不要)。IO は薄い ReadGitLog に隔離する——本リポジトリの
//	content-based lens と同じ流儀。
//
// zero-dep(ADR-0001): stdlib のみ。
package churn

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// FileChange は 1 コミット中の 1 ファイルの増減。
type FileChange struct {
	Path    string
	Added   int
	Deleted int
}

// Commit は 1 コミットの numstat。
type Commit struct {
	Hash  string
	When  time.Time
	Files []FileChange
}

// Measures は Nagappan & Ball (ICSE 2005) の相対 churn 指標 M1-M8。
// 分母が 0 の場合は 0 を返す(NaN/Inf を外に出さない)。
type Measures struct {
	M1 float64 `json:"m1_churned_per_total_loc"`  // Churned LOC / Total LOC
	M2 float64 `json:"m2_deleted_per_total_loc"`  // Deleted LOC / Total LOC
	M3 float64 `json:"m3_files_churned_per_file"` // Files churned / File count
	M4 float64 `json:"m4_churn_count_per_file"`   // Churn count / Files churned
	M5 float64 `json:"m5_weeks_per_file"`         // Weeks of churn / File count
	M6 float64 `json:"m6_lines_per_week"`         // Lines worked on / Weeks of churn
	M7 float64 `json:"m7_churned_per_deleted"`    // Churned LOC / Deleted LOC
	M8 float64 `json:"m8_lines_per_churn_count"`  // Lines worked on / Churn count
}

// FileRisk は 1 ファイルの相対 churn と(あれば)複雑度を合成した危険度。
type FileRisk struct {
	Path          string  `json:"path"`
	ChurnedLOC    int     `json:"churned_loc"`
	DeletedLOC    int     `json:"deleted_loc"`
	ChurnCount    int     `json:"churn_count"` // このファイルを触ったコミット数
	SizeLOC       int     `json:"size_loc"`
	RelativeChurn float64 `json:"relative_churn"` // (churned+deleted)/size — 論文 M1+M2 のファイル版
	Complexity    int     `json:"complexity,omitempty"`
	RiskScore     float64 `json:"risk_score"` // relative churn × max(complexity,1) — Tornhill 則
}

// Report は churn 解析の結果。
type Report struct {
	Measures     Measures   `json:"measures"`
	Files        []FileRisk `json:"files"` // RiskScore 降順
	Commits      int        `json:"commits"`
	FilesRead    int        `json:"files_analyzed"`
	Skipped      int        `json:"skipped_unknown_size"` // サイズ不明で相対化できなかった数
	Hotspot      string     `json:"hotspot,omitempty"`    // 最も危険なファイル
	WeeksOfChurn int        `json:"weeks_of_churn"`
}

// Parse は `git log --numstat --format=%H|%aI` 形式の出力を解析する。
// バイナリファイル行("-\t-\tpath")は LOC シグナルを持たないため無視する。
// 空入力はエラーではなく空スライスを返す。
func Parse(out string) ([]Commit, error) {
	var commits []Commit
	var cur *Commit
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// commit ヘッダ行: <hash>|<iso8601>
		if hash, ts, ok := strings.Cut(line, "|"); ok && !strings.Contains(hash, "\t") {
			commits = append(commits, Commit{Hash: hash})
			cur = &commits[len(commits)-1]
			if when, err := time.Parse(time.RFC3339, ts); err == nil {
				cur.When = when
			}
			continue
		}
		// numstat 行: added\tdeleted\tpath
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || cur == nil {
			continue
		}
		if parts[0] == "-" || parts[1] == "-" {
			continue // binary
		}
		added, err1 := strconv.Atoi(parts[0])
		deleted, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}
		cur.Files = append(cur.Files, FileChange{
			Path: strings.TrimSpace(parts[2]), Added: added, Deleted: deleted,
		})
	}
	return commits, nil
}

// Analyze は commits と各ファイルの現在サイズ(LOC)から相対 churn 指標を計算し、
// RiskScore 降順に並べた Report を返す。complexity は nil 可(その場合 churn のみで
// 順位付け)。sizes に無いファイルは相対化できないので Skipped に数える——
// 0 除算して無限大を出したり、黙って 0 扱いにして「危険でない」と誤報しない。
func Analyze(commits []Commit, sizes map[string]int, complexity map[string]int) Report {
	rep := Report{Files: []FileRisk{}}
	rep.Commits = len(commits)

	type agg struct {
		added, deleted, touches int
	}
	per := map[string]*agg{}
	weeks := map[string]bool{}
	var churnedLOC, deletedLOC, churnCount int

	for _, c := range commits {
		if !c.When.IsZero() {
			y, w := c.When.ISOWeek()
			weeks[strconv.Itoa(y)+"-"+strconv.Itoa(w)] = true
		}
		for _, f := range c.Files {
			a, ok := per[f.Path]
			if !ok {
				a = &agg{}
				per[f.Path] = a
			}
			a.added += f.Added
			a.deleted += f.Deleted
			a.touches++
			churnedLOC += f.Added
			deletedLOC += f.Deleted
			churnCount++
		}
	}

	var totalLOC int
	for _, n := range sizes {
		totalLOC += n
	}
	linesWorkedOn := churnedLOC + deletedLOC
	filesChurned := len(per)
	fileCount := len(sizes)
	weeksOfChurn := len(weeks)
	rep.WeeksOfChurn = weeksOfChurn

	rep.Measures = Measures{
		M1: ratio(float64(churnedLOC), float64(totalLOC)),
		M2: ratio(float64(deletedLOC), float64(totalLOC)),
		M3: ratio(float64(filesChurned), float64(fileCount)),
		M4: ratio(float64(churnCount), float64(filesChurned)),
		M5: ratio(float64(weeksOfChurn), float64(fileCount)),
		M6: ratio(float64(linesWorkedOn), float64(weeksOfChurn)),
		M7: ratio(float64(churnedLOC), float64(deletedLOC)),
		M8: ratio(float64(linesWorkedOn), float64(churnCount)),
	}

	for path, a := range per {
		size, known := sizes[path]
		if !known || size <= 0 {
			rep.Skipped++
			continue
		}
		rel := float64(a.added+a.deleted) / float64(size)
		cx := 0
		if complexity != nil {
			cx = complexity[path]
		}
		weight := cx
		if weight < 1 {
			weight = 1 // complexity 不明なら churn のみで順位付け
		}
		rep.Files = append(rep.Files, FileRisk{
			Path:          path,
			ChurnedLOC:    a.added,
			DeletedLOC:    a.deleted,
			ChurnCount:    a.touches,
			SizeLOC:       size,
			RelativeChurn: rel,
			Complexity:    cx,
			RiskScore:     rel * float64(weight),
		})
	}
	rep.FilesRead = len(rep.Files)

	// RiskScore 降順。同点は path 昇順(Deterministic output ルール)。
	sort.SliceStable(rep.Files, func(i, j int) bool {
		if rep.Files[i].RiskScore != rep.Files[j].RiskScore {
			return rep.Files[i].RiskScore > rep.Files[j].RiskScore
		}
		return rep.Files[i].Path < rep.Files[j].Path
	})
	if len(rep.Files) > 0 {
		rep.Hotspot = rep.Files[0].Path
	}
	return rep
}

// ratio は 0 除算を避けた除算(分母 0 → 0)。
func ratio(num, den float64) float64 {
	if den == 0 {
		return 0
	}
	return num / den
}
