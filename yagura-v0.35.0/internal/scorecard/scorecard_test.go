package scorecard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── normalizeRepo ───────────────────────────────────────────

func TestNormalizeRepo(t *testing.T) {
	tests := map[string]string{
		"owner/repo":                          "github.com/owner/repo",
		"github.com/owner/repo":               "github.com/owner/repo",
		"https://github.com/owner/repo":       "github.com/owner/repo",
		"http://github.com/owner/repo":        "github.com/owner/repo",
		"  github.com/shizukutanaka/yagura  ": "github.com/shizukutanaka/yagura",
		"github.com/owner/repo/":              "github.com/owner/repo",
		"owner/repo/extra":                    "github.com/owner/repo", // 余分は無視
		"":                                    "",
		"justonepart":                         "",
		"/":                                   "",
	}
	for in, want := range tests {
		if got := normalizeRepo(in); got != want {
			t.Errorf("normalizeRepo(%q): got %q want %q", in, got, want)
		}
	}
}

// ─── HealthCategory ──────────────────────────────────────────

func TestHealthCategory(t *testing.T) {
	tests := []struct {
		score float64
		want  string
	}{
		{10.0, "excellent"},
		{8.0, "excellent"},
		{7.9, "good"},
		{6.0, "good"},
		{5.9, "fair"},
		{4.0, "fair"},
		{3.9, "poor"},
		{0.0, "poor"},
		{-1.0, "unknown"},
	}
	for _, tt := range tests {
		if got := HealthCategory(tt.score); got != tt.want {
			t.Errorf("score=%g: got %q want %q", tt.score, got, tt.want)
		}
	}
}

// ─── PriorityChecks ──────────────────────────────────────────

func TestPriorityChecks(t *testing.T) {
	pc := PriorityChecks()
	if len(pc) != 7 {
		t.Errorf("expected 7 priority checks, got %d", len(pc))
	}
	for _, c := range pc {
		if c == "" {
			t.Error("empty check name in list")
		}
	}
}

// ─── Client.Fetch (with httptest) ────────────────────────────

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetch_HappyPath(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/projects/github.com/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"date": "2026-01-15",
			"repo": map[string]string{
				"name":   "github.com/owner/repo",
				"commit": "abc123def456",
			},
			"scorecard": map[string]string{"version": "v5.0.0"},
			"score":     7.8,
			"checks": []map[string]any{
				{
					"name":   "Branch-Protection",
					"score":  10,
					"reason": "branch protection enabled",
					"documentation": map[string]string{
						"url": "https://github.com/ossf/scorecard/blob/main/docs/checks.md#branch-protection",
					},
				},
				{
					"name":   "Code-Review",
					"score":  -1,
					"reason": "no recent merges",
				},
			},
		})
	})

	c := New(WithBaseURL(srv.URL))
	score, err := c.Fetch(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if score.Score != 7.8 {
		t.Errorf("score: got %g", score.Score)
	}
	if score.Repo != "github.com/owner/repo" {
		t.Errorf("repo: got %s", score.Repo)
	}
	if score.Commit != "abc123def456" {
		t.Errorf("commit: got %s", score.Commit)
	}
	if score.ScorecardVersion != "v5.0.0" {
		t.Errorf("version: got %s", score.ScorecardVersion)
	}
	want := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if !score.Date.Equal(want) {
		t.Errorf("date: got %v want %v", score.Date, want)
	}
	if len(score.Checks) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(score.Checks))
	}
	// Sorted by name
	if score.Checks[0].Name != "Branch-Protection" {
		t.Errorf("first check should be Branch-Protection: got %s", score.Checks[0].Name)
	}
	if score.Checks[0].Documentation == "" {
		t.Error("documentation URL not extracted")
	}
}

func TestFetch_NotScored(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	c := New(WithBaseURL(srv.URL))
	_, err := c.Fetch(context.Background(), "owner/unscored")
	if err == nil {
		t.Fatal("expected error")
	}
	if err != ErrNotScored {
		t.Errorf("expected ErrNotScored, got %v", err)
	}
}

func TestFetch_HTTPError(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("backend down"))
	})
	c := New(WithBaseURL(srv.URL))
	_, err := c.Fetch(context.Background(), "owner/repo")
	if err == nil {
		t.Error("500 should error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error mentions status: %v", err)
	}
}

func TestFetch_MalformedJSON(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	c := New(WithBaseURL(srv.URL))
	_, err := c.Fetch(context.Background(), "owner/repo")
	if err == nil {
		t.Error("malformed should error")
	}
}

func TestFetch_EmptyRepo(t *testing.T) {
	c := New()
	if _, err := c.Fetch(context.Background(), ""); err == nil {
		t.Error("empty repo should error")
	}
	if _, err := c.Fetch(context.Background(), "garbage"); err == nil {
		t.Error("invalid repo should error")
	}
}

func TestFetch_ContextCanceled(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"score": 5.0})
	})
	c := New(WithBaseURL(srv.URL))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := c.Fetch(ctx, "owner/repo")
	if err == nil {
		t.Error("canceled context should error")
	}
}

func TestFetch_AcceptsFullURL(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"date":  "2026-01-01",
			"repo":  map[string]string{"name": "github.com/x/y"},
			"score": 5.0,
		})
	})
	c := New(WithBaseURL(srv.URL))
	for _, repo := range []string{"x/y", "github.com/x/y", "https://github.com/x/y"} {
		_, err := c.Fetch(context.Background(), repo)
		if err != nil {
			t.Errorf("repo=%q: %v", repo, err)
		}
	}
}

func TestWithHTTPClient_AppliesOption(t *testing.T) {
	custom := &http.Client{Timeout: 5 * time.Second}
	c := New(WithHTTPClient(custom))
	if c.httpClient != custom {
		t.Error("WithHTTPClient option should set the http client")
	}
}
