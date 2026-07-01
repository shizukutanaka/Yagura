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
| Function-body readability | `nakedret` (naked return in long named-result functions) |
| Correctness / shadowing | `predeclared` (declarations shadowing Go builtins) |
| Panic safety | `typeassert` (single-value type assertions that panic on mismatch) |
| Function internals | `complexity` (cyclomatic / breadth), `nestdepth` (max nesting / depth), `errdiscard` (discarded errors), `errpolicy` (diagnosability) |
| Cognitive load | `cognit` (cognitive complexity — nesting-weighted human reading cost; synthesis of breadth × depth) |
| Performance | `prealloc` (slices grown by append in a range loop without preallocation) |
| Error-chain integrity | `errwrap` (`%w` / `errors.Is` / `errors.As`; errorlint-style) |
| Package structure | `coupling`, `deprank` (in-degree rank), `deadcode` (unreachable unexported) |
| Public contract | `apidoc` (exported-symbol docs), `recvcheck` (receiver consistency) |
| Test trust | `testcoverage` (source↔test mapping), `assertcheck` (assertion density), `thelper` (test-helper `t.Helper()` hygiene) |
| Concurrency | `ctxcheck` (`context.Context` first-param + no struct field), `synccheck` (sync-lock copy discipline), `globalcheck` (mutable global state) |
| Interface design | `ifacebloat` (named interfaces with too many methods; interfacebloat-style) |
| Meta | `coverage` (scan blind spots), `hotspot` (multi-lens convergence), `calibrate` (corpus-derived thresholds) |

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
  *Addressed v0.80 by the `calibrate` meta-lens (§13), which derives
  corpus-specific percentile distributions so thresholds can be data-driven.*
- **W4 — Convergence needs population.** `hotspot` is only meaningful on a
  codebase large enough for signals to overlap; on a 3-file project it is noise.
- **W5 — Synthesis staleness (self-referential blind spot).** `hotspot` (§4)
  was wired to exactly the 4 lenses that existed at its v0.70 launch
  (complexity/paramcheck/flagarg/returncheck). By v0.94 the lens roster had
  tripled to 21, but hotspot's convergence pool was never revisited — its own
  most valuable feature (multi-signal confidence) silently decayed to 19%
  coverage of the available signals, and repo-wide convergence dropped to 0
  reported hotspots even as newer single lenses (`cognit`, `nestdepth`, etc.)
  kept finding real issues in isolation. A synthesis lens is itself subject to
  the same drift every other lens targets — a *meta*-Socratic blind spot: who
  audits the auditor's completeness? *Addressed v0.95 — hotspot now unions 12
  lenses (adding cognit/nestdepth/typeassert/namecheck/ctxcheck/errwrap/
  nakedret/prealloc, all sharing the `(Recv).Method` func-key convention).
  Dogfood: repo-wide convergent hotspots jumped from 0 to 69 (13 high-severity)
  on the same codebase — proof the population had been undercounted, not that
  the codebase had gotten worse.*

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

## 8. `ctxcheck` — specification

**Question:** *Does `context.Context` flow the way Go convention requires?*

Added v0.75 after a Qiita/Zenn survey of Go concurrency conventions identified
two canonical, deterministically-checkable rules (`containedctx` linter + the
golint "context first arg" check) that no Yagura lens measured. This is the
first lens on the **concurrency / cancellation-propagation** axis.

| Rule | Condition | Severity |
|---|---|---|
| `context-not-first` | a func/method has a `context.Context` parameter that is not in position 0 | medium |
| `contained-ctx` | a struct type has a field (named or embedded) of type `context.Context` | low |

**Exception** (`context-not-first`): if the first parameter is `*testing.T`,
`*testing.B`, or `*testing.F`, the function is a test helper and is exempt — the
canonical carve-out every implementation of this rule includes.

**Conservative detection**: only the literal selector `context.Context` (the
standard import name) is matched. An aliased import (`import ctxpkg "context"`)
would need type resolution and is deliberately **not** flagged — consistent with
the lens family's zero-`go/types` stance (no false positives without type info).

