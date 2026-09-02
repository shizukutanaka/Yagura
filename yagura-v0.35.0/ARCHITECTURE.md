# Architecture

This document describes Yagura's structural design, control flow, and the
rationale for major decisions. For decisions captured in detail see
`docs/adr/`. For threat model see `docs/security-spec.md`.

## High-level diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                         User's machine                          │
│                                                                 │
│   ┌──────────────┐                ┌──────────────────────────┐  │
│   │ Claude Code  │  JSON-RPC 2.0  │       Yagura daemon      │  │
│   │   (or any    │ ──────────────►│                          │  │
│   │ MCP client)  │   /mcp         │  ┌────────────────────┐  │  │
│   └──────────────┘                │  │  MCP server (5)    │  │  │
│                                   │  │  - yagura_list     │  │  │
│   ┌──────────────┐                │  │  - yagura_get      │  │  │
│   │   Browser    │      GET       │  │  - yagura_search   │  │  │
│   │              │ ──────────────►│  │  - yagura_today    │  │  │
│   └──────────────┘   /dashboard   │  │  - yagura_register │  │  │
│                                   │  └────────────────────┘  │  │
│                                   │            │             │  │
│                                   │            ▼             │  │
│                                   │  ┌────────────────────┐  │  │
│                                   │  │     Registry       │  │  │
│                                   │  │ (RWMutex + JSON)   │  │  │
│                                   │  └────────────────────┘  │  │
│                                   │            │             │  │
│                                   │            ▼             │  │
│                                   │  ┌────────────────────┐  │  │
│                                   │  │  ~/.yagura/state   │  │  │
│                                   │  │  ├─ projects/      │  │  │
│                                   │  │  ├─ audit/         │  │  │
│                                   │  │  └─ drafts/        │  │  │
│                                   │  └────────────────────┘  │  │
│                                   │                          │  │
│                                   │  ┌────────────────────┐  │  │
│                                   │  │  Scanner (5min)    │──┼──┼──► api.github.com
│                                   │  └────────────────────┘  │  │
│                                   │                          │  │
│                                   │  ┌────────────────────┐  │  │
│                                   │  │  Audit (JSONL +    │  │  │
│                                   │  │  hash chain)       │  │  │
│                                   │  └────────────────────┘  │  │
│                                   └──────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

## Component responsibilities

### cmd/yagura
Entry point. Wires together all internal packages. Handles signal-based
graceful shutdown, the HTTP server, sub-commands (`verify` / `version`).

### internal/config
Reads environment variables, validates them, and produces an immutable
`*Config`. Refuses to start when binding to a public interface without
auth token (defense in depth). Redacts secrets in log output.

### internal/logging
Thin wrapper over `log/slog` producing structured JSON. Adds `service` and
`version` to every record via `slog.With`.

### internal/metrics
Prometheus-format exposition (zero-dep). Counter / Gauge / Histogram types
with `sync/atomic` for lock-free updates on the hot path.

### internal/project
Pure data: the `Project` struct, validation, sort helpers. No I/O.

### internal/registry
Persistent state for the project portfolio. Atomic file writes (temp + rename),
in-memory cache protected by `sync.RWMutex`, file mode 0600. One JSON file
per project, named by slug.

### internal/github
Minimal REST API client. Implements only the four endpoints scanner needs:
GET repo, count open PRs, latest release, latest workflow run. Tracks rate
limit headers. Returns sentinel errors for 404 / 401 / 429.

### internal/scanner
Background goroutine that polls GitHub every `ScanInterval` (default 5m).
For each scannable project, fetches the four endpoints in parallel
(bounded by `MaxConcurrent`), then merges results into the registry
without overwriting manually-managed fields.

### internal/mcp
JSON-RPC 2.0 server over HTTP. Implements `initialize`, `tools/list`,
`tools/call`. Five default tools registered at startup. Optional audit sink
injected via `SetAudit()`.

### internal/audit
Append-only JSONL with SHA-256 hash chain. Each daily file is independent.
Verifies at byte level via `audit.Verify()`. Hash chain continues across
daemon restarts by reading the last line of the existing file at open time.

### internal/dashboard
Single-page server-rendered HTML. Pure `html/template`, no JavaScript,
no external resources. Sorted by stage → priority → slug.

## Control flow: startup

