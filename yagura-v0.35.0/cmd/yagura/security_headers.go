package main

import "net/http"

// securityCSP is the Content-Security-Policy applied to every response.
// The dashboard renders a small number of inline <style>/<script> blocks
// and no inline event handlers (verified), so 'unsafe-inline' for
// style-src/script-src is the pragmatic baseline here; a future pass can
// move to nonces. connect-src 'self' covers the dashboard's SSE stream;
// manifest-src 'self' covers the PWA manifest; form-action 'self' covers
// the register-project form.
const securityCSP = "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; manifest-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// withSecurityHeaders is a commercial-grade baseline applied uniformly to
// every response on the daemon's HTTP surface (dashboard HTML, JSON API,
// MCP, metrics, health) — a single seam so the frontend (dashboard) and
// backend (API/MCP) never drift out of sync on security posture. HSTS is
// deliberately omitted: this daemon is loopback-default (ADR-0004) and
// typically has no TLS termination in front of it, so pinning HTTPS would
// break the common case.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", securityCSP)
		next.ServeHTTP(w, r)
	})
}
