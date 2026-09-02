package metrics

import (
	"math"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestCounter_BasicOps(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("c", "x")
	c.Inc()
	c.Inc()
	c.Add(5)
	if got := c.Value(); got != 7 {
		t.Errorf("expected 7, got %d", got)
	}
}

func TestCounter_DuplicateRegistration(t *testing.T) {
	r := NewRegistry()
	c1 := r.NewCounter("dup", "x")
	c2 := r.NewCounter("dup", "y")
	if c1 != c2 {
		t.Error("duplicate registration should return same Counter")
	}
}

func TestGauge_SetIncDec(t *testing.T) {
	r := NewRegistry()
	g := r.NewGauge("g", "x")
	g.Set(10)
	g.Inc()
	g.Dec()
	g.Dec()
	if v := g.Value(); v != 9 {
		t.Errorf("expected 9, got %d", v)
	}
}

func TestRegistry_ServeHTTP(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("yagura_test_total", "test counter")
	c.Add(3)
	g := r.NewGauge("yagura_test_g", "test gauge")
	g.Set(42)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, nil)
	body := w.Body.String()

	for _, want := range []string{
		"# HELP yagura_test_total test counter",
		"# TYPE yagura_test_total counter",
		"yagura_test_total 3",
		"# HELP yagura_test_g test gauge",
		"yagura_test_g 42",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestHistogram_Basic(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogram("h", "x", []float64{0.1, 0.5, 1.0})
	h.Observe(0.05) // bucket 0
	h.Observe(0.3)  // bucket 1
	h.Observe(2.0)  // +Inf

	w := httptest.NewRecorder()
	r.ServeHTTP(w, nil)
	body := w.Body.String()

	for _, want := range []string{
		`h_bucket{le="0.1"} 1`,
		`h_bucket{le="0.5"} 2`,
		`h_bucket{le="1"} 2`,
		`h_bucket{le="+Inf"} 3`,
		`h_count 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestHistogram_DefaultBuckets(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogram("def_h", "x", nil)
	if len(h.buckets) != len(DefaultBuckets) {
		t.Errorf("expected default buckets length")
	}
}

func TestHistogram_UnsortedBucketsAreSorted(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogram("unsorted", "x", []float64{5.0, 0.1, 1.0})
	if h.buckets[0] != 0.1 || h.buckets[2] != 5.0 {
		t.Errorf("buckets should be sorted: %v", h.buckets)
	}
}

func TestHistogram_NaNAndInfIgnored(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogram("nan_h", "x", []float64{1.0})
	h.Observe(math.NaN())
	h.Observe(math.Inf(1))
	h.Observe(0.5) // valid
	w := httptest.NewRecorder()
	r.ServeHTTP(w, nil)
	if !strings.Contains(w.Body.String(), "nan_h_count 1") {
		t.Errorf("only valid observation should count:\n%s", w.Body.String())
	}
}

func TestConcurrent_CounterInc(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("conc", "x")
	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	if v := c.Value(); v != N {
		t.Errorf("expected %d, got %d", N, v)
	}
}
