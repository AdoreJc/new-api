package service

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
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
