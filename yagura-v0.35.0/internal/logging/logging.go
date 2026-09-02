// Package logging は Yagura 全体で使う slog ベースの構造化ログを提供する。
//
// 全ログは JSON 形式で stdout に出力される。フリーテキストは禁止。
// 必須フィールド: time / level / msg / service / version。
// 任意フィールド: trace_id (request scoped)、tool (MCP tool 名) 等。
//
// 設計判断:
//   - JSON のみ(運用集約しやすい)。text handler は dev/test 用に discard 専用。
//   - service / version は With() で全ログに自動付与
//   - レベルは config から動的に切替可能(将来の SIGHUP rotate 対応の布石)
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New は構造化 JSON ログを生成する。
//
//	level: debug / info / warn / error
//	serviceName: ログに自動付与する service タグ
//	version: ビルドバージョン
//	out: 出力先(通常 os.Stdout、テストは bytes.Buffer)
func New(level, serviceName, version string, out io.Writer) *slog.Logger {
	l := slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level: parseLevel(level),
	}))
	return l.With(
		slog.String("service", serviceName),
		slog.String("version", version),
	)
}

// Discard はテスト用、ログを完全に捨てる logger。
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// parseLevel は文字列を slog.Level に変換する。
// 不明な値は info にフォールバック(設定エラーで daemon が死なないため)。
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
