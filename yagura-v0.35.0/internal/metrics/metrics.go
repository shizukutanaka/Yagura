// Package metrics は Prometheus exposition format で最小限のメトリクスを露出する。
// 外部ライブラリ依存ゼロ、標準ライブラリのみで実装。
//
// 提供する型:
//   - Counter:   単調増加カウンタ(イベント数等)
//   - Gauge:     任意増減可能な値(現在のプロジェクト数等)
//   - Histogram: 値の分布を bucket でまとめる(scan duration 等)
//
// 設計は Mihari v0.11.0 のものを継承(独立パッケージとして再実装、import なし)。
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// DefaultBuckets はレイテンシ・所要時間用の汎用 bucket(秒)。
var DefaultBuckets = []float64{
	0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10,
}

// Registry は全メトリクスの登録と露出を管理する。
type Registry struct {
	mu         sync.RWMutex
	counters   map[string]*Counter
	gauges     map[string]*Gauge
	histograms map[string]*Histogram
}

// NewRegistry は Registry を生成する。
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// Counter は単調増加カウンタ。
type Counter struct {
	name  string
	help  string
	value uint64
}

// Inc は値を 1 加算する。
func (c *Counter) Inc() { atomic.AddUint64(&c.value, 1) }

// Add は任意値を加算する。
func (c *Counter) Add(n uint64) { atomic.AddUint64(&c.value, n) }

// Value は現在値を読む(主にテスト用)。
func (c *Counter) Value() uint64 { return atomic.LoadUint64(&c.value) }

// Gauge は任意増減可能な計測値。
type Gauge struct {
	name  string
	help  string
	value int64
}

// Set は値を上書きする。
func (g *Gauge) Set(v int64) { atomic.StoreInt64(&g.value, v) }

// Inc は 1 加算。
func (g *Gauge) Inc() { atomic.AddInt64(&g.value, 1) }

// Dec は 1 減算。
func (g *Gauge) Dec() { atomic.AddInt64(&g.value, -1) }

// Value は現在値を読む(主にテスト用)。
func (g *Gauge) Value() int64 { return atomic.LoadInt64(&g.value) }

// Histogram は値の分布を bucket カウントで保持する(Prometheus 仕様準拠)。
type Histogram struct {
	name    string
	help    string
	buckets []float64
	counts  []uint64
	infCnt  uint64
	sum     uint64 // milli-units で保持
	count   uint64
}

// Observe は値 v(秒等の浮動小数)を Histogram に記録する。
// NaN/Inf は無視。
func (h *Histogram) Observe(v float64) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	atomic.AddUint64(&h.count, 1)
	atomic.AddUint64(&h.sum, uint64(v*1000))

	for i, ub := range h.buckets {
		if v <= ub {
			atomic.AddUint64(&h.counts[i], 1)
			return
		}
	}
	atomic.AddUint64(&h.infCnt, 1)
}

// NewCounter はカウンタを登録(同名は既存を返す)。
func (r *Registry) NewCounter(name, help string) *Counter {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	c := &Counter{name: name, help: help}
	r.counters[name] = c
	return c
}

// NewGauge はゲージを登録(同名は既存を返す)。
func (r *Registry) NewGauge(name, help string) *Gauge {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g := &Gauge{name: name, help: help}
	r.gauges[name] = g
	return g
}

// NewHistogram は Histogram を登録(同名は既存を返す)。
// buckets が空なら DefaultBuckets を使う。
func (r *Registry) NewHistogram(name, help string, buckets []float64) *Histogram {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.histograms[name]; ok {
		return h
	}
	if len(buckets) == 0 {
		buckets = DefaultBuckets
	}
	sorted := make([]float64, len(buckets))
	copy(sorted, buckets)
	sort.Float64s(sorted)

	h := &Histogram{
		name:    name,
		help:    help,
		buckets: sorted,
		counts:  make([]uint64, len(sorted)),
	}
	r.histograms[name] = h
	return h
}

// ServeHTTP は Prometheus exposition format で全メトリクスを返す。
func (r *Registry) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, c := range r.counters {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n",
			c.name, c.help, c.name, c.name, atomic.LoadUint64(&c.value))
	}
	for _, g := range r.gauges {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n",
			g.name, g.help, g.name, g.name, atomic.LoadInt64(&g.value))
	}
	for _, h := range r.histograms {
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s histogram\n", h.name, h.help, h.name)
		var cumul uint64
		for i, ub := range h.buckets {
			cumul += atomic.LoadUint64(&h.counts[i])
			fmt.Fprintf(w, "%s_bucket{le=\"%g\"} %d\n", h.name, ub, cumul)
		}
		total := cumul + atomic.LoadUint64(&h.infCnt)
		fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", h.name, total)
		fmt.Fprintf(w, "%s_sum %g\n", h.name, float64(atomic.LoadUint64(&h.sum))/1000.0)
		fmt.Fprintf(w, "%s_count %d\n", h.name, total)
	}
}
