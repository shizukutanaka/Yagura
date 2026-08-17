# CLAUDE.md — yagura development guide

> このファイルは yagura を Claude Code / Windsurf で開発する際の context guide。
> m's harness G1.P 「Plan.md 必須記載項目」の構造に従う + Claude Code 推奨形式
> (Why / Map / Rules / Workflows) を採用。

## Why — yagura は何か、何でないか

yagura は **portfolio orchestrator + harness** で、m's sovereign computing stack
23+ projects (Breeze, Tessera, IZANAGI, Cotton, Strawberry, Otedama, ...) を
**1 つの zero-dep Go daemon** で運用する MCP server。

cortex flywheel 4 段階すべてを単体で機械化:
- ① Plan:    `plan_status` + `today`(優先度ランキング)+ `agents_md` + `feature_list`
  (Plan.md 起点の計画/引き継ぎ artifact 生成)
- ② Review:  `quality_check` + `ai_verify` + `test_audit` + `secretscan` + `gha_audit` + `pin_drift`
- ③ Release: `release_radar` (Plan.md aware ranking)
- ④ Alert-Fix: `alert_fix` (6 source × 4 severity の rule-based recommendation hub)

**yagura が *ない* もの**:
- LLM 呼出(全 rule-based、deterministic)
- 外部 SaaS 依存(GitHub API は scanner 経由のみ、optional)
- マルチエージェント orchestrator(MCP server 一品)
- code generation tool(yagura は audit/orchestrate のみ)

## Map — 94 internal packages

### Core orchestration
- `internal/registry` — 23+ projects の inventory CRUD
- `internal/project` — Project struct + validation
- `internal/mcp` — MCP server + 93 tool definitions
- `internal/audit` — JSONL audit log + replay
- `internal/config` — env / flag 設定
- `internal/today` — portfolio「今日注力すべき」ランキング(priority/PRs/CI/staleness
  スコア)。MCP `yagura_today` の handler から純関数 `Rank` を抽出し CLI `today` と共有
  (CLI parity)★ v0.37

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
  スコア化、人手プロセス項目はガイダンス提示。CLI `cc-security`、MCP
  `yagura_cc_security`(client が事実を集めて渡す content-based 契約)★ v0.36(MCP★ v0.102)
- `internal/reviewgate` — ② Review scanner 群(secretscan/aiverify/qualitycheck/astcheck)
  の結果を 1 つの合成判定(allow/review/block)へ束ねる deterministic gate。hard signal は
  secure-by-default で即 block。opsrisk(操作)/pathpolicy(パス)の ② Review 版の対。
  CLI `review-gate --dir . [--strict]`、MCP `yagura_review_gate`★ v0.36(MCP★ v0.102)

### Security sensors (observation)
- `internal/osv` — OSV.dev 脆弱性 API client
- `internal/scorecard` — OpenSSF Scorecard 取得
- `internal/scanner` — 24h 周期で project metadata 更新

### Agent handoff (Claude Code ↔ Windsurf)
- `internal/quotamonitor` — quota tracking + forecast + JSONL persist
- `internal/handoff` — handoff session management
- `internal/agentlauncher` — agent process spawn
- `internal/workspace` — session save/load
- `internal/harness` — .claude/ + MCP 監査(skill/subagent/workflow/settings/agent-config/plugin/mcp)。
  `AuditClaudeMd` は CLAUDE.md 構造監査(canonical 4 セクション/命令数/Lost in the Middle)、
  CLI `claudemd-audit`、MCP `yagura_claudemd_audit`★ v0.102。
  `AuditMCPConfig`(mcp_audit.go)は .mcp.json / tools list の tool-poisoning 監査。
  v0.110.0 で 2026 の脅威タクソノミー(MCP-38 arXiv 2603.18063、CSA unicode-injection)に
  整合: instruction-override / data-exfil / zero-width 隠し文字 / HTML・markdown コメント
  smuggling / base64 隠しペイロード(injectscan と同じ decode-then-rescan gate)/
  cross-tool shadowing(同名 tool 上書き — v0.110.0 まで header comment だけが主張して
  実装が無かった intent-vs-impl ギャップを解消)。CLI `mcp-audit`、MCP `yagura_mcp_audit`★ v0.35(taxonomy★ v0.110)

### Reasoning / multi-AI (★ v0.35)
- `internal/agentparallel` — 複数 AI への deterministic な並列 dispatch planner(LPT)
- `internal/riskreason` — Cyber Risk Reasoning Layer(CVSS+資産+到達性+攻撃性の複合判断)
- `internal/recovery` — self-healing orchestration の決定論的 recovery 判断(retry/replan/escalate)
- `internal/selfimprove` — harness レベル再帰的自己改善(RSI)の決定論的カーネル
  (自己メトリクス→優先度つき改善提案 + 後退検出)。spec: `docs/self-improvement.md`。
  MCP `yagura_self_improve`(都度評価)+ `yagura_self_improve_history`(監査証跡の
  再生、CLI `self-improve-history` 対)★ v0.35(history MCP★ v0.102)
- `internal/pathpolicy` — 変更パスを glob ルールで deny/review/allow する deterministic guardrail
  (CLI `path-policy` / `.yagura/paths.json` で ADR-0001 等を gate)★ v0.35
- `internal/opsrisk` — 操作の段階的自律性(auto/log/review/human)を capability・可逆性・
  影響範囲から分類する deterministic guardrail(Zenn governance 調査)★ v0.35

### Graph / dependencies
- `internal/projectgraph` — depends_on graph(neighbors / impact / stats)
- `internal/diffscan` — unified diff から追加行/削除行を抽出する純粋プリミティブ。
  snapshot ではなく delta の視点。`AddedLines`(「変更が新たに持ち込んだもの」)+
  `RemovedLines`/`RemovedGuards`(「外された安全装置」= error-check/recover/cleanup の
  削除)。CLI `diff-scan` が追加行に secretscan を適用 + 削除 guard を review-only 報告、
  MCP `yagura_diff_scan`★ v0.36(MCP★ v0.102)
- `internal/flowrisk` — 操作シーケンスの危険な *順序* を検出(temporal/flow の視点)。
  secret-read→network(exfiltration)/ fetch-untrusted→exec(injection→実行)/
  fetch-untrusted→write を taint-flow 的に走査。`Analyze`(純関数)+ `ClassifyTool`
  (ツール名→capability)。CLI `flow-risk`(1 行 1 操作名、--strict で high flow gate)、
  MCP `yagura_flow_risk`★ v0.36(MCP★ v0.102)
- `internal/coverage` — scan の盲点を meta 視点で数値化。全ファイルを「解析可能/
  未対応ソース(盲点)/非ソース」に分類し coverage 比率を報告。CLI `coverage --dir .
  [--min R]`、MCP `yagura_coverage`★ v0.36(MCP★ v0.102、v0.101 の "coverage は CLI-only"
  という自己申告ギャップを解消)
- `internal/assertcheck` — テストのアサーション密度を分析(ソクラテス新視点)。
  hollow test(assertion 無し)= 常に緑でも何も証明しない。`Scan(files)` →
  `Report{HollowFiles, AvgDensity, ...}`。CLI `assert-check --dir . [--max-hollow F]`★ v0.36
- `internal/errpolicy` — エラー診断可能性を go/ast で計測(ソクラテス新視点)。
  「失敗時に *どこで・なぜ* 分かるか」の軸。naked `return err`(context 喪失)vs
  wrapped `fmt.Errorf(...%w...)` の wrap 率 + `_ = call()` の blank-discard 検出。
  type-free(Go の慣習に依拠)。CLI `err-policy --dir . [--min-wrap R]`★ v0.36
