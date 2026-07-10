# 櫓 Yagura

[![CI](https://github.com/shizukutanaka/yagura/actions/workflows/ci.yml/badge.svg)](https://github.com/shizukutanaka/yagura/actions/workflows/ci.yml)
[![CodeQL](https://github.com/shizukutanaka/yagura/actions/workflows/codeql.yml/badge.svg)](https://github.com/shizukutanaka/yagura/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/shizukutanaka/yagura/badge)](https://scorecard.dev/viewer/?uri=github.com/shizukutanaka/yagura)
[![Go Reference](https://pkg.go.dev/badge/github.com/shizukutanaka/yagura.svg)](https://pkg.go.dev/github.com/shizukutanaka/yagura)
[![Go Report Card](https://goreportcard.com/badge/github.com/shizukutanaka/yagura)](https://goreportcard.com/report/github.com/shizukutanaka/yagura)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Reproducible Build](https://img.shields.io/badge/build-reproducible-success)](#reproducibility)

**A zero-dependency Go MCP server for orchestrating a portfolio of solo-developer projects** — and a working example of harness engineering as a deployable artifact.

Status: **v0.109.0** — 101 MCP tools, 86 internal packages, 24 computational sensors, shell tab-completion (`yagura completion bash|zsh|fish`). **Commercial-grade hardening pass 6:** a fresh audit found v0.107.0's panic-recovery fix had incomplete scope — it covered `cmd/yagura/main.go`'s background goroutines but missed `internal/scanner`'s own (higher-risk, since they parse real GitHub/OSV/Scorecard API responses per project). Now protected too. Full lens-by-lens release history: see [CHANGELOG.md](CHANGELOG.md).

---

## What is Yagura?

If you maintain more than a handful of repositories at once, the cost of context-switching grows superlinearly. Existing tools (Backstage, Linear, Sourcegraph) assume team scale, fight against MCP-first workflows, or pull you into someone else's data plane.

Yagura sits in that gap. It is **one process you run locally** that:

- Knows about all your repositories (registered once with `yagura_register`).
- Scans them for vulnerabilities, secrets, GitHub Actions drift, and pin staleness (24 computational sensors).
- Generates the cross-tool agent harness artifacts that Anthropic's 2-agent long-running pattern needs (`AGENTS.md`, `feature-list.json`, `claude-progress.txt`, `init.sh` / `init.ps1`).
- Receives Claude Code's HTTP hooks at `/hooks/claude-code` and turns them into Prometheus metrics and queryable timelines.
- Tracks alert lifecycle (active / resolved / snoozed) so the same problem is not nagged twice.

It exposes all of this via the [Model Context Protocol](https://modelcontextprotocol.io/), so Claude Code, OpenAI Codex, Cursor, and any future MCP client can drive it without bespoke integration.

```
┌──────────────────────────────────────────────────────┐
│  Claude Code / Codex / Cursor / Factory              │
│        │                              ▲              │
│        │ JSON-RPC over HTTP           │ /hooks POST  │
│        ▼                              │              │
│  ┌────────────────────────────────────────────────┐  │
│  │  yagura daemon (single binary, ~9 MB)          │  │
│  │  - 93 MCP tools                                │  │
│  │  - HTTP hook receiver                          │  │
│  │  - Prometheus /metrics                         │  │
│  │  - .well-known/mcp (2026 spec)                 │  │
│  │  - Append-only audit log (hash-chained)        │  │
│  └────────────────────────────────────────────────┘  │
│        │                                             │
│        ├──→ GitHub API (read-only by default)        │
│        ├──→ OSV.dev (vulnerabilities)                │
│        ├──→ OpenSSF Scorecard                        │
│        └──→ JSON file state (~/.yagura/state)        │
└──────────────────────────────────────────────────────┘
```

## Design tenets

- **Zero external Go dependencies.** Only the standard library. `go.sum` is empty by policy ([ADR-0001](docs/adr/0001-zero-dependencies.md)).
- **Single binary.** ~9 MB statically linked. No runtime, no installer, no daemon manager required.
- **Reproducible build.** `-trimpath -buildvcs=false` + pinned Go version. Verified on every release.
- **Local-first.** Binds to `127.0.0.1` by default. State lives in `~/.yagura/state/` as JSON files.
- **Read-default, write-explicit.** Yagura never writes back to GitHub. Disk writes require `write: true`.
- **Append-only audit.** Every MCP call recorded in JSONL with hash chain for tamper detection.
- **MCP-first.** Designed to be invoked by an AI agent, not clicked on. The HTML dashboard at `/dashboard` is read-mostly: its one state-changing action (registering a project) is sent to the MCP server and audited like any other call — sensor data stays scanner-only.

## Desktop app (no terminal required)

Prefer not to use the command line? The dashboard is an **installable web app
(PWA)**: open it in Chrome/Edge and click **Install** to get a Yagura desktop
icon that opens in its own window, like a native app. On Windows, double-click
`yagura-tray.exe` to start the daemon, get a tray icon, and open the dashboard
as an app window; on macOS/Linux, run `yagura-tray` for the same one-click
launch. From the app you can **register your first project with a form** (no
terminal needed) — it goes through the MCP server and is audited like any other
call. This adds nothing to the core — the daemon and the 93 MCP tools are
unchanged; the desktop app is just the dashboard made installable via web
standards. See [docs/desktop.md](docs/desktop.md).

## Install

### Pre-built binary (recommended)

```bash
# Linux / macOS
curl -L https://github.com/shizukutanaka/yagura/releases/latest/download/yagura-linux-amd64 -o yagura
chmod +x yagura
./yagura --version
```

```powershell
# Windows
Invoke-WebRequest -Uri 'https://github.com/shizukutanaka/yagura/releases/latest/download/yagura-windows-amd64.exe' -OutFile yagura.exe
.\yagura.exe --version
```

SHA-256 checksums are signed and published with each release. See [docs/WINDOWS.md](docs/WINDOWS.md) for Windows service / Task Scheduler / NSSM setup.

### Build from source

```bash
git clone https://github.com/shizukutanaka/yagura
cd yagura
make build              # → bin/yagura
./bin/yagura --version
```

Requires Go 1.22+. No other build tools.

### Cross-compile for all targets

```bash
make build-all          # → bin/yagura-{linux,darwin,windows}-{amd64,arm64}{.exe}
```

### As a Claude Code plugin

Yagura ships a plugin manifest (`.claude-plugin/`) that bundles the `yagura`
skill, the `yagura-reviewer` agent, and the MCP server connection. Add this repo
as a marketplace and install:

```bash
claude plugin marketplace add shizukutanaka/yagura
claude plugin install yagura@yagura
```

The plugin connects to a locally running `yagura` daemon (HTTP MCP at
`127.0.0.1:8090`), so start `yagura` first. Validate the manifest with
`yagura plugin-audit`.

## Quickstart

```bash
export YAGURA_GITHUB_TOKEN=ghp_yourPersonalAccessToken
export YAGURA_STATE_DIR=$HOME/.yagura
./bin/yagura
```

In another shell:

```bash
# 1. Register a project
curl -sS -X POST http://127.0.0.1:8090/mcp -H 'Content-Type: application/json' -d \
  '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{
    "name":"yagura_register",
    "arguments":{
      "slug":"breeze",
      "repository":"shizukutanaka/breeze",
      "local_path":"/home/you/breeze",
      "language":"javascript"
    }
  }}'

# 2. Generate the agent harness artifacts
for tool in yagura_agents_md yagura_feature_list yagura_init_sh; do
  curl -sS -X POST http://127.0.0.1:8090/mcp -H 'Content-Type: application/json' -d \
    "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{
      \"name\":\"$tool\",\"arguments\":{\"slug\":\"breeze\",\"write\":true}}}"
done

# 3. Open the dashboard
open http://127.0.0.1:8090/dashboard
```

See [docs/QUICKSTART.md](docs/QUICKSTART.md) for a full walkthrough including Claude Code integration.

## Connecting Claude Code

Point Claude Code's HTTP hooks at the local yagura, so every tool call yagura sees becomes observable:

```jsonc
// ~/.claude/settings.json
{
  "hooks": {
    "PreToolUse":         [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }],
    "PostToolUse":        [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }],
    "PostToolUseFailure": [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }],
    "Stop":               [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }],
    "SubagentStop":       [{ "type": "http", "url": "http://127.0.0.1:8090/hooks/claude-code" }]
  }
}
```

Now `yagura_hook_timeline` and `yagura_hook_stats` show what Claude Code has been doing in each project.

### Other agents (Gemini CLI, Codex, custom)

Yagura is agent-agnostic. Its 93 MCP tools work with **any** MCP client, and the
daemon's hook ingestion is agent-neutral too: **point any agent's lifecycle
hooks at `/hooks/agent`** (Gemini CLI, Codex, raw OpenTelemetry, or a generic
shape) and the receiver normalizes them via `internal/agentevent` — aligned to
the [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
(`gen_ai.operation.name`, `gen_ai.tool.name`, …) — so `yagura_hook_stats` and
`yagura_hook_timeline` work for that agent with the same vocabulary as Claude
Code. The `yagura_agent_event` and `yagura_session_summary` tools expose the same
normalization for programmatic use, and `/metrics` exports per-project, per-tool
agent activity (`yagura_hook_tool_calls_total{project,tool}`, aligned to the
OTel `gen_ai.tool.name` convention) for Prometheus/Grafana.

## MCP tools (101 total)

Tools are tagged `[G]` (guide / feedforward) or `[S]` (sensor / feedback), following the [Fowler harness taxonomy](https://martinfowler.com/articles/harness-engineering.html).

| Category | Example tools | Purpose |
|---|---|---|
| Inventory | `list`, `get`, `search`, `today`, `stats`, `register`, `unregister`, `update` | Manage the portfolio |
| Security `[S]` | `vulns`, `scorecard`, `secretscan`, `sbom`, `gha_audit`, `pin_drift`, `ai_verify`, `test_audit`, `quality_check` | Computational sensors |
| Code quality `[G]` | `ast_check`, `complexity`, `coupling`, `api_doc`, `dead_code`, `recv_check`, `assert_check`, `err_policy`, `code_health` | Go static-analysis lens family (go/ast, zero-dep) + composite A–F maintainability grade. CLI-only siblings: `review-gate`, `diff-scan`, `flow-risk`, `coverage` |
| Supply chain `[G]` | `sbom`, `vex`, `mcp_audit` | CycloneDX SBOM + OpenVEX exploitability statements + MCP config audit |
| Injection defense `[S]` | `inject_scan` | Multilingual indirect prompt-injection scan of untrusted content |
| Harness `[G]` | `agents_md`, `feature_list`, `progress_file`, `init_sh`, `harness_recommend` | Generate handoff artifacts |
| Self-audit `[G]` | `harness_coverage` | Fowler matrix self-check |
| Alerts | `alert_fix`, `alert_resolve` | Lifecycle: active / resolved / snoozed |
| Plan tracking | `plan_status`, `release_radar` | Plan.md awareness |
| Handoff | 8 tools | Plan / Run / Handoff for cross-session work |
| Observability | `hook_timeline`, `hook_stats`, `agent_status`, `quota_report` | Claude Code activity |
| Graph | 3 tools | Cross-project dependencies |
| Reasoning `[G]` | `risk_triage`, `parallel_plan` | Composite vuln fix-priority / multi-AI parallel fan-out |
| Control plane | `recovery_decide`, `self_improve`, `path_policy`, `ops_risk` | Self-healing, bounded RSI, path governance, operation autonomy-tiering |

Full reference: [docs/MCP_TOOLS.md](docs/MCP_TOOLS.md).

## HTTP endpoints

| Path | Method | Purpose |
|---|---|---|
| `/mcp` | POST | JSON-RPC 2.0 MCP endpoint |
| `/hooks/claude-code` | POST | Receive Claude Code HTTP hooks |
| `/metrics` | GET | Prometheus exposition format ([reference](docs/METRICS.md)) |
| `/.well-known/mcp` | GET | MCP 2026 spec server metadata |
| `/healthz` | GET | Liveness |
| `/readyz` | GET | Readiness (drains 5 s on shutdown) |
| `/dashboard` | GET | Read-only HTML overview |
| `/sbom` | GET | CycloneDX SBOM for yagura itself |
| `/gha-audit` | GET | GitHub Actions audit |
| `/pin-drift` | GET | Dependency pin drift |
| `/debug/pprof` | GET | Go profiler (off by default) |

## Configuration

All configuration is via environment variables. See `.env.example` for the full list and defaults.

| Variable | Default | Purpose |
|---|---|---|
| `YAGURA_ADDR` | `127.0.0.1:8090` | HTTP listen address |
| `YAGURA_STATE_DIR` | `$HOME/.yagura` | State directory |
| `YAGURA_GITHUB_TOKEN` | — | GitHub PAT for vulnerability / scorecard scans |
| `YAGURA_AUTH_TOKEN` | — | Bearer token required for `/mcp` if set |
| `YAGURA_MCP_COMPACT` | `0` | `1` = compact tool descriptions to save context tokens |
| `YAGURA_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

## Reproducibility

Every release is byte-for-byte reproducible:

```bash
make verify
# → ✓ reproducible: byte-for-byte identical (SHA256: ...)
```

105 consecutive releases (v0.6 → v0.109.0) have shipped with identical SHA-256 across independent builds on the same Go version, `-trimpath`, `-buildvcs=false`, and `CGO_ENABLED=0`.

Released binaries are accompanied by `SHA256SUMS`. Verify before running:

```bash
sha256sum -c SHA256SUMS
```

## Project layout

```
.
├── cmd/yagura/              # Entry point (single binary)
├── internal/                # 86 packages, none exported
│   ├── mcp/                 # MCP server, tool registration
│   ├── registry/            # Project registry (JSON file per project)
│   ├── scanner/             # Background sensor loop (24 h)
│   ├── alertfix/            # Alert lifecycle store
│   ├── hookreceiver/        # Claude Code HTTP hook ingest
│   ├── agentmd/             # AGENTS.md generator
│   ├── featurelist/         # feature-list.json generator
│   ├── progressfile/        # claude-progress.txt generator
│   ├── initsh/              # init.sh generator (POSIX)
│   ├── initps1/             # init.ps1 generator (PowerShell)
│   ├── promexport/          # Prometheus exposition format
│   └── …                    # 27 more (see ARCHITECTURE.md)
├── docs/
│   ├── adr/                 # Architecture decision records
│   ├── QUICKSTART.md
│   ├── MCP_TOOLS.md
│   ├── WINDOWS.md
│   ├── security-spec.md
│   ├── vex-spec.md          # VEX subsystem specification (normative)
│   ├── harness-mandate.md   # Generic AI operating-mandate template (.yagura/harness.json)
│   └── vex/                 # VEX statements for transitive deps
├── deploy/                  # Dockerfile, systemd unit, container manifests
├── .github/workflows/       # ci / codeql / release / scorecard
└── Makefile                 # build / build-all / verify / test / lint
```

For a tour of the internals see [ARCHITECTURE.md](ARCHITECTURE.md).

## Harness engineering positioning

Yagura is not just a tool — it is a working example of the [harness engineering](https://martinfowler.com/articles/harness-engineering.html) discipline that emerged from Anthropic / OpenAI / Thoughtworks in early 2026.

Using Fowler's two-axis taxonomy (Computational × Inferential × Guide × Sensor), yagura currently covers:

|  | Computational | Inferential |
|---|---|---|
| **Guide** (feedforward) | `feature_list` | `agents_md`, `harness_recommend`, `skill_audit`, `subagent_audit` |
| **Sensor** (feedback) | `quality_check`, `secretscan`, `gha_audit`, `pin_drift`, `ai_verify`, `test_audit`, `vulns`, `scorecard`, `sbom` | (intentionally empty — see ADR-0001) |

The inferential-sensor quadrant is intentionally empty: LLM-as-judge would require an external dependency, violating the zero-dep tenet. External Claude Code subagents can plug in via `/hooks/claude-code` instead.

Self-check: `yagura_harness_coverage` reports this matrix at runtime.

## Security

- All scanners are read-only.
- Bearer-token auth is constant-time-compared to prevent timing leaks.
- Audit log is hash-chained (each entry includes SHA-256 of the previous).
- Bound to `127.0.0.1` by default; binding a public interface without `YAGURA_AUTH_TOKEN` is refused at startup.
- Threat model: [docs/security-spec.md](docs/security-spec.md).
- Report vulnerabilities privately: see [SECURITY.md](SECURITY.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Please read [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). The bar is high: zero external dependencies, deterministic tests, reproducible builds, no team-scale features. If you are unsure whether a change fits, open an issue first.

## License

[MIT](LICENSE). Copyright (c) 2026 shizukutanaka.

## Acknowledgements

This project would not exist without the public engineering writing of:

- Anthropic — [Effective harnesses for long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents), [Harness design for long-running application development](https://www.anthropic.com/engineering/harness-design-long-running-apps)
- OpenAI — [Harness engineering: leveraging Codex](https://openai.com/index/harness-engineering/)
- Martin Fowler / Birgitta Böckeler / Thoughtworks — [Harness engineering for coding agent users](https://martinfowler.com/articles/harness-engineering.html)
- LangChain — [Anatomy of an Agent Harness](https://blog.langchain.com/the-anatomy-of-an-agent-harness/)
- Mitchell Hashimoto — for naming the discipline

> *"Agent = Model + Harness. If you're not the model, you're the harness."*
