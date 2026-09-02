package pindrift

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/github"
)

// ─── ExtractPins ─────────────────────────────────────────────

func TestExtractPins_BasicPin(t *testing.T) {
	yaml := `name: ci
jobs:
  x:
    steps:
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683
`
	pins := ExtractPins("ci.yml", yaml)
	if len(pins) != 1 {
		t.Fatalf("expected 1 pin, got %d", len(pins))
	}
	p := pins[0]
	if p.Owner != "actions" || p.Repo != "checkout" {
		t.Errorf("owner/repo: %s/%s", p.Owner, p.Repo)
	}
	if p.PinnedSHA != "11bd71901bbe5b1630ceea73d27597364c9af683" {
		t.Errorf("SHA: %s", p.PinnedSHA)
	}
	if p.TagComment != "" {
		t.Errorf("unexpected tag comment: %s", p.TagComment)
	}
}

func TestExtractPins_WithTagComment(t *testing.T) {
	yaml := `      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2
      - uses: actions/setup-go@41dfa10bad2bb2ae585af6ee5bb4d7d973ad74ed # pin@v5
`
	pins := ExtractPins("ci.yml", yaml)
	if len(pins) != 2 {
		t.Fatalf("expected 2 pins, got %d", len(pins))
	}
	if pins[0].TagComment != "v4.2.2" {
		t.Errorf("first tag: %q", pins[0].TagComment)
	}
	if pins[1].TagComment != "pin@v5" {
		t.Errorf("second tag: %q", pins[1].TagComment)
	}
}

func TestExtractPins_IgnoresUnpinned(t *testing.T) {
	yaml := `      - uses: actions/checkout@v4
      - uses: actions/setup-go@main
`
	pins := ExtractPins("ci.yml", yaml)
	if len(pins) != 0 {
		t.Errorf("non-SHA refs should not be extracted as pins: %+v", pins)
	}
}

func TestExtractPins_IgnoresLocalActions(t *testing.T) {
	yaml := `      - uses: ./local/action
`
	pins := ExtractPins("ci.yml", yaml)
	if len(pins) != 0 {
		t.Errorf("local actions should be ignored: %+v", pins)
	}
}

// TestExtractPins_LocalActionWithSHA covers the `continue` for a local action
// (owner starts with "."). A local composite action can legitimately be pinned
// with a 40-hex string, so the regex matches but ExtractPins must skip it.
func TestExtractPins_LocalActionWithSHA(t *testing.T) {
	yaml := "      - uses: ./.github/actions/setup@" + goodSHA + "\n"
	pins := ExtractPins("ci.yml", yaml)
	if len(pins) != 0 {
		t.Errorf("local action pinned with a SHA must be skipped, got %+v", pins)
	}
}

func TestExtractPins_ReusableWorkflow(t *testing.T) {
	// reusable workflow form: owner/repo/.github/workflows/x.yml@sha
	yaml := `jobs:
  x:
    uses: slsa-framework/slsa-github-generator/.github/workflows/builder.yml@b18c6c84f0bd6f1f74c12dabc2f9aa54ec48b80c
`
	pins := ExtractPins("ci.yml", yaml)
	if len(pins) != 1 {
		t.Fatalf("expected 1 reusable workflow pin, got %d", len(pins))
	}
	if pins[0].Owner != "slsa-framework" || pins[0].Repo != "slsa-github-generator" {
		t.Errorf("owner/repo: %s/%s", pins[0].Owner, pins[0].Repo)
	}
}

func TestExtractPins_LineNumberCorrect(t *testing.T) {
	yaml := `name: ci
on: [push]
jobs:
  x:
    steps:
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683
`
	pins := ExtractPins("ci.yml", yaml)
	if pins[0].Line != 6 {
		t.Errorf("expected line 6, got %d", pins[0].Line)
	}
}

// ─── CheckPin: mock GH ─────────────────────────────────────

// mockGH は GitHubClient interface の手書きモック。
type mockGH struct {
	commits   map[string]*github.CommitInfo // key = "owner/repo/sha"
	commitErr map[string]error
	tagSHAs   map[string]string // key = "owner/repo/tag"
	tagErr    map[string]error
}

