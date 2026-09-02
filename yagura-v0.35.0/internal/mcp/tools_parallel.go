// tools_parallel.go: yagura_parallel_plan — 複数 AI を使った処理の並列化を計画する。
//
// 独立した task 群を複数の AI agent(Claude Opus/Sonnet/Haiku, Codex, Windsurf, ...)
// に capacity(残 quota)と capability(tier)で重み付けして fan-out する deterministic
// な dispatch plan を返す(internal/agentparallel)。Yagura は LLM を呼ばないので、
// 実際の spawn/実行は MCP client が行い、本 tool は「どの agent にどの task を、どの
// 並列幅で振るか」の再現可能な計画だけを返す。
//
// quotamonitor が設定済みで agent 名が既知(claude_code / windsurf)かつ
// capacity_percent 省略時は、live quota を capacity として埋める(枯渇 agent に積まない
// quota-aware な fan-out)。

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shizukutanaka/yagura/internal/agentparallel"
	"github.com/shizukutanaka/yagura/internal/quotamonitor"
)

// parseTier は tier 文字列(別名込み)を agentparallel.Tier に変換する。
// "" / "any" は TierAny。haiku/sonnet/opus を cheap/mid/strong の別名として受ける。
func parseTier(s string) (agentparallel.Tier, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "any":
		return agentparallel.TierAny, true
	case "cheap", "haiku":
		return agentparallel.TierCheap, true
	case "mid", "sonnet", "medium":
		return agentparallel.TierMid, true
	case "strong", "opus", "high":
		return agentparallel.TierStrong, true
	}
	return agentparallel.TierAny, false
}

// maxSynthTasks は task_count 省略記法の上限(暴発防止)。
const maxSynthTasks = 10000

// parallelTaskInput / parallelAgentInput は yagura_parallel_plan の decode 後の
// 入力要素(handler の anonymous struct と同形)。helper へ渡すための named 型。
type parallelTaskInput struct {
	ID      string  `json:"id"`
	Weight  float64 `json:"weight"`
	MinTier string  `json:"min_tier"`
}

type parallelAgentInput struct {
	Name            string `json:"name"`
	Tier            string `json:"tier"`
	CapacityPercent *int   `json:"capacity_percent"`
	MaxConcurrency  int    `json:"max_concurrency"`
}

// buildParallelTasks は明示 tasks list か task_count 省略記法から Task 群を組む。
func buildParallelTasks(in []parallelTaskInput, taskCount int) ([]agentparallel.Task, *ToolError) {
	if len(in) > 0 {
		tasks := make([]agentparallel.Task, 0, len(in))
		for i, t := range in {
			id := strings.TrimSpace(t.ID)
			if id == "" {
				id = fmt.Sprintf("task-%d", i+1)
			}
			tier, ok := parseTier(t.MinTier)
			if !ok {
				return nil, &ToolError{Code: "invalid_input",
					Message: fmt.Sprintf("task %q: unknown min_tier %q (use any/cheap/mid/strong)", id, t.MinTier)}
			}
			tasks = append(tasks, agentparallel.Task{ID: id, Weight: t.Weight, MinTier: tier})
		}
		return tasks, nil
	}
	if taskCount <= 0 {
		return nil, &ToolError{Code: "invalid_input", Message: "provide 'tasks' or a positive 'task_count'"}
	}
	if taskCount > maxSynthTasks {
		return nil, &ToolError{Code: "invalid_input",
			Message: fmt.Sprintf("task_count %d exceeds limit %d", taskCount, maxSynthTasks)}
	}
	tasks := make([]agentparallel.Task, 0, taskCount)
	for i := 0; i < taskCount; i++ {
		tasks = append(tasks, agentparallel.Task{ID: fmt.Sprintf("task-%d", i+1), Weight: 1})
	}
	return tasks, nil
}