- `internal/complexity` — 循環的複雑度(McCabe、gocyclo 互換)を go/ast で計測
  (ソクラテス新視点)。「そもそも完全にテストできるか」という testability の前提
  条件 = 全パス網羅に要するテスト数の下限。関数別スコア + しきい値超過 flag。
  CLI `complexity --dir . [--max N] [--strict]`、MCP `yagura_complexity`★ v0.36
- `internal/nestdepth` — 関数の最大制御構文ネスト深度を計測(ソクラテス新視点 XV)。
  complexity が分岐パスの *数* を測るのに対し、こちらは最深経路の *深さ* を測る直交軸。
  complexity 同値でも flat な guard 4 個と 4 段入れ子では可読性が違う——その差を捉える。
  if/for/range/switch/select の本体で +1、`else if` 連鎖は同一深度、FuncLit は非算入。
  guard-clause/early-return 規律の機械化。default threshold 4(超過で flag、5=medium/6+=high)。
  dogfood で 3 件(apidoc.scanFile=6, deadcode.collectCandidates=5, plantracker.Parse=5)を
  検出 → v0.88–v0.89 で 3 件すべてヘルパ分解し、`nest-depth --dir .` は repo 全体で 0 件
  (全 1337 関数が深さ ≤4)。pyramid-of-doom 軸を自リポジトリで clean 化済み。
  CLI `nest-depth --dir . [--max N] [--strict]`、MCP `yagura_nest_depth`★ v0.85
- `internal/globalcheck` — package-level の *可変* グローバル変数を検出(ソクラテス新視点 XVI、
  共有可変状態の軸)。synccheck=ロックコピー、ctxcheck=context 伝播 を見るが、データ競合と
  テスト不能性の最大の源=可変グローバルは未計測だった。const / error sentinel / 読取専用
  table は自動的に対象外(再代入されないため)。*実際に mutate される* var のみ flag。
  ファイルは dir(=package)単位で束ねる。保守的: 名前がローカル宣言で shadow される場合は
  型情報なしで断定不能のためスキップ(false positive を出さない)。exported=high/unexported=
  medium。dogfood で 140 中 5(tray の Win32 callback globals 4 + serverVersion の init 注入)。
  CLI `global-check --dir . [--strict]`、MCP `yagura_global_check`★ v0.86
- `internal/typeassert` — panic しうる単一値の型アサーション `x.(T)` を検出(ソクラテス新視点
  XVII、暗黙 panic ハザード軸; forcetypeassert 相当)。astcheck は明示的 panic() を見るが、
  `v := x.(T)` は x が T でなければ *暗黙に* panic する。安全形は `v, ok := x.(T)`。errwrap は
  *error* 型(errors.As 推奨)を見るが、本レンズは型を問わず panic 安全性のみを見る(comma-ok
  形は安全なので除外)。2 パス: comma-ok 位置を集める→残りを flag。_test.go / TestXxx 除外。
  dogfood で 5(dedupe の container/list `Value.(*entry)` 3 + tools.go の map sort comparator 2、
  いずれも構造上安全だが panic 面を可視化)。CLI `type-assert --dir . [--strict]`、MCP
  `yagura_type_assert`★ v0.87
- `internal/cognit` — 関数の認知的複雑度(Cognitive Complexity, Sonar/gocognit 互換)を
  go/ast で計測(ソクラテス新視点 XVIII、Qiita/Zenn 調査、Go Conference 2022 の go/ast 実装
  発表が題材)。complexity=分岐パス数、nestdepth=最深ネスト深度——どちらも単独では「人間が
  読んでどれだけ理解しづらいか」を捉えない。cognit は両軸を直感に合わせ統合: フロー破壊構造
  (if/for/switch/select/論理演算子列/ラベル付き分岐)に基本 +1、ただし *ネスト段で重み付け*
  (3 段の if は +4)。switch は case 数に依らず +1(flat 多分岐は人間に優しい — McCabe との
  決定的差)。else if は構造増分のみ、クロージャはネスト +1、直接再帰は +1。既定 gate 15
  (golangci-lint 推奨 10-20)。型情報不要・決定論的。CLI `cognit --dir . [--max N] [--strict]`、
  MCP `yagura_cognit`★ v0.91
- `internal/prealloc` — range ループ内で事前確保なしに append され続けるスライスを go/ast で
  検出する *パフォーマンス軸* のレンズ(ソクラテス新視点 XIX、Qiita/Zenn 調査、
  alexkohler/prealloc 由来)。既存 ~19 レンズは全て correctness/readability/safety/architecture
  を測るが、「無駄に遅くないか」を問うレンズは皆無だった——その盲点を塞ぐ最初の性能軸。
  `var s []T` / `[]T{}` / `make([]T,0)` を range ループのトップレベル `append` で伸ばす形を
  flag(`make([]T,0,len(coll))` で再確保を消せる)。偽陽性を避け保守的: range のみ(回数既知)、
  トップレベル append のみ(条件分岐内は除外)、確保済み形は除外。型情報不要・決定論的。
  dogfood で 52 候補、うち textbook 3 件(coupling.parseImports / initsh・initps1.uniqueSorted)を
  修正しテスト不変、残りは perf backlog。CLI `prealloc --dir . [--strict]`、MCP `yagura_prealloc`★ v0.92
- `internal/thelper` — テストヘルパーが `t.Helper()` を呼んでいるかを go/ast で検査する
  *テスト品質軸* のレンズ(ソクラテス新視点 XX、Qiita/Zenn 調査、kulti/thelper 由来)。
  assertcheck は assertion 密度を測るが、ヘルパー自身の衛生は未計測だった。`*testing.T`/
  `*testing.B`/`testing.TB` を受け取りながら `t.Helper()` を呼ばないヘルパーは、失敗時の行が
  *ヘルパー内部* を指し、どのテストが落ちたか分からなくなる。保守的: リテラル `testing.X` のみ、
  エントリポイント(Test/Benchmark/Fuzz/Example)・`_`/無名引数は除外、Helper() の *完全な不在*
  のみ flag(style ノイズを出さない)。テストが主題なので _test.go も走査する(L4 の逆)。
  dogfood で 68 ヘルパー中 1 件(mcp の depsWithPinDrift)を検出し修正、`thelper --dir .` は 0 に。
  CLI `thelper --dir . [--strict]`、MCP `yagura_thelper`★ v0.93
- `internal/ifacebloat` — 名前付きインターフェースのメソッド数を go/ast で計測する
  *インターフェース設計軸* のレンズ(ソクラテス新視点 XXI、Qiita/Zenn 調査、
  sashamelentyev/interfacebloat 由来)。Rob Pike の格言「the bigger the interface, the
  weaker the abstraction」を機械化。method = 1 per name、埋め込みインターフェース = 1、
  型ユニオン項 = 1。_test.go 除外(モック用大インターフェースは意図的)。default threshold 10。
  severity: medium(> threshold)/ high(> 2× threshold)。Interface Segregation 違反を可視化。
  dogfood で Yagura 自身のインターフェース全件はしきい値以内。
  CLI `iface-bloat --dir . [--max N] [--strict]`、MCP `yagura_ifacebloat`★ v0.94
