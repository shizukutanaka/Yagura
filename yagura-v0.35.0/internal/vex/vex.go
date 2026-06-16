// Package vex は OpenVEX(Vulnerability Exploitability eXchange)ドキュメントを
// 決定論的に生成・検証する。
//
// 動機: Yagura は SBOM(「何が入っているか」)を生成するが、SBOM 単体は脆弱性を
// 過剰報告する(arXiv 2511.20313 SBOM reality-check)。VEX は「どの脆弱性がこの製品
// 文脈で実際に影響するか/しないか」を機械可読に伝える補完アーティファクト
// (not_affected / affected / fixed / under_investigation + justification)。
// risk_triage(SSVC/EPSS の exploitability 推論)や運用者の判断を OpenVEX に束ねる。
//
// 本 package は OpenVEX v0.2.0 形式の canonical builder + validator(stdlib のみ、
// ADR-0001)。VEX は「主張」であって検証エンジンではない(producer が責任を持つ)ため、
// Yagura は well-formed な文書の生成と構造検証に徹する。
package vex

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"time"
)

const contextURL = "https://openvex.dev/ns/v0.2.0"

// Status は OpenVEX のステータス。
type Status string

const (
	// StatusNotAffected indicates the product is not affected by the vulnerability.
	StatusNotAffected Status = "not_affected"
	// StatusAffected indicates the product is affected and no fix is yet available.
	StatusAffected Status = "affected"
	// StatusFixed indicates the vulnerability is fixed in the product version.
	StatusFixed Status = "fixed"
	// StatusUnderInvestigation indicates triage is in progress; verdict not yet known.
	StatusUnderInvestigation Status = "under_investigation"
)

// Justification は not_affected の根拠(OpenVEX 列挙)。
type Justification string

const (
	// JustComponentNotPresent: the vulnerable component is not present in the product.
	JustComponentNotPresent Justification = "component_not_present"
	// JustVulnerableCodeNotPresent: the vulnerable code path is absent in this build.
	JustVulnerableCodeNotPresent Justification = "vulnerable_code_not_present"
	// JustVulnerableCodeNotInExecutePath: the path exists but is never executed in context.
	JustVulnerableCodeNotInExecutePath Justification = "vulnerable_code_not_in_execute_path"
	// JustVulnerableCodeCannotBeControlled: the code is present but adversary cannot trigger it.
	JustVulnerableCodeCannotBeControlled Justification = "vulnerable_code_cannot_be_controlled_by_adversary"
	// JustInlineMitigationsAlreadyExist: input validation or other controls block exploitation.
	JustInlineMitigationsAlreadyExist Justification = "inline_mitigations_already_exist"
)

