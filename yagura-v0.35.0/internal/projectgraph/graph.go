// Package projectgraph は portfolio の depends_on 関係を graph として扱う。
//
// 動機 (v0.18.0):
//
//	cortex/aircloset の "Product Graph" 構想を yagura に翻訳。
//	project.DependsOn は既に v0.x から存在していたが、検索可能な graph として
//	公開する MCP tool が無かった。
//	"プロジェクト X を変更したら何が影響を受けるか?" "X が依存している全 transitive deps は?"
//	といった問いに 1 回の MCP 呼出で答えられる土台を提供する。
//
// 設計判断:
//   - ゼロ依存(ADR-0001)、隣接リスト map[slug][]slug の素朴実装
//   - DAG 前提だが cycle 検出はする(誤登録への防御)
//   - graph は immutable(registry snapshot から build)
//   - 探索は BFS で深さ制限可
//   - missing slug (depends_on に書かれているが registry に存在しない) は警告として残す
//     → これも graph drift 検出として価値ある signal
package projectgraph

import (
	"fmt"
	"sort"
)

// Graph は portfolio の依存関係を表す。
//
// forwards[slug] = この slug が直接依存する slug 群(順方向、depends_on そのまま)
// reverses[slug] = この slug に直接依存する slug 群(逆方向、impact 分析用)
type Graph struct {
	forwards map[string][]string
	reverses map[string][]string
	slugs    []string // 全 slug の sorted list(deterministic 出力のため)
	dangling []DanglingDep
}

// DanglingDep は depends_on に記載されているが registry に存在しない slug 参照。
//
// graph drift の signal として収集。例: 古いプロジェクトを unregister したが
// 依存していたプロジェクトの depends_on をクリーンアップ忘れ。
type DanglingDep struct {
	From string `json:"from"` // 依存元(存在する slug)
	To   string `json:"to"`   // 依存先(存在しない slug)
}

// Project は graph 入力に必要な最小 view。
// 呼出側は registry.Project から fold する。
type Project struct {
	Slug      string
	DependsOn []string
}

// Build は projects から graph を構築する。
//
// 重複 slug は最後のものが勝つ(これは registry 側で防ぐべき問題なので、
// graph 側は防御的処理のみ)。
func Build(projects []Project) *Graph {
	g := &Graph{
		forwards: map[string][]string{},
		reverses: map[string][]string{},
	}
	exists := map[string]bool{}
	for _, p := range projects {
		exists[p.Slug] = true
	}
	for _, p := range projects {
		for _, dep := range p.DependsOn {
			if !exists[dep] {
				g.dangling = append(g.dangling, DanglingDep{From: p.Slug, To: dep})
				continue
			}
			g.forwards[p.Slug] = append(g.forwards[p.Slug], dep)
			g.reverses[dep] = append(g.reverses[dep], p.Slug)
		}
	}
	// 各 list を sort して deterministic output
	for k := range g.forwards {
		sort.Strings(g.forwards[k])
	}
	for k := range g.reverses {
		sort.Strings(g.reverses[k])
	}
	// 全 slug list
	for s := range exists {
		g.slugs = append(g.slugs, s)
	}
	sort.Strings(g.slugs)
	// dangling も (From, To) で sort して deterministic output(他の list と同様、
	// 呼出側の project 順序に依存させない)。
	sort.Slice(g.dangling, func(i, j int) bool {
		if g.dangling[i].From != g.dangling[j].From {
			return g.dangling[i].From < g.dangling[j].From
		}
		return g.dangling[i].To < g.dangling[j].To
	})
	return g
}

// NeighborsResult は graph_neighbors の戻り値。
type NeighborsResult struct {
	Slug                 string   `json:"slug"`
	DirectDeps           []string `json:"direct_deps,omitempty"`           // 直接依存先
	DirectDependents     []string `json:"direct_dependents,omitempty"`     // 直接依存元
	TransitiveDeps       []string `json:"transitive_deps,omitempty"`       // depth N までの全依存先
	TransitiveDependents []string `json:"transitive_dependents,omitempty"` // depth N までの全依存元
	Depth                int      `json:"depth"`
}

// Neighbors は slug から depth 階層まで graph を BFS で探索する。
//
// 戻り値:
//   - DirectDeps / DirectDependents: 距離 1
//   - TransitiveDeps / TransitiveDependents: 距離 2..depth(direct を除く)
//
// slug が存在しない場合は空 result を返す。
func (g *Graph) Neighbors(slug string, depth int) NeighborsResult {
	r := NeighborsResult{Slug: slug, Depth: depth}
	if depth < 1 {
		depth = 1
	}
	r.DirectDeps = append([]string(nil), g.forwards[slug]...)
	r.DirectDependents = append([]string(nil), g.reverses[slug]...)

	if depth >= 2 {
		r.TransitiveDeps = bfsBeyondFirst(g.forwards, slug, depth)
		r.TransitiveDependents = bfsBeyondFirst(g.reverses, slug, depth)
	}
	return r
}

