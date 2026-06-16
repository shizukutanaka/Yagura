// Package dedupe は content-addressed cache + tool result cache を提供する。
//
// 動機 (v0.23.0):
//
//	AI agent (Claude Code 等) が短期間に同じ tool を同じ引数で呼ぶ、
//	または同じ source content を scan する重複が観察される。
//	cortex (aircloset 2026/05) でも "AI が迷う頻度を構造的に減らす" として
//	Product Graph で同じ問いを 1 回で答える設計を採用している。
//
//	yagura はもう一歩踏み込んで、tool 呼出 + content scan の deterministic な
//	結果を content hash でキャッシュし、重複呼出を即時に応答する。
//
// 設計判断 (ADR-0001 ゼロ依存準拠):
//   - sync.Map ではなく explicit lock の RWMutex map[string]*entry
//   - SHA-256 で content addressing(stdlib のみ)
//   - LRU eviction(linked list + map で O(1) update)
//   - TTL: 各 entry に CreatedAt、Stats() で expired もカウント
//   - thread-safe: 全 public method は lock 取得
//
// 性能特性:
//   - Get: O(1) hash lookup + O(1) LRU update
//   - Set: O(1) insert + O(1) LRU update + 必要なら 1 eviction
//   - Stats: O(1) atomic load
package dedupe

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shizukutanaka/yagura/internal/atomicfile"
)

// DefaultMaxEntries は cache が保持する最大 entry 数。
// 超えると LRU で evict。
const DefaultMaxEntries = 256

// DefaultTTL は entry の expiration window。
// この時間を超えた entry は Get で miss として扱われ、次の Set で上書きされる。
const DefaultTTL = 1 * time.Hour

// Cache は thread-safe な content-addressed cache。
type Cache struct {
	mu      sync.Mutex // RW lock より単純な Mutex(LRU update が write を含むため)
	max     int
	ttl     time.Duration
	entries map[string]*list.Element // key → list element
	lru     *list.List               // front = most recent

	// dir, if non-empty, enables a write-through disk layer so heavy results
	// survive a daemon restart (v0.35). Empty = in-memory only (unchanged).
	dir string

	// stats (atomic for lock-free read in Stats())
	hits        uint64
	misses      uint64
	evictions   uint64
	expirations uint64
	bytesSaved  uint64 // hit の累積で「もしキャッシュなければ生成されたであろう byte 数」

	// NowFn はテスト用時刻 hook。
	NowFn func() time.Time
}

type entry struct {
	key       string
	value     []byte
	createdAt time.Time
}

// New は cache を生成する。
// maxEntries=0 で DefaultMaxEntries 採用、ttl=0 で DefaultTTL 採用。
func New(maxEntries int, ttl time.Duration) *Cache {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Cache{
		max:     maxEntries,
		ttl:     ttl,
		entries: make(map[string]*list.Element, maxEntries),
		lru:     list.New(),
	}
}

func (c *Cache) now() time.Time {
	if c.NowFn != nil {
		return c.NowFn()
	}
	return time.Now()
}

// Key は引数群から決定論的な cache key を生成する。
//
// usage:
//
//	key := dedupe.Key("quality_check", string(contentHash))
//	key := dedupe.Key("vulns", "shizukutanaka/breeze")
func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0}) // null separator で衝突防止
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HashBytes は content の SHA-256 を hex 文字列で返す。
//
// quality_check 等で source content をキーにする用途。
func HashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Get は cache hit なら value と true、miss なら nil と false を返す。
//
// hit 時に LRU を更新(front に移動)、TTL 超過の entry は miss 扱い + 削除。
func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[key]; ok {
		e := elem.Value.(*entry)
		if c.now().Sub(e.createdAt) > c.ttl {
			// expired
			c.lru.Remove(elem)
			delete(c.entries, key)
			c.deleteDiskLocked(key)
			atomic.AddUint64(&c.expirations, 1)
			atomic.AddUint64(&c.misses, 1)
			return nil, false
		}
		// LRU 更新
		c.lru.MoveToFront(elem)
		atomic.AddUint64(&c.hits, 1)
		atomic.AddUint64(&c.bytesSaved, uint64(len(e.value)))
		return cloneBytes(e.value), true
	}
	// memory miss — fall back to the disk layer (durable across restart).
	if c.dir != "" {
		if val, createdAt, ok := c.readDiskLocked(key); ok {
			if c.now().Sub(createdAt) > c.ttl {
				c.deleteDiskLocked(key)
				atomic.AddUint64(&c.expirations, 1)
				atomic.AddUint64(&c.misses, 1)
				return nil, false
			}
			c.insertMemLocked(key, val, createdAt) // promote to memory (may evict)
			atomic.AddUint64(&c.hits, 1)
			atomic.AddUint64(&c.bytesSaved, uint64(len(val)))
			return cloneBytes(val), true
		}
	}
	atomic.AddUint64(&c.misses, 1)
	return nil, false
}