Rationale for `contained-ctx`: the Go blog *Contexts and structs* establishes
that contexts should be passed explicitly per-call as the first argument, never
stashed in a struct, so that cancellation scope is visible at every call site
rather than hidden in object lifetime. Storing a context in a struct couples the
context's lifetime to the object's and obscures who can cancel what.

## 9. `errwrap` — specification

**Question:** *When code wraps or inspects an error, does the error chain survive?*

Added v0.76 after a Qiita/Zenn survey of Go 1.13 error-handling conventions
identified the three checks standardized by `polyfloyd/go-errorlint`. Where
`errpolicy` measures the *rate* of wrapping (discipline meta-metric), `errwrap`
measures *correctness*: whether `errors.Is`/`errors.As` will actually traverse
the wrapped chain. This is the **error-chain integrity** axis.

| Rule | Condition | Severity |
|---|---|---|
| `non-wrapping-verb` | `fmt.Errorf` formats an error value with `%v`/`%s` (no `%w` anywhere in the format) | medium |
| `err-value-compare` | an error is compared with `==`/`!=` against a sentinel (not `nil`) | medium |
| `err-type-assert` | a type assertion `err.(T)` is performed on an error (not `err.(type)`) | medium |

**Type-free heuristics** (consistent with `errpolicy`): an expression is treated
as an error if it is the identifier `err`, ends in `Err`/`err` (e.g. `readErr`),
or is an `.Err` selector. A sentinel is an identifier/selector named `Err…` or
containing `EOF`. `err == nil`/`err != nil` are idiomatic and exempt; a
non-literal format string is not analyzed; `%w` already present means the call is
already wrapping and is not flagged.

Why all three matter: once any layer wraps an error with `%w`, a downstream
`err == io.EOF` silently becomes false and `err.(*MyError)` silently fails —
both are latent bugs that surface only after someone *adds* wrapping elsewhere.
`errors.Is`/`errors.As` are wrap-transparent and never regress this way.

**Dogfood note**: errwrap's first run on Yagura found 14 such latent risks — 13
`err.(scanner.ErrorList)` assertions in the lens packages' own parse-error
handlers and 1 `err == io.EOF` — all converted to `errors.As`/`errors.Is`. The
lens that checks error chains hardened the error chains of every other lens.

## 10. `synccheck` — specification

**Question:** *Are types that own a mutex ever copied?*

Added v0.77 after a Qiita/Zenn survey of Go's `go vet copylocks` conventions.
A type that embeds or contains `sync.Mutex` (or `RWMutex`/`WaitGroup`/`Once`/
`Cond`) must not be copied — copying produces a fresh, unrelated lock and
silently breaks the invariant the mutex was supposed to protect. `go vet`
enforces this; Yagura's ADR-0001 forbids shelling out to `go vet`, so the same
check is implemented natively as a lens.

| Rule | Condition | Severity |
|---|---|---|
| `mutex-value-receiver` | a method has a value receiver on a struct that (directly or one-hop transitively) contains a known lock type | high |
| `mutex-by-value-param` | a function/method parameter passes a lock-bearing type by value | high |
| `mutex-by-value-return` | a function/method returns a lock-bearing type by value | medium |

**Detection** (two-pass, type-free):

1. **Pass 1 — Collect lock-bearing types.** Walk every `TypeSpec` in the file
   set. Seed the set with structs that have a field of type `sync.Mutex`,
   `sync.RWMutex`, `sync.WaitGroup`, `sync.Once`, or `sync.Cond` (matched as
   literal selectors). Apply a fixed-point pass: a struct whose field type is
   an already-known lock-bearing struct (same package) joins the set. Single
   hop only — deeper transitivity is not chased (avoids false positives).
2. **Pass 2 — Apply rules.** Walk every `FuncDecl` and check receiver, params,
   and results against the lock-bearing set.

**Conservative scope**: aliased `sync` imports (`import s "sync"`) are not
matched (no `go/types`). Multi-hop transitivity is not chased. Both consequences
trade recall for precision in line with the rest of the lens family.

**Dogfood note**: synccheck's first run on Yagura found **0 violations** across
283 files and 21 lock-bearing structs — Yagura was already strict about pointer
receivers on every locked type. Unlike v0.76 (where errwrap found 14 latent
defects), v0.77's value is making that absence-of-defects measurable: a future
patch that adds a value receiver to a mutex-bearing struct now fails CI.

