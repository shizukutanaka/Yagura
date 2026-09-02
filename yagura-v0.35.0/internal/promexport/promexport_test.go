package promexport

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestRender_SingleCounter(t *testing.T) {
	cs := []Collection{
		{
			Name: "yagura_tool_calls_total",
			Type: "counter",
			Help: "Total MCP tool invocations",
			Samples: []Sample{
				{Labels: map[string]string{"tool": "yagura_list"}, Value: 5},
			},
		},
	}
	var buf bytes.Buffer
	if err := Render(&buf, cs); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "# HELP yagura_tool_calls_total Total MCP tool invocations") {
		t.Errorf("missing HELP: %s", out)
	}
	if !strings.Contains(out, "# TYPE yagura_tool_calls_total counter") {
		t.Errorf("missing TYPE: %s", out)
	}
	if !strings.Contains(out, `yagura_tool_calls_total{tool="yagura_list"} 5`) {
		t.Errorf("missing series: %s", out)
	}
}

func TestRender_NoLabels(t *testing.T) {
	cs := []Collection{
		{Name: "uptime_seconds", Type: "gauge", Samples: []Sample{{Value: 42}}},
	}
	var buf bytes.Buffer
	_ = Render(&buf, cs)
	if !strings.Contains(buf.String(), "uptime_seconds 42") {
		t.Errorf("no-labels: %s", buf.String())
	}
}

func TestRender_DeterministicLabelsOrder(t *testing.T) {
	cs := []Collection{
		{
			Name: "x", Type: "counter",
			Samples: []Sample{
				{Labels: map[string]string{"b": "2", "a": "1"}, Value: 1},
			},
		},
	}
	var buf bytes.Buffer
	_ = Render(&buf, cs)
	// a が b より先
	if !strings.Contains(buf.String(), `x{a="1",b="2"} 1`) {
		t.Errorf("labels order: %s", buf.String())
	}
}

func TestRender_DeterministicSamplesOrder(t *testing.T) {
	cs := []Collection{
		{
			Name: "x", Type: "counter",
			Samples: []Sample{
				{Labels: map[string]string{"tool": "z"}, Value: 3},
				{Labels: map[string]string{"tool": "a"}, Value: 1},
				{Labels: map[string]string{"tool": "m"}, Value: 2},
			},
		},
	}
	var buf bytes.Buffer
	_ = Render(&buf, cs)
	// a, m, z の順
	out := buf.String()
	posA := strings.Index(out, `tool="a"`)
	posM := strings.Index(out, `tool="m"`)
	posZ := strings.Index(out, `tool="z"`)
	if !(posA < posM && posM < posZ) {
		t.Errorf("sample order broken: %s", out)
	}
}

func TestEscapeLabelValue(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`abc`, `abc`},
		{`a"b`, `a\"b`},
		{`a\b`, `a\\b`},
		{"a\nb", `a\nb`},
		{`a\"b`, `a\\\"b`},
	}
	for _, c := range cases {
		if got := escapeLabelValue(c.in); got != c.want {
			t.Errorf("escape(%q): got %q want %q", c.in, got, c.want)
		}
	}
}

func TestFormatFloat_IntegerNoDecimal(t *testing.T) {
	if formatFloat(5) != "5" {
		t.Errorf("integer should not have decimal")
	}
}

func TestFormatFloat_Fractional(t *testing.T) {
	if !strings.Contains(formatFloat(1.5), ".") {
		t.Errorf("fractional should have decimal")
	}
}

func TestRender_FloatValueFormatted(t *testing.T) {
	cs := []Collection{
		{Name: "ratio", Type: "gauge", Samples: []Sample{{Value: 0.75}}},
	}
	var buf bytes.Buffer
	_ = Render(&buf, cs)
	if !strings.Contains(buf.String(), "ratio 0.75") {
		t.Errorf("fractional: %s", buf.String())
	}
}

func TestRender_CollectionsNameSorted(t *testing.T) {
	cs := []Collection{
		{Name: "z_metric", Type: "counter", Samples: []Sample{{Value: 1}}},
		{Name: "a_metric", Type: "counter", Samples: []Sample{{Value: 2}}},
	}
	var buf bytes.Buffer
	_ = Render(&buf, cs)
	out := buf.String()
	if strings.Index(out, "a_metric") > strings.Index(out, "z_metric") {
		t.Errorf("collection order: %s", out)
	}
}

