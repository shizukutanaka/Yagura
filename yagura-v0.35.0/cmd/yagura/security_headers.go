package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/shizukutanaka/yagura/internal/dashboard"
)

// securityCSPTemplate is the Content-Security-Policy applied to every
// response, with %s placeholders for the per-request nonce (v0.106.0 —
// replaces the earlier 'unsafe-inline' baseline). The dashboard's inline
// <style>/<script> blocks carry a matching nonce attribute
// (internal/dashboard's WithNonce/NonceFromContext); no inline event
// handlers exist (verified), so nonce-gated style-src/script-src covers
// every inline block without weakening the policy to 'unsafe-inline'.
// connect-src 'self' covers the dashboard's SSE stream; manifest-src
// 'self' covers the PWA manifest; form-action 'self' covers the
// register-project form.
const securityCSPTemplate = "default-src 'none'; style-src 'nonce-%[1]s'; script-src 'nonce-%[1]s'; " +
	"img-src 'self' data:; connect-src 'self'; manifest-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// generateNonce returns a fresh base64-encoded 16-byte CSP nonce.
// crypto/rand.Read's error is ignored per this repo's existing convention
// (internal/sbom.randomUUIDv4) — on all supported platforms it does not
// fail in practice.
func generateNonce() string {
	var b [16]byte
	rand.Read(b[:]) //nolint:errcheck
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// withSecurityHeaders is a commercial-grade baseline applied uniformly to
// every response on the daemon's HTTP surface (dashboard HTML, JSON API,
// MCP, metrics, health) — a single seam so the frontend (dashboard) and
// backend (API/MCP) never drift out of sync on security posture. HSTS is
// deliberately omitted: this daemon is loopback-default (ADR-0004) and
// typically has no TLS termination in front of it, so pinning HTTPS would
// break the common case. Generates a fresh nonce per request and injects
// it into the request context (dashboard.WithNonce) so the dashboard's
// templates can render the matching attribute.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := generateNonce()
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", fmt.Sprintf(securityCSPTemplate, nonce))
		next.ServeHTTP(w, r.WithContext(dashboard.WithNonce(r.Context(), nonce)))
	})
}

// originAllowed reports whether a request's Origin header is safe to accept.
//
// A same-machine, non-browser MCP client (CLI, SDK, curl) never sends the
// Origin header at all, so an absent Origin is the normal, expected case and
// is allowed. A *present* Origin is only accepted if its host is loopback
// (localhost/127.0.0.1/::1) — anything else means a web page running in the
// operator's own browser is driving the request: the classic DNS-rebinding /
// browser-to-localhost attack against this daemon's no-token loopback-trust
// mode (ADR-0004), which was never accounted for. "null" (the Origin
// serialization of a sandboxed iframe or data: URL) is treated as untrusted,
// not as absent — url.Parse("null") yields an empty Hostname(), which
// naturally falls through to the reject path below.
func originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// restrictOrigin rejects browser-originated cross-origin requests before
// they reach the mux, mitigating DNS rebinding against the no-token
// loopback-trust mode (ADR-0004; docs/security-spec.md T13). It is a
// transport-level gate, not a credential check, so it applies uniformly
// regardless of whether YAGURA_MCP_TOKEN is configured. Compose it inside
// withSecurityHeaders (not outside) so a rejected request's response still
// carries the same security header baseline as every other response.
func restrictOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !originAllowed(r.Header.Get("Origin")) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireBearerToken guards h with the same Bearer-token check already used
// by /mcp (internal/mcp/server.go) and the HTTP API (httpapi.go's
// authMiddleware) — /hooks/claude-code and /hooks/agent previously had no
// auth check at all despite hookreceiver's own package doc claiming one
// (v0.105.0 fix). An empty token means auth is not enforced, consistent
// with the existing loopback-default convention (ADR-0004).
func requireBearerToken(token string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			h(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		// HasPrefix が偽でも常に ConstantTimeCompare まで走らせ、タイミング攻撃を防ぐ
		// (/mcp・HTTP API と同じ規約)。
		received := strings.TrimPrefix(auth, prefix)
		if !strings.HasPrefix(auth, prefix) ||
			subtle.ConstantTimeCompare([]byte(received), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}
