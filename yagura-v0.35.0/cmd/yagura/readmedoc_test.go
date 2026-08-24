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

// ヘッダ 1 行だけを見る旧ガードの盲点(v1.2.1 で実際に露呈): README 本文の
// ASCII 図と散文に「93 MCP tools」が 3 箇所残ったまま、ヘッダだけが 107 → 79 と
// 更新され続けていた。**部分的なガードは「守られている」という誤った安心を作る**
// ——本文のツール数表記は全数を検査する。
func TestReadmeDoc_EveryToolCountMentionMatches(t *testing.T) {
	reg, err := registry.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := mcp.New("", nil)
	mcp.RegisterDefaultTools(s, mcp.Deps{Registry: reg, Now: func() time.Time { return time.Unix(0, 0) }})
	registered := len(s.ToolNames())

	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(\d+) MCP tools`).FindAllStringSubmatch(string(b), -1) {
		if stated, _ := strconv.Atoi(m[1]); stated != registered {
			t.Errorf("README says %q but %d tools are registered — every mention must match, "+
				"not just the section header", m[0], registered)
		}
	}
}
