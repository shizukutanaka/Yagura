// readmedoc_test.go: the README advertises a fixed MCP tool count
// ("## MCP tools (N total)"). Unlike SKILL.md (guarded by skilldoc_test) this
// prose had no guard and silently drifted to 62 while 71 tools were registered.
// This ties the README claim to the actual registered count so a mismatch fails
// CI — same "逸脱を物理的に潰す" guard, applied to the user-facing README.
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

func TestReadmeDoc_ToolCountMatchesRegistered(t *testing.T) {
	// actual registered tool count (same path RegisterDefaultTools uses at startup)
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := mcp.New("", nil)
	mcp.RegisterDefaultTools(s, mcp.Deps{Registry: reg, Now: func() time.Time { return time.Unix(0, 0) }})
	registered := len(s.ToolNames())

	const path = "../../README.md"
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := regexp.MustCompile(`## MCP tools \((\d+) total\)`).FindSubmatch(b)
	if m == nil {
		t.Fatalf("%s: could not find a \"## MCP tools (N total)\" header to verify", path)
	}
	stated, _ := strconv.Atoi(string(m[1]))
	if stated != registered {
		t.Errorf("%s advertises %d MCP tools but %d are registered — update the README header",
			path, stated, registered)
	}
}