## 11. `nakedret` — specification

**Question:** *In a long function, can a reader tell what `return` returns?*

Added v0.78 after a Qiita/Zenn survey of Go's named-return / naked-return
conventions, mechanized by `alexkohler/nakedret`. Where `returncheck` measures
the *width* of the result signature, `nakedret` measures how the body *uses* it:
a bare `return` (no operands) is only legal when results are named, and in a
short function it is fine — but in a long function the reader must scroll back to
the signature and mentally track each named result's current value to know what
is actually returned. That is a readability and latent-bug risk.

| Rule | Condition | Severity |
|---|---|---|
| `naked-return-long-func` | a `return` with no operands, inside a function/closure that has named results and spans more than the threshold (default 30) lines | medium |

**Detection** (recursive, type-free): each `FuncDecl` body is analyzed; nested
`FuncLit` closures are analyzed independently so a naked return binds to its
**innermost** enclosing function — a long outer function does not implicate a
naked return inside a short closure, and vice versa. Line span is measured from
the body's opening to closing brace. Naked returns only occur in named-result
functions (an unnamed-result naked return is a compile error), so the named-result
check alone scopes the rule precisely. `_test.go` files are skipped.

The threshold is configurable (`--max-lines`, MCP `max_lines`); the default of 30
matches `nakedret`'s default.

**Dogfood note**: at the default threshold Yagura reports **0** issues across 285
files. It does contain two naked returns — in an 11-line and a 27-line function —
both comfortably under 30, which is exactly the line the convention draws:
naked returns are a readability tool for short functions, a hazard for long ones.
`naked-ret --strict` now blocks any future long-function naked return in CI.

## 12. `predeclared` — specification

**Question:** *Does any declaration silently mask a Go builtin?*

Added v0.79 after a Qiita/Zenn survey of classic Go pitfalls highlighted
shadowing of predeclared identifiers, mechanized by `nishanths/predeclared`.
Go permits redeclaring any of its 39 predeclared identifiers — `len`, `cap`,
`new`, `error`, `string`, and (since Go 1.21) `min`/`max`/`clear`. A local
`cap := capacity` makes the builtin `cap(s)` uncallable in that scope; a later
edit that "obviously" calls the builtin instead silently reads the variable.

| Rule | Condition | Severity |
|---|---|---|
| `shadow-predeclared` | a declaration whose name equals a predeclared identifier | high for `function`/`type`/`constant`, medium for `variable`/`parameter`/`result` |

**Scope of declarations checked**: function/method parameters, named results,
top-level function names, type/const/var declarations, `:=` short declarations,
and `for range` key/value. The blank identifier `_` is skipped. **Methods**
(receiver-bearing `FuncDecl`) are *not* flagged — a method `len()` is namespaced
by its receiver and never shadows the builtin (matching the canonical linter's
default). An `ignore` list (`--ignore cap,min,max`, MCP `ignore`) suppresses
chosen identifiers, since `min`/`max`/`cap` as locals predate their builtin
status and some teams accept them.

