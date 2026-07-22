# Yagura 改善ループ指示書 — Opus / Sonnet 用

> この文書は Yagura の継続的改善ループを **Claude Opus / Claude Sonnet** セッションが
> 引き継ぐための実務指示書。CLAUDE.md(開発ガイド)を置き換えるものではなく、
> 「このループを回す AI セッション」向けの運用手順・役割分担・既知の罠を集約する。
> まず CLAUDE.md を読み、次に本書を読むこと。

## 0. 不可侵ルール(モデル不問・要暗記)

1. **ADR-0001 zero-dep**: `go.mod` は自モジュールのみ。`go.sum` は存在しない状態が正。
   外部 Go module を足す提案は原則却下(x/text すら不可 — homoglyph 検出がこれで
   ブロックされている前例あり)。
2. **Reproducible build**: `make verify` が 2 回ビルドで SHA 一致すること。リリース毎に必須。
3. **Trust base**: sensor 値(vuln/ci/scorecard 等)は scanner 専用。MCP tool 経由で
   書けるようにしない。Resources も read-only のみ。
4. **単一 seam 原則**: HTTP 横断関心事(headers/auth/body 上限/Origin 検証)は
   `withSecurityHeaders(restrictOrigin(mux))` の単一 seam に置く。endpoint 個別に
   複製しない(v0.107→v0.109 の教訓: 狭すぎる適用は後で必ず漏れが見つかる)。
5. **Honest capability**: 実装が伴わない capability/フラグ/バージョンを advertise しない。
   逆に「doc が主張して実装が無い」ものは高価値バグ(過去 4 例: /hooks 認証、
   cross-tool-shadowing、fake handshake version、Origin 未検証)。
6. **Deterministic output**: sort 順・tie-break を固定。テストが出力比較できる状態を保つ。
7. **新 tool は Title + Annotations 必須**(completeness guard テストが落ちる)。
   新 inline `<style>/<script>` は nonce 属性必須。新 dashboard 色は CSS 変数経由。

## 1. リリースの型(house recipe — 毎回この順)

1. **Red**: `internal/<topic>/` にテストを先に書き、赤を確認(コンパイルエラーも赤)。
2. **Green**: 最小実装。テストファイルは実装に合わせて書き換えない。
3. 全体検証: `gofmt -s -l <touched>` / `go vet ./...` / `go build ./...` /
   `go test -race -count=1 ./...`(**実行結果を貼る**。「通ったはず」禁止)。
4. **live dogfood**: daemon を実際に起動して curl で当該機能を叩き、応答を貼る。
   起動は env var(`YAGURA_ADDR`/`YAGURA_STATE_DIR`/`YAGURA_GITHUB_TOKEN=ghp_…` 形式
   ダミー可)。flag ではない。終了は `pkill -f "bin/yagura$"` + state dir 削除。
5. Docs: CHANGELOG(Theme/Fixed|Added/Verification/Counts/What's not yet/Sources の
   節構成を既存エントリからコピー)、CLAUDE.md(Rules/Map への追記)、README
   (status 行 + reproducible counter を +1)。
6. Version bump は **4 箇所固定**: `cmd/yagura/main.go` / `cmd/yagura/main_test.go` /
   `cmd/yagura-tray/main.go` / `internal/dashboard/dashboard.go`(footer)。
7. `make verify VERSION=0.X.0` で SHA 一致確認。
8. Tarball: `tar czf yagura-v0.X.0-source.tar.gz --exclude='yagura-v0.35.0/bin'
   yagura-v0.35.0/` → **旧 tarball との file list diff を必ず取り**、意図した新規ファイル
   だけが増えていることを確認。
9. Stage: 旧 tarball は `rm` した後 **`git add <旧名>` で削除を stage**(v0.111.0 で
   stage 漏れ事故→後追い commit になった)。新規ファイルは gitignore に食われるので
   `git add -f`。`git status` で staged set を目視。
10. Commit(下記文体)→ `git push -u origin claude/wizardly-goldberg-ZbjGY`
    (失敗時 2/4/8/16s backoff で 4 回まで)。**PR は作らない** — 既存 PR #4 が
    この branch を追跡している。push 後 `git merge-base --is-ancestor origin/main HEAD`
    で conflict-free を確認。

コミット文体: `feat(v0.X.0): <要約>` or `fix(v0.X.0): …`。本文に動機→変更→検証。
モデル ID は書かない。末尾に Co-Authored-By + Claude-Session 行。

## 2. 役割分担

### Opus に回す仕事(判断・設計・監査)
- 次リリース候補の**選定と敵対的検証**(discovery→「本当にまだ無いか code を読んで
  確認」→採否)。表面的な grep で確定させない。
- セキュリティ/transport/ADR 級の設計判断(例: Streamable HTTP 移行、Origin 検証の
  scope 判断)。ADR は `docs/adr/0000-template.md` の型で。
- 「handler 実体を読んで分類する」系(ToolAnnotations 分類のような、誤ると wire に
  嘘が載る仕事)。
- CHANGELOG の Theme 文と intent-vs-implementation の物語部分。

