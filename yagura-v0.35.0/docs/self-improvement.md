# Self-Improvement in Yagura (bounded, deterministic RSI)

How Yagura incorporates **recursive self-improvement (RSI)** without becoming
the thing RSI research warns about. Implemented in `internal/selfimprove` and
exposed as the `yagura_self_improve` MCP tool.

## The idea, and why Yagura is the right place for it

RSI in its naive form — *a model that rewrites its own weights to get smarter,
which lets it rewrite them better* — is both outside Yagura's design
(zero-LLM, deterministic, [ADR-0001](adr/0001-zero-dependencies.md)) and the
unsafe end of the research. Three results reshape it into something Yagura can
own safely:

- **STOP** ([arXiv 2310.02304](https://arxiv.org/abs/2310.02304)) — *Self-Taught
  Optimizer*. Key result: **for a fixed model, the design of the scaffolding
  program is itself an optimization problem.** Self-improvement does not require
  touching weights; it lives in the **harness**. Yagura *is* that harness.
- **Darwin Gödel Machine** ([arXiv 2505.22954](https://arxiv.org/abs/2505.22954))
  — relaxes the original Gödel machine's "*prove* a change helps before adopting
  it" to **empirical validation**: produce → trial → **select**, keeping an
  archive of what worked. The lesson Yagura takes: **never adopt a harness change
  blindly; measure whether it actually helped, and be ready to revert.**
- **"Your Agent May Misevolve"** ([arXiv 2509.26354](https://arxiv.org/abs/2509.26354))
  and the ICLR 2026 RSI workshop — self-evolving agents carry emergent risk
  (*misevolution*) and stay safe only when **"feedback loops are instrumented,
  rewards logged in real time, adaptations kept within guardrails, and memories
  auditable."**

That last sentence is Yagura's whole thesis (*kernel, not brain*). So:

> **Yagura does not modify itself.** It is the deterministic, auditable
> **fitness-and-guardrail kernel** that makes an agent's *harness-level*
> self-improvement loop measurable, gated, and reversible.

## The loop

```
        ┌──────────────────────────────────────────────────────────┐
        │                                                          │
        ▼                                                          │
  observe self-metrics            propose (ranked)        adopt / revert
  token_stats (calls,      →   yagura_self_improve   →   human or agent acts;
  errors, resp bytes)          • reliability             next window's metrics
  skill_audit (score,          • token_economy           feed back in as the
  retire)                      • coverage                 empirical "select"
  harness_coverage             • retire                          │
  (matrix gaps)                • fitness (regression)            │
        ▲                                                        │
        └────────────────────────────────────────────────────────┘
```

Yagura already *measures* (`yagura_token_stats`, `yagura_hook_stats`,
`yagura_skill_audit`, `yagura_harness_coverage`). `yagura_self_improve` closes
the loop: it turns a snapshot of those metrics into **ranked, actionable
proposals**, and — when given the previous window — flags **regressions** so a
recent change is treated as *unvalidated* until the numbers say otherwise.

## Proposal kinds

| Kind | Trigger | Maps to |
|---|---|---|
| `reliability` | a tool's error rate ≥ 5% (medium) / 20% (high) over ≥ 5 calls | STOP: fix the scaffold around a failing tool |
| `token_economy` | large avg response (≥ 4 KB) on a frequently-called tool | input-token economy (`summary_only` / compact) |
| `retire` | a skill scores < 40 or is flagged retire | MUSE-Autoskill self-cleaning |
| `coverage` | a Fowler feedforward/feedback quadrant has no control | harness-engineering matrix |
| `fitness` | a tool's error rate rose ≥ 10 points since the last window | **Darwin Gödel** empirical select / misevolution defense |

Output is **deterministic**: proposals are sorted `severity → kind → target`,
so regression tests and audit logs can compare runs directly. Severity counts
and a one-line summary accompany the list.

## Guardrails (why this is the *safe* form of RSI)

- **No self-modification.** Yagura only *advises*; a human or agent executes.
  Auto-deletion / auto-rewrite is explicitly out of scope.
- **No LLM.** All rules are deterministic thresholds (named constants in
  `internal/selfimprove`), so the advice is reproducible and reviewable.
- **Empirical, not assumed.** The `fitness` kind refuses to call a change good
  until the next window's metrics confirm it — and surfaces regressions loudly.
- **Auditable.** Output is plain JSON; set `record: true` and the assessment
  (counts + proposal ids) is appended to the append-only, hash-chained audit log
  as a `self_improve` record — so the **self-improvement trajectory itself is
  tamper-evident and replayable** (`yagura verify`). This is the "memories
  auditable" requirement made concrete: you can diff successive assessments to
  confirm the loop is converging, not misevolving.

## Surface

`yagura_self_improve` (MCP, `[S]` sensor):

```jsonc
{
  "session_calls": 120,
  "tools":      [{ "name": "...", "calls": 40, "errors": 7, "avg_resp_bytes": 9000 }],
  "prev_tools": [{ "name": "...", "calls": 30, "errors": 1 }],   // optional → fitness
  "skills":     [{ "path": ".claude/skills/x/SKILL.md", "score": 25, "retire": true }],
  "coverage_gaps": ["feedback:post-deploy"]
}
// → { "proposals": [ … ranked … ], "by_kind": {…}, "by_severity": {…},
//     "summary": "…", "self_collected": false }
```

**Closing the loop — self-collection.** Omit `tools` and the tool reads *this
running daemon's own* live token stats (`AllToolStats`: calls, errors, response
bytes per tool) and analyses them — so a bare call observes the actual harness:

```jsonc
{}  // → proposals about the live daemon; "self_collected": true
```

**Auditable trajectory.** Add `"record": true` and the assessment is appended to
the hash-chained audit log (`kind: self_improve`, with `by_severity` / `by_kind`
/ the proposal ids). The response echoes `"recorded": true`. Each recorded
assessment becomes a tamper-evident entry you can later replay with
`yagura verify` and diff over time — the durable memory that safe RSI requires.

Read the trajectory back with the CLI (no daemon needed — it reads the same
audit dir):

```bash
yagura self-improve-history          # timeline: when / high·med·low / proposals / self
yagura self-improve-history --json   # machine-readable; --limit N for the last N
```

It prints a `trend` line (high-severity first→last: converging / flat /
regressing) so a human or CI step can confirm the loop is actually improving.

This is the loop closed in practice: the harness observes itself, with no
hand-assembled snapshot. A caller stashing the previous window's stats can pass
them as `prev_tools` to get `fitness` regression checks against the live current
window. `skills` / `coverage_gaps` (which the daemon can't read from its own
process) are still supplied by the caller.

Together with `yagura_parallel_plan` (1→N dispatch) and `yagura_recovery_decide`
(failure handling), it completes Yagura's deterministic control plane for agent
work.

## References

- STOP — arXiv 2310.02304
- Darwin Gödel Machine — arXiv 2505.22954
- Your Agent May Misevolve — arXiv 2509.26354; ICLR 2026 RSI workshop
- Schmidhuber, *Gödel Machines* (the provably-optimal ideal DGM relaxes)
- Fowler, *Harness Engineering* (the feedforward/feedback matrix)
