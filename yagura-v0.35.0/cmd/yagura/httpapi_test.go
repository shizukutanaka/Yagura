package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"context"
	"errors"
	"testing"

	"github.com/shizukutanaka/yagura/internal/ghaaudit"
	"github.com/shizukutanaka/yagura/internal/github"
	"github.com/shizukutanaka/yagura/internal/pindrift"
	"github.com/shizukutanaka/yagura/internal/sbom"
)

func newTestHTTPMux(t *testing.T, authToken string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	registerHTTPAPI(mux, httpAPIDeps{
		Sbom:           sbom.New(),
		Ghaaudit:       ghaaudit.New(),
		PinDrift:       pindrift.New(&nullGH{}),
		MainModulePath: "github.com/shizukutanaka/yagura",
		MainVersion:    "0.11.0-test",
		AuthToken:      authToken,
	})
	return mux
}


// ─── GET /sbom ───────────────────────────────────────────────

func TestHTTP_GETSBOM_NoAuth(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sbom")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var bom map[string]any
	if err := json.Unmarshal(body, &bom); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if bom["bomFormat"] != "CycloneDX" {
		t.Errorf("bomFormat: %v", bom["bomFormat"])
	}
	if bom["specVersion"] != "1.5" {
		t.Errorf("specVersion: %v", bom["specVersion"])
	}
}

func TestHTTP_GETSBOM_SummaryOnly(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sbom?summary_only=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var s map[string]any
	_ = json.Unmarshal(body, &s)
	if s["spec_version"] != "1.5" {
		t.Errorf("expected summary form, got: %s", body)
	}
}

func TestHTTP_GETSBOM_WrongMethod(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/sbom", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

// ─── Auth ────────────────────────────────────────────────────

func TestHTTP_Auth_Required(t *testing.T) {
	mux := newTestHTTPMux(t, "secret-token")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// no auth → 401
	resp, err := http.Get(srv.URL + "/sbom")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// wrong token → 401
	req, _ := http.NewRequest("GET", srv.URL+"/sbom", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp2.StatusCode)
	}

	// correct token → 200
	req3, _ := http.NewRequest("GET", srv.URL+"/sbom", nil)
	req3.Header.Set("Authorization", "Bearer secret-token")
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp3.StatusCode)
	}
}

// ─── POST /gha-audit ─────────────────────────────────────────

func TestHTTP_POSTGhaAudit(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := map[string]any{
		"files": map[string]string{
			"vuln.yml": `on:
  pull_request_target:
jobs:
  x:
    steps:
      - uses: tj-actions/changed-files@main
`,
		},
	}
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/gha-audit", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, buf)
	}
	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), "dangerous-trigger") {
		t.Errorf("expected dangerous-trigger finding: %s", out)
	}
	if !strings.Contains(string(out), "mutable-ref") {
		t.Errorf("expected mutable-ref finding: %s", out)
	}
}

