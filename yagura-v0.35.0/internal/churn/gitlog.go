package churn

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// LogFormat は Parse が期待する git log の書式(<hash>|<ISO8601>)。
const LogFormat = "--format=%H|%aI"

// DefaultMaxCommits は既定で遡るコミット数。履歴が巨大なリポジトリで
// git log が無制限に走るのを防ぐ(部分履歴でも相対 churn は意味を持つ)。
const DefaultMaxCommits = 500

// ErrNotGitRepo は dir が git 管理下でない場合に返る。
// 「時間軸データが取れない」ことを *明示的な失敗* として扱う——churn 0 件を
// 「変更されていない=安全」と誤読させないため(fail-open 防止)。
var ErrNotGitRepo = errors.New("not a git repository (no commit history to analyze)")

// ReadGitLog は dir で `git log --numstat` を実行して生出力を返す。
// maxCommits <= 0 なら DefaultMaxCommits。
//
// 解析ロジックは Parse(純関数)側にあり、ここは IO のみ——テストは実 git を
// 必要としない(本リポジトリの content-based lens と同じ分離)。
func ReadGitLog(ctx context.Context, dir string, maxCommits int) (string, error) {
	if maxCommits <= 0 {
		maxCommits = DefaultMaxCommits
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git",
		"-C", dir, "log", "--numstat", "--no-merges",
		"-n", strconv.Itoa(maxCommits), LogFormat)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.ToLower(string(ee.Stderr))
			if strings.Contains(stderr, "not a git repository") {
				return "", ErrNotGitRepo
			}
			return "", errors.New("git log failed: " + strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}