- `internal/paramcheck` — 関数のパラメータ数(Fowler "Long Parameter List" smell)を
  go/ast で計測(ソクラテス新視点、complexity の *水平方向の対*)。complexity だけを
  gate にすると巨大関数をヘルパに割って複雑度を下げつつ 6・7 個と引数を引き回す退行を
  見逃す——その盲点を塞ぐ。引数は名前単位で計数(`a, b int`=2)、可変長=1、レシーバ除外。
  CLI `param-check --dir . [--max N] [--strict]`、MCP `yagura_param_check`★ v0.65
- `internal/flagarg` — bool 型引数(Fowler "Flag Argument" smell)を go/ast で検出
  (ソクラテス新視点)。complexity=分岐パス数、paramcheck=引数総数を補完する「引数の
  意味的制御結合」軸。`process(data, true)` の呼び出し元で "true" が何を意味するか
  即座にわからない問題を可視化。threshold>=1 bool param でフラグ(default=1)。
  _test.go / TestXxx 関数 / *bool ポインタは除外。low(1 bool)/medium(2+ bool)。
  CLI `flag-arg --dir . [--min-bools N] [--strict]`、MCP `yagura_flag_arg`★ v0.66
- `internal/returncheck` — 戻り値の数(出口の幅)を go/ast で計測(ソクラテス新視点)。
  paramcheck が入口(引数)の広さを測るなら、returncheck は出口(戻り値)の広さを測る。
  Go 慣用句 `(T, error)` = 2、`(T1, T2, error)` = 3 は許容範囲(flag しない)。
  4 以上は「関数がやりすぎ」臭いとして flag。_test.go / TestXxx / FuncLit 除外。
  low(4-5 returns)/medium(6+ returns)。paramcheck + flagarg + returncheck で
  「入力幅 + 出力幅 + 意味的制御結合」三軸のシグネチャ全体像が揃う。
  CLI `return-check --dir . [--max N] [--strict]`、MCP `yagura_return_check`★ v0.67
- `internal/errdiscard` — コールサイトで error を返す関数が ExprStmt として呼ばれている箇所
  (= error が暗黙的に捨てられている)を二パス AST 走査で検出(ソクラテス新視点 IV)。
  シグネチャ三軸(paramcheck+flagarg+returncheck)は定義側をプロファイルしたが、
  呼び出し側の規律は見えていなかった——その盲点を塞ぐ。同一パッケージ内のコールのみ
  (型情報不要・zero-dep)。severity: medium。CLI `err-discard --dir . [--strict]`、
  MCP `yagura_err_discard`★ v0.68
- `internal/coupling` — package 間 import 結合度(アーキテクチャの絡まり)を計測
  (ソクラテス新視点)。fan-in(Ca)/fan-out(Ce)/instability I=Ce/(Ca+Ce)+ Stable
  Dependencies Principle 違反(安定 package が より不安定な package に依存)。
  projectgraph(registry の宣言的 depends_on)と違い実ソース import から導出。
  CLI `coupling --dir . [--module M] [--strict]`、MCP `yagura_coupling`★ v0.36
- `internal/apidoc` — exported API のドキュメント規律を go/ast で計測
  (ソクラテス新視点)。package が依存側に約束する公開契約面。doc コメントの無い
  exported func/type/const/var/method = 仕様の無い契約。documented 率 + 未文書化
  シンボル一覧。godoc 規律(golint 互換)。CLI `api-doc --dir . [--min-doc R]`、
  MCP `yagura_api_doc`★ v0.36
- `internal/deprank` — パッケージ import グラフの in-degree(被参照数)を go/parser で算出
  (ソクラテス新視点 V)。全先行レンズは関数レベル/コールサイトレベルで動作していたが、
  *パッケージグラフ構造結合*——どの内部パッケージが変更時に最大 blast radius を持つか——は
  どのレンズも測っていなかった。in-degree が高い内部パッケージ = 変更時に多くの importers を
  コンパイルエラー/型エラーリスクにさらす。threshold(default 5)超過を severity 付きで flag。
  CLI `dep-rank --dir . [--module M] [--threshold N] [--top N]`、MCP `yagura_dep_rank`★ v0.69
- `internal/hotspot` — 関数キー付きレンズ群を同じ file set に適用し、複数レンズが独立に
  指摘した関数(収束シグナル)を高信頼リファクタ対象として報告(ソクラテス新視点 VI、
  synthesis)。個々のレンズは偽陽性を持つが、独立シグナルの *収束* は単一シグナルより高信頼——
  引数 6 個 *かつ* 戻り値 4 個 *かつ* 複雑度超過の関数はほぼ確実に本物。既存レンズを再利用
  するだけでロジック再実装なし。非テストかつパース可能な .go に scope を確定してから委譲
  (下流レンズの _test.go/parse-error 挙動差を吸収)。2 レンズ収束=medium / 3+ 収束=high。
  v0.70 発足時は complexity/paramcheck/flagarg/returncheck の 4 レンズのみを束ねていたが、
  その後 cognit/nestdepth/typeassert/namecheck/ctxcheck/errwrap/nakedret/prealloc の 8 レンズ
  (いずれも File/Line/Func を報告する同一規約)が追加されても hotspot は更新されず、収束母数が
  21 レンズ中 4 つ(19%)まで陳腐化していた——**hotspot 自身がソクラテスの盲点**になっていた。
  v0.95 で 12 レンズへ拡張、dogfood で収束ホットスポットが 0 件 → **69 件**(high 13 件)に
  急増して発見(thelper はテスト専用ファイルが主題のため対象外、errdiscard/synccheck/
  predeclared/errpolicy は Func と同型の関数キーを持たないため対象外)。
  CLI `hotspot --dir . [--min-lenses N] [--strict]`、MCP `yagura_hotspot`★ v0.70
- `internal/lensoverlap` — hotspot が束ねる 12 レンズ間の指摘関数集合を Jaccard 係数で
  比較する *メタ軸* のレンズ(ソクラテス式自己監査、v0.99.1 の問答の直接の帰結)。
  selfimprove は Darwin Gödel Machine の「produce → trial → select」を引用し skill には
  retire 提案(harness の skill-audit)があるが、quality lens 自身にはその "select"
  (淘汰・統合)の仕組みが一つも無かった——complexity/cognit/nestdepth のような似た軸が
  実際どれだけ相関しているか検証する手段が無いまま増え続けていた。統合すべきか否かの
  判断は本レンズの役目ではなく、判断材料(相関の実測値)を提供するだけ(observability、
  pass/fail gate ではない)。dogfood で最大重なりは `cognit`↔`complexity` の Jaccard
  **0.39**(medium 閾値 0.4 未満)——「冗長」仮説を裏付ける結果にはならず、他の全ペアは
  ≤0.03 で直交性を実証。ソクラテス的検証: 仮説を測定で裏切られても、その通りに報告する。
  CLI `lens-overlap --dir .`、MCP `yagura_lens_overlap`★ v0.100
  (12-lens expansion★ v0.95); `ReleaseReadinessExt` top finding resolved v0.71
- `internal/namecheck` — 関数名・error 変数・error 型のシグネチャ整合を検査する *意味軸*
  のレンズ(ソクラテス新視点 VII)。これまで全レンズは *構造* を測ったが、名前と振る舞いの
  整合は未計測の契約だった(W2)。is/has/can/should/must 述語は第一戻り値 bool を、Get/New
  接頭辞は戻り値を返すべき(v0.73)。v0.74 で errname 準拠の error 命名規約を追加: sentinel
  は Err…/err… 接頭辞、Error() string メソッド持ち型は Error/Errors 接尾辞。Qiita/Zenn 調査
  で「Go コミュニティ標準だが Yagura 未計測」だったルールを取り込み。語境界(接頭辞の次が
  大文字)で "Hash" を "has"、"Errno" を "Err" と誤認しない。型情報不要・決定論的。
  CLI `name-check --dir . [--strict]`、MCP `yagura_name_check`★ v0.73
