package mcp

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/logging"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/registry"
)

// serverWithRegistryResources builds a server whose resource source is backed
// by a registry pre-seeded with the given projects.
func serverWithRegistryResources(t *testing.T, projects ...*project.Project) *Server {
	t.Helper()
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range projects {
		// fill registry-required fields so tests can pass concise literals
		if p.DisplayName == "" {
			p.DisplayName = strings.ToUpper(p.Slug[:1]) + p.Slug[1:]
		}
		if p.Repository == "" {
			p.Repository = "github.com/test/" + p.Slug
		}
		if err := reg.Add(p); err != nil {
			t.Fatalf("seed %s: %v", p.Slug, err)
		}
	}
	s := New("", logging.Discard())
	RegisterDefaultTools(s, Deps{Registry: reg, Now: func() time.Time { return fixedNow }})
	s.SetResourceSource(NewRegistryResourceSource(reg))
	return s
}

func rpc(t *testing.T, s http.Handler, method, params string) map[string]any {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"`
	if params != "" {
		body += `,"params":` + params
	}
	body += `}`
	w := postJSON(t, s, body, "")
	if w.Code != http.StatusOK {
		t.Fatalf("%s: code=%d body=%s", method, w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s unmarshal: %v", method, err)
	}
	if resp["error"] != nil {
		t.Fatalf("%s error: %v", method, resp["error"])
	}
	res, _ := resp["result"].(map[string]any)
	return res
}

// TestInitialize_AdvertisesResourcesCapabilityWhenSourceSet proves the
// handshake declares the resources capability only once a source is wired.
func TestInitialize_AdvertisesResourcesCapabilityWhenSourceSet(t *testing.T) {
	// no source: capability absent
	bare, _ := newServerForTest(t, "")
	caps := rpc(t, bare, "initialize", "")["capabilities"].(map[string]any)
	if _, ok := caps["resources"]; ok {
		t.Errorf("resources capability should be absent when no source is set: %v", caps)
	}

	// with source: capability present
	s := serverWithRegistryResources(t, &project.Project{Slug: "alpha", Priority: 3})
	caps2 := rpc(t, s, "initialize", "")["capabilities"].(map[string]any)
	if _, ok := caps2["resources"]; !ok {
		t.Errorf("resources capability should be present when a source is set: %v", caps2)
	}
}

// TestResourcesList_CollectionPlusPerProject verifies resources/list returns
// the registry collection resource and one resource per registered project.
func TestResourcesList_CollectionPlusPerProject(t *testing.T) {
	s := serverWithRegistryResources(t,
		&project.Project{Slug: "alpha", Priority: 3},
		&project.Project{Slug: "beta", Priority: 1},
	)
	res := rpc(t, s, "resources/list", "")
	raw, ok := res["resources"].([]any)
	if !ok {
		t.Fatalf("resources not a list: %v", res)
	}
	uris := map[string]bool{}
	for _, r := range raw {
		m := r.(map[string]any)
		uris[m["uri"].(string)] = true
		if m["name"] == nil || m["name"] == "" {
			t.Errorf("resource %v missing name", m["uri"])
		}
	}
	for _, want := range []string{"yagura://registry", "yagura://project/alpha", "yagura://project/beta"} {
		if !uris[want] {
			t.Errorf("resources/list missing %q; got %v", want, uris)
		}
	}
}

// TestResourcesRead_Project returns one project's JSON as resource contents.
func TestResourcesRead_Project(t *testing.T) {
	s := serverWithRegistryResources(t, &project.Project{Slug: "alpha", Priority: 3, Language: "Go"})
	res := rpc(t, s, "resources/read", `{"uri":"yagura://project/alpha"}`)
	contents, ok := res["contents"].([]any)
	if !ok || len(contents) == 0 {
		t.Fatalf("contents missing/empty: %v", res)
	}
	c := contents[0].(map[string]any)
	if c["uri"] != "yagura://project/alpha" {
		t.Errorf("content uri = %v", c["uri"])
	}
	if c["mimeType"] != "application/json" {
		t.Errorf("mimeType = %v, want application/json", c["mimeType"])
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(c["text"].(string)), &p); err != nil {
		t.Fatalf("content text not JSON: %v", err)
	}
	if p["slug"] != "alpha" {
		t.Errorf("project slug = %v, want alpha", p["slug"])
	}
}

// TestResourcesRead_Collection returns the whole registry snapshot.
func TestResourcesRead_Collection(t *testing.T) {
	s := serverWithRegistryResources(t,
		&project.Project{Slug: "alpha", Priority: 3},
		&project.Project{Slug: "beta", Priority: 1},
	)
	res := rpc(t, s, "resources/read", `{"uri":"yagura://registry"}`)
	c := res["contents"].([]any)[0].(map[string]any)
	var snap map[string]any
	if err := json.Unmarshal([]byte(c["text"].(string)), &snap); err != nil {
		t.Fatalf("collection text not JSON: %v", err)
	}
	if snap["count"].(float64) != 2 {
		t.Errorf("count = %v, want 2", snap["count"])
	}
}

// TestResourcesRead_UnknownURI returns a JSON-RPC error, not a panic.
func TestResourcesRead_UnknownURI(t *testing.T) {
	s := serverWithRegistryResources(t, &project.Project{Slug: "alpha", Priority: 3})
	w := postJSON(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"yagura://project/nope"}}`, "")
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == nil {
		t.Errorf("expected error for unknown uri, got: %s", w.Body.String())
	}
}

// projectWithPlan seeds a project whose LocalPath holds a Plan.md.
func projectWithPlan(t *testing.T, slug, planBody string) *project.Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Plan.md"), []byte(planBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return &project.Project{Slug: slug, Priority: 3, LocalPath: dir}
}

// TestResourcesList_IncludesPlanWhenPresent verifies a project with a Plan.md
// gets a yagura://project/{slug}/plan resource, while one without does not.
func TestResourcesList_IncludesPlanWhenPresent(t *testing.T) {
	withPlan := projectWithPlan(t, "hadescribe", "# Plan\n- [x] done\n- [ ] todo\n")
	noPlan := &project.Project{Slug: "bare", Priority: 1} // no LocalPath → no plan
	s := serverWithRegistryResources(t, withPlan, noPlan)

	res := rpc(t, s, "resources/list", "")
	uris := map[string]bool{}
	for _, r := range res["resources"].([]any) {
		uris[r.(map[string]any)["uri"].(string)] = true
	}
	if !uris["yagura://project/hadescribe/plan"] {
		t.Errorf("expected plan resource for project with Plan.md; got %v", uris)
	}
	if uris["yagura://project/bare/plan"] {
		t.Errorf("project without a Plan.md must not list a plan resource")
	}
}

// TestResourcesRead_Plan returns the raw Plan.md markdown.
func TestResourcesRead_Plan(t *testing.T) {
	body := "# Plan\n## 目的\nship it\n- [x] a\n- [ ] b\n"
	s := serverWithRegistryResources(t, projectWithPlan(t, "hadescribe", body))
	res := rpc(t, s, "resources/read", `{"uri":"yagura://project/hadescribe/plan"}`)
	c := res["contents"].([]any)[0].(map[string]any)
	if c["mimeType"] != "text/markdown" {
		t.Errorf("plan mimeType = %v, want text/markdown", c["mimeType"])
	}
	if c["text"] != body {
		t.Errorf("plan text = %q, want %q", c["text"], body)
	}
}

// TestResourcesRead_PlanMissing errors for a project that has no Plan.md.
func TestResourcesRead_PlanMissing(t *testing.T) {
	s := serverWithRegistryResources(t, &project.Project{Slug: "bare", Priority: 1})
	w := postJSON(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"yagura://project/bare/plan"}}`, "")
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == nil {
		t.Errorf("expected error reading a missing Plan.md")
	}
}
