package service

import (
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// SubscriptionUsageCacheTTL is how long the usage windows fetched for one
// bound OAuth channel are served to every subscriber of that channel, by the
// session and the API-key usage endpoints alike, before upstream is asked
// again. The cache lives in this process only; with several nodes each node
// asks upstream at most once per TTL and channel.
const SubscriptionUsageCacheTTL = 60 * time.Second

type subscriptionUsageCacheEntry struct {
	value    any
	storedAt time.Time
}

var (
	subscriptionUsageCacheMu sync.Mutex
	subscriptionUsageCache   = map[int]subscriptionUsageCacheEntry{}
	subscriptionUsageFlight  singleflight.Group
)

// LoadSubscriptionUsage returns the usage of channelId cached within
// SubscriptionUsageCacheTTL, or runs fetch once for all concurrent callers.
// The result is cached only when fetch reports it cacheable (an upstream
// success), so a failure is retried on the next request.
func LoadSubscriptionUsage[T any](channelId int, now time.Time, fetch func() (value T, cacheable bool, err error)) (T, error) {
	subscriptionUsageCacheMu.Lock()
	entry, ok := subscriptionUsageCache[channelId]
	subscriptionUsageCacheMu.Unlock()
	if ok && now.Sub(entry.storedAt) < SubscriptionUsageCacheTTL {
		if value, typed := entry.value.(T); typed {
			return value, nil
		}
	}

	shared, err, _ := subscriptionUsageFlight.Do(strconv.Itoa(channelId), func() (any, error) {
		value, cacheable, err := fetch()
		if err == nil && cacheable {
			subscriptionUsageCacheMu.Lock()
			subscriptionUsageCache[channelId] = subscriptionUsageCacheEntry{value: value, storedAt: time.Now()}
			subscriptionUsageCacheMu.Unlock()
		}
		return value, err
	})
	value, _ := shared.(T)
	return value, err
}

// InvalidateSubscriptionUsage drops the cached usage of channelId, for
// example after a reset credit changed the windows.
func InvalidateSubscriptionUsage(channelId int) {
	subscriptionUsageCacheMu.Lock()
	delete(subscriptionUsageCache, channelId)
	subscriptionUsageCacheMu.Unlock()
}
