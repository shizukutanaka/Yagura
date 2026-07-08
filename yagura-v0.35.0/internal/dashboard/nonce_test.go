package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ─── CSP nonce context plumbing (v0.106.0) ─────────────────────────

func TestWithNonce_RoundTrips(t *testing.T) {
	ctx := WithNonce(context.Background(), "abc123")
	if got := NonceFromContext(ctx); got != "abc123" {
		t.Errorf("got %q, want %q", got, "abc123")
	}
}

func TestNonceFromContext_EmptyWhenAbsent(t *testing.T) {
	if got := NonceFromContext(context.Background()); got != "" {
		t.Errorf("expected empty string when no nonce set, got %q", got)
	}
}

// TestServeHTTP_RendersNonceOnStyleAndScript verifies the main dashboard
// template's inline <style>/<script> blocks carry the request's nonce, so
// the CSP's 'nonce-<value>' directive (set by cmd/yagura's
// withSecurityHeaders) actually matches what the browser executes.
func TestServeHTTP_RendersNonceOnStyleAndScript(t *testing.T) {
	h, _ := setupHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req = req.WithContext(WithNonce(req.Context(), "test-nonce-123"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	body := w.Body.String()
	if got := strings.Count(body, `nonce="test-nonce-123"`); got < 3 {
		t.Errorf("expected at least 3 nonce attributes (1 style + 2 script) on main dashboard template, found %d in body", got)
	}
	if strings.Contains(body, "<style>") || strings.Contains(body, "<script>") {
		t.Error("found a bare <style>/<script> tag without a nonce attribute")
	}
}

func TestServeAlertDetail_RendersNonceOnStyleAndScript(t *testing.T) {
	h, _ := setupHandler(t)
	// The script block only renders in the {{else}} branch (Found=true, at
	// least one alert) — an empty portfolio takes the {{if not .Found}}
	// branch, which has no script tag at all.
	h.SetPortfolioHealthProvider(fakeHealth{ok: true, alerts: []AlertItem{
		{ID: "a1", Project: "breeze", Source: "secretscan", Severity: "high", Title: "t", Recommendation: "r"},
	}})
	req := httptest.NewRequest(http.MethodGet, "/dashboard/alerts", nil)
	req = req.WithContext(WithNonce(req.Context(), "alert-nonce-456"))
	w := httptest.NewRecorder()
	h.serveAlertDetail(w, req)

	body := w.Body.String()
	if got := strings.Count(body, `nonce="alert-nonce-456"`); got < 2 {
		t.Errorf("expected at least 2 nonce attributes (1 style + 1 script) on alerts template, found %d in body", got)
	}
	if strings.Contains(body, "<style>") || strings.Contains(body, "<script>") {
		t.Error("found a bare <style>/<script> tag without a nonce attribute")
	}
}

func TestServeActivityDetail_RendersNonceOnStyle(t *testing.T) {
	h, _ := setupHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/dashboard/activity", nil)
	req = req.WithContext(WithNonce(req.Context(), "activity-nonce-789"))
	w := httptest.NewRecorder()
	h.serveActivityDetail(w, req)

	body := w.Body.String()
	if got := strings.Count(body, `nonce="activity-nonce-789"`); got < 1 {
		t.Errorf("expected at least 1 nonce attribute (style) on activity template, found %d in body", got)
	}
	if strings.Contains(body, "<style>") {
		t.Error("found a bare <style> tag without a nonce attribute")
	}
}
