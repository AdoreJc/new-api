package service

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	routeUserId        = 41
	routeTokenId       = 41
	routeTokenKey      = "sk-route-key"
	routeBoundSubId    = 41
	routeSharedSubId   = 42
	routeBoundPlanId   = 41
	routeSharedPlanId  = 42
	routeBoundChannel  = 7
	routeBoundGroup    = "claude-ch7"
	routeWalletQuota   = 50000
	routeTokenQuota    = 50000
	routeSubTotal      = 1000
	routeRequestQuota  = 300
	routeOriginalGroup = "default"
)

type subscriptionRouteFixture struct {
	t *testing.T
}

// newSubscriptionRouteFixture seeds a user holding a subscription bound to
// channel 7 (dedicated group claude-ch7) and an ordinary subscription, each
// with routeSubTotal quota, plus wallet and token quota.
func newSubscriptionRouteFixture(t *testing.T) *subscriptionRouteFixture {
	t.Helper()
	require.NoError(t, model.DB.AutoMigrate(&model.SubscriptionPlan{}, &model.SubscriptionPreConsumeRecord{}))
	truncate(t)
	t.Cleanup(func() {
		model.DB.Exec("DELETE FROM subscription_plans")
		model.DB.Exec("DELETE FROM subscription_pre_consume_records")
		model.InvalidateSubscriptionPlanCache(routeBoundPlanId)
		model.InvalidateSubscriptionPlanCache(routeSharedPlanId)
		model.InvalidateSubscriptionRouteBindingCache(routeUserId)
	})
	seedUser(t, routeUserId, routeWalletQuota)
	seedToken(t, routeTokenId, routeUserId, routeTokenKey, routeTokenQuota)
	for _, plan := range []*model.SubscriptionPlan{
		{Id: routeBoundPlanId, Title: "Dedicated", PriceAmount: 1, TotalAmount: routeSubTotal},
		{Id: routeSharedPlanId, Title: "Shared", PriceAmount: 1, TotalAmount: routeSubTotal},
	} {
		plan.NormalizeDefaults()
		require.NoError(t, model.DB.Create(plan).Error)
		model.InvalidateSubscriptionPlanCache(plan.Id)
	}
	now := time.Now()
	for _, sub := range []*model.UserSubscription{
		{Id: routeBoundSubId, PlanId: routeBoundPlanId, BoundChannelId: routeBoundChannel, BoundGroup: routeBoundGroup, AllowWalletOverflow: true},
		{Id: routeSharedSubId, PlanId: routeSharedPlanId, AllowWalletOverflow: true},
	} {
		sub.UserId = routeUserId
		sub.AmountTotal = routeSubTotal
		sub.Status = "active"
		sub.StartTime = now.Unix()
		sub.EndTime = now.Add(30 * 24 * time.Hour).Unix()
		require.NoError(t, model.DB.Create(sub).Error)
	}
	model.InvalidateSubscriptionRouteBindingCache(routeUserId)
	return &subscriptionRouteFixture{t: t}
}

func (f *subscriptionRouteFixture) setPlanFallback(enabled bool) {
	require.NoError(f.t, model.DB.Model(&model.SubscriptionPlan{}).Where("id = ?", routeBoundPlanId).
		Update("fallback_to_shared_pool_on_limit", enabled).Error)
	model.InvalidateSubscriptionPlanCache(routeBoundPlanId)
}

// request builds the context and relay info of one request. A non-nil route
// marks it as steered to the bound account by the distributor.
func (f *subscriptionRouteFixture) request(requestId string, preference string, route *relaycommon.SubscriptionRoute) (*gin.Context, *relaycommon.RelayInfo) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Set("id", routeUserId)
	usingGroup := routeOriginalGroup
	if route != nil {
		usingGroup = route.Group
		common.SetContextKey(c, constant.ContextKeySubscriptionRoute, route)
	}
	common.SetContextKey(c, constant.ContextKeyUsingGroup, usingGroup)
	info := &relaycommon.RelayInfo{
		RequestId:         requestId,
		UserId:            routeUserId,
		TokenId:           routeTokenId,
		TokenKey:          routeTokenKey,
		TokenGroup:        routeOriginalGroup,
		UsingGroup:        usingGroup,
		OriginModelName:   "claude-sonnet-4-5",
		UserSetting:       dto.UserSetting{BillingPreference: preference},
		SubscriptionRoute: route,
	}
	info.PriceData.QuotaToPreConsume = routeRequestQuota
	return c, info
}

