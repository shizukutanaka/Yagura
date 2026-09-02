// Package harness は Claude Code ハーネスエンジニアリングの best practices を
// 構造化データ + scaffolding generator として提供する。
//
// 動機 (v0.19.0):
//
//	Anthropic / Fowler / cortex(aircloset)が confirm した知見:
//	  - CLAUDE.md = advisory (≤60 行で hard-rule は hook へ移譲)
//	  - skill description = trigger condition (NOT summary)
//	  - skill Gotchas section = highest-signal content
//	  - 一般知識を再述しない / 既定挙動の divergence を書く
//	  - subagent body = system prompt (NOT user prompt) ← #1 misunderstanding
//	  - hooks = deterministic (lint / format / 危険 cmd block)
//	  - permissions = allow/deny/ask 明示で逐次確認を回避
//
// yagura は portfolio orchestrator の立場で、各 project 用 .claude/ scaffolding を
// 生成・audit する MCP tools の土台を提供する。
//
// 設計判断:
//   - ゼロ依存(stdlib のみ)
//   - 言語別テンプレートは Go/TS/Python/Rust に対応、それ以外は generic
//   - audit は heuristic ベース(LLM 判定は client 側に任せる)
package harness

import (
	"fmt"
	"strings"
)

// Recommendation は project に対する .claude/ scaffolding 一式。
//
// 「全部この通りに作れ」ではなく「starting point」として返す。
// クライアントは編集して .claude/ 配下に save するか、yagura に
// commit を任せる(未実装、v0.20+ 候補)。
type Recommendation struct {
	Language     string             `json:"language"`
	ClaudeMd     string             `json:"claude_md"`     // CLAUDE.md content (≤60 lines)
	SettingsJSON string             `json:"settings_json"` // .claude/settings.json (hooks + permissions)
	Skills       []SkillTemplate    `json:"skills"`        // 推奨 skill list
	Subagents    []SubagentTemplate `json:"subagents"`     // 推奨 subagent list
	Citations    []string           `json:"citations"`     // 参考ドキュメント(Anthropic 公式優先)
}

// SkillTemplate は単一 skill のスケルトン(SKILL.md content + 配置先 path)。
type SkillTemplate struct {
	Path        string `json:"path"`        // 例: .claude/skills/lint/SKILL.md
	Description string `json:"description"` // YAML frontmatter description (trigger 形式)
	Body        string `json:"body"`        // markdown body
}

// SubagentTemplate は単一 subagent のスケルトン。
type SubagentTemplate struct {
	Path        string   `json:"path"` // 例: .claude/agents/security-reviewer.md
	Name        string   `json:"name"`
	Description string   `json:"description"` // trigger 形式("Use proactively when ...")
	Tools       []string `json:"tools"`       // allowlist
	Model       string   `json:"model"`       // sonnet / haiku / opus / inherit
	Body        string   `json:"body"`        // system prompt(NOT user prompt!)
}

// RecommendForLanguage は言語に応じた scaffolding 一式を返す。
//
// 未対応言語は generic template を返す(壊れない fallback)。
func RecommendForLanguage(lang string) Recommendation {
	lang = strings.ToLower(strings.TrimSpace(lang))
	switch lang {
	case "go", "golang":
		return goRecommendation()
	case "typescript", "ts":
		return tsRecommendation()
	case "javascript", "js":
		return jsRecommendation()
	case "python", "py":
		return pythonRecommendation()
	case "rust", "rs":
		return rustRecommendation()
	default:
		return genericRecommendation(lang)
	}
}

// ─── Go (yagura, Otedama, Cotton, Rope) ──────────────────────