```
config.Load
  └► returns *Config or errors.Join(missing required vars, ...)

logging.New(level, "yagura", version, stdout)
  └► slog.Logger with service+version auto-attached

metrics.NewRegistry()
  └► Counter/Gauge instances created

registry.New(cfg.ProjectsDir())
  └► MkdirAll 0700
  └► load all *.json into in-memory cache
  └► return *Registry (partial errors as warnings only)

github.NewClient(cfg.GitHubToken, ...)
  └► returns *Client

scanner.New(...) + scan.Start(ctx)
  └► spawns goroutine, immediate scan, then ticker every Interval

audit.New(cfg.AuditPath())
  └► MkdirAll 0700
  └► open today's .jsonl in O_APPEND
  └► tail file to restore last hash and seq

mcp.New(token, logger).SetAudit(auditLog)
  └► returns *Server with 5 tools registered

dashboard.New(reg, logger)
  └► returns *Handler with template parsed once

http.Server.ListenAndServe(cfg.Addr, mux)
  └► /healthz /readyz /metrics /mcp /dashboard [+/debug/pprof]
```

## Control flow: graceful shutdown

```
SIGTERM/SIGINT received
  └► ready.Store(false)
  └► time.Sleep(readyDrainGrace = 5s)        // drain in-flight requests
  └► scanner.Stop()                          // stop goroutine
  └► http.Server.Shutdown(httpShutdownGrace) // close listener, finish current requests
  └► audit.Append({kind: "yagura_stopped"})
  └► audit.Close()                           // fsync + close
  └► return nil
```

## Data: project file format

`~/.yagura/state/projects/<slug>.json` example:

```json
{
  "slug": "mihari",
  "display_name": "Mihari (見張り)",
  "repository": "shizukutanaka/mihari",
  "language": "Go",
  "tags": ["daemon", "mcp", "github"],
  "stage": "active",
  "priority": 4,
  "notes": "GitHub webhook + AI orchestrator",
  "latest_version": "v0.11.0",
  "latest_activity": "2026-05-11T08:23:11Z",
  "open_prs": 2,
  "open_issues": 5,
  "ci_status": "passing",
  "created_at": "2026-03-01T00:00:00Z",
  "updated_at": "2026-05-13T02:11:25Z"
}
```

## Data: audit file format

`~/.yagura/state/audit/YYYY-MM-DD.jsonl` example (3 records):

```jsonl
{"time":"2026-05-13T02:11:19.642066122Z","seq":1,"kind":"yagura_started","actor":"yagura","fields":{"go":"go1.22.2","version":"0.1.0"},"hash":"8d87d6..."}
{"time":"2026-05-13T02:11:20.648112543Z","prev_hash":"8d87d6...","seq":2,"kind":"mcp_call_ok","actor":"mcp","target":"yagura_list","fields":{"duration_ms":0},"hash":"5d9abf..."}
{"time":"2026-05-13T02:11:25.673570430Z","prev_hash":"5d9abf...","seq":3,"kind":"yagura_stopped","actor":"yagura","hash":"ff0e26..."}
```

Each record's `hash` is `SHA-256(JSON of record with hash field empty)`.
Each record's `prev_hash` is the `hash` of the previous record in the same file.
Files are independent: hash chain does not span files.

## Concurrency model

- Registry: `sync.RWMutex`. Reads are concurrent, writes serial.
- Audit: `sync.Mutex`. All operations serial. File I/O within lock.
- Scanner: 1 goroutine ticking the interval. Per-cycle spawns up to
  `MaxConcurrent` worker goroutines, joined before returning.
- HTTP: Go's `http.Server` handles goroutine-per-request.
- All long-lived goroutines respect `ctx.Done()` for graceful shutdown.

## Failure modes and handling

| Failure | Behavior |
|---|---|
| Config missing required | Refuse to start, exit 1 |
| State dir inaccessible | Refuse to start, exit 1 |
| One project JSON corrupt | Skip that project, log warning, continue |
| GitHub API unreachable | Per-project failure logged, scanner continues |
| GitHub API rate limited | Wait until reset, next cycle resumes |
| MCP tool panic | Recovered, returned as `internal` error, audit recorded |
| Audit file unwritable | Log error, MCP/scanner continue (degraded) |
| HTTP listen fails | Exit 1 |
| Daemon crash | All state preserved in JSON files; restart resumes |

## Performance characteristics

For a portfolio of 23 projects:

- Cold start: ~10 ms (load 23 JSON files)
- `yagura_list`: O(N) where N = project count, served from in-memory cache (~1 ms)
- `yagura_get`: O(1), single map lookup
- `yagura_search`: O(N) with filter, returns deep copies
- `yagura_today`: O(N log N) for score-based sort
- Scan cycle: ~4 GitHub API requests × 23 projects = 92 requests, ~5 seconds with concurrency 4
- Audit append: 1 fsync per record (~1-5 ms on SSD)

The system is designed for portfolios up to ~100 projects without architectural
changes. Beyond that, consider migrating registry persistence to SQLite (ADR pending).
