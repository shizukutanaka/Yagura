// Package publicityscan は「公開リポへ出す前の leak チェック(publicity review)」を
// rule-based / deterministic に行う。
//
// 着想は Claude Code スキル自己改善ループの publicity-review ゲート — SKILL.md / docs /
// PR を public repo へ publish する前に、絶対 home パス(OS ユーザ名が漏れる)、内部
// hostname、private IP、ユーザ識別子(email)といった「公開すると困る」痕跡を検出する。
//
// secretscan が credential(鍵・トークン)を見るのに対し、本 scanner はそれ以外の
// 「身元・内部構造の leak」を補完する。全 check は heuristic(正規表現、RE2 のみ)で
// 決定論的。誤検出を抑えるため generic な placeholder(runner/ubuntu/user 等)や
// settings.local.json のような filename は除外する。
package publicityscan

import (
	"regexp"
	"sort"
	"strings"
)

// Severity は finding の深刻度。
type Severity string

const (
	// SevHigh はホームパス・内部ホスト・プライベート IP 等の高深刻度 leak finding。
	SevHigh Severity = "HIGH"
	// SevMedium はメールアドレス・汎用社内識別子等の中深刻度 leak finding。
	SevMedium Severity = "MEDIUM"
	// SevLow は軽微または任意対応の低深刻度 leak finding。
	SevLow Severity = "LOW"
)

// Finding は 1 件の leak 検出(ghaaudit.Finding と同じ shape）。
type Finding struct {
	RuleID      string   `json:"rule_id"`
	Severity    Severity `json:"severity"`
	Line        int      `json:"line"`
	Snippet     string   `json:"snippet"`
	Description string   `json:"description"`
	Suggestion  string   `json:"suggestion"`
}

