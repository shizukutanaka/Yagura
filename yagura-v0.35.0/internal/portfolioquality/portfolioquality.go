// Package portfolioquality ranks every registered project by code health.
//
// なぜ必要か(v0.118.0、First Principles 由来):
//
//	Yagura の存在理由は「N プロジェクトを抱えた個人開発者の *注意* をどこへ向けるか」を
//	決めること。しかし注意配分を出す層(alert_fix / today / release_radar)が使う signal は
//	vulns / plan / ci / stale / scorecard / open_issues / visibility の 7 つ——すべて
//	GitHub/OSV 由来の *外部センサー* で、本リポジトリ最大の投資である ~24 個の go/ast
//	quality lens は 1 つも入っていなかった(internal/alertfix を grep すると 0 件)。
//	つまり「どのプロジェクトのコードが一番傷んでいるか」をポートフォリオ横断で問えない。
//	本パッケージはその C5 ギャップ(C4 品質計測 → C3 注意ランキング)を埋める。
//
//	同時に token 経済の矛盾も解消する: 既存の quality tool 群は client が
//	{path: content} を LLM context 越しに送る content-based 契約だったが、daemon は
//	registry で LocalPath を既に知っている。ここでは daemon 側がディスクを読むので
//	ファイル内容は 1 バイトも context を通らない。
//
// 判定は codehealth(complexity/apidoc/deadcode/recvcheck/astcheck/assertcheck の合成)に
// 委譲し、本パッケージは走査・集約・順序付けだけを行う(単一情報源)。
//
// zero-dep(ADR-0001): stdlib + 既存 internal のみ。
package portfolioquality

import (
	"sort"

	"github.com/shizukutanaka/yagura/internal/codehealth"
	"github.com/shizukutanaka/yagura/internal/srcfiles"
)

// Project は走査対象の最小ビュー(registry.Project の写し)。
// registry を import しないのは、この純関数をテストから直接叩けるようにするため。
type Project struct {
	Slug      string
	LocalPath string
}

// ReadFunc はディレクトリ → ソース集合の読み取り関数。既定は srcfiles.ReadGo。
// 注入可能にしてあるのでテストは実ファイルシステムを必要としない。
type ReadFunc func(dir string) (srcfiles.Result, error)

// ProjectQuality は 1 プロジェクトの評点。
type ProjectQuality struct {
	Slug       string `json:"slug"`
	Grade      string `json:"grade"`
	Score      int    `json:"score"`
	Packages   int    `json:"packages"`
	FilesRead  int    `json:"files_read"`
	Incomplete bool   `json:"incomplete"` // cap 到達 or 読取失敗を含む(部分スキャン)
}

// Unscannable は評価できなかったプロジェクトとその理由。
// 黙って落とさない——「対象 0 件で全部きれい」という fail-open を防ぐ。
type Unscannable struct {
	Slug   string `json:"slug"`
	Reason string `json:"reason"`
}

// Report はポートフォリオ全体の品質ランキング。
type Report struct {
	Projects    []ProjectQuality `json:"projects"`    // worst-first
	Unscannable []Unscannable    `json:"unscannable"` // 評価不能(理由つき)
	Scanned     int              `json:"scanned"`
	Worst       string           `json:"worst,omitempty"` // 最も注意が必要な slug
}

// Rank は各プロジェクトの LocalPath を走査して codehealth を測り、
// **worst-first**(score 昇順、同点は slug 昇順)で並べた Report を返す。
//
// read が nil なら srcfiles.ReadGo を使う。LocalPath が空、または読み取りに失敗した
// プロジェクトは Unscannable に理由つきで載せる。
func Rank(projects []Project, read ReadFunc) Report {
	if read == nil {
		read = srcfiles.ReadGo
	}
	rep := Report{Projects: []ProjectQuality{}, Unscannable: []Unscannable{}}
	for _, p := range projects {
		if p.LocalPath == "" {
			rep.Unscannable = append(rep.Unscannable, Unscannable{
				Slug:   p.Slug,
				Reason: "no local_path registered (set it with yagura_update to include this project)",
			})
			continue
		}
		res, err := read(p.LocalPath)
		if err != nil {
			rep.Unscannable = append(rep.Unscannable, Unscannable{
				Slug:   p.Slug,
				Reason: "cannot read local_path: " + err.Error(),
			})
			continue
		}
		if len(res.Files) == 0 {
			rep.Unscannable = append(rep.Unscannable, Unscannable{
				Slug:   p.Slug,
				Reason: "no Go source files found under local_path",
			})
			continue
		}
		ch := codehealth.Analyze(res.Files)
		rep.Projects = append(rep.Projects, ProjectQuality{
			Slug:       p.Slug,
			Grade:      ch.OverallGrade,
			Score:      ch.OverallScore,
			Packages:   len(ch.Packages),
			FilesRead:  len(res.Files),
			Incomplete: res.Incomplete(),
		})
	}
	// worst-first: score 昇順。同点は slug 昇順(Deterministic output ルール)。
	sort.SliceStable(rep.Projects, func(i, j int) bool {
		if rep.Projects[i].Score != rep.Projects[j].Score {
			return rep.Projects[i].Score < rep.Projects[j].Score
		}
		return rep.Projects[i].Slug < rep.Projects[j].Slug
	})
	sort.SliceStable(rep.Unscannable, func(i, j int) bool {
		return rep.Unscannable[i].Slug < rep.Unscannable[j].Slug
	})
	rep.Scanned = len(rep.Projects)
	if len(rep.Projects) > 0 {
		rep.Worst = rep.Projects[0].Slug
	}
	return rep
}
