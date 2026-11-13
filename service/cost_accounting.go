package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// standardPricingRelayInfo returns a shallow copy of info priced as if the
// group ratio were 1. Settlement code run on the copy yields the list price
// (standard_quota) through the exact path that produced the real charge. The
// copy never touches the original's billing session, clamp or snapshot.
func standardPricingRelayInfo(info *relaycommon.RelayInfo) *relaycommon.RelayInfo {
	standard := *info
	standard.PriceData.GroupRatioInfo.GroupRatio = 1
	standard.QuotaClamp = nil
	standard.FinalPreConsumedQuota = 0
	if snap := info.TieredBillingSnapshot; snap != nil {
		snapCopy := *snap
		snapCopy.GroupRatio = 1
		snapCopy.EstimatedQuotaAfterGroup = billingexpr.QuotaRound(snap.EstimatedQuotaBeforeGroup)
		standard.TieredBillingSnapshot = &snapCopy
	}
	return &standard
}

// standardQuotaFromGroupDelta converts a charge delta computed under
// groupRatio into list-price units. Async task settlement only keeps the
// frozen group ratio, so the list price of its adjustment is the delta
// without that factor; a free group (ratio 0) has no recoverable list price.
func standardQuotaFromGroupDelta(quota int, groupRatio float64) int {
	if quota <= 0 || groupRatio <= 0 {
		return 0
	}
	return common.QuotaRound(float64(quota) / groupRatio)
}

const costBackfillLogBatchSize = 500

// costBackfillHandler estimates cost accounting for data written before it
// existed: consume logs first, then quota_data. Every write is guarded by
// "standard_quota IS NULL", so a failed or interrupted run is resumed by
// starting the task again. Charged quota and balances are never modified.
type costBackfillHandler struct{}

func (costBackfillHandler) Type() string { return model.SystemTaskTypeCostBackfill }

func (costBackfillHandler) Run(ctx context.Context, task *model.SystemTask, runnerID string) {
	if err := runCostBackfill(ctx, NewSystemTaskProgressReporter(task, runnerID)); err != nil {
		if ctx.Err() != nil {
			logSystemTaskLockError(ctx, task, model.ErrSystemTaskLockLost)
			return
		}
		failSystemTask(task, runnerID, err)
		return
	}
	if err := model.FinishSystemTask(task.TaskID, runnerID, model.SystemTaskStatusSucceeded, nil, ""); err != nil {
		logSystemTaskLockError(ctx, task, err)
	}
}

func init() {
	RegisterSystemTaskHandler(costBackfillHandler{})
}

// runCostBackfill drives the batched backfill and reports progress.
func runCostBackfill(ctx context.Context, report func(processed, total int)) error {
	pendingLogs, pendingData, err := model.CountCostBackfillPending(ctx)
	if err != nil {
		return err
	}
	total := int(pendingLogs + pendingData)
	processed := 0
	report(processed, total)

	afterId := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastId, updated, err := model.BackfillLogCostBatch(ctx, afterId, costBackfillLogBatchSize)
		if err != nil {
			return err
		}
		if lastId == 0 {
			break
		}
		afterId = lastId
		processed += updated
		report(min(processed, total), total)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		updated, more, err := model.BackfillQuotaDataCostWindow(ctx)
		if err != nil {
			return err
		}
		if !more {
			break
		}
		if updated == 0 {
			return errors.New("cost backfill made no progress on quota_data")
		}
		processed += updated
		report(min(processed, total), total)
	}
	report(total, total)
	return nil
}

// StartCostBackfillTask queues the cost backfill, or returns the run already
// in progress.
func StartCostBackfillTask() (*model.SystemTask, error) {
	task, _, err := EnqueueSystemTask(model.SystemTaskTypeCostBackfill, struct{}{})
	return task, err
}

// CodexSubscription is what the ChatGPT id_token says about the plan.
type CodexSubscription struct {
	PlanType    string
	ActiveStart *int64
	ActiveUntil *int64
}

// ExtractCodexSubscriptionFromJWT reads chatgpt_plan_type,
// chatgpt_subscription_active_start and chatgpt_subscription_active_until
// from the "https://api.openai.com/auth" claim of a ChatGPT id_token. It only
// decodes the payload of a token already stored on the channel; it never
// verifies, sends or logs the token. Dates may be RFC 3339 strings or unix
// seconds; unparsable or missing values stay nil.
func ExtractCodexSubscriptionFromJWT(token string) (CodexSubscription, bool) {
	claims, ok := decodeJWTClaims(token)
	if !ok {
		return CodexSubscription{}, false
	}
	auth, ok := claims[codexJWTClaimPath].(map[string]any)
	if !ok {
		return CodexSubscription{}, false
	}
	sub := CodexSubscription{
		ActiveStart: codexClaimTime(auth["chatgpt_subscription_active_start"]),
		ActiveUntil: codexClaimTime(auth["chatgpt_subscription_active_until"]),
	}
	if plan, ok := auth["chatgpt_plan_type"].(string); ok {
		sub.PlanType = strings.TrimSpace(plan)
		if len(sub.PlanType) > 32 {
			sub.PlanType = sub.PlanType[:32]
		}
	}
	if sub.PlanType == "" && sub.ActiveStart == nil && sub.ActiveUntil == nil {
		return sub, false
	}
	return sub, true
}

func codexClaimTime(value any) *int64 {
	switch v := value.(type) {
	case float64:
		if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) || v > 1e11 {
			return nil
		}
		ts := int64(v)
		return &ts
	case string:
		v = strings.TrimSpace(v)
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999", "2006-01-02"} {
			if t, err := time.Parse(layout, v); err == nil {
				ts := t.Unix()
				return &ts
			}
		}
	}
	return nil
}

// SyncCodexChannelSubscription stores the plan and subscription window of a
// Codex channel from the id_token in its saved key. It is called when the
// account is saved and after every credential refresh, so a renewal moves
// the end date forward on the next refresh. Failures only leave the old
// values in place.
func SyncCodexChannelSubscription(channelID int) {
	ch, err := model.GetChannelById(channelID, true)
	if err != nil || ch == nil || ch.Type != constant.ChannelTypeCodex {
		return
	}
	key, err := parseCodexOAuthKey(strings.TrimSpace(ch.Key))
	if err != nil {
		return
	}
	syncCodexSubscriptionFromIDToken(channelID, key.IDToken)
}

func syncCodexSubscriptionFromIDToken(channelID int, idToken string) {
	sub, ok := ExtractCodexSubscriptionFromJWT(idToken)
	if !ok {
		return
	}
	if err := model.UpdateChannelSubscriptionWindow(channelID, sub.PlanType, sub.ActiveStart, sub.ActiveUntil, common.GetTimestamp()); err != nil {
		common.SysError(common.LogText("failed to store the Codex subscription window: channel_id=%d, error=%v", channelID, err))
	}
}
