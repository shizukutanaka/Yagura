# CLAUDE.md — yagura development guide

> このファイルは yagura を Claude Code / Windsurf で開発する際の context guide。
> m's harness G1.P 「Plan.md 必須記載項目」の構造に従う + Claude Code 推奨形式
> (Why / Map / Rules / Workflows) を採用。

## Why — yagura は何か、何でないか

yagura は **portfolio orchestrator + harness** で、m's sovereign computing stack
23+ projects (Breeze, Tessera, IZANAGI, Cotton, Strawberry, Otedama, ...) を
**1 つの zero-dep Go daemon** で運用する MCP server。

cortex flywheel 4 段階すべてを単体で機械化:
- ② Review:  `quality_check` + `ai_verify` + `test_audit` + `secretscan` + `gha_audit` + `pin_drift`
- ③ Release: `release_radar` (Plan.md aware ranking)
- ④ Alert-Fix: `alert_fix` (6 source × 4 severity の rule-based recommendation hub)

**yagura が *ない* もの**:
- LLM 呼出(全 rule-based、deterministic)
- 外部 SaaS 依存(GitHub API は scanner 経由のみ、optional)
- マルチエージェント orchestrator(MCP server 一品)
- code generation tool(yagura は audit/orchestrate のみ)

## Map — 56 internal packages

### Core orchestration
- `internal/registry` — 23+ projects の inventory CRUD
- `internal/project` — Project struct + validation
- `internal/mcp` — MCP server + 63 tool definitions
- `internal/audit` — JSONL audit log + replay
- `internal/config` — env / flag 設定

### Quality guides (pre-emptive)
- `internal/qualitycheck` — code lint (as any, TODO/FIXME 等)
- `internal/secretscan` — gitleaks-like secret 検出
- `internal/publicityscan` — 公開前 leak 検出(home パス/内部 host/private IP/email)★ v0.35
- `internal/injectscan` — untrusted content の間接プロンプトインジェクション検出
  (override/exfil/hidden/encoding、多言語。CLI `inject-scan` + MCP `yagura_inject_scan`、S4.3)★ v0.35
- `internal/sbom` — CycloneDX SBOM 生成
- `internal/vex` — OpenVEX v0.2.0 文書の決定論的生成(Build/Merge)+ 構造 lint
  (Validate/ParseAndValidate)。spec: `docs/vex-spec.md`。CLI `vex-audit` が
  `docs/vex/*.json` を CI 検証(SBOM 過剰報告の補完)★ v0.35