- `internal/ctxcheck` — context.Context の取り扱い規律を検査する *並行性軸* のレンズ
  (ソクラテス新視点 VIII、Qiita/Zenn 調査)。context-not-first(ctx は第一引数であるべき、
  *testing.T 先頭の test helper は例外)+ contained-ctx(struct field に context を溜めない、
  Go 公式 blog "Contexts and structs")。canonical linter containedctx / 引数順チェックを機械化。
  literal `context.Context` selector のみ照合(別名 import は型解決要のため対象外)。型情報
  不要・決定論的。dogfood で `writeSSEPinDrift(w, ctx, …)` を発見し `(ctx, w, …)` に修正。
  CLI `ctx-check --dir . [--strict]`、MCP `yagura_ctx_check`★ v0.75
- `internal/errwrap` — Go 1.13 エラーラッピング規約違反を go/ast で検査する *error-chain
  健全性軸* のレンズ(ソクラテス新視点 IX、Qiita/Zenn 調査、go-errorlint 由来)。errpolicy が
  wrap *率* の meta 指標を測るのに対し、errwrap は「正しくラップできているか」を測る。
  non-wrapping-verb(%v でなく %w)/ err-value-compare(== でなく errors.Is)/
  err-type-assert(x.(T) でなく errors.As)。エラー値らしさは命名規約で type-free 判定。
  dogfood で 14 件の error-chain 切断リスク(13× `err.(scanner.ErrorList)` + 1× io.EOF 比較)を
  検出し全て errors.As/Is へ修正。CLI `err-wrap --dir . [--strict]`、MCP `yagura_err_wrap`★ v0.76
- `internal/synccheck` — sync ロック型(Mutex/RWMutex/WaitGroup/Once/Cond)を含む型の値
  コピー誤用を go/ast で検査する *concurrency-safety 軸* のレンズ(ソクラテス新視点 X、
  Qiita/Zenn 調査、go vet copylocks 由来)。値レシーバ(mutex-value-receiver)/ 値引数
  (mutex-by-value-param)/ 値返却(mutex-by-value-return)。2 パス: ファイル集合全体から
  TypeSpec を集めてロック含有型の集合を構築(1 hop の固定点反復で `Outer{ Inner }` 推移
  も解決)→ FuncDecl を走査。別名 import は対象外。型情報不要・決定論的。dogfood で
  0 違反(Yagura は元々 21 lock-bearing struct すべてで pointer receiver を使用)。
  CLI `sync-check --dir . [--strict]`、MCP `yagura_sync_check`★ v0.77
- `internal/nakedret` — named result を持つ長い関数内の naked return を go/ast で検出する
  *可読性軸* のレンズ(ソクラテス新視点 XI、Qiita/Zenn 調査、alexkohler/nakedret 由来)。
  returncheck が戻り値の *数* を測るのに対し、nakedret は本体の中の `return` の書き方を見る。
  短い関数なら無害だが、閾値(既定 30)行を超える関数末尾の引数なし return は「今何が返るか」
  を読み手がスクロールで named result を追わねば分からずバグの温床。naked return は named
  result を持つ関数でしか書けないので named 判定で対象を絞る。クロージャ内 naked return は
  最も内側の関数に帰属。dogfood は既定で 0(naked return は 11/27 行関数のみ、閾値以下)。
  CLI `naked-ret --dir . [--max-lines N] [--strict]`、MCP `yagura_naked_ret`★ v0.78
- `internal/predeclared` — Go の組み込み識別子(len/cap/new/error/min/max/clear 等 39 個)を
  shadowing する宣言を go/ast で検出する *correctness 軸* のレンズ(ソクラテス新視点 XII、
  Qiita/Zenn 調査、nishanths/predeclared 由来)。`cap := capacity` のような shadowing は
  そのスコープで組み込み `cap(s)` を呼べなくする静的バグの温床。引数・名前付き戻り値・
  関数/型/定数/変数宣言・短縮宣言・range key/value を対象。method (receiver 付き) は名前空間
  が分かれるため除外。`--ignore` で許容識別子を列挙可。dogfood で 20 件(全て cap/min/max の
  Go 1.21+ builtin shadowing)を検出し全リネーム修正。CLI `predeclared --dir . [--ignore a,b]
  [--strict]`、MCP `yagura_predeclared`★ v0.79
- `internal/calibrate` — 数値系レンズ(complexity/params/returns/func-lines)のしきい値を
  *コーパス由来* に校正する meta レンズ(ソクラテス新視点 XIII、quality-lens-spec の弱点
  W3「threshold arbitrariness」への対応)。findings ではなく分布(min/median/p90/p95/p99/
  max/mean + ceil(P95) の suggested threshold + 現行 default 超過数)を出す。named function
  のみ走査(FuncLit 除外)、complexity は complexity レンズと同一の McCabe 定義、percentile は
  線形補間(R-7)。v0.81 で outlier 検出を追加: Tukey 外側フェンス(Q3+3·IQR)*かつ* 慣習
  しきい値超過の関数を「直すべき極端値」として列挙(積を取ることで returns/params の
  `(T,error)` 等の慣用ノイズを排除)。dogfood(1280 関数)で 41 outliers(543 行 run、
  complexity-32 plantracker.Parse、レンズ自身の param 過多 3 件)を surface。
  CLI `calibrate --dir . [--json] [--write]`、MCP `yagura_calibrate`★ v0.80(outliers v0.81)。
  v0.103.0 で feedback loop を閉鎖(W3 完全対応): `--write` が suggested_threshold を
  `<dir>/.yagura/thresholds.json` へ書き出し、`complexity`/`param-check`/`return-check`/
  `naked-ret` の CLI が(他 lens の `.yagura/<tool>.json` custom-rule 自動検出と同じ規約で)
  これを自動検出して既定しきい値を上書きする。明示的な `--max`/`--max-lines` は常に優先
  (`flagWasSet` で判定)。「測るだけで適用されない」半分実装状態を解消——ただし *自動適用は
  しない*: `.yagura/thresholds.json` を書くかどうかは利用者の明示的な選択(`calibrate --write`
  を実行して初めて発生)であり、Yagura 自身のリポジトリには意図的にコミットしていない
  (このリポジトリの --strict ゲートを本リリースで変えないため)。
- `internal/regress` — old/new 2 状態を比較し、関数ごとの品質メトリクス
  (complexity/params/returns/func-lines)が *悪化* した箇所を報告する **時系列/回帰軸**
  のレンズ(ソクラテス新視点 XIV)。既存 ~13 レンズは全て単一スナップショットだが、CI で
  最重要なのは「この変更が品質を後退させたか」。(File, Func) で突き合わせ、増加メトリクスを
  Regression として列挙。Crossed = new 値が慣習しきい値超過。calibrate.FuncMetrics を再利用
  (単一情報源)。品質ラチェット = `regress --old DIR --new DIR --strict` で Crossed があれば
  CI fail。CLI `regress --base <git-rev> --strict`(v0.84: old 側を `git archive`+stdlib
  archive/tar で git revision から直接読む。`yagura regress --base origin/main --strict` が
  CI 一行ゲートに)/ `--old DIR`、MCP `yagura_regress`★ v0.83。v0.104.0 で calibrate
  feedback loop(v0.103.0)との整合性を解消: `CompareWithThresholds(old,new,overrides)`
  を追加(`Compare` はこれの nil-overrides ラッパー)、CLI は `<new>/.yagura/thresholds.json`
  を他 4 レンズと同じ規約で自動検出、MCP は任意の `thresholds` パラメータを受付
  (client 明示、ファイルシステム access なし)——v0.103.0 が閉じ残していた「calibrate
  対象 4 metric のうち regress だけ校正値を無視する」不整合を解消★ v0.104
