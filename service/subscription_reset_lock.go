package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// subscriptionResetLockTTL outlasts one redemption request, including the
// token refresh and the single retry, so a crashed node frees the account.
const subscriptionResetLockTTL = 3 * time.Minute

const subscriptionResetLockReleaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`

// subscriptionResetLocks holds the running redemptions when Redis is disabled.
var subscriptionResetLocks sync.Map

// AcquireSubscriptionResetLock reserves the one subscriber reset-credit
// redemption that may run against a bound channel account at a time. ok is
// false while another redemption for the account is in flight; otherwise
// release must be called when the redemption ends.
func AcquireSubscriptionResetLock(channelId int) (release func(), ok bool, err error) {
	if !common.RedisEnabled {
		if _, running := subscriptionResetLocks.LoadOrStore(channelId, struct{}{}); running {
			return nil, false, nil
		}
		return func() { subscriptionResetLocks.Delete(channelId) }, true, nil
	}

	ctx := context.Background()
	key := fmt.Sprintf("subscription_reset:%d", channelId)
	owner := common.GetRandomString(16)
	acquired, err := common.RDB.SetNX(ctx, key, owner, subscriptionResetLockTTL).Result()
	if err != nil || !acquired {
		return nil, false, err
	}
	return func() {
		common.RDB.Eval(ctx, subscriptionResetLockReleaseScript, []string{key}, owner)
	}, true, nil
}
