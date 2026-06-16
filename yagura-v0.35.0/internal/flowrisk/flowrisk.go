// Package flowrisk は操作シーケンスに潜む危険な *順序* を検出する(新視点 v0.36)。
//
// ソクラテス的動機:
//
//	既存のレンズ(capability surface / review gate / diff added・removed)は
//	いずれも *単一時点* を見る。だが AI エージェントは時間をかけて複数の操作を行い、
//	個々は無害でも合わさると kill chain になる順序がある:
//	  - 秘密読取 → ネットワーク送信(exfiltration)
//	  - 未信頼コンテンツ取得 → exec(injection→実行)
//	  - 未信頼コンテンツ取得 → ディスク書込
//	injectscan が *内容* で見るインジェクションを、本 package は *行動シーケンス* の
//	層で見る(temporal/flow の視点)。taint-flow 的に source→sink の順序関係を走査する。
//
// stdlib のみ(ADR-0001)。決定論的(flow ルールは固定順、各 kind につき最早ペア)。
package flowrisk

import "strings"

// Capability constants (normalised operation kind, used by ClassifyTool and Analyze).
const (
	// CapSecretRead classifies a tool that reads secrets, credentials, or env vars.
	CapSecretRead = "secret-read"
	// CapNetwork classifies a tool that sends data over the network (HTTP, TCP, SSH).
	CapNetwork = "network"
	// CapExec classifies a tool that spawns a subprocess or evaluates code.
	CapExec = "exec"
	// CapFetchUntrusted classifies a tool that reads content from an untrusted source.
	CapFetchUntrusted = "fetch-untrusted"
	// CapWrite classifies a tool that writes files or mutates the filesystem.
	CapWrite = "write"
	// CapOther is the fallback for tools that do not match any of the above.
	CapOther = "other"
)

// Step は 1 操作(正規化済み capability つき)。
type Step struct {
	Name       string `json:"name"`
	Capability string `json:"capability"`
}

// FlowRisk は source→sink の危険な順序 1 件。
type FlowRisk struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	From     int    `json:"from"` // source step の位置(0-based)
	FromName string `json:"from_name"`
	To       int    `json:"to"` // sink step の位置(0-based)
	ToName   string `json:"to_name"`
	Message  string `json:"message"`
}

// flows は「source が先、sink が後」だと危険な順序の定義。
var flows = []struct {
	kind, severity, from, to, msg string
}{
	{"exfiltration", "high", CapSecretRead, CapNetwork,
		"secret/credential read is followed by a network send — possible exfiltration"},
	{"injection-to-exec", "high", CapFetchUntrusted, CapExec,
		"untrusted content is fetched then code is executed — possible injection-to-execution"},
	{"untrusted-to-disk", "medium", CapFetchUntrusted, CapWrite,
		"untrusted content is fetched then written to disk"},
}

// Analyze は steps を走査し、各 flow ルールについて最早の source とその後の最早の
// sink のペアを 1 件報告する(決定論的)。
func Analyze(steps []Step) []FlowRisk {
	var out []FlowRisk
	for _, fl := range flows {
		src := -1
		for i, s := range steps {
			if s.Capability == fl.from {
				src = i
				break
			}
		}
		if src < 0 {
			continue
		}
		sink := -1
		for j := src + 1; j < len(steps); j++ {
			if steps[j].Capability == fl.to {
				sink = j
				break
			}
		}
		if sink < 0 {
			continue
		}
		out = append(out, FlowRisk{
			Kind: fl.kind, Severity: fl.severity,
			From: src, FromName: steps[src].Name,
			To: sink, ToName: steps[sink].Name,
			Message: fl.msg,
		})
	}
	return out
}

// ClassifyTool はツール/操作名を capability に正規化する(best-effort、小文字部分一致)。
// 入力が既知の capability そのものならそのまま返す。判定順は誤分類を避けるため
// secret→fetch→exec→network→write の優先度。
func ClassifyTool(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	case CapSecretRead, CapNetwork, CapExec, CapFetchUntrusted, CapWrite, CapOther:
		return n
	}
	switch {
	case containsAny(n, "getenv", "secret", "credential", "token", "password", "id_rsa", ".ssh", ".env", "private_key"):
		return CapSecretRead
	case containsAny(n, "fetch", "download", "scrape", "browse", "read_url", "readurl", "geturl", "get_url", "wget"):
		return CapFetchUntrusted
	case containsAny(n, "exec", "shell", "spawn", "command", "/bin/", "bash", "system("):
		return CapExec
	case containsAny(n, "http", "post", "upload", "send", "email", "webhook", "request", "curl", "net."):
		return CapNetwork
	case containsAny(n, "write", "create", "save", "put_file", "writefile", "openfile", "mkdir"):
		return CapWrite
	}
	return CapOther
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
