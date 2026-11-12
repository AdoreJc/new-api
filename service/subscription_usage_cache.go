package service

import (
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	// SubscriptionUsageCacheTTL is how long the usage windows fetched for one
	// bound OAuth channel are served to every subscriber of that channel, by
	// the session and the API-key usage endpoints alike, before upstream is
	// asked again. The cache lives in this process only; with several nodes
	// each node asks upstream at most once per TTL and channel.
	SubscriptionUsageCacheTTL = 60 * time.Second
	// SubscriptionUsageFailureTTL is how long a failed fetch (an upstream
	// error status or no answer) is served again, so clients that poll in the
	// background do not hit a failing upstream on every request.
	SubscriptionUsageFailureTTL = 20 * time.Second
	// SubscriptionUsageRefreshCooldown is the shortest time between two forced
	// credential refreshes of one channel caused by an upstream 401 on the
	// subscriber usage endpoints.
	SubscriptionUsageRefreshCooldown = 2 * time.Minute
)

type subscriptionUsageCacheEntry struct {
	value     any
	err       error
	expiresAt time.Time
}

var (
	subscriptionUsageCacheMu sync.Mutex
	subscriptionUsageCache   = map[int]subscriptionUsageCacheEntry{}
	// subscriptionUsageGenerations counts the invalidations of each channel. A
	// fetch that started before an invalidation neither writes the cache nor
	// joins with fetches started after it.
	subscriptionUsageGenerations = map[int]uint64{}
	subscriptionUsageRefreshedAt = map[int]time.Time{}
	subscriptionUsageFlight      singleflight.Group
)

func cachedSubscriptionUsage[T any](channelId int, now time.Time) (value T, err error, ok bool) {
	subscriptionUsageCacheMu.Lock()
	entry, found := subscriptionUsageCache[channelId]
	subscriptionUsageCacheMu.Unlock()
	if !found || !now.Before(entry.expiresAt) {
		return value, nil, false
	}
	value, typed := entry.value.(T)
	return value, entry.err, typed
}

// LoadSubscriptionUsage returns the usage of channelId as last fetched, or
// runs fetch once for all concurrent callers. A successful result is kept for
// SubscriptionUsageCacheTTL; a failure (succeeded false or an error) for
// SubscriptionUsageFailureTTL, and is returned as the same failure.
func LoadSubscriptionUsage[T any](channelId int, now time.Time, fetch func() (value T, succeeded bool, err error)) (T, error) {
	if value, err, ok := cachedSubscriptionUsage[T](channelId, now); ok {
		return value, err
	}
	subscriptionUsageCacheMu.Lock()
	generation := subscriptionUsageGenerations[channelId]
	subscriptionUsageCacheMu.Unlock()

	flightKey := strconv.Itoa(channelId) + ":" + strconv.FormatUint(generation, 10)
	shared, err, _ := subscriptionUsageFlight.Do(flightKey, func() (any, error) {
		// A flight that finished just before this one may have stored the answer.
		if value, err, ok := cachedSubscriptionUsage[T](channelId, now); ok {
			return value, err
		}
		value, succeeded, err := fetch()
		ttl := SubscriptionUsageFailureTTL
		if err == nil && succeeded {
			ttl = SubscriptionUsageCacheTTL
		}
		subscriptionUsageCacheMu.Lock()
		if subscriptionUsageGenerations[channelId] == generation {
			subscriptionUsageCache[channelId] = subscriptionUsageCacheEntry{value: value, err: err, expiresAt: now.Add(ttl)}
		}
		subscriptionUsageCacheMu.Unlock()
		return value, err
	})
	value, _ := shared.(T)
	return value, err
}

// InvalidateSubscriptionUsage drops the cached usage of channelId, for
// example after a reset credit changed the windows. Fetches already in flight
// keep their answer to themselves.
func InvalidateSubscriptionUsage(channelId int) {
	subscriptionUsageCacheMu.Lock()
	delete(subscriptionUsageCache, channelId)
	subscriptionUsageGenerations[channelId]++
	subscriptionUsageCacheMu.Unlock()
}

// ClaimSubscriptionUsageRefresh reports whether a usage fetch of channelId
// that upstream answered with 401 may force a credential refresh now, and
// starts the SubscriptionUsageRefreshCooldown when it may.
func ClaimSubscriptionUsageRefresh(channelId int, now time.Time) bool {
	subscriptionUsageCacheMu.Lock()
	defer subscriptionUsageCacheMu.Unlock()
	if last, ok := subscriptionUsageRefreshedAt[channelId]; ok && now.Sub(last) < SubscriptionUsageRefreshCooldown {
		return false
	}
	subscriptionUsageRefreshedAt[channelId] = now
	return true
}