func goRecommendation() Recommendation {
	return Recommendation{
		Language: "go",
		ClaudeMd: strings.TrimSpace(`
# Project: Go

## Why
[fill in: 1-2 lines, why this project exists]

## Rules (hard)
- Zero external dependencies (ADR-0001) unless explicitly justified
- All exported funcs have docstrings (godoc-compatible)
- 1 function = 1 responsibility, ≤40 lines, ≤3 args

## DON'T
- Don't add deps without ADR
- Don't use ` + "`panic`" + ` outside main(); return error values
- Don't ignore errors with ` + "`_ =`" + ` except in defer close()

## Test
- ` + "`go test -race -count=1 ./...`" + ` must be green
- Coverage ≥80% on all internal/ packages

## Build
- ` + "`make build`" + ` (reproducible: CGO_ENABLED=0 + -trimpath + -buildvcs=false)
`),
		SettingsJSON: strings.TrimSpace(`
{
  "permissions": {
    "deny": ["Bash(rm -rf*)", "Bash(git push --force*)", "Bash(go clean -modcache)"],
    "ask":  ["Write", "Edit", "Bash"]
  },
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write|MultiEdit",
        "hooks": [
          {"type": "command", "command": "gofmt -w \"$CLAUDE_TOOL_INPUT_FILE_PATH\""},
          {"type": "command", "command": "go vet ./..."}
        ]
      }
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "go test -race -count=1 ./..."}]}
    ]
  }
}
`),
		Skills: []SkillTemplate{
			{
				Path:        ".claude/skills/go-test/SKILL.md",
				Description: "Use when the user asks to add tests, fix failing tests, or improve test coverage in a Go project. Triggers on phrases like 'write a test for', 'fix TestXxx', 'increase coverage'.",
				Body: strings.TrimSpace(`
# Go Test Skill

Run ` + "`go test -race -count=1 ./...`" + ` for full coverage.

## Patterns
- Use ` + "`t.TempDir()`" + ` for filesystem isolation
- Use ` + "`atomic.Pointer[time.Time]`" + ` for race-free time hooks
- Mock external dependencies via interface; never inject real HTTP clients

## Gotchas
- Race conditions surface only with ` + "`-race`" + `; never trust without it
- ` + "`time.Now()`" + ` in tests must go through ` + "`NowFn`" + ` hook
- ` + "`go test ./...`" + ` without ` + "`-count=1`" + ` uses cache → false greens
- ` + "`t.Parallel()`" + ` requires capture of loop variable: ` + "`tc := tc; t.Run(...)`" + `
`),
			},
		},
		Subagents: []SubagentTemplate{
			{
				Path:        ".claude/agents/go-reviewer.md",
				Name:        "go-reviewer",
				Description: "Expert Go code reviewer. Use proactively after any commit or PR to check for race conditions, error handling, and idiomatic style.",
				Tools:       []string{"Read", "Grep", "Glob", "Bash"},
				Model:       "sonnet",
				Body: strings.TrimSpace(`
You are a senior Go reviewer. Focus on:

1. Race conditions — check goroutines / channels / shared state.
2. Error handling — never silently ignore errors; wrap with %w for chains.
3. Resource cleanup — defer Close() must be paired with non-nil check.
4. Idiomatic style — Pike's "errors are values"; small interfaces; no unnecessary getters.

For each issue: cite line, explain why, suggest fix. Don't modify files yourself.
`),
			},
		},
		Citations: []string{
			"https://www.anthropic.com/engineering/claude-code-best-practices",
			"https://code.claude.com/docs/en/sub-agents",
			"https://martinfowler.com/articles/harness-engineering.html",
		},
	}
}

// ─── TypeScript (NovaEdit, Strawberry, Breeze SDK) ───────────

