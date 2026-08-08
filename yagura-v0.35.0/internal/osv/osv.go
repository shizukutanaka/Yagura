// Package osv は OSV.dev API ([1]) への問合せクライアントを提供する。
//
// 設計判断(security spec S1.2):
//   - Yagura は read-only クライアントとして OSV.dev に問合せる
//   - 結果は MCP tool として返すのみ。Yagura は自動修正しない
//   - レスポンスの severity は OSV scheme に従い CRITICAL/HIGH/MEDIUM/LOW に正規化
//   - ゼロ依存(標準ライブラリのみ)
//
// 対応 ecosystem:
//   - Go (Go module path)
//   - Python (PyPI)
//   - JavaScript/TypeScript (npm)
//   - Rust (crates.io)
//   - Ruby (RubyGems)
//   - Java/Kotlin (Maven)
//
// [1] https://google.github.io/osv.dev/api/
package osv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.osv.dev"
	defaultTimeout = 30 * time.Second
	maxResponseLen = 5 << 20 // 5 MB 上限(極端なレスポンス防止)
)

// Severity は OSV scheme で正規化された深刻度。
type Severity string

const (
	// SeverityCritical は CVSS 9.0-10.0 相当の最重大度。
	SeverityCritical Severity = "CRITICAL"
	// SeverityHigh は CVSS 7.0-8.9 相当の高重大度。
	SeverityHigh Severity = "HIGH"
	// SeverityMedium は CVSS 4.0-6.9 相当の中重大度。
	SeverityMedium Severity = "MEDIUM"
	// SeverityLow は CVSS 0.1-3.9 相当の低重大度。
	SeverityLow Severity = "LOW"
	// SeverityUnknown は重大度が判定できない場合。
	SeverityUnknown Severity = "UNKNOWN"
)

// Vuln は単一の脆弱性レコード。
//
// OSV.dev の生レスポンスはより豊富だが、Yagura が必要とするフィールドのみ抽出して
// 安定したスキーマで返す。生レスポンスへの依存を内部に閉じ込めるため。
type Vuln struct {
	ID         string    `json:"id"`         // 例: "GHSA-xxxx-xxxx-xxxx", "CVE-2024-...", "GO-2024-..."
	Summary    string    `json:"summary"`    // 1 行サマリ
	Severity   Severity  `json:"severity"`   // 正規化された深刻度
	CVSSScore  float64   `json:"cvss_score"` // CVSS v3 base score (取得できれば)
	Published  time.Time `json:"published"`  // 公開日
	Modified   time.Time `json:"modified"`   // 最終更新日
	References []string  `json:"references"` // URL 一覧
	Aliases    []string  `json:"aliases"`    // 他名(CVE, GHSA 等)
}

// Client は OSV.dev への問合せクライアント。
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// Option は Client 生成時の設定。
type Option func(*Client)

// WithBaseURL は問合先 URL を上書きする(主にテスト用)。
func WithBaseURL(url string) Option {
	return func(c *Client) { c.baseURL = url }
}

