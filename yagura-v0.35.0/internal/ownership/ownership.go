// Package ownership は「誰がそのコードを書いたか」を計測する(v0.120.0)。
//
// v0.119.0 で churn(どれだけ変わったか)という時間軸を入れたが、behavioral code
// analysis のもう半分——**所有権**(誰が書いたか)——は空白のままだった。
//
// 研究的根拠:
//
//   - Bird, Nagappan, Murphy, Gall, Devanbu, "Don't Touch My Code! Examining the
//     Effects of Ownership on Software Quality", ESEC/FSE 2011 (Microsoft Research)。
//     Windows Vista / Windows 7 を対象に、**低専門性(minor)寄与者の数**と
//     **最大所有者の所有割合**が pre-release fault / post-release failure の双方と
//     関係することを示した。本パッケージはその 4 指標を論文どおりに実装する:
//     Minor(寄与率 < 5% の寄与者数)/ Major(>= 5%)/ Total(総寄与者数)/
//     Ownership(最大寄与者の割合)。閾値 5% は論文の定義そのままで、
//     `TestMinorThresholdIsFivePercent` が固定している。
//     論文の含意は「所有権が低く minor 寄与者が多いほど危険」なので、
//     **ランキングは Ownership 昇順**(危険なものが先頭)。
//
//   - 関連: Meneely & Williams は Linux kernel で「10 人以上が触ったソースファイルは
//     セキュリティ脆弱性を含む確率が 16 倍」と報告(同論文の関連研究より)。
//     Total が多いこと自体が独立したリスクシグナルであることの傍証。
//
// 2026 年の拡張(**論文には無い、本リポジトリ独自の指標**):
//
//	AIProportion / HumanOwnership / TopHumanOwner。Bird らの機序は「専門性の低い
//	寄与者は誤りを入れやすい」だが、AI エージェントが大半のコミットを書く現代の
//	リポジトリでは「人間の所有権が実質ゼロのファイル」が生まれうる。これは論文が
//	想定していない状況なので、**研究の裏付けがある指標(Minor/Major/Total/Ownership)
//	とは明確に分けて報告する**(honest capability: 研究が言っていないことを
//	研究の権威で語らない)。AI 判定はヒューリスティックであり、その旨を明示する。
//
// 設計:
//
//	Analyze は churn.Commit のスライスを受ける **純関数**。git の実行は
//	churn.ReadGitLog という単一 seam に集約し、二本目の git 経路を作らない
//	(v0.118.0 の srcfiles と同じ単一 seam 原則)。
//
// zero-dep(ADR-0001): stdlib + internal/churn のみ。
package ownership

import (
	"sort"
	"strings"

	"github.com/shizukutanaka/yagura/internal/churn"
)

// MinorThreshold は minor / major 寄与者を分ける割合。Bird et al. (FSE 2011) の
// 定義そのまま = 5%。変更するとその時点で論文の指標ではなくなる。
const MinorThreshold = 0.05

// FileOwnership は 1 ファイルの所有権指標。
type FileOwnership struct {
	Path      string  `json:"path"`
	Commits   int     `json:"commits"`
	Total     int     `json:"total_contributors"` // Bird: Total
	Minor     int     `json:"minor_contributors"` // Bird: Minor (< 5%)
	Major     int     `json:"major_contributors"` // Bird: Major (>= 5%)
	Ownership float64 `json:"ownership"`          // Bird: 最大寄与者の割合
	TopOwner  string  `json:"top_owner,omitempty"`

	// --- 以下は論文外の拡張(v0.120.0)。研究の裏付けは無い。---
	AIProportion   float64 `json:"ai_proportion"`             // AI エージェント由来コミットの割合
	HumanOwnership float64 `json:"human_ownership"`           // 最大 *人間* 寄与者の割合
	TopHumanOwner  string  `json:"top_human_owner,omitempty"` // 人間が 1 人も触っていなければ空
}

// Report は所有権解析の結果。Files は Ownership 昇順(= 危険な順)。
type Report struct {
	Files           []FileOwnership `json:"files"`
	Riskiest        string          `json:"riskiest,omitempty"`
	Commits         int             `json:"commits"`
	FilesAnalyzed   int             `json:"files_analyzed"`
	FullyAIAuthored int             `json:"fully_ai_authored"` // 人間が一度も触っていないファイル数(拡張指標)
	OrphanedFiles   []string        `json:"orphaned_files"`    // 同上のファイル名(先頭 20 件)
}