// buildParallelAgents は agent 群を組む。capacity 省略時は live quota で埋める
// (既知 agent のみ)。2 つ目の戻り値は quota 由来で埋めたかどうか。
func buildParallelAgents(d Deps, in []parallelAgentInput) ([]agentparallel.Agent, bool, *ToolError) {
	filledFromQuota := false
	agents := make([]agentparallel.Agent, 0, len(in))
	for _, a := range in {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			return nil, false, &ToolError{Code: "invalid_input", Message: "agent name must not be empty"}
		}
		tier, ok := parseTier(a.Tier)
		if !ok {
			return nil, false, &ToolError{Code: "invalid_input",
				Message: fmt.Sprintf("agent %q: unknown tier %q (use any/cheap/mid/strong)", name, a.Tier)}
		}
		capPct := agentCapacity(d, name, a.CapacityPercent, &filledFromQuota)
		agents = append(agents, agentparallel.Agent{
			Name:           name,
			Tier:           tier,
			CapacityPct:    capPct,
			MaxConcurrency: a.MaxConcurrency,
		})
	}
	return agents, filledFromQuota, nil
}

// agentCapacity は capacity_percent 明示値(あれば)を優先し、無ければ既知 agent の
// live quota を引いて埋める。quota で埋めた場合 *filled を true にする。
func agentCapacity(d Deps, name string, explicit *int, filled *bool) int {
	if explicit != nil {
		return *explicit
	}
	if d.QuotaMonitor != nil {
		if qa, err := quotamonitor.AgentFromString(name); err == nil {
			if st, err := d.QuotaMonitor.Status(qa); err == nil {
				*filled = true
				return st.RemainingPercent
			}
		}
	}
	return 100
}

func buildParallelPlanTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_parallel_plan",
		Title:       "Plan Parallel Task Fan-out",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Plan parallel task fan-out across AI agents.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type":        "array",
					"description": "Independent work items. Each: {id, weight?, min_tier?}.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":       map[string]any{"type": "string"},
							"weight":   map[string]any{"type": "number"},
							"min_tier": map[string]any{"type": "string"},
						},
					},
				},
				"task_count": map[string]any{
					"type":        "integer",
					"description": "Shorthand: N uniform unit-weight tasks (task-1..task-N) when 'tasks' omitted.",
				},
				"agents": map[string]any{
					"type":        "array",
					"description": "AI agents to fan out to. Each: {name, tier, capacity_percent?, max_concurrency?}.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name":             map[string]any{"type": "string"},
							"tier":             map[string]any{"type": "string"},
							"capacity_percent": map[string]any{"type": "integer"},
							"max_concurrency":  map[string]any{"type": "integer"},
						},
						"required": []string{"name"},
					},
				},
				"global_concurrency": map[string]any{"type": "integer"},
				"strategy":           map[string]any{"type": "string"},
			},
			"required": []string{"agents"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Tasks             []parallelTaskInput  `json:"tasks"`
				TaskCount         int                  `json:"task_count"`
				Agents            []parallelAgentInput `json:"agents"`
				GlobalConcurrency int                  `json:"global_concurrency"`
				Strategy          string               `json:"strategy"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}

			if s := strings.ToLower(strings.TrimSpace(in.Strategy)); s != "" && s != "data" {
				return nil, &ToolError{Code: "invalid_input",
					Message: fmt.Sprintf("unsupported strategy %q (only 'data' is supported)", in.Strategy)}
			}
			if len(in.Agents) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "at least one agent is required"}
			}

			tasks, terr := buildParallelTasks(in.Tasks, in.TaskCount)
			if terr != nil {
				return nil, terr
			}

			agents, filledFromQuota, terr := buildParallelAgents(d, in.Agents)
			if terr != nil {
				return nil, terr
			}

			plan := agentparallel.PlanDataParallel(tasks, agents, in.GlobalConcurrency)
			if filledFromQuota {
				plan.Notes = append(plan.Notes,
					"some agent capacities were filled from live quotamonitor quota (omitted capacity_percent)")
			}
			return plan, nil
		},
	}
}
