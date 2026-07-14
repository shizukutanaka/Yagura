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

// ─── Origin validation / DNS-rebinding mitigation (v0.112.0) ──────

func TestOriginAllowed_NoHeaderIsAllowed(t *testing.T) {
	// non-browser MCP clients (CLI, SDKs, curl) never send Origin at all.
	if !originAllowed("") {
		t.Error("expected absent Origin to be allowed")
	}
}

func TestOriginAllowed_LoopbackOriginsAllowed(t *testing.T) {
	for _, origin := range []string{
		"http://localhost:8090",
		"http://127.0.0.1:8090",
		"https://127.0.0.1",
		"http://[::1]:8090",
		"http://localhost",
	} {
		if !originAllowed(origin) {
			t.Errorf("originAllowed(%q) = false, want true", origin)
		}
	}
}

func TestOriginAllowed_ForeignOriginRejected(t *testing.T) {
	for _, origin := range []string{
		"https://evil.example",
		"http://attacker.test:8090",
		"https://127.0.0.1.evil.example", // lookalike host, not an actual loopback address
	} {
		if originAllowed(origin) {
			t.Errorf("originAllowed(%q) = true, want false", origin)
		}
	}
}

func TestOriginAllowed_NullOriginRejected(t *testing.T) {
	// "null" is the Origin serialization of a sandboxed iframe / data: URL —
	// an attacker-controlled context, must not be treated as "absent".
	if originAllowed("null") {
		t.Error("expected \"null\" Origin to be rejected")
	}
}

func TestRestrictOrigin_AllowsNoOriginHeader(t *testing.T) {
	h := restrictOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with no Origin header, got %d", rec.Code)
	}
}

func TestRestrictOrigin_AllowsLoopbackOrigin(t *testing.T) {
	h := restrictOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8090")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with loopback Origin, got %d", rec.Code)
	}
}

func TestRestrictOrigin_RejectsCrossOriginBrowserRequest(t *testing.T) {
	h := restrictOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for foreign Origin, got %d (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestRestrictOrigin_ComposesWithSecurityHeaders(t *testing.T) {
	// restrictOrigin must sit inside withSecurityHeaders so a rejected
	// (403) response still carries the same security header baseline as
	// every other response — headers and origin-gating must never drift.
	h := withSecurityHeaders(restrictOrigin(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("security headers missing on rejected-origin response: %q", got)
	}
}
