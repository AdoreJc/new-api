package model

import (
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// Fixed monthly costs (subscription accounts) are amortized per calendar day
// in the server's time zone:
//
//	daily cost  = monthly cost / days in that month
//	amount(day) = daily cost × seconds of the day inside both the selected
//	              range and the cost window / seconds in the day
//
// Days outside the cost window, and the part of the range after now, cost
// nothing. The window is the manual effective_from/to, falling back to the
// subscription start/until parsed from the account.

// maxAmortizedDays bounds the per-day loop for absurd ranges.
const maxAmortizedDays = 3700

// AmortizeMonthlyCost returns the cost in USD of a monthly amount per day
// start (unix seconds in loc) for the overlap of [start, end) and the window
// [from, to). A nil bound is open.
func AmortizeMonthlyCost(monthlyUSD float64, start, end int64, from, to *int64, loc *time.Location) map[int64]float64 {
	result := map[int64]float64{}
	if monthlyUSD <= 0 || math.IsNaN(monthlyUSD) || math.IsInf(monthlyUSD, 0) {
		return result
	}
	if from != nil {
		start = max(start, *from)
	}
	if to != nil {
		end = min(end, *to)
	}
	if end <= start {
		return result
	}
	t := time.Unix(start, 0).In(loc)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	for range maxAmortizedDays {
		next := day.AddDate(0, 0, 1)
		dayStart, dayEnd := day.Unix(), next.Unix()
		if dayStart >= end {
			break
		}
		overlap := min(end, dayEnd) - max(start, dayStart)
		if overlap > 0 {
			daysInMonth := time.Date(day.Year(), day.Month()+1, 0, 0, 0, 0, 0, loc).Day()
			result[dayStart] = monthlyUSD / float64(daysInMonth) * float64(overlap) / float64(dayEnd-dayStart)
		}
		day = next
	}
	return result
}

// FixedCostChannel describes one fixed-cost channel in a cost summary.
type FixedCostChannel struct {
	ChannelID      int      `json:"channel_id"`
	ChannelName    string   `json:"channel_name"`
	ChannelType    int      `json:"channel_type"`
	MonthlyCostUSD *float64 `json:"monthly_cost_usd"`
	EffectiveFrom  *int64   `json:"effective_from"`
	EffectiveTo    *int64   `json:"effective_to"`
	FromAuto       bool     `json:"effective_from_auto"`
	ToAuto         bool     `json:"effective_to_auto"`
	PlanType       string   `json:"plan_type,omitempty"`
	// Expired: the subscription window ended and was not renewed, so
	// amortization stopped at its end.
	Expired bool `json:"expired"`
	// Missing: no monthly cost entered yet; nothing is amortized.
	Missing bool `json:"missing"`
	// CostQuota is the amount amortized over the selected range.
	CostQuota int64 `json:"cost_quota"`
}

type fixedCostWeight struct {
	ChannelID int
	CreatedAt int64
	UseGroup  string
	ModelName string
	UserID    int
	Username  string
	Weight    float64
}

func (w fixedCostWeight) rowKey(dimension CostDimension) string {
	return costRowKey(dimension, w.UseGroup, w.ChannelID, w.ModelName, w.UserID)
}

func costRowKey(dimension CostDimension, group string, channelID int, modelName string, userID int) string {
	switch dimension {
	case CostDimensionGroup:
		return "g:" + group
	case CostDimensionChannel:
		return "c:" + strconv.Itoa(channelID)
	case CostDimensionModel:
		return "m:" + modelName
	default:
		return "u:" + strconv.Itoa(userID)
	}
}

// applyFixedCosts amortizes every fixed-cost channel over the range and adds
// the result to the summary rows. On the channel breakdown the cost stays on
// its channel; on the other breakdowns each day's cost of a channel is split
// by the list-price share of that day's requests on the channel. A day
// without requests is reported as unallocated. With a username filter only
// that user's share is counted.
func applyFixedCosts(summary *CostSummary, startTime, endTime int64, username string, dimension CostDimension, now int64, loc *time.Location) error {
	var channels []Channel
	if err := DB.Model(&Channel{}).
		Select("id, name, type, cost_mode, monthly_cost_usd, cost_effective_from, cost_effective_to, subscription_plan_type, subscription_active_start, subscription_active_until").
		Find(&channels).Error; err != nil {
		return err
	}
	end := min(endTime+1, now)
	amounts := map[int]map[int64]float64{}
	channelTotals := map[int]int64{}
	var fixedIDs []int
	for i := range channels {
		channel := &channels[i]
		if channel.GetCostMode() != ChannelCostModeFixedMonthly {
			continue
		}
		from, fromAuto, to, toAuto := channel.FixedCostWindow()
		if (from != nil && *from > endTime) || (to != nil && *to < startTime) {
			continue
		}
		info := FixedCostChannel{
			ChannelID: channel.Id, ChannelName: channel.Name, ChannelType: channel.Type,
			MonthlyCostUSD: channel.MonthlyCostUSD, EffectiveFrom: from, EffectiveTo: to,
			FromAuto: fromAuto, ToAuto: toAuto, PlanType: channel.SubscriptionPlanType,
			Expired: toAuto && to != nil && *to < now,
			Missing: channel.MonthlyCostUSD == nil,
		}
		if !info.Missing {
			perDay := AmortizeMonthlyCost(*channel.MonthlyCostUSD, startTime, end, from, to, loc)
			total := 0.0
			for _, usd := range perDay {
				total += usd
			}
			info.CostQuota = int64(math.Round(total * common.QuotaPerUnit))
			channelTotals[channel.Id] = info.CostQuota
			if len(perDay) > 0 {
				amounts[channel.Id] = perDay
				fixedIDs = append(fixedIDs, channel.Id)
			}
		}
		summary.FixedChannels = append(summary.FixedChannels, info)
	}
	if len(fixedIDs) == 0 {
		return nil
	}

	var weights []fixedCostWeight
	if err := DB.Table("quota_data").
		Select("channel_id, created_at, use_group, model_name, user_id, username, COALESCE(sum(COALESCE(standard_quota, quota)), 0) as weight").
		Where("channel_id IN ? AND created_at >= ? AND created_at <= ?", fixedIDs, startTime, endTime).
		Group("channel_id, created_at, use_group, model_name, user_id, username").
		Scan(&weights).Error; err != nil {
		return err
	}
	type dayKey struct {
		channelID int
		day       int64
	}
	dayWeights := map[dayKey][]fixedCostWeight{}
	dayTotals := map[dayKey]float64{}
	for _, w := range weights {
		t := time.Unix(w.CreatedAt, 0).In(loc)
		key := dayKey{w.ChannelID, time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).Unix()}
		dayWeights[key] = append(dayWeights[key], w)
		dayTotals[key] += w.Weight
	}

	rows := map[string]*CostSummaryRow{}
	for _, row := range summary.Rows {
		rows[costRowKey(dimension, row.UseGroup, row.ChannelID, row.ModelName, row.UserID)] = row
	}
	rowFor := func(key string, template CostSummaryRow) {
		if rows[key] == nil {
			row := template
			rows[key] = &row
			summary.Rows = append(summary.Rows, &row)
		}
	}
	const unallocatedKey = "unallocated"
	for channelID, perDay := range amounts {
		shares := map[string]float64{}
		for day, usd := range perDay {
			amount := usd * common.QuotaPerUnit
			key := dayKey{channelID, day}
			total := dayTotals[key]
			if total <= 0 {
				if username != "" {
					continue
				}
				if dimension == CostDimensionChannel {
					rowKey := costRowKey(dimension, "", channelID, "", 0)
					rowFor(rowKey, CostSummaryRow{ChannelID: channelID})
					shares[rowKey] += amount
				} else {
					shares[unallocatedKey] += amount
				}
				continue
			}
			for _, w := range dayWeights[key] {
				if username != "" && w.Username != username {
					continue
				}
				rowKey := w.rowKey(dimension)
				template := CostSummaryRow{}
				switch dimension {
				case CostDimensionGroup:
					template.UseGroup = w.UseGroup
				case CostDimensionChannel:
					template.ChannelID = w.ChannelID
				case CostDimensionModel:
					template.ModelName = w.ModelName
				default:
					template.UserID, template.Username = w.UserID, w.Username
				}
				rowFor(rowKey, template)
				shares[rowKey] += amount * w.Weight / total
			}
		}
		// Every breakdown hands out exactly the channel's rounded total, so
		// the channel, group, model and user views add up to the same sum.
		target := channelTotals[channelID]
		if username != "" {
			sum := 0.0
			for _, share := range shares {
				sum += share
			}
			target = int64(math.Round(sum))
		}
		for rowKey, quota := range largestRemainder(shares, target) {
			if rowKey == unallocatedKey {
				rowFor(unallocatedKey, CostSummaryRow{Unallocated: true})
			}
			rows[rowKey].FixedCostQuota += quota
			summary.Total.FixedCostQuota += quota
		}
	}
	return nil
}

// largestRemainder splits target into integers proportional to shares: each
// key gets the floor of its share, and the units left go to the largest
// fractional parts (ties by key), so the parts always sum to target.
func largestRemainder(shares map[string]float64, target int64) map[string]int64 {
	result := make(map[string]int64, len(shares))
	keys := make([]string, 0, len(shares))
	sum := 0.0
	for key, share := range shares {
		keys = append(keys, key)
		sum += share
	}
	if len(keys) == 0 || sum <= 0 {
		return result
	}
	scale := float64(target) / sum
	var assigned int64
	for _, key := range keys {
		result[key] = int64(math.Floor(shares[key] * scale))
		assigned += result[key]
	}
	sort.Slice(keys, func(a, b int) bool {
		fa := shares[keys[a]]*scale - float64(result[keys[a]])
		fb := shares[keys[b]]*scale - float64(result[keys[b]])
		if fa != fb {
			return fa > fb
		}
		return keys[a] < keys[b]
	})
	for i := 0; assigned < target && len(keys) > 0; i = (i + 1) % len(keys) {
		result[keys[i]]++
		assigned++
	}
	return result
}