- `internal/deadcode` — 自 package 内で参照されない unexported 宣言を検出
  (ソクラテス新視点、apidoc の非公開側の双対)。Go コンパイラが弾かない package
  レベル未使用 func/type/const/var。unexported = 閉じた世界なので保守的かつ安全に
  到達不能を断定(method/init/main/test 宣言は除外)。CLI `dead-code --dir .
  [--strict]`、MCP `yagura_dead_code`★ v0.36
- `internal/recvcheck` — メソッドレシーバの自己一貫性を go/ast で検査
  (ソクラテス新視点、unit を自分自身の他の部分と照らす軸)。レシーバ名の不揃い /
  値・ポインタ混在(満たす interface が変わる実害)/ this・self 等非慣習名。
  golint/govet 隣接。CLI `recv-check --dir . [--strict]`、MCP `yagura_recv_check`★ v0.36
- `internal/codehealth` — 保守性レンズ群(complexity/apidoc/deadcode/recvcheck/astcheck/
  assertcheck)を package 別 grade(A-F)へ合成(ソクラテス新視点 synthesis)。
  reviewgate(security 合成)の maintainability 版。`Score`(純関数)+ `Analyze`
  (各レンズ実行)。CLI `code-health --dir . [--min-grade G]`、MCP `yagura_code_health`★ v0.36

### feature 窓は expanding / sliding を選べる(★ v0.127.0、論文由来 + null result)
- `walkforward.Options.WindowDays` — 0(既定)は **expanding**(履歴先頭から cut まで)、
  >0 で **sliding**(直近 N 日のみ)。`Report.WindowMode` と note に必ずどちらか明記する。
- 根拠は **McIntosh & Kamei, "Are Fix-Inducing Changes a Moving Target?", IEEE TSE 44(5),
  2018**(Qt / OpenStack の 37,524 changes)。JIT モデルは「過去の fix-inducing change は
  将来のものと似ている」と仮定するが、**訓練から 1 年後には AUC と calibration が大きく低下**
  する(訓練に使った指標の値がシフトするため)。同論文は **6 か月以上**の履歴での訓練を推奨。
- **本リポジトリでは null result**: 追跡履歴が約 64 日しかなく、30 日 sliding は
  expanding と完全に同一(最初の 108 commits が 30 日未満に収まる)。14 日でようやく
  fold 2 が 108→89 commits に縮むが、lift はほぼ動かない
  (size_loc 6.08→6.09 / churn_count 5.93→5.96)。**論文の効果は 1 年規模の話であり、
  2 か月のリポジトリでは検証できない**——効果が出たふりをしないこと。
- 窓を短くする場合は「論文の推奨は 6 か月以上であり、それより短い窓は本プロジェクト独自の
  探索であって推奨ではない」と note に明記する(既に実装済み)。

### 検証窓には gap を置ける(★ v0.126.0、論文由来 + 自己修正)
- `walkforward.Options.GapDays` — feature 窓の末尾から N 日以内のコミットは
  **特徴にもラベルにも使わない**。gap が label 窓を食い尽くした fold は理由付きで skip。
- 根拠は JIT 欠陥予測の **verification latency**: ラベルは特徴より遅れて到着するため
  「訓練とテストの間には訓練データを作れない固定の時間差がある」、変更を clean と
  ラベルできるのは待機期間の経過後で、**latency を無視すると性能推定が偽になる**
  (ACM CSUR 2022 の JIT-SDP サーベイ、Cabral & Minku TSE 2022 ほか)。
- **正直な但し書きを必ず添えること**: 論文の待機期間は *online* JIT モデルの日数であり、
  ここで実装したのは *offline* 窓間の日数 gap。**類似の論法であって同じプロトコルではない**。
- gap の有無は **常に応答に明記**する。gap=0 のときは「cut 直後の fix がラベルに残る」
  ことと「最新 label 窓は HEAD に隣接するので not-fixed は "まだ修正されていない" だけ
  かもしれない(片側ラベルノイズ)」を note に書く。
- v0.125.0 の自己修正: **label 窓は全 fold 等サイズ**(最後の fold に残りを吸わせると
  陽性数と baseline が変わるのに平均では 1 票のままで歪む)。**K は fold ごとに公開**
  (rows/10 が 1 に潰れると precision が 0/1 になり lift が不安定 — 見えないと気づけない)。
- 実測: gap=0 → gap=3日 で **全 scorer の lift が低下し、順位も入れ替わる**
  (size_loc 6.08→3.08、churn_count 5.93→**4.40 で首位**)。cut 直後の fix を外すと
  サイズ交絡が弱まりプロセス指標が残る、という読み方ができる。

### 検証は時系列順を保つ(★ v0.125.0、論文由来 + 自己反証)
- `internal/walkforward` — 履歴を Folds+1 区間に切り、fold ごとに「区間 i+1 より前」で
  特徴、「区間 i+1」でラベルを作る **walk-forward**。各 fold は
  `defectdataset.BuildWindows`(本リリースで `Build` から抽出)を再利用する。
- 根拠は **Falessi et al., EMSE 2020**: cross-validation / bootstrap / walk-forward の
  うち順序を保つのは walk-forward のみで、同一分類器・同一プロジェクトでも 10-fold と
  walk-forward の **AUC 差 [-0.20,+0.22]、45% のケースで有意**。
- **`yagura_process_risk` の headline は walk-forward。** 同一窓の数値は消さずに
  `in_window_agreement` として `predictive:false` + 理由付きで残す
  ——「誤解を招くと自分で書いた数値を出し続けない」。v0.123.0 の誤りの是正。
- **Scorer は注入可能**にして同一プロトコルで競合シグナルを比較する。陽性 0 の fold は
  捏造 0 を混ぜず skip して数える。**逆順 scorer が勝てないテストを必ず置く**
  (常に成功する検証器は何も検証しない)。
- 実測(このリポジトリ、3 fold): size_loc 6.08× / churn_count 5.93× / complexity 3.66× /
  **relative_churn 1.14×**。v0.119.0 が選んだ relative_churn が最弱という自己反証。
  ただし **size_loc の勝ちはサイズ交絡**(El Emam et al., TSE 2001)であり発見ではない。
  不都合な baseline を隠さないために載せる。重み変更は複数リポジトリ検証後。

### データセットは必ず時間分割する(★ v0.124.0、論文由来 + 自己反証)
- `internal/defectdataset` — git 履歴から **ファイル単位の欠陥データセット**(行=ファイル、
  列=メトリクス群 + 末尾に fix ラベル)を生成。JSON / CSV。MCP `yagura_defect_dataset`。
  形式は **Zimmermann, Premraj & Zeller "Predicting Defects for Eclipse"(PROMISE 2007)**
  ——pre-release メトリクス + post-release 欠陥数——に倣う。
