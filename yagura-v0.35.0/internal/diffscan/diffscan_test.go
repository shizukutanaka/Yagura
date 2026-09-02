package diffscan

import "testing"

func TestAddedLines_Simple(t *testing.T) {
	diff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package foo

+var added = 1
 func F() {}
`
	got := AddedLines(diff)
	if len(got) != 1 {
		t.Fatalf("expected 1 added line, got %d: %+v", len(got), got)
	}
	if got[0].Path != "foo.go" || got[0].Line != 3 || got[0].Text != "var added = 1" {
		t.Errorf("wrong added line: %+v", got[0])
	}
}

func TestAddedLines_RemovedDoesNotShift(t *testing.T) {
	// a removed line must not advance the new-file line counter.
	diff := `--- a/x.go
+++ b/x.go
@@ -1,3 +1,3 @@
 a
-old
+new
 c
`
	got := AddedLines(diff)
	if len(got) != 1 {
		t.Fatalf("expected 1 added line, got %d", len(got))
	}
	if got[0].Line != 2 || got[0].Text != "new" {
		t.Errorf("expected 'new' at new-file line 2, got %+v", got[0])
	}
}

func TestAddedLines_NewFile(t *testing.T) {
	diff := `diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+package new
+var x = 1
`
	got := AddedLines(diff)
	if len(got) != 2 {
		t.Fatalf("expected 2 added lines, got %d: %+v", len(got), got)
	}
	if got[0].Path != "new.go" || got[0].Line != 1 || got[1].Line != 2 {
		t.Errorf("new-file added lines wrong: %+v", got)
	}
}

func TestAddedLines_MultipleFiles(t *testing.T) {
	diff := `--- a/a.go
+++ b/a.go
@@ -1 +1,2 @@
 a
+addA
--- a/b.go
+++ b/b.go
@@ -1 +1,2 @@
 b
+addB
`
	got := AddedLines(diff)
	if len(got) != 2 {
		t.Fatalf("expected 2 added lines across files, got %d", len(got))
	}
	if got[0].Path != "a.go" || got[0].Text != "addA" || got[1].Path != "b.go" || got[1].Text != "addB" {
		t.Errorf("multi-file attribution wrong: %+v", got)
	}
}

func TestAddedLines_PlusPlusPlusNotCountedAsAdded(t *testing.T) {
	// the +++ header must never be treated as an added content line.
	diff := `--- a/x.go
+++ b/x.go
@@ -1 +1 @@
 unchanged
`
	if got := AddedLines(diff); len(got) != 0 {
		t.Errorf("no content added; +++ header must not count, got %+v", got)
	}
}

func TestAddedLines_Empty(t *testing.T) {
	if got := AddedLines(""); len(got) != 0 {
		t.Errorf("empty diff yields no added lines, got %v", got)
	}
}

// ─── RemovedLines: the delta "removed" axis ──────────────────────────────

func TestRemovedLines_TracksOldLineNumbers(t *testing.T) {
	diff := `--- a/x.go
+++ b/x.go
@@ -1,3 +1,2 @@
 a
-removed
 c
`
	got := RemovedLines(diff)
	if len(got) != 1 {
		t.Fatalf("expected 1 removed line, got %d: %+v", len(got), got)
	}
	if got[0].Path != "x.go" || got[0].Line != 2 || got[0].Text != "removed" {
		t.Errorf("wrong removed line: %+v", got[0])
	}
}

func TestRemovedLines_AddedDoesNotShiftOld(t *testing.T) {
	diff := `--- a/x.go
+++ b/x.go
@@ -1,2 +1,3 @@
 a
+inserted
-gone
`
	got := RemovedLines(diff)
	if len(got) != 1 || got[0].Line != 2 || got[0].Text != "gone" {
		t.Errorf("an added line must not shift the old-file counter; got %+v", got)
	}
}

func TestRemovedLines_MinusMinusMinusNotCounted(t *testing.T) {
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n unchanged\n"
	if got := RemovedLines(diff); len(got) != 0 {
		t.Errorf("--- header must not count as a removed line, got %+v", got)
	}
}

// ─── RemovedGuards: removing a safety construct is a risky change ─────────

func TestRemovedGuards_ErrorCheck(t *testing.T) {
	diff := `--- a/x.go
+++ b/x.go
@@ -1,4 +1,2 @@
 v, err := do()
-if err != nil {
-	return err
-}
 use(v)
`
	g := RemovedGuards(diff)
	var kinds = map[string]int{}
	for _, x := range g {
		kinds[x.Kind]++
	}
	if kinds["error-check-removed"] != 1 {
		t.Errorf("expected an error-check-removed guard, got %+v", g)
	}
}

func TestRemovedGuards_DeferUnlock(t *testing.T) {
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,1 @@\n mu.Lock()\n-defer mu.Unlock()\n"
	g := RemovedGuards(diff)
	if len(g) != 1 || g[0].Kind != "cleanup-removed" {
		t.Errorf("expected cleanup-removed for a removed defer Unlock, got %+v", g)
	}
}

func TestRemovedGuards_RecoverRemoved(t *testing.T) {
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,1 @@\n func F() {\n-	defer func() { recover() }()\n }\n"
	g := RemovedGuards(diff)
	found := false
	for _, x := range g {
		if x.Kind == "recover-removed" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected recover-removed, got %+v", g)
	}
}

// Adding a guard (not removing) must NOT flag; only removals are risky here.
func TestRemovedGuards_AddedGuardIgnored(t *testing.T) {
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1 +1,2 @@\n x()\n+if err != nil { return err }\n"
	if g := RemovedGuards(diff); len(g) != 0 {
		t.Errorf("an ADDED guard must not flag as removed, got %+v", g)
	}
}
