package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
