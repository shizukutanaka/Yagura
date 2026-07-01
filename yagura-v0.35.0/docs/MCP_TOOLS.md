# MCP tools reference

Generated from a live yagura — **93 tools**.

Tools are tagged `[G]` (guide / feedforward) or `[S]` (sensor / feedback) per the [Fowler harness taxonomy](https://martinfowler.com/articles/harness-engineering.html).

---

## Table of contents

- [Alerts](#alerts) (2)
- [Code quality (guides)](#code-quality-guides) (9)
- [Graph](#graph) (3)
- [Handoff](#handoff) (1)
- [Harness (guides)](#harness-guides) (8)
- [Inventory](#inventory) (8)
- [Misc](#misc) (46)
- [Observability](#observability) (4)
- [Plan tracking](#plan-tracking) (2)
- [Security (sensors)](#security-sensors) (10)

---

## Alerts

### `yagura_alert_fix`

[S] Aggregate health signals across portfolio. Returns actionable alerts with suggested_tool + args.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `open_issues_high` (integer) |  |  |
| `scorecard_min` (number) |  |  |
| `severity_min` (string) |  |  |
| `slug` (string) |  |  |
| `stale_days` (integer) |  |  |

---

### `yagura_alert_resolve`

[G] Manage alert lifecycle: resolve/snooze/reopen. Persists to {state_dir}/alert_state.jsonl.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `action` (string) | ★ |  |
| `alert_id` (string) | ★ |  |
| `note` (string) |  |  |
| `snooze_days` (integer) |  |  |

---

## Code quality (guides)

### `yagura_api_doc`

[G] Exported-API doc discipline (Go). Documented ratio + undocumented exported funcs/types/consts/vars/methods.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files to analyse |

---

### `yagura_assert_check`

[G] Test assertion density analysis. Detects hollow test files (zero assertions), reports avg density per test function.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for *_test.go files to analyse |

---

### `yagura_ast_check`

[G] Go AST structural audit. os.Exit in library, empty != nil branch, defer in loop, err-string compare, parse errors.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_code_health`

[G] Composite maintainability grade (Go). Per-package A-F from complexity/apidoc/deadcode/recvcheck/assertcheck/astcheck.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files (paths relative to module root) |

---

### `yagura_complexity`

[G] Cyclomatic complexity (Go, gocyclo-compatible). Per-function McCabe score; flags functions over threshold (default 10).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files to analyse |
| `threshold` (integer) |  | complexity threshold for findings (default 10) |

---

### `yagura_coupling`

[G] Package import coupling (Go). Fan-in/out + instability (Ce/(Ca+Ce)) + Stable Dependencies Principle violations.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files (paths relative to module root) |
| `module_path` (string) |  | go.mod module path for internal-import detection (defaults to the server's main module) |

---

### `yagura_dead_code`

[G] Dead unexported declarations (Go). Package-level funcs/types/consts/vars never referenced within their package.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files (paths relative to package root) |

---

### `yagura_err_policy`

[G] Error-context discipline (Go). Wrap ratio (fmt.Errorf %w vs naked return err) + blank-discard (`_ = call()`) detection.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files to analyse |

---

### `yagura_recv_check`

[G] Method receiver consistency (Go). Inconsistent receiver names, mixed value/pointer receivers, un-idiomatic names (this/self).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files to analyse |

---

## Graph

### `yagura_graph_impact`

[G] Project impact (transitive reverse deps). Cycle-aware.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `slug` (string) | ★ |  |

---

### `yagura_graph_neighbors`

[G] Graph walk from slug. Returns direct + N-hop deps/dependents.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `depth` (integer) |  | max hops (default 2, max 10) |
| `slug` (string) | ★ | project slug to explore from |

---

### `yagura_graph_stats`

[G] Graph stats: nodes/edges/hubs + dangling deps.

---

## Handoff

### `yagura_handoff`

[S] Handoff: save + mark + launch target. dry_run optional.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `dry_run` (boolean) |  |  |
| `free_notes` (string) |  |  |
| `target` (string) | ★ |  |
| `workspace` (string) |  |  |

---

## Harness (guides)

### `yagura_agents_md`

[G] Generate AGENTS.md for a registered project from Plan.md + registry facts. Cross-tool: Claude Code / Codex / Cursor.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `include_rules` (boolean) |  | Include house rules section (default true) |
| `slug` (string) | ★ |  |
| `write` (boolean) |  | Also write to {local_path}/AGENTS.md (v0.33.0) |

---

### `yagura_feature_list`

[G] Convert Plan.md into Anthropic-style feature-list.json for long-running agent harnesses.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `slug` (string) | ★ |  |
| `write` (boolean) |  | Also write to {local_path}/feature-list.json (v0.33.0) |

---

### `yagura_harness_coverage`

[G] Self-audit: which Fowler taxonomy quadrants does yagura cover? Returns guides/sensors × computational/inferential matrix.

---

### `yagura_harness_recommend`

[G] Claude Code .claude/ scaffold by slug or language.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `language` (string) |  | language override (go/typescript/python/rust/...) |
| `slug` (string) |  | project slug (looks up language from registry) |

---

### `yagura_init_sh`

[G] Generate init script (sh or PowerShell) for long-running agent sessions (Anthropic 2-agent harness).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `slug` (string) | ★ |  |
| `target` (string) |  | 'posix' (default, init.sh) or 'powershell'/'windows' (init.ps1). |
| `write` (boolean) |  | Also write to {local_path}/init.{sh,ps1}. |

---

### `yagura_progress_file`

[G] Generate claude-progress.txt for cross-session handoff (Anthropic 2-agent harness pattern).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `note` (string) |  | Optional free-form intent / state. |
| `slug` (string) | ★ |  |
| `write` (boolean) |  | Also write to {local_path}/claude-progress.txt |

---

### `yagura_skill_audit`

[G] SKILL.md audit: trigger, Gotchas, length. 0-100 score + retire signal.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `content` (string) | ★ | full SKILL.md text including frontmatter |

---

### `yagura_subagent_audit`

[G] Subagent .md audit: prompt style, tools, description.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `content` (string) | ★ | full subagent .md text including frontmatter |

---

## Inventory

### `yagura_get`

[G] Get project by slug.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `slug` (string) | ★ |  |

---

### `yagura_list`

[G] List projects (compact). Optional limit caps rows.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `limit` (integer) |  | 最大返却件数(省略時は全件)。トークン節約用。 |

---

### `yagura_register`

[G] Register project.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `depends_on` (array) |  |  |
| `display_name` (string) |  |  |
| `language` (string) |  |  |
| `local_path` (string) |  |  |
| `notes` (string) |  |  |
| `priority` (integer) |  |  |
| `repository` (string) | ★ |  |
| `slug` (string) | ★ |  |
| `stage` (string) |  |  |
| `tags` (array) |  |  |

---

### `yagura_search`

[G] Search projects: tag/lang/stage/text (AND).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `language` (string) |  | 言語完全一致 (大文字小文字無視) |
| `limit` (integer) |  | 最大返却件数(省略時は全件)。 |
| `query` (string) |  | slug/name/notes/tags を横断する部分一致 |
| `stage` (string) |  | active / maintenance / paused / archived |
| `tag` (string) |  | tag 完全一致 (大文字小文字無視) |

---

### `yagura_stats`

[G] Portfolio counts/totals/averages.

---

### `yagura_today`

[G] Top projects today by score.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `limit` (integer) |  |  |

---

### `yagura_unregister`

[G] Unregister project (hard delete).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `slug` (string) | ★ |  |

---

### `yagura_update`

[G] Update project manual fields. Omit field = unchanged.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `depends_on` (array) |  |  |
| `display_name` (string) |  |  |
| `language` (string) |  |  |
| `local_path` (string) |  |  |
| `notes` (string) |  |  |
| `priority` (integer) |  |  |
| `slug` (string) | ★ |  |
| `stage` (string) |  |  |
| `tags` (array) |  |  |

---

## Misc

### `yagura_agent_config_audit`

[G] OpenClaw-style openclaw.json audit: security + reliability + model refs.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `content` (string) | ★ | full openclaw.json text |

---

### `yagura_agent_event`

[G] Normalize any agent's lifecycle event (Claude Code/Gemini/Codex/OTel/generic) to OTel GenAI semconv.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `event` (object) | ★ | the raw lifecycle event object from any agent's hook/telemetry. |

---

### `yagura_calibrate`

[Q] Threshold calibration: percentile distributions of complexity/params/returns/func-lines to set data-driven --max gates

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_cognit`

[Q] Cognitive complexity per function (human reading cost; nesting-weighted, switch=1; gocognit-style — complements McCabe)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `max` (integer) |  | cognitive-complexity threshold above which a function is flagged (default 15) |

---

### `yagura_ctx_check`

[Q] context.Context discipline: must be first param (not in struct fields). Go convention (containedctx-style)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_dedupe_stats`

[S] Content cache stats: hits/misses/bytes saved. Visualizes redundant-read prevention.

---

### `yagura_dep_rank`

[Q] Package dependency rank: internal packages by import in-degree (blast radius when changed)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `module_prefix` (string) | ★ | Go module path prefix (e.g. github.com/shizukutanaka/yagura) |
| `threshold` (integer) |  | Minimum in-degree to flag (default 5) |

---

### `yagura_err_discard`

[Q] Error-discard smell: call sites where a returned error is silently ignored

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `strict` (boolean) |  |  |

---

### `yagura_err_wrap`

[Q] Error-wrapping discipline (Go 1.13): %w not %v, errors.Is over ==, errors.As over type assert (errorlint-style)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_flag_arg`

[G] Boolean flag-argument smell (Go, Fowler). Detects functions with bool parameters that encode hidden control-flow branches. A bool arg that selects behavior ('if verbose', 'if dryRun') is opaque at call sites; consider splitting into two clearly named functions.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files to analyse |
| `threshold` (integer) |  | minimum number of bool params to flag (default 1; set 2 to skip single-bool cases) |

---

### `yagura_global_check`

[Q] Mutable global state: package-level vars actually mutated somewhere (testability + data-race hazard; gochecknoglobals-style)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_heartbeat`

[S] Agent heartbeat (~5min). Detects stale agents.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `agent` (string) | ★ |  |

---

### `yagura_hotspot`

[Q] Convergent-signal hotspots: functions flagged by 2+ of 12 independent lenses (complexity, params, returns, cognit, nestdepth, and more)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `min_lenses` (integer) |  | Minimum number of lenses that must converge to report a hotspot (default 2) |

---

### `yagura_ifacebloat`

[Q] Interface design: named interfaces with too many methods (Rob Pike "bigger interface = weaker abstraction"; interfacebloat-style)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `threshold` (integer) |  | method-count threshold above which an interface is flagged (default 10) |

---

### `yagura_inject_scan`

[S] Indirect prompt-injection scan of untrusted content: override/exfil/hidden/encoding (multilingual).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `content` (string) | ★ | untrusted text the agent ingested (fetched web page, issue body, tool output, file) |

---

### `yagura_lens_overlap`

[Q] Meta: Jaccard overlap between hotspot's 12 lenses — high overlap flags consolidation candidates, near-zero confirms orthogonal axes

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_mcp_audit`

[S] MCP .mcp.json / tools audit: tool-poisoning, fetch|sh, unpinned npx, secrets.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `content` (string) | ★ | full .mcp.json server config or a tools/list JSON |

---

### `yagura_naked_ret`

[Q] Naked-return readability: naked `return` in long named-result functions (nakedret-style, default >30 lines)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `max_lines` (integer) |  | function line-count threshold above which naked returns are flagged (default 30) |

---

### `yagura_name_check`

[Q] Name↔signature consistency: predicates (is/has) must return bool, getters/constructors must return a value

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_nest_depth`

[Q] Max control-flow nesting depth per function (the pyramid-of-doom signal complexity misses; guard-clause discipline)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `max_depth` (integer) |  | nesting-depth threshold above which a function is flagged (default 4) |

---

### `yagura_ops_risk`

[G] Classify operations into an autonomy tier (auto/log/review/human) by capability/reversibility/blast.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `operations` (array) | ★ | operations to classify. |

---

### `yagura_parallel_plan`

[G] Plan parallel task fan-out across AI agents.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `agents` (array) | ★ | AI agents to fan out to. Each: {name, tier, capacity_percent?, max_concurrency?}. |
| `global_concurrency` (integer) |  |  |
| `strategy` (string) |  |  |
| `task_count` (integer) |  | Shorthand: N uniform unit-weight tasks (task-1..task-N) when 'tasks' omitted. |
| `tasks` (array) |  | Independent work items. Each: {id, weight?, min_tier?}. |

---

### `yagura_param_check`

[G] Long-parameter-list smell (Go, Fowler). Per-function param count; flags functions over threshold (default 5).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files to analyse |
| `threshold` (integer) |  | parameter-count threshold for findings (default 5) |

---

### `yagura_path_policy`

[G] Gate changed paths against glob rules → deny/review/allow (strictest match wins).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `changed` (array) | ★ | changed file paths to evaluate (e.g. git diff --name-only). |
| `policy` (object) | ★ | {rules:[{path(glob),action(deny|review|allow),reason?}], default?(default allow)}. |

---

### `yagura_plugin_audit`

[G] Claude Code plugin.json / marketplace.json audit (auto-detected).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `content` (string) | ★ | full plugin.json or marketplace.json text |

---

### `yagura_prealloc`

[Q] Performance: slices grown by append in a range loop without preallocation (make([]T,0,len); prealloc-style)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_predeclared`

[Q] Predeclared-identifier shadowing: vars/params/types/funcs that shadow Go builtins (predeclared-style)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `ignore` (array) |  | predeclared identifiers to allow shadowing (e.g. ["cap","min","max"]) |

---

### `yagura_publicity_scan`

[S] Pre-publish leak scan: home paths, internal hosts, private IPs, emails.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `content` (string) | ★ | text content to scan before publishing (SKILL.md, docs, diff) |

---

### `yagura_quota_forecast`

[S] Empty-time linreg forecast. Needs ≥3 samples.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `agent` (string) | ★ |  |

---

### `yagura_recovery_decide`

[G] Pick the next recovery action for a failed agent task (retry/replan/escalate...).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `agent` (string) |  | current agent (used for substitute decisions). |
| `attempt` (integer) |  | 1-based attempt count for this task. |
| `class` (string) | ★ | failure class: timeout/rate_limit/tool_init/bad_args/tool_error/auth/quota/context_overflow/wrong_result/unknown (aliases like 429/403 accepted). |
| `max_attempts` (integer) |  | recovery budget (default 3). |
| `severity` (string) |  | 'low' lets an exhausted budget degrade gracefully instead of escalating. |

---

### `yagura_regress`

[Q] Quality ratchet: compare old vs new code and report functions whose complexity/params/returns/lines regressed

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `new` (object) | ★ | current file set (path→content) |
| `old` (object) | ★ | baseline file set (path→content) |

---

### `yagura_return_check`

[G] Many-return-values smell (Go). Counts return values per function; flags functions over threshold (default 3). Complements param_check (input width) with output width — together they form a complete function-signature profile.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ | map of filename → content for .go files to analyse |
| `threshold` (integer) |  | return-value count threshold for findings (default 3; flags 4+ returns) |

---

### `yagura_risk_triage`

[G] Composite fix-priority for CVEs (asset+reach+exploit, with rationale).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `asset_priority` (integer) |  |  |
| `dependents` (integer) |  |  |
| `findings` (array) | ★ | Vulnerabilities to triage. Each: {cve, cvss?, severity?, internet_exposed?, auth_required?, waf_protected?, known_exploited?, public_exploit?, patch_blocks_business?}. |
| `slug` (string) |  | Registered asset slug — auto-fills asset_priority/stage/tags and blast radius (dependents) from the registry + dependency graph. |
| `stage` (string) |  |  |
| `tags` (array) |  |  |
| `weights` (object) |  | Optional partial override of scoring weights (merged over defaults), e.g. {"known_exploited": 40, "band_now": 80}. |

---

### `yagura_self_improve`

[S] Turn harness self-metrics into ranked, gated improvement proposals (RSI, deterministic). Omit 'tools' to self-collect live stats; set record=true to append the assessment to the audit trail.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `coverage_gaps` (array) |  | Fowler matrix quadrants with no control (from yagura_harness_coverage). |
| `prev_tools` (array) |  | previous window's tool stats; enables fitness/regression detection (revert advice). |
| `record` (boolean) |  | append this assessment (counts + proposal ids) to the append-only audit log — the auditable RSI trajectory. |
| `session_calls` (integer) |  | total tool calls in the window (used for token-economy call-share thresholds). |
| `skills` (array) |  | skill audit results (from yagura_skill_audit): [{path, score, retire}]. |
| `tools` (array) |  | observed per-tool stats; OMIT to auto-collect this daemon's live token stats. [{name, calls, errors, avg_resp_bytes}]. |

---

### `yagura_session_load`

[S] Load handoff context. Null if none.

---

### `yagura_session_save`

[S] Save handoff context.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `active_files` (array) |  |  |
| `branch` (string) |  |  |
| `free_notes` (string) |  |  |
| `last_commit` (string) |  |  |
| `open_todos` (array) |  |  |
| `plan_md_step` (string) |  |  |
| `saved_by` (string) |  |  |
| `workspace` (string) | ★ |  |

---

### `yagura_session_summary`

[S] Structured tool-call summary of an agent session (any agent): pass events, or a slug to summarize the recorded timeline.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `events` (array) |  | chronological raw lifecycle events from any agent (each normalized via agent_event). |
| `limit` (integer) |  | with slug: max recorded events to consider (default 500). |
| `session` (string) |  | with slug: restrict to one session id. |
| `slug` (string) |  | summarize the daemon's recorded hook timeline for this project instead of passing events. |

---

### `yagura_settings_audit`

[G] .claude/settings.json audit: permissions deny, unrestricted allow, hooks.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `content` (string) | ★ | full settings.json text |

---

### `yagura_sync_check`

[Q] sync-lock copy discipline: methods/params/returns must not copy types containing sync.Mutex/RWMutex/etc (copylocks-style)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_thelper`

[Q] Test quality: test helpers (take *testing.T/B/TB) that never call t.Helper() (failures point at the helper; thelper-style)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_token_stats`

[S] Per-tool byte counts since daemon start.

---

### `yagura_tools_catalog`

[G] Full tool details lookup. Use when compact mode hides info you need.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `name` (string) |  |  |
| `query` (string) |  |  |

---

### `yagura_type_assert`

[Q] Panic-safety: single-value type assertions x.(T) that panic on mismatch (use comma-ok; forcetypeassert-style)

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |

---

### `yagura_usage_summary`

[S] Agent usage summary + sparkline. Both agents by default.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `agent` (string) |  |  |

---

### `yagura_vex`

[G] Build/merge + lint an OpenVEX v0.2.0 doc from per-CVE statements (not_affected/affected/fixed).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `author` (string) |  | VEX author identity (default 'yagura'). |
| `base` (object) |  | optional existing OpenVEX doc; statements are merged in (new vulns only, existing verdicts preserved, version bumped). |
| `statements` (array) | ★ | per-vulnerability statements. |
| `tooling` (string) |  | optional tool identifier that produced this doc. |

---

### `yagura_workflow_audit`

[G] Dynamic Workflow JS audit: token budget, /goal, quarantine, fan-out.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `content` (string) | ★ | full workflow JavaScript source |

---

## Observability

### `yagura_agent_status`

[S] Get both agents' quota state + recommended next agent.

---

### `yagura_hook_stats`

[S] Aggregate Claude Code hook stats per project (event counts, errors, top tools).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `slug` (string) |  | Project slug. Empty = all projects. |
| `top_n` (integer) |  | Top-N tools. Default 10. |

---

### `yagura_hook_timeline`

[S] Recent Claude Code hook events for a project. Use to see what tools agents have invoked recently.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `event_type` (string) |  | Filter by hook_event_name (PreToolUse, PostToolUse, Stop, …). |
| `hours` (integer) |  | Look-back window. Default 24. |
| `limit` (integer) |  | Max events returned. Default 100, capped at 500. |
| `slug` (string) |  | Project slug. Empty = all projects. |

---

### `yagura_quota_report`

[S] Report agent quota. Triggers auto-handoff.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `agent` (string) | ★ |  |
| `remaining_percent` (integer) | ★ |  |
| `source` (string) |  |  |
| `weekly_resets_at` (string) |  |  |
| `window_resets_at` (string) |  |  |

---

## Plan tracking

### `yagura_plan_status`

[G] Plan.md progress for project. Parses checkboxes + required sections (目的/スコープ/フェーズ/DoD).

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `slug` (string) | ★ |  |

---

### `yagura_release_radar`

[S] Cross-project release readiness ranking. Aggregates Plan/CI/issues/quality/AI-risk.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `limit` (integer) |  |  |
| `scan_code` (boolean) |  |  |

---

## Security (sensors)

### `yagura_ai_verify`

[G] AI code risk audit. 6 categories: auth/billing/data/external/crypto/secret. 2x multiplier inside AI-marker zones.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `custom_rules` (array) |  | project-specific AI risk rules: [{id, pattern(regex), category, risk(CRITICAL|HIGH|MEDIUM|LOW), message, languages?}] |
| `disable_rules` (array) |  | built-in rule IDs to suppress (e.g. ["billing-stripe-uncaught"]) |
| `files` (object) |  |  |
| `path` (string) |  |  |
| `summary_only` (boolean) |  |  |
| `text` (string) |  |  |

---

### `yagura_gha_audit`

[G] GHA workflow audit: 12 supply-chain risk patterns.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `summary_only` (boolean) |  |  |

---

### `yagura_health`

[S] Security summary: vuln + Scorecard issues. Cached.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `individual` (boolean) |  |  |
| `slug` (string) |  |  |

---

### `yagura_pin_drift`

[G] GHA SHA pin verify via API. Detects drift/stale.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `concurrency` (integer) |  |  |
| `files` (object) | ★ |  |
| `summary_only` (boolean) |  |  |

---

### `yagura_quality_check`

[G] Code lint: as any, ts-ignore, TODO. 3 severity tiers.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `custom_rules` (array) |  | project-specific lint rules: [{id, pattern(regex), severity(prohibited|warning|info), languages?, description?, suggestion?}] |
| `files` (object) |  |  |
| `language` (string) |  |  |
| `summary_only` (boolean) |  |  |
| `text` (string) |  |  |

---

### `yagura_sbom`

[G] yagura SBOM CycloneDX 1.5. Set summary_only for compact.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `summary_only` (boolean) |  |  |

---

### `yagura_scorecard`

[S] OpenSSF Scorecard fetch. priority_only=top 7 checks.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `priority_only` (boolean) |  | 重要 7 check に絞る |
| `repo` (string) |  | owner/repo 形式 |
| `slug` (string) |  |  |

---

### `yagura_secretscan`

[G] Secret scan: 14 patterns + entropy. Redacts hits.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `custom_rules` (array) |  | project-specific secret rules: [{id, pattern(regex), severity(CRITICAL|HIGH|MEDIUM|LOW), description?, entropy_min?, capture_idx?}] |
| `disable_rules` (array) |  | built-in rule IDs to suppress (e.g. ["aws-access-key-id"]) |
| `min_severity` (string) |  |  |
| `slug` (string) |  |  |

---

### `yagura_test_audit`

[G] Source-test coverage detection. Go/TS/JS/Python/Rust/Java filename + Rust inline #[cfg(test)] + Python doctest.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `files` (object) | ★ |  |
| `untested_only` (boolean) |  |  |

---

### `yagura_vulns`

[S] OSV.dev vulns by CVSS desc.

**Arguments:**

| Name | Required | Description |
|---|---|---|
| `ecosystem` (string) |  | OSV ecosystem(Go/PyPI/npm 等) |
| `min_severity` (string) |  |  |
| `package` (string) |  | パッケージ識別子(Go module path 等) |
| `slug` (string) |  | 登録済みプロジェクトの slug |
| `version` (string) |  | バージョン文字列(省略時は全バージョン) |

---

## Generation

Auto-generated from `tools/list`. Regenerate with `make docs-mcp`.
