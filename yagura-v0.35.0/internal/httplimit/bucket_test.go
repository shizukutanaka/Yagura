package httplimit

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBucket_AllowAndRefill(t *testing.T) {
	now := time.Now()
	b := &Bucket{capacity: 3, refillRate: 1, tokens: 3, lastRefill: now}

	// 初期 3 tokens consume 3 → ok
	for i := 0; i < 3; i++ {
		if !b.allow(now) {
			t.Errorf("token %d should be allowed", i+1)
		}
	}
	// 4th → denied
	if b.allow(now) {
		t.Error("4th request should be denied")
	}
	// 1 秒経過 → 1 token refilled
	if !b.allow(now.Add(1 * time.Second)) {
		t.Error("1s later should be allowed (1 token refilled)")
	}
	// 直後 → again denied
	if b.allow(now.Add(1 * time.Second)) {
		t.Error("immediate next should be denied")
	}
}

func TestBucket_RefillCappedAtCapacity(t *testing.T) {
	now := time.Now()
	b := &Bucket{capacity: 3, refillRate: 1, tokens: 0, lastRefill: now}
	// 100 秒経過 → 100 tokens refilled, but capped at 3
	b.allow(now.Add(100 * time.Second))
	// tokens should be capped → at most 2 left (1 consumed)
	if b.tokens > 2.5 {
		t.Errorf("tokens should be capped, got %v", b.tokens)
	}
}

// ─── Limiter ────────────────────────────────────────────────

func TestNew_DefaultsApplied(t *testing.T) {
	l := New(Options{})
	if l.capacity != 10 {
		t.Errorf("default capacity: %v", l.capacity)
	}
	if l.refillRate != 0.5 { // 30/min ÷ 60 = 0.5/sec
		t.Errorf("default refill: %v", l.refillRate)
	}
}

func TestLimiter_AllowsFirstNRequests(t *testing.T) {
	l := New(Options{Capacity: 5, RefillPerMinute: 60})
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "1.2.3.4:5678"
	for i := 0; i < 5; i++ {
		if !l.Allow(req) {
			t.Errorf("request %d should be allowed", i+1)
		}
	}
	if l.Allow(req) {
		t.Error("6th request should be denied")
	}
}

func TestLimiter_DifferentKeysIndependent(t *testing.T) {
	l := New(Options{Capacity: 2, RefillPerMinute: 60})
	a := httptest.NewRequest("GET", "/x", nil)
	a.RemoteAddr = "1.1.1.1:1"
	b := httptest.NewRequest("GET", "/x", nil)
	b.RemoteAddr = "2.2.2.2:2"
	for i := 0; i < 2; i++ {
		if !l.Allow(a) || !l.Allow(b) {
			t.Errorf("both keys should be allowed on iter %d", i)
		}
	}
	if l.Allow(a) || l.Allow(b) {
		t.Error("both should now be denied")
	}
}

func TestLimiter_Middleware_Returns429(t *testing.T) {
	l := New(Options{Capacity: 1, RefillPerMinute: 1})
	called := 0
	h := l.Middleware(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(200)
	})

	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "9.9.9.9:1"

	// 1st: allowed
	w1 := httptest.NewRecorder()
	h(w1, req)
	if w1.Code != 200 {
		t.Errorf("1st: got %d", w1.Code)
	}
	// 2nd: 429
	w2 := httptest.NewRecorder()
	h(w2, req)
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("2nd: got %d", w2.Code)
	}
	if w2.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header missing")
	}
	if called != 1 {
		t.Errorf("handler called %d times, want 1", called)
	}
}

// ─── KeyFn helpers ──────────────────────────────────────────

func TestRemoteAddrKey(t *testing.T) {
	cases := []struct {
		name, addr, xff, want string
	}{
		{"plain", "1.2.3.4:5678", "", "1.2.3.4"},
		{"xff single", "10.0.0.1:80", "203.0.113.5", "203.0.113.5"},
		{"xff chain", "10.0.0.1:80", "203.0.113.5, 10.0.0.99", "203.0.113.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = c.addr
			if c.xff != "" {
				req.Header.Set("X-Forwarded-For", c.xff)
			}
			if got := remoteAddrKey(req); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestTokenKey(t *testing.T) {
	cases := map[string]string{
		"Bearer ghp_abc123": "tok:ghp_abc123",
		"":                  "anonymous",
		"Basic xyz":         "anonymous",
	}
	for in, want := range cases {
		req := httptest.NewRequest("GET", "/", nil)
		if in != "" {
			req.Header.Set("Authorization", in)
		}
		if got := TokenKey(req); got != want {
			t.Errorf("TokenKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// ─── GC ─────────────────────────────────────────────────────

func TestLimiter_GC(t *testing.T) {
	l := New(Options{Capacity: 1, RefillPerMinute: 1, IdleTTL: 100 * time.Millisecond})

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.1.1.1:1"
	l.Allow(req)
	if l.ActiveCount() != 1 {
		t.Fatalf("active: %d", l.ActiveCount())
	}

	// fake clock advance
	l.now = func() time.Time { return time.Now().Add(1 * time.Hour) }
	removed := l.GC()
	if removed != 1 {
		t.Errorf("GC removed %d, want 1", removed)
	}
	if l.ActiveCount() != 0 {
		t.Errorf("after GC: %d", l.ActiveCount())
	}
}

// ─── concurrency stress ────────────────────────────────────

func TestLimiter_Concurrent(t *testing.T) {
	l := New(Options{Capacity: 100, RefillPerMinute: 6000})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/x", nil)
			req.RemoteAddr = "1.1.1.1:1"
			for j := 0; j < 5; j++ {
				_ = l.Allow(req)
			}
		}()
	}
	wg.Wait()
	// no race or panic = pass
}

func TestStrconvI(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 42: "42", -5: "-5", 1000: "1000"}
	for in, want := range cases {
		if got := strconvI(in); got != want {
			t.Errorf("strconvI(%d) = %q, want %q", in, got, want)
		}
	}
}