func boundRoute(originalGroup string) *relaycommon.SubscriptionRoute {
	return &relaycommon.SubscriptionRoute{
		SubscriptionId: routeBoundSubId,
		ChannelId:      routeBoundChannel,
		Group:          routeBoundGroup,
		OriginalGroup:  originalGroup,
	}
}

func preConsumeRecordStatus(t *testing.T, requestId string) string {
	t.Helper()
	var record model.SubscriptionPreConsumeRecord
	require.NoError(t, model.DB.Where("request_id = ?", requestId).First(&record).Error)
	return record.Status
}

func TestSubscriptionRouteBillingAttribution(t *testing.T) {
	tests := []struct {
		name          string
		preference    string
		routed        bool
		dropShared    bool
		wantSource    string
		wantBoundUsed int64
		wantShareUsed int64
		wantWallet    int
	}{
		// A steered request pays from the bound subscription only, whatever
		// the preference and even with another subscription available.
		{name: "routed subscription_first", preference: "subscription_first", routed: true, wantSource: BillingSourceSubscription, wantBoundUsed: routeRequestQuota, wantWallet: routeWalletQuota},
		{name: "routed wallet_first ignores preference", preference: "wallet_first", routed: true, wantSource: BillingSourceSubscription, wantBoundUsed: routeRequestQuota, wantWallet: routeWalletQuota},
		{name: "routed wallet_only ignores preference", preference: "wallet_only", routed: true, wantSource: BillingSourceSubscription, wantBoundUsed: routeRequestQuota, wantWallet: routeWalletQuota},
		// Other requests never touch the bound subscription.
		{name: "unrouted uses the ordinary subscription", preference: "subscription_first", wantSource: BillingSourceSubscription, wantShareUsed: routeRequestQuota, wantWallet: routeWalletQuota},
		{name: "unrouted subscription_only uses the ordinary subscription", preference: "subscription_only", wantSource: BillingSourceSubscription, wantShareUsed: routeRequestQuota, wantWallet: routeWalletQuota},
		{name: "unrouted with only a bound subscription uses the wallet", preference: "subscription_first", dropShared: true, wantSource: BillingSourceWallet, wantWallet: routeWalletQuota - routeRequestQuota},
		{name: "unrouted wallet_first uses the wallet", preference: "wallet_first", wantSource: BillingSourceWallet, wantWallet: routeWalletQuota - routeRequestQuota},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newSubscriptionRouteFixture(t)
			if tt.dropShared {
				require.NoError(t, model.DB.Delete(&model.UserSubscription{}, routeSharedSubId).Error)
			}
			var route *relaycommon.SubscriptionRoute
			if tt.routed {
				route = boundRoute(routeOriginalGroup)
			}
			c, info := (&subscriptionRouteFixture{t: t}).request("req-"+tt.name, tt.preference, route)

			apiErr := PreConsumeBilling(c, routeRequestQuota, info)

			require.Nil(t, apiErr)
			assert.Equal(t, tt.wantSource, info.BillingSource)
			assert.Equal(t, tt.wantBoundUsed, getSubscriptionUsed(t, routeBoundSubId))
			if !tt.dropShared {
				assert.Equal(t, tt.wantShareUsed, getSubscriptionUsed(t, routeSharedSubId))
			}
			assert.Equal(t, tt.wantWallet, getUserQuota(t, routeUserId))
		})
	}
}

