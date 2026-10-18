package analysis

import (
	"testing"
	"time"
)

func TestReportCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newReportCache(100, 100)
	base := time.Now()
	entry := func(size int64, mod time.Time) *cachedReport {
		return &cachedReport{size: size, modTime: mod}
	}
	first := entry(40, base)
	second := entry(40, base)
	cache.put("run_1", first)
	cache.put("run_2", second)
	// Touch run_1 so run_2 becomes the least recently used entry.
	if got := cache.get("run_1", 40, base); got == nil {
		t.Fatal("run_1 is missing before eviction")
	}
	cache.put("run_3", entry(40, base))
	if got := cache.get("run_2", 40, base); got != nil {
		t.Error("run_2 was not evicted")
	}
	if got := cache.get("run_1", 40, base); got == nil {
		t.Error("run_1 was evicted despite being recently used")
	}
	if got := cache.get("run_3", 40, base); got == nil {
		t.Error("run_3 was not cached")
	}
}

func TestReportCacheInvalidatesOnIdentityChange(t *testing.T) {
	cache := newReportCache(100, 100)
	base := time.Now()
	cache.put("run_1", &cachedReport{size: 10, modTime: base})
	if got := cache.get("run_1", 11, base); got != nil {
		t.Error("cache returned an entry after a size change")
	}
	cache.put("run_1", &cachedReport{size: 10, modTime: base})
	if got := cache.get("run_1", 10, base.Add(time.Second)); got != nil {
		t.Error("cache returned an entry after a modification-time change")
	}
}

func TestReportCacheRefusesOversizedEntries(t *testing.T) {
	cache := newReportCache(50, 50)
	cache.put("run_1", &cachedReport{size: 51, modTime: time.Now()})
	if got := cache.get("run_1", 51, time.Time{}); got != nil {
		t.Error("cache accepted an entry above the byte bound")
	}
}

func TestReportCacheDisabled(t *testing.T) {
	store := newTestStore(t)
	service := newTestService(t, store, newFakeBackend(), func(opts *Options) {
		opts.MaxCacheBytes = -1
	})
	if service.cache != nil {
		t.Error("cache is enabled despite MaxCacheBytes < 0")
	}
}