// aiMarkers は AI エージェントの著者判定に使うヒューリスティック。
// 完全ではない(自前の bot 名は拾えない)ため、判定は目安として扱うこと。
var aiMarkers = []string{
	"noreply@anthropic.com",
	"devin-ai-integration",
	"[bot]",
	"bot@",
	"copilot",
	"users.noreply.github.com/bot",
}

var aiNameMarkers = []string{"claude", "devin", "copilot", "dependabot", "renovate", "codex"}

// IsAIAuthor は著者名/メールから AI エージェント由来かを推定する。
// ヒューリスティックであり誤判定しうる——研究指標(Minor/Major/Total/Ownership)は
// この判定に依存しないので、外した場合でも論文由来の数値は汚染されない。
func IsAIAuthor(name, email string) bool {
	e := strings.ToLower(email)
	for _, m := range aiMarkers {
		if strings.Contains(e, m) {
			return true
		}
	}
	n := strings.ToLower(name)
	for _, m := range aiNameMarkers {
		if strings.Contains(n, m) {
			return true
		}
	}
	return false
}

// Analyze は commits からファイル単位の所有権指標を計算する。
// only が非 nil の場合、そのファイルのみを対象にする(削除済み/対象外パスの除外用)。
func Analyze(commits []churn.Commit, only map[string]bool) Report {
	rep := Report{Files: []FileOwnership{}, OrphanedFiles: []string{}}
	rep.Commits = len(commits)

	// path → author key → count
	type authorStat struct {
		name  string
		count int
		ai    bool
	}
	perFile := map[string]map[string]*authorStat{}
	for _, c := range commits {
		key := c.Email
		if key == "" {
			key = c.Author
		}
		ai := IsAIAuthor(c.Author, c.Email)
		for _, f := range c.Files {
			if only != nil && !only[f.Path] {
				continue
			}
			m, ok := perFile[f.Path]
			if !ok {
				m = map[string]*authorStat{}
				perFile[f.Path] = m
			}
			a, ok := m[key]
			if !ok {
				a = &authorStat{name: c.Author, ai: ai}
				m[key] = a
			}
			a.count++
		}
	}

	for path, authors := range perFile {
		var total int
		for _, a := range authors {
			total += a.count
		}
		if total == 0 {
			continue
		}
		fo := FileOwnership{Path: path, Commits: total, Total: len(authors)}
		var aiCommits int
		var topCount, topHumanCount int
		for _, a := range authors {
			share := float64(a.count) / float64(total)
			if share < MinorThreshold {
				fo.Minor++
			} else {
				fo.Major++
			}
			if a.count > topCount || (a.count == topCount && a.name < fo.TopOwner) {
				topCount = a.count
				fo.TopOwner = a.name
			}
			if a.ai {
				aiCommits += a.count
			} else if a.count > topHumanCount || (a.count == topHumanCount && a.name < fo.TopHumanOwner) {
				topHumanCount = a.count
				fo.TopHumanOwner = a.name
			}
		}
		fo.Ownership = float64(topCount) / float64(total)
		fo.AIProportion = float64(aiCommits) / float64(total)
		if topHumanCount > 0 {
			fo.HumanOwnership = float64(topHumanCount) / float64(total)
		} else {
			fo.TopHumanOwner = "" // 人間が一人も触っていない
			rep.FullyAIAuthored++
			if len(rep.OrphanedFiles) < 20 {
				rep.OrphanedFiles = append(rep.OrphanedFiles, path)
			}
		}
		rep.Files = append(rep.Files, fo)
	}
	rep.FilesAnalyzed = len(rep.Files)

	// Bird et al. の含意どおり「所有権が低いほど危険」= Ownership 昇順。
	// 同値は Minor 降順(minor 寄与者が多いほど危険)、さらに path 昇順で決定論的に。
	sort.SliceStable(rep.Files, func(i, j int) bool {
		if rep.Files[i].Ownership != rep.Files[j].Ownership {
			return rep.Files[i].Ownership < rep.Files[j].Ownership
		}
		if rep.Files[i].Minor != rep.Files[j].Minor {
			return rep.Files[i].Minor > rep.Files[j].Minor
		}
		return rep.Files[i].Path < rep.Files[j].Path
	})
	sort.Strings(rep.OrphanedFiles)
	if len(rep.Files) > 0 {
		rep.Riskiest = rep.Files[0].Path
	}
	return rep
}