func (m *mockGH) GetCommit(ctx context.Context, owner, repo, sha string) (*github.CommitInfo, error) {
	key := owner + "/" + repo + "/" + sha
	if err := m.commitErr[key]; err != nil {
		return nil, err
	}
	if c := m.commits[key]; c != nil {
		return c, nil
	}
	return nil, github.ErrNotFound
}

func (m *mockGH) GetTagSHA(ctx context.Context, owner, repo, tag string) (string, error) {
	key := owner + "/" + repo + "/" + tag
	if err := m.tagErr[key]; err != nil {
		return "", err
	}
	return m.tagSHAs[key], nil
}

func newMockGH() *mockGH {
	return &mockGH{
		commits:   map[string]*github.CommitInfo{},
		commitErr: map[string]error{},
		tagSHAs:   map[string]string{},
		tagErr:    map[string]error{},
	}
}

// テスト用 commit 構築 helper
func makeCommit(sha, date string) *github.CommitInfo {
	c := &github.CommitInfo{SHA: sha}
	c.Commit.Committer.Date = date
	return c
}

// ─── CheckPin tests ──────────────────────────────────────────

const goodSHA = "11bd71901bbe5b1630ceea73d27597364c9af683"

func TestCheckPin_OK(t *testing.T) {
	gh := newMockGH()
	gh.commits["actions/checkout/"+goodSHA] = makeCommit(goodSHA, time.Now().AddDate(0, -3, 0).UTC().Format(time.RFC3339))
	c := New(gh)
	c.NowFn = time.Now
	r := c.CheckPin(context.Background(), Pin{
		File: "ci.yml", Line: 1,
		Owner: "actions", Repo: "checkout", PinnedSHA: goodSHA,
	})
	if r.Status != StatusOK {
		t.Errorf("expected OK, got %s: %s", r.Status, r.Detail)
	}
}

func TestCheckPin_Missing(t *testing.T) {
	gh := newMockGH()
	// commit を登録しない → 404
	c := New(gh)
	r := c.CheckPin(context.Background(), Pin{
		Owner: "actions", Repo: "checkout", PinnedSHA: goodSHA,
	})
	if r.Status != StatusMissing {
		t.Errorf("expected MISSING, got %s: %s", r.Status, r.Detail)
	}
}

func TestCheckPin_TagDrift(t *testing.T) {
	gh := newMockGH()
	pinDate := time.Now().AddDate(0, -1, 0).UTC().Format(time.RFC3339)
	gh.commits["actions/checkout/"+goodSHA] = makeCommit(goodSHA, pinDate)
	// tag が今は別の SHA を指している
	gh.tagSHAs["actions/checkout/v4.2.2"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	c := New(gh)
	r := c.CheckPin(context.Background(), Pin{
		Owner: "actions", Repo: "checkout",
		PinnedSHA: goodSHA, TagComment: "v4.2.2",
	})
	if r.Status != StatusTagDrift {
		t.Fatalf("expected TAG_DRIFT, got %s: %s", r.Status, r.Detail)
	}
	if r.LatestTagSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("LatestTagSHA: %s", r.LatestTagSHA)
	}
	if !strings.Contains(r.Detail, "Trivy") {
		t.Errorf("detail should mention attack pattern: %s", r.Detail)
	}
}

func TestCheckPin_TagMatchIsOK(t *testing.T) {
	gh := newMockGH()
	gh.commits["actions/checkout/"+goodSHA] = makeCommit(goodSHA, time.Now().AddDate(0, -1, 0).UTC().Format(time.RFC3339))
	gh.tagSHAs["actions/checkout/v4.2.2"] = goodSHA // tag still points to pinned SHA
	c := New(gh)
	r := c.CheckPin(context.Background(), Pin{
		Owner: "actions", Repo: "checkout",
		PinnedSHA: goodSHA, TagComment: "v4.2.2",
	})
	if r.Status != StatusOK {
		t.Errorf("expected OK, got %s: %s", r.Status, r.Detail)
	}
}

