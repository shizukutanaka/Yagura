package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/shizukutanaka/yagura/internal/dashboard"
)

// ─── withSecurityHeaders middleware (v0.104.0) ────────────────────

// wantSecurityHeaders は全レスポンスに要求される市販グレードのヘッダ集合。
var wantSecurityHeaders = map[string]string{
	"X-Content-Type-Options": "nosniff",
	"X-Frame-Options":        "DENY",
	"Referrer-Policy":        "no-referrer",
}

func TestSecurityHeaders_SetOnEveryResponse(t *testing.T) {
	h := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

	for k, want := range wantSecurityHeaders {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy header missing")
	}
	for _, directive := range []string{"default-src 'none'", "frame-ancestors 'none'", "connect-src 'self'", "form-action 'self'"} {
		if !strings.Contains(csp, directive) {
			t.Errorf("CSP missing directive %q, got %q", directive, csp)
		}
	}
}

func TestSecurityHeaders_DoNotClobberHandlerHeaders(t *testing.T) {
	h := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTeapot)
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			panic(err)
		}
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))

	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("handler Content-Type clobbered: %q", got)
	}
	if rec.Code != http.StatusTeapot {
		t.Errorf("status not passed through: %d", rec.Code)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Errorf("body not passed through: %q", rec.Body.String())
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("security header missing on JSON response: %q", got)
	}
}

// ─── CSP nonce (v0.106.0) ──────────────────────────────────────────

var nonceDirectiveRe = regexp.MustCompile(`'nonce-([A-Za-z0-9_-]+)'`)

func TestSecurityHeaders_CSPUsesNonceNotUnsafeInline(t *testing.T) {
	h := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

	csp := rec.Header().Get("Content-Security-Policy")
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP still uses 'unsafe-inline', want a per-request nonce: %q", csp)
	}
	matches := nonceDirectiveRe.FindAllStringSubmatch(csp, -1)
	if len(matches) < 2 {
		t.Fatalf("expected a 'nonce-<value>' directive on both style-src and script-src, got %q", csp)
	}
	if matches[0][1] == "" {
		t.Errorf("nonce value is empty: %q", csp)
	}
}

func TestSecurityHeaders_NoncesAreUniquePerRequest(t *testing.T) {
	h := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

	n1 := nonceDirectiveRe.FindStringSubmatch(rec1.Header().Get("Content-Security-Policy"))
	n2 := nonceDirectiveRe.FindStringSubmatch(rec2.Header().Get("Content-Security-Policy"))
	if len(n1) < 2 || len(n2) < 2 {
		t.Fatalf("expected nonce in both responses' CSP")
	}
	if n1[1] == n2[1] {
		t.Errorf("expected different nonces per request, both were %q", n1[1])
	}
}

func TestSecurityHeaders_InjectsNonceIntoRequestContext(t *testing.T) {
	var seenInContext, seenInHeader string
	h := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenInContext = dashboard.NonceFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

	if m := nonceDirectiveRe.FindStringSubmatch(rec.Header().Get("Content-Security-Policy")); len(m) >= 2 {
		seenInHeader = m[1]
	}
	if seenInContext == "" {
		t.Fatal("handler did not see a nonce in its request context")
	}
	if seenInContext != seenInHeader {
		t.Errorf("context nonce %q does not match CSP header nonce %q", seenInContext, seenInHeader)
	}
}
