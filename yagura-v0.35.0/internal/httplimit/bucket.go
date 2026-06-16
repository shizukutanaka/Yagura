// Package httplimit implements per-key token bucket rate limiting for HTTP.
//
// 動機 (v0.12.0):
//   v0.11 で追加した /sbom, /gha-audit, /pin-drift endpoint は認証以外の
//   保護が無い。悪意ある(or 単に bug の)client が連打した場合:
//     - /pin-drift 連打で GitHub PAT の rate limit を消費し、scanner も巻き
//       添えで失敗(blast radius が portfolio 全体)
//     - /gha-audit に 5MB body を毎秒投げて CPU を喰い潰す
//     - /sbom 連打で I/O 圧迫
//
// 解決:
//   標準ライブラリのみで token bucket を実装(ADR-0001 維持)。golang.org/x/time/rate
//   は使わない。
//
// 設計:
//   - per-key の bucket map(キーは IP or Auth token、route で異なる戦略)
//   - 各 bucket は capacity と refill rate を持つ
//   - 古い未使用 bucket は GC される(LRU っぽく lastSeen で expire)
//   - middleware として http.HandlerFunc を wrap
package httplimit

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// Bucket は単一 key 用の token bucket。
type Bucket struct {
	capacity   float64
	refillRate float64 // tokens per second

	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

// allow は 1 token を消費しようとし、成功なら true。
func (b *Bucket) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	// refill
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * b.refillRate
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.lastRefill = now
	}
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ─── Limiter ────────────────────────────────────────────────

// Limiter は per-key の bucket 集合。
type Limiter struct {
	capacity   float64
	refillRate float64
	keyFn      func(*http.Request) string
	idleTTL    time.Duration

	mu      sync.RWMutex
	buckets map[string]*Bucket
	now     func() time.Time
}

// Options は Limiter の構築パラメータ。
type Options struct {
	// Capacity はバケットの最大トークン数(burst 許容量)
	Capacity int
	// RefillPerMinute は 1 分あたりの補充量(平均 throughput)
	RefillPerMinute float64
	// KeyFn は request → key(デフォルトは IP)
	KeyFn func(*http.Request) string
	// IdleTTL を超えて未使用の bucket は GC 対象(デフォルト 10 分)
	IdleTTL time.Duration
}

// New は Options から Limiter を生成する。
func New(opts Options) *Limiter {
	if opts.Capacity <= 0 {
		opts.Capacity = 10
	}
	if opts.RefillPerMinute <= 0 {
		opts.RefillPerMinute = 30
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 10 * time.Minute
	}
	if opts.KeyFn == nil {
		opts.KeyFn = remoteAddrKey
	}
	return &Limiter{
		capacity:   float64(opts.Capacity),
		refillRate: opts.RefillPerMinute / 60.0,
		keyFn:      opts.KeyFn,
		idleTTL:    opts.IdleTTL,
		buckets:    map[string]*Bucket{},
		now:        time.Now,
	}
}

// Allow は request を許可するか判定する(主に test 用、middleware が利用)。
func (l *Limiter) Allow(r *http.Request) bool {
	key := l.keyFn(r)
	if key == "" {
		return true // key が取れない場合は通す(filter 不能)
	}
	now := l.now()
	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		b = &Bucket{
			capacity:   l.capacity,
			refillRate: l.refillRate,
			tokens:     l.capacity, // 初期 full
			lastRefill: now,
		}
		l.buckets[key] = b
	}
	l.mu.Unlock()
	return b.allow(now)
}

// Middleware は handler を wrap して rate-limited にする。
func (l *Limiter) Middleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(r) {
			// Retry-After を計算(残量から推定)
			retryAfter := int((1.0 / l.refillRate) + 0.5)
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", strconvI(retryAfter))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limit exceeded"}`))
			return
		}
		next(w, r)
	}
}

// GC は idle bucket を削除する(daemon が定期的に呼ぶ)。
func (l *Limiter) GC() int {
	now := l.now()
	cutoff := now.Add(-l.idleTTL)
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, b := range l.buckets {
		b.mu.Lock()
		idle := b.lastSeen.Before(cutoff)
		b.mu.Unlock()
		if idle {
			delete(l.buckets, k)
			n++
		}
	}
	return n
}

// ActiveCount は現在保持中の bucket 数を返す(metrics 用)。
func (l *Limiter) ActiveCount() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.buckets)
}

// ─── KeyFn helpers ──────────────────────────────────────────

// remoteAddrKey は RemoteAddr の host 部分を返す(IP 単位の制限)。
// X-Forwarded-For があれば最初の IP を採用(proxy 後ろ想定)。
func remoteAddrKey(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	addr := r.RemoteAddr
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}

// TokenKey は Authorization: Bearer の token 部分を返す(API token 単位の制限)。
// 認証なし request では "anonymous" key にまとめる。
func TokenKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return "tok:" + auth[7:]
	}
	return "anonymous"
}

// ─── helpers (zero-dep) ──────────────────────────────────────

func strconvI(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
