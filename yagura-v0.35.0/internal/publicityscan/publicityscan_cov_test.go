package publicityscan

import "testing"

// TestIsExampleEmail_NoAtSign covers the `at < 0` guard: a string without an
// "@" is treated as a non-address (returns true, i.e. not flagged).
func TestIsExampleEmail_NoAtSign(t *testing.T) {
	if !isExampleEmail("notanemail") {
		t.Error("a string without '@' should be treated as an example/non-address")
	}
}

// TestSortFindings_DifferentLines covers the `fs[i].Line != fs[j].Line` branch
// in SortFindings — the existing SummarizeAndSort test happens to put both
// findings on the same line, so the line-ordering arm was never exercised.
func TestSortFindings_DifferentLines(t *testing.T) {
	// home path on line 1, private IP on line 3 → two findings on distinct lines
	fs := Scan("/Users/bob/secret\n\n10.0.0.5 is a host")
	if len(fs) < 2 {
		t.Fatalf("expected at least 2 findings on different lines, got %+v", fs)
	}
	SortFindings(fs)
	for i := 1; i < len(fs); i++ {
		if fs[i].Line < fs[i-1].Line {
			t.Errorf("findings not sorted ascending by line: %+v", fs)
		}
	}
	// confirm the lines actually differ (otherwise the branch isn't exercised)
	if fs[0].Line == fs[len(fs)-1].Line {
		t.Errorf("test premise broken: all findings on the same line: %+v", fs)
	}
}