// WithHTTPClient は HTTP クライアントを上書きする。
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// New は Client を生成する。
func New(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		baseURL:    defaultBaseURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Query は単一パッケージの脆弱性を問合せる。
//
// ecosystem は OSV scheme の値(例: "Go", "PyPI", "npm")。
// pkg はパッケージ識別子(Go の場合は module path)。
// version は問合せ対象バージョン。空文字列の場合はパッケージ全体の脆弱性を返す。
//
// 結果は severity 降順 → published 降順でソートされる。
func (c *Client) Query(ctx context.Context, ecosystem, pkg, version string) ([]Vuln, error) {
	if ecosystem == "" {
		return nil, errors.New("osv: ecosystem is required")
	}
	if pkg == "" {
		return nil, errors.New("osv: package is required")
	}

	reqBody := queryRequest{
		Package: queryPackage{
			Name:      pkg,
			Ecosystem: ecosystem,
		},
		Version: version,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/query", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "yagura/0.3 (+https://github.com/shizukutanaka/yagura)")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// OSV.dev は 404 を「該当なし」ではなく 200 + {} で返すので、
		// ここに来る非 200 は本物のエラー。
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("osv: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseLen))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var qr queryResponse
	if err := json.Unmarshal(raw, &qr); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	out := make([]Vuln, 0, len(qr.Vulns))
	for _, v := range qr.Vulns {
		out = append(out, normalizeVuln(v))
	}
	sortVulns(out)
	return out, nil
}

// LanguageToEcosystem は project の Language フィールド (Go の自由形式) を
// OSV.dev の ecosystem 値にマップする。未対応言語は "" を返す。
//
// 大文字小文字は無視する。
func LanguageToEcosystem(lang string) string {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "go", "golang":
		return "Go"
	case "python", "py":
		return "PyPI"
	case "javascript", "js", "typescript", "ts", "node", "node.js":
		return "npm"
	case "rust", "rs":
		return "crates.io"
	case "ruby", "rb":
		return "RubyGems"
	case "java", "kotlin", "scala":
		return "Maven"
	case "c#", "csharp", ".net", "dotnet":
		return "NuGet"
	case "php":
		return "Packagist"
	}
	return ""
}

// ─── internal types (OSV.dev wire format) ────────────────────

type queryRequest struct {
	Package queryPackage `json:"package"`
	Version string       `json:"version,omitempty"`
}

type queryPackage struct {
	Name      string `json:"name"`
	Ecosystem string `json:"ecosystem"`
}

type queryResponse struct {
	Vulns []rawVuln `json:"vulns"`
}

type rawVuln struct {
	ID               string         `json:"id"`
	Aliases          []string       `json:"aliases"`
	Summary          string         `json:"summary"`
	Details          string         `json:"details"`
	Published        time.Time      `json:"published"`
	Modified         time.Time      `json:"modified"`
	Severity         []rawSeverity  `json:"severity"`
	References       []rawReference `json:"references"`
	DatabaseSpecific map[string]any `json:"database_specific"`
}

type rawSeverity struct {
	Type  string `json:"type"`  // 例: "CVSS_V3"
	Score string `json:"score"` // 例: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H/E:H/RL:O/RC:C"
}

type rawReference struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// ─── normalization ───────────────────────────────────────────

// normalizeVuln は wire format を Yagura 安定スキーマに変換する。
func normalizeVuln(v rawVuln) Vuln {
	score := extractCVSSScore(v)
	refs := make([]string, 0, len(v.References))
	for _, r := range v.References {
		if r.URL != "" {
			refs = append(refs, r.URL)
		}
	}
	return Vuln{
		ID:         v.ID,
		Summary:    truncate(v.Summary, 500),
		Severity:   severityFromScore(score, v),
		CVSSScore:  score,
		Published:  v.Published,
		Modified:   v.Modified,
		References: refs,
		Aliases:    v.Aliases,
	}
}

// extractCVSSScore は severity 配列から CVSS v3/v4 score を抽出する。
// 取得できない場合は 0 を返す(0 は CVSS では本来「none」を意味する有効値)。
func extractCVSSScore(v rawVuln) float64 {
	for _, s := range v.Severity {
		if !strings.HasPrefix(s.Type, "CVSS_") {
			continue
		}
		score := parseCVSSBaseScore(s.Score)
		if score > 0 {
			return score
		}
	}
	// database_specific.cvss.score の fallback パス
	if v.DatabaseSpecific != nil {
		if cvss, ok := v.DatabaseSpecific["cvss"].(map[string]any); ok {
			if score, ok := cvss["score"].(float64); ok {
				return score
			}
		}
		// GHSA の場合は severity が string で入る("HIGH", "MEDIUM" etc)
		if sev, ok := v.DatabaseSpecific["severity"].(string); ok {
			return defaultScoreForCategoricalSeverity(sev)
		}
	}
	return 0
}

// parseCVSSBaseScore は CVSS vector string から base score を計算しないが、
// もし score の最後に "/B:N.N" のような形式で base score が含まれていれば抽出する。
// CVSS vector の full parser ではない(それは外部依存が必要なため割愛)。
//
// 一般には OSV.dev は vector しか返さないため、ここでは "best effort" として
// vector の文字パターンから推定する。詳細な score 計算は database_specific 経由か
// 呼出側の責任とする。
func parseCVSSBaseScore(vector string) float64 {
	// 多くの OSV 提供データには base score が直接含まれないため、
	// vector から impact / exploitability の概算を行う。
	// 簡素化: AV:N + C:H + I:H + A:H があれば 9.8 とみなす等のヒューリスティック。
	// より正確には CVSS calculator が必要だが、Yagura ではカテゴリ判定で十分。
	v := strings.ToUpper(vector)

	// 影響(Confidentiality/Integrity/Availability)の最大値
	cHigh := strings.Contains(v, "C:H")
	iHigh := strings.Contains(v, "I:H")
	aHigh := strings.Contains(v, "A:H")
	cLow := strings.Contains(v, "C:L")
	iLow := strings.Contains(v, "I:L")
	aLow := strings.Contains(v, "A:L")

	// 攻撃ベクトル
	avNet := strings.Contains(v, "AV:N")
	avAdj := strings.Contains(v, "AV:A")

	// 認証要件
	prNone := strings.Contains(v, "PR:N")

	switch {
	case avNet && prNone && cHigh && iHigh && aHigh:
		return 9.8 // 典型的な RCE-class
	case avNet && (cHigh || iHigh || aHigh):
		return 7.5 // network exploit + high impact
	case (avNet || avAdj) && (cHigh || iHigh || aHigh):
		return 6.5
	case cHigh || iHigh || aHigh:
		return 5.5
	case cLow || iLow || aLow:
		return 3.5
	}
	return 0
}

// defaultScoreForCategoricalSeverity はカテゴリラベル ("CRITICAL", "HIGH" 等)を
// CVSS の中央値スコアにマップする(database_specific.severity 経由)。
func defaultScoreForCategoricalSeverity(s string) float64 {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "CRITICAL":
		return 9.5
	case "HIGH":
		return 8.0
	case "MEDIUM", "MODERATE":
		return 5.5
	case "LOW":
		return 2.5
	}
	return 0
}

// severityFromScore は CVSS score を OSV scheme の severity に変換する。
// CVSS v3.1 仕様 (https://www.first.org/cvss/v3.1/specification-document):
//
//	9.0-10.0: CRITICAL
//	7.0-8.9:  HIGH
//	4.0-6.9:  MEDIUM
//	0.1-3.9:  LOW
//	0.0:      UNKNOWN(計測不能 or 未提供)
func severityFromScore(score float64, raw rawVuln) Severity {
	if score == 0 {
		// database_specific.severity の文字列をそのまま採用する fallback
		if raw.DatabaseSpecific != nil {
			if sev, ok := raw.DatabaseSpecific["severity"].(string); ok {
				switch strings.ToUpper(strings.TrimSpace(sev)) {
				case "CRITICAL":
					return SeverityCritical
				case "HIGH":
					return SeverityHigh
				case "MEDIUM", "MODERATE":
					return SeverityMedium
				case "LOW":
					return SeverityLow
				}
			}
		}
		return SeverityUnknown
	}
	switch {
	case score >= 9.0:
		return SeverityCritical
	case score >= 7.0:
		return SeverityHigh
	case score >= 4.0:
		return SeverityMedium
	case score > 0:
		return SeverityLow
	}
	return SeverityUnknown
}

// ─── sorting / formatting ────────────────────────────────────

// SeverityRank は severity を数値化する(降順ソート用)。CRITICAL=4, HIGH=3, ...
func SeverityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	}
	return 0
}

// sortVulns は severity 降順 → published 降順で並び替える。
func sortVulns(vs []Vuln) {
	sort.SliceStable(vs, func(i, j int) bool {
		ri, rj := SeverityRank(vs[i].Severity), SeverityRank(vs[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return vs[i].Published.After(vs[j].Published)
	})
}

// truncate は文字列を rune 単位で切詰める。
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
