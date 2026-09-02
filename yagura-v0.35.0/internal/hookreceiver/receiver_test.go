package hookreceiver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedNowH() time.Time {
	return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
}

type fakeLookup struct {
	m map[string]string // path prefix → slug
}

func (f *fakeLookup) ResolveByPath(cwd string) (string, bool) {
	for prefix, slug := range f.m {
		if strings.HasPrefix(cwd, prefix) {
			return slug, true
		}
	}
	return "", false
}

func newRcv(t *testing.T) (*Receiver, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.jsonl")
	lookup := &fakeLookup{m: map[string]string{
		"/home/m/breeze":  "breeze",
		"/home/m/tessera": "tessera",
	}}
	r, err := NewReceiver(path, lookup, 100)
	if err != nil {
		t.Fatal(err)
	}
	r.NowFn = fixedNowH
	return r, path
}

// ─── Basic POST handling ───────────────────────────

func TestHandle_PreToolUse(t *testing.T) {
	r, _ := newRcv(t)
	payload := `{
		"hook_event_name": "PreToolUse",
		"session_id": "sess-1",
		"cwd": "/home/m/breeze/src",
		"tool_name": "Bash",
		"tool_input": {"command": "ls"}
	}`
	req := httptest.NewRequest("POST", "/hooks/claude-code", strings.NewReader(payload))
	w := httptest.NewRecorder()
	r.Handle(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("{}")) {
		t.Errorf("response should be empty object: %s", w.Body.String())
	}
	st := r.ProjectStats("breeze")
	if st.Total != 1 || st.ByEvent["PreToolUse"] != 1 || st.ByTool["Bash"] != 1 {
		t.Errorf("stats not updated: %+v", st)
	}
}

