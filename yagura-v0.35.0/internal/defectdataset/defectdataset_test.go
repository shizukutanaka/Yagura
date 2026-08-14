package defectdataset

import (
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/churn"
)

func at(day int) time.Time {
	return time.Date(2026, 1, day, 12, 0, 0, 0, time.UTC)
}

// commit builds a commit on the given day touching paths with +10/-2 each.
func commit(day int, subject string, paths ...string) churn.Commit {
	c := churn.Commit{
		When: at(day), Subject: subject,
		Author: "Dev", Email: "dev@x",
	}
	for _, p := range paths {
		c.Files = append(c.Files, churn.FileChange{Path: p, Added: 10, Deleted: 2})
	}
	return c
}

func sizes(paths ...string) map[string]int {
	m := map[string]int{}
	for _, p := range paths {
		m[p] = 100
	}
	return m
}

func rowFor(t *testing.T, d Dataset, path string) Row {
	t.Helper()
	for _, r := range d.Rows {
		if r.Path == path {
			return r
		}
	}
	t.Fatalf("row %q not in dataset (%d rows)", path, len(d.Rows))
	return Row{}
}

// TestBuild_LabelsComeOnlyFromTheLabelWindow is the central methodological
// guard. Zimmermann/Premraj/Zeller's Eclipse dataset pairs PRE-release metrics
// with POST-release defects for a reason: drawing features and labels from the
// same window yields a dataset that "predicts" the past. Here, early commits
// form the feature window and later commits the label window, so a fix that
// happens only in the early window must NOT become a label.
func TestBuild_LabelsComeOnlyFromTheLabelWindow(t *testing.T) {
	commits := []churn.Commit{
		// --- feature window (early) ---
		commit(1, "feat: add a", "a.go"),
		commit(2, "fix: early bug in a", "a.go"), // fix, but BEFORE the split
		commit(3, "feat: add b", "b.go"),
		commit(4, "feat: more b", "b.go"),
		// --- label window (late) ---
		commit(5, "fix: late bug in b", "b.go"), // this is the only label-window fix
		commit(6, "feat: unrelated", "b.go"),
	}
	d := Build(commits, sizes("a.go", "b.go"), nil, Options{SplitRatio: 0.66})

	a := rowFor(t, d, "a.go")
	if a.FixCount != 0 || a.Fixed {
		t.Errorf("a.go was fixed only in the FEATURE window; it must not be labelled defective (got fix_count=%d fixed=%v)",
			a.FixCount, a.Fixed)
	}
	b := rowFor(t, d, "b.go")
	if b.FixCount != 1 || !b.Fixed {
		t.Errorf("b.go was fixed in the LABEL window; want fix_count=1 fixed=true, got %d/%v", b.FixCount, b.Fixed)
	}
	if d.Meta.Leakage {
		t.Errorf("a temporally split dataset must not be flagged as leaking")
	}
}

// TestBuild_FeaturesComeOnlyFromTheFeatureWindow: churn accumulated after the
// split must not inflate the features, or the model would be shown the future.
func TestBuild_FeaturesComeOnlyFromTheFeatureWindow(t *testing.T) {
	commits := []churn.Commit{
		commit(1, "feat: a", "a.go"), // feature window: 1 change
		commit(2, "feat: a again", "a.go"),
		commit(3, "feat: a x3", "a.go"),
		// label window — heavy churn that must be invisible to the features
		commit(4, "feat: a x4", "a.go"),
		commit(5, "feat: a x5", "a.go"),
		commit(6, "feat: a x6", "a.go"),
	}
	d := Build(commits, sizes("a.go"), nil, Options{SplitRatio: 0.5})
	a := rowFor(t, d, "a.go")
	if a.ChurnCount != 3 {
		t.Errorf("features must cover the feature window only: churn_count = %d, want 3", a.ChurnCount)
	}
}

// TestBuild_NoSplitIsMarkedAsLeaking: opting out of the temporal split is
// allowed, but the dataset must carry the admission in its own metadata so a
// downstream consumer cannot mistake it for a clean one.
func TestBuild_NoSplitIsMarkedAsLeaking(t *testing.T) {
	commits := []churn.Commit{
		commit(1, "feat: a", "a.go"),
		commit(2, "fix: a", "a.go"),
	}
	d := Build(commits, sizes("a.go"), nil, Options{SplitRatio: 0})
	if !d.Meta.Leakage {
		t.Fatal("SplitRatio=0 draws features and labels from the same commits; Meta.Leakage must be true")
	}
	if !strings.Contains(strings.ToLower(d.Meta.Note), "leak") {
		t.Errorf("the leakage warning must be stated in the note, got %q", d.Meta.Note)
	}
	// with no split the fix IS visible as a label
	if a := rowFor(t, d, "a.go"); a.FixCount != 1 {
		t.Errorf("unsplit dataset should label the fix, got %d", a.FixCount)
	}
}

