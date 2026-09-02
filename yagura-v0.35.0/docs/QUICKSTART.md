# Quickstart

This guide gets you from zero to a running yagura with one project registered, one alert resolved, and Claude Code observing every tool call — in about five minutes.

## Prerequisites

- A POSIX shell (Linux / macOS) or PowerShell (Windows — see [WINDOWS.md](WINDOWS.md)).
- Go 1.22+ **only if building from source**. Pre-built binaries need nothing.
- A GitHub Personal Access Token with `public_repo` scope.

## 1. Install

```bash
# Linux amd64
curl -L https://github.com/shizukutanaka/yagura/releases/latest/download/yagura-linux-amd64 -o yagura
chmod +x yagura
./yagura --version
```

For other platforms see the [Install section of README](../README.md#install).

## 2. Configure

```bash
export YAGURA_GITHUB_TOKEN=ghp_yourPersonalAccessToken
export YAGURA_STATE_DIR=$HOME/.yagura
# Optional:
# export YAGURA_AUTH_TOKEN=somethingSecret
# export YAGURA_LOG_LEVEL=debug
```

A full reference is in `.env.example`.

## 3. Start

```bash
./yagura
# yagura ready  addr=127.0.0.1:8090 tools=46 state_dir=/home/you/.yagura
```

Leave this running. The next steps use a second shell.

## 4. Verify it is up

```bash
curl -sS http://127.0.0.1:8090/healthz
# ok

curl -sS http://127.0.0.1:8090/.well-known/mcp | jq
# {
#   "name": "yagura",
#   "version": "0.34.0",
#   "protocol": "mcp/2025-11",
#   "endpoints": { … },
#   "capabilities": { "tools": 46, "hook_receiver": true, "alert_lifecycle": true, … }
# }
```

## 5. Register your first project

```bash
curl -sS -X POST http://127.0.0.1:8090/mcp \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc":"2.0","id":1,"method":"tools/call","params":{
      "name":"yagura_register",
      "arguments":{
        "slug":"breeze",
        "repository":"shizukutanaka/breeze",
        "local_path":"/home/you/code/breeze",
        "language":"javascript",
        "priority":5,
        "tags":["messaging","encryption"]
      }
    }
  }' | jq
```

`slug` is yagura's internal name; `repository` is the GitHub `owner/repo`; `local_path` is where the project lives on this machine (used for cwd → project lookup in HTTP hooks); `priority` is 1–10 (used by `yagura_today` to surface the most urgent projects); `tags` are free-form.

## 6. Scan it

The background scanner runs every 24 hours, but you can trigger one immediately:

```bash
curl -sS -X POST http://127.0.0.1:8090/mcp \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc":"2.0","id":2,"method":"tools/call","params":{
      "name":"yagura_health",
      "arguments":{"slug":"breeze"}
    }
  }' | jq
```

This calls OSV.dev and OpenSSF Scorecard, then stores results in `~/.yagura/state/projects/breeze.json`.

## 7. Generate the agent harness artifacts

```bash
for tool in yagura_agents_md yagura_feature_list yagura_init_sh; do
  curl -sS -X POST http://127.0.0.1:8090/mcp \
    -H 'Content-Type: application/json' \
    -d "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{
      \"name\":\"$tool\",
      \"arguments\":{\"slug\":\"breeze\",\"write\":true}
    }}" | jq '.result.content[0].text | fromjson | {filename, written_to}'
done
```

Expected output:

```
{"filename":"AGENTS.md","written_to":"/home/you/code/breeze/AGENTS.md"}
{"filename":"feature-list.json","written_to":"/home/you/code/breeze/feature-list.json"}
{"filename":"init.sh","written_to":"/home/you/code/breeze/init.sh"}
```

If your project has a `Plan.md`, the `AGENTS.md` and `feature-list.json` will include its sections automatically.

For Windows projects, pass `"target":"powershell"` to `yagura_init_sh` to get `init.ps1` instead.

## 8. Connect Claude Code

Edit `~/.claude/settings.json`:

```jsonc
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

If you set `YAGURA_AUTH_TOKEN`, also add the Authorization header:

```jsonc
{ "type": "http", "url": "...", "headers": { "Authorization": "Bearer ${YAGURA_AUTH_TOKEN}" }, "allowedEnvVars": ["YAGURA_AUTH_TOKEN"] }
```

Open a Claude Code session in the `breeze` directory. Run any prompt — say `ls -la`. Then in your other shell:

```bash
curl -sS -X POST http://127.0.0.1:8090/mcp \
  -H 'Content-Type: application/json' \
  -d '{
    "jsonrpc":"2.0","id":4,"method":"tools/call","params":{
      "name":"yagura_hook_stats",
      "arguments":{"slug":"breeze"}
    }
  }' | jq '.result.content[0].text | fromjson'
```

You should see counts for `PreToolUse`, `PostToolUse`, `Stop`, plus the top tools used.

## 9. Open the dashboard

```bash
xdg-open http://127.0.0.1:8090/dashboard    # Linux
open      http://127.0.0.1:8090/dashboard   # macOS
start     http://127.0.0.1:8090/dashboard   # Windows
```

The dashboard is read-mostly — apart from the "Add a project" form (which sends an audited `yagura_register` to the MCP server), every action goes through the MCP server, not a separate browser write path.

## 10. Stop

`Ctrl+C` in the yagura shell. On Windows under NSSM, `nssm stop yagura`.

The process drains for 5 seconds (`/readyz` returns 503) then shuts down gracefully. All state is in `~/.yagura/state/` and will be intact on next start.

---

## What next?

- [MCP_TOOLS.md](MCP_TOOLS.md) — full reference for all 62 tools.
- [WINDOWS.md](WINDOWS.md) — Task Scheduler / NSSM / Windows service setup.
- [ARCHITECTURE.md](../ARCHITECTURE.md) — internals and design rationale.
- [security-spec.md](security-spec.md) — threat model.
- `docs/adr/` — Architecture Decision Records.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `yagura ready` then immediate exit | `YAGURA_STATE_DIR` not writable. Try `$HOME/.yagura` or `$TMPDIR/yagura`. |
| `/mcp` returns 401 | `YAGURA_AUTH_TOKEN` is set but the request lacks `Authorization: Bearer ...`. |
| `unknown tool: yagura_xxx` | Old client cached the tool list. Restart the client; `/tools/list` will refresh. |
| Hooks return non-2xx but Claude Code still works | Hooks are non-blocking by design ([Claude Code docs](https://code.claude.com/docs/en/hooks)). Check `~/.yagura/state/claude_hooks.jsonl`. |
| Scanner shows `quota_exhausted` | GitHub PAT rate limit. `yagura_quota_report` shows current quotas. |
