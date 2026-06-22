package regress

import "testing"

func hasReg(r Report, fn, metric string) (Regression, bool) {
	for _, x := range r.Regressions {
		if x.Func == fn && x.Metric == metric {
			return x, true
		}
	}
	return Regression{}, false
}

// ─── complexity regression ───────────────────────────────

func TestCompare_ComplexityIncreaseFlagged(t *testing.T) {
	old := map[string]string{"x.go": `package p
func F(a int) {
}
`}
	newer := map[string]string{"x.go": `package p
func F(a int) {
	if a > 0 {
	}
	if a > 1 {
	}
}
`}
	r := Compare(old, newer)
	reg, ok := hasReg(r, "F", "complexity")
	if !ok {
		t.Fatalf("F complexity regression expected, got: %+v", r.Regressions)
	}
	if reg.Old != 1 || reg.New != 3 || reg.Delta != 2 {
		t.Errorf("want old1/new3/delta2, got old%d/new%d/delta%d", reg.Old, reg.New, reg.Delta)
	}
}

func TestCompare_NoChangeNoRegression(t *testing.T) {
	src := map[string]string{"x.go": `package p
func F(a int) int { return a }
`}
	r := Compare(src, src)
	if len(r.Regressions) != 0 {
		t.Errorf("identical input should have no regressions, got: %+v", r.Regressions)
	}
}

func TestCompare_ImprovementNotFlagged(t *testing.T) {
	old := map[string]string{"x.go": `package p
func F(a int) {
	if a > 0 {
	}
	if a > 1 {
	}
}
`}
	newer := map[string]string{"x.go": `package p
func F(a int) {
}
`}
	r := Compare(old, newer)
	if len(r.Regressions) != 0 {
		t.Errorf("complexity decrease must NOT be a regression, got: %+v", r.Regressions)
	}
}

// ─── params / returns regression ─────────────────────────

func TestCompare_ParamsIncreaseFlagged(t *testing.T) {
	old := map[string]string{"x.go": "package p\nfunc F(a int) {}\n"}
	newer := map[string]string{"x.go": "package p\nfunc F(a, b, c int) {}\n"}
	r := Compare(old, newer)
	reg, ok := hasReg(r, "F", "params")
	if !ok || reg.Old != 1 || reg.New != 3 {
		t.Errorf("params regression 1->3 expected, got: %+v", r.Regressions)
	}
}

func TestCompare_ReturnsIncreaseFlagged(t *testing.T) {
	old := map[string]string{"x.go": "package p\nfunc F() int { return 0 }\n"}
	newer := map[string]string{"x.go": "package p\nfunc F() (int, error) { return 0, nil }\n"}
	r := Compare(old, newer)
	if _, ok := hasReg(r, "F", "returns"); !ok {
		t.Errorf("returns regression 1->2 expected, got: %+v", r.Regressions)
	}
}

// ─── Crossed flag (over the conventional gate) ───────────

func TestCompare_CrossedWhenOverGate(t *testing.T) {
	// params 5 -> 6 crosses the param-check default (5).
	old := map[string]string{"x.go": "package p\nfunc F(a, b, c, d, e int) {}\n"}
	newer := map[string]string{"x.go": "package p\nfunc F(a, b, c, d, e, f int) {}\n"}
	r := Compare(old, newer)
	reg, ok := hasReg(r, "F", "params")
	if !ok {
		t.Fatalf("expected params regression, got: %+v", r.Regressions)
	}
	if !reg.Crossed {
		t.Errorf("6 params is over the gate (5) — Crossed should be true: %+v", reg)
	}
	if r.Crossed != 1 {
		t.Errorf("Report.Crossed count: want 1, got %d", r.Crossed)
	}
}

func TestCompare_NotCrossedWhenUnderGate(t *testing.T) {
	// params 1 -> 2 increases but stays well under gate (5).
	old := map[string]string{"x.go": "package p\nfunc F(a int) {}\n"}
	newer := map[string]string{"x.go": "package p\nfunc F(a, b int) {}\n"}
	r := Compare(old, newer)
	reg, ok := hasReg(r, "F", "params")
	if !ok {
		t.Fatalf("expected params regression, got: %+v", r.Regressions)
	}
	if reg.Crossed {
		t.Errorf("2 params is under the gate — Crossed should be false: %+v", reg)
	}
	if r.Crossed != 0 {
		t.Errorf("Report.Crossed: want 0, got %d", r.Crossed)
	}
}

// ─── new / removed functions ─────────────────────────────

func TestCompare_NewFunctionNotRegression(t *testing.T) {
	old := map[string]string{"x.go": "package p\nfunc F() {}\n"}
	newer := map[string]string{"x.go": "package p\nfunc F() {}\nfunc G(a, b, c, d, e, f int) {}\n"}
	r := Compare(old, newer)
	// G is new — not a regression of an existing function.
	if _, ok := hasReg(r, "G", "params"); ok {
		t.Errorf("new function G must not count as a regression, got: %+v", r.Regressions)
	}
}

