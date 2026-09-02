# ADR-0006 — Design Decisions Accumulated v0.7 → v0.27

Status: Accepted (retroactive documentation)
Date: 2026-05-13
Supersedes: none
Note: ADR-0001 through 0005 already existed (zero-deps / json-state / append-audit / bearer-auth / no-write-back); this ADR retrospectively documents decisions made in v0.7-v0.27 not yet covered by existing ADRs.
Related: ADR-0001 (zero external Go dependencies)

## Context

22 リリース (v0.6 → v0.27) で重要な設計判断を多数行ったが、ADR は 0001 のみ。
honest engineering critique (v0.28) で「ADR が ADR-0001 のみで、22 リリースで決定積み
上がってるのに記録少ない」と特定された。本 ADR で主要決定を retrospective に記録。

## Decisions

### D-1: Caveman tool descriptions (v0.16, v0.21)
**Decision**: MCP tool descriptions を `[G]` / `[S]` prefix + 47 byte 平均に圧縮。

**Rationale**:
- Anthropic engineering blog: tool description が context budget の大部分を占める
- arXiv 2602.14878 (Tool Descriptions Are Smelly) で「短すぎると精度低下」と警告
- v0.21 で full → caveman → ベンチマーク → 34% reduction を実証
- compact mode (`YAGURA_MCP_COMPACT=1`) で更に -54%、補完 `yagura_tools_catalog` で
  full info lazy fetch

**Trade-off**: tool selection accuracy が low end で下がる可能性。yagura では tool 名
規約 (`yagura_<topic>_<action>`) で意味推測可能なので採用。

### D-2: Atomic JSONL persistence with O_APPEND (v0.17)
**Decision**: `usage_history.jsonl` を O_APPEND で原子的追記。

**Rationale**:
- POSIX `O_APPEND` write は 1 line < PIPE_BUF で concurrent safe
- 単一 line 単位で破損しても他 line は読める(corrupt-line tolerance)
- v0.22 で compact form (`{a, t, r, s}`) に移行、legacy + compact 両 read 対応

**Trade-off**: 個別 entry の random access 不可。time-series scan のみ。

### D-3: Sensor / metadata の trust separation (v0.13 〜)
**Decision**: `yagura_register` / `yagura_update` は manual metadata 専用、sensor
data (vuln_critical, ci_status, scorecard_score, latest_activity 等) は scanner
専用とする。

**Rationale**:
- MCP tool 経由で sensor 値を捏造できないことで trust base を保護
- alert_fix (v0.27) の信頼性の前提
- scanner は GitHub API / OSV.dev で source of truth から取得

**Trade-off**: live smoke で全 alert source を実演できない → unit test 中心で証明
(alertfix 20 tests でカバー)。

### D-4: dedupe cache LRU + TTL (v0.23)
**Decision**: content-addressed cache を `container/list` + atomic stats で実装。

**Rationale**:
- Get O(1), Set O(1), evict O(1) を stdlib のみで達成
- defensive copy で caller mutation を防ぐ
- atomic.Uint64 で stats を lock-free read
- 65% 削減実証 (quality_check 重複 100 files 3 回 scan: 67ms → 32ms → 10ms)

**Trade-off**: in-memory only。daemon restart で消失。persistent cache は v0.29+。

### D-5: Plan.md aware Release Radar (v0.24)
**Decision**: m's harness G1.P の共通 Plan.md format を 23+ projects 横断で parse、
release readiness を weighted score (plan 40% + ci 25% + critical 20% + quality 15%)
でランク付け。

**Rationale**:
- 23 projects 全てが同じ harness を共有 → Plan.md format も共通
- portfolio orchestrator の core value
- cortex flywheel ③ Release に対応

**Trade-off**: Plan.md がない project は skip。CI status が unknown だと score 87
が cap(passing でないため)。

### D-6: AI verifier — regex base, not LLM (v0.25)
**Decision**: AI 生成 code の risk pattern を regex で deterministic に検出、AI marker
の ±10 lines を 2x severity multiplier。

**Rationale**:
- 「AI が書いたコードを AI で review してはいけない」(Medium 2026) — same blind
  spots
- zero-dep ADR-0001 維持(外部 LLM call なし)
- reproducibility 維持(audit 結果が決定論的)
- Veracode 2025 (45% OWASP failure) / CodeRabbit (1.7× multiplier) を pattern set
  に反映

**Trade-off**: false negative がある(regex の限界)。AST は将来課題。

### D-7: cortex flywheel ④ Alert-Fix as recommendation hub (v0.27)
**Decision**: alert を 6 source × 4 severity × suggested_tool + args の構造に。
LLM を呼ばず rule-based。

**Rationale**:
- agent loop で「次に呼ぶべき tool」が即明確
- determinism + reproducibility 維持
- cortex flywheel ④ の閉ループを zero-dep で実現

**Trade-off**: 推奨が事前定義の template に限られる。動的な diagnosis は不可。

### D-8: Backward compat 優先 (継続)
**Decision**: 機能変更時は backward compat を厳守。

**Examples**:
- v0.22: JSONL legacy/compact 両 read。新 write のみ compact。
- v0.21: tools/list の compact mode は env opt-in。
- v0.26: `aiverify.ScanFiles` API は v0.25 同じ。`ScanFilesCached` を追加。

**Rationale**: 22 リリースで accumulated user (m 単独だが) を壊さない。dogfood と
しても重要。

### D-9: deterministic sort + tie-break (継続)
**Decision**: 全 list 出力で sort 基準を多段にして tie-break を決定論的に。

**Examples**:
- `release_radar`: readiness desc → plan_progress desc → slug asc
- `alert_fix`: severity weight → source alpha → project alpha
- `aiverify Findings`: file → line → column

**Rationale**: regression test が安定。replay や diff が意味を持つ。

### D-10: Reproducible build に投資 (継続)
**Decision**: `make verify` を毎リリース実行、22 リリース連続で SHA 一致を確認。

**Implementation**:
- `CGO_ENABLED=0` + `-trimpath` + `-buildvcs=false`
- VERSION 環境変数で injection、それ以外はコード内の時刻 hook 排除
- Docker 化されてないが、それでも 2 連続 build で identical

**Rationale**: supply chain attack 対応 (m's harness G7.10)。配布バイナリの完全性
検証可能。

## Trade-offs Accepted

これらの decisions により以下を accept した:

1. **MCP tool 数が増えても compact mode で対処** — 個別 tool 統合 (graph 3 → 1 等)
   は Anthropic engineering blog が警告する failure mode (似た名前 tool の混同) を
   避けるため棄却。
2. **persistent cache なし** — daemon restart で消失するが、最重要 (sbom) は
   24h 周期 scanner が再生成するので影響軽微。
3. **regex 限界** — AST が必要な検出 (例: 関数間データフロー) は未対応。
4. **single binary** — 機能の独立配布は不可。yagura 全体で 1 unit。

## Future ADRs needed

- ADR-0003: tools.go split strategy (v0.29 候補、構造改善)
- ADR-0004: persistent cache の format and migration
- ADR-0005: scanner ↔ alert_fix integration loop の cadence

## References

- v0.6 〜 v0.27 CHANGELOG entries (each release section)
- m's harness V1.8 (一次入力)
- Anthropic engineering: https://www.anthropic.com/engineering/claude-code-best-practices
- cortex / aircloset: https://zenn.dev/aircloset/articles/d416342f46f16b
