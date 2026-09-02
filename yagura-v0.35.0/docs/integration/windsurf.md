# Windsurf Integration (v0.13.0+)

## Overview

Yagura is a portfolio orchestrator daemon that exposes a JSON-RPC 2.0 MCP server. It can be used simultaneously from **Claude Code** and **Windsurf** (Codeium's AI IDE) as a common state hub. The v0.13.0 release adds five MCP tools that enable **automatic handoff** when one agent runs out of quota.

## Setup

### 1. Run yagura daemon

```bash
YAGURA_GITHUB_TOKEN=ghp_... \
YAGURA_MCP_TOKEN=<choose-a-strong-token> \
YAGURA_ADDR=127.0.0.1:9090 \
  yagura
```

### 2. Configure Windsurf

Edit `~/.codeium/windsurf/mcp_config.json` (macOS/Linux) or `%USERPROFILE%\.codeium\windsurf\mcp_config.json` (Windows):

```json
{
  "mcpServers": {
    "yagura": {
      "serverUrl": "http://127.0.0.1:9090/mcp",
      "headers": {
        "Authorization": "Bearer ${env:YAGURA_MCP_TOKEN}"
      }
    }
  }
}
```

Set `YAGURA_MCP_TOKEN` in your shell environment. **Restart Windsurf completely** (full quit + relaunch, not just close window) — config changes require subsystem reload.

### 3. Configure Claude Code

Add the same MCP server entry to Claude Code's MCP config (so both agents share state):

```bash
claude mcp add --transport http yagura \
  --url http://127.0.0.1:9090/mcp \
  --header "Authorization: Bearer $YAGURA_MCP_TOKEN"
```

### 4. Verify

In either IDE, ask the agent: "Call yagura_agent_status." It should return:

```json
{
  "statuses": {
    "claude_code": {"state": "ACTIVE", "remaining_percent": 100},
    "windsurf":    {"state": "ACTIVE", "remaining_percent": 100}
  },
  "recommended_agent": "claude_code",
  "recommendation_reason": "claude_code has more remaining (100% vs windsurf 100%)"
}
```

## Auto-handoff workflow

```
┌──────────────────────────────────────────────────────────────┐
│  Claude Code session                                         │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ /usage shows: 18% remaining, resets in 2h 30m          │  │
│  │ Agent calls: yagura_quota_report(                      │  │
│  │   agent="claude_code", remaining_percent=18,           │  │
│  │   source="auto", window_resets_at="2026-05-13T08:00Z") │  │
│  │ → response includes should_handoff: true               │  │
│  │   handoff_target: "windsurf"                           │  │
│  │   handoff_reason: "claude_code in WARN (18%)..."       │  │
│  │                                                        │  │
│  │ Agent calls: yagura_session_save({                     │  │
│  │   workspace: "/home/you/yagura", branch: "main",      │  │
│  │   plan_md_step: "Phase 2 — testing",                   │  │
│  │   open_todos: [...], active_files: [...]               │  │
│  │ })                                                      │  │
│  │                                                        │  │
│  │ Agent calls: yagura_handoff(target="windsurf")         │  │
│  │ → yagura: (1) save context, (2) mark Claude Code as    │  │
│  │   SWITCHED, (3) launch Windsurf via OS                 │  │
│  └────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
                          ▼
┌──────────────────────────────────────────────────────────────┐
│  Windsurf launched at the same workspace                     │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ Cascade auto-calls: yagura_session_load()              │  │
│  │ → receives: {workspace, branch, plan_md_step, todos,   │  │
│  │              free_notes} from Claude Code               │  │
│  │                                                        │  │
│  │ Cascade resumes work where Claude Code left off.       │  │
│  │ When Cascade quota also runs low, repeat in reverse.   │  │
│  └────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
```

## Tools reference

| Tool | Purpose |
|---|---|
| `yagura_quota_report` | Report remaining quota for an agent. Source: `manual` / `auto` / `429`. |
| `yagura_agent_status` | Get current state for both agents + recommendation. |
| `yagura_session_save` | Persist current work context (workspace, branch, TODOs, ...). |
| `yagura_session_load` | Read context saved by the previous agent. |
| `yagura_handoff` | Execute full handoff: save → mark switched → launch target IDE. |

## State machine

```
ACTIVE  ──── remaining drops below 20% ────→  WARN
WARN    ──── 429 received or remaining=0 ──→  EXHAUSTED
WARN    ──── recovered above 20% ─────────→  ACTIVE
EXHAUSTED ── handoff executed ─────────────→  SWITCHED
SWITCHED ─── MarkResumed(agent, percent) ──→  ACTIVE / WARN
```

`SWITCHED` is sticky — once an agent is marked switched, subsequent `yagura_quota_report` calls preserve `SWITCHED` until explicit `MarkResumed`. This prevents oscillation when the agent's quota briefly recovers during cooldown.

## Limitations (honest accounting)

1. **No automatic detection of Claude Code quota** — the agent must call `yagura_quota_report` from its own `/usage` output. Anthropic does not expose subscription-tier quota via external API.
2. **No OAuth flow yet** — only Bearer token authentication. OAuth is on the v0.14 roadmap.
3. **No Windsurf Marketplace registration** — install requires manually editing `mcp_config.json`. Marketplace submission requires upstream review.
4. **Windsurf has a 100-tool cap per session.** Yagura exposes 20 tools, well within the limit, but if you stack other MCP servers in Windsurf, watch for the ceiling.
5. **Workspace path defaults to yagura's state dir.** Pass an explicit `workspace` field in `yagura_handoff` for real project handoff.