func TestCompare_RemovedFunctionIgnored(t *testing.T) {
	old := map[string]string{"x.go": "package p\nfunc F() {}\nfunc G(a, b, c int) {}\n"}
	newer := map[string]string{"x.go": "package p\nfunc F() {}\n"}
	r := Compare(old, newer)
	if len(r.Regressions) != 0 {
		t.Errorf("removing a function is not a regression, got: %+v", r.Regressions)
	}
}

// ─── method matching by (Recv).Method ────────────────────

func TestCompare_MethodMatchedByQualifiedName(t *testing.T) {
	old := map[string]string{"x.go": `package p
type T struct{}
func (t T) M(a int) {}
`}
	newer := map[string]string{"x.go": `package p
type T struct{}
func (t T) M(a, b, c int) {}
`}
	r := Compare(old, newer)
	if _, ok := hasReg(r, "(T).M", "params"); !ok {
		t.Errorf("method (T).M params regression expected, got: %+v", r.Regressions)
	}
}

// Same func name in different files must not cross-match.
func TestCompare_SameNameDifferentFileNotMatched(t *testing.T) {
	old := map[string]string{"a.go": "package p\nfunc F(x int) {}\n"}
	newer := map[string]string{"b.go": "package p\nfunc F(x, y, z int) {}\n"}
	r := Compare(old, newer)
	if len(r.Regressions) != 0 {
		t.Errorf("F in a.go vs b.go are different functions; no regression, got: %+v", r.Regressions)
	}
}

// ─── summary + determinism ───────────────────────────────

func TestCompare_Counts(t *testing.T) {
	old := map[string]string{"x.go": "package p\nfunc F() {}\nfunc G() {}\n"}
	newer := map[string]string{"x.go": "package p\nfunc F() {}\nfunc G() {}\nfunc H() {}\n"}
	r := Compare(old, newer)
	if r.OldFuncs != 2 || r.NewFuncs != 3 {
		t.Errorf("OldFuncs/NewFuncs: want 2/3, got %d/%d", r.OldFuncs, r.NewFuncs)
	}
}

func TestCompare_MultiMetricRegressionEachReported(t *testing.T) {
	old := map[string]string{"x.go": "package p\nfunc F(a int) int { return a }\n"}
	newer := map[string]string{"x.go": `package p
func F(a, b int) (int, error) {
	if a > 0 {
	}
	return a, nil
}
`}
	r := Compare(old, newer)
	// params 1->2, returns 1->2, complexity 1->2, lines up — at least 3 regressions for F.
	count := 0
	for _, x := range r.Regressions {
		if x.Func == "F" {
			count++
		}
	}
	if count < 3 {
		t.Errorf("expected >=3 metric regressions for F, got %d: %+v", count, r.Regressions)
	}
}

func TestCompare_Deterministic(t *testing.T) {
	old := map[string]string{"x.go": "package p\nfunc F(a int) {}\nfunc G(a int) {}\n"}
	newer := map[string]string{"x.go": "package p\nfunc F(a, b, c int) {}\nfunc G(a, b int) {}\n"}
	a := Compare(old, newer)
	b := Compare(old, newer)
	if len(a.Regressions) != len(b.Regressions) {
		t.Fatalf("non-deterministic count")
	}
	for i := range a.Regressions {
		if a.Regressions[i] != b.Regressions[i] {
			t.Errorf("regression %d differs: %+v vs %+v", i, a.Regressions[i], b.Regressions[i])
		}
	}
}

func TestCompare_SortedByDeltaDesc(t *testing.T) {
	old := map[string]string{"x.go": "package p\nfunc Small(a int) {}\nfunc Big(a int) {}\n"}
	newer := map[string]string{"x.go": "package p\nfunc Small(a, b int) {}\nfunc Big(a, b, c, d, e int) {}\n"}
	r := Compare(old, newer)
	if len(r.Regressions) < 2 {
		t.Fatalf("expected >=2 regressions, got %+v", r.Regressions)
	}
	// First should have the larger delta (Big: 1->5 delta 4 > Small: 1->2 delta 1).
	if r.Regressions[0].Delta < r.Regressions[1].Delta {
		t.Errorf("regressions not sorted by delta desc: %+v", r.Regressions)
	}
}

func TestCompare_EmptyInputs(t *testing.T) {
	r := Compare(map[string]string{}, map[string]string{})
	if r.OldFuncs != 0 || r.NewFuncs != 0 || len(r.Regressions) != 0 {
		t.Errorf("empty inputs should be empty report, got %+v", r)
	}
}
