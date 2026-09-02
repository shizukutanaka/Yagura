package dedupe

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// ─── 基本動作 ─────────────────────────────────────────────

func TestCache_SetGet_Basic(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("k1", []byte("v1"))
	v, ok := c.Get("k1")
	if !ok {
		t.Fatal("Get should hit after Set")
	}
	if string(v) != "v1" {
		t.Errorf("got %q, want v1", v)
	}
}

func TestCache_Miss(t *testing.T) {
	c := New(10, time.Minute)
	_, ok := c.Get("never-set")
	if ok {
		t.Error("Get should miss for unset key")
	}
	s := c.Stats()
	if s.Misses != 1 {
		t.Errorf("expected 1 miss, got %d", s.Misses)
	}
}

func TestCache_GetReturnsCopy(t *testing.T) {
	c := New(10, time.Minute)
	original := []byte("hello")
	c.Set("k", original)

	got, _ := c.Get("k")
	got[0] = 'H' // 改変
	got2, _ := c.Get("k")
	if string(got2) != "hello" {
		t.Errorf("cache should return defensive copy; got %q", got2)
	}
}

// ─── LRU eviction ─────────────────────────────────────────

func TestCache_EvictsOldestWhenFull(t *testing.T) {
	c := New(3, time.Minute)
	c.Set("a", []byte("1"))
	c.Set("b", []byte("2"))
	c.Set("c", []byte("3"))
	c.Set("d", []byte("4")) // a が evict されるはず

	if _, ok := c.Get("a"); ok {
		t.Error("a should be evicted")
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s should remain", k)
		}
	}
	if c.Stats().Evictions == 0 {
		t.Error("evictions counter should be > 0")
	}
}

func TestCache_LRURecentlyUsedSurvives(t *testing.T) {
	c := New(3, time.Minute)
	c.Set("a", []byte("1"))
	c.Set("b", []byte("2"))
	c.Set("c", []byte("3"))

	_, _ = c.Get("a")       // a を front に移動
	c.Set("d", []byte("4")) // b が evict されるはず(LRU)

	if _, ok := c.Get("a"); !ok {
		t.Error("a should survive (recently used)")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("b should be evicted (least recently used)")
	}
}

// ─── TTL expiration ─────────────────────────────────────

func TestCache_ExpiredEntryMisses(t *testing.T) {
	c := New(10, 100*time.Millisecond)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	c.NowFn = func() time.Time { return now }
	c.Set("k", []byte("v"))

	// TTL 内
	if _, ok := c.Get("k"); !ok {
		t.Error("should hit within TTL")
	}

	// TTL 超過
	c.NowFn = func() time.Time { return now.Add(200 * time.Millisecond) }
	if _, ok := c.Get("k"); ok {
		t.Error("should miss after TTL")
	}
	s := c.Stats()
	if s.Expirations != 1 {
		t.Errorf("expected 1 expiration, got %d", s.Expirations)
	}
}

// ─── 統計 ────────────────────────────────────────────────

func TestStats_HitRate(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("k", []byte("v"))
	c.Get("k")     // hit
	c.Get("k")     // hit
	c.Get("miss1") // miss
	c.Get("miss2") // miss

	s := c.Stats()
	if s.Hits != 2 || s.Misses != 2 {
		t.Errorf("hits=%d misses=%d, want 2/2", s.Hits, s.Misses)
	}
	if s.HitRate != 0.5 {
		t.Errorf("hit_rate: got %v, want 0.5", s.HitRate)
	}
}

// TestStats_FreshCacheHitRateIsZeroNotNaN pins the h+m==0 boundary: a cache that
// has never been accessed must report HitRate 0, not NaN. NaN would make Stats
// fail json.Marshal (json: unsupported value: NaN), breaking yagura_dedupe_stats
// on every cold start.
func TestStats_FreshCacheHitRateIsZeroNotNaN(t *testing.T) {
	c := New(10, time.Minute)
	s := c.Stats()
	if s.HitRate != 0 {
		t.Errorf("fresh cache HitRate = %v, want 0", s.HitRate)
	}
	if _, err := json.Marshal(s); err != nil {
		t.Errorf("fresh Stats must be JSON-marshalable (no NaN/Inf): %v", err)
	}
}

func TestStats_BytesSavedAccumulates(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("k", []byte("0123456789")) // 10 bytes
	c.Get("k")                       // +10
	c.Get("k")                       // +10
	c.Get("k")                       // +10
	s := c.Stats()
	if s.BytesSaved != 30 {
		t.Errorf("bytes_saved: got %d, want 30", s.BytesSaved)
	}
}

// ─── Key 生成 ──────────────────────────────────────────

func TestKey_DeterministicAcrossInvocations(t *testing.T) {
	k1 := Key("tool_a", "arg1", "arg2")
	k2 := Key("tool_a", "arg1", "arg2")
	if k1 != k2 {
		t.Errorf("Key should be deterministic: %s != %s", k1, k2)
	}
}

