// Package workspace detects the project root directory for handoff context.
//
// 動機 (v0.14.0):
//
//	v0.13.0 の `yagura_handoff` は workspace パスを明示指定する必要があり、
//	client が忘れると state_dir に fallback されてしまう。実用上、ほぼ
//	常に「.git があるディレクトリ」が正解なので、yagura 起動時に
//	自動で検出して Deps.WorkspaceRoot に注入する。
//
// 検出戦略:
//  1. 引数 startDir から上方向に `.git` を探索(最大 MaxDepth=20 階層)
//  2. 見つかれば、その親ディレクトリが workspace root
//  3. 見つからなければ startDir 自体を返す(プロジェクト未配置でも壊れない)
//
// 設計判断:
//   - ゼロ依存(ADR-0001、os.Stat / filepath.Dir のみ)
//   - symlink 透過(EvalSymlinks 経由)
//   - bare repository (.git is a file with "gitdir:" pointer) は parent 扱い
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// MaxDepth は探索の最大階層数(無限ループ防止)。
const MaxDepth = 20

// Detect は startDir から上方向に .git を探し、見つかった親 dir を返す。
//
// 戻り値:
//
//	workspace: 検出された workspace root の絶対パス
//	gitFound:  .git ディレクトリが見つかったか
//	err:       startDir が不正な場合のみ
//
// 検出失敗時は workspace = startDir 絶対パス(壊れない fallback)。
func Detect(startDir string) (workspace string, gitFound bool, err error) {
	if startDir == "" {
		return "", false, errors.New("startDir is empty")
	}
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", false, fmt.Errorf("abs: %w", err)
	}
	// symlink を解決(/var → /private/var on macOS 等の差異吸収)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// EvalSymlinks 失敗時は abs path で続行(存在しないディレクトリでも abs は取れる)
		resolved = abs
	}

	current := resolved
	for i := 0; i < MaxDepth; i++ {
		gitPath := filepath.Join(current, ".git")
		info, statErr := os.Stat(gitPath)
		if statErr == nil {
			// .git が dir でも file (gitfile/worktree) でも workspace 扱い
			_ = info
			return current, true, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			// root に到達("/" の親は "/")
			break
		}
		current = parent
	}
	// 未検出 → startDir 自体を workspace 扱い(壊れない fallback)
	return resolved, false, nil
}

// DetectCWD は os.Getwd() から検出する shortcut。
//
// daemon 起動時に呼ぶことを想定。CWD が取れない場合は ("", false, err)。
func DetectCWD() (workspace string, gitFound bool, err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false, fmt.Errorf("getwd: %w", err)
	}
	return Detect(cwd)
}
