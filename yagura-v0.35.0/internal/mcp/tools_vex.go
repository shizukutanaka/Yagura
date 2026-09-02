// tools_vex.go: yagura_vex — OpenVEX(Vulnerability Exploitability eXchange)文書を
// 決定論的に生成・検証する(internal/vex)。
//
// SBOM は「何が入っているか」を出すが、SBOM 単体は脆弱性を過剰報告する。VEX は
// 「どの脆弱性がこの製品文脈で実際に影響するか/しないか」(not_affected / affected /
// fixed / under_investigation + justification)を機械可読に伝える補完アーティファクト。
// risk_triage(SSVC/EPSS の exploitability 推論)や運用者の判断を OpenVEX v0.2.0 に束ねる。
//
// Yagura は LLM を呼ばず、well-formed な文書の canonical 生成と構造 lint に徹する
// (VEX は producer 責任の「主張」であり検証エンジンではない)。@id は内容ハッシュ由来で
// 決定論的。timestamp は Deps.Now() を使うので test で固定できる。

package mcp

import (
	"context"
	"encoding/json"

	"github.com/shizukutanaka/yagura/internal/vex"
)

func buildVEXTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_vex",
		Title:       "Build VEX Document",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[G] Build/merge + lint an OpenVEX v0.2.0 doc from per-CVE statements (not_affected/affected/fixed).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"author": map[string]any{
					"type":        "string",
					"description": "VEX author identity (default 'yagura').",
				},
				"tooling": map[string]any{
					"type":        "string",
					"description": "optional tool identifier that produced this doc.",
				},
				"base": map[string]any{
					"type":        "object",
					"description": "optional existing OpenVEX doc; statements are merged in (new vulns only, existing verdicts preserved, version bumped).",
				},
				"statements": map[string]any{
					"type":        "array",
					"description": "per-vulnerability statements.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"cve":           map[string]any{"type": "string", "description": "vulnerability id/name (e.g. CVE-2025-0001)."},
							"vuln_id":       map[string]any{"type": "string", "description": "canonical vulnerability URL (NVD/OSV @id, optional)."},
							"description":   map[string]any{"type": "string"},
							"product":       map[string]any{"type": "string", "description": "affected product @id (e.g. pkg:golang/...)."},
							"subcomponents": map[string]any{"type": "array", "description": "affected subcomponent @ids within the product (e.g. pkg:golang/net/http).", "items": map[string]any{"type": "string"}},
							"status":        map[string]any{"type": "string", "description": "not_affected/affected/fixed/under_investigation (default under_investigation)."},
							"justification": map[string]any{"type": "string", "description": "OpenVEX justification when not_affected."},
							"impact":        map[string]any{"type": "string", "description": "impact_statement (alternative to justification for not_affected)."},
							"action":        map[string]any{"type": "string", "description": "action_statement (remediation when affected)."},
						},
						"required": []string{"cve"},
					},
				},
			},
			"required": []string{"statements"},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Author     string        `json:"author"`
				Tooling    string        `json:"tooling"`
				Base       *vex.Document `json:"base"`
				Statements []struct {
					CVE           string   `json:"cve"`
					VulnID        string   `json:"vuln_id"`
					Description   string   `json:"description"`
					Product       string   `json:"product"`
					Subcomponents []string `json:"subcomponents"`
					Status        string   `json:"status"`
					Justification string   `json:"justification"`
					Impact        string   `json:"impact"`
					Action        string   `json:"action"`
				} `json:"statements"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Cause: err}
			}
			if len(in.Statements) == 0 {
				return nil, &ToolError{Code: "invalid_input", Message: "at least one statement is required"}
			}
			stmts := make([]vex.Statement, 0, len(in.Statements))
			for _, s := range in.Statements {
				st := vex.Statement{
					Vulnerability:   vex.Vuln{ID: s.VulnID, Name: s.CVE, Description: s.Description},
					Status:          vex.Status(s.Status),
					Justification:   vex.Justification(s.Justification),
					ImpactStatement: s.Impact,
					ActionStatement: s.Action,
				}
				if s.Product != "" {
					p := vex.Product{ID: s.Product}
					for _, sc := range s.Subcomponents {
						if sc != "" {
							p.Subcomponents = append(p.Subcomponents, vex.Subcomponent{ID: sc})
						}
					}
					st.Products = []vex.Product{p}
				}
				stmts = append(stmts, st)
			}
			var doc vex.Document
			if in.Base != nil {
				doc = vex.Merge(*in.Base, stmts, d.Now())
			} else {
				doc = vex.Build(in.Author, d.Now(), stmts)
			}
			if in.Tooling != "" && doc.Tooling == "" {
				doc.Tooling = in.Tooling
			}
			return map[string]any{
				"document": doc,
				"issues":   vex.Validate(doc),
			}, nil
		},
	}
}
