// Package agentparallel は「複数 AI を使った処理の並列化」を deterministic に
// 計画する rule-based planner。
//
// 着想: tensor/data parallelism が計算を複数 device(GPU)に分割して並列実行し
// barrier で結合するのと同様に、独立した task 群を複数の AI agent(Claude
// Opus/Sonnet/Haiku, Codex, Windsurf, ...)に fan-out し、各 agent の
//   - capacity(残 quota %) … device の空き容量に相当
//   - capability tier(cheap/mid/strong) … device の能力に相当
//
// に応じて割り当て、bounded concurrency(wave)で実行、最後に synthesize barrier
// で集約する — という data-parallel な分散計画を立てる。
//
// Yagura は LLM を呼ばない(ADR-0001 / rule-based)。実際に agent を spawn して
// task を走らせるのは MCP client 側で、本 package は「どの agent にどの task を、
// どの並列幅で振るか」という再現可能な plan を返すだけ(quotamonitor の live quota
// と組み合わせると、枯渇した agent に積まない quota-aware な fan-out になる)。
//
// 割り当ては最小 makespan スケジューリング(P_m||C_max, strongly NP-hard)の
// 古典 greedy "Longest Processing Time first"(LPT)を heterogeneous machine 向けに
// 一般化したもの: task を weight 降順に見て、その時点で
// (projected load + weight) / capacity が最小になる eligible agent へ置く。
// LPT は identical machine で 2-(1/m) 近似、O(n log n)。tie-break まで決定論的に
// 固定する(Yagura の deterministic output ルール)。
package agentparallel

import (
	"math"
	"sort"
)

// Tier は agent の能力段(= 使用モデルの強さ)。task 側は「最低これ以上」を要求できる。
type Tier int

const (
	// TierAny means no tier constraint (task) or undeclared tier (agent accepts any min_tier).
	TierAny Tier = iota
	// TierCheap is haiku-level: fast and inexpensive, good for exploration tasks.
	TierCheap
	// TierMid is sonnet-level: balanced capability and cost.
	TierMid
	// TierStrong is opus-level: highest capability, reserved for hard reasoning tasks.
	TierStrong
)

// Agent は fan-out 先の AI(= 並列計算の device)。
type Agent struct {
	Name           string `json:"name"`
	Tier           Tier   `json:"tier"`            // capability(eligibility 判定に使用)
	CapacityPct    int    `json:"capacity_pct"`    // 0-100 の残 quota。<=0 は除外(枯渇 device)
	MaxConcurrency int    `json:"max_concurrency"` // 同時 in-flight 上限。<=0 は 1 とみなす
}

// Task は分散する独立な仕事 1 単位。
type Task struct {
	ID      string  `json:"id"`
	Weight  float64 `json:"weight"`   // 推定コスト/難易度。<=0 は 1 とみなす
	MinTier Tier    `json:"min_tier"` // 必要最低 capability(TierAny=制約なし)
}

// Assignment は 1 agent への割り当て結果。
type Assignment struct {
	Agent      string   `json:"agent"`
	Tasks      []string `json:"tasks"`       // 割り当て順(weight 降順で配置された順)
	Waves      int      `json:"waves"`       // ceil(len(Tasks)/MaxConcurrency)
	LoadWeight float64  `json:"load_weight"` // 割り当て総 weight
}

// Plan は parallel dispatch 計画(barrier 付き fan-out)。
type Plan struct {
	Strategy    string       `json:"strategy"`             // 現状 "data"(data-parallel fan-out)
	FanOutWidth int          `json:"fan_out_width"`        // task を持つ agent 数
	Assignments []Assignment `json:"assignments"`          // agent 名昇順
	Unassigned  []string     `json:"unassigned,omitempty"` // どの eligible agent も無い task
	EstWaves    int          `json:"est_waves"`            // 推定 makespan(wave 数)
	Barrier     bool         `json:"barrier"`              // 全 fan-out 後に synthesize する(data は常に true)
	Notes       []string     `json:"notes,omitempty"`
}

// eligible は task が agent に割り当て可能か(tier 制約)。agent.Tier==TierAny は
// 「能力未宣言」とみなし任意の min_tier に適格(誤って除外しないため)。
func eligible(a Agent, t Task) bool {
	return a.Tier == TierAny || a.Tier >= t.MinTier
}

