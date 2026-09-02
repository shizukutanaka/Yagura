package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

// noTempLeak は dir 配下に .atomic-* の残骸が無いことを確認する。
func noTempLeak(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, e := range entries {
		if len(e.Name()) >= 8 && e.Name()[:8] == ".atomic-" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestWrite_RoundTripAndMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	want := []byte("hello\nworld")
	if err := Write(p, want, 0o644); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("content = %q, want %q", got, want)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
	noTempLeak(t, dir)
}

func TestWrite_AtomicOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := Write(p, []byte("old-content-longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(p, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "new" {
		t.Errorf("overwrite content = %q, want %q (no leftover bytes of old)", got, "new")
	}
	noTempLeak(t, dir)
}

func TestWrite_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a", "b", "c", "f.txt")
	if err := Write(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("Write into missing dirs: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("file not created: %v", err)
	}
	// 親ディレクトリは 0755 で作られる。
	di, err := os.Stat(filepath.Join(dir, "a"))
	if err != nil {
		t.Fatal(err)
	}
	if !di.IsDir() {
		t.Error("parent is not a directory")
	}
}

func TestWrite_EmptyData(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "empty")
	if err := Write(p, nil, 0o644); err != nil {
		t.Fatalf("Write empty: %v", err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty file, got %d bytes", len(got))
	}
	noTempLeak(t, dir)
}

func TestWrite_ErrorWhenParentIsFile_NoLeak(t *testing.T) {
	dir := t.TempDir()
	// 既存の通常ファイルを「親ディレクトリ」に見立てる → MkdirAll が失敗する。
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(blocker, "child.txt") // blocker はファイルなので親作成不可
	if err := Write(p, []byte("data"), 0o644); err == nil {
		t.Error("expected error when parent path is a file, got nil")
	}
	// temp は dir 直下に作られないこと(MkdirAll が先に失敗するため)。
	noTempLeak(t, dir)
}

func TestWrite_ErrorWhenTargetIsDir_CleansTemp(t *testing.T) {
	// path が既存ディレクトリだと最終 Rename が失敗する。これは temp file を
	// 作成した *後* の error path なので、defer os.Remove によるクリーンアップが
	// 効いていること(=残骸ゼロ)を確認する。MkdirAll-fail ケースとは別経路。
	dir := t.TempDir()
	target := filepath.Join(dir, "iamadir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Write(target, []byte("data"), 0o644); err == nil {
		t.Error("expected rename error when target is an existing directory, got nil")
	}
	// temp file は作られたが defer で消えているはず。
	noTempLeak(t, dir)
	// 既存ディレクトリは壊されていないこと。
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Errorf("target dir damaged: info=%v err=%v", info, err)
	}
}

func TestWrite_ConcurrentAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shared")
	const n = 24
	valid := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		valid[fmt.Sprintf("value-%02d-payload", i)] = true
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := Write(p, []byte(fmt.Sprintf("value-%02d-payload", i)), 0o644); err != nil {
				t.Errorf("concurrent Write: %v", err)
			}
		}(i)
	}
	wg.Wait()
	// 最終ファイルは「いずれかの完全な値」でなければならない(torn write 無し)。
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !valid[string(got)] {
		t.Errorf("final content %q is not one of the complete written values (torn write?)", got)
	}
	noTempLeak(t, dir)
}

func TestWrite_DistinctPathsNoCrossTalk(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := filepath.Join(dir, fmt.Sprintf("f%02d", i))
			_ = Write(p, []byte(fmt.Sprintf("content-%02d", i)), 0o644)
		}(i)
	}
	wg.Wait()
	var got []string
	for i := 0; i < 10; i++ {
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("f%02d", i)))
		if err != nil {
			t.Fatalf("read f%02d: %v", i, err)
		}
		got = append(got, string(b))
	}
	sort.Strings(got)
	for i := 0; i < 10; i++ {
		want := fmt.Sprintf("content-%02d", i)
		if got[i] != want {
			t.Errorf("file %d = %q, want %q", i, got[i], want)
		}
	}
	noTempLeak(t, dir)
}

func TestWrite_ErrorWhenDirReadOnly_CreateTempFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permission; CreateTemp would succeed")
	}
	// 親ディレクトリは存在するが書込不可 → MkdirAll は成功(既存)、
	// CreateTemp が permission denied で失敗する経路。
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // TempDir cleanup のため戻す
	if err := Write(filepath.Join(dir, "f.txt"), []byte("data"), 0o644); err == nil {
		t.Error("expected CreateTemp error in a read-only directory, got nil")
	}
}