- **既定で時間分割(feature window 70% / label window 30%)。** Eclipse データセットが
  時間を分けているのは、同一期間から特徴とラベルを取ると「過去を予測する」リーク付き
  データになるため。**分割を切る(split_ratio=0)場合は `Meta.Leakage=true` と警告文を
  データ自身に刻む**——黙って混ぜない。
- **自己反証**: v0.123.0 の precision@10 = 0.60(lift 2.27×)は同一期間の特徴とラベルを
  突き合わせていた。時間分割で測り直すと **0.25 / baseline 0.15 = lift 1.68×**。
  リークを外せば数字は落ちる——落ちた事実をそのまま記録すること。
- 実測の含意: このリポジトリでは **変更回数(9.64×)が相対 churn(1.22×)より遥かに
  クラスを分ける**のに、processrisk は両者を等重みにしている。データセットはこういう
  検算のために在る(重み変更は別リリース + 複数リポジトリでの検証後)。

### ランキングは自己較正する(★ v0.123.0、論文由来)
- `internal/fixhistory` — **SZZ 第 1 段**(Śliwerski/Zimmermann/Zeller, MSR 2005)の
  fix コミット特定を実装し、`yagura_process_risk` の応答に `fix_history` と
  `validation`(precision@10 / random baseline / lift)を載せる。git 読み出しは
  churn と共有で 1 回のまま(単一 seam)。`churn.Commit` に `Subject` を追加
  (`|` を含みうるので format 末尾)。
- **実装は第 1 段のみ**で、第 2-3 段(行 blame 遡行 → bug 導入コミット特定)は未実装
  ——「fix が触れたファイル」は欠陥出現の *近似* であること、message ベース特定の限界
  (fix と書かない fix の見逃し)を package doc に明記している。
- **単語断片を fix と誤認しない**(prefix/suffix/fixture)——SZZ 追試文献が指摘する
  古典的偽陽性。単語境界 regex + テスト固定。Revert/Merge は除外。
- **検証器は落ちうるものであること**: 逆順ランキングが 0 点になるテスト
  (`TestValidate_InvertedRankingScoresLow`)を必ず持つ——常に成功する検証器は何も
  検証しない。fix データが無いリポジトリは `valid=false` + 理由(偽スコアを出さない)。
  precision は必ず **baseline(ランダム期待値)と lift を併記**する。
- 実測(このリポジトリ): 26/142 が fix コミット、precision@10 = 0.60 vs baseline 0.26
  = **lift 2.27×**。数値は較正ではなく単一リポジトリの 1 観測点——応答に毎回計算して
  載せるのはそのため(他リポジトリでは各自の数値が出る)。

### alert は「件数予算」を持つ(★ v0.122.0、論文由来)
- `internal/alertfix` に 8 番目の source `process_risk` を追加(churn × ownership を
  注意配分レイヤへ供給)。**1 プロジェクトあたり最大 3 件**(`MaxProcessAlertsPerProject`)。
- 根拠は **Sadowski et al., "Lessons from Building Static Analysis Tools at Google",
  CACM 2018**: 解析結果を自動起票していた初期方式は **起票 bug の 84% が未修正**。
  同論文の "effective false positive" = 「開発者が見た後に何も行動しなかった指摘」——
  技術的に正しくても理解・行動につながらなければ偽陽性と同じ害。code review に出す
  チェックは effective FP 率 10% 未満が要件で、「偽陽性率を決めるのは開発者であって
  ツール作者ではない」。
- **新しい alert source を足すときは必ず件数予算・沈黙条件・行動可能性をセットで設計する**:
  ①上限件数 ②健全時は 0 件 ③全 alert に「なぜ出たか(証拠)」と「次に何をするか
  (recommendation + suggested_tool)」。①②③ は各々テストで固定すること。
- **合成スコアに絶対閾値を置かない**(v0.122.0 の実失敗): percentile 平均のスコアは
  リポジトリごとに意味が変わり、実測で最上位が 0.695 だったため 0.85 の閾値は
  「絶対に発火しない死んだ条件」だった。発火判定は解釈可能な絶対条件
  (相対 churn >= 1.0 / 所有率 < 0.5)に置き、スコアは **並び順専用**にする。
  選抜は score 順・**提示は severity 順**(他 source の rankAlerts と規約を揃える)。

### プロセス指標 > 製品指標(★ v0.121.0、論文由来 + 自己反証)
- `internal/processrisk` — churn(v0.119)と ownership(v0.120)の **プロセス指標のみ**を
  合成して順位付けする。根拠は **Rahman & Devanbu (ICSE 2013)**(製品指標は stasis =
  リリース間で動かないため同じファイルを指し続ける)と
  **Majumder, Mody & Menzies (EMSE 2022、700 projects / 722,471 commits)**
  ——プロセス指標 recall 98% / **AUC 95%** に対し製品指標 recall 44% / **AUC 54%**(ほぼ偶然)。
- **これは v0.119.0 の自己反証**: churn の `RiskScore = 相対churn × 複雑度` は、ほぼ偶然と
  変わらない信号に乗算という同等の重みを与えていた。processrisk では複雑度を
  **表示するが採点に使わない**(`TestScore_ComplexityIsReportedButNotScored` が固定)。
  Tornhill hotspot は独立した公表手法なので churn 側はそのまま残し、**両方の順位を
  見比べられるようにする**(都合のいい方だけ見せない)。
- 各シグナルは **リポジトリ内 percentile に正規化**してから平均する(単位の大きさだけで
  特定シグナルが支配するのを防ぐ)。**ただし重み配分は研究由来ではない**——
  「どのシグナルを使うか」だけが研究に基づく。応答の `note` にもその旨を埋め込むこと。
- 新しいリスク指標を足すときは、**プロセス指標か製品指標かを明示**し、製品指標を
  採点に混ぜる場合は理由を書くこと。

### 所有権 / ownership(★ v0.120.0、論文由来)
- `internal/ownership` — behavioral code analysis のもう半分「誰が書いたか」。
  根拠は **Bird, Nagappan, Murphy, Gall, Devanbu, "Don't Touch My Code!", ESEC/FSE 2011**
  (Windows Vista/7 で、minor 寄与者数と最大所有者の所有割合が pre-release fault /
  post-release failure と関係)。4 指標は論文の定義そのまま:
  Minor(< **5%**)/ Major(>= 5%)/ Total / Ownership(最大寄与者の割合)。
  閾値は `TestMinorThresholdIsFivePercent` が固定——変えた時点で論文の指標ではなくなる。
  **ランキングは Ownership 昇順**(所有権が低いほど危険、という論文の含意どおり)。
- **論文外の拡張は必ず分けて報告すること**: `ai_proportion` / `human_ownership` /
  `top_human_owner` / `fully_ai_authored` は本リポジトリ独自のヒューリスティックで
  研究の裏付けが無い。MCP 応答でも `research_metrics` と `extension_metrics` を
  別配列で返す。Bird らの機序は「専門性の低い寄与者が誤りを入れる」であって
  「AI が全部書いた」状況は論文の射程外——研究の権威を借りて語らない(honest capability)。
- git 読み出しは `churn.ReadGitLog` の **単一 seam** を共有する(`churn.Commit` に
  Author/Email を足した)。二本目の git 経路を作らないこと。
  自リポジトリ dogfood: 全 187 Go ファイルが Ownership 1.00 / minor 0(論文指標では
  最も安全)である一方、187/187 が fully AI-authored ——両指標を分けている理由の実例。