var (
	reUnixHome     = regexp.MustCompile(`(?:/Users/|/home/)([A-Za-z0-9._-]+)`)
	reWinHome      = regexp.MustCompile(`(?i)[A-Za-z]:\\Users\\([A-Za-z0-9._-]+)`)
	reInternalHost = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9.-]*\.(?:local|internal|corp|lan|intranet)\b`)
	rePrivateIP    = regexp.MustCompile(`\b(?:10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3})\b`)
	reEmail        = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
)

// genericUsers は home パスに出ても個人特定にならない placeholder/CI 名(誤検出抑制）。
var genericUsers = map[string]bool{
	"root": true, "runner": true, "runneradmin": true, "ubuntu": true,
	"user": true, "users": true, "app": true, "ci": true, "home": true,
	"node": true, "vscode": true, "admin": true, "codespace": true, "build": true,
	// explicit placeholders used in docs/examples
	"you": true, "username": true, "yourname": true, "name": true, "me": true,
}

// exampleEmailDomains は flag しない email ドメイン(ドキュメント例 / no-reply）。
var exampleEmailDomains = []string{"example.com", "example.org", "example.net", "example.edu", "localhost"}

// Scan は content 全体を行単位で走査し、publicity leak を返す。決定論的。
// 各 check は独立(order のみ固定)なので行単位に checkXxx へ委譲する。
func Scan(content string) []Finding {
	var out []Finding
	for i, line := range strings.Split(content, "\n") {
		ln := i + 1
		out = append(out, checkHomePaths(line, ln)...)
		out = append(out, checkInternalHost(line, ln)...)
		out = append(out, checkPrivateIP(line, ln)...)
		out = append(out, checkEmail(line, ln)...)
	}
	return out
}

// checkHomePaths は絶対 home パス(Unix/Windows、OS ユーザ名の leak)を検出する。
func checkHomePaths(line string, ln int) []Finding {
	var out []Finding
	for _, m := range reUnixHome.FindAllStringSubmatch(line, -1) {
		if !genericUsers[strings.ToLower(m[1])] {
			out = append(out, Finding{
				RuleID: "absolute-home-path", Severity: SevHigh, Line: ln, Snippet: m[0],
				Description: "absolute home path leaks an OS username and local filesystem layout",
				Suggestion:  "replace with a relative path, $HOME, or a placeholder like /Users/<you>",
			})
		}
	}
	for _, m := range reWinHome.FindAllStringSubmatch(line, -1) {
		if !genericUsers[strings.ToLower(m[1])] {
			out = append(out, Finding{
				RuleID: "absolute-home-path", Severity: SevHigh, Line: ln, Snippet: m[0],
				Description: "absolute Windows home path leaks a username",
				Suggestion:  `replace with %USERPROFILE% or a placeholder`,
			})
		}
	}
	return out
}

// checkInternalHost は内部 hostname(末尾が .local 等)を検出する。filename
// (settings.local.json 等)誤検出を避けるため、直後が '.' なら skip。
func checkInternalHost(line string, ln int) []Finding {
	idxs := reInternalHost.FindAllStringIndex(line, -1)
	out := make([]Finding, 0, len(idxs))
	for _, idx := range idxs {
		if idx[1] < len(line) && line[idx[1]] == '.' {
			continue
		}
		out = append(out, Finding{
			RuleID: "internal-hostname", Severity: SevMedium, Line: ln, Snippet: line[idx[0]:idx[1]],
			Description: "internal hostname may reveal internal network topology",
			Suggestion:  "use a public hostname or a placeholder in published docs",
		})
	}
	return out
}

// checkPrivateIP は private IP(127.x loopback は除外)を検出する。直後が '/' の
// CIDR(10.0.0.0/8 等)はレンジ定義であって host leak ではないので除外する。
func checkPrivateIP(line string, ln int) []Finding {
	idxs := rePrivateIP.FindAllStringIndex(line, -1)
	out := make([]Finding, 0, len(idxs))
	for _, idx := range idxs {
		if idx[1] < len(line) && line[idx[1]] == '/' {
			continue
		}
		out = append(out, Finding{
			RuleID: "private-ip", Severity: SevMedium, Line: ln, Snippet: line[idx[0]:idx[1]],
			Description: "private/RFC1918 IP address reveals internal addressing",
			Suggestion:  "remove or replace with a placeholder before publishing",
		})
	}
	return out
}

// checkEmail はユーザ email(ドキュメント例 / no-reply は除外)を検出する。
func checkEmail(line string, ln int) []Finding {
	matches := reEmail.FindAllString(line, -1)
	out := make([]Finding, 0, len(matches))
	for _, m := range matches {
		if isExampleEmail(m) {
			continue
		}
		out = append(out, Finding{
			RuleID: "user-email", Severity: SevLow, Line: ln, Snippet: m,
			Description: "email address is a personal/user identifier",
			Suggestion:  "confirm this address is meant to be public; otherwise redact",
		})
	}
	return out
}

// isExampleEmail は example / no-reply 系ドメインか(flag しない)。
func isExampleEmail(addr string) bool {
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return true
	}
	local := strings.ToLower(addr[:at])
	domain := strings.ToLower(addr[at+1:])
	if strings.Contains(local, "noreply") || strings.Contains(local, "no-reply") {
		return true
	}
	for _, d := range exampleEmailDomains {
		if domain == d || strings.HasSuffix(domain, "."+d) {
			return true
		}
	}
	return false
}

// Summary は severity 別集計。
type Summary struct {
	Total      int            `json:"total"`
	BySeverity map[string]int `json:"by_severity"`
}

// Summarize は findings を severity 別に集計する。
func Summarize(findings []Finding) Summary {
	s := Summary{BySeverity: map[string]int{}}
	for _, f := range findings {
		s.Total++
		s.BySeverity[string(f.Severity)]++
	}
	return s
}

// SortFindings は line 昇順, 同 line は severity(HIGH→LOW)で安定整列する。
func SortFindings(fs []Finding) {
	rank := map[Severity]int{SevHigh: 0, SevMedium: 1, SevLow: 2}
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Line != fs[j].Line {
			return fs[i].Line < fs[j].Line
		}
		return rank[fs[i].Severity] < rank[fs[j].Severity]
	})
}