func TestSubscriptionRouteBoundSubscriptionShortDoesNotUseWallet(t *testing.T) {
	f := newSubscriptionRouteFixture(t)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", routeBoundSubId).Update("amount_used", routeSubTotal-100).Error)
	c, info := f.request("req-bound-short", "subscription_first", boundRoute(routeOriginalGroup))

	apiErr := PreConsumeBilling(c, routeRequestQuota, info)

	require.NotNil(t, apiErr)
	assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
	assert.Contains(t, apiErr.Error(), logger.FormatQuota(100))
	assert.Contains(t, apiErr.Error(), logger.FormatQuota(routeRequestQuota))
	assert.Nil(t, info.Billing)
	assert.Equal(t, int64(routeSubTotal-100), getSubscriptionUsed(t, routeBoundSubId))
	assert.Zero(t, getSubscriptionUsed(t, routeSharedSubId), "the ordinary subscription does not pay for the bound account")
	assert.Equal(t, routeWalletQuota, getUserQuota(t, routeUserId))
	assert.Equal(t, routeTokenQuota, getTokenRemainQuota(t, routeTokenId), "the token reservation is rolled back")
}

func TestSubscriptionRouteWalletOverflowIgnoresBoundSubscriptions(t *testing.T) {
	t.Run("bound subscription without overflow does not block the wallet", func(t *testing.T) {
		f := newSubscriptionRouteFixture(t)
		require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", routeBoundSubId).Update("allow_wallet_overflow", false).Error)
		require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", routeSharedSubId).Update("amount_used", routeSubTotal).Error)
		c, info := f.request("req-overflow-allowed", "subscription_first", nil)

		require.Nil(t, PreConsumeBilling(c, routeRequestQuota, info))
		assert.Equal(t, BillingSourceWallet, info.BillingSource)
		assert.Equal(t, routeWalletQuota-routeRequestQuota, getUserQuota(t, routeUserId))
		assert.Zero(t, getSubscriptionUsed(t, routeBoundSubId))
	})
	t.Run("ordinary subscription without overflow still blocks the wallet", func(t *testing.T) {
		f := newSubscriptionRouteFixture(t)
		require.NoError(t, model.DB.Model(&model.UserSubscription{}).Where("id = ?", routeSharedSubId).Updates(map[string]any{"amount_used": routeSubTotal, "allow_wallet_overflow": false}).Error)
		c, info := f.request("req-overflow-blocked", "subscription_first", nil)

		apiErr := PreConsumeBilling(c, routeRequestQuota, info)
		require.NotNil(t, apiErr)
		assert.Equal(t, types.ErrorCodeInsufficientUserQuota, apiErr.GetErrorCode())
		assert.Equal(t, routeWalletQuota, getUserQuota(t, routeUserId))
	})
}

