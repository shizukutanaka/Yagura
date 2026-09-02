// Package featurelist converts a Plan.md into the feature-list.json structure
// that Anthropic's long-running harness pattern uses.
//
// Motivation (v0.32.0):
//
//	Anthropic's "Effective harnesses for long-running agents" (2026) describes
//	a 2-agent pattern where an Initializer agent produces a feature-list.json
//	that the recurring Coding agent works through one feature at a time. The
//	key insight: agents reliably claim premature completion unless the work
//	is enumerated up front with explicit acceptance criteria.
//
//	m's harness G1.P already requires every project to have a Plan.md with
//	purpose/scope/phases/DoD. yagura already parses these (plantracker pkg).
//	What was missing: a converter from this human-authored format to the
//	machine-actionable feature-list.json the Anthropic pattern needs.
//
// Design (ADR-0001 zero-dep):
//   - Pure converter: plantracker.PlanState → []Feature → JSON
//   - Each unchecked Plan.md task becomes one Feature with status="pending"
//   - Each checked task becomes status="done" (preserves portfolio history)
//   - DoD items are attached to every feature as acceptance_criteria
//   - Phases become a top-level grouping, preserving Plan.md ordering
//
// Reference:
//
//	https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents
//	https://github.com/anthropics/cwc-long-running-agents
package featurelist

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Feature represents one unit of work the long-running agent will pick up.
//
// Schema is intentionally compatible with Anthropic's reference example —
// status field uses the same three values, acceptance_criteria is a slice of
// strings, and "id" is a stable kebab-case slug derived from the title.
type Feature struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Phase              string   `json:"phase,omitempty"`
	Status             string   `json:"status"` // pending / in_progress / done
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	Notes              string   `json:"notes,omitempty"`
}

// FeatureList is the top-level document.
type FeatureList struct {
	Project     string    `json:"project"`
	GeneratedAt time.Time `json:"generated_at"`
	Source      string    `json:"source"` // typically "Plan.md"
	Features    []Feature `json:"features"`
	Stats       Stats     `json:"stats"`
}

// Stats lets the agent see at a glance whether it should keep going.
type Stats struct {
	Total      int `json:"total"`
	Pending    int `json:"pending"`
	InProgress int `json:"in_progress"`
	Done       int `json:"done"`
}

// PlanInput is the minimum subset of a parsed Plan.md needed to build the
// feature list. Caller supplies this from plantracker.PlanState — we don't
// import plantracker directly so this package stays cycle-free.
type PlanInput struct {
	Project string
	Phases  []PhaseInput
	DoD     []string
}

// PhaseInput is one ## フェーズ block.
type PhaseInput struct {
	Name  string
	Tasks []TaskInput
}

// TaskInput is one bullet (checked or unchecked).
type TaskInput struct {
	Title string
	Done  bool
}

// Build produces a FeatureList from a parsed plan.
//
// Behavior:
//   - One Feature per task
//   - DoD items attach to every feature as acceptance criteria
//   - Feature IDs are deterministic kebab-case slugs of the title
//   - Status mapping: Done -> "done", else "pending"
//   - GeneratedAt is now-injectable via the optional NowFn parameter
func Build(in PlanInput, now func() time.Time) FeatureList {
	if now == nil {
		now = time.Now
	}
	out := FeatureList{
		Project:     in.Project,
		GeneratedAt: now().UTC(),
		Source:      "Plan.md",
	}
	usedIDs := map[string]int{}
	for _, ph := range in.Phases {
		for _, t := range ph.Tasks {
			f := Feature{
				Title:              strings.TrimSpace(t.Title),
				Phase:              strings.TrimSpace(ph.Name),
				Status:             "pending",
				AcceptanceCriteria: append([]string{}, in.DoD...),
			}
			if t.Done {
				f.Status = "done"
			}
			f.ID = uniqueSlug(f.Title, usedIDs)
			out.Features = append(out.Features, f)
		}
	}
	out.Stats = computeStats(out.Features)
	return out
}

func computeStats(fs []Feature) Stats {
	var s Stats
	for _, f := range fs {
		s.Total++
		switch f.Status {
		case "pending":
			s.Pending++
		case "in_progress":
			s.InProgress++
		case "done":
			s.Done++
		}
	}
	return s
}

// slug converts an arbitrary title to a stable kebab-case identifier.
//
// Rules:
//   - lowercase ASCII letters and digits are kept
//   - everything else becomes a single hyphen
//   - leading/trailing hyphens trimmed
//   - empty result falls back to "task"
//   - max length 50
func slug(s string) string {
	s = strings.ToLower(s)
	var sb strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			sb.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && sb.Len() > 0 {
				sb.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := strings.Trim(sb.String(), "-")
	if out == "" {
		return "task"
	}
	if len(out) > 50 {
		out = strings.TrimRight(out[:50], "-")
	}
	return out
}

// uniqueSlug ensures stable IDs across duplicate titles by appending a counter.
//
// First occurrence: "do-the-thing"
// Second occurrence: "do-the-thing-2"
// Third occurrence: "do-the-thing-3"
func uniqueSlug(title string, used map[string]int) string {
	base := slug(title)
	used[base]++
	n := used[base]
	if n == 1 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, n)
}

// Marshal renders the FeatureList as indented JSON ready to write to disk.
func Marshal(fl FeatureList) ([]byte, error) {
	return json.MarshalIndent(fl, "", "  ")
}
