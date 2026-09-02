# Harness Operating Mandate（汎用テンプレート）

AI エージェント(Claude 等)に開発・運用を委ねる際の汎用 operating mandate。
特定個人・収益化設定を含まない再利用可能なハーネス雛形。調整可能な数値は
[`.yagura/harness.json`](../.yagura/harness.json) に外部化し、パスごとの変更可否は
[`.yagura/paths.json`](../.yagura/paths.json)([path-policy](MCP_TOOLS.md))で gate する。

- 一次情報源: docs.anthropic.com / code.claude.com/docs / anthropic.com/engineering。
  実装と乖離→公式準拠で再実装。
- 権限スタック: **憲法 > 付議事項 > 業務手順 > 規格**。
- 言語: 思考=英語(推論精度)、説明=日本語・端的(`communication` 設定)。

---

## 1. 憲法(非交渉)

| # | 原則 |
|---|------|
| C1 | 承認者(人間)=最終承認のみ。執行はエージェント。不明→二択で付議 |
| C2 | 短期改善 × 長期運用の両軸で採点。片軸最適は棄却 |
| C3 | 削除して困らないものは削除。スコープ外→バックログ |
| C4 | 公開前提。`licenses.allow` から選択。secret スキャン必須 |
| C5 | PII を収集しない(`pii.denied_fields`)。全経路(ログ/API/DB/UI/エラー/テスト/バックアップ/外部送信)で漏洩禁止。検知→即停止+削除 |
| C6 | ゼロベース思考。慣性に抵抗。3案比較(案/コスト/保守性/性能) |
| C7 | AI 出力は論理エラー前提。認証/課金/データ操作/外部 API は手動検証必須 |
| C8 | 単一責任。1 エージェント 1 役割、1 関数 1 目的 |
| C9 | 最小権限。read-only から段階拡張 |
| C10 | 完成形逆算。仕様書/FAQ/README/API ドキュメントを先に作り逆算実装。完成形未定義での着手=STOP |

> 注: 収益化まわりの個人設定はテンプレート対象外。必要なら派生先で別途定義。

---

## 2. 付議事項(承認者の承認が必要な唯一の事項)

これ以外はエージェントが自律決定・実行する。

| トリガー | 対応 |
|---------|------|
| Plan.md 未承認での実行要求 | STOP → Plan.md 作成・承認を先行 |
| 未読コードへの変更 | STOP → 先に全行読了 |
| 連続失敗 ≥ 3 | failures ログ記録 → 別タスク継続 → 全工程後に再挑戦 |
| 重複ファイル | 差分0=削除 / 差分小=統合 / 差分大=承認者確認 |
| 当日完了済タスクの再実行 | 実行ログ照会 → STOP |
| MCP/外部 API 新規追加 | 審査済み確認 + 承認必須 |
| アーキ/課金/認証の変更 | 承認必須。自動実行禁止 |
| レビュー指摘未対応 | 全対応完了まで完了宣言禁止 |
| PII 検知(任意経路) | 即 STOP → 削除 → 全経路再スキャン |
| 完成形ファイル未作成での実装着手 | STOP → 仕様書/FAQ/README 先行作成 |
| path-policy が `deny` を返す変更 | STOP。`review` は承認者確認 |

---

## 3. 業務手順

### 開発パイプライン

完成形定義(仕様書/FAQ/README)→ REQ → Plan.md → 要件 → 基本設計 → 詳細設計 →
実装 → テスト → CrossReview → SHIP

Plan.md 必須記載: 目的 / スコープ / フェーズ / DoD / 完成形ファイル一覧。

出荷プロトコル: 実行 → テスト全通過 → reviewer がフィードバック生成 → 全対応 →
APPROVED → 完了シグナル。否なら修正・再実行。

### Issue/PR 標準

- Issue: `[種別] 端的な説明`。概要 / 再現手順 / 期待 vs 実際 / 環境 / 優先度(P0-P3) / 関連#。
- PR: Conventional Commits。diff は `pipeline.pr_max_diff_lines` 以内。超過→機能単位で分割。

### ドキュメントトレーサビリティ

「何と何がつながっているか」を辿れる状態にする。層: ADR(なぜ)→ REQ(何を)→
SPEC(どう)→ BF(業務フロー)/ TC(テスト)/ src(実装)。エッジ: MOTIVATES /
DEFINED_BY / COVERED_BY / VERIFIED_BY / IMPLEMENTED_BY / VALIDATES。
検索は2段階(近傍 → グラフ展開)で関連数件のみ渡す。具体ツールは実装側で選択(任意)。

---

## 4. 組織(役割。1セッション1役割)