func TestDetectSubscriptionAccountLimit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	reset5h := now.Add(2 * time.Hour).Unix()
	reset7d := now.Add(3 * 24 * time.Hour).Unix()
	headers := func(pairs ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(pairs); i += 2 {
			h.Set(pairs[i], pairs[i+1])
		}
		return h
	}
	status := func(code int) *types.NewAPIError {
		return types.NewOpenAIError(assert.AnError, types.ErrorCodeBadResponseStatusCode, code)
	}
	tests := []struct {
		name        string
		err         *types.NewAPIError
		headers     http.Header
		passive     map[string]ClaudePassiveUsageWindow
		wantLimited bool
		want        SubscriptionAccountLimit
	}{
		{
			name:        "429 with the 5h window rejected",
			err:         status(http.StatusTooManyRequests),
			headers:     headers("anthropic-ratelimit-unified-5h-status", "rejected", "anthropic-ratelimit-unified-5h-reset", "1800007200"),
			wantLimited: true,
			want:        SubscriptionAccountLimit{Window: "5h", ResetAt: reset5h},
		},
		{
			name: "both windows exhausted reports the later reset",
			err:  status(http.StatusTooManyRequests),
			headers: headers("anthropic-ratelimit-unified-5h-status", "rejected", "anthropic-ratelimit-unified-5h-reset", "1800007200",
				"anthropic-ratelimit-unified-7d-utilization", "1.0", "anthropic-ratelimit-unified-7d-reset", "1800259200"),
			wantLimited: true,
			want:        SubscriptionAccountLimit{Window: "7d", ResetAt: reset7d},
		},
		{
			name:        "rate_limit_error without status 429",
			err:         types.WithClaudeError(types.ClaudeError{Type: "rate_limit_error", Message: "limited"}, http.StatusBadRequest),
			wantLimited: true,
		},
		{
			name:        "codex usage_limit_reached",
			err:         types.WithOpenAIError(types.OpenAIError{Type: "usage_limit_reached", Message: "The usage limit has been reached"}, http.StatusTooManyRequests),
			wantLimited: true,
		},
		{
			name:        "no headers falls back to the passive sample",
			err:         status(http.StatusTooManyRequests),
			passive:     map[string]ClaudePassiveUsageWindow{"five_hour": {Utilization: 100, ResetsAt: reset5h, SampledAt: now.Unix()}, "seven_day": {Utilization: 40, ResetsAt: reset7d, SampledAt: now.Unix()}},
			wantLimited: true,
			want:        SubscriptionAccountLimit{Window: "5h", ResetAt: reset5h},
		},
		{
			name:        "a reset in the past is not shown",
			err:         status(http.StatusTooManyRequests),
			headers:     headers("anthropic-ratelimit-unified-5h-status", "rejected", "anthropic-ratelimit-unified-5h-reset", "1799990000"),
			wantLimited: true,
			want:        SubscriptionAccountLimit{Window: "5h"},
		},
		{
			name:    "server error is not a usage limit",
			err:     status(http.StatusInternalServerError),
			headers: headers("anthropic-ratelimit-unified-5h-status", "allowed", "anthropic-ratelimit-unified-5h-utilization", "0.4"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, limited := DetectSubscriptionAccountLimit(tt.err, tt.headers, tt.passive, now)
			assert.Equal(t, tt.wantLimited, limited)
			assert.Equal(t, tt.want, limit)
		})
	}
}

// boundAccountLimited prepares a failed attempt on the bound channel whose
// answer was a 5h window rejection resetting in two hours.
func boundAccountLimited(c *gin.Context) (*types.NewAPIError, int64) {
	resetAt := time.Now().Add(2 * time.Hour).Unix()
	headers := http.Header{}
	headers.Set("anthropic-ratelimit-unified-5h-status", "rejected")
	headers.Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(resetAt, 10))
	common.SetContextKey(c, constant.ContextKeyChannelId, routeBoundChannel)
	RecordSubscriptionLimitHeaders(c, headers)
	return types.NewOpenAIError(assert.AnError, types.ErrorCodeBadResponseStatusCode, http.StatusTooManyRequests), resetAt
}

