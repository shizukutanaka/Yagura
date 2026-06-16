// Package agentlauncher launches Windsurf / Claude Code via OS-native commands
// for cross-agent handoff (v0.13.0).
//
// 動機:
//   quotamonitor が "Claude Code 使い切った" と判断したら、yagura は
//   Windsurf を起動して context を Cascade に渡す必要がある。
//   逆方向(Windsurf → Claude Code)も同様。
//
// 仕組み(OS-agnostic):
//   - macOS:  `open <deeplink>` or `open -a Windsurf <path>`
//   - Linux:  `xdg-open <deeplink>` or `windsurf <path>`
//   - Windows: `cmd /c start <deeplink>` or `windsurf <path>`
//
// Deeplink:
//   - Windsurf: `windsurf://file/<absolute-path>` で workspace を開く
//   - Claude Code: subcommand `claude code <path>` で start
//
// 設計:
//   - ゼロ依存(ADR-0001、os/exec のみ)
//   - Spawner interface で OS 呼出を抽象化(test 容易性)
//   - Dry-run mode で安全な確認可能
package agentlauncher

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Spawner は外部 process 起動の最小 interface。test 用に inject 可能。
type Spawner interface {
	// Start は process を起動して終了を待たずに戻る。
	Start(ctx context.Context, cmd string, args ...string) error
}

// OSSpawner は os/exec を使う標準実装。
type OSSpawner struct{}

func (o *OSSpawner) Start(ctx context.Context, cmd string, args ...string) error {
	c := exec.CommandContext(ctx, cmd, args...)
	if err := c.Start(); err != nil {
		return fmt.Errorf("start %s: %w", cmd, err)
	}
	// Wait for the process completion asynchronously to avoid zombie processes.
	go func() { c.Wait() }()
	return nil
}

// Launcher は agent 起動を担当する。
type Launcher struct {
	// Spawner は外部 process 起動 backend(nil なら OSSpawner)。
	Spawner Spawner
	// DryRun=true なら実際の起動はせず、組み立てたコマンドをログ用に保持。
	DryRun bool
	// GOOSOverride はテスト用の OS 上書き(空なら runtime.GOOS)。
	GOOSOverride string

	lastCmd string
	lastArg []string
}

// New は標準 Launcher を生成する。
func New() *Launcher {
	return &Launcher{Spawner: &OSSpawner{}}
}

// LaunchWindsurf は workspace ディレクトリで Windsurf を起動する。
//
// workspaceDir は絶対パスを推奨(deeplink 解決のため)。
// ctx は cancellation のために spawner に渡される。
//
// 実装:
//   - 通常は `open -a Windsurf <path>` (macOS), `windsurf <path>` (Linux/Win)
//   - PATH に windsurf 実行可能ファイルがあればそれを使う
//   - 無ければ deeplink (`windsurf://file/<path>`) で OS 既定ブラウザに任せる
func (l *Launcher) LaunchWindsurf(ctx context.Context, workspaceDir string) error {
	if workspaceDir == "" {
		return errors.New("workspaceDir is required")
	}
	abs, err := filepath.Abs(workspaceDir)
	if err != nil {
		return fmt.Errorf("abs path: %w", err)
	}
	cmd, args := l.windsurfCommand(abs)
	return l.run(ctx, cmd, args...)
}

// LaunchClaudeCode は workspace ディレクトリで Claude Code を起動する。
//
// 既存 `claude` CLI が PATH にあれば `claude code <path>` で起動する。
// OS 既定の方法(macOS open -a, Linux/Win 直接実行)も fallback として試す。
func (l *Launcher) LaunchClaudeCode(ctx context.Context, workspaceDir string) error {
	if workspaceDir == "" {
		return errors.New("workspaceDir is required")
	}
	abs, err := filepath.Abs(workspaceDir)
	if err != nil {
		return fmt.Errorf("abs path: %w", err)
	}
	cmd, args := l.claudeCodeCommand(abs)
	return l.run(ctx, cmd, args...)
}

// LastCommand は最後に実行(または DryRun で組み立てた)コマンドを返す。
// (debug/test 用)
func (l *Launcher) LastCommand() (string, []string) {
	return l.lastCmd, l.lastArg
}

// ─── 内部: OS 別コマンド組み立て ─────────────────────────

func (l *Launcher) goos() string {
	if l.GOOSOverride != "" {
		return l.GOOSOverride
	}
	return runtime.GOOS
}

func (l *Launcher) windsurfCommand(absPath string) (string, []string) {
	switch l.goos() {
	case "darwin":
		// macOS: open -a でアプリ名指定。Windsurf がインストール済み前提。
		// fallback: windsurf:// deeplink で起動
		return "open", []string{"-a", "Windsurf", absPath}
	case "windows":
		// Windows: cmd start で URL or 実行ファイル
		// /c でコマンド実行、 start で非同期起動。
		return "cmd", []string{"/c", "start", "windsurf", absPath}
	default:
		// Linux & その他: PATH の windsurf を直接実行(deeplink 開きは xdg-open)
		// 一般的に Windsurf 公式 .deb / .rpm は `windsurf` 実行可能ファイルを提供。
		return "windsurf", []string{absPath}
	}
}

func (l *Launcher) claudeCodeCommand(absPath string) (string, []string) {
	switch l.goos() {
	case "darwin":
		// claude CLI が PATH にあれば直接 使う(Pro/Max の標準セットアップ)
		return "claude", []string{"code", absPath}
	case "windows":
		return "cmd", []string{"/c", "claude", "code", absPath}
	default:
		return "claude", []string{"code", absPath}
	}
}

// run は spawner 呼出 + DryRun 記録。
func (l *Launcher) run(ctx context.Context, cmd string, args ...string) error {
	l.lastCmd = cmd
	l.lastArg = append([]string(nil), args...) // copy

	if l.DryRun {
		return nil
	}
	if l.Spawner == nil {
		l.Spawner = &OSSpawner{}
	}
	return l.Spawner.Start(ctx, cmd, args...)
}

// ─── Deeplink ────────────────────────────────────────────────

// WindsurfDeeplink は Windsurf の deeplink URL を組み立てる。
//
// 例: WindsurfDeeplink("/home/m/yagura") → "windsurf://file//home/m/yagura"
//
// 用途: 公式 MCP marketplace 経由の install URL や、ブラウザリンクとして
// ユーザーに提示する場合に使う(直接起動は LaunchWindsurf を推奨)。
func WindsurfDeeplink(workspaceDir string) string {
	// 既に絶対パスでなければそのまま使う(呼出側責任)
	return "windsurf://file" + ensureLeadingSlash(workspaceDir)
}

func ensureLeadingSlash(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}
