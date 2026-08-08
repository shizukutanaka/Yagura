package churn

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// LogFormat は Parse が期待する git log の書式(<hash>|<ISO8601>)。
const LogFormat = "--format=%H|%aI|%an|%ae"

// DefaultMaxCommits は既定で遡るコミット数。履歴が巨大なリポジトリで
// git log が無制限に走るのを防ぐ(部分履歴でも相対 churn は意味を持つ)。
const DefaultMaxCommits = 500

// gitTimeout は git log 1 回あたりの上限時間。
const gitTimeout = 60 * time.Second

// ErrNotGitRepo は dir が git 管理下でない場合に返る。
// 「時間軸データが取れない」ことを *明示的な失敗* として扱う——churn 0 件を
// 「変更されていない=安全」と誤読させないため(fail-open 防止)。
var ErrNotGitRepo = errors.New("not a git repository (no commit history to analyze)")

// ReadGitLog は dir で `git log --numstat` を実行して生出力を返す。
// maxCommits <= 0 なら DefaultMaxCommits。
//
// pathspec で対象を絞る(既定 = Go ソースのみ)。これは速度だけの話ではない:
// 相対 churn の分母(totalLOC)は Go ソースから算出しているので、分子の churn に
// tarball や markdown の変更を混ぜると M1 が 1 を超えるなど指標が壊れる。
// 分子と分母のファイル集合を一致させる(v0.120.0 で dogfood 中に発見した不整合)。
//
// 解析ロジックは Parse(純関数)側にあり、ここは IO のみ——テストは実 git を
// 必要としない(本リポジトリの content-based lens と同じ分離)。
func ReadGitLog(ctx context.Context, dir string, maxCommits int) (string, error) {
	return ReadGitLogPaths(ctx, dir, maxCommits, DefaultPathspec)
}

// DefaultPathspec は既定の対象(Go ソースのみ)。
var DefaultPathspec = []string{"*.go"}

// ReadGitLogPaths は pathspec を指定できる版。pathspec が空なら全ファイル。
func ReadGitLogPaths(ctx context.Context, dir string, maxCommits int, pathspec []string) (string, error) {
	if maxCommits <= 0 {
		maxCommits = DefaultMaxCommits
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	args := []string{"-C", dir, "log", "--numstat", "--no-merges",
		"-n", strconv.Itoa(maxCommits), LogFormat}
	if len(pathspec) > 0 {
		args = append(args, "--")
		args = append(args, pathspec...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		// timeout で CommandContext が git を SIGKILL した場合、返るのは
		// stderr が空の ExitError になる。そのまま流すと "git log failed: " という
		// 情報ゼロのメッセージになるので、先に ctx を見て原因を名指しする。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("git log timed out after %s (repository history too large; "+
				"lower max_commits): %w", gitTimeout, ctxErr)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr := strings.ToLower(string(ee.Stderr))
			if strings.Contains(stderr, "not a git repository") {
				return "", ErrNotGitRepo
			}
			msg := strings.TrimSpace(string(ee.Stderr))
			if msg == "" {
				msg = "exit status " + ee.String() + " with no stderr"
			}
			return "", errors.New("git log failed: " + msg)
		}
		return "", err
	}
	return string(out), nil
}
