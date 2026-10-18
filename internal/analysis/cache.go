package analysis

import (
	"sync"
	"time"

	"github.com/Morditux/traceanvil-mcp/internal/diagnostic"
)

const (
	// DefaultMaxCacheBytes bounds the process memory used by decoded reports.
	DefaultMaxCacheBytes int64 = 64 << 20
	// DefaultMaxCacheEntryBytes bounds one decoded report. Larger reports are
	// served by the streaming reader instead of being materialized.
	DefaultMaxCacheEntryBytes int64 = 16 << 20
	defaultMaxCacheEntries          = 16
)

// cachedReport is an immutable decoded report. The diagnostics slice is shared
// between concurrent pages and must never be modified.
type cachedReport struct {
	header      reportHeader
	diagnostics []diagnostic.Diagnostic
	unreadable  int

	size     int64
	cost     int64
	modTime  time.Time
	lastUsed uint64
}

// reportCache is a small LRU over decoded reports. It exists because paging by
// re-scanning a multi-megabyte JSON document for every page is quadratic; the
// cache is bounded by both entries and bytes, and entries are invalidated when
// the stored report identity changes.
type reportCache struct {
	mu       sync.Mutex
	entries  map[string]*cachedReport
	bytes    int64
	maxBytes int64
	maxEntry int64
	maxItems int
	clock    uint64
}

func newReportCache(maxBytes, maxEntryBytes int64) *reportCache {
	if maxEntryBytes <= 0 || maxEntryBytes > maxBytes {
		maxEntryBytes = DefaultMaxCacheEntryBytes
		if maxEntryBytes > maxBytes {
			maxEntryBytes = maxBytes
		}
	}
	return &reportCache{
		entries:  make(map[string]*cachedReport),
		maxBytes: maxBytes,
		maxEntry: maxEntryBytes,
		maxItems: defaultMaxCacheEntries,
	}
}

func (c *reportCache) get(runID string, size int64, modTime time.Time) *cachedReport {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[runID]
	if !ok {
		return nil
	}
	if entry.size != size || !entry.modTime.Equal(modTime) {
		c.removeLocked(runID)
		return nil
	}
	c.clock++
	entry.lastUsed = c.clock
	return entry
}

func (c *reportCache) put(runID string, entry *cachedReport) {
	if entry.cost <= 0 {
		entry.cost = entry.size
	}
	if entry.cost > c.maxEntry || entry.cost > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[runID]; ok {
		c.bytes -= existing.cost
		delete(c.entries, runID)
	}
	c.clock++
	entry.lastUsed = c.clock
	c.entries[runID] = entry
	c.bytes += entry.cost

	for (c.bytes > c.maxBytes || len(c.entries) > c.maxItems) && len(c.entries) > 0 {
		oldestID := ""
		var oldest uint64
		for id, candidate := range c.entries {
			if oldestID == "" || candidate.lastUsed < oldest {
				oldestID = id
				oldest = candidate.lastUsed
			}
		}
		c.removeLocked(oldestID)
	}
}

func (c *reportCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cachedReport)
	c.bytes = 0
}

func (c *reportCache) removeLocked(runID string) {
	entry, ok := c.entries[runID]
	if !ok {
		return
	}
	c.bytes -= entry.cost
	delete(c.entries, runID)
}
