package dashboard

import "context"

// ctxKeyNonce is the unexported context key carrying the per-request CSP
// nonce (v0.106.0). cmd/yagura's withSecurityHeaders middleware generates
// a fresh crypto/rand nonce per request, sets it as the CSP header's
// 'nonce-<value>' directive, and injects it into the request context via
// WithNonce so the dashboard's inline <style>/<script> blocks can render
// the matching nonce attribute. Without this, the CSP would need
// 'unsafe-inline' (the pre-v0.106.0 baseline), which defeats the point of
// having a CSP against injected inline scripts.
type ctxKeyNonce struct{}

// WithNonce returns a context carrying nonce for later retrieval via
// NonceFromContext.
func WithNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, ctxKeyNonce{}, nonce)
}

// NonceFromContext returns the CSP nonce set by WithNonce, or "" if none
// was set (e.g. a handler invoked directly in a test, or a build where the
// nonce middleware isn't wired — templates simply render without a nonce
// attribute value in that case, which is harmless for tests but would fail
// CSP enforcement in a real browser if the middleware were ever skipped).
func NonceFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyNonce{}).(string)
	return v
}
