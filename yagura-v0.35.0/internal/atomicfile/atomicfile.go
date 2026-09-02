// Package atomicfile provides crash-safe file writes via the standard
// temp-file + fsync + rename pattern.
//
// 動機:
//
//	registry / handoff / secrets / mcp が同じ「temp に書いて rename」処理を
//	個別に実装しており、durability semantics が揃っていなかった
//	(secrets と mcp は fsync を欠いていた)。ここに一本化することで
//	「rename は atomic、かつ data は rename 前に fsync 済み」を全箇所で保証する。
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write は data を path へ atomic に書き込む。
//
// 手順: 同一ディレクトリに temp file を作成 → Write → Chmod(mode) →
// fsync → Close → Rename(temp, path)。rename は POSIX 上 atomic なので、
// 読み手は常に「古い完全なファイル」か「新しい完全なファイル」のどちらかを
// 見る(torn write は起きない)。fsync により rename 前に data が物理媒体へ
// 到達することを保証する。
//
// 親ディレクトリが無ければ作成する。失敗時は temp file を残さない。
func Write(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".atomic-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // error path のクリーンアップ(rename 成功後は no-op)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