// bfsBeyondFirst は BFS で distance 2..maxDepth の slug を返す。
//
// 距離 1(direct)は除く。重複は dedup。slug 自身も除く。
func bfsBeyondFirst(adj map[string][]string, start string, maxDepth int) []string {
	visited := map[string]bool{start: true}
	for _, n := range adj[start] {
		visited[n] = true // 距離 1 は最初に mark
	}
	// 距離 1 をキューに(これらの隣接 = 距離 2)
	frontier := adj[start]
	beyond := map[string]bool{}
	for d := 2; d <= maxDepth && len(frontier) > 0; d++ {
		next := []string{}
		for _, n := range frontier {
			for _, m := range adj[n] {
				if visited[m] {
					continue
				}
				visited[m] = true
				beyond[m] = true
				next = append(next, m)
			}
		}
		frontier = next
	}
	out := make([]string, 0, len(beyond))
	for s := range beyond {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ImpactResult は graph_impact の戻り値。
//
// 「このプロジェクトを変更したら、どこに波及するか」を提供する。
// AI レビュアー(cortex Auto Review 相当)が PR の影響範囲を把握するために使う。
type ImpactResult struct {
	Slug             string   `json:"slug"`
	DirectImpact     []string `json:"direct_impact,omitempty"`     // 直接依存元
	TransitiveImpact []string `json:"transitive_impact,omitempty"` // 全 transitive 依存元
	ImpactCount      int      `json:"impact_count"`
	HasCycle         bool     `json:"has_cycle"`
	CyclePath        []string `json:"cycle_path,omitempty"`
}

// Impact は slug を変更した時に影響を受けるプロジェクトを列挙する。
//
// 戻り値:
//   - DirectImpact: 直接依存元
//   - TransitiveImpact: 全 transitive 依存元(direct 含む)
//   - HasCycle: depth 探索中に slug 自身に戻ったか
//   - CyclePath: cycle を構成する path(あれば)
func (g *Graph) Impact(slug string) ImpactResult {
	r := ImpactResult{Slug: slug}
	// BFS で全 reverses transitive 探索。
	// parent で各ノードへの最短経路(BFS 順)を保持し、cycle 検出時に
	// slug→…→cur の実経路を復元する。以前は単一の path スライスに全訪問
	// ノードを蓄積していたため、CyclePath に無関係な兄弟ブランチが混入していた。
	visited := map[string]bool{}
	parent := map[string]string{slug: ""}
	queue := []string{slug}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, n := range g.reverses[cur] {
			if n == slug {
				// cycle 検出。最初に見つかった(=最短)経路のみ採用する。
				r.HasCycle = true
				if r.CyclePath == nil {
					r.CyclePath = cyclePathTo(parent, slug, cur)
				}
				continue
			}
			if visited[n] {
				continue
			}
			visited[n] = true
			parent[n] = cur
			queue = append(queue, n)
		}
	}
	r.DirectImpact = append([]string(nil), g.reverses[slug]...)
	all := make([]string, 0, len(visited))
	for s := range visited {
		all = append(all, s)
	}
	sort.Strings(all)
	r.TransitiveImpact = all
	r.ImpactCount = len(all)
	return r
}

// cyclePathTo は parent map を逆走して slug→…→cur の経路を組み立て、
// 末尾に slug を付けて循環として閉じた path を返す。
func cyclePathTo(parent map[string]string, slug, cur string) []string {
	chain := []string{cur}
	for x := cur; x != slug; {
		p := parent[x]
		if p == "" {
			break
		}
		chain = append([]string{p}, chain...)
		x = p
	}
	return append(chain, slug)
}

// Dangling は graph drift(depends_on で参照されているが存在しない slug)を返す。
func (g *Graph) Dangling() []DanglingDep {
	// 空でも `[]` を返す(v1.3.3)。null は「参照切れ無し」と「未検査」を
	// 区別できない。
	if g.dangling == nil {
		return []DanglingDep{}
	}
	return append([]DanglingDep(nil), g.dangling...)
}

// Summary は portfolio 全体の graph 統計を返す。
type Summary struct {
	NodeCount      int    `json:"node_count"`
	EdgeCount      int    `json:"edge_count"`
	RootCount      int    `json:"root_count"`     // 誰にも依存されていない node
	LeafCount      int    `json:"leaf_count"`     // 何にも依存しない node
	IsolatedCount  int    `json:"isolated_count"` // 入次数=出次数=0
	MaxFanOut      int    `json:"max_fan_out"`    // 最大依存先数
	MaxFanIn       int    `json:"max_fan_in"`     // 最大依存元数
	MostDependedOn string `json:"most_depended_on,omitempty"`
	DanglingCount  int    `json:"dangling_count"`
}

// Stats は graph の集計統計を返す。
func (g *Graph) Stats() Summary {
	s := Summary{
		NodeCount:     len(g.slugs),
		DanglingCount: len(g.dangling),
	}
	for _, slug := range g.slugs {
		outDeg := len(g.forwards[slug])
		inDeg := len(g.reverses[slug])
		s.EdgeCount += outDeg
		if inDeg == 0 {
			s.RootCount++
		}
		if outDeg == 0 {
			s.LeafCount++
		}
		if inDeg == 0 && outDeg == 0 {
			s.IsolatedCount++
		}
		if outDeg > s.MaxFanOut {
			s.MaxFanOut = outDeg
		}
		if inDeg > s.MaxFanIn {
			s.MaxFanIn = inDeg
			s.MostDependedOn = slug
		}
	}
	return s
}

// String は debug 用の compact 表現。
func (g *Graph) String() string {
	return fmt.Sprintf("Graph{nodes=%d, edges=%d, dangling=%d}",
		len(g.slugs), countEdges(g.forwards), len(g.dangling))
}

func countEdges(m map[string][]string) int {
	n := 0
	for _, v := range m {
		n += len(v)
	}
	return n
}
