package model

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// SubscriptionRouteBinding is the part of a user's active channel binding the
// distributor needs to steer a request: requests for models the bound channel
// serves in its dedicated group are routed to that group instead of the group
// the token would otherwise use. The user's own group is never changed.
type SubscriptionRouteBinding struct {
	SubscriptionId int    `json:"subscription_id"`
	ChannelId      int    `json:"channel_id"`
	Group          string `json:"group"`
	EndTime        int64  `json:"end_time"`
}

type subscriptionRouteCacheEntry[T any] struct {
	value     T
	expiresAt int64
}

type subscriptionRouteCache[K comparable, V any] struct {
	mu      sync.RWMutex
	entries map[K]subscriptionRouteCacheEntry[V]
}

func (c *subscriptionRouteCache[K, V]) get(key K) (V, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || entry.expiresAt <= time.Now().Unix() {
		var zero V
		return zero, false
	}
	return entry.value, true
}

func (c *subscriptionRouteCache[K, V]) set(key K, value V, ttlSeconds int) {
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[K]subscriptionRouteCacheEntry[V])
	}
	c.entries[key] = subscriptionRouteCacheEntry[V]{value: value, expiresAt: time.Now().Unix() + int64(ttlSeconds)}
	c.mu.Unlock()
}

func (c *subscriptionRouteCache[K, V]) delete(key K) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

func (c *subscriptionRouteCache[K, V]) clear() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

var (
	// user id -> binding (ChannelId 0 caches "no binding"); used when Redis is
	// not enabled, Redis being the shared layer otherwise, like the user cache.
	subscriptionRouteBindingMemory subscriptionRouteCache[int, SubscriptionRouteBinding]
	// "group\x00model\x00channel" -> served; only without the in-memory channel
	// cache, which answers from group2model2channels directly.
	subscriptionRouteModelMemory subscriptionRouteCache[string, bool]
)

func subscriptionRouteBindingCacheKey(userId int) string {
	return fmt.Sprintf("subscription_route_binding:%d", userId)
}

func loadSubscriptionRouteBindingDB(userId int) (SubscriptionRouteBinding, error) {
	var sub UserSubscription
	query := DB.Select("id", "bound_channel_id", "bound_group", "end_time").
		Where("user_id = ? AND bound_channel_id > 0 AND status = ?", userId, "active").
		Order("end_time desc, id desc").Limit(1).Find(&sub)
	if query.Error != nil {
		return SubscriptionRouteBinding{}, query.Error
	}
	if query.RowsAffected == 0 {
		return SubscriptionRouteBinding{}, nil
	}
	return SubscriptionRouteBinding{
		SubscriptionId: sub.Id,
		ChannelId:      sub.BoundChannelId,
		Group:          strings.TrimSpace(sub.BoundGroup),
		EndTime:        sub.EndTime,
	}, nil
}

func cachedSubscriptionRouteBinding(userId int) (SubscriptionRouteBinding, bool) {
	if !common.RedisEnabled {
		return subscriptionRouteBindingMemory.get(userId)
	}
	raw, err := common.RedisGet(subscriptionRouteBindingCacheKey(userId))
	if err != nil || raw == "" {
		return SubscriptionRouteBinding{}, false
	}
	var binding SubscriptionRouteBinding
	if err := common.UnmarshalJsonStr(raw, &binding); err != nil {
		return SubscriptionRouteBinding{}, false
	}
	return binding, true
}

func storeSubscriptionRouteBinding(userId int, binding SubscriptionRouteBinding) {
	ttl := userCacheTTLSeconds()
	if !common.RedisEnabled {
		subscriptionRouteBindingMemory.set(userId, binding, ttl)
		return
	}
	encoded, err := common.Marshal(binding)
	if err != nil {
		return
	}
	if err := common.RedisSet(subscriptionRouteBindingCacheKey(userId), string(encoded), time.Duration(ttl)*time.Second); err != nil {
		common.SysError(common.LogText("failed to cache subscription route binding for user %d: %v", userId, err))
	}
}

// InvalidateSubscriptionRouteBindingCache drops the cached binding of a user.
// Bind, unbind, invalidate, delete and expiry call it after they commit; the
// expiry time stored with the entry also ends routing on its own.
func InvalidateSubscriptionRouteBindingCache(userId int) {
	if userId <= 0 {
		return
	}
	if !common.RedisEnabled {
		subscriptionRouteBindingMemory.delete(userId)
		return
	}
	if err := common.RedisDelKey(subscriptionRouteBindingCacheKey(userId)); err != nil {
		common.SysError(common.LogText("failed to invalidate subscription route binding cache for user %d: %v", userId, err))
	}
}

// GetUserSubscriptionRouteBinding returns the binding that steers the user's
// requests at time now: the newest active subscription with a bound channel
// whose end time has not passed. A binding without a stored group (never
// written by the bind path) routes nothing.
func GetUserSubscriptionRouteBinding(userId int, now int64) (SubscriptionRouteBinding, bool) {
	if userId <= 0 {
		return SubscriptionRouteBinding{}, false
	}
	binding, found := cachedSubscriptionRouteBinding(userId)
	if !found {
		loaded, err := loadSubscriptionRouteBindingDB(userId)
		if err != nil {
			common.SysError(common.LogText("failed to load subscription route binding for user %d: %v", userId, err))
			return SubscriptionRouteBinding{}, false
		}
		binding = loaded
		storeSubscriptionRouteBinding(userId, binding)
	}
	if binding.ChannelId <= 0 || binding.Group == "" || binding.EndTime <= now {
		return SubscriptionRouteBinding{}, false
	}
	return binding, true
}

// subscriptionBoundChannelServesModel reports whether the bound channel has an
// enabled ability for modelName in its dedicated group. The in-memory channel
// cache answers directly; otherwise the ability lookup is cached until the
// channel cache is rebuilt (channel edits, bind/unbind, periodic sync) or the
// entry ages out.
func subscriptionBoundChannelServesModel(group string, modelName string, channelId int) bool {
	if common.MemoryCacheEnabled {
		return IsChannelEnabledForGroupModel(group, modelName, channelId)
	}
	key := group + "\x00" + modelName + "\x00" + strconv.Itoa(channelId)
	if served, ok := subscriptionRouteModelMemory.get(key); ok {
		return served
	}
	served := isChannelEnabledForGroupModelDB(group, modelName, channelId)
	subscriptionRouteModelMemory.set(key, served, userCacheTTLSeconds())
	return served
}

func clearSubscriptionRouteModelCache() {
	subscriptionRouteModelMemory.clear()
}

// ResolveSubscriptionRouteGroup returns the dedicated group a request of
// userId for modelName is steered to: the user holds an active channel binding
// and the bound channel serves the model in that group. Every other request
// (no binding, expired or cancelled subscription, model not on the channel,
// model or channel disabled) keeps its original group.
func ResolveSubscriptionRouteGroup(userId int, modelName string, now int64) (string, bool) {
	if modelName == "" {
		return "", false
	}
	binding, ok := GetUserSubscriptionRouteBinding(userId, now)
	if !ok {
		return "", false
	}
	if !subscriptionBoundChannelServesModel(binding.Group, modelName, binding.ChannelId) {
		return "", false
	}
	return binding.Group, true
}