**Dogfood note**: predeclared's first run on Yagura found **20** real
shadowings — every one a `cap`, `min`, or `max` local that became a builtin in
Go 1.21, spread across 9 files (`cli.go` threshold vars, `cli_format.go` row
caps, severity-filter helpers, `opsrisk`, `astcheck/surface`). All 20 were
renamed (`cap`→`maxRows`/`capName`/`capLower`, `min`→`minSev`/`minScore`/
`minRisk`, `max`→`maxThreshold`/`maxVal`); the lens now reports 0. This is the
fifth release where a new lens found and fixed genuine defects on first run
(after v0.73 namecheck, v0.75 ctxcheck, v0.76 errwrap, v0.77's clean guard).

## 13. `calibrate` — specification

**Question:** *Are the numeric `--max` thresholds right for THIS codebase?*

Added v0.80 to address **W3**. The numeric lenses gate on conventional
constants — `complexity --max 10`, `param-check --max 5`, `return-check --max 3`,
`naked-ret --max-lines 30`. These are community defaults, not values derived
from the code under analysis. `calibrate` is a **meta-lens**: it emits no
findings, only the empirical distribution of each metric, so a project can set
thresholds from its own data.

For every **named function** (top-level `FuncDecl` and methods; `FuncLit`
closures excluded), `calibrate` computes four metrics:

| Metric | Definition | Mapped gate (default) |
|---|---|---|
| `complexity` | McCabe cyclomatic (same decision-point set as the `complexity` lens) | `complexity --max 10` |
| `params` | parameter count, name-unit (`a, b int` = 2), variadic = 1, receiver excluded | `param-check --max 5` |
| `returns` | result count, name-unit | `return-check --max 3` |
| `func_lines` | body line span (`{` … `}`) | `naked-ret --max-lines 30` |

Each metric's `Distribution` reports `Min/Max/Mean/Median/P25/P75/P90/P95/P99`,
the current gate default, the number of functions strictly above it
(`OverCurrentDefault`), and a `SuggestedThreshold = ceil(P95)` — a data-driven
gate that would flag roughly the worst 5% of the corpus. Percentiles use linear
interpolation (R-7). `_test.go` files and `TestXxx`/`BenchmarkXxx`/`ExampleXxx`/
`FuzzXxx` are excluded.

### Outlier identification (v0.81)

Distributions describe; they do not point at specific functions. v0.81 adds an
`Outliers` list that makes calibrate **actionable**: a function is an outlier on
a metric when its value exceeds **both**

1. the **Tukey far-out fence** `UpperFence = Q3 + 3·IQR` (the *outer* fence,
   for genuinely extreme values — the inner 1.5·IQR fence floods the upper
   quartile on a large corpus), **and**
2. the metric's **conventional gate** (`CurrentDefault`).

The conjunction is deliberate. Low-cardinality metrics (`returns`, `params`
cluster at 0/1/2) have near-zero IQR, so the Tukey fence alone would flag
idiomatic `(T, error)` returns; requiring the value to also beat the community
baseline removes that noise. Outliers are sorted deterministically
(metric → value desc → file → line → func).

**Dogfood note**: on Yagura (1280 functions) calibrate showed `complexity` p95=13
(default `--max 10` is slightly stricter than the corpus's 95th percentile),
`params` p95≈3 (default 5 is lenient — 4 would fit), `returns` p95=2 (default 3
is well-calibrated), and `func_lines` p95=65 with a 543-line outlier. The outlier
list surfaced **41** functions worth review — the 543-line `cmd/yagura/main.go:run`,
the complexity-32 `plantracker.Parse`, and (notably) three param-count outliers in
the lens code itself (`nakedret.analyzeFunc`, `predeclared.emitIfShadow`,
`errwrap.emit`), catalogued for follow-up. This is the first **meta** lens whose
deliverable is calibration insight rather than defects or convergence —
completing the spec's W1–W4 weakness review by turning W3 from an open caveat
into a measurable, tunable, and now actionable quantity.

**Dogfood note**: on Yagura (1277 functions) calibrate showed `complexity` p95=13
(default `--max 10` is slightly stricter than the corpus's 95th percentile),
`params` p95≈3 (default 5 is lenient — 4 would fit), `returns` p95=2 (default 3
is well-calibrated), and `func_lines` p95=65 with a 543-line outlier. This is
the first **meta** lens whose deliverable is calibration insight rather than
defects or convergence — completing the spec's W1–W4 weakness review by turning
W3 from an open caveat into a measurable, tunable quantity.

## 14. `regress` — specification (temporal axis)

**Question:** *did this change make any function worse?*

Every lens in §3 measures a single snapshot. `regress` is the first to measure
the **delta** between two states — the quality ratchet CI needs.

`regress.Compare(oldFiles, newFiles)` computes per-function metrics for both
sides (via the shared `calibrate.FuncMetrics`), matches functions by
`(file, func)`, and emits a `Regression` for every metric whose value increased:

| Field | Meaning |
|---|---|
| `Old` / `New` / `Delta` | metric value before, after, and `New-Old` (>0) |
| `Crossed` | the new value exceeds the metric's conventional gate (complexity 10 / params 5 / returns 3 / func_lines 30) |

Design choices (all deterministic, type-free):

- **Conservative matching.** `(File, Func)` exact match. A rename appears as an
  old-func removed + a new-func added — neither is a regression of an existing
  function. No type resolution, so no mis-attribution.
- **New / removed functions are not regressions.** Only functions present on
  *both* sides can regress.
- **`--strict` gates on `Crossed`.** A 2→3 complexity bump is reported but does
  not fail CI; a function crossing a conventional gate does. This makes the
  ratchet practical: it blocks meaningful degradation without nagging on noise.
- **Order:** Delta desc → File → Func → Metric.

This closes the snapshot-only limitation shared by all prior lenses: paired with
a baseline (e.g. the merge target), `regress --strict` is a deterministic,
zero-dependency quality gate that prevents backsliding even when the absolute
metric levels are imperfect.

### Git baseline (v0.84)

To make the ratchet a one-line CI gate, the CLI reads the *old* tree directly
from a git revision: `regress --base <rev> [--new DIR]`. The baseline is
obtained with `git archive --format=tar <rev>[:<subtree>]` (one process) parsed
by the stdlib `archive/tar` — no Go module dependency, ADR-0001 intact.
`rev-parse --show-prefix` keeps paths relative to `--new` when it is a
subdirectory, so the `(file, func)` match with the working tree is exact. Typical
usage:

```
yagura regress --base origin/main --strict   # fail PR if any function degraded
```

`--base` and `--old` are mutually exclusive; exactly one is required.

## 15. `nestdepth` — specification (the depth complement to complexity)

**Question:** *complexity says "4" for two functions — four flat guard clauses
and a four-deep `if{for{if{if}}}` pyramid. Which is harder to read?*

McCabe cyclomatic complexity counts the *number* of branch paths (breadth) but
is blind to how deeply those branches nest. `nestdepth` measures the orthogonal
quantity — the **maximum control-flow nesting depth** — so the two functions
above score 1 (flat) vs 5 (pyramid). The whole point of guard-clause /
early-return refactoring is to cut depth while preserving complexity; that win
is invisible to complexity alone.

Rules (deterministic, type-free):

- Depth = number of control-flow blocks one must enter to reach the deepest
  statement; the function body is depth 0.
- `if` / `for` / `range` / `switch` / `type-switch` / `select` bodies each add 1.
- **`else if` chains stay at the same depth** (a continuation, not a nest) —
  matching SonarSource cognitive-complexity intent.
- Bare blocks `{}` and `FuncLit` closures do not add depth; a closure's internal
  nesting is *not* charged to the enclosing function (separate scope).
- Default threshold 4 (flag depth > 4); severity medium (5) / high (6+).
  `_test.go` and `TestXxx`/`BenchmarkXxx`/`ExampleXxx`/`FuzzXxx` excluded.

**Dogfood note**: on Yagura (1303 functions) only 3 exceed depth 4 —
`apidoc.scanFile` (6), `deadcode.collectCandidates` (5), and `plantracker.Parse`
(5). The last is *also* the complexity-32 calibrate outlier, confirming it is
both wide and deep; the other two are deep but not complexity outliers, which is
exactly the slice complexity alone misses. `nest-depth --strict` is a CI gate
against the pyramid-of-doom.

> **v0.88 follow-through.** Because `plantracker.Parse` was flagged by three
> independent axes at once (`calibrate` complexity/lines, `nest-depth` depth,
> `hotspot` convergence), it was the highest-confidence refactor target the
> suite could name. v0.88 decomposed it into six single-responsibility helpers;
> all three lenses now clear it (complexity ≤10, package nest-depth max 3) with
> the 11 `TestParse_*` cases unchanged. This is the suite's purpose realized:
> convergence is not just an audit, it is a refactor backlog ordered by
> confidence.

## 16. `globalcheck` — specification (shared mutable global state)

**Question:** *`synccheck` checks mutex copies and `ctxcheck` checks context
propagation — but what is the single largest source of data races and
untestable code?* Shared mutable global state. No lens looked at it.

Not every package-level `var` is dangerous: a read-only lookup table or config
is effectively immutable. `const` and error sentinels (`var ErrX =
errors.New(...)`, never reassigned) self-exempt. Only a var that is *actually
mutated* is the hazard.

`globalcheck.Scan(files)` buckets files by directory (= package) and runs three
passes per package: collect top-level `var` names; collect every locally-declared
name (`:=`, `var`, params, range vars); collect every mutation target
(`=`/`+=`/`++`, `m[k]=`, `g.f=`). A global is flagged `mutable-global` when it is
mutated **and** its name is not shadowed by any local declaration in the package
— the conservative carve-out that keeps the lens false-positive-free without type
info (if the name is ever a local, the mutation is ambiguous, so skip).
Severity: exported `high` (any package can mutate), unexported `medium`.

**Dogfood note**: on Yagura, 5 of 140 package vars are mutable — four in the
Windows tray (`currentDaemon`/`currentAddr`/`hwnd`/`nid`, forced by the Win32
syscall-callback signature which cannot capture a closure) and `mcp.serverVersion`
(set once via `SetVersion` to inject the build version without a circular import).
Both are justified constrained patterns; the lens's value is making them
*visible and measurable* (à la the calibrate outliers) rather than implying every
one must be removed.

## 17. `typeassert` — specification (implicit-panic / panic safety)

**Question:** *`astcheck` flags the literal `panic` keyword in libraries — but
what about code that panics implicitly?* The classic is `v := x.(T)`: a
single-value type assertion **panics at runtime** if `x` is not a `T`. The safe
form is the comma-ok `v, ok := x.(T)`. This is the recognized `forcetypeassert`
linter, and no Yagura lens covered implicit-panic hazards.

`typeassert.Scan(files)` runs two passes per file: pass 1 records every
comma-ok assertion position (RHS of a 2-LHS `AssignStmt` or 2-name `ValueSpec`);
pass 2 walks each `FuncDecl` body and flags every `TypeAssertExpr` with a
non-nil `Type` (so `x.(type)` switches are excluded) whose position is not in
the safe set. Findings are attributed to the enclosing function.

Distinct from `errwrap`'s `err-type-assert`: that rule is error-specific and
recommends `errors.As` (error-chain correctness); `typeassert` is type-agnostic
and about *panic safety* (only the single-value form; comma-ok is safe). The two
give complementary advice and only overlap on single-value error assertions —
of which Yagura has none (all converted to `errors.As` in v0.76).

**Dogfood note**: 5 unchecked single-value assertions, all safe-by-construction —
three `elem.Value.(*entry)` in `internal/dedupe` (the idiomatic `container/list`
pattern, where the list is type-homogeneous) and two `matches[i]["name"].(string)`
in a sort comparator over a locally-built `map[string]any`. Like the globalcheck
findings, these are surfaced (making the panic surface *visible*) rather than
force-refactored: converting idiomatic `container/list` assertions to comma-ok is
over-defensive, and the map comparator is safe by construction. The lens's value
is the audit, not a mandate.

## 18. `cognit` — specification (cognitive complexity / human reading cost)

**Question:** *`complexity` (McCabe) measures branch paths; `nestdepth` measures
the deepest nesting. But which functions are actually hard for a human to
read?* Neither single axis answers that: McCabe charges +1 per `switch` case
(over-penalizing a flat 10-way `switch` that a human scans easily) yet is blind to
nesting; `nestdepth` sees only the single deepest path. **Cognitive Complexity**
(Sonar; implemented in Go as `gocognit`, and the subject of a Go Conference 2022
Spring talk on a go/ast implementation) is the recognized metric that synthesizes
both — the breadth × depth product weighted to human intuition. Surfaced via
Qiita/Zenn as a Go-community standard distinct from `gocyclo`.

`cognit.Scan(files, threshold)` walks each `FuncDecl` body with a `nesting`
counter:

- **base +1** for `if` / `for` / `range` / `switch` / `select`, each sequence of
  `&&` or `||` (operator change starts a new sequence; parens do not break a
  same-operator run), and labeled `break`/`continue`/`goto`.
- **nesting increment**: `if`/`for`/`switch`/`select` additionally add `+nesting`
  (depth at which they appear). An `if` nested 3 deep contributes `1 + 3 = 4`.
- **`switch` = +1 regardless of case count** — the decisive divergence from
  McCabe.
- `else if` adds +1 structural only (no nesting penalty); `else` likewise. A
  function literal raises the nesting level by one but adds no base increment and
  is folded into the enclosing function (McCabe counts closures as separate
  functions — `cognit` does not). Direct self-recursion adds +1 once per function.

Default threshold **15** (golangci-lint's recommended 10–20 band). Type-free,
deterministic, standard test-exclusion. This is the breadth-and-depth pair to
`complexity` and `nestdepth`: a function flagged by **all three** is the
highest-confidence refactor target the suite can name.

**Dogfood note**: 88 of 1364 functions exceed 15. The instructive result is the
*ordering*: `globalcheck.collectLocalsAndMutations` (~80 lines, cognit **88**)
outranks `cmd/yagura/main.go:run` (**543 lines**, cognit 84). McCabe and raw line
count both call `run` the worst function in the repo; `cognit` says the
nesting-heavy 80-line analyzer is harder to *read*. That inversion — and the fact
that the lens's own sibling lens code tops the list — is the lens working as
intended. Findings are surfaced, not force-fixed: cognit + McCabe + nestdepth
convergence defines the refactor backlog (cf. the v0.88–v0.90 sweep), not a
mass-rewrite mandate.

## 19. `prealloc` — specification (performance / slice preallocation)

**Question:** *Every lens so far measures correctness, readability, safety, or
architecture — but is the code wasteful?* The performance axis was entirely
unmeasured. The most widely-recognized Go performance anti-pattern (a Qiita/Zenn
staple) is growing an empty slice by `append` inside a loop over a known-length
collection: each time capacity is exceeded the runtime allocates a new backing
array, copies the old contents, and frees the old one — repeatedly, with GC
pressure. When the final size is known up front, `make([]T, 0, len(coll))` does
it in one allocation. This is the recognized `prealloc` (alexkohler/prealloc)
check, and no Yagura lens covered it.

`prealloc.Scan(files)` walks each `FuncDecl`: pass 1 collects "empty" slice
declarations (`var s []T`, `s := []T{}`, `s := make([]T, 0)` — `make([]T, 0, n)`
and `make([]T, n)` are already allocated and exempt); pass 2 finds `range` loops
and, for each, inspects the **top-level** statements of the loop body for
`s = append(s, …)` where `s` is one of the empty slices declared *before* the
loop. Such a slice is flagged with a `make([]T, 0, len(<range>))` suggestion.

Deliberately conservative — the canonical `prealloc` defaults, chosen to keep
false positives near zero:

- **`range` loops only.** A plain `for` has a statically-unknown iteration count,
  so no capacity can be suggested. (`range` over slice/array/map/string has a
  known length; `range` over a channel is the residual caveat the reader judges.)
- **Top-level appends only.** An `append` guarded by an `if` inside the loop runs
  a data-dependent number of times — preallocating to the range length would
  merely be an upper bound, and flagging it risks noise, so it is skipped.
- **Empty declarations only**, declared before the loop in the same function.

**Dogfood note**: 52 candidates across 303 files. Three textbook single-append
cases were fixed in the same release — `coupling.parseImports`
(`make([]string, 0, len(f.Imports))`) and the `uniqueSorted` helpers in `initsh`
and `initps1` (`make([]string, 0, len(in))`) — each covered by existing tests
that passed unchanged, demonstrating the lens drives real, verifiable
optimization. The remaining ~49 are surfaced as the performance backlog: like the
other lenses, `prealloc`'s value is the audit, and the highest-traffic loops
(scanners, formatters) are the first to address.

## 20. `thelper` — specification (test-helper `t.Helper()` hygiene)

**Question:** *`assertcheck` measures whether tests assert anything — but is the
test scaffolding itself trustworthy?* A test helper that takes `*testing.T` and
calls `t.Fatal`/`t.Error` but never calls `t.Helper()` reports its failures
against its *own* line numbers, so a failing assertion points inside the helper
rather than at the test that called it — actively misleading during debugging.
`t.Helper()` fixes this, and the recognized `thelper` (kulti/thelper) linter
enforces it. No Yagura lens covered test-helper hygiene.

`thelper.Scan(files)` walks every `FuncDecl`. A function is a *helper* if it
takes a parameter whose type (pointer stripped) is the literal selector
`testing.T` / `testing.B` / `testing.TB` / `testing.F`. It is flagged
(`missing-t-helper`, medium) when **no** `<param>.Helper()` call appears anywhere
in its body.

Conservative scoping keeps false positives near zero:

- **Entry points are excluded.** Names matching Go's test-runner convention
  (`Test`/`Benchmark`/`Fuzz`/`Example` followed by an uppercase letter, digit,
  `_`, or end — so `TestMain` and `TestFoo` are out, but `testHelper` is in) are
  run *by* the framework and need no `Helper()`.
- **Literal `testing.X` only.** Aliased imports would need type resolution
  (same constraint as `ctxcheck`); skipped.
- **Blank / unnamed `testing` params are skipped** — `Helper()` can't be called
  on them, so flagging would be noise.
- **Absence only.** A helper that calls `Helper()` anywhere (even not first) is
  accepted; the lens does not enforce *position*, avoiding style churn.
- **FuncLit closures are not scanned** (named `FuncDecl` only), sidestepping the
  `t.Run` subtest-closure debate.

Unlike production-code lenses (L4), `thelper`'s subject *is* tests, so it scans
`_test.go` files too (and `testutil`-style helpers in regular files).

**Dogfood note**: of 68 helper candidates across Yagura, exactly **one**
(`internal/mcp/tools_pindrift_test.go:depsWithPinDrift`) lacked `t.Helper()`. It
was fixed in the same release (one line), so `thelper --dir .` now reports zero —
a high-discipline result that doubles as a regression gate (`thelper --strict` in
CI keeps it at zero).

## 21. `ifacebloat` — specification (interface design / Interface Segregation)

### Motivation (Socratic新視点 XXI — Qiita/Zenn 調査)

Rob Pike's proverb — **"The bigger the interface, the weaker the abstraction"** —
is one of Go's most-cited design aphorisms (Qiita/Zenn 上で繰り返し言及される
「インターフェースは小さく保て」). The dedicated linter `sashamelentyev/interfacebloat`
enforces a method-count gate; prior Yagura lenses measured function internals,
signatures, and package structure, but the *interface granularity* axis was
untouched. Large interfaces:

1. Make mocking difficult (many unused stubs to write).
2. Force callers to depend on methods they don't need (Interface Segregation
   Principle violation).
3. Signal vague responsibility boundaries.

### Counting rules (go/ast, type-info-free)

- Named method declaration → **+1 per name** (`A, B func()` = 2).
- Embedded interface (`io.Reader`) → **+1** (type resolution not available without
  `go/types`; counted as one element in the interface body).
- Type-union term (`~int | ~string`) → **+1** (the whole constraint term, not per
  constituent).

These rules match `interfacebloat`'s conservative behaviour and require no
external packages (ADR-0001).

### Thresholds and severity

| Condition | Severity |
|---|---|
| count ≤ threshold | OK (no finding) |
| count > threshold | `medium` |
| count > 2 × threshold | `high` |

Default threshold: **10** (interfacebloat default).

### Exclusions

- `_test.go` files — test mocks intentionally implement large interfaces.
- Non-`.go` files.
- Anonymous / unnamed `interface{...}` that are not `TypeSpec` (inline type
  constraints in generics are not named interfaces and are not counted).

### Output (deterministic)

`Report.Interfaces` sorted `File → Line → Name`. Findings same order. Ties in
name are broken alphabetically. `MaxMethods` is the largest method count observed
(including non-flagged interfaces).

**Dogfood note**: `iface-bloat --dir .` on Yagura found **1 violation** on first
run — `mcp.QuotaMonitor` (12 methods). Inspection confirmed that `IsStale` and
`AnyStale` were included in the interface but never called through it (only used
internally within `internal/quotamonitor`): a textbook ISP violation. Both methods
were removed from the interface in the same release; `QuotaMonitor` is now exactly
10 methods (at threshold, not flagged). `iface-bloat --dir .` now reports 0, and
the fix shrank the public contract without changing any behaviour — the lens
immediately paid for itself.