func TestRender_LabelValueWithSpecialChars(t *testing.T) {
	cs := []Collection{
		{Name: "x", Type: "counter", Samples: []Sample{
			{Labels: map[string]string{"path": `/tmp/"quoted"\back`}, Value: 1},
		}},
	}
	var buf bytes.Buffer
	_ = Render(&buf, cs)
	out := buf.String()
	if !strings.Contains(out, `path="/tmp/\"quoted\"\\back"`) {
		t.Errorf("escape failure: %s", out)
	}
}

// ─── formatFloat special values ──────────────────────────────

func TestFormatFloat_NaN(t *testing.T) {
	if got := formatFloat(math.NaN()); got != "NaN" {
		t.Errorf("formatFloat(NaN) = %q, want NaN", got)
	}
}

func TestFormatFloat_PosInf(t *testing.T) {
	if got := formatFloat(math.Inf(1)); got != "+Inf" {
		t.Errorf("formatFloat(+Inf) = %q, want +Inf", got)
	}
}

func TestFormatFloat_NegInf(t *testing.T) {
	if got := formatFloat(math.Inf(-1)); got != "-Inf" {
		t.Errorf("formatFloat(-Inf) = %q, want -Inf", got)
	}
}

// ─── labelsKey with empty labels ──────────────────────────────

func TestRender_NoLabelsKey(t *testing.T) {
	// A sample with an empty Labels map exercises the labelsKey("") path.
	cs := []Collection{{
		Name: "no_labels_metric",
		Help: "metric with no labels",
		Type: "gauge",
		Samples: []Sample{{
			Labels: map[string]string{},
			Value:  42,
		}},
	}}
	var buf bytes.Buffer
	if err := Render(&buf, cs); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no_labels_metric 42") {
		t.Errorf("expected metric line without labels, got: %s", out)
	}
}

// TestLabelsKey_NilMap calls labelsKey directly to cover the len==0 early-return
// branch, which sort.Slice never invokes when a collection has only one sample.
func TestLabelsKey_NilMap(t *testing.T) {
	if got := labelsKey(nil); got != "" {
		t.Errorf("labelsKey(nil) = %q, want empty string", got)
	}
	if got := labelsKey(map[string]string{}); got != "" {
		t.Errorf("labelsKey({}) = %q, want empty string", got)
	}
}

// ─── Render write-error paths ─────────────────────────────────

// errAfterN fails after writing N bytes successfully.
type errAfterN struct {
	written int
	limit   int
}

func (e *errAfterN) Write(p []byte) (int, error) {
	if e.written >= e.limit {
		return 0, errors.New("forced write error")
	}
	n := len(p)
	if e.written+n > e.limit {
		n = e.limit - e.written
	}
	e.written += n
	return n, nil
}

func TestRender_HELPWriteError(t *testing.T) {
	cs := []Collection{{Name: "x", Help: "desc", Type: "counter", Samples: []Sample{{Value: 1}}}}
	w := &errAfterN{limit: 0} // fail immediately on first byte
	if err := Render(w, cs); err == nil {
		t.Error("expected error when HELP write fails")
	}
}

func TestRender_TYPEWriteError(t *testing.T) {
	// Let HELP succeed, fail on TYPE (second fmt.Fprintf call).
	cs := []Collection{{Name: "x", Help: "h", Type: "counter", Samples: []Sample{{Value: 1}}}}
	// HELP line is "# HELP x h\n" (12 bytes). Allow 12, fail on next write.
	helpLine := "# HELP x h\n"
	w := &errAfterN{limit: len(helpLine)}
	if err := Render(w, cs); err == nil {
		t.Error("expected error when TYPE write fails")
	}
}

func TestRender_SampleWriteError(t *testing.T) {
	// Let HELP+TYPE succeed, fail on the sample line (third write).
	cs := []Collection{{Name: "x", Help: "h", Type: "counter", Samples: []Sample{{Value: 1}}}}
	helpLine := "# HELP x h\n"
	typeLine := "# TYPE x counter\n"
	w := &errAfterN{limit: len(helpLine) + len(typeLine)}
	if err := Render(w, cs); err == nil {
		t.Error("expected error when sample write fails")
	}
}