// PlanDataParallel は task 群を agent 群へ capacity 重み付き LPT で割り当てた
// data-parallel な fan-out plan を返す。入力は変更しない。完全に決定論的。
func PlanDataParallel(tasks []Task, agents []Agent, globalConcurrency int) Plan {
	p := Plan{Strategy: "data", Barrier: true}

	// capacity を持つ agent だけを名前昇順で(tie-break 安定化)。
	live := make([]Agent, 0, len(agents))
	for _, a := range agents {
		if a.CapacityPct > 0 {
			if a.MaxConcurrency <= 0 {
				a.MaxConcurrency = 1
			}
			live = append(live, a)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Name < live[j].Name })

	if len(live) == 0 {
		// 全 agent 枯渇 → 何も割り当てられない。
		for _, t := range tasks {
			p.Unassigned = append(p.Unassigned, t.ID)
		}
		if len(tasks) > 0 {
			p.Notes = append(p.Notes, "no agent has remaining capacity (>0%); nothing scheduled")
		}
		sort.Strings(p.Unassigned)
		return p
	}

	// LPT: task を (weight 降順, min_tier 降順, id 昇順) で並べる。
	// 大きい/制約の強い task を先に置くほど makespan が縮む。
	order := make([]Task, len(tasks))
	copy(order, tasks)
	for i := range order {
		if order[i].Weight <= 0 {
			order[i].Weight = 1
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].Weight != order[j].Weight {
			return order[i].Weight > order[j].Weight
		}
		if order[i].MinTier != order[j].MinTier {
			return order[i].MinTier > order[j].MinTier
		}
		return order[i].ID < order[j].ID
	})

	// load[name] = 現在の割り当て総 weight。割り当て task list も保持。
	load := make(map[string]float64, len(live))
	picked := make(map[string][]string, len(live))
	usedTier := false

	for _, t := range order {
		best := -1
		bestScore := math.Inf(1)
		for i, a := range live {
			if !eligible(a, t) {
				continue
			}
			// projected finish = (現 load + weight) / capacity。小さいほど良い。
			score := (load[a.Name] + t.Weight) / float64(a.CapacityPct)
			if score < bestScore {
				bestScore = score
				best = i
			}
			// tie は live が名前昇順なので最初に当たった(=辞書順最小)を保持。
		}
		if best < 0 {
			p.Unassigned = append(p.Unassigned, t.ID)
			continue
		}
		a := live[best]
		load[a.Name] += t.Weight
		picked[a.Name] = append(picked[a.Name], t.ID)
		if t.MinTier > TierAny {
			usedTier = true
		}
	}

	// Assignment を agent 名昇順で組み立て。
	totalAssigned := 0
	maxAgentWaves := 0
	for _, a := range live {
		ids := picked[a.Name]
		if len(ids) == 0 {
			continue
		}
		waves := int(math.Ceil(float64(len(ids)) / float64(a.MaxConcurrency)))
		if waves > maxAgentWaves {
			maxAgentWaves = waves
		}
		totalAssigned += len(ids)
		p.Assignments = append(p.Assignments, Assignment{
			Agent:      a.Name,
			Tasks:      ids,
			Waves:      waves,
			LoadWeight: load[a.Name],
		})
	}
	p.FanOutWidth = len(p.Assignments)

	// makespan(wave)推定: agent 内 wave の最大。global concurrency が fan-out 幅
	// より小さいと同時稼働 agent が絞られるので、その制約も加味する。
	p.EstWaves = maxAgentWaves
	if globalConcurrency > 0 {
		slots := 0
		for _, a := range live {
			if len(picked[a.Name]) > 0 {
				slots += a.MaxConcurrency
			}
		}
		if globalConcurrency < slots && totalAssigned > 0 {
			gw := int(math.Ceil(float64(totalAssigned) / float64(globalConcurrency)))
			if gw > p.EstWaves {
				p.EstWaves = gw
			}
			p.Notes = append(p.Notes,
				"global_concurrency caps simultaneous tasks below the fan-out's natural width; makespan is bounded by the global cap")
		}
	}

	if usedTier {
		p.Notes = append(p.Notes,
			"tier constraints applied: tasks were routed only to agents meeting their min_tier (classify-and-act / per-agent model)")
	}
	sort.Strings(p.Unassigned)
	if len(p.Unassigned) > 0 {
		p.Notes = append(p.Notes,
			"some tasks had no agent meeting their min_tier; raise an agent's tier or lower the task's min_tier")
	}
	return p
}