### 時間軸 / churn(★ v0.119.0、論文由来)
- `internal/churn` — yagura 唯一の **時間軸** レンズ。v0.118 まで全 lens が snapshot 解析で、
  `git log` を読む箇所は 0 だった(既存 `internal/hotspot` は名前に反して「複数 lens の収束」で
  あり、業界・研究で言う hotspot = 複雑度 × 変更頻度 ではない)。
- 根拠: **Nagappan & Ball, ICSE 2005**(絶対 churn は defect density の予測子として貧弱、
  サイズ・時間幅で正規化した相対 churn M1-M8 は fault-prone を **89.0%** で判別)と
  **Tornhill の behavioral code analysis**(危険なのは「頻繁に変わる複雑なコード」、
  上位 hotspot が欠陥の 25-70% を占める)。
  → **順位付けは必ず相対 churn で行う**(絶対 churn で並べないことが論文の中心的知見。
  回帰テスト `TestAnalyze_RanksByRelativeNotAbsoluteChurn` が固定している)。
- `Parse` は `git log --numstat` 出力の **純関数**(git 不要でテスト可能)、IO は `ReadGitLog`
  に隔離。サイズ不明ファイルは `skipped` に計上して 0 除算も「churn 0 = 安全」の誤報もしない。
  git 履歴が無いリポジトリは **明示的な `no_history` エラー**(空結果で安心させない)。
- MCP `yagura_churn_risk` は slug のみ受け取り daemon 側で git とソースを読む
  (files を LLM context に通さない)。`git` を spawn するので `openWorldHint: true`
  (v0.113.0 の `yagura_handoff` 分類と一貫)。★ v0.119

### Portfolio-wide quality(★ v0.118.0、First Principles 由来)
- `internal/srcfiles` — capped/fail-open-aware なソースツリー walker の**単一 seam**。
  元は `cmd/yagura/cli.go` の `readFilesLimited` にあり doc comment 自身が「新スキャナは
  これを再利用せよ」と要求していたが、`cmd/yagura` は `internal/*` から import できない
  ため MCP 側が従えなかった。引き上げて CLI と MCP の双方が同じ 1 本を使う
  (v0.109.0 の runSafely のように複製しない)。上限 1000 files / 50 MB、
  vendor/node_modules/.git/.yagura を skip、`Incomplete()` で部分スキャンを通知。
  **新しいディレクトリ走査は必ずこれを predicate 付きで使うこと。**
- `internal/portfolioquality` — 登録済み全プロジェクトを `local_path` から走査して
  `codehealth` で採点し **worst-first** に並べる。alert_fix の 7 signal が全て外部
  センサーで、~24 個の quality lens が portfolio から不可視だった C5 ギャップを埋める。
  `local_path` 無し/読取失敗/Go ソース無しは *理由つきで* `unscannable` に載せる
  (ランキングから消えたプロジェクトが「きれい」と誤読されないため)。
  MCP `yagura_portfolio_quality` は **files を受け取らない**——daemon がディスクを読むので
  ソース内容が LLM context を 1 バイトも通らない(token 経済の矛盾解消)。★ v0.118

### Cross-tool infra
- `internal/dedupe` — content-addressed cache (LRU + TTL) ★ v0.23
- `internal/plantracker` — Plan.md aware progress parser ★ v0.24
- `internal/alertfix` — health signal aggregator + rule-based recommendation ★ v0.27。
  MCP `yagura_alert_snapshot`(現在の lifecycle state 一覧 + stats、CLI `alert-snapshot`
  対、既存 `buildAlertResolveTool` と同じ `*Store` を再利用)★ v0.102

### Observation / meta
- `internal/agentevent` — 任意エージェント(Claude Code/Gemini CLI/Codex/OTel/汎用)の
  lifecycle イベントを OpenTelemetry GenAI semconv 整合の canonical event へ正規化
  (observability を Claude Code 専用でなく汎用に。MCP `yagura_agent_event`)★ v0.35
- `internal/sessionsummary` — 正規化イベント列をセッションの構造化 tool-call サマリへ集約
  (tool 別件数/順序/エラー/異常検知。Hermes Desktop 参照。MCP `yagura_session_summary`)★ v0.35
- `internal/dashboard` — HTML dashboard (薄め)
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

### HTTP security headers(v0.104.0〜)
- 全レスポンス(dashboard HTML / JSON API / MCP / metrics / health)に
  `withSecurityHeaders` middleware(`cmd/yagura/security_headers.go`)経由で
  `X-Content-Type-Options` / `X-Frame-Options` / `Referrer-Policy` /
  `Content-Security-Policy` を一律付与。frontend(dashboard)と backend
  (API/MCP)がセキュリティ姿勢で乖離しないための単一の座。HSTS は意図的に
  未設定(loopback-default、ADR-0004、TLS 終端は前段が担う想定)。
- v0.106.0 で CSP の `style-src`/`script-src` を `'unsafe-inline'` から
  per-request nonce へ移行。`withSecurityHeaders` が `crypto/rand` で毎リクエスト
  nonce を生成し、`dashboard.WithNonce` で request context に注入。
  `internal/dashboard` の 3 テンプレート(dashboard/alerts/activity)6 箇所の
  inline `<style>`/`<script>` は `dashboard.NonceFromContext(r.Context())` を
  `Nonce` として template data に渡し、`nonce="{{.Nonce}}"` で描画。新しい
  inline block を足す場合は必ず nonce 属性を付けること(さもないと CSP に
  ブロックされる)。

### 外部到達可能な write endpoint は auth + body 上限を必須とする(v0.105.0〜)
- `/mcp`(`internal/mcp/server.go`)・HTTP API(`cmd/yagura/httpapi.go`)・
  `/hooks/*`(`internal/hookreceiver` + `cmd/yagura/security_headers.go` の
  `requireBearerToken`)は全て同じ `cfg.MCPToken` を Bearer 認証で検証
  (constant-time compare、空 token なら認証なし)。v0.105.0 で
  `/hooks/claude-code` / `/hooks/agent` が無認証だった不整合(受信側の
  doc comment は認証ありと主張していたが実装が伴っていなかった)を解消。
- 同様に body size も 3 endpoint 群で上限必須(`/mcp` 1 MiB、HTTP API
  5 MiB、`/hooks/*` 1 MiB)。新しい write endpoint を足す際はこの 2 点
  (auth + body 上限)を両方満たすこと。

### 長寿命 background goroutine は panic recovery 必須(v0.107.0〜)
- `net/http` は 1 リクエストの handler panic をプロセス全体には波及させない
  (標準ライブラリの挙動)。MCP tool call も `internal/mcp/server.go` で
  独自に `recover()` している。しかし daemon の生存期間ずっと動く
  `go func(){ for { ... } }()` 形の background goroutine(gauge 更新・
  audit prune・cache prune・rate-limiter GC 等)は無防備だと 1 回の panic で
  daemon 全体(MCP/dashboard/HTTP API 込み)を道連れにする。
  `cmd/yagura/safego.go` の `runSafely(logger, task, fn)` で全ての周期
  呼び出し(起動直後の warm-up 呼び出しも含む)をラップすること。
- v0.108.0: これら background goroutine が listen する `ctx` の `cancel()` は
  shutdown signal 受信直後(drain sleep / scanner stop / HTTP shutdown の
  *前*)に呼ぶこと。関数末尾の `defer cancel()` だけに頼ると、shutdown
  シーケンス(最大 15 秒)のほぼ全期間 background task が動き続け、
  graceful-stop が事実上無意味になる(プロセス終了直前に cancel されても
  もう手遅れ)。`context.CancelFunc` は冪等なので、明示呼び出し + defer の
  二重掛けで問題ない。