func TestKey_DifferentArgsDifferKeys(t *testing.T) {
	k1 := Key("a", "x", "y")
	k2 := Key("a", "xy", "")
	if k1 == k2 {
		t.Error("Key with different args should produce different hashes (null separator)")
	}
}

func TestHashBytes(t *testing.T) {
	a := HashBytes([]byte("hello"))
	b := HashBytes([]byte("hello"))
	if a != b {
		t.Errorf("same content should hash same: %s != %s", a, b)
	}
	c := HashBytes([]byte("world"))
	if a == c {
		t.Error("different content should differ")
	}
	if len(a) != 64 {
		t.Errorf("SHA-256 hex should be 64 chars, got %d", len(a))
	}
}

// ─── 並行性 ────────────────────────────────────────────

func TestCache_ConcurrentAccess(t *testing.T) {
	c := New(100, time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", n%10)
			c.Set(key, []byte(fmt.Sprintf("v-%d", n)))
			_, _ = c.Get(key)
		}(i)
	}
	wg.Wait()
	s := c.Stats()
	if s.Hits+s.Misses < 100 {
		t.Errorf("expected at least 100 accesses, got %d", s.Hits+s.Misses)
	}
}

// ─── Delete / Reset ───────────────────────────────────

func TestCache_Delete(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("k", []byte("v"))
	c.Delete("k")
	if _, ok := c.Get("k"); ok {
		t.Error("should miss after Delete")
	}
	// 存在しない key の Delete は no-op
	c.Delete("never-was")
}

func TestCache_Reset(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("k1", []byte("v1"))
	c.Set("k2", []byte("v2"))
	c.Get("k1") // hit
	c.Reset()

	if c.Len() != 0 {
		t.Errorf("Len after Reset: got %d, want 0", c.Len())
	}
	s := c.Stats()
	if s.Hits != 0 || s.Misses != 0 {
		t.Errorf("stats not reset: %+v", s)
	}
}

// ─── 既存 entry update ──────────────────────────────────

func TestCache_UpdateExisting(t *testing.T) {
	c := New(10, time.Minute)
	c.Set("k", []byte("v1"))
	c.Set("k", []byte("v2"))
	if c.Len() != 1 {
		t.Errorf("update should not increase Len: %d", c.Len())
	}
	v, _ := c.Get("k")
	if string(v) != "v2" {
		t.Errorf("update should replace value; got %q", v)
	}
}

// ─── Defaults ────────────────────────────────────────

func TestNew_DefaultsApplied(t *testing.T) {
	c := New(0, 0)
	if c.max != DefaultMaxEntries || c.ttl != DefaultTTL {
		t.Errorf("defaults not applied: max=%d ttl=%v", c.max, c.ttl)
	}
}

// ─── v0.35: persistent (write-through disk) layer ───────────

func TestCache_Persistence_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	key := Key("quality_check", HashBytes([]byte("source")))

	c1 := New(10, time.Hour)
	if err := c1.EnablePersistence(dir); err != nil {
		t.Fatal(err)
	}
	c1.Set(key, []byte("RESULT"))

	// "restart": a brand-new cache (empty memory) pointed at the same dir.
	c2 := New(10, time.Hour)
	if err := c2.EnablePersistence(dir); err != nil {
		t.Fatal(err)
	}
	got, ok := c2.Get(key)
	if !ok || string(got) != "RESULT" {
		t.Fatalf("expected disk-backed hit after restart, got %q ok=%v", got, ok)
	}
	// the disk hit promoted into memory
	if c2.Len() != 1 {
		t.Errorf("disk hit should promote to memory, Len=%d", c2.Len())
	}
}

func TestCache_Persistence_TTLAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	key := Key("k")
	base := time.Now()

	c1 := New(10, time.Hour)
	c1.NowFn = func() time.Time { return base }
	if err := c1.EnablePersistence(dir); err != nil {
		t.Fatal(err)
	}
	c1.Set(key, []byte("v"))

	// restart 2h later — the on-disk createdAt makes it expired.
	c2 := New(10, time.Hour)
	c2.NowFn = func() time.Time { return base.Add(2 * time.Hour) }
	if err := c2.EnablePersistence(dir); err != nil { // prunes expired on enable
		t.Fatal(err)
	}
	if _, ok := c2.Get(key); ok {
		t.Error("entry should be expired across restart (TTL measured from disk createdAt)")
	}
}

