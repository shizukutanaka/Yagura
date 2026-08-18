#!/usr/bin/env bash
# tag.sh — リリースタグを **人間に手で打たせない**(v1.1.0)。
#
# 実際に起きた事故:
#
#   このリポジトリの 3 つのタグのうち 2 つ(ｖ1.78.0 / ｖ1.79.0)は、先頭が
#   ASCII の 'v' (U+0076) ではなく **全角の 'ｖ' (U+FF56)** で打たれていた。
#   .github/workflows/release.yml の trigger は `tags: ['v*']`(ASCII)なので、
#   **この 2 本は release workflow を一度も起動していない**。タグは付いているのに
#   バイナリも SBOM も provenance も生成されていない——しかも失敗が「何も起きない」
#   という形で現れるので、誰も気づかない。
#
#   日本語 IME で 'v' を打つと全角になることがある。これは注意力の問題ではなく
#   **手で打たせている設計の問題** なので、機械に打たせる。
#
# 何をしないか:
#   push しない。タグを打つ=公開の意思決定であり、release.yml が動いて成果物が
#   世に出る。その判断は人間に残す(scripts/release.sh と同じ方針)。
#
# 使い方:
#   make tag VERSION=1.1.0     # 注釈付きタグをローカルに作る
#   bash scripts/tag.sh --check v1.1.0   # 名前の検証だけ(CI/テスト用、git に触れない)

set -euo pipefail

# tagNameIsValid — release.yml の `tags: ['v*']` に **確実に** 一致する形だけを通す。
# ASCII 'v' + X.Y.Z のみ。全角 v・大文字 V・接頭辞なし・余分な文字はすべて弾く。
check_name() {
	local name="$1"
	# ASCII の範囲だけを明示的に許す。`[[ ]]` は keyword なので LC_ALL= の前置は
	# できない(前置すると "[[: command not found" になる——最初の実装で踏んだ)。
	# 文字クラスに頼らず **列挙した ASCII 文字だけ** を許すことでロケール非依存にする。
	local ok=1
	case "$name" in
	v[0-9]*.[0-9]*.[0-9]*) ;;
	*) ok=0 ;;
	esac
	# 上の glob は全角も数字以外も緩く通すので、厳密な形は正規表現で確認する。
	if [[ $ok -eq 1 ]] && ! [[ "$name" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
		ok=0
	fi
	# 非 ASCII バイトが 1 つでもあれば拒否(全角 'ｖ' はここで確実に落ちる)。
	if [[ $ok -eq 1 ]] && [[ -n "$(printf '%s' "$name" | LC_ALL=C tr -d '\41-\176')" ]]; then
		ok=0
	fi
	if [[ $ok -eq 0 ]]; then
		echo "ERROR: invalid tag name: $(printf '%q' "$name")" >&2
		echo "       must be ASCII 'v' + X.Y.Z (e.g. v1.1.0)." >&2
		echo "       NOTE: a full-width 'ｖ' (U+FF56) looks identical here but does NOT match" >&2
		echo "       release.yml's \`tags: ['v*']\`, so the release workflow would never run." >&2
		return 1
	fi
	return 0
}

if [[ "${1:-}" == "--check" ]]; then
	check_name "${2:-}"
	exit $?
fi

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
	echo "usage: $0 <version>   e.g. $0 1.1.0   (no leading v)" >&2
	exit 2
fi
# 呼び出し側が 'v' を付けてしまっても受ける(ただし全角は弾く)
VERSION="${VERSION#v}"
TAG="v$VERSION"
check_name "$TAG"

cd "$(dirname "$0")/.."

CURRENT="$(sed -nE 's/^\tversion[[:space:]]*=[[:space:]]*"([0-9]+\.[0-9]+\.[0-9]+)".*/\1/p' cmd/yagura/main.go | head -1)"
if [[ "$CURRENT" != "$VERSION" ]]; then
	echo "ERROR: the tree is at $CURRENT, not $VERSION — tag what you built." >&2
	echo "       Run: make release VERSION=$VERSION" >&2
	exit 1
fi
if ! grep -q "^## \[v$VERSION\]" CHANGELOG.md; then
	echo "ERROR: CHANGELOG.md has no '## [v$VERSION]' entry." >&2
	exit 1
fi
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
	echo "ERROR: tag $TAG already exists." >&2
	exit 1
fi

git tag -a "$TAG" -m "$TAG"
echo "✓ created annotated tag $TAG (ASCII-verified)"
echo
echo "Not pushed — pushing the tag starts the release workflow and publishes artifacts:"
echo "  git push origin $TAG"