func TestHandle_RejectsGET(t *testing.T) {
	r, _ := newRcv(t)
	req := httptest.NewRequest("GET", "/hooks/claude-code", nil)
	w := httptest.NewRecorder()
	r.Handle(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandle_InvalidJSON(t *testing.T) {
	r, _ := newRcv(t)
	req := httptest.NewRequest("POST", "/hooks/claude-code", strings.NewReader(`{not json`))
	w := httptest.NewRecorder()
	r.Handle(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestHandle_RejectsOversizedBody verifies req.Body is bounded (v0.105.0):
// an unbounded json.Decoder on an externally-reachable webhook endpoint is a
// memory-exhaustion DoS vector. Mirrors the limits already enforced on /mcp
// (1 MiB) and the HTTP API (5 MiB).
func TestHandle_RejectsOversizedBody(t *testing.T) {
	r, _ := newRcv(t)
	huge := `{"hook_event_name":"Stop","session_id":"` + strings.Repeat("x", maxHookBodyBytes+1) + `"}`
	req := httptest.NewRequest("POST", "/hooks/claude-code", strings.NewReader(huge))
	w := httptest.NewRecorder()
	r.Handle(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d body=%s", w.Code, w.Body.String())
	}
}

// TestHandle_AcceptsBodyUnderLimit is a regression guard: a normal-sized
// payload well under the cap must still be accepted after MaxBytesReader
// wiring.
func TestHandle_AcceptsBodyUnderLimit(t *testing.T) {
	r, _ := newRcv(t)
	payload := `{"hook_event_name":"Stop","session_id":"sess-1","cwd":"/home/m/breeze"}`
	req := httptest.NewRequest("POST", "/hooks/claude-code", strings.NewReader(payload))
	w := httptest.NewRecorder()
	r.Handle(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for normal-sized body, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestHandle_UnknownProject(t *testing.T) {
	r, _ := newRcv(t)
	payload := `{"hook_event_name":"Stop","session_id":"x","cwd":"/tmp/nowhere"}`
	req := httptest.NewRequest("POST", "/hooks/claude-code", strings.NewReader(payload))
	w := httptest.NewRecorder()
	r.Handle(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	st := r.ProjectStats("unknown")
	if st.Total != 1 {
		t.Errorf("unknown bucket: %+v", st)
	}
}

func TestHandle_PostToolUseDuration(t *testing.T) {
	r, _ := newRcv(t)
	payload := `{
		"hook_event_name": "PostToolUse",
		"session_id": "s",
		"cwd": "/home/m/breeze",
		"tool_name": "Bash",
		"duration_ms": 1234
	}`
	req := httptest.NewRequest("POST", "/hooks/claude-code", strings.NewReader(payload))
	r.Handle(httptest.NewRecorder(), req)
	st := r.ProjectStats("breeze")
	if st.TotalMS != 1234 {
		t.Errorf("duration: %d", st.TotalMS)
	}
}

func TestHandle_PostToolUseFailureFlagsError(t *testing.T) {
	r, _ := newRcv(t)
	payload := `{
		"hook_event_name": "PostToolUseFailure",
		"session_id": "s",
		"cwd": "/home/m/breeze",
		"tool_name": "Bash"
	}`
	req := httptest.NewRequest("POST", "/hooks/claude-code", strings.NewReader(payload))
	r.Handle(httptest.NewRecorder(), req)
	st := r.ProjectStats("breeze")
	if st.ErrorCount != 1 {
		t.Errorf("error_count: %d", st.ErrorCount)
	}
}

func TestHandle_ToolResponseIsErrorFlagged(t *testing.T) {
	r, _ := newRcv(t)
	payload := `{
		"hook_event_name": "PostToolUse",
		"session_id": "s",
		"cwd": "/home/m/breeze",
		"tool_name": "Bash",
		"tool_response": {"is_error": true, "stderr": "permission denied"}
	}`
	req := httptest.NewRequest("POST", "/hooks/claude-code", strings.NewReader(payload))
	r.Handle(httptest.NewRecorder(), req)
	st := r.ProjectStats("breeze")
	if st.ErrorCount != 1 {
		t.Errorf("is_error should bump error count: %+v", st)
	}
}

// ─── Persistence ───────────────────────────────────

func TestPersistAndReplay(t *testing.T) {
	r, path := newRcv(t)
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("POST", "/hooks/claude-code",
			strings.NewReader(`{"hook_event_name":"PreToolUse","cwd":"/home/m/breeze","tool_name":"Bash","session_id":"s"}`))
		r.Handle(httptest.NewRecorder(), req)
	}
	// replay
	r2, err := NewReceiver(path, &fakeLookup{m: map[string]string{"/home/m/breeze": "breeze"}}, 100)
	if err != nil {
		t.Fatal(err)
	}
	st := r2.ProjectStats("breeze")
	if st.Total != 3 {
		t.Errorf("replay total: %d", st.Total)
	}
}

func TestReplay_CorruptLineSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "h.jsonl")
	good := `{"hook_event_name":"Stop","project":"x","timestamp":"2026-05-13T12:00:00Z"}`
	os.WriteFile(path, []byte("{not_json\n"+good+"\n"), 0o644)
	r, err := NewReceiver(path, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if r.ProjectStats("x").Total != 1 {
		t.Error("good entry should survive corrupt line")
	}
}

// ─── In-memory mode ────────────────────────────────

func TestInMemoryMode(t *testing.T) {
	r, err := NewReceiver("", nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/hooks/claude-code",
		strings.NewReader(`{"hook_event_name":"Stop"}`))
	r.Handle(httptest.NewRecorder(), req)
	if r.ProjectStats("unknown").Total != 1 {
		t.Error("in-memory should still aggregate")
	}
}

// ─── Ring buffer eviction ─────────────────────────

func TestRingBufferEviction(t *testing.T) {
	r, err := NewReceiver("", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	r.NowFn = fixedNowH
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/hooks/claude-code",
			strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`))
		r.Handle(httptest.NewRecorder(), req)
	}
	r.mu.RLock()
	got := len(r.recent)
	r.mu.RUnlock()
	if got != 3 {
		t.Errorf("ring should cap at 3, got %d", got)
	}
}

// ─── Timeline & TopTools ──────────────────────────

func TestTimeline_FiltersBySlugAndEvent(t *testing.T) {
	r, _ := newRcv(t)
	send := func(event, cwd, tool string) {
		body, _ := json.Marshal(map[string]any{
			"hook_event_name": event, "cwd": cwd, "tool_name": tool, "session_id": "s",
		})
		req := httptest.NewRequest("POST", "/hooks/claude-code", bytes.NewReader(body))
		r.Handle(httptest.NewRecorder(), req)
	}
	send("PreToolUse", "/home/m/breeze", "Bash")
	send("PostToolUse", "/home/m/breeze", "Bash")
	send("PreToolUse", "/home/m/tessera", "Read")
	tl := r.Timeline("breeze", fixedNowH().Add(-1*time.Hour), "", 10)
	if len(tl) != 2 {
		t.Errorf("breeze timeline: %d", len(tl))
	}
	tl = r.Timeline("breeze", fixedNowH().Add(-1*time.Hour), "PreToolUse", 10)
	if len(tl) != 1 {
		t.Errorf("breeze PreToolUse only: %d", len(tl))
	}
}

func TestTopTools_GlobalAndPerProject(t *testing.T) {
	r, _ := newRcv(t)
	send := func(cwd, tool string) {
		body, _ := json.Marshal(map[string]any{
			"hook_event_name": "PreToolUse", "cwd": cwd, "tool_name": tool,
		})
		r.Handle(httptest.NewRecorder(), httptest.NewRequest("POST", "/h", bytes.NewReader(body)))
	}
	send("/home/m/breeze", "Bash")
	send("/home/m/breeze", "Bash")
	send("/home/m/breeze", "Read")
	send("/home/m/tessera", "Bash")

	top := r.TopTools("", 5)
	if len(top) < 2 || top[0].Tool != "Bash" {
		t.Errorf("global top: %v", top)
	}
	if top[0].Count != 3 {
		t.Errorf("global Bash count: %d", top[0].Count)
	}

	top = r.TopTools("breeze", 5)
	if top[0].Tool != "Bash" || top[0].Count != 2 {
		t.Errorf("breeze top: %v", top)
	}
}

// TestTopTools_NonPositiveNDefaultsAndDoesNotPanic guards the n<=0 contract:
// TopTools defaults a non-positive n to 10 (so `out[:n]` never sees a negative
// index), and with fewer tools than the default it returns the full sorted list.
// This pins the existing guard so a future refactor can't reintroduce a
// negative-index slice panic on either the global or per-slug path.
func TestTopTools_NonPositiveNDefaultsAndDoesNotPanic(t *testing.T) {
	r, _ := newRcv(t)
	send := func(cwd, tool string) {
		body, _ := json.Marshal(map[string]any{
			"hook_event_name": "PreToolUse", "cwd": cwd, "tool_name": tool,
		})
		r.Handle(httptest.NewRecorder(), httptest.NewRequest("POST", "/h", bytes.NewReader(body)))
	}
	send("/home/m/breeze", "Bash")
	send("/home/m/breeze", "Read")

	// global path and per-slug path both must default n and stay panic-free.
	if got := r.TopTools("", -1); len(got) != 2 {
		t.Errorf("global TopTools(-1): expected full list (2), got %d", len(got))
	}
	if got := r.TopTools("breeze", -1); len(got) != 2 {
		t.Errorf("per-slug TopTools(-1): expected full list (2), got %d", len(got))
	}
	if got := r.TopTools("", 0); len(got) != 2 {
		t.Errorf("global TopTools(0): expected full list (2), got %d", len(got))
	}
}

func TestAllStats(t *testing.T) {
	r, _ := newRcv(t)
	body := `{"hook_event_name":"Stop","cwd":"/home/m/breeze"}`
	r.Handle(httptest.NewRecorder(),
		httptest.NewRequest("POST", "/h", strings.NewReader(body)))
	all := r.AllStats()
	if _, ok := all["breeze"]; !ok {
		t.Error("breeze missing from AllStats")
	}
}

// ─── NewReceiver edge cases ────────────────────────────────────

// TestNewReceiver_DefaultsMaxBuf covers the maxBuf<=0 → 10000 branch.
func TestNewReceiver_DefaultsMaxBuf(t *testing.T) {
	r, err := NewReceiver("", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.maxBuf != 10000 {
		t.Errorf("maxBuf default: got %d, want 10000", r.maxBuf)
	}
}

// TestNewReceiver_MkdirAllFails covers the MkdirAll error path in NewReceiver.
// A regular file at the parent path makes os.MkdirAll fail with ENOTDIR.
func TestNewReceiver_MkdirAllFails(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewReceiver(filepath.Join(blocker, "hooks.jsonl"), nil, 1)
	if err == nil {
		t.Error("expected error when parent path is a regular file")
	}
}

// TestNewReceiver_ReplayError covers the r.replay() error return in NewReceiver.
// A JSONL file with a 2MB line triggers bufio.ErrTooLong from the scanner.
func TestNewReceiver_ReplayError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.jsonl")
	huge := make([]byte, 2*1024*1024+2)
	for i := range huge {
		huge[i] = 'x'
	}
	huge[0] = '{'
	huge[len(huge)-1] = '\n'
	if err := os.WriteFile(path, huge, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewReceiver(path, nil, 1)
	if err == nil {
		t.Error("expected error for file with line > 1MB")
	}
}

// ─── replay edge cases ───────────────────────────────────────

// TestReplay_BlankLinesSkipped covers the line=="" continue in replay.
func TestReplay_BlankLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "replay.jsonl")
	good := `{"hook_event_name":"Stop","project":"myproj","timestamp":"2026-05-13T12:00:00Z"}`
	content := "\n" + good + "\n\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &Receiver{
		path:   path,
		stats:  map[string]*Stats{},
		recent: make([]Event, 0, 10),
		maxBuf: 10,
		NowFn:  time.Now,
	}
	if err := r.replay(); err != nil {
		t.Fatalf("replay: %v", err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.stats["myproj"] == nil || r.stats["myproj"].Total != 1 {
		t.Error("blank lines should be skipped; valid entry should load")
	}
}

// TestReplay_NonENOENTOpenError covers the fmt.Errorf return in replay when
// os.Open fails with a non-ENOENT error (path component is a regular file).
func TestReplay_NonENOENTOpenError(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Receiver{
		path:   filepath.Join(blocker, "nested.jsonl"),
		stats:  map[string]*Stats{},
		recent: make([]Event, 0, 10),
		maxBuf: 10,
		NowFn:  time.Now,
	}
	if err := r.replay(); err == nil {
		t.Error("expected error when path component is a regular file (ENOTDIR)")
	}
}

// ─── append error path ────────────────────────────────────────

// TestAppend_OpenFileFails covers the os.OpenFile failure return in append.
func TestAppend_OpenFileFails(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Receiver{
		path:   filepath.Join(blocker, "nested.jsonl"),
		stats:  map[string]*Stats{},
		recent: make([]Event, 0, 10),
		maxBuf: 10,
		NowFn:  time.Now,
	}
	if err := r.append(Event{HookEventName: "Stop"}); err == nil {
		t.Error("expected error when OpenFile fails due to ENOTDIR path")
	}
}

// ─── Timeline edge cases ─────────────────────────────────────

// TestTimeline_DefaultsLimit covers limit<=0 → 100 in Timeline.
func TestTimeline_DefaultsLimit(t *testing.T) {
	r, err := NewReceiver("", nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	r.NowFn = fixedNowH
	req := httptest.NewRequest("POST", "/h",
		strings.NewReader(`{"hook_event_name":"Stop","cwd":""}`))
	r.Handle(httptest.NewRecorder(), req)
	// limit=0 → default 100 → returns the event
	tl := r.Timeline("", fixedNowH().Add(-time.Hour), "", 0)
	if len(tl) != 1 {
		t.Errorf("expected 1 event with limit=0 (default 100), got %d", len(tl))
	}
}

// TestTimeline_BreaksOnOldEvent covers the !e.Timestamp.After(since) break.
func TestTimeline_BreaksOnOldEvent(t *testing.T) {
	r, err := NewReceiver("", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Add event with an old timestamp by inserting directly into recent.
	old := fixedNowH().Add(-2 * time.Hour)
	r.mu.Lock()
	r.recent = append(r.recent, Event{
		HookEventName: "Stop",
		Project:       "x",
		Timestamp:     old,
	})
	r.mu.Unlock()
	// Query with since = 1 hour before now → old event is before since → break
	tl := r.Timeline("", fixedNowH().Add(-time.Hour), "", 10)
	if len(tl) != 0 {
		t.Errorf("event older than since should be excluded via break: got %d events", len(tl))
	}
}

// TestTimeline_LimitHit covers the len(out)>=limit break.
func TestTimeline_LimitHit(t *testing.T) {
	r, err := NewReceiver("", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	r.NowFn = fixedNowH
	send := func() {
		req := httptest.NewRequest("POST", "/h",
			strings.NewReader(`{"hook_event_name":"Stop","cwd":""}`))
		r.Handle(httptest.NewRecorder(), req)
	}
	for i := 0; i < 5; i++ {
		send()
	}
	tl := r.Timeline("", fixedNowH().Add(-time.Hour), "", 2)
	if len(tl) != 2 {
		t.Errorf("limit=2 should return 2 events, got %d", len(tl))
	}
}

// ─── hookNameFor unknown op ───────────────────────────────────

// TestHookNameFor_UnknownOp covers the return op+":"+phase fallthrough.
func TestHookNameFor_UnknownOp(t *testing.T) {
	got := hookNameFor("custom_op", "begin")
	if got != "custom_op:begin" {
		t.Errorf("unknown op: got %q, want custom_op:begin", got)
	}
}

// ─── TopTools trim and unknown slug ──────────────────────────

// TestTopTools_TrimToN covers the len(out)>n slice-trim in both global and slug paths.
func TestTopTools_TrimToN(t *testing.T) {
	r, _ := newRcv(t)
	send := func(cwd, tool string) {
		body, _ := json.Marshal(map[string]any{
			"hook_event_name": "PreToolUse", "cwd": cwd, "tool_name": tool,
		})
		r.Handle(httptest.NewRecorder(), httptest.NewRequest("POST", "/h", bytes.NewReader(body)))
	}
	// Register 4 distinct tools so we can trim to n=2.
	for _, tool := range []string{"Bash", "Read", "Edit", "Write"} {
		send("/home/m/breeze", tool)
	}
	top := r.TopTools("", 2) // global: trim 4 → 2
	if len(top) != 2 {
		t.Errorf("global trim to 2: got %d", len(top))
	}
	top = r.TopTools("breeze", 2) // per-slug: trim 4 → 2
	if len(top) != 2 {
		t.Errorf("per-slug trim to 2: got %d", len(top))
	}
}

// TestTopTools_UnknownSlug covers the !ok return nil in TopTools.
func TestTopTools_UnknownSlug(t *testing.T) {
	r, _ := newRcv(t)
	if got := r.TopTools("no-such-project", 5); got != nil {
		t.Errorf("unknown slug should return nil, got %v", got)
	}
}

// TestHandle_AppendFails_BestEffort covers the "_ = err" best-effort append branch
// in Handle: the receiver still returns HTTP 200 even when append fails.
func TestHandle_AppendFails_BestEffort(t *testing.T) {
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// path under a regular file → OpenFile inside append returns ENOTDIR.
	r := &Receiver{
		path:   filepath.Join(blocker, "nested.jsonl"),
		lookup: nil,
		stats:  map[string]*Stats{},
		recent: make([]Event, 0, 10),
		maxBuf: 10,
		NowFn:  fixedNowH,
	}
	req := httptest.NewRequest("POST", "/hooks", strings.NewReader(`{"hook_event_name":"Stop"}`))
	w := httptest.NewRecorder()
	r.Handle(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("Handle should return 200 even when append fails; got %d", w.Code)
	}
}

// helper imports
var _ = io.EOF
var _ = json.NewEncoder

// foreign-agent events (no hook_event_name) are normalized via agentevent and
// recorded with Claude-Code-equivalent vocabulary, so hook_stats works for any agent.
func TestHandle_ForeignAgentNormalized(t *testing.T) {
	r, _ := newRcv(t)
	post := func(payload string) {
		req := httptest.NewRequest("POST", "/hooks/agent", strings.NewReader(payload))
		w := httptest.NewRecorder()
		r.Handle(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	// Gemini CLI style: beforeToolCall → PreToolUse
	post(`{"agent":"gemini_cli","event":"beforeToolCall","tool":"Bash","cwd":"/home/m/breeze/src","session":"g1"}`)
	// Codex style failure: toolResult + status failure → PostToolUseFailure
	post(`{"agent":"codex","type":"toolResult","tool":"Bash","cwd":"/home/m/breeze/src","status":"failure"}`)

	st := r.ProjectStats("breeze")
	if st.Total != 2 {
		t.Fatalf("expected 2 events, got %d (%+v)", st.Total, st)
	}
	if st.ByEvent["PreToolUse"] != 1 || st.ByEvent["PostToolUseFailure"] != 1 {
		t.Errorf("event vocabulary not normalized: %+v", st.ByEvent)
	}
	if st.ByTool["Bash"] != 2 {
		t.Errorf("tool not recorded across agents: %+v", st.ByTool)
	}
	if st.ErrorCount != 1 {
		t.Errorf("foreign-agent error not counted: %+v", st)
	}
}

// hookNameFor maps canonical operation/phase to Claude-Code-equivalent names.
func TestHookNameFor(t *testing.T) {
	cases := []struct{ op, phase, want string }{
		{"execute_tool", "start", "PreToolUse"},
		{"execute_tool", "end", "PostToolUse"},
		{"execute_tool", "error", "PostToolUseFailure"},
		{"invoke_agent", "start", "SessionStart"},
		{"invoke_agent", "end", "Stop"},
		{"chat", "end", "Notification"},
		{"", "end", "Notification"},
	}
	for _, c := range cases {
		if got := hookNameFor(c.op, c.phase); got != c.want {
			t.Errorf("hookNameFor(%q,%q)=%q want %q", c.op, c.phase, got, c.want)
		}
	}
}

// TestProjectStats_NoRaceWithConcurrentWrites reproduces the v0.35 fix: callers
// range the returned ByTool/ByEvent maps without holding the receiver lock, so
// ProjectStats/AllStats must hand back deep copies — otherwise a concurrent hook
// write triggers a fatal "concurrent map iteration and map write". Run with -race.
func TestProjectStats_NoRaceWithConcurrentWrites(t *testing.T) {
	r, err := NewReceiver("", &fakeLookup{m: map[string]string{"/p": "proj"}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	// writer: a stream of tool events mutating proj's ByTool/ByEvent maps.
	go func() {
		for i := 0; i < 2000; i++ {
			r.Handle(httptest.NewRecorder(), httptest.NewRequest("POST", "/hooks/agent",
				strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","cwd":"/p"}`)))
		}
		close(done)
	}()
	// reader: range the returned maps the way the dashboard / metrics paths do.
	for {
		select {
		case <-done:
			return
		default:
		}
		st := r.ProjectStats("proj")
		for k := range st.ByTool {
			_ = st.ByTool[k]
		}
		for range r.AllStats() {
		}
	}
}