func TestHandleSubscriptionRouteFailureWithoutFallback(t *testing.T) {
	f := newSubscriptionRouteFixture(t)
	c, info := f.request("req-limit-off", "subscription_first", boundRoute(routeOriginalGroup))
	require.Nil(t, PreConsumeBilling(c, routeRequestQuota, info))
	upstreamErr, resetAt := boundAccountLimited(c)
	retry := &RetryParam{Ctx: c, TokenGroup: RelayRetryGroup(info), Retry: common.GetPointer(1)}
	assert.Equal(t, routeBoundGroup, retry.TokenGroup, "retries of a steered request stay in the dedicated group")

	apiErr, fellBack := HandleSubscriptionRouteFailure(c, info, retry, upstreamErr, false)

	assert.False(t, fellBack)
	require.NotNil(t, apiErr)
	assert.Equal(t, ErrorCodeSubscriptionAccountLimited, apiErr.GetErrorCode())
	assert.Equal(t, http.StatusTooManyRequests, apiErr.StatusCode)
	assert.True(t, types.IsSkipRetryError(apiErr))
	assert.Contains(t, apiErr.Error(), "5-hour")
	assert.Contains(t, apiErr.Error(), time.Unix(resetAt, 0).Format("2006-01-02 15:04"))
	assert.Equal(t, routeBoundGroup, retry.TokenGroup)
	assert.True(t, info.SubscriptionRoute.Active())

	// The failed request refunds the bound subscription through the normal path.
	info.Billing.Refund(c)
	require.Eventually(t, func() bool { return getSubscriptionUsed(t, routeBoundSubId) == 0 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, routeWalletQuota, getUserQuota(t, routeUserId))
}

func TestHandleSubscriptionRouteFailureFallsBackToSharedPool(t *testing.T) {
	f := newSubscriptionRouteFixture(t)
	f.setPlanFallback(true)
	c, info := f.request("req-limit-on", "subscription_first", boundRoute(routeOriginalGroup))
	require.Nil(t, PreConsumeBilling(c, routeRequestQuota, info))
	require.Equal(t, int64(routeRequestQuota), getSubscriptionUsed(t, routeBoundSubId))
	upstreamErr, _ := boundAccountLimited(c)
	retry := &RetryParam{Ctx: c, TokenGroup: RelayRetryGroup(info), Retry: common.GetPointer(2)}

	apiErr, fellBack := HandleSubscriptionRouteFailure(c, info, retry, upstreamErr, false)

	require.True(t, fellBack)
	assert.Same(t, upstreamErr, apiErr)
	// The retry continues in the original group with a fresh attempt budget.
	assert.Equal(t, routeOriginalGroup, retry.TokenGroup)
	retry.IncreaseRetry()
	assert.Zero(t, retry.GetRetry())
	assert.Equal(t, routeOriginalGroup, common.GetContextKeyString(c, constant.ContextKeyUsingGroup))
	assert.Equal(t, routeOriginalGroup, info.UsingGroup)
	// The bound subscription reservation is refunded synchronously and the
	// wallet reserves the request instead.
	assert.Zero(t, getSubscriptionUsed(t, routeBoundSubId))
	assert.Equal(t, "refunded", preConsumeRecordStatus(t, "req-limit-on"))
	assert.Zero(t, getSubscriptionUsed(t, routeSharedSubId))
	assert.Equal(t, routeWalletQuota-routeRequestQuota, getUserQuota(t, routeUserId))
	assert.Equal(t, routeTokenQuota-routeRequestQuota, getTokenRemainQuota(t, routeTokenId))
	assert.Equal(t, BillingSourceWallet, info.BillingSource)
	assert.Equal(t, "5h", info.SubscriptionRoute.LimitWindow)
	assert.Equal(t, routeOriginalGroup, RelayRetryGroup(info))

	// Idempotent per request id: the route no longer applies, the bound
	// subscription cannot be charged again, and the wallet is charged once.
	again, fellBackAgain := HandleSubscriptionRouteFailure(c, info, retry, upstreamErr, false)
	assert.False(t, fellBackAgain)
	assert.Same(t, upstreamErr, again)
	_, err := model.PreConsumeBoundUserSubscription("req-limit-on", routeUserId, routeBoundSubId, routeRequestQuota)
	assert.ErrorContains(t, err, "already refunded")
	assert.Zero(t, getSubscriptionUsed(t, routeBoundSubId))
	assert.Equal(t, routeWalletQuota-routeRequestQuota, getUserQuota(t, routeUserId))

	// The consume log tells the fallback apart.
	other := model.NewLogOther()
	appendBillingInfo(info, other)
	fallback, ok := other.Snapshot()["subscription_fallback"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "bound_account_limited", fallback["reason"])
	assert.Equal(t, routeBoundGroup, fallback["from_group"])
	assert.Equal(t, routeOriginalGroup, fallback["to_group"])

	// If the shared pool fails as well, only the wallet reservation is refunded.
	info.Billing.Refund(c)
	require.Eventually(t, func() bool { return getUserQuota(t, routeUserId) == routeWalletQuota }, 2*time.Second, 10*time.Millisecond)
	assert.Zero(t, getSubscriptionUsed(t, routeBoundSubId))
}

func TestHandleSubscriptionRouteFailureAutoExcludesDedicatedGroup(t *testing.T) {
	configureRequestAutoGroupsTest(t)
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(`{"default":1,"vip":1,"svip":1,"claude-ch7":1}`))
	f := newSubscriptionRouteFixture(t)
	f.setPlanFallback(true)
	c, info := f.request("req-limit-auto", "subscription_first", boundRoute("auto"))
	common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
	require.Equal(t, []string{routeBoundGroup, "vip", "default", "svip"}, GetRequestAutoGroups(c, "default"))
	require.Nil(t, PreConsumeBilling(c, routeRequestQuota, info))
	upstreamErr, _ := boundAccountLimited(c)
	retry := &RetryParam{Ctx: c, TokenGroup: RelayRetryGroup(info), Retry: common.GetPointer(0)}

	_, fellBack := HandleSubscriptionRouteFailure(c, info, retry, upstreamErr, false)

	require.True(t, fellBack)
	assert.Equal(t, "auto", retry.TokenGroup)
	assert.Equal(t, []string{"vip", "default", "svip"}, GetRequestAutoGroups(c, "default"))
}

