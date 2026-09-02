# Metrics reference

Yagura exposes Prometheus metrics at **`GET /metrics`** (plain-text exposition
format, no auth). They fall into two groups, both served from the same endpoint:

1. **Process / scan gauges** — registered at startup (`internal/metrics`).
2. **Label-rich operational metrics** — assembled per-scrape from live daemon
   state (`collectYaguraMetrics` in `cmd/yagura/main.go`): MCP tools, agent
   hooks, dedupe cache, portfolio health, and alert lifecycle.

All names are prefixed `yagura_`. Counters are monotonic; gauges can go up or
down. This file is kept in sync with the code by
`cmd/yagura/metricsdoc_test.go` — every metric the daemon can emit must appear
here, or the build fails.

> Zero dependencies (ADR-0001): the exposition format is rendered by
> `internal/promexport`, stdlib only — no Prometheus client library.

## Process & scan

| Metric | Type | Labels | Description |
|---|---|---|---|
| `yagura_build_info` | gauge | `version` | Always `1`; the build version rides in the label. |
| `yagura_start_time_unix` | gauge | — | Unix timestamp of process start. |
| `yagura_scan_total` | counter | — | Total project scans performed. |
| `yagura_scan_failed_total` | counter | — | Total scans that failed (per project, per cycle). |
| `yagura_last_scan_duration_ms` | gauge | — | Duration of the most recent scan cycle, milliseconds. |
| `yagura_last_scan_unix` | gauge | — | Unix timestamp of the most recent scan cycle. |
| `yagura_projects_total` | gauge | — | Total registered projects. |
| `yagura_projects_active` | gauge | — | Projects in the `active` stage. |
| `yagura_projects_failing_ci` | gauge | — | Projects whose last observed CI status is failing. |

## Portfolio health

Emitted from the latest `alert_fix` health sweep (the scanner runs one after each
cycle). Resolved/snoozed alerts are already excluded — these are *open* alerts.
The series is **absent until the first sweep has run** (no misleading zeros).

| Metric | Type | Labels | Description |
|---|---|---|---|
| `yagura_portfolio_alerts` | gauge | `severity` (`critical`/`high`/`medium`/`low`) | Open alerts from the latest health sweep, by severity. |
| `yagura_alert_lifecycle_current` | gauge | `status` (`active`/`resolved`/`snoozed`) | Current count of alerts by lifecycle status (from the alert store). |

Example alerting rule:

```yaml
- alert: YaguraCriticalPortfolioAlert
  expr: yagura_portfolio_alerts{severity="critical"} > 0
  for: 10m
  annotations:
    summary: "A critical alert is open across the project portfolio"
```

## MCP tools

Per-tool counters covering every MCP tool invocation through `/mcp`.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `yagura_mcp_tool_calls_total` | counter | `tool` | Total invocations per tool. |
| `yagura_mcp_tool_errors_total` | counter | `tool` | Total errors returned per tool. |
| `yagura_mcp_tool_request_bytes_total` | counter | `tool` | Cumulative request bytes per tool. |
| `yagura_mcp_tool_response_bytes_total` | counter | `tool` | Cumulative response bytes per tool. |

## Agent hooks

Any agent (Claude Code / Gemini CLI / Codex / OTel) that posts lifecycle events
to `/hooks/agent` is aggregated here. Tool names map to the OpenTelemetry GenAI
`gen_ai.tool.name` convention, exposed as the `tool` label.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `yagura_hook_events_total` | counter | `project`, `event` | Hook events received per project per event type. |
| `yagura_hook_tool_calls_total` | counter | `project`, `tool` | Tool calls observed via hooks per project per tool. |
| `yagura_hook_errors_total` | counter | `project` | Tool errors observed via hooks per project. |

## Cache

Emitted only once the dedupe cache has been exercised.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `yagura_cache_hits_total` | counter | — | Cumulative dedupe cache hits. |
| `yagura_cache_misses_total` | counter | — | Cumulative dedupe cache misses. |

## Notes

- Label-rich operational metrics are computed lazily per scrape from live state,
  so values always reflect the moment of scraping.
- Conditional series (`yagura_portfolio_alerts`, `yagura_cache_*`,
  `yagura_hook_*`, `yagura_alert_lifecycle_current`) appear only once there is
  data — before then the series is simply absent, which is normal.
