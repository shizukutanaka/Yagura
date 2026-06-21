# Quality-Lens System — Specification

Status: **normative** · applies to Yagura v0.72.0+ · ADR-0001 (zero-dependency)

This document specifies the *quality-lens* subsystem: the family of static
analyzers (`internal/{complexity,paramcheck,flagarg,returncheck,errdiscard,…}`)
that Yagura applies to source code — including its own. It is both a contract
for new lenses and the record of the Socratic strength/weakness analysis that
drives the self-improvement loop.

## 1. Purpose

A *lens* answers one focused question about a body of source files, using only
the Go standard library, deterministically, with no type-checking or network
access. Lenses are deliberately narrow: each measures a single axis so that the
`hotspot` lens can later detect where independent axes **converge** (a far
higher-confidence signal than any single lens — see §5).

## 2. Lens contract (normative)

Every lens package MUST satisfy all of the following:

| # | Requirement |
|---|---|
| L1 | Expose `func Scan(files map[string]string, …) Report` — input is `path→content`, never touches the filesystem itself. |
| L2 | Be **deterministic**: identical input ⇒ identical `Report`, including `Findings` order (sort by File→Line→Func). |
| L3 | Use **stdlib only** (`go/ast`, `go/parser`, `regexp`, `strings`, …). No new `go.mod` entries, ever (ADR-0001). |
| L4 | Skip `_test.go` files unless the lens's explicit subject *is* tests (`assertcheck`, `testcoverage`). |
| L5 | Skip `TestXxx`/`BenchmarkXxx`/`ExampleXxx`/`FuzzXxx` function decls when scanning production code. |
| L6 | Never panic on unparseable input — emit a `parse-error` finding (severity `low`) and continue. |
| L7 | Carry a `Severity` (`low`/`medium`/`high`) on each finding, and a `Report` summary with counts. |
| L8 | Name methods uniformly as `(Recv).Method` at the FuncDecl line, so `hotspot` can key findings by `(file, func)` across lenses. |
| L9 | Ship TDD tests (Red→Green): tests written first, never edited to match the implementation. |
| L10 | Be wired as **both** a CLI verb (`yagura <verb> --dir . [--strict]`) and an MCP tool (`yagura_<verb>`). |

## 3. Lens taxonomy (current)

Lenses are organized by the **axis** they measure:

| Axis | Lenses |
|---|---|
| Function signature | `paramcheck` (input width), `flagarg` (bool control-coupling), `returncheck` (output width) |
| Function internals | `complexity` (cyclomatic), `errdiscard` (discarded errors), `errpolicy` (diagnosability) |
| Package structure | `coupling`, `deprank` (in-degree rank), `deadcode` (unreachable unexported) |
| Public contract | `apidoc` (exported-symbol docs), `recvcheck` (receiver consistency) |
| Test trust | `testcoverage` (source↔test mapping), `assertcheck` (assertion density) |
| Meta | `coverage` (scan blind spots), `hotspot` (multi-lens convergence) |

## 4. Strengths (長所)

- **S1 — Composability.** The uniform `Scan` contract (L1) lets `hotspot`
  orchestrate every signature lens with zero per-lens special-casing.
- **S2 — Determinism as a feature.** No flakiness, reproducible builds, and
  results that can be diffed release-to-release (the hotspot before/after proof
  in v0.71→v0.72 depended on this).
- **S3 — Self-application (dogfooding).** Yagura runs every lens on itself; the
  Socratic loop (each lens exposes the next lens's blind spot) is the result.
- **S4 — Zero dependency.** Auditability and supply-chain safety by construction
  (ADR-0001); `go/ast` is sufficient for syntactic axes without type info.
- **S5 — Actionability proven.** v0.70→v0.72 closed the full loop: hotspot found
  3 convergent targets → each was refactored away → hotspot now reports 0.

## 5. Weaknesses (短所)

- **W1 — Syntactic ceiling.** Without type information, lenses cannot see
  cross-package call graphs (`errdiscard` is same-package only) or true types.
- **W2 — Semantic blind spot.** Every lens measures *structure*. None checks
  whether a name matches its behaviour — a function called `isReady` that
  returns an `int`, or `getName` that returns nothing, passes all lenses while
  actively misleading readers. **Naming is an unmeasured contract.**
- **W3 — Threshold arbitrariness.** Per-lens default thresholds (params>5,
  returns>3) are conventions, not derived — defensible but not self-justifying.
- **W4 — Convergence needs population.** `hotspot` is only meaningful on a
  codebase large enough for signals to overlap; on a 3-file project it is noise.

## 6. Improvements (改善点) — prioritized

| Pri | Item | Closes |
|---|---|---|
| **1** | **`namecheck` lens** — name↔signature consistency: predicate names (`is/has/can/should`) must return `bool`; getters (`Get…`) and constructors (`New…`) must return a value. First lens on the *semantic* axis. | **W2** |
| 2 | Optional type-aware mode behind a build tag (would need `go/types`, weigh against ADR-0001). | W1 |
| 3 | Threshold calibration from corpus percentiles rather than fixed constants. | W3 |

Improvement #1 is implemented in this release (see §7). #2 conflicts with
ADR-0001 and is deferred; #3 is catalogued.

## 7. `namecheck` — specification

**Question:** *Does each identifier's name keep the promise its declaration makes?*

Deterministic rules (all use `go/ast`; word boundary = prefix followed by an
uppercase letter or end-of-name, so `Hash` is **not** a `has` predicate and
`Errno` is **not** an `Err` prefix):

### Function-name rules (signature axis)

| Rule | Condition | Severity |
|---|---|---|
| `predicate-not-bool` | name is `is`/`has`/`can`/`should`/`must` predicate, has ≥1 result, but first result type is not `bool` | medium |
| `getter-no-return` | name is `Get`/`get` prefix but function returns nothing | medium |
| `constructor-no-return` | name is `New`/`new` prefix but function returns nothing | low |

### Error-naming rules (Go community standard; errname-derived)

Added v0.74 after Qiita/Zenn survey of established Go static-analysis
conventions identified two rules every well-known linter (`errname`,
`go-ruleguard` recipes) enforces but Yagura did not yet measure.

| Rule | Condition | Severity |
|---|---|---|
| `sentinel-err-prefix` | `var` initialized by `errors.New(…)` / `fmt.Errorf(…)`, or declared with explicit `error` type, whose name does not start with `Err…` (exported) / `err…` (unexported) | medium |
| `error-type-suffix` | type with `Error() string` method (i.e. implements `error`) whose name does not end with `Error` or `Errors` | medium |

Detection is conservative — only `errors.New`/`fmt.Errorf` and explicit
`error`-type annotations trigger the sentinel check; user-defined constructors
(e.g. `NewMyError()`) would require type resolution and are skipped. Same for
the error-type check: only the standard `func (T) Error() string` shape counts.

### Exclusions

`_test.go` files; `TestXxx`/`BenchmarkXxx`/`ExampleXxx`/`FuzzXxx`;
function literals; bare prefix names with no suffix (no word boundary).
Same-package, no type resolution: the first result's type is read syntactically
(`*ast.Ident{Name:"bool"}`), so only the literal predeclared `bool` counts —
a named type aliasing bool is conservatively **not** flagged (avoids false
positives without type info).

This is the first lens to measure the **semantic** axis (W2) and completes the
function-signature picture: `paramcheck` (input), `returncheck` (output),
`flagarg` (coupling), `namecheck` (the name's promise about all three) and now
also the Go community's two well-established error-naming conventions —
catching naming defects that would otherwise survive every structural lens.