// Set は entry を追加または更新する。
//
// max を超えた場合は LRU の末尾を evict する。dir が設定されていれば disk にも
// write-through する(lock 外、best-effort)。
func (c *Cache) Set(key string, value []byte) {
	c.mu.Lock()
	now := c.now()
	c.insertMemLocked(key, value, now)
	dir := c.dir
	c.mu.Unlock()
	if dir != "" {
		c.writeDisk(key, value, now) // best-effort, outside the lock
	}
}

// insertMemLocked は memory map/LRU へ insert または update する(disk は触らない)。
// 呼出側が c.mu を保持していること。
func (c *Cache) insertMemLocked(key string, value []byte, createdAt time.Time) {
	if elem, ok := c.entries[key]; ok {
		e := elem.Value.(*entry)
		e.value = append(e.value[:0], value...)
		e.createdAt = createdAt
		c.lru.MoveToFront(elem)
		return
	}
	for c.lru.Len() >= c.max {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		oldEntry := oldest.Value.(*entry)
		c.lru.Remove(oldest)
		delete(c.entries, oldEntry.key)
		atomic.AddUint64(&c.evictions, 1)
	}
	val := make([]byte, len(value))
	copy(val, value)
	e := &entry{key: key, value: val, createdAt: createdAt}
	c.entries[key] = c.lru.PushFront(e)
}

func cloneBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// Delete は key を削除する。存在しない場合は no-op。
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[key]; ok {
		c.lru.Remove(elem)
		delete(c.entries, key)
	}
	c.deleteDiskLocked(key)
}

// ─── v0.35: optional write-through disk layer (persistent cache) ─────────
//
// 動機(Roadmap #3 / 既知 gotcha): dedupe は in-memory のみで、daemon restart で
// 全結果が消えていた。重い sbom / ai_verify / quality_check の結果が再起動のたびに
// 再計算される。dir を設定すると、Set 時に content-hash をファイル名にして disk へ
// 書き(crash-safe: internal/atomicfile)、memory miss 時に disk から lazy reload する。
// TTL は disk 上の createdAt から測るので再起動をまたいでも期限が一貫する。
//
// best-effort: disk I/O 失敗は Get/Set を壊さない(in-memory cache が常に正)。keys は
// SHA-256 hex(filesystem-safe)。eviction は memory のみ LRU、disk は TTL 期限切れを
// read 時と EnablePersistence 時に prune(=自然に縮む。active な size cap は持たない)。

// EnablePersistence は dir に write-through disk 層を有効化する。dir を作成し、
// 既存の期限切れファイルを prune する。mkdir 失敗時のみ error を返す。
func (c *Cache) EnablePersistence(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	c.mu.Lock()
	c.dir = dir
	c.mu.Unlock()
	c.PruneExpiredDisk()
	return nil
}

func (c *Cache) diskPath(key string) string { return filepath.Join(c.dir, key) }