func TestCheckPin_Stale(t *testing.T) {
	gh := newMockGH()
	// 2 年前
	gh.commits["actions/checkout/"+goodSHA] = makeCommit(goodSHA, time.Now().AddDate(-2, 0, 0).UTC().Format(time.RFC3339))
	c := New(gh)
	r := c.CheckPin(context.Background(), Pin{
		Owner: "actions", Repo: "checkout", PinnedSHA: goodSHA,
	})
	if r.Status != StatusStale {
		t.Errorf("expected STALE, got %s: %s", r.Status, r.Detail)
	}
	if r.AgeDays < 365 {
		t.Errorf("AgeDays should be >= 365: %d", r.AgeDays)
	}
}

func TestCheckPin_Unverifiable(t *testing.T) {
	gh := newMockGH()
	gh.commitErr["actions/checkout/"+goodSHA] = errors.New("rate limited")
	c := New(gh)
	r := c.CheckPin(context.Background(), Pin{
		Owner: "actions", Repo: "checkout", PinnedSHA: goodSHA,
	})
	if r.Status != StatusUnverifiable {
		t.Errorf("expected UNVERIFIABLE, got %s: %s", r.Status, r.Detail)
	}
}

// ─── looksLikeTag ────────────────────────────────────────────

func TestLooksLikeTag(t *testing.T) {
	cases := map[string]bool{
		"v4":        true,
		"v4.2.2":    true,
		"1.0":       true,
		"1.2.3-rc1": true,
		"pin@v4":    true,
		"trusted":   false,
		"":          false,
		"main":      false,
		"some text": false,
	}
	for in, want := range cases {
		if got := looksLikeTag(in); got != want {
			t.Errorf("looksLikeTag(%q) = %v, want %v", in, got, want)
		}
	}
}

// ─── Summarize ──────────────────────────────────────────────

func TestSummarize(t *testing.T) {
	results := []Result{
		{Status: StatusOK},
		{Status: StatusOK},
		{Status: StatusTagDrift, Pin: Pin{Owner: "x", Repo: "y"}},
		{Status: StatusStale, Pin: Pin{Owner: "a", Repo: "b"}},
	}
	s := Summarize(results)
	if s.TotalPins != 4 {
		t.Errorf("TotalPins: %d", s.TotalPins)
	}
	if s.ByStatus["OK"] != 2 || s.ByStatus["TAG_DRIFT"] != 1 || s.ByStatus["STALE"] != 1 {
		t.Errorf("ByStatus: %+v", s.ByStatus)
	}
	if len(s.Concerning) != 2 {
		t.Errorf("Concerning should be 2 (non-OK): %d", len(s.Concerning))
	}
}

// ─── CheckPins (batch) ──────────────────────────────────────

func TestCheckPins_Batch(t *testing.T) {
	gh := newMockGH()
	gh.commits["actions/checkout/"+goodSHA] = makeCommit(goodSHA, time.Now().UTC().Format(time.RFC3339))
	c := New(gh)
	results := c.CheckPins(context.Background(), []Pin{
		{Owner: "actions", Repo: "checkout", PinnedSHA: goodSHA},
		{Owner: "actions", Repo: "missing", PinnedSHA: goodSHA},
	})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Status != StatusOK {
		t.Errorf("first should be OK: %s", results[0].Status)
	}
	if results[1].Status != StatusMissing {
		t.Errorf("second should be MISSING: %s", results[1].Status)
	}
}

func TestCheckPins_ContextCancel(t *testing.T) {
	gh := newMockGH()
	c := New(gh)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // すぐキャンセル
	results := c.CheckPins(ctx, []Pin{
		{Owner: "a", Repo: "b", PinnedSHA: goodSHA},
		{Owner: "c", Repo: "d", PinnedSHA: goodSHA},
	})
	// 少なくとも len(results) == 2 (preallocated)
	if len(results) != 2 {
		t.Errorf("results length: %d", len(results))
	}
}

// ─── 数値変換 helpers ─────────────────────────────────────

