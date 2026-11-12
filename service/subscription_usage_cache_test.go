package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// useSubscriptionUsageClock makes cache entries written from now on dated at.
func useSubscriptionUsageClock(t *testing.T, at time.Time) {
	t.Helper()
	original := subscriptionUsageClock
	subscriptionUsageClock = func() time.Time { return at }
	t.Cleanup(func() { subscriptionUsageClock = original })
}

func TestSubscriptionUsageCacheKeepsSuccessesAndFailuresForTheirTTL(t *testing.T) {
	const channelId = 910001
	t.Cleanup(func() { InvalidateSubscriptionUsage(channelId) })
	start := time.Now()
	calls := 0
	answers := []struct {
		value     string
		succeeded bool
		err       error
	}{
		{"429", false, nil},
		{"", false, errors.New("no answer")},
		{"windows", true, nil},
		{"newer windows", true, nil},
	}
	load := func(at time.Time) (string, error) {
		useSubscriptionUsageClock(t, at)
		return LoadSubscriptionUsage(channelId, at, func() (string, bool, error) {
			answer := answers[calls]
			calls++
			return answer.value, answer.succeeded, answer.err
		})
	}

	value, err := load(start)
	require.NoError(t, err)
	assert.Equal(t, "429", value)
	value, _ = load(start.Add(SubscriptionUsageFailureTTL - time.Second))
	assert.Equal(t, "429", value, "an upstream failure status is served again within the failure TTL")
	assert.Equal(t, 1, calls)

	at := start.Add(SubscriptionUsageFailureTTL)
	_, err = load(at)
	assert.EqualError(t, err, "no answer")
	_, err = load(at.Add(SubscriptionUsageFailureTTL - time.Second))
	assert.EqualError(t, err, "no answer", "a fetch without an answer is a failure too")
	assert.Equal(t, 2, calls)

	at = at.Add(SubscriptionUsageFailureTTL)
	value, err = load(at)
	require.NoError(t, err)
	assert.Equal(t, "windows", value)
	value, _ = load(at.Add(SubscriptionUsageCacheTTL - time.Second))
	assert.Equal(t, "windows", value)
	assert.Equal(t, 3, calls)

	value, _ = load(at.Add(SubscriptionUsageCacheTTL))
	assert.Equal(t, "newer windows", value)
	assert.Equal(t, 4, calls)
}

// The TTL runs from when the fetch finished: a fetch that hung past the
// failure TTL (an upstream timeout) is still served to the next caller.
func TestSubscriptionUsageTTLStartsWhenTheFetchFinishes(t *testing.T) {
	const channelId = 910005
	t.Cleanup(func() { InvalidateSubscriptionUsage(channelId) })
	start := time.Now()
	finished := start.Add(SubscriptionUsageFailureTTL + 5*time.Second)
	calls := 0
	_, err := LoadSubscriptionUsage(channelId, start, func() (string, bool, error) {
		calls++
		useSubscriptionUsageClock(t, finished)
		return "", false, errors.New("upstream timeout")
	})
	assert.EqualError(t, err, "upstream timeout")

	_, err = LoadSubscriptionUsage(channelId, finished, func() (string, bool, error) {
		calls++
		return "unexpected fetch", true, nil
	})
	assert.EqualError(t, err, "upstream timeout", "the failure is served right after the slow fetch")
	assert.Equal(t, 1, calls)
}

// A fetch that was in flight when the usage was invalidated (a reset credit
// was redeemed meanwhile) must not put its older windows back in the cache,
// nor answer callers that arrive after the invalidation.
func TestSubscriptionUsageInvalidationDiscardsTheFetchInFlight(t *testing.T) {
	const channelId = 910002
	t.Cleanup(func() { InvalidateSubscriptionUsage(channelId) })
	now := time.Now()
	started := make(chan struct{})
	release := make(chan struct{})
	staleDone := make(chan string)
	go func() {
		value, _ := LoadSubscriptionUsage(channelId, now, func() (string, bool, error) {
			close(started)
			<-release
			return "before reset", true, nil
		})
		staleDone <- value
	}()
	<-started

	InvalidateSubscriptionUsage(channelId)
	value, err := LoadSubscriptionUsage(channelId, now, func() (string, bool, error) {
		return "after reset", true, nil
	})
	require.NoError(t, err)
	assert.Equal(t, "after reset", value, "a caller after the invalidation does not join the older fetch")

	close(release)
	assert.Equal(t, "before reset", <-staleDone, "the older fetch still answers its own caller")

	value, _ = LoadSubscriptionUsage(channelId, now, func() (string, bool, error) {
		return "unexpected fetch", true, nil
	})
	assert.Equal(t, "after reset", value, "the older fetch did not overwrite the cache")
}

func TestSubscriptionUsageRefreshCooldownIsPerChannel(t *testing.T) {
	now := time.Now()
	assert.True(t, ClaimSubscriptionUsageRefresh(910003, now))
	assert.False(t, ClaimSubscriptionUsageRefresh(910003, now.Add(SubscriptionUsageRefreshCooldown-time.Second)))
	assert.True(t, ClaimSubscriptionUsageRefresh(910004, now), "another channel has its own cooldown")
	assert.True(t, ClaimSubscriptionUsageRefresh(910003, now.Add(SubscriptionUsageRefreshCooldown)))
}
