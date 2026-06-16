#!/usr/bin/env bash
# Regenerate docs/MCP_TOOLS.md from the live tools/list endpoint of a freshly
# spawned yagura daemon. Idempotent.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin/yagura"
OUT="$ROOT/docs/MCP_TOOLS.md"

if [ ! -x "$BIN" ]; then
  echo "build the binary first: make build" >&2
  exit 1
fi

# Spawn ephemeral daemon on a free localhost port
PORT=$((20000 + RANDOM % 10000))
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

YAGURA_GITHUB_TOKEN=ghp_dummy \
YAGURA_STATE_DIR="$TMP" \
YAGURA_ADDR="127.0.0.1:$PORT" \
  "$BIN" > "$TMP/log" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null || true; rm -rf "$TMP"' EXIT

# Wait for readiness (max 5 s)
for i in $(seq 1 50); do
  if curl -sS -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/healthz" 2>/dev/null | grep -q 200; then
    break
  fi
  sleep 0.1
done

curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  "http://127.0.0.1:$PORT/mcp" > "$TMP/tools.json"

python3 - "$TMP/tools.json" "$OUT" <<'PYEOF'
import json, sys
with open(sys.argv[1]) as f:
    r = json.load(f)
tools = r['result']['tools']

def categorize(name):
    rest = name.split('_', 1)[1] if '_' in name else name
    if rest in ('list','get','search','today','stats','register','unregister','update'): return 'Inventory'
    if rest in ('vulns','scorecard','secretscan','sbom','gha_audit','pin_drift','ai_verify','test_audit','quality_check','health'): return 'Security (sensors)'
    if rest in ('ast_check','complexity','coupling','api_doc','dead_code','recv_check','assert_check','err_policy','code_health'): return 'Code quality (guides)'
    if rest in ('agents_md','feature_list','progress_file','init_sh','harness_recommend','skill_audit','subagent_audit','harness_coverage'): return 'Harness (guides)'
    if rest.startswith('alert'): return 'Alerts'
    if rest in ('plan_status','release_radar'): return 'Plan tracking'
    if 'handoff' in rest or rest in ('plan_lock','run_lock','run_release','run_status'): return 'Handoff'
    if rest in ('hook_timeline','hook_stats','agent_status','quota_report'): return 'Observability'
    if 'graph' in rest or rest in ('depends_on','impact','reverse_deps'): return 'Graph'
    return 'Misc'

by_cat = {}
for t in tools:
    by_cat.setdefault(categorize(t['name']), []).append(t)

out = ["# MCP tools reference", "",
       f"Generated from a live yagura — **{len(tools)} tools**.", "",
       "Tools are tagged `[G]` (guide / feedforward) or `[S]` (sensor / feedback) per the [Fowler harness taxonomy](https://martinfowler.com/articles/harness-engineering.html).", "",
       "---", "", "## Table of contents", ""]
for cat in sorted(by_cat):
    out.append(f"- [{cat}](#{cat.lower().replace(' ', '-').replace('(','').replace(')','')}) ({len(by_cat[cat])})")
out += ["", "---", ""]
for cat in sorted(by_cat):
    out.append(f"## {cat}\n")
    for t in sorted(by_cat[cat], key=lambda x: x['name']):
        out.append(f"### `{t['name']}`\n")
        out.append((t.get('description','') or "(no description)") + "\n")
        props = t.get('inputSchema', {}).get('properties', {})
        required = set(t.get('inputSchema', {}).get('required', []))
        if props:
            out.append("**Arguments:**\n")
            out.append("| Name | Required | Description |")
            out.append("|---|---|---|")
            for k in sorted(props):
                v = props[k]
                req = '★' if k in required else ''
                out.append(f"| `{k}` ({v.get('type','')}) | {req} | {v.get('description','')} |")
            out.append("")
        out.append("---\n")
out += ["## Generation", "",
        "Auto-generated from `tools/list`. Regenerate with `make docs-mcp`.", ""]

with open(sys.argv[2], 'w') as f:
    f.write('\n'.join(out))
print(f"wrote {len(tools)} tools", file=sys.stderr)
PYEOF

echo "✓ regenerated $OUT"
