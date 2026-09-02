package osv

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── LanguageToEcosystem ─────────────────────────────────────

func TestLanguageToEcosystem(t *testing.T) {
	tests := map[string]string{
		"Go":         "Go",
		"go":         "Go",
		"GOLANG":     "Go",
		" python ":   "PyPI",
		"py":         "PyPI",
		"JavaScript": "npm",
		"typescript": "npm",
		"node.js":    "npm",
		"Rust":       "crates.io",
		"rust":       "crates.io",
		"ruby":       "RubyGems",
		"java":       "Maven",
		"Kotlin":     "Maven",
		"C#":         "NuGet",
		"dotnet":     "NuGet",
		"PHP":        "Packagist",
		"":           "",
		"klingon":    "",
		"Cobol":      "",
	}
	for in, want := range tests {
		if got := LanguageToEcosystem(in); got != want {
			t.Errorf("LanguageToEcosystem(%q): got %q want %q", in, got, want)
		}
	}
}

// ─── SeverityRank ────────────────────────────────────────────

func TestSeverityRank(t *testing.T) {
	tests := map[Severity]int{
		SeverityCritical:    4,
		SeverityHigh:        3,
		SeverityMedium:      2,
		SeverityLow:         1,
		SeverityUnknown:     0,
		Severity("garbage"): 0,
	}
	for in, want := range tests {
		if got := SeverityRank(in); got != want {
			t.Errorf("SeverityRank(%q): got %d want %d", in, got, want)
		}
	}
}

// ─── severityFromScore ───────────────────────────────────────

func TestSeverityFromScore(t *testing.T) {
	tests := []struct {
		score float64
		want  Severity
	}{
		{10.0, SeverityCritical},
		{9.0, SeverityCritical},
		{8.9, SeverityHigh},
		{7.0, SeverityHigh},
		{6.9, SeverityMedium},
		{4.0, SeverityMedium},
		{3.9, SeverityLow},
		{0.1, SeverityLow},
		{0.0, SeverityUnknown},
	}
	for _, tt := range tests {
		got := severityFromScore(tt.score, rawVuln{})
		if got != tt.want {
			t.Errorf("score=%g: got %q want %q", tt.score, got, tt.want)
		}
	}
}

// ─── parseCVSSBaseScore ──────────────────────────────────────

func TestParseCVSSBaseScore(t *testing.T) {
	tests := map[string]float64{
		"CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H": 9.8,
		"CVSS:3.1/AV:N/AC:L/PR:L/UI:N/S:U/C:H/I:N/A:N": 7.5,
		"CVSS:3.1/AV:A/AC:H/PR:L/UI:N/S:U/C:L/I:N/A:N": 6.5,
		"CVSS:3.1/AV:L/AC:L/PR:L/UI:R/S:U/C:H/I:N/A:N": 5.5,
		"CVSS:3.1/AV:L/AC:L/PR:L/UI:R/S:U/C:L/I:N/A:N": 3.5,
		"":        0,
		"garbage": 0,
	}
	for in, want := range tests {
		if got := parseCVSSBaseScore(in); got != want {
			t.Errorf("parseCVSSBaseScore(%q): got %g want %g", in, got, want)
		}
	}
}

// ─── truncate ────────────────────────────────────────────────

func TestTruncate(t *testing.T) {
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("short input: got %q", got)
	}
	if got := truncate("abcdef", 3); got != "abc…" {
		t.Errorf("long input: got %q", got)
	}
	if got := truncate("abc", 0); got != "" {
		t.Errorf("zero n: got %q", got)
	}
	// Multi-byte safety
	if got := truncate("日本語テスト", 2); got != "日本…" {
		t.Errorf("multibyte: got %q", got)
	}
}