func TestCache_Persistence_PrunesExpiredOnEnable(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	c1 := New(10, time.Hour)
	c1.NowFn = func() time.Time { return base }
	if err := c1.EnablePersistence(dir); err != nil {
		t.Fatal(err)
	}
	c1.Set(Key("old"), []byte("x"))

	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("expected 1 file on disk, got %d", len(files))
	}
	// a fresh cache enabling persistence 2h later prunes the expired file.
	c2 := New(10, time.Hour)
	c2.NowFn = func() time.Time { return base.Add(2 * time.Hour) }
	if err := c2.EnablePersistence(dir); err != nil {
		t.Fatal(err)
	}
	files, _ = os.ReadDir(dir)
	if len(files) != 0 {
		t.Errorf("expired disk file should be pruned on EnablePersistence, %d remain", len(files))
	}
}

func TestCache_Persistence_DeleteRemovesDiskFile(t *testing.T) {
	dir := t.TempDir()
	c := New(10, time.Hour)
	if err := c.EnablePersistence(dir); err != nil {
		t.Fatal(err)
	}
	key := Key("d")
	c.Set(key, []byte("v"))
	c.Delete(key)
	if files, _ := os.ReadDir(dir); len(files) != 0 {
		t.Errorf("Delete should remove the disk file, %d remain", len(files))
	}
}

func TestCache_NoPersistenceByDefault(t *testing.T) {
	// without EnablePersistence, behaviour is unchanged (in-memory only).
	c := New(10, time.Hour)
	c.Set(Key("x"), []byte("v"))
	c2 := New(10, time.Hour)
	if _, ok := c2.Get(Key("x")); ok {
		t.Error("a separate in-memory cache must not see another's entries")
	}
}

func TestCache_PruneExpiredDisk_ReclaimsMidRun(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	now := base
	c := New(2, time.Hour) // tiny memory cap so disk outlives memory
	c.NowFn = func() time.Time { return now }
	if err := c.EnablePersistence(dir); err != nil {
		t.Fatal(err)
	}
	// write 3 entries; memory cap=2 evicts one from RAM but all 3 stay on disk
	c.Set(Key("a"), []byte("1"))
	c.Set(Key("b"), []byte("2"))
	c.Set(Key("c"), []byte("3"))
	if n, _ := os.ReadDir(dir); len(n) != 3 {
		t.Fatalf("expected 3 disk files, got %d", len(n))
	}
	// advance past TTL and prune (no restart) — all expired files reclaimed
	now = base.Add(2 * time.Hour)
	c.PruneExpiredDisk()
	if n, _ := os.ReadDir(dir); len(n) != 0 {
		t.Errorf("PruneExpiredDisk should reclaim expired files mid-run, %d remain", len(n))
	}
}

func TestCache_PruneExpiredDisk_NoPersistenceIsNoop(t *testing.T) {
	c := New(10, time.Hour) // persistence not enabled
	c.PruneExpiredDisk()    // must not panic
}

// TestEnablePersistence_EmptyDirReturnsNil covers the dir=="" early return.
func TestEnablePersistence_EmptyDirReturnsNil(t *testing.T) {
	c := New(10, time.Hour)
	if err := c.EnablePersistence(""); err != nil {
		t.Errorf("EnablePersistence empty dir should return nil, got %v", err)
	}
}

// TestCache_MaxZero_LRUBackNil covers the oldest==nil branch in insertMemLocked.
func TestCache_MaxZero_LRUBackNil(t *testing.T) {
	// max=0 → every insert immediately tries to evict; LRU back is nil → break.
	c := New(0, time.Hour)
	c.Set(Key("x"), []byte("v")) // must not panic
}

// TestCache_DiskHit_Expired covers the disk-fallback TTL-expiry path in Get.
func TestCache_DiskHit_Expired(t *testing.T) {
	dir := t.TempDir()
	base := time.Now()
	now := base
	c := New(1, time.Hour) // memory cap = 1
	c.NowFn = func() time.Time { return now }
	if err := c.EnablePersistence(dir); err != nil {
		t.Fatal(err)
	}

	k1, k2 := Key("first"), Key("second")
	c.Set(k1, []byte("v1"))
	// Adding k2 evicts k1 from memory (cap=1), but k1 stays on disk.
	c.Set(k2, []byte("v2"))
	// Advance past TTL.
	now = base.Add(2 * time.Hour)
	// Get k1: disk hit, but expired → should return false and prune the disk file.
	if _, ok := c.Get(k1); ok {
		t.Error("expired disk entry should return not-found")
	}
}

// TestPruneExpiredDisk_SkipsDirectoriesAndCorrupt covers the IsDir+short-file continue paths.
func TestPruneExpiredDisk_SkipsDirectoriesAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	c := New(10, time.Hour)
	if err := c.EnablePersistence(dir); err != nil {
		t.Fatal(err)
	}
	// Create a subdirectory inside the cache dir (IsDir → continue).
	if err := os.Mkdir(dir+"/subdir", 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a file with fewer than 8 bytes (corrupt → continue).
	if err := os.WriteFile(dir+"/short", []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Must not panic or error.
	c.PruneExpiredDisk()
}