func TestBuild_MetaDescribesBothWindows(t *testing.T) {
	commits := []churn.Commit{
		commit(1, "feat: a", "a.go"),
		commit(2, "feat: a", "a.go"),
		commit(5, "fix: a", "a.go"),
	}
	d := Build(commits, sizes("a.go"), nil, Options{SplitRatio: 0.66})
	if d.Meta.FeatureCommits != 2 || d.Meta.LabelCommits != 1 {
		t.Errorf("window commit counts wrong: feature=%d label=%d", d.Meta.FeatureCommits, d.Meta.LabelCommits)
	}
	if d.Meta.FeatureEnd.After(d.Meta.LabelStart) {
		t.Errorf("windows must not overlap: featureEnd=%v labelStart=%v", d.Meta.FeatureEnd, d.Meta.LabelStart)
	}
	if d.Meta.SplitRatio != 0.66 {
		t.Errorf("SplitRatio not recorded: %v", d.Meta.SplitRatio)
	}
	if d.Meta.FormatVersion == "" || d.Meta.Note == "" {
		t.Errorf("meta must carry a format version and a provenance note")
	}
}

// TestBuild_RowsAreDeterministic pins path-ascending order so two runs produce
// byte-identical CSV (required for diffing datasets across time).
func TestBuild_RowsAreDeterministic(t *testing.T) {
	commits := []churn.Commit{commit(1, "feat", "z.go", "a.go", "m.go"), commit(5, "fix: x", "a.go")}
	d := Build(commits, sizes("z.go", "a.go", "m.go"), nil, Options{SplitRatio: 0.5})
	var got []string
	for _, r := range d.Rows {
		got = append(got, r.Path)
	}
	want := []string{"a.go", "m.go", "z.go"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("rows must be path-ascending: got %v want %v", got, want)
		}
	}
}

// TestCSV_HasHeaderAndOneRowPerFile checks the PROMISE-style shape: one row per
// file, metric columns plus the label columns last.
func TestCSV_HasHeaderAndOneRowPerFile(t *testing.T) {
	commits := []churn.Commit{commit(1, "feat", "a.go"), commit(5, "fix: a", "a.go")}
	d := Build(commits, sizes("a.go"), map[string]int{"a.go": 7}, Options{SplitRatio: 0.5})
	csv := d.CSV()
	lines := strings.Split(strings.TrimSpace(csv), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected header + 1 row, got %d lines:\n%s", len(lines), csv)
	}
	header := lines[0]
	for _, col := range []string{"path", "relative_churn", "churn_count", "ownership", "complexity", "fix_count", "fixed"} {
		if !strings.Contains(header, col) {
			t.Errorf("CSV header missing %q: %s", col, header)
		}
	}
	// label columns must be last (convention: features then label)
	if !strings.HasSuffix(header, "fix_count,fixed") {
		t.Errorf("label columns must come last, header = %q", header)
	}
	if !strings.HasPrefix(lines[1], "a.go,") {
		t.Errorf("row should start with the path: %q", lines[1])
	}
}

// TestCSV_QuotesPathsContainingCommas keeps the output parseable.
func TestCSV_QuotesPathsContainingCommas(t *testing.T) {
	p := "weird,name.go"
	commits := []churn.Commit{commit(1, "feat", p)}
	d := Build(commits, sizes(p), nil, Options{SplitRatio: 0})
	if !strings.Contains(d.CSV(), `"weird,name.go"`) {
		t.Errorf("path with a comma must be quoted:\n%s", d.CSV())
	}
}

// TestBuild_UnknownSizeFilesAreExcluded: a file with no current source cannot
// have relative churn computed, so it must not appear with a misleading 0.
func TestBuild_UnknownSizeFilesAreExcluded(t *testing.T) {
	commits := []churn.Commit{commit(1, "feat", "ghost.go", "real.go")}
	d := Build(commits, sizes("real.go"), nil, Options{SplitRatio: 0})
	for _, r := range d.Rows {
		if r.Path == "ghost.go" {
			t.Errorf("file with unknown size must be excluded, not emitted with zeros")
		}
	}
	if d.Meta.SkippedUnknownSize != 1 {
		t.Errorf("skipped count must be reported, got %d", d.Meta.SkippedUnknownSize)
	}
}

func TestBuild_Empty(t *testing.T) {
	d := Build(nil, nil, nil, Options{})
	if len(d.Rows) != 0 {
		t.Errorf("empty input must give no rows")
	}
	if d.CSV() == "" {
		t.Errorf("CSV must still emit a header for an empty dataset")
	}
}

// TestBuild_PositiveRateReported: class balance is the first thing anyone needs
// when handed a defect dataset, so it ships in the metadata.
func TestBuild_PositiveRateReported(t *testing.T) {
	commits := []churn.Commit{
		commit(1, "feat", "a.go", "b.go", "c.go", "d.go"),
		commit(5, "fix: a", "a.go"),
	}
	d := Build(commits, sizes("a.go", "b.go", "c.go", "d.go"), nil, Options{SplitRatio: 0.5})
	if d.Meta.DefectiveRows != 1 {
		t.Errorf("DefectiveRows = %d, want 1", d.Meta.DefectiveRows)
	}
	if d.Meta.PositiveRate <= 0 || d.Meta.PositiveRate > 1 {
		t.Errorf("PositiveRate out of range: %v", d.Meta.PositiveRate)
	}
}