// ─── Client.Query (with httptest) ────────────────────────────

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_Query_HappyPath(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/query" {
			t.Errorf("expected /v1/query, got %s", r.URL.Path)
		}
		var req queryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.Package.Ecosystem != "Go" {
			t.Errorf("expected ecosystem=Go, got %s", req.Package.Ecosystem)
		}
		resp := map[string]any{
			"vulns": []map[string]any{
				{
					"id":        "GO-2024-1234",
					"summary":   "Buffer overflow in foo.Bar",
					"published": "2024-01-15T00:00:00Z",
					"modified":  "2024-01-20T00:00:00Z",
					"severity": []map[string]any{
						{
							"type":  "CVSS_V3",
							"score": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
						},
					},
					"references": []map[string]any{
						{"type": "WEB", "url": "https://example.com/advisory"},
					},
					"aliases": []string{"CVE-2024-99999"},
				},
				{
					"id":      "GHSA-low-low-low",
					"summary": "Minor info leak",
					"database_specific": map[string]any{
						"severity": "LOW",
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	c := New(WithBaseURL(srv.URL))
	vulns, err := c.Query(context.Background(), "Go", "github.com/example/foo", "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(vulns) != 2 {
		t.Fatalf("expected 2 vulns, got %d", len(vulns))
	}
	// Order: CRITICAL/HIGH first
	if vulns[0].Severity != SeverityCritical {
		t.Errorf("first vuln should be CRITICAL: got %s", vulns[0].Severity)
	}
	if vulns[0].ID != "GO-2024-1234" {
		t.Errorf("ID: got %s", vulns[0].ID)
	}
	if vulns[0].CVSSScore != 9.8 {
		t.Errorf("CVSS score: got %g", vulns[0].CVSSScore)
	}
	if len(vulns[0].References) != 1 {
		t.Errorf("refs count: got %d", len(vulns[0].References))
	}
	if vulns[0].Aliases[0] != "CVE-2024-99999" {
		t.Errorf("alias: got %v", vulns[0].Aliases)
	}
	if vulns[1].Severity != SeverityLow {
		t.Errorf("second vuln should be LOW: got %s", vulns[1].Severity)
	}
}

func TestClient_Query_NoVulns(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"vulns": []any{}})
	})
	c := New(WithBaseURL(srv.URL))
	vulns, err := c.Query(context.Background(), "Go", "github.com/x/y", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(vulns) != 0 {
		t.Errorf("expected 0 vulns, got %d", len(vulns))
	}
}

func TestClient_Query_EmptyResponse(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{}"))
	})
	c := New(WithBaseURL(srv.URL))
	vulns, err := c.Query(context.Background(), "Go", "x", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(vulns) != 0 {
		t.Errorf("empty {} should produce 0 vulns, got %d", len(vulns))
	}
}

func TestClient_Query_HTTPError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error"))
	})
	c := New(WithBaseURL(srv.URL))
	_, err := c.Query(context.Background(), "Go", "x", "1")
	if err == nil {
		t.Error("500 should produce error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500: %v", err)
	}
}

func TestClient_Query_ValidationErrors(t *testing.T) {
	c := New()
	if _, err := c.Query(context.Background(), "", "pkg", "1"); err == nil {
		t.Error("empty ecosystem should error")
	}
	if _, err := c.Query(context.Background(), "Go", "", "1"); err == nil {
		t.Error("empty package should error")
	}
}

func TestClient_Query_MalformedResponse(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json at all"))
	})
	c := New(WithBaseURL(srv.URL))
	_, err := c.Query(context.Background(), "Go", "x", "1")
	if err == nil {
		t.Error("malformed response should error")
	}
}