func tsRecommendation() Recommendation {
	return Recommendation{
		Language: "typescript",
		ClaudeMd: strings.TrimSpace(`
# Project: TypeScript

## Why
[fill in]

## Rules (hard)
- ` + "`strict: true`" + ` in tsconfig.json, no exceptions
- No ` + "`any`" + ` / ` + "`as any`" + ` / ` + "`@ts-ignore`" + ` without inline justification
- No ` + "`eslint-disable`" + ` lines without reason comment

## DON'T
- Don't add ` + "`console.log`" + `; use a centralized logger
- Don't hardcode URLs / keys; use env vars
- Don't create CSS/SCSS files; use Tailwind utilities

## Test
- ` + "`pnpm test`" + ` (vitest) + ` + "`pnpm tsc --noEmit`" + `
- Coverage ≥80% on src/

## Build
- ` + "`pnpm build`" + `
`),
		SettingsJSON: strings.TrimSpace(`
{
  "permissions": {
    "deny": ["Bash(rm -rf*)", "Bash(git push --force*)", "Bash(npm publish*)"],
    "ask":  ["Write", "Edit", "Bash"]
  },
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write|MultiEdit",
        "hooks": [
          {"type": "command", "command": "pnpm prettier --write \"$CLAUDE_TOOL_INPUT_FILE_PATH\""},
          {"type": "command", "command": "pnpm tsc --noEmit"}
        ]
      }
    ]
  }
}
`),
		Skills: []SkillTemplate{
			{
				Path:        ".claude/skills/tsc-strict/SKILL.md",
				Description: "Use when the user encounters TypeScript errors with strict mode, asks to remove 'any' types, or asks to fix tsc compile errors.",
				Body: strings.TrimSpace(`
# TypeScript Strict Skill

## Patterns
- Replace ` + "`any`" + ` with ` + "`unknown`" + ` + narrowing
- Use ` + "`satisfies`" + ` for const assertions with type checking
- Discriminated unions over ` + "`as`" + ` casts

## Gotchas
- ` + "`Object.keys(x)`" + ` returns ` + "`string[]`" + ` not ` + "`keyof typeof x`" + ` — cast carefully
- Array indexed access can be undefined under ` + "`noUncheckedIndexedAccess`" + `
- ` + "`Record<K, V>`" + ` lookups return ` + "`V | undefined`" + ` under strict
`),
			},
		},
		Subagents: []SubagentTemplate{
			{
				Path:        ".claude/agents/ts-reviewer.md",
				Name:        "ts-reviewer",
				Description: "TypeScript code reviewer focused on strict-mode compliance and security. Use proactively after edits to .ts/.tsx files.",
				Tools:       []string{"Read", "Grep", "Glob"},
				Model:       "sonnet",
				Body: strings.TrimSpace(`
You are a senior TypeScript reviewer. Check for:

1. Type safety — no ` + "`any`" + ` / ` + "`as`" + ` / ` + "`@ts-ignore`" + ` without justification.
2. Null safety — strictNullChecks; no ` + "`!`" + ` non-null assertions on user input.
3. Security — no hardcoded secrets; sanitize HTML; validate inputs.
4. React/Vue specific — no missing dependency in useEffect; no key={index}; controlled components.

Report findings only. Never modify code.
`),
			},
		},
		Citations: []string{
			"https://www.anthropic.com/engineering/claude-code-best-practices",
			"https://zenn.dev/aircloset/articles/d416342f46f16b",
		},
	}
}

// ─── JavaScript (legacy / no-types) ──────────────────────────

func jsRecommendation() Recommendation {
	r := tsRecommendation()
	r.Language = "javascript"
	r.ClaudeMd = strings.Replace(r.ClaudeMd, "TypeScript", "JavaScript", -1)
	return r
}

// ─── Python (Tessera, Kaya, NovaEdit-legacy, Forge3D) ────────

func pythonRecommendation() Recommendation {
	return Recommendation{
		Language: "python",
		ClaudeMd: strings.TrimSpace(`
# Project: Python

## Why
[fill in]

## Rules (hard)
- Type hints on all public functions (mypy --strict compatible)
- f-strings, no ` + "`%`" + ` / ` + "`.format()`" + ` outside legacy code
- Single-file design preferred (stdlib only, ADR-0001 if dep added)

## DON'T
- Don't use ` + "`print()`" + ` in library code; use logging
- Don't catch bare ` + "`except:`" + ` — name the exception
- Don't ` + "`from X import *`" + ` outside __init__.py

## Test
- ` + "`pytest -x --cov=src --cov-fail-under=80`" + `
- ` + "`ruff check .`" + ` clean
- ` + "`mypy --strict src/`" + ` clean

## Build
- ` + "`python -m build`" + ` if PyPI package, else single-file
`),
		SettingsJSON: strings.TrimSpace(`
{
  "permissions": {
    "deny": ["Bash(rm -rf*)", "Bash(git push --force*)", "Bash(pip install*)"],
    "ask":  ["Write", "Edit", "Bash"]
  },
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write|MultiEdit",
        "hooks": [
          {"type": "command", "command": "ruff format \"$CLAUDE_TOOL_INPUT_FILE_PATH\""},
          {"type": "command", "command": "ruff check --fix \"$CLAUDE_TOOL_INPUT_FILE_PATH\""}
        ]
      }
    ]
  }
}
`),
		Skills: []SkillTemplate{
			{
				Path:        ".claude/skills/python-test/SKILL.md",
				Description: "Use when adding pytest tests, fixing pytest failures, or increasing Python test coverage. Triggers on 'pytest', 'add a test', 'mock'.",
				Body: strings.TrimSpace(`
# Python Test Skill

Use pytest fixtures over unittest.TestCase setUp.

## Patterns
- ` + "`tmp_path`" + ` fixture for filesystem isolation
- ` + "`monkeypatch.setattr`" + ` over manual mock
- Parametrize edge cases: ` + "`@pytest.mark.parametrize`" + `

## Gotchas
- ` + "`mocker.patch`" + ` needs full import path of where it's _used_, not _defined_
- Fixture scope defaults to function; use ` + "`scope='module'`" + ` for DB setup
- ` + "`assert x == y`" + ` pytest rewrites; explicit ` + "`assertEqual`" + ` doesn't
`),
			},
		},
		Subagents: []SubagentTemplate{
			{
				Path:        ".claude/agents/py-reviewer.md",
				Name:        "py-reviewer",
				Description: "Python code reviewer for type hints, error handling, and PEP 8. Use proactively after .py edits.",
				Tools:       []string{"Read", "Grep", "Glob"},
				Model:       "sonnet",
				Body: strings.TrimSpace(`
Senior Python reviewer. Check:

1. Type hints — public APIs, return types, generic parameters
2. Exception handling — never bare except; preserve cause with ` + "`raise X from Y`" + `
3. Resource cleanup — context managers (` + "`with`" + `) over manual close
4. Security — no ` + "`pickle`" + ` on untrusted data; no shell=True with user input

Report only. Don't modify.
`),
			},
		},
		Citations: []string{
			"https://www.anthropic.com/engineering/claude-code-best-practices",
		},
	}
}

