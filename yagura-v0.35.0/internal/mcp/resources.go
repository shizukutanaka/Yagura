// resources.go: registry-backed MCP Resources source (v0.114.0).
//
// yagura の読み取り専用ポートフォリオ状態(registry)を MCP の Resources primitive
// として公開する。tool(yagura_list / yagura_get)経由でも同じ情報は取れるが、
// Resources は URI 指定で直接読める・browse/cache しやすいという別モダリティ。
// action 形の tools/call と区別された read-only セマンティクスを client に与える。
//
// URI scheme:
//   - yagura://registry          … 全プロジェクトのスナップショット(count + projects)
//   - yagura://project/{slug}     … 単一プロジェクトの JSON
//
// zero-dep(ADR-0001): registry と encoding/json のみ。
package mcp

import (
	"encoding/json"
	"strings"

	"github.com/shizukutanaka/yagura/internal/registry"
)

const (
	registryURI      = "yagura://registry"
	projectURIPrefix = "yagura://project/"
)

// registryResourceSource は registry を backing にした ResourceSource。
type registryResourceSource struct {
	reg *registry.Registry
}

// NewRegistryResourceSource は registry を公開する ResourceSource を返す。
// reg が nil の場合でも安全(空リストを返す)。
func NewRegistryResourceSource(reg *registry.Registry) ResourceSource {
	return &registryResourceSource{reg: reg}
}

// ListResources は collection リソース + 登録プロジェクトごとの 1 リソースを返す。
// registry.List() は slug 昇順(registry の Deterministic output)なので順序は安定。
func (s *registryResourceSource) ListResources() []Resource {
	out := []Resource{{
		URI:         registryURI,
		Name:        "registry",
		Title:       "Project Registry",
		Description: "Snapshot of every registered project (count + projects array).",
		MIMEType:    "application/json",
	}}
	if s.reg == nil {
		return out
	}
	for _, p := range s.reg.List() {
		name := p.DisplayName
		if name == "" {
			name = p.Slug
		}
		out = append(out, Resource{
			URI:         projectURIPrefix + p.Slug,
			Name:        p.Slug,
			Title:       name,
			Description: "Registry facts for project " + p.Slug + ".",
			MIMEType:    "application/json",
		})
	}
	return out
}

// ReadResource は collection または単一プロジェクトの JSON を返す。
func (s *registryResourceSource) ReadResource(uri string) (ResourceContents, bool) {
	if s.reg == nil {
		return ResourceContents{}, false
	}
	if uri == registryURI {
		projects := s.reg.List()
		body, err := json.Marshal(map[string]any{
			"count":    len(projects),
			"projects": projects,
		})
		if err != nil {
			return ResourceContents{}, false
		}
		return ResourceContents{URI: uri, MIMEType: "application/json", Text: string(body)}, true
	}
	slug, ok := strings.CutPrefix(uri, projectURIPrefix)
	if !ok || slug == "" {
		return ResourceContents{}, false
	}
	p, err := s.reg.Get(slug)
	if err != nil || p == nil {
		return ResourceContents{}, false
	}
	body, err := json.Marshal(p)
	if err != nil {
		return ResourceContents{}, false
	}
	return ResourceContents{URI: uri, MIMEType: "application/json", Text: string(body)}, true
}