func TestHandleSubscriptionRouteFailureKeepsTheBoundAccount(t *testing.T) {
	tests := []struct {
		name            string
		originalGroup   string
		responseStarted bool
		walletQuota     int
		upstreamStatus  int
		wantCode        types.ErrorCode
	}{
		{name: "response already started", originalGroup: routeOriginalGroup, responseStarted: true, walletQuota: routeWalletQuota, upstreamStatus: http.StatusTooManyRequests, wantCode: ErrorCodeSubscriptionAccountLimited},
		{name: "token already uses the dedicated group", originalGroup: routeBoundGroup, walletQuota: routeWalletQuota, upstreamStatus: http.StatusTooManyRequests, wantCode: ErrorCodeSubscriptionAccountLimited},
		{name: "wallet cannot cover the request", originalGroup: routeOriginalGroup, walletQuota: 100, upstreamStatus: http.StatusTooManyRequests, wantCode: types.ErrorCodeInsufficientUserQuota},
		{name: "not a usage limit", originalGroup: routeOriginalGroup, walletQuota: routeWalletQuota, upstreamStatus: http.StatusInternalServerError, wantCode: types.ErrorCodeBadResponseStatusCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSubscriptionRouteFixture(t)
			f.setPlanFallback(true)
			require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", routeUserId).Update("quota", tt.walletQuota).Error)
			c, info := f.request("req-keep-"+tt.name, "subscription_first", boundRoute(tt.originalGroup))
			require.Nil(t, PreConsumeBilling(c, routeRequestQuota, info))
			limitErr, _ := boundAccountLimited(c)
			upstreamErr := limitErr
			if tt.upstreamStatus != http.StatusTooManyRequests {
				RecordSubscriptionLimitHeaders(c, nil)
				upstreamErr = types.NewOpenAIError(assert.AnError, types.ErrorCodeBadResponseStatusCode, tt.upstreamStatus)
			}
			retry := &RetryParam{Ctx: c, TokenGroup: RelayRetryGroup(info), Retry: common.GetPointer(0)}

			apiErr, fellBack := HandleSubscriptionRouteFailure(c, info, retry, upstreamErr, tt.responseStarted)

			assert.False(t, fellBack)
			require.NotNil(t, apiErr)
			assert.Equal(t, tt.wantCode, apiErr.GetErrorCode())
			assert.NotEqual(t, routeOriginalGroup, retry.TokenGroup)
			assert.Equal(t, tt.walletQuota, getUserQuota(t, routeUserId), "the wallet is not charged")
		})
	}
}
