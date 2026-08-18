#!/usr/bin/env bash
# release.sh — 版番号の書き換えとリリース gate を **機械にやらせる**(v0.131.0)。
#
# なぜ最後なのか(Musk アルゴリズム ⑤):
#   v0.113→v0.130 の 18 リリースは、この手順を毎回人手で実行していた。自動化を
#   先にやっていたら「29 個の tool を配る手順」を自動化してしまっていた——
#   自動化は削除(②)と単純化(③)の **後** に来る。手順そのものが正しいと
#   分かってから機械に渡す。
#
# 何を自動化し、何をしないか:
#   する   — 版番号 4 箇所の書き換え / MCP ドキュメント再生成 / 全 gate の実行 /
#            tarball の作成と前版の削除
#   しない — CHANGELOG の執筆 / commit / push / タグ付け
#
#   CHANGELOG は「今回何を学んだか」を書く場所で、機械に書かせると
#   埋め草になる。commit と push は外向きで巻き戻しにくいので人間(または
#   明示的に指示されたエージェント)の判断に残す。**自動化は判断を消す道具ではない。**
#
# 使い方: make release VERSION=0.131.0

set -euo pipefail

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
	echo "usage: $0 <version>   e.g. $0 0.131.0" >&2
	exit 2
fi
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
	echo "ERROR: version must be X.Y.Z (got '$VERSION')" >&2
	exit 2
fi

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

# 版番号を持つ全ファイル。versionsites_test.go がこの集合を固定している
# ——5 つ目が増えたらテストが落ちるので、ここを黙って外れることはない。
SITES=(
	"cmd/yagura/main.go"
	"cmd/yagura/main_test.go"
	"cmd/yagura-tray/main.go"
	"internal/dashboard/dashboard.go"
)

CURRENT="$(sed -nE 's/^\tversion[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' cmd/yagura/main.go | head -1)"
if [[ -z "$CURRENT" ]]; then
	echo "ERROR: could not read the current version from cmd/yagura/main.go" >&2
	exit 1
fi
# 既に目的の版なら **再実行**(resume)として扱う。
#
# 版を書き換えてから gate を回す以上、gate が落ちれば tree は半適用のまま残る。
# そこで「もう $VERSION だ」を error にすると、直して再実行する道が塞がれて
# 手で版を巻き戻す羽目になる(初回の実装で実際に踏んだ)。冪等にしておく。
RESUME=0
if [[ "$CURRENT" == "$VERSION" ]]; then
	RESUME=1
	echo "→ already at $VERSION — resuming (re-running docs and gates)"
else
	# 版は必ず進める(取り違えで巻き戻すと tarball と履歴が食い違う)。
	older="$(printf '%s\n%s\n' "$CURRENT" "$VERSION" | sort -V | head -1)"
	if [[ "$older" != "$CURRENT" ]]; then
		echo "ERROR: $VERSION is not newer than the current $CURRENT" >&2
		exit 1
	fi
	echo "→ $CURRENT → $VERSION"
fi

# CHANGELOG は人が書く。書かれていないなら止める——「あとで書く」は書かれない。
if ! grep -q "^## \[v$VERSION\]" CHANGELOG.md; then
	echo "ERROR: CHANGELOG.md has no '## [v$VERSION]' entry." >&2
	echo "       Write it first: the entry is the release, the tarball is only its packaging." >&2
	exit 1
fi

# 散文(README / CLAUDE.md)の版は機械置換しない——文脈があるので人が書く。
# ただし **書き忘れると versionsites_test が落ちる** ので、分かりにくいテスト失敗に
# なる前にここで止める。
for doc in README.md CLAUDE.md; do
	if ! grep -q "$VERSION" "$doc"; then
		echo "ERROR: $doc still describes $CURRENT, not $VERSION." >&2
		echo "       Update its prose first — this script deliberately does not rewrite prose." >&2
		exit 1
	fi
done

if [[ $RESUME -eq 1 ]]; then
	echo "→ version sites already at $VERSION; skipping rewrite"
else
echo "→ rewriting $((${#SITES[@]})) version sites"
for f in "${SITES[@]}"; do
	if ! grep -q "$CURRENT" "$f"; then
		echo "ERROR: $f does not contain $CURRENT — the version sites have drifted." >&2
		echo "       Run: go test ./cmd/yagura/ -run TestVersionSites" >&2
		exit 1
	fi
	# 版番号そのものだけを置換する(前後の文脈には触れない)
	perl -pi -e "s/\Q$CURRENT\E/$VERSION/g" "$f"
done
fi

echo "→ regenerating docs/MCP_TOOLS.md"
make docs-mcp >/dev/null

echo "→ gate: go vet"
go vet ./... >/dev/null

echo "→ gate: gofmt on files this release touched"
# repo 全体には以前からの整形ゆれが在る。**今回触った分だけ** を見る
# (無関係な差分をリリースに巻き込まないため——過去に一度やらかしている)。
changed="$(git diff --name-only HEAD -- '*.go'; git diff --cached --name-only HEAD -- '*.go'; git ls-files --others --exclude-standard -- '*.go')"
unformatted=""
for f in $(printf '%s\n' $changed | sort -u); do
	[[ -f "$f" ]] || continue
	if [[ -n "$(gofmt -l "$f")" ]]; then unformatted+=" $f"; fi
done
if [[ -n "$unformatted" ]]; then
	echo "ERROR: not gofmt-clean:$unformatted" >&2
	exit 1
fi

echo "→ gate: go test -race ./... (the real gate)"
go test -race -count=1 ./... >/dev/null

echo "→ gate: reproducible build"
make verify >/dev/null

echo "→ packaging"
DIRNAME="$(basename "$ROOT")"
TARBALL="../yagura-v$VERSION-source.tar.gz"
OLD="../yagura-v$CURRENT-source.tar.gz"
[[ $RESUME -eq 1 ]] && OLD=""
rm -rf bin
(cd .. && tar czf "yagura-v$VERSION-source.tar.gz" --exclude="$DIRNAME/bin" "$DIRNAME/")
if [[ -n "$OLD" && -f "$OLD" ]]; then rm -f "$OLD"; fi

echo
echo "✓ v$VERSION prepared"
echo "  tarball: $(cd .. && ls -la "yagura-v$VERSION-source.tar.gz" | awk '{print $5" bytes"}')"
if [[ -n "$OLD" ]]; then echo "  removed: $(basename "$OLD")"; fi
echo
echo "Left to you on purpose — commit and push are outward-facing:"
if [[ -n "$OLD" ]]; then
	echo "  git add ../yagura-v$VERSION-source.tar.gz ../yagura-v$CURRENT-source.tar.gz"
else
	# resume 時は直前版が分からないので、消えた tarball も含めて拾わせる。
	echo "  git add -A ../yagura-v*-source.tar.gz"
fi
echo "  git add -u . && git commit && git push -u origin <branch>"
