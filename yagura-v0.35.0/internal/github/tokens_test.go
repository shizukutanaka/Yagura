package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ─── TokenStore unit tests ───────────────────────────────────

func TestNewTokenStore_DefaultFallback(t *testing.T) {
	s := NewTokenStore("fallback-token")
	if s.fallback != "fallback-token" {
		t.Errorf("fallback: %q", s.fallback)
	}
	if s.HasPerOwner() {
		t.Error("new store should have no per-owner tokens")
	}
}

func TestAddOwnerToken_Normalizes(t *testing.T) {
	s := NewTokenStore("")
	s.AddOwnerToken("ShizukuTanaka", "token-X")
	// case-insensitive lookup
	if got := s.TokenForOwner("shizukutanaka"); got != "token-X" {
		t.Errorf("lower case lookup: %q", got)
	}
	if got := s.TokenForOwner("SHIZUKUTANAKA"); got != "token-X" {
		t.Errorf("upper case lookup: %q", got)
	}
}

func TestAddOwnerToken_EmptyIgnored(t *testing.T) {
	s := NewTokenStore("default")
	s.AddOwnerToken("", "token")
	s.AddOwnerToken("owner", "")
	if s.HasPerOwner() {
		t.Error("empty owner/token should be ignored")
	}
}

func TestTokenForOwner_FallsBack(t *testing.T) {
	s := NewTokenStore("default-token")
	s.AddOwnerToken("known", "specific-token")
	if got := s.TokenForOwner("known"); got != "specific-token" {
		t.Errorf("known: %q", got)
	}
	if got := s.TokenForOwner("unknown"); got != "default-token" {
		t.Errorf("unknown should fall back: %q", got)
	}
}

func TestTokenForPath_ExtractsOwner(t *testing.T) {
	s := NewTokenStore("default")
	s.AddOwnerToken("shizukutanaka", "shizuku-token")

	cases := map[string]string{
		"/repos/shizukutanaka/yagura":        "shizuku-token",
		"/repos/Shizukutanaka/yagura/pulls":  "shizuku-token", // case-insensitive
		"/repos/other-user/repo":             "default",
		"/repos/other-user/repo/commits/abc": "default",
		"/user":                              "default", // not a repo path
		"/orgs/myorg":                        "default",
		"":                                   "default",
	}
	for path, want := range cases {
		if got := s.TokenForPath(path); got != want {
			t.Errorf("TokenForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestPerOwnerCount(t *testing.T) {
	s := NewTokenStore("default")
	if s.PerOwnerCount() != 0 {
		t.Errorf("initial count: %d", s.PerOwnerCount())
	}
	s.AddOwnerToken("a", "t1")
	s.AddOwnerToken("b", "t2")
	if s.PerOwnerCount() != 2 {
		t.Errorf("after 2 adds: %d", s.PerOwnerCount())
	}
}

func TestExtractOwnerFromPath(t *testing.T) {
	cases := map[string]string{
		"/repos/foo/bar":          "foo",
		"/repos/foo/bar/commits":  "foo",
		"/repos/foo/bar/git/refs": "foo",
		"/user":                   "",
		"/orgs/myorg":             "",
		"":                        "",
		"/repos/":                 "",
	}
	for path, want := range cases {
		if got := extractOwnerFromPath(path); got != want {
			t.Errorf("extractOwnerFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// ─── Integration: Client uses TokenStore per-owner ──────────

func TestClient_UsesOwnerSpecificToken(t *testing.T) {
	// Track which token was sent per path
	receivedTokens := struct {
		sync.Mutex
		m map[string]string
	}{m: map[string]string{}}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		auth = strings.TrimPrefix(auth, "Bearer ")
		receivedTokens.Lock()
		receivedTokens.m[r.URL.Path] = auth
		receivedTokens.Unlock()

		// Minimal Repository JSON
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"full_name":"x/y","default_branch":"main","pushed_at":"2026-01-01T00:00:00Z"}`)
	}))
	defer srv.Close()

	store := NewTokenStore("fallback-tok")
	store.AddOwnerToken("shizukutanaka", "shizuku-tok")
	store.AddOwnerToken("acme", "acme-tok")

	c := NewClient(Config{Tokens: store, BaseURL: srv.URL})
	ctx := context.Background()

	// shizukutanaka/* → shizuku-tok
	if _, err := c.GetRepository(ctx, "shizukutanaka", "yagura"); err != nil {
		t.Fatal(err)
	}
	// acme/* → acme-tok
	if _, err := c.GetRepository(ctx, "acme", "tool"); err != nil {
		t.Fatal(err)
	}
	// unknown owner → fallback
	if _, err := c.GetRepository(ctx, "stranger", "lib"); err != nil {
		t.Fatal(err)
	}

	receivedTokens.Lock()
	defer receivedTokens.Unlock()
	cases := map[string]string{
		"/repos/shizukutanaka/yagura": "shizuku-tok",
		"/repos/acme/tool":            "acme-tok",
		"/repos/stranger/lib":         "fallback-tok",
	}
	for path, want := range cases {
		got, ok := receivedTokens.m[path]
		if !ok {
			t.Errorf("path %q not received", path)
			continue
		}
		if got != want {
			t.Errorf("path %q used token %q, want %q", path, got, want)
		}
	}
}

func TestClient_BackwardCompat_SingleToken(t *testing.T) {
	// Tokens 未指定、Token 単独 → 全リクエストで Token を使う
	receivedToken := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedToken = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"full_name":"x/y","default_branch":"main","pushed_at":"2026-01-01T00:00:00Z"}`)
	}))
	defer srv.Close()

	c := NewClient(Config{Token: "legacy-token", BaseURL: srv.URL})
	if _, err := c.GetRepository(context.Background(), "any", "repo"); err != nil {
		t.Fatal(err)
	}
	if receivedToken != "legacy-token" {
		t.Errorf("backward compat broke: token=%q", receivedToken)
	}
}
