# .claude/ — Claude Code Harness Scaffolding

This directory configures Claude Code when working on the yagura repo itself.

## Layout

```
.claude/
├── README.md              # this file
├── settings.json          # permissions + hooks (deterministic)
├── skills/
│   └── yagura/
│       └── SKILL.md       # how to use yagura's 30 MCP tools
└── agents/
    └── yagura-reviewer.md # zero-dep / atomic-write / reproducibility reviewer
```

## Design choices (per Anthropic best practices + Thariq's guidance)

- **CLAUDE.md** is intentionally absent. yagura's hard rules live in `settings.json` hooks (deterministic) and `.claude/agents/yagura-reviewer.md` (review-time check). Advisory rules can be added per-need.
- **Skill description is a trigger condition**, not a summary. Run `yagura_skill_audit` if you edit `SKILL.md`.
- **Subagent body is the SYSTEM PROMPT**, not a user prompt. The "You are a senior reviewer" framing is deliberate.
- **Hooks** target Go-specific tooling (gofmt, go vet) with `${CLAUDE_TOOL_INPUT_FILE_PATH##*.}` suffix-check guard to avoid running on non-Go edits.

## Validating this scaffolding

```sh
# Audit the yagura skill from inside yagura itself:
yagura_skill_audit(content: <contents of .claude/skills/yagura/SKILL.md>)

# Audit the reviewer subagent:
yagura_subagent_audit(content: <contents of .claude/agents/yagura-reviewer.md>)
```

Both should score ≥90.
