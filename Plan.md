# Yagura Roadmap Plan — v2.0.0 → v3.0.0

> 本ファイルは yagura 自身の `plantracker` が parse できる Plan.md 形式
> (必須セクション: 目的 / スコープ / フェーズ / リスク / 完了定義 + checkbox) で
> 記述している。`yagura_plan_status` で進捗を機械計測できる(dogfooding)。
>
> 起点: v0.34.1 (46 MCP tools / 39 internal packages / 5-OS reproducible / zero-dep ADR-0001)。
> 目標: メジャー2世代 (v2.0.0, v3.0.0) の改善点を体系化する。

## 目的 (Purpose / 背景)

yagura は 0.x 系で「portfolio harness daemon」として 46 tools まで育った。
0.x の間は API 破壊を許容してきたが、外部 (Claude Code / 他 MCP client) が
依存し始めるため、**メジャー版で API 契約を固定し、その上で
"閉ループ自律化" と "アーキテクチャ刷新" を段階的に行う**のが本計画の目的。

- v1.0.0 = API 安定化ゲート (semver 保証の起点、破壊変更はここで打ち止め)
- v2.0.0 = Closed-loop intelligence (検知→修正→検証の自律ループ + namespace + authz)
- v3.0.0 = Architecture & ecosystem (Code Mode 化 / Marketplace / hybrid 解析)

不変条件 (全フェーズ通貫):
- ADR-0001 ゼロ外部依存 (`go.sum` 空) を維持
- `-trimpath -buildvcs=false` + `CGO_ENABLED=0` による byte-for-byte 再現ビルド
- append-only hash-chained audit (ADR-0003) / loopback-default (ADR-0004)
- trust base (`RegisterDefaultTools` 登録順 / sensor は MCP 経由 write 不可) 保護

## スコープ (Scope)

In scope:
- MCP tool API / HTTP API の semver 契約化と namespace 再編
- 検知系 (secretscan / sbom / gha_audit / pin_drift / aiverify / quality_check) と
  alert_fix の閉ループ化
- 永続キャッシュ / カスタムルール / Product Graph × quality 連携
- 認証 (OAuth 2.1 + per-tool scope) と CLI direct mode
- Code Mode (meta-tool) アーキテクチャ、Marketplace、配布形態の見直し

Out of scope (本計画では扱わない):
- GitHub への write-back (ADR-0005 で恒久的に禁止)
- ゼロ依存を破る外部ライブラリ導入 (cgo tray / 言語別 AST parser の無条件追加など)
- マルチテナント SaaS 化 (v3 以降の別計画)

## 現状ベースライン (v0.34.1)

- [x] tools.go トピック別分割 (2,156 → 385 行、6 topic files) — PR #2 で完了
- [x] golangci-lint 段階導入 38 linter (finding-0 保証) — PR #2
- [x] テストカバレッジ 79.3% (閾値 75%) — PR #2
- [x] README/MCP_TOOLS.md 自動生成・OSS doc 一式 — v0.34.1
- [x] Windows tray launcher (1-click) — v0.32
- [x] alert lifecycle (last-seen/resolved/snooze) 初版 — v0.30
- [x] HTTP hook receiver + Prometheus + .well-known/mcp — v0.31

## フェーズ (Phases / マイルストーン)

### Phase 0 — v1.0.0: API 安定化ゲート (on-ramp)

破壊変更をここで吸収し、以後の semver 契約を確定する。

- [ ] MCP tool 入出力スキーマを凍結し、`docs/MCP_TOOLS.md` を契約として版管理
- [ ] HTTP API の OpenAPI 3.1 仕様を生成 (stdlib のみで spec を emit、外部 gen 不使用)
- [ ] semver ポリシーを `docs/adr/0007-semver-and-api-stability.md` に明文化
- [ ] 互換性テスト (golden JSON: tools/list と各 tool の schema スナップショット)
- [ ] CHANGELOG の重複エントリ (v0.34.1 が4回出現等) を一意化し生成を自動化
- [ ] govulncheck を CI 化 (Go toolchain を 1.23.x へ更新し再現性 pin を再固定)
- [ ] golangci-lint の残ノイジー linter を 1 つずつ解消 (wsl/varnamelen/mnd 等)
- [ ] DoD: 全 public API が凍結・文書化され、互換テストが green

### Phase 1 — v2.0.0: Closed-loop intelligence

検知の "受動 audit" を、検知→修正提案→検証の "能動ループ" へ。
破壊変更可 (tool namespace 再編・認可必須化)。

#### 1a. Tool namespace 再編 (破壊変更)

- [ ] tool 名を `portfolio.* / harness.* / security.* / alert.* / graph.*` へ namespace 化
- [ ] 旧名→新名の alias table を 1 メジャー間だけ提供 (deprecation warning 付き)
- [ ] `YAGURA_MCP_COMPACT` と統合した tool 数インフレ対策 (catalog tool 経由 discovery)

#### 1b. Scanner ↔ alert_fix 閉ループ

- [ ] scanner 群を periodic に駆動する loop runner (interval / on-demand)
- [ ] 検知 finding を alert に昇格し、alert_fix が修正パッチ候補を生成
- [ ] 修正後に同 scanner を再実行して resolved を自動判定 (last-seen 連動)
- [ ] ループ全体を audit chain に記録 (誰が何を検知・修正したか追跡可能)