### Sonnet に回す仕事(機械的・大量・パターン)
- スクリプト化した一括編集(populate.py 型: Name: 行アンカーで挿入→gofmt -w →
  **source-vs-table 照合スクリプトで全件一致を機械確認**)。
- version bump、カウンタ更新、tarball 手順、dogfood curl の再実行。
- 既存テストパターンの複製(server_test.go の postJSON/newServerForTest ヘルパを再利用)。
- gofmt/lint 一掃、doc-guard カウント合わせ。

### どちらでも可
- 小粒バグ修正(再現テスト→修正)、CHANGELOG の Verification/Counts 節。

## 3. 既知の罠(全て本セッションで実際に踏んだ/確認した)

- **git は curated subset**: `/yagura-v*/` は gitignore 済みで一部ファイルのみ force-add
  されている。`tools_audit.go` など 10+ ファイルは untracked のまま=**git diff に出ない**。
  完全な差分は tarball が正。編集後は tarball への反映確認(`tar xzf -O … | grep`)を。
- **`gofmt -s -w` の巻き添え**: ディレクトリ一括にかけると無関係ファイルの既存
  misalignment まで直してしまう。リリース scope を汚すので、巻き添え分は
  `git restore` で戻し、tarball を作り直す。
- **doc-guard テスト**: README のパッケージ数/tool 数/counter はテストが照合している。
  数字を変えたら `go test ./cmd/yagura/` を回して確認。
- **`yagura_register` は display_name / repository 必須**、priority は 0-5。
  テストの seed でも同じ(registry.Add が validate する)。
- **mustCall の戻りは具象型**(`map[string]any` とは限らない)。型 assert は実装を見て。
- **CLI と MCP の入力面は別**(例: mcp-audit は `--file`、stdin ではない)。
- **Edit ツール不調時**は Python heredoc で冪等編集に切替(過去に恒常的失敗あり)。
- **週次 rate limit**: サブエージェント大量並列は上限に当たる(46 並列で 7 落ち)。
  分割・直列 fallback を用意。

## 4. 現在地と backlog(2026-07 時点)

現在: v0.115.0(dashboard light/dark theming)まで。PR #4 が v0.35→最新を main へ
公開中(open, conflict-free 維持)。連続 reproducible release counter は README 参照。

### 長所(維持すべきもの)
- TDD-first + `-race` + reproducible + doc-guard + live dogfood が毎リリース回る検証文化
- zero-dep 110+ リリース維持(supply-chain T10 を構造的に排除)
- intent-vs-implementation ギャップ検出→完全性ガードテスト化の習慣
- MCP 2025-06-18 準拠(structuredContent / 正直 handshake / 検証済み Annotations 101/101 /
  Resources)+ honest capability 原則
- 商用級 HTTP 防御が単一 seam に集約
- 自作 ~24 quality lens の自己 dogfood と実修正

### 短所(認識しておくもの)
- curated git subset ゆえ GitHub 上の diff レビューが不完全(tarball が正)
- one-shot POST transport — server 発通知(subscribe/list_changed/elicitation)不可
- outputSchema 全 101 tool 未宣言、tools/list pagination 無し
- golangci-lint/coverage がこのループの DoD 外、gofmt baseline 汚れ ~38 ファイル
- dashboard: 手動テーマトグル無し、E2E 薄い
- homoglyph 検出は zero-dep 制約でブロック中

### 改善 backlog(優先度順・入口ファイル付き)
1. **per-tool outputSchema** — `internal/mcp/server.go` の Tool struct に
   `OutputSchema any` を追加し tools/list に載せ、scans/security 系 topic ファイルから
   漸進宣言。structuredContent(v0.111.0)の対。
2. **tools/list pagination**(cursor/nextCursor)— `handleToolsList`。小粒。
3. **Streamable HTTP transport**(GET/SSE + Mcp-Session-Id)— ADR 起草から。
   subscribe/elicitation の前提。`TestServer_GetIsNotAllowed` の仕様変更を伴う。
4. **golangci-lint を Makefile/DoD へ** + gofmt baseline 一掃(Sonnet 向き)。
5. **Resources 第 2 弾**: Plan.md / audit log / CHANGELOG を `ResourceSource` 実装で
   (`internal/mcp/resources.go` の seam に追加、`SetResourceSource` は複数 source の
   合成が必要になる点に注意)。
6. **手動テーマトグル**(localStorage + `data-theme` 上書き、nonce 付き 1 行 JS、
   CSS は既に変数化済みなので `:root[data-theme=…]` override を足すだけ)。

## 5. 起動チェックリスト(新セッション最初の 5 分)

1. `git log --oneline -5` と `git status --short` で現在地確認。
2. CLAUDE.md の Rules 更新差分を確認(前セッションが追記している)。
3. CHANGELOG 先頭エントリの「What's not yet」= 直近の申し送り。
4. `go build ./... && go test -race -count=1 ./...` が緑であることを確認してから着手。
5. 本書 §4 の backlog から次の 1 テーマを選び、**1 リリース = 1 テーマ**で回す。