func TestHTTP_POSTGhaAudit_EmptyBody(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/gha-audit", "application/json", strings.NewReader(`{"files":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHTTP_POSTGhaAudit_WrongContentType(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/gha-audit", "text/plain", strings.NewReader(`{"files":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for wrong content-type, got %d", resp.StatusCode)
	}
}

// ─── POST /pin-drift ─────────────────────────────────────────

func TestHTTP_POSTPinDrift_NoPins(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	body := map[string]any{
		"files": map[string]string{
			"x.yml": "on: [push]\n", // no uses:
		},
	}
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/pin-drift", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), "no SHA-pinned") {
		t.Errorf("expected 'no SHA-pinned' note: %s", out)
	}
}

func TestHTTP_POSTPinDrift_405_GET(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/pin-drift")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", resp.StatusCode)
	}
}

// nullGH は pindrift 用の no-op mock(全 SHA を 404 扱いで MISSING に)
type nullGH struct{}

var errAuth = errors.New("test: no api")

func (n *nullGH) GetCommit(ctx context.Context, owner, repo, sha string) (*github.CommitInfo, error) {
	return nil, github.ErrNotFound
}
func (n *nullGH) GetTagSHA(ctx context.Context, owner, repo, tag string) (string, error) {
	return "", nil
}

// ─── writeSSEPinDrift ────────────────────────────────────────

// mockPinDriftStreamer は即座に空 channel を閉じる stub。
type mockPinDriftStreamer struct{}

func (m *mockPinDriftStreamer) CheckPinsStream(ctx context.Context, pins []pindrift.Pin, concurrency int) <-chan pindrift.ResultEvent {
	ch := make(chan pindrift.ResultEvent)
	close(ch)
	return ch
}

func TestWriteSSEPinDrift_EmptyPins(t *testing.T) {
	w := httptest.NewRecorder()
	writeSSEPinDrift(context.Background(), w, &mockPinDriftStreamer{}, nil, 1)
	resp := w.Result()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected text/event-stream, got %q", resp.Header.Get("Content-Type"))
	}
	body := w.Body.String()
	if !strings.Contains(body, "event: done") {
		t.Errorf("expected done event, got %q", body)
	}
}

// ─── handlePinDrift additional paths ─────────────────────────

func TestHTTP_POSTPinDrift_BadJSON(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/pin-drift", "application/json", strings.NewReader("{not-valid-json}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for bad JSON, got %d", resp.StatusCode)
	}
}

func TestHTTP_POSTPinDrift_EmptyFilesMap(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	b, _ := json.Marshal(map[string]any{"files": map[string]string{}})
	resp, err := http.Post(srv.URL+"/pin-drift", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty files, got %d", resp.StatusCode)
	}
}

// TestHTTP_POSTPinDrift_SummaryOnly exercises the summary_only branch with a
// workflow containing a SHA-pinned uses: (avoids live GitHub API via nullGH).
func TestHTTP_POSTPinDrift_SummaryOnly(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	body := map[string]any{
		"files": map[string]string{
			"ci.yml": "- uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683\n",
		},
		"summary_only": true,
		"concurrency":  -1, // serial → CheckPins (not parallel)
	}
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/pin-drift", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, out)
	}
	out, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, out)
	}
	// summary_only=true → response is a Summary struct (has total_pins key)
	if _, ok := m["total_pins"]; !ok {
		t.Errorf("expected total_pins in summary response: %v", m)
	}
}

func TestHTTP_POSTGhaAudit_SummaryOnly(t *testing.T) {
	mux := newTestHTTPMux(t, "")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := map[string]any{
		"files": map[string]string{
			"safe.yml": "on: [push]\njobs:\n  x:\n    steps:\n      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683\n",
		},
		"summary_only": true,
	}
	b, _ := json.Marshal(body)
	resp, err := http.Post(srv.URL+"/gha-audit", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, buf)
	}
	out, _ := io.ReadAll(resp.Body)
	// summary_only → returns ghaaudit.Summary struct directly (has total key)
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, out)
	}
	if _, ok := m["total_files"]; !ok {
		t.Errorf("expected 'total_files' in summary response, got keys: %v", m)
	}
}

// nonFlushRecorder is an httptest.ResponseRecorder that does NOT implement
// http.Flusher, exercising the !ok branch in writeSSEPinDrift.
type nonFlushRecorder struct {
	*httptest.ResponseRecorder
}

func (nonFlushRecorder) Flush() { /* not present — breaks interface */ }

// Ensure nonFlushRecorder does NOT satisfy http.Flusher at compile time.
// (The embedded ResponseRecorder does; we must strip it.)
type strippedRecorder struct {
	code    int
	body    bytes.Buffer
	headers http.Header
}

func (r *strippedRecorder) Header() http.Header {
	if r.headers == nil {
		r.headers = make(http.Header)
	}
	return r.headers
}
func (r *strippedRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }
func (r *strippedRecorder) WriteHeader(code int)        { r.code = code }

func TestWriteSSEPinDrift_NonFlusher(t *testing.T) {
	// strippedRecorder does NOT implement http.Flusher → triggers the !ok branch.
	w := &strippedRecorder{}
	writeSSEPinDrift(context.Background(), w, &mockPinDriftStreamer{}, nil, 1)
	if w.code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", w.code)
	}
	if !strings.Contains(w.body.String(), "streaming not supported") {
		t.Errorf("expected 'streaming not supported', got %q", w.body.String())
	}
}

// resultPinDriftStreamer sends one result event then closes.
type resultPinDriftStreamer struct{}

func (m *resultPinDriftStreamer) CheckPinsStream(ctx context.Context, pins []pindrift.Pin, concurrency int) <-chan pindrift.ResultEvent {
	ch := make(chan pindrift.ResultEvent, 1)
	ch <- pindrift.ResultEvent{
		Index:      0,
		TotalCount: 1,
		Result:     pindrift.Result{Pin: pindrift.Pin{Owner: "actions", Repo: "checkout"}, Status: "unpinned"},
	}
	close(ch)
	return ch
}

func TestWriteSSEPinDrift_WithResult(t *testing.T) {
	w := httptest.NewRecorder()
	pins := []pindrift.Pin{{Owner: "actions", Repo: "checkout"}}
	writeSSEPinDrift(context.Background(), w, &resultPinDriftStreamer{}, pins, 1)
	body := w.Body.String()
	if !strings.Contains(body, "event: result") {
		t.Errorf("expected 'event: result' in SSE body, got %q", body)
	}
	if !strings.Contains(body, "event: done") {
		t.Errorf("expected 'event: done' in SSE body, got %q", body)
	}
	if !strings.Contains(body, "unpinned") {
		t.Errorf("expected unpinned status in body, got %q", body)
	}
}
