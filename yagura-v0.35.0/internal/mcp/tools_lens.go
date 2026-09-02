package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/lens"
	"github.com/shizukutanaka/yagura/internal/srcfiles"
)

// buildLensTool は **29 個の「1 レンズ 1 tool」を置き換える 1 つの tool**(v0.129.0)。
//
// 何を削ったか:
//
//	v0.128.0 まで、複雑度を 1 つ測るのに `yagura_complexity` が `files`
//	(ファイル名→内容の map)を **必須** で要求していた。29 レンズすべてが同じ形で、
//	つまり「このリポジトリを検査して」と頼むには、ソース全体を LLM の context に
//	通す必要があった。このリポジトリで言えば約 3.3 MB ≒ 80 万トークン——
//	slug 1 個(約 10 トークン)で足りる仕事に対して、である。
//
//	v0.118.0 は同じ矛盾を `yagura_portfolio_quality` 1 つで解消し、CHANGELOG に
//	「daemon がディスクを読むのでソース内容が LLM context を 1 バイトも通らない」と
//	書いた。29 個には波及していなかった。本 tool がその波及であり、同時に
//	tools/list の handshake から約 15 KB を削る。
//
// 能力は 1 つも減らしていない: レンズ 29 種は `internal/lens` の表に全て在り、
// `lens` パラメータで選ぶ。`lens` を省くと **全レンズの件数だけ** を返すので、
// 「まずどこを見るべきか」を 1 往復で決められる(本文は選んでから取る)。
func buildLensTool(d Deps) *Tool {
	names := lens.Names()
	enum := make([]any, 0, len(names))
	for _, n := range names {
		enum = append(enum, n)
	}
	return &Tool{
		Name:        "yagura_lens",
		Title:       "Code Quality Lenses (29 in one)",
		Description: "[G] Run any of 29 structural code lenses over a project the daemon reads from disk. Omit `lens` for finding counts across all 29. Needs a slug or dir — never source in context.",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"lens": map[string]any{
					"type": "string",
					"enum": enum,
					// **29 個の説明文をここに並べない**。それは全セッションの handshake に
					// 乗る固定費になる。何がどのレンズかは `lens` を省いた 1 回の呼び出しで
					// summary つきの件数が返るので、必要になった時にだけ払えばよい。
					"description": "which lens to run; omit to get finding counts for all of them WITH one-line summaries (the cheap way to choose one)",
				},
				"slug":       map[string]any{"type": "string", "description": "registered project slug (daemon reads its local_path)"},
				"dir":        map[string]any{"type": "string", "description": "absolute directory to scan; alternative to slug"},
				"threshold":  map[string]any{"type": "integer", "description": "lens threshold where applicable (0 = that lens's default)"},
				"module":     map[string]any{"type": "string", "description": "module path for import resolution (coupling, dep_rank)"},
				"ignore":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "identifiers to allow (predeclared)"},
				"min_lenses": map[string]any{"type": "integer", "description": "lenses that must converge to count as a hotspot"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Lens      string   `json:"lens"`
				Slug      string   `json:"slug"`
				Dir       string   `json:"dir"`
				Threshold int      `json:"threshold"`
				Module    string   `json:"module"`
				Ignore    []string `json:"ignore"`
				MinLenses int      `json:"min_lenses"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			dir := in.Dir
			if dir == "" {
				if in.Slug == "" {
					return nil, &ToolError{Code: "invalid_input", Message: "slug or dir required"}
				}
				if d.Registry == nil {
					return nil, &ToolError{Code: "unavailable", Message: "registry not configured"}
				}
				p, err := d.Registry.Get(in.Slug)
				if err != nil || p == nil {
					return nil, &ToolError{Code: "not_found", Message: "project not registered"}
				}
				if p.LocalPath == "" {
					return nil, &ToolError{Code: "invalid_input", Message: "project has no local_path; set it with yagura_update"}
				}
				dir = p.LocalPath
			}
			// 単一 seam(v0.118.0):走査は必ず srcfiles を通す。
			sr, err := srcfiles.ReadGo(dir)
			if err != nil {
				return nil, &ToolError{Code: "read_failed", Message: err.Error()}
			}
			opts := lens.Options{
				Threshold: in.Threshold,
				Module:    in.Module,
				Ignore:    in.Ignore,
				MinLenses: in.MinLenses,
			}
			out := map[string]any{
				"dir":          dir,
				"files_read":   len(sr.Files),
				"files_total":  sr.Matched,
				"incomplete":   sr.Incomplete(),
				"truncated_by": sr.TruncatedBy,
				"lenses_total": len(lens.Names()),
			}
			if in.Slug != "" {
				out["slug"] = in.Slug
			}
			if in.Lens == "" {
				// 件数だけ。全レンズの本文を返すなら 29 tool のままと変わらない。
				out["counts"] = lens.RunAll(sr.Files, opts)
				out["note"] = "Finding counts across all lenses; call again with `lens` set to get that " +
					"lens's full report. A count of 0 means that lens found nothing in the files that " +
					"were read — check `incomplete` before reading it as clean."
				return out, nil
			}
			res, err := lens.Run(in.Lens, sr.Files, opts)
			if err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: err.Error()}
			}
			out["result"] = res
			return out, nil
		},
	}
}