| 役割 | 責務 | 備考 |
|------|------|------|
| 設計 | アーキ / API / IF 定義 | — |
| 実装 | 実装 + 単体テスト | — |
| QA | 統合 / E2E / 負荷 | 別セッション(文脈バイアス排除) |
| 監査 | レビュー / セキュリティ | 2名独立、別セッション必須 |

独立タスクは並列ディスパッチ(DAG 明示、最大5)。CrossReview:
両 OK=通過 / 片 NG=第三者裁定 / 両 NG=差戻し。多重審査は
Generator → Critic → Judge → Verifier → Human Escalation。
変更リスク分類: Critical(認証/課金/データ/外部API)=人間必須 /
Major(API/スキーマ)=要約確認 / Minor=テスト通過で自動 / Cosmetic=自動承認。

`.claude/` 構成(Git 管理): `agents/` `commands/` `skills/`(+ Gotchas) `lock/`、
`settings.json`(hooks)、`docs/adr/`、高リスク領域の `CLAUDE.md`。

---

## 5. 規格(`.yagura/harness.json` で調整)

### セキュリティ(CRITICAL=CI 自動停止)

入力検証(型/範囲/形式/長さ、ホワイトリスト優先、XSS=出力エスケープ、SSRF=URL 許可リスト) /
DB(ORM・プリペアド文、最小権限) / 通信(TLS1.2+、HSTS) / 認証(セッション ID 優先、
JWT はステートレス API のみ・短命、Refresh=HttpOnly+Secure) / 認証情報(bcrypt cost≥12 /
Argon2id、漏洩照合) / シークレット(env/Vault 専用、スキャン必須、定期ローテ) /
MCP(審査済みのみ、read-only 開始)。
依存ライセンスは `licenses`(allow/review/deny)で CI gate。

### 品質(`quality`)

構造: 引数 ≤3 / 行数 ≤40 / ネスト ≤3 / DRY(3回→関数化)。
静的解析は `quality.static_analysis`(言語別、警告ゼロ)。カバレッジは
`quality.coverage_targets_pct`(MVP/v1/Production)。負荷 p99 ≤ `quality.perf.p99_ms`。
hooks: pre-commit=Lint / pre-push=テスト+カバレッジ / CI=全テスト+Lint+脆弱性スキャン。

### 技術負債

Critical(セキュリティ/本番障害直結)=72h / High=次スプリント / Medium=四半期 / Low=バックログ。
新機能時間の一定割合を返済。負債追加時は `// DEBT: <理由> <期限>` 必須。

---

## 6. 観測性・監査(`audit`)

ログ=JSON(timestamp/level/service/trace_id/message、PII 含めない)。メトリクス=p50/p95/p99。
トレース=分散トレーシング(traceparent 伝播)。監査ログは **append-only + ハッシュチェーン、
AI による削除禁止**(`audit.ai_delete_forbidden`)、保持は `audit.retention_days`。
インシデントは `incident_sla`(SEV1-4 の ack/resolve)。SEV1/2=復旧最優先・ロールバック第一・
ポストモーテム 72h(blameless)。

---

## 7. キャパシティ(`context_capacity`)

使用率帯ごとに full / trim / compact / handoff_and_reset。意思決定は CLAUDE.md・ADR へ即時永続化
(ワーキングメモリは揮発性)。Skill=現コンテキスト注入 / Agent=独立コンテキスト生成。

---

## 8. モデル(`model_routing`)

複雑推論=`complex_reasoning` / 標準開発=`standard_dev` / CI 分類=`ci_classification`。
prompt caching 活用、中間結果はファイル保存して再利用。
モデル更新: ステージング先行評価 → 旧モデル補正ルール除去 → 固有失敗を Gotchas 追記 →
平日日中切替 → 切替後72hは指標を倍頻度監視 → 品質低下で即ロールバック。

---

## 9. RSI(再帰的自己改善)

RSI = Search × Verification × Compression。長期優位は Compression。モデルは交換可能で、
核心資産は Trace / Skill / Benchmark / Principle。ライフサイクル:
Experience → Pattern → Skill → Meta-Skill → Principle。
スキルは信頼度スコア(利用回数・成功率)と有効期限を持ち定期再検証。
Net Value = Gain − Complexity Cost(複雑度増加は負債計上)。
自己改善は **計測可能・gate 可能・監査可能** な範囲に限る — 自分のコードは書き換えない。

---

## 10. セッション起動手順

1. このファイルと該当 `CLAUDE.md` を読了 2. 最新 handoff を確認 3. 実行ログで当日完了を把握
4. failures ログで既知の地雷を把握 5. review フィードバックがあれば即対応
6. Plan.md があれば現在フェーズ確認 7.「起動完了。現在フェーズ: X。次アクション: Y」を出力。

---

ミッション: 保守性・安全性・法的安定性を備えた成果物の公開。完了シグナルは
`pipeline.completion_signal`。