// Vuln / Product / Statement / Document は OpenVEX の構造。
type Vuln struct {
	ID          string `json:"@id,omitempty"` // NVD/OSV など脆弱性の正準 URL(任意)
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Subcomponent は影響を受ける product 内の下位コンポーネント(例: pkg:golang/net/http)。
// Yagura は Go stdlib のみ依存するため、stdlib CVE はこの粒度で表現する。
type Subcomponent struct {
	ID string `json:"@id"`
}
// Product represents an OpenVEX product (e.g. a package URI) that a statement applies to.
type Product struct {
	ID            string         `json:"@id"`
	Subcomponents []Subcomponent `json:"subcomponents,omitempty"`
}

// Statement binds a vulnerability to one or more products with a triage verdict.
type Statement struct {
	Vulnerability   Vuln          `json:"vulnerability"`
	Products        []Product     `json:"products,omitempty"`
	Status          Status        `json:"status"`
	Justification   Justification `json:"justification,omitempty"`
	ImpactStatement string        `json:"impact_statement,omitempty"`
	ActionStatement string        `json:"action_statement,omitempty"`
}

// Document is a complete OpenVEX v0.2.0 document: metadata + ordered statements.
type Document struct {
	Context    string      `json:"@context"`
	ID         string      `json:"@id"`
	Author     string      `json:"author"`
	Timestamp  string      `json:"timestamp"`
	Version    int         `json:"version"`
	Tooling    string      `json:"tooling,omitempty"`
	Statements []Statement `json:"statements"`
}

var validStatuses = map[Status]bool{
	StatusNotAffected: true, StatusAffected: true, StatusFixed: true, StatusUnderInvestigation: true,
}
var validJustifications = map[Justification]bool{
	JustComponentNotPresent: true, JustVulnerableCodeNotPresent: true,
	JustVulnerableCodeNotInExecutePath: true, JustVulnerableCodeCannotBeControlled: true,
	JustInlineMitigationsAlreadyExist: true,
}

// Build は statements から canonical な OpenVEX Document を構築する。
// status 未指定は under_investigation。statements は (vuln name, 先頭 product id) で
// 安定整列。@id は内容ハッシュ由来で決定論的。timestamp は now を使う。
func Build(author string, now time.Time, stmts []Statement) Document {
	if strings.TrimSpace(author) == "" {
		author = "yagura"
	}
	out := make([]Statement, 0, len(stmts))
	for _, s := range stmts {
		s.Vulnerability.Name = strings.TrimSpace(s.Vulnerability.Name)
		if s.Status == "" {
			s.Status = StatusUnderInvestigation
		}
		out = append(out, s)
	}
	sortStatements(out)
	d := Document{
		Context:    contextURL,
		Author:     author,
		Timestamp:  now.UTC().Format(time.RFC3339),
		Version:    1,
		Statements: out,
	}
	d.ID = "urn:yagura:vex:" + contentHash(d.Author, d.Timestamp, out)
	return d
}

// Merge は既存 VEX 文書に新規脆弱性の statement を加えた改訂版を返す。
//
// incremental discovery 向け: 新しいスキャン(OSV など)が CVE を見つけるたび、
// base に未登録の vuln だけを追加する。既存 statement の verdict(not_affected /
// fixed / affected など、運用者が triage した結果)は決して変更しない — 再スキャンで
// triage を失わないため。新規 status 未指定は under_investigation。
//
// 新規が 1 件も無ければ base をそのまま返す(no-op は冪等で @id/version も不変)。
// 新規があれば version を +1 し、timestamp/@id を now で再計算した改訂版を返す。
func Merge(base Document, additions []Statement, now time.Time) Document {
	have := make(map[string]bool, len(base.Statements))
	for _, s := range base.Statements {
		have[strings.TrimSpace(s.Vulnerability.Name)] = true
	}
	merged := append([]Statement(nil), base.Statements...)
	added := 0
	for _, s := range additions {
		name := strings.TrimSpace(s.Vulnerability.Name)
		if name == "" || have[name] {
			continue
		}
		have[name] = true
		s.Vulnerability.Name = name
		if s.Status == "" {
			s.Status = StatusUnderInvestigation
		}
		merged = append(merged, s)
		added++
	}
	if added == 0 {
		return base
	}
	sortStatements(merged)
	author := base.Author
	if strings.TrimSpace(author) == "" {
		author = "yagura"
	}
	ctx := base.Context
	if ctx == "" {
		ctx = contextURL
	}
	ver := base.Version
	if ver < 1 {
		ver = 1
	}
	d := Document{
		Context:    ctx,
		Author:     author,
		Timestamp:  now.UTC().Format(time.RFC3339),
		Version:    ver + 1,
		Statements: merged,
	}
	d.ID = "urn:yagura:vex:" + contentHash(d.Author, d.Timestamp, merged)
	return d
}

// sortStatements は (vuln name, 先頭 product id) で安定整列する(決定論的出力)。
func sortStatements(s []Statement) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].Vulnerability.Name != s[j].Vulnerability.Name {
			return s[i].Vulnerability.Name < s[j].Vulnerability.Name
		}
		return firstProduct(s[i]) < firstProduct(s[j])
	})
}

// Validate は OpenVEX Document の構造的問題を返す(空なら OK)。
// Validate returns a list of structural problems in d (empty = valid).
// Checks: @context presence, statement count, vuln name, status, justification
// (not_affected requires one), action_statement (affected should have one), product @id.
func Validate(d Document) []string {
	var issues []string
	if d.Context == "" {
		issues = append(issues, "missing @context")
	}
	if len(d.Statements) == 0 {
		issues = append(issues, "no statements")
	}
	for i, s := range d.Statements {
		who := s.Vulnerability.Name
		if who == "" {
			who = fmt.Sprintf("statement #%d", i)
			issues = append(issues, fmt.Sprintf("%s: vulnerability.name is required", who))
		}
		if !validStatuses[s.Status] {
			issues = append(issues, fmt.Sprintf("%s: invalid status %q (not_affected/affected/fixed/under_investigation)", who, s.Status))
		}
		if s.Status == StatusNotAffected {
			if s.Justification == "" && s.ImpactStatement == "" {
				issues = append(issues, fmt.Sprintf("%s: not_affected requires a justification or an impact_statement", who))
			}
			if s.Justification != "" && !validJustifications[s.Justification] {
				issues = append(issues, fmt.Sprintf("%s: invalid justification %q", who, s.Justification))
			}
		}
		if s.Status == StatusAffected && s.ActionStatement == "" {
			issues = append(issues, fmt.Sprintf("%s: affected should include an action_statement (remediation guidance)", who))
		}
		for j, p := range s.Products {
			if strings.TrimSpace(p.ID) == "" {
				issues = append(issues, fmt.Sprintf("%s: product #%d is missing an @id", who, j))
			}
		}
	}
	return issues
}

// ParseAndValidate は OpenVEX JSON を読み、構造的問題(空なら OK)を返す。
// JSON 自体が壊れている場合のみ error を返す(lint 不能)。docs/vex/*.json の
// CI 検証(yagura vex-audit)で使う。
func ParseAndValidate(data []byte) (Document, []string, error) {
	var d Document
	if err := json.Unmarshal(data, &d); err != nil {
		return Document{}, nil, err
	}
	return d, Validate(d), nil
}

func firstProduct(s Statement) string {
	if len(s.Products) > 0 {
		return s.Products[0].ID
	}
	return ""
}

func contentHash(author, ts string, stmts []Statement) string {
	h := fnv.New32a()
	b, _ := json.Marshal(stmts)
	_, _ = h.Write([]byte(author))
	_, _ = h.Write([]byte(ts))
	_, _ = h.Write(b)
	return fmt.Sprintf("%08x", h.Sum32())
}
