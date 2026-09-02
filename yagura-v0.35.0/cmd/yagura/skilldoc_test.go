// skilldoc_test.go: the shipped skill doc advertises a fixed MCP tool count
// (".claude/skills/yagura/SKILL.md" → "exposes N MCP tools"). That prose drifts
// silently as tools are added. This guard ties it to the actual registered count
// so a mismatch fails CI — "逸脱を物理的に潰す" applied to the skill doc.
package main

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/mcp"
	"github.com/shizukutanaka/yagura/internal/registry"
)

func TestSkillDoc_ToolCountMatchesRegistered(t *testing.T) {
	// actual registered tool count (same path RegisterDefaultTools uses at startup)
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := mcp.New("", nil)
	mcp.RegisterDefaultTools(s, mcp.Deps{Registry: reg, Now: func() time.Time { return time.Unix(0, 0) }})
	registered := len(s.ToolNames())

	const path = "../../.claude/skills/yagura/SKILL.md"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := regexp.MustCompile(`exposes (\d+) MCP tools`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s: could not find an \"exposes N MCP tools\" claim to verify", path)
	}
	stated, _ := strconv.Atoi(string(m[1]))
	if stated != registered {
		t.Errorf("%s advertises %d MCP tools but %d are registered — update the skill doc",
			path, stated, registered)
	}
}