func TestClient_Query_ContextCanceled(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("{}"))
	})
	c := New(WithBaseURL(srv.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := c.Query(ctx, "Go", "x", "1")
	if err == nil {
		t.Error("canceled context should produce error")
	}
}

// ─── normalizeVuln edge cases ────────────────────────────────

func TestNormalizeVuln_TruncatesLongSummary(t *testing.T) {
	long := strings.Repeat("x", 1000)
	v := normalizeVuln(rawVuln{ID: "X", Summary: long})
	if len([]rune(v.Summary)) > 501 { // 500 chars + ellipsis
		t.Errorf("summary not truncated: %d runes", len([]rune(v.Summary)))
	}
}

func TestNormalizeVuln_GHSAStyle(t *testing.T) {
	// GHSA は CVSS の代わりに database_specific.severity を持つことが多い
	v := normalizeVuln(rawVuln{
		ID: "GHSA-abc",
		DatabaseSpecific: map[string]any{
			"severity": "MODERATE",
		},
	})
	if v.Severity != SeverityMedium {
		t.Errorf("MODERATE should map to MEDIUM: got %s", v.Severity)
	}
}

func TestNormalizeVuln_CategoricalCritical(t *testing.T) {
	v := normalizeVuln(rawVuln{
		ID: "GHSA-x",
		DatabaseSpecific: map[string]any{
			"severity": "CRITICAL",
		},
	})
	if v.Severity != SeverityCritical {
		t.Errorf("CRITICAL: got %s", v.Severity)
	}
}

func TestNormalizeVuln_SortByPublished(t *testing.T) {
	// 同じ severity なら新しいものが先
	vulns := []Vuln{
		{ID: "old", Severity: SeverityHigh, Published: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: "new", Severity: SeverityHigh, Published: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	sortVulns(vulns)
	if vulns[0].ID != "new" {
		t.Errorf("newest should be first: got %v", vulns[0].ID)
	}
}

// ─── WithHTTPClient ──────────────────────────────────────────

func TestWithHTTPClient(t *testing.T) {
	custom := &http.Client{Timeout: 5 * time.Second}
	c := New(WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Error("WithHTTPClient should replace the default http.Client")
	}
}

// ─── defaultScoreForCategoricalSeverity ──────────────────────

func TestDefaultScoreForCategoricalSeverity_AllCases(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"CRITICAL", 9.5},
		{"HIGH", 8.0},
		{"MEDIUM", 5.5},
		{"MODERATE", 5.5},
		{"LOW", 2.5},
		{"unknown-label", 0},
		{"", 0},
	}
	for _, tc := range cases {
		got := defaultScoreForCategoricalSeverity(tc.in)
		if got != tc.want {
			t.Errorf("defaultScoreForCategoricalSeverity(%q) = %g, want %g", tc.in, got, tc.want)
		}
	}
}

// ─── severityFromScore DatabaseSpecific fallback ─────────────

func TestSeverityFromScore_DatabaseSpecificFallback(t *testing.T) {
	cases := []struct {
		sev  string
		want Severity
	}{
		{"HIGH", SeverityHigh},
		{"MEDIUM", SeverityMedium},
		{"MODERATE", SeverityMedium},
		{"LOW", SeverityLow},
		{"unknown-junk", SeverityUnknown},
	}
	for _, tc := range cases {
		raw := rawVuln{
			DatabaseSpecific: map[string]any{"severity": tc.sev},
		}
		got := severityFromScore(0, raw)
		if got != tc.want {
			t.Errorf("severityFromScore(0, {severity:%q}) = %q, want %q", tc.sev, got, tc.want)
		}
	}
}

// TestExtractCVSSScore_NonCVSSTypeSkipped covers the continue for non-CVSS severity types.
func TestExtractCVSSScore_NonCVSSTypeSkipped(t *testing.T) {
	// "RHSA" is not a CVSS type → the loop skips it.
	v := rawVuln{
		Severity: []rawSeverity{{Type: "RHSA", Score: "7.0"}},
	}
	if got := extractCVSSScore(v); got != 0 {
		t.Errorf("non-CVSS severity type should yield 0, got %g", got)
	}
}

// TestExtractCVSSScore_DatabaseSpecificCvssScore covers the cvss.score float64 branch.
func TestExtractCVSSScore_DatabaseSpecificCvssScore(t *testing.T) {
	v := rawVuln{
		DatabaseSpecific: map[string]any{
			"cvss": map[string]any{
				"score": float64(8.5),
			},
		},
	}
	if got := extractCVSSScore(v); got != 8.5 {
		t.Errorf("cvss.score branch: got %g, want 8.5", got)
	}
}