#### 1c. 永続キャッシュ

- [ ] sbom / aiverify / quality_check 結果を content-hash キーで disk 永続化
- [ ] 入力不変なら再計算を skip (大 monorepo での incremental scan)
- [ ] キャッシュ無効化ポリシー (mtime + content hash) を明文化

#### 1d. カスタムルール & Graph 連携

- [ ] `.yagura/aiverify.yaml` 等で project 毎のカスタムルール読込 (`ScanWithRules` を UX 化)
- [ ] allowlist 機構 (特定 finding を理由付きで抑制、audit に残す)
- [ ] Product Graph node に最新 quality/scan state を属性として載せる
- [ ] "禁止 finding を持つ全 project" を 1 query で取得可能に

#### 1e. 認証 & CLI

- [ ] OAuth 2.1 + per-tool scope (MCP server デメリット #5 解消)
- [ ] CLI direct mode (`yagura list` / `yagura register`、daemon 無しでも実行)
- [ ] DoD: ループが 23+ projects で無人実行でき、認可・キャッシュ・カスタムルールが動作

### Phase 2 — v3.0.0: Architecture & ecosystem

トークン効率と拡張性のためのアーキテクチャ刷新。最も破壊的。

#### 2a. Code Mode (meta-tool) アーキテクチャ

- [ ] 46 tools を 4 meta-tool (listToolFiles/readToolFile/getToolDocs/executeToolCode) の背後へ集約
- [ ] heavy multi-server 構成での token 150K→2K 級削減を計測で実証
- [ ] 旧 flat tool surface を互換モードとして残す (移行期間)

#### 2b. Streamable HTTP / spec 追従

- [ ] MCP 2026 spec の Streamable HTTP transport 対応
- [ ] `.well-known/mcp` と OpenAPI を 2026 spec に整合

#### 2c. Hybrid 解析 (zero-dep を守る形で)

- [ ] AST-context 解析 (Go は go/ast、他言語は optional plugin の境界設計で zero-dep core を維持)
- [ ] pattern + LLM hybrid を opt-in (core は regex のまま、LLM は外部 sidecar として分離)
- [ ] 誤検知 (文字列リテラル内 `as any` 等) の低減を回帰テストで担保

#### 2d. Ecosystem / 配布

- [ ] Marketplace 連携 (tool/rule の配布・発見)
- [ ] ソース配布を tarball から通常の Git 管理ツリーへ移行 (binary は Releases asset 化)
- [ ] mkdocs / Hugo で GitHub Pages 公開
- [ ] DoD: Code Mode が default、Marketplace 経由で rule 配布、配布形態が Git-native

## リスク (Risks)

- **再現性 pin の破壊**: toolchain 更新 (1.22→1.23) や Code Mode 刷新で build SHA 基準が変わる。
  → メジャー境界でのみ実施し、各メジャーで新 baseline SHA を確定・記録する。
- **ADR-0001 違反圧力**: AST/LLM hybrid・tray・Marketplace が外部依存を誘発。
  → core は zero-dep 維持、拡張は optional sidecar/plugin 境界に隔離する。
- **API 破壊による client 影響**: namespace 再編・Code Mode 化は Claude Code 連携を壊しうる。
  → v1.0 で凍結 → v2/v3 で alias と互換モードを 1 メジャー間提供。
- **trust base 毀損**: ループ自律化で sensor への意図せぬ write 経路が生まれる懸念。
  → `RegisterDefaultTools` 順と sensor write 禁止を不変条件として回帰テスト化。
- **スコープ肥大**: 各メジャーが大きく、1 PR 集約は不可。
  → フェーズ単位で独立 PR、各 PR で build/vet/lint/test/make verify を必須化。

## 完了定義 (Definition of Done / DoD)

各メジャーは以下を全て満たして初めて release:

- [ ] `go build ./...` / `go vet ./...` クリーン
- [ ] `gofmt -s -l .` 0 件 / `golangci-lint run` 0 issues (有効 linter は退行なし)
- [ ] `go test -race -count=1 ./...` 全パス、総カバレッジ ≥ 75%
- [ ] `make verify` で byte-for-byte 再現ビルド一致、新 baseline SHA を CHANGELOG に記録
- [ ] `go.sum` 空 (ADR-0001) を確認
- [ ] CHANGELOG に Theme / Honest audit / What it still doesn't have / Sources を記載
- [ ] 破壊変更がある場合は移行ガイドと alias/互換モードを同梱

## 改善点インデックス (出典)

- tools.go 分割 / lint / coverage … PR #2 (本セッション)
- govulncheck CI / 残 linter / tarball→Git / OpenAPI … 本セッションのコードレビュー所見
- namespace / OAuth / CLI / Streamable HTTP / GitHub Pages … CHANGELOG「v0.35 candidates」
- Code Mode (150K→2K) … CHANGELOG「Why didn't we adopt Code Mode」(v1.0+ 候補)
- AST / LLM hybrid … CHANGELOG「pattern + LLM hybrid △ v1.0+」
- scanner↔alert_fix loop / persistent cache / custom rule / alert lifecycle … CLAUDE.md「Roadmap (carry-over)」
- Graph × quality 連携 / allowlist / incremental scan … CHANGELOG v0.19「still doesn't have」
- CHANGELOG 重複の一意化 … 本セッションで検出した既存データ品質問題
