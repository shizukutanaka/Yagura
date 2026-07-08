package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/shizukutanaka/yagura/internal/hookreceiver"
)

// ─── requireBearerToken guarding /hooks/* (v0.105.0) ──────────────

// TestRequireBearerToken_EmptyTokenIsUnauthenticated preserves the
// established convention (mirrors /mcp and the HTTP API): an empty
// AuthToken means no auth is enforced (loopback-default, ADR-0004).
func TestRequireBearerToken_EmptyTokenIsUnauthenticated(t *testing.T) {
	called := false
	h := requireBearerToken("", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/hooks/claude-code", nil))
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("expected pass-through with empty token, called=%v code=%d", called, rec.Code)
	}
}

func TestRequireBearerToken_RejectsMissingHeader(t *testing.T) {
	h := requireBearerToken("secret", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called without a valid token")
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/hooks/claude-code", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestRequireBearerToken_RejectsWrongToken(t *testing.T) {
	h := requireBearerToken("secret", func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called with a wrong token")
	})
	req := httptest.NewRequest(http.MethodPost, "/hooks/claude-code", nil)
	req.Header.Set("Authorization", "Bearer nope")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestRequireBearerToken_AcceptsCorrectToken(t *testing.T) {
	called := false
	h := requireBearerToken("secret", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/hooks/claude-code", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	h(rec, req)
	if !called || rec.Code != http.StatusOK {
		t.Fatalf("expected pass-through with correct token, called=%v code=%d", called, rec.Code)
	}
}

// TestHooksReceiver_RequiresConfiguredToken is an integration-shaped check:
// a real hookreceiver.Handle wrapped by requireBearerToken must reject an
// unauthenticated request end-to-end.
func TestHooksReceiver_RequiresConfiguredToken(t *testing.T) {
	hr, err := hookreceiver.NewReceiver(filepath.Join(t.TempDir(), "hooks.jsonl"), nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	h := requireBearerToken("secret", hr.Handle)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/hooks/claude-code", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 without token, got %d", rec.Code)
	}
}