- `internal/ghaaudit` — GitHub Actions workflow audit
- `internal/pindrift` — 依存 pin 漏れ検出
- `internal/aiverify` — AI 生成 code の risk pattern 検出 ★ v0.25
- `internal/testcoverage` — source-test 対応検出 ★ v0.26
- `internal/astcheck` — Go 構造解析(go/ast、zero-dep)。行 regex では不能な検査:
  os.Exit in library(package 文脈)/ 空 `!= nil` 分岐(block 構造)/ defer-in-loop
  (ループ×関数スコープ)/ err 文字列比較 / parse-error。加えて capability surface
  分析(`Surface`: import から exec/network/unsafe/reflect/crypto を静的プロファイル、
  least-privilege レンズ)。CLI `ast-check` + `--surface`、MCP `yagura_ast_check`
  (Roadmap #6 の増分、自リポジトリ dogfood 済み)★ v0.36
- `internal/ccsecurity` — Claude Code プロジェクトのセキュリティ姿勢を決定論的に監査
  (機械判定可能な対策 = .env 同梱/危険フラグ/deny ルール/CLAUDE.md ルール/git/MCP 最小化 を
  スコア化、人手プロセス項目はガイダンス提示。CLI `cc-security`)★ v0.36
- `internal/reviewgate` — ② Review scanner 群(secretscan/aiverify/qualitycheck/astcheck)
  の結果を 1 つの合成判定(allow/review/block)へ束ねる deterministic gate。hard signal は
  secure-by-default で即 block。opsrisk(操作)/pathpolicy(パス)の ② Review 版の対。
  CLI `review-gate --dir . [--strict]`★ v0.36

### Security sensors (observation)
- `internal/osv` — OSV.dev 脆弱性 API client
- `internal/scorecard` — OpenSSF Scorecard 取得
- `internal/scanner` — 24h 周期で project metadata 更新

### Agent handoff (Claude Code ↔ Windsurf)
- `internal/quotamonitor` — quota tracking + forecast + JSONL persist
- `internal/handoff` — handoff session management
- `internal/agentlauncher` — agent process spawn
- `internal/workspace` — session save/load
- `internal/harness` — .claude/ + MCP 監査(skill/subagent/workflow/settings/agent-config/plugin/mcp)

### Reasoning / multi-AI (★ v0.35)
- `internal/agentparallel` — 複数 AI への deterministic な並列 dispatch planner(LPT)
- `internal/riskreason` — Cyber Risk Reasoning Layer(CVSS+資産+到達性+攻撃性の複合判断)
- `internal/recovery` — self-healing orchestration の決定論的 recovery 判断(retry/replan/escalate)
- `internal/selfimprove` — harness レベル再帰的自己改善(RSI)の決定論的カーネル
  (自己メトリクス→優先度つき改善提案 + 後退検出)。spec: `docs/self-improvement.md` ★ v0.35
- `internal/pathpolicy` — 変更パスを glob ルールで deny/review/allow する deterministic guardrail
  (CLI `path-policy` / `.yagura/paths.json` で ADR-0001 等を gate)★ v0.35
- `internal/opsrisk` — 操作の段階的自律性(auto/log/review/human)を capability・可逆性・
  影響範囲から分類する deterministic guardrail(Zenn governance 調査)★ v0.35

### Graph / dependencies
- `internal/projectgraph` — depends_on graph(neighbors / impact / stats)
- `internal/diffscan` — unified diff から追加行/削除行を抽出する純粋プリミティブ。
  snapshot ではなく delta の視点。`AddedLines`(「変更が新たに持ち込んだもの」)+
  `RemovedLines`/`RemovedGuards`(「外された安全装置」= error-check/recover/cleanup の
  削除)。CLI `diff-scan` が追加行に secretscan を適用 + 削除 guard を review-only 報告★ v0.36
- `internal/flowrisk` — 操作シーケンスの危険な *順序* を検出(temporal/flow の視点)。
  secret-read→network(exfiltration)/ fetch-untrusted→exec(injection→実行)/
  fetch-untrusted→write を taint-flow 的に走査。`Analyze`(純関数)+ `ClassifyTool`
  (ツール名→capability)。CLI `flow-risk`(1 行 1 操作名、--strict で high flow gate)★ v0.36

### Cross-tool infra
- `internal/dedupe` — content-addressed cache (LRU + TTL) ★ v0.23
- `internal/plantracker` — Plan.md aware progress parser ★ v0.24
- `internal/alertfix` — health signal aggregator + rule-based recommendation ★ v0.27

### Observation / meta
- `internal/agentevent` — 任意エージェント(Claude Code/Gemini CLI/Codex/OTel/汎用)の
  lifecycle イベントを OpenTelemetry GenAI semconv 整合の canonical event へ正規化
  (observability を Claude Code 専用でなく汎用に。MCP `yagura_agent_event`)★ v0.35
- `internal/sessionsummary` — 正規化イベント列をセッションの構造化 tool-call サマリへ集約
  (tool 別件数/順序/エラー/異常検知。Hermes Desktop 参照。MCP `yagura_session_summary`)★ v0.35
- `internal/dashboard` — HTML dashboard (薄め)
- `internal/telemetry` — internal stats
- `internal/metrics` — counter / gauge primitives
- `internal/logging` — structured slog wrapper
- `internal/httplimit` — http rate limit middleware
- `internal/github` — minimal GitHub API client

## Rules (immutable — 22 リリースで維持)

### ADR-0001 — Zero external Go dependencies
- `go.mod` は `github.com/shizukutanaka/yagura` のみ、stdlib のみ依存
- 22 リリース連続で維持(reproducible build の前提)
- 例外: 検証なし(他案を尽くしてから提案)

### Reproducibility
- `make verify` が byte-for-byte identical build を 2 回連続で生成
- `-trimpath -buildvcs=false` + `CGO_ENABLED=0`
- 22 リリース連続で SHA 一致を維持

### Trust base
- sensor data (vuln_critical, ci_status, scorecard_score, latest_activity 等)
  は scanner 専用で MCP tool 経由 update 不可
- これにより agent / human が sensor 値を捏造できない
- `yagura_update` は manual metadata (display_name, language, local_path, notes,
  priority, tags, stage, depends_on) のみ受付

### Tool descriptions
- v0.21 以降 caveman 化: 最大 ~50 byte 平均
- `[G]` prefix = Guide (pre-emptive control)
- `[S]` prefix = Sensor (observation)
- compact mode (`YAGURA_MCP_COMPACT=1`) では `[G]` / `[S]` だけに圧縮

### Deterministic output
- sort 順、tie-break が決定論的
- audit log は時系列順
- regression test で出力比較が安定

## Workflows (典型的な開発フロー)

### 実装の進め方 — test-first(TDD を標準動作にする)

機能追加・バグ修正は原則 **test-first**。自然言語の指示は解釈の幅があり、AI
(Claude Code 等)はその幅の中で「それっぽいもの」を生成してしまう。先にテストを
書くと、合否の判定者が人間の目視から **テストランナー(機械)** に変わり、仕様が
コードとして 1 つに凍結される。これが「越えてはいけない柵」になり、暴走と往復を防ぐ。

1. **Red — 失敗するテストだけ書く**。実装は書かない。正常系・境界値・異常系を
   1 ケース 1 観点で分け、`go test ./internal/<topic>/` が **赤(未実装で fail)**
   になることまで確認する。緑になるはずのないテストが緑なら、テスト自体が壊れている。
2. **(レビュー)テストの妥当性を人間が確認するまで実装に進まない**。特に各
   `want`/期待値が本当に正しいかを 1 ケースずつ読む。テストが間違っていれば AI は
   間違った仕様を完璧に実装する(= TDD なしより危険、緑のお墨付きが付くため)。
   テストを読む時間は実装全体を読む時間より遥かに短い。
3. **Green — テストを通す最小実装**。**テストファイルは勝手に変更しない**(仕様を
   実装に合わせて書き換えるのは仕様が実装に負ける瞬間)。`go test -race` を実際に
   実行し、全件緑になるまで自分で直す。「通ったはず」の推測報告はしない。
4. **Refactor — 緑を保ったまま整える**。振る舞いを変えず、各ステップ後に test 緑を維持。
5. **バグ修正は再現テストから**。実装を触らず「そのバグで赤くなるテスト」を先に書き、
   赤を確認 → 実装を直して緑 → 全 test green。これで同じバグの regression を恒久的に塞ぐ。

注: 厳密な三角測量までは不要。「実装より先に合格条件をコードで固定する」の 1 点で効果が出る。
本リポジトリは `make verify` の reproducible build + 全 package `-race` test がこの柵を担保している。

### 新 MCP tool 追加
1. **(Red)`internal/<topic>/<topic>_test.go` に domain logic の spec をテストで先に固定**
   (正常系・境界・異常系を 1 ケース 1 観点、赤を確認)。人間が期待値を確認してから次へ。
2. `internal/<topic>/<topic>.go` で domain logic を書く(test cov ≥ 90% 目標、テストを緑に)
3. `internal/mcp/tools.go` に `build<X>Tool(d Deps) *Tool` を追加
4. `RegisterDefaultTools` の末尾に `s.Register(build<X>Tool(d))` を追加
5. `cmd/yagura/integration_test.go` の expected names list と tool 数 (`if len(...) != N`) を更新
6. `internal/mcp/server_test.go` も同様
7. CHANGELOG に entry 追加(必ず Synergy / What's not yet / Sources セクション含む)
8. version bump (`cmd/yagura/main.go`, `cmd/yagura/main_test.go`,
   `internal/dashboard/dashboard.go`, `README.md`)
9. `make verify` で reproducible 確認
10. tarball を `/mnt/user-data/outputs/yagura-vX.Y.Z-source.tar.gz` に出力

### 新 sensor 統合
1. 既存 `internal/scanner` の 24h loop に hook
2. `project.Project` struct に新 field 追加(`omitempty`)
3. registry validate を通すか確認
4. alertfix.ProjectSnapshot に extract field 追加
5. alertfix.Evaluate に新 rule 追加(rule-based recommendation 込み)

### handoff loop の test
1. 2 つの agent (claude_code + windsurf) を `quota_report` で manual 報告
2. `usage_summary` で reliability check (samples ≥ 3 必要)
3. `handoff` で switch trigger
4. `agent_status` で state 確認

## Gotchas — 22 リリースで踏んだ罠

### Registry.Get は `(*project.Project, error)`、Registry.List は `[]*project.Project`
ポインタ。値型と勘違いしやすい。

### `yagura_register` の priority は 0-5
9 や 10 を渡すと validate で reject される。

### Plan.md は LocalPath 配下にあるのが前提
`yagura_register` で local_path を省略すると plan_status / release_radar が
全 project skip する。

### compact mode は env opt-in
`YAGURA_MCP_COMPACT=1` で発火。他は完全に後方互換。

### dedupe cache は in-memory + optional disk(v0.35〜)
`dedupe.Cache.EnablePersistence(dir)` で write-through disk 層を有効化でき、daemon は
`{StateDir}/cache` に有効化する。memory miss は disk から lazy reload(TTL は disk 上の
createdAt 起点)。未有効化(EnablePersistence 未呼出)なら従来どおり in-memory のみ。
eviction は memory が LRU、disk は TTL 期限切れを read 時/起動時に prune。

### audit log の write は best-effort
write エラーは ignore。critical path をブロックしない設計。

### JSONL legacy / compact 両対応
v0.17 以降の `usage_history.jsonl` は legacy + compact 形式が混在。
`persistEntry.resolve()` が両方を扱う。手動で書き換えるとフォーマットが
壊れる可能性。

### sensor data は MCP tool で update 不可
trust base 保護のため意図的。alert_fix の live smoke で plan 以外は scanner
経由 inject が必要。

## Roadmap (carry-over)

優先度順:
1. ~~**tools.go の topic 別分割**~~ ✅ v0.35 — scans / plan / alerts / quality /
   guides / hooks を topic 別ファイルへ分割(2183 → 443 行)。tools.go は
   Deps・interface・RegisterDefaultTools・共有 infra(version / atomicWriteFile)のみ。
2. ~~**Scanner ↔ alert_fix periodic loop**~~ ✅ v0.35 — `scanner.Config.AfterScan`
   callback + `healthSweep` で毎 scan サイクル後に alertfix を実行、結果を
   dashboard banner に表示(lifecycle filter 適用済)。
3. ~~**Persistent cache** (sbom / aiverify 結果を disk に)~~ ✅ v0.35 — `dedupe` に
   optional write-through disk 層(`EnablePersistence`)。daemon は `{StateDir}/cache` に有効化。
4. ~~**Custom rule loading** (`.yagura/aiverify.yaml` 等)~~ ✅ v0.36 —
   3 scanner 全てで project 固有ルールを追加/無効化可能に統一:
   `aiverify.UserConfig` / `secretscan.UserConfig`(+ `RuleSpec`/`CompileRules`)
   + `LoadUserConfig` + `Apply`、qualitycheck は既存 `RuleSpec`/`CompileRules`。
   CLI `ai-verify` / `quality-check` / `secretscan` が `--rules-file` または
   `.yagura/{aiverify,quality,secretscan}.json` を自動検出。MCP
   `yagura_ai_verify` / `yagura_secretscan` が `custom_rules` / `disable_rules`
   パラメータを受付(`yagura_quality_check` は従来から)。
   CLI `test-audit` / `alert-fix`(token-free portfolio health sweep)も追加。
5. ~~**Alert lifecycle** (last-seen / resolved / snooze)~~ ✅ v0.30 —
   `internal/alertfix.Store` に resolve/snooze/reopen + JSONL 永続化。
   MCP `yagura_alert_resolve` / `yagura_alert_snooze` / `yagura_alert_reopen`。
6. **AST analysis** (zero-dep 制約と相談)— 🚧 着手 v0.36: `internal/astcheck`
   (go/parser + go/ast, stdlib のみ)+ CLI `ast-check`。行 regex では不能な
   os-exit-library(package 文脈)/ empty-nil-branch(block 構造)/ parse-error を
   検出。今後 ignored-error 等の構造ルールを追加予定(go/types は要パッケージ
   ロードのため zero-dep と要相談、現状は go/ast のみで型不要の検査に限定)。
7. **OAuth / Marketplace / Code Mode** (long-standing)

## References

- m's harness V1.8 (一次入力)
- cortex / aircloset 2026/05 Zenn 記事 (4 flywheel)
- arXiv 2604.01052 VibeGuard (AI Code Security Gate Framework)
- Anthropic engineering blog (advanced tool use)
- Veracode 2025 (45% OWASP failure for AI code)
