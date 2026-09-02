// metricsdoc_test.go: every Prometheus metric the daemon can emit must be
// documented in docs/METRICS.md. Metric names are defined as string literals in
// main.go — either registered via mreg.NewCounter/NewGauge or named in a
// promexport.Collection (collectYaguraMetrics). This guard extracts those exact
// definition sites and fails the build if any name is missing from the doc, so
// adding a metric without documenting it can't slip through ("逸脱を物理的に潰す").
package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func definedMetricNames(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	// metric definition sites only — NOT every "yagura_*" literal (audit record
	// Kinds like "yagura_started" are not metrics).
	res := []*regexp.Regexp{
		regexp.MustCompile(`mreg\.New(?:Counter|Gauge)\("(yagura_[a-z_]+)"`),
		regexp.MustCompile(`Name: *"(yagura_[a-z_]+)"`),
	}
	seen := map[string]bool{}
	for _, re := range res {
		for _, m := range re.FindAllSubmatch(src, -1) {
			seen[string(m[1])] = true
		}
	}
	var names []string
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func TestMetricsDoc_AllMetricsDocumented(t *testing.T) {
	names := definedMetricNames(t)
	if len(names) == 0 {
		t.Fatal("found no metric definitions in main.go — guard regex is stale")
	}
	doc, err := os.ReadFile("../../docs/METRICS.md")
	if err != nil {
		t.Fatalf("read docs/METRICS.md: %v", err)
	}
	body := string(doc)
	for _, n := range names {
		// documented as `yagura_xxx` in a table row
		if !strings.Contains(body, "`"+n+"`") {
			t.Errorf("metric %q is emitted by the daemon but not documented in docs/METRICS.md", n)
		}
	}
}
