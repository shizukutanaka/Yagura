---
name: yagura
description: Use when the user asks about portfolio status, project registry, dependency impact, agent quota / handoff, security scans (SBOM / vulns / GHA / pin-drift / secrets), or anything that should query the local yagura daemon. Triggers on "my projects", "what's in registry", "impact of changing X", "quota", "handoff", "yagura".
---

# yagura — Portfolio Orchestrator Skill

yagura is a long-running daemon that manages m's 23+ project portfolio. It exposes 104 MCP tools. Call them via the local MCP endpoint instead of guessing portfolio state from memory.

## Mental model

yagura is a Guides + Sensors machine (per Fowler 2026):

- **[G] Guides** = pre-control. Provide context, prevent drift. (registry list/get/search, graph_neighbors/impact, sbom, secretscan, gha_audit, pin_drift, harness_recommend, skill_audit, subagent_audit)
- **[S] Sensors** = post-control. Observe state, react to deviation. (quota_report, agent_status, handoff, heartbeat, vulns, scorecard, health, usage_summary, token_stats)

Every tool description starts with `[G]` or `[S]`. Use this to pick the right tool quickly.

## Common workflows

### Before changing project X
1. `yagura_graph_impact(slug: X)` → see who depends on X
2. `yagura_get(slug: X)` → check stage, latest activity, notes
3. If X has many dependents (max_fan_in ≥ 3), proceed carefully

### Starting a new Claude Code session on project X
1. `yagura_harness_recommend(slug: X)` → get CLAUDE.md + settings.json + skill skeletons
2. Save outputs under `.claude/` in X's repo
3. `yagura_register` if not already registered

### Auditing an existing skill before commit
1. Read `.claude/skills/foo/SKILL.md`
2. `yagura_skill_audit(content: <full text>)` → score + issues
3. If score < 80, apply suggestions before commit

### Quota-aware handoff
1. `yagura_agent_status({})` → see Claude Code / Windsurf quota
2. If recommended != current, `yagura_session_save({...})` then `yagura_handoff({to: <recommended>})`

## Gotchas

These are the failure modes I have actually hit. Anthropic's general knowledge does not predict them.

- **Tool name typos return -32601, not similar-tool suggestions.** Check `tools/list` first if you're unsure.
- **`yagura_quota_report` with `remaining_percent: 0` flips state to EXHAUSTED.** To clear, you need a fresh report with non-zero, OR a `yagura_handoff` cycle — not via another `quota_report`.
- **Auth header is `Authorization: Bearer <token>`. Forgetting the `Bearer` prefix returns 401 with no hint.** Token comes from `YAGURA_MCP_TOKEN` env var on daemon side.
- **`yagura_register` requires `slug` AND `repository`.** Other fields optional. Missing `repository` returns confusing 'invalid_input' without naming the field.
- **`depends_on` slugs that don't exist in registry appear in `yagura_graph_stats.dangling`** — not an error, but a drift signal. Clean them when refactoring.
- **`yagura_usage_summary` returns `reliable: false` when window < 5 minutes (v0.17).** `avg_consume_per_hour` and `slope_percent_per_sec` are zeroed in that case. Don't divide by them in client logic — check `reliable` first.
- **`yagura_token_stats` measures BYTES, not LLM tokens.** Multiply by ~0.25 for a rough token estimate.
- **`yagura_session_load` is destructive** in the sense that it marks the source session consumed. Don't call it for a peek-only read.
- **`yagura_handoff` does not migrate `usage_history.jsonl`.** Persistence is per-state-dir; if you change `YAGURA_STATE_DIR` between sessions, history is lost.
- **Skill description ≤1024 chars and name ≤64 chars are HARD limits.** `yagura_skill_audit` will report violations; the Claude Code loader silently truncates beyond that.

## References

- yagura repo's `CHANGELOG.md` — version history, especially v0.13-v0.19 covering the harness work
- yagura repo's `docs/integration/` — fuller integration guides
- Anthropic Claude Code docs: https://code.claude.com/docs/en
