package repository

import (
	"time"

	"cpa-usage-keeper/internal/timeutil"
)

// storedUsageEventsFromRecentCache observes one scoped, half-open event snapshot.
// It never extends a historical upper edge using the current-path EventsSince
// behavior. Closed, pending, unavailable or uncovered caches must use the DB.
func storedUsageEventsFromRecentCache(cache *UsageRecentEventCache, start, end, queryNow time.Time, key string) ([]RecentUsageEvent, bool) {
	if cache == nil {
		return nil, false
	}
	revision := cache.storedAppendRevision.Load()
	if cache.now == nil || cache.storedReadGap.Load() || cache.storedAppendPending.Load() != 0 || len(cache.appendSlots) != cap(cache.appendSlots) {
		return nil, false
	}
	select {
	case <-cache.stopCh:
		return nil, false
	default:
	}
	anchor := timeutil.NormalizeStorageTime(queryNow)
	if cacheNow := timeutil.NormalizeStorageTime(cache.now()); cacheNow.After(anchor) {
		anchor = cacheNow
	}
	if start.Before(anchor.Add(-cache.Window())) {
		return nil, false
	}
	rows, ok := cache.Events(start, end, false, key)
	if !ok || cache.storedReadGap.Load() || cache.storedAppendPending.Load() != 0 || cache.storedAppendRevision.Load() != revision || len(cache.appendSlots) != cap(cache.appendSlots) {
		return nil, false
	}
	return rows, true
}