- v0.109.0: v0.107.0 の適用範囲は `cmd/yagura/main.go` に直接書かれた
  goroutine のみで、`internal/scanner`(`Scanner.run`/`SecurityScanner.run`、
  GitHub/OSV/Scorecard API を叩いて外部レスポンスをパースする分、こちらの方が
  panic 面が大きい)を見落としていた不完全な適用だった。`internal/scanner/safe.go`
  に同型の `runSafely` を複製(import 方向が逆なので `cmd/yagura` のものは
  使えない)して追加済み。**教訓**: 「無防備な background goroutine」を横断的に
  探す際は grep を単一パッケージ(`cmd/yagura`)だけで終わらせず、daemon が
  `go xxx.Start(ctx)` のように委譲している他パッケージの内部 goroutine も
  必ず洗うこと。

### dashboard は手動テーマトグルで OS 設定を上書きできる(v0.116.0〜)
- `:root[data-theme="light"]` / `:root[data-theme="dark"]`(全 3 テンプレート)が
  `@media (prefers-color-scheme)` より詳細度で勝ち、OS 設定を強制上書きする。
  `<head>` の nonce 付き pre-paint スクリプトが `localStorage['yagura-theme']` を読んで
  body 描画前に `data-theme` を設定(テーマちらつき防止)。header の `◐` ボタン
  (nonce 付き click handler)が auto→反対テーマを切替えて localStorage に永続化する。
- 新しいトグル/永続化ロジックを足す場合も **inline script は nonce 必須**(既存の
  service-worker/toggle スクリプトと同様)。CSS は変数化済みなので新テーマ軸は
  attribute selector の追加で済む。

### dashboard は prefers-color-scheme に追従する(v0.115.0〜)
- `internal/dashboard/dashboard.go` の 3 テンプレート(dashboard/activity/alerts)は
  色をハードコードせず `:root` の CSS 変数(23 個)経由で描画する。dark 既定値は
  従来の GitHub-dark と完全一致(dark ユーザは変化なし)、`@media (prefers-color-scheme:
  light)` が Primer-light 値で上書きする。`color-scheme:light dark` を宣言済みで
  native control も追従する。
- **新しい dashboard の色は必ず CSS 変数を使うこと**(生 hex を足すと light テーマで
  ダークのまま残り半端になる)。新 inline `<style>/<script>` の nonce 属性ルールは不変。
  手動テーマトグルを足す場合は CSS は変数化済みなので `:root[data-theme=…]` override +
  nonce 付き localStorage スニペットだけで済む。

### registry を MCP Resources として公開する(v0.114.0〜、Plan.md★ v0.117.0)
- `internal/mcp/server.go` の `ResourceSource` interface(`ListResources`/`ReadResource`)+
  `SetResourceSource` で読み取り専用リソースを注入でき、`resources/list`/`resources/read`
  ハンドラと `initialize` の `resources` capability(**source が注入された時のみ** advertise)
  を提供する。`internal/mcp/resources.go` の `NewRegistryResourceSource(reg)` が registry を
  `yagura://registry`(全 project スナップショット)+ `yagura://project/{slug}`(単一 project)
  として公開し、`cmd/yagura/main.go` が daemon に wire する。tool(yagura_list/yagura_get)と
  同じデータの read-only ビューであり、trust base は不変(write path を増やさない)。
- **新しい read-only サーフェスを Resources 化する際は同じ seam を使う**: `ResourceSource` を
  実装して `SetResourceSource` で注入するだけ。subscribe/listChanged は未対応(one-shot
  POST transport ゆえ server 発の通知が出せない)——非対応フラグを capability に捏造しないこと。

### 全 tool は検証済み ToolAnnotations + Title を宣言する(v0.113.0〜)
- `internal/mcp/server.go` の `Tool` は `Title`(表示名)と `Annotations`
  (`*ToolAnnotations`: readOnlyHint/destructiveHint/idempotentHint/openWorldHint、
  MCP 2025-06-18)を持ち、`handleToolsList` が両方を `tools/list` に載せる
  (compact mode でも落とさない——client の auto-approval/確認ダイアログ判断に必要な
  小さな構造化フィールドだから)。101 tool 全てが宣言済みで、値は各 Handler の
  実体を読んで導出・敵対的に再検証した(description の `[G]`/`[S]` prefix から推測して
  いない)。`yagura_update`/`yagura_handoff` は「上書きが履歴なしで不可逆」ゆえ
  destructive=true、`yagura_handoff`/`yagura_vulns`/`yagura_scorecard`/`yagura_pin_drift`
  は外部プロセス/API に触れるため open_world=true。
- **新 tool を足す際は Title と Annotations を必ず設定すること**。
  `TestAllRegisteredTools_HaveAnnotationsAndTitle`(`internal/mcp/toolannotations_test.go`)が
  未設定を検出して fail する(panic-recovery 網羅と同じ「静かな退行を防ぐ完全性ガード」)。

### Origin header を全 route で検証する(v0.112.0〜、DNS rebinding 対策)
- ADR-0004 の「無 token + loopback bind」時の安全性は「同一マシンの非ブラウザ
  プロセスしか叩けない」という前提だったが、ブラウザの JS は same-origin policy に
  阻まれず loopback へ *送信* できる(レスポンスを *読む* ことだけ阻まれる)——
  操作系ツールへの `tools/call` は response を読めなくても実行されてしまう
  DNS-rebinding / browser-to-localhost 攻撃が可能だった(ADR-0007)。
  `cmd/yagura/security_headers.go` の `originAllowed`/`restrictOrigin` が
  `Origin` header 無し(非ブラウザ client)/ loopback host のみ許可し、それ以外
  (`"null"` 含む)を 403 で拒否する。`withSecurityHeaders(restrictOrigin(mux))`
  (`cmd/yagura/main.go`)という単一 seam で dashboard/`/mcp`/`/hooks/*`/HTTP API/
  `/metrics` 全 route に一律適用——auth+body 上限ルール(v0.105.0〜)と同じく、
  新しい write endpoint を足す際にこの単一 seam の *外側* に置いてはいけない。

### MCP tools/call レスポンスは structuredContent を持つ、handshake は正直(v0.111.0〜)
- `internal/mcp/server.go` の `handleToolsCall` は tool の返り値が JSON object に
  marshal される場合(`{`で始まる場合)、従来の `content:[{type:"text",text:<JSON文字列>}]`
  に加えて MCP 2025-06-18 の `structuredContent`(パース済み JSON object)を併記する。
  配列/スカラーを返す handler(現状の 101 tool には無いが、将来追加され得る)は
  `structuredContent` を省略(spec が object を要求するため)。新しい tool の handler が
  何を返しても、object を返す限り自動的にこの恩恵を受ける——個別対応不要。
- `initialize` の `serverInfo.version` は `mcp.version()`(`SetVersion` で注入された実際の
  running version)を返す。`protocolVersion` は `"2025-06-18"`。どちらも v0.111.0 以前は
  ハードコードされた嘘の値(`"0.1.0"` / `"2024-11-05"`)を返していた——`/hooks` 認証や
  cross-tool-shadowing と同じ「doc/spec が主張する動作と実装が食い違う」クラスの発見。

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
