// Package today は portfolio の「今日どれに注力すべきか」ランキングを提供する。
//
// 元々 MCP tool yagura_today の handler 内にあったスコアリングを純関数として
// 抽出し、CLI(`yagura today`)と MCP の両方が同一ロジックを共有できるようにした
// (v0.35 の CLI direct mode で他ツールに行った CLI parity を today にも適用)。
//
// スコアリング(決定論的):
//   - 対象は active / maintenance のプロジェクトのみ
//   - priority×10(priority>=4 で "high priority")/ openPRs×3 /
//     CI failing +20 / active かつ 14 日以上 idle +5
//   - score 降順、同点は slug 昇順、limit 件に切り詰め(limit<=0 で全件)
//
// stdlib のみ(ADR-0001)。
package today

import (
	"sort"
	"time"

	"github.com/shizukutanaka/yagura/internal/project"
)

// Item は today ランキングの 1 件。JSON タグは yagura_today MCP tool の
// 従来出力と一致させてある(mcp 側は type alias で再利用する)。
type Item struct {
	Slug        string   `json:"slug"`
	DisplayName string   `json:"display_name"`
	Score       float64  `json:"score"`
	Reasons     []string `json:"reasons"`
	Priority    int      `json:"priority"`
	OpenPRs     int      `json:"open_prs"`
	CIStatus    string   `json:"ci_status,omitempty"`
	DaysIdle    int      `json:"days_idle"`
}

// Rank は projects を active/maintenance に絞ってスコアリングし、score 降順
// (同点 slug 昇順)で並べて limit 件返す。limit<=0 なら全件。決定論的。
func Rank(projects []*project.Project, now time.Time, limit int) []Item {
	items := make([]Item, 0, len(projects))
	for _, p := range projects {
		if p.Stage != project.StageActive && p.Stage != project.StageMaintenance {
			continue
		}
		score := 0.0
		reasons := []string{}

		if p.Priority > 0 {
			score += float64(p.Priority * 10)
			if p.Priority >= 4 {
				reasons = append(reasons, "high priority")
			}
		}
		if p.OpenPRs > 0 {
			score += float64(p.OpenPRs * 3)
			reasons = append(reasons, "open PRs")
		}
		if p.CIStatus == project.CIStatusFailing {
			score += 20
			reasons = append(reasons, "CI failing")
		}
		days := project.DaysSince(p.LatestActivity, now)
		if days >= 14 && p.Stage == project.StageActive {
			score += 5
			reasons = append(reasons, "stale (active but idle)")
		}

		items = append(items, Item{
			Slug:        p.Slug,
			DisplayName: p.DisplayName,
			Score:       score,
			Reasons:     reasons,
			Priority:    p.Priority,
			OpenPRs:     p.OpenPRs,
			CIStatus:    string(p.CIStatus),
			DaysIdle:    days,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].Slug < items[j].Slug
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}