func TestStrconvI(t *testing.T) {
	cases := map[int]string{
		0:    "0",
		1:    "1",
		42:   "42",
		1234: "1234",
		-5:   "-5",
	}
	for in, want := range cases {
		if got := strconvI(in); got != want {
			t.Errorf("strconvI(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDurationLabel(t *testing.T) {
	cases := map[int]string{
		5:    "5 days",
		30:   "1 month",
		60:   "2 months",
		365:  "1 year",
		730:  "2 years",
		1095: "3 years",
	}
	for days, want := range cases {
		if got := durationLabel(days); got != want {
			t.Errorf("durationLabel(%d) = %q, want %q", days, got, want)
		}
	}
}

// ─── CheckPinsParallel ───────────────────────────────────────

// TestCheckPinsParallel_OrderPreserved は input 順序が出力でも保持されることを検証。
func TestCheckPinsParallel_OrderPreserved(t *testing.T) {
	gh := newMockGH()
	now := time.Now().UTC().Format(time.RFC3339)
	// 各 owner ごとに distinct な SHA を登録
	for i := 0; i < 10; i++ {
		sha := goodSHA[:len(goodSHA)-1] + string(rune('0'+i))
		gh.commits["owner/repo/"+sha] = makeCommit(sha, now)
	}

	pins := make([]Pin, 10)
	for i := range pins {
		pins[i] = Pin{
			Owner:     "owner",
			Repo:      "repo",
			PinnedSHA: goodSHA[:len(goodSHA)-1] + string(rune('0'+i)),
		}
	}

	c := New(gh)
	results := c.CheckPinsParallel(context.Background(), pins, 4)

	if len(results) != 10 {
		t.Fatalf("results length: %d", len(results))
	}
	// 各 result が対応する pin と一致するか
	for i, r := range results {
		if r.Pin.PinnedSHA != pins[i].PinnedSHA {
			t.Errorf("order violated at %d: got %s, want %s",
				i, r.Pin.PinnedSHA, pins[i].PinnedSHA)
		}
	}
}

// TestCheckPinsParallel_ConcurrencyDefault は 0 以下の concurrency でデフォルト動作。
func TestCheckPinsParallel_ConcurrencyDefault(t *testing.T) {
	gh := newMockGH()
	gh.commits["a/b/"+goodSHA] = makeCommit(goodSHA, time.Now().UTC().Format(time.RFC3339))
	c := New(gh)
	results := c.CheckPinsParallel(context.Background(),
		[]Pin{{Owner: "a", Repo: "b", PinnedSHA: goodSHA}}, 0)
	if len(results) != 1 || results[0].Status != StatusOK {
		t.Errorf("default concurrency failed: %+v", results)
	}
}

// TestCheckPinsParallel_EmptyInput は空入力で nil 返却。
func TestCheckPinsParallel_EmptyInput(t *testing.T) {
	c := New(newMockGH())
	results := c.CheckPinsParallel(context.Background(), nil, 4)
	if results != nil {
		t.Errorf("expected nil for empty input, got: %+v", results)
	}
}

// TestCheckPinsParallel_ConcurrencyClampedToPinsCount は concurrency が pin 数より
// 大きい場合に clamp されることを確認。
func TestCheckPinsParallel_ConcurrencyClampedToPinsCount(t *testing.T) {
	gh := newMockGH()
	now := time.Now().UTC().Format(time.RFC3339)
	gh.commits["a/b/"+goodSHA] = makeCommit(goodSHA, now)
	gh.commits["c/d/"+goodSHA] = makeCommit(goodSHA, now)

	c := New(gh)
	results := c.CheckPinsParallel(context.Background(),
		[]Pin{{Owner: "a", Repo: "b", PinnedSHA: goodSHA}, {Owner: "c", Repo: "d", PinnedSHA: goodSHA}},
		100) // concurrency much larger than 2 pins
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

// TestCheckPinsParallel_ContextCancel は途中キャンセルで停止することを確認。
func TestCheckPinsParallel_ContextCancel(t *testing.T) {
	c := New(newMockGH())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// すべて missing で返るが、count は保たれる
	results := c.CheckPinsParallel(ctx,
		[]Pin{
			{Owner: "a", Repo: "b", PinnedSHA: goodSHA},
			{Owner: "c", Repo: "d", PinnedSHA: goodSHA},
		}, 2)
	if len(results) != 2 {
		t.Errorf("results length: %d", len(results))
	}
}

// ─── ベンチマーク: serial vs parallel ───────────────────────

// mockSlowGH は I/O delay をシミュレートする GitHub mock(各 call 5ms)。
type mockSlowGH struct {
	delay time.Duration
}

func (m *mockSlowGH) GetCommit(ctx context.Context, owner, repo, sha string) (*github.CommitInfo, error) {
	select {
	case <-time.After(m.delay):
		return makeCommit(sha, time.Now().UTC().Format(time.RFC3339)), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (m *mockSlowGH) GetTagSHA(ctx context.Context, owner, repo, tag string) (string, error) {
	return "", nil
}

func BenchmarkCheckPins_Serial(b *testing.B) {
	gh := &mockSlowGH{delay: 5 * time.Millisecond}
	c := New(gh)
	pins := make([]Pin, 20)
	for i := range pins {
		pins[i] = Pin{Owner: "a", Repo: "b", PinnedSHA: goodSHA}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CheckPins(context.Background(), pins)
	}
}

func BenchmarkCheckPins_Parallel4(b *testing.B) {
	gh := &mockSlowGH{delay: 5 * time.Millisecond}
	c := New(gh)
	pins := make([]Pin, 20)
	for i := range pins {
		pins[i] = Pin{Owner: "a", Repo: "b", PinnedSHA: goodSHA}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CheckPinsParallel(context.Background(), pins, 4)
	}
}

func BenchmarkCheckPins_Parallel8(b *testing.B) {
	gh := &mockSlowGH{delay: 5 * time.Millisecond}
	c := New(gh)
	pins := make([]Pin, 20)
	for i := range pins {
		pins[i] = Pin{Owner: "a", Repo: "b", PinnedSHA: goodSHA}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.CheckPinsParallel(context.Background(), pins, 8)
	}
}

// ─── CheckPinsStream ──────────────────────────────────────────

func TestCheckPinsStream_EmitsAllEvents(t *testing.T) {
	gh := newMockGH()
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < 5; i++ {
		sha := goodSHA[:len(goodSHA)-1] + string(rune('0'+i))
		gh.commits["owner/repo/"+sha] = makeCommit(sha, now)
	}
	pins := make([]Pin, 5)
	for i := range pins {
		pins[i] = Pin{Owner: "owner", Repo: "repo", PinnedSHA: goodSHA[:len(goodSHA)-1] + string(rune('0'+i))}
	}
	c := New(gh)
	ch := c.CheckPinsStream(context.Background(), pins, 2)

	got := map[int]bool{}
	for ev := range ch {
		if ev.TotalCount != 5 {
			t.Errorf("TotalCount: %d", ev.TotalCount)
		}
		got[ev.Index] = true
	}
	if len(got) != 5 {
		t.Errorf("expected 5 events, got %d: %v", len(got), got)
	}
}

func TestCheckPinsStream_EmptyInput(t *testing.T) {
	c := New(newMockGH())
	ch := c.CheckPinsStream(context.Background(), nil, 4)
	count := 0
	for range ch {
		count++
	}
	if count != 0 {
		t.Errorf("empty input emitted %d events", count)
	}
}

// TestCheckPinsStream_ContextCancelled covers the early `if ctx.Err() != nil`
// return inside the stream goroutine: a pre-cancelled context means the loop
// returns before emitting any event, and the channel is still closed.
func TestCheckPinsStream_ContextCancelled(t *testing.T) {
	gh := newMockGH()
	gh.commits["a/b/"+goodSHA] = makeCommit(goodSHA, time.Now().UTC().Format(time.RFC3339))
	c := New(gh)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before streaming
	ch := c.CheckPinsStream(ctx, []Pin{
		{Owner: "a", Repo: "b", PinnedSHA: goodSHA},
		{Owner: "c", Repo: "d", PinnedSHA: goodSHA},
	}, 1)
	count := 0
	for range ch {
		count++
	}
	// channel must be closed (loop ends), regardless of how many events slipped out
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after context cancellation")
	}
}

func TestCheckPinsStream_ChannelClosed(t *testing.T) {
	gh := newMockGH()
	gh.commits["a/b/"+goodSHA] = makeCommit(goodSHA, time.Now().UTC().Format(time.RFC3339))
	c := New(gh)
	ch := c.CheckPinsStream(context.Background(), []Pin{{Owner: "a", Repo: "b", PinnedSHA: goodSHA}}, 1)
	// Drain
	for range ch {
	}
	// Read after close should return zero immediately
	_, ok := <-ch
	if ok {
		t.Error("channel should be closed")
	}
}