// writeDisk は createdAt(8byte big-endian unix-nano)+ value を atomic に書く。
// best-effort(error は無視)。lock 外から呼ぶ。
func (c *Cache) writeDisk(key string, value []byte, createdAt time.Time) {
	buf := make([]byte, 8+len(value))
	binary.BigEndian.PutUint64(buf[:8], uint64(createdAt.UnixNano()))
	copy(buf[8:], value)
	atomicfile.Write(c.diskPath(key), buf, 0o644)
}

// readDiskLocked は disk entry を読む。戻り value は呼出側が copy 前提(insertMemLocked
// が copy する)。c.mu 保持前提(c.dir 読み取りのため)。
func (c *Cache) readDiskLocked(key string) ([]byte, time.Time, bool) {
	b, err := os.ReadFile(c.diskPath(key))
	if err != nil || len(b) < 8 {
		return nil, time.Time{}, false
	}
	ts := int64(binary.BigEndian.Uint64(b[:8]))
	return b[8:], time.Unix(0, ts), true
}

func (c *Cache) deleteDiskLocked(key string) {
	if c.dir != "" {
		os.Remove(c.diskPath(key))
	}
}

// PruneExpiredDisk は dir 内の TTL 超過ファイルを削除する。EnablePersistence(起動時)で
// 一度走るほか、長時間稼働する daemon では定期的に呼ぶことで「memory LRU で evict されたが
// disk には残る期限切れエントリ」を回収し、cache ディレクトリの肥大を防ぐ。persistence 未
// 有効(dir 空)なら no-op。best-effort(I/O エラーは無視)。
func (c *Cache) PruneExpiredDisk() {
	c.mu.Lock()
	dir, ttl, now := c.dir, c.ttl, c.now()
	c.mu.Unlock()
	if dir == "" {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, de := range ents {
		if de.IsDir() {
			continue
		}
		p := filepath.Join(dir, de.Name())
		b, err := os.ReadFile(p)
		if err != nil || len(b) < 8 {
			continue
		}
		ts := int64(binary.BigEndian.Uint64(b[:8]))
		if now.Sub(time.Unix(0, ts)) > ttl {
			os.Remove(p)
		}
	}
}

// Len は現在の entry 数を返す。
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

// Stats は累積統計を返す。
type Stats struct {
	Hits        uint64  `json:"hits"`
	Misses      uint64  `json:"misses"`
	Evictions   uint64  `json:"evictions"`
	Expirations uint64  `json:"expirations"`
	BytesSaved  uint64  `json:"bytes_saved"` // hit で返した cumulative bytes(節約推定)
	HitRate     float64 `json:"hit_rate"`    // hits / (hits + misses)
	CurrentSize int     `json:"current_size"`
	MaxSize     int     `json:"max_size"`
	TTLSeconds  int64   `json:"ttl_seconds"`
}

// Stats は atomic load で snapshot を返す。
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	size := c.lru.Len()
	maxv := c.max
	ttlSec := int64(c.ttl.Seconds())
	c.mu.Unlock()

	h := atomic.LoadUint64(&c.hits)
	m := atomic.LoadUint64(&c.misses)
	var rate float64
	if h+m > 0 {
		rate = float64(h) / float64(h+m)
	}
	return Stats{
		Hits:        h,
		Misses:      m,
		Evictions:   atomic.LoadUint64(&c.evictions),
		Expirations: atomic.LoadUint64(&c.expirations),
		BytesSaved:  atomic.LoadUint64(&c.bytesSaved),
		HitRate:     rate,
		CurrentSize: size,
		MaxSize:     maxv,
		TTLSeconds:  ttlSec,
	}
}

// Reset は entry と stats を全クリア(test 用)。
func (c *Cache) Reset() {
	c.mu.Lock()
	c.entries = make(map[string]*list.Element, c.max)
	c.lru.Init()
	c.mu.Unlock()
	atomic.StoreUint64(&c.hits, 0)
	atomic.StoreUint64(&c.misses, 0)
	atomic.StoreUint64(&c.evictions, 0)
	atomic.StoreUint64(&c.expirations, 0)
	atomic.StoreUint64(&c.bytesSaved, 0)
}