// ─── Rust (IZANAGI, Rope, Breeze-LD) ─────────────────────────

func rustRecommendation() Recommendation {
	return Recommendation{
		Language: "rust",
		ClaudeMd: strings.TrimSpace(`
# Project: Rust

## Why
[fill in]

## Rules (hard)
- ` + "`#![deny(warnings)]`" + ` at crate root, no exceptions
- ` + "`cargo clippy -- -D warnings`" + ` clean
- ` + "`unsafe`" + ` blocks require // SAFETY: justification

## DON'T
- Don't ` + "`.unwrap()`" + ` outside tests / main()
- Don't ` + "`.clone()`" + ` when you can borrow
- Don't use ` + "`Box<dyn Error>`" + ` in library code; define proper errors

## Test
- ` + "`cargo test --all-features`" + ` clean
- ` + "`cargo clippy --all-targets`" + ` clean
`),
		SettingsJSON: strings.TrimSpace(`
{
  "permissions": {
    "deny": ["Bash(rm -rf*)", "Bash(git push --force*)", "Bash(cargo publish*)"],
    "ask":  ["Write", "Edit", "Bash"]
  },
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Edit|Write|MultiEdit",
        "hooks": [
          {"type": "command", "command": "cargo fmt --quiet"},
          {"type": "command", "command": "cargo clippy --quiet -- -D warnings"}
        ]
      }
    ]
  }
}
`),
		Skills: []SkillTemplate{},
		Subagents: []SubagentTemplate{
			{
				Path:        ".claude/agents/rust-reviewer.md",
				Name:        "rust-reviewer",
				Description: "Rust reviewer for ownership, lifetimes, and unsafe. Use proactively after .rs edits.",
				Tools:       []string{"Read", "Grep", "Glob"},
				Model:       "sonnet",
				Body: strings.TrimSpace(`
Senior Rust reviewer. Check:

1. Ownership — unnecessary ` + "`.clone()`" + `, missing ` + "`&`" + ` borrows
2. Lifetimes — minimize ` + "`'static`" + `, prefer elision
3. Unsafe — every ` + "`unsafe`" + ` block needs SAFETY comment
4. Errors — typed errors over ` + "`Box<dyn Error>`" + `; thiserror in libraries

Report only. Don't modify.
`),
			},
		},
		Citations: []string{
			"https://www.anthropic.com/engineering/claude-code-best-practices",
		},
	}
}

// ─── Generic fallback ───────────────────────────────────────

func genericRecommendation(lang string) Recommendation {
	return Recommendation{
		Language: lang,
		ClaudeMd: fmt.Sprintf(strings.TrimSpace(`
# Project: %s

## Why
[fill in]

## Rules (hard)
- All public APIs documented
- No magic numbers; use named constants

## DON'T
- Don't hardcode secrets; use env vars
- Don't catch all exceptions silently

## Test / Build
- [fill in build/test commands for your language]
`), lang),
		SettingsJSON: strings.TrimSpace(`
{
  "permissions": {
    "deny": ["Bash(rm -rf*)", "Bash(git push --force*)"],
    "ask":  ["Write", "Edit", "Bash"]
  }
}
`),
		Skills:    []SkillTemplate{},
		Subagents: []SubagentTemplate{},
		Citations: []string{
			"https://www.anthropic.com/engineering/claude-code-best-practices",
		},
	}
}
