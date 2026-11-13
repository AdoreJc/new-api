package model

import (
	"errors"
	"time"

	"gorm.io/gorm/clause"
)

// ErrChannelAutoResetLeaseLost reports that a conditional state update found
// the lease held by another owner or expired.
var ErrChannelAutoResetLeaseLost = errors.New("channel reset lease lost")

// ChannelAutoResetState is the reset-credit redemption state of one channel,
// shared by every node. It holds the redemption lease (one redemption per
// channel at a time, automatic or manual), the in-flight marker written before
// a redemption request is sent, the redemption history the guards read, and
// the last outcome shown in the console. Every write updates named columns
// only, and writes that belong to a redemption are conditional on the lease
// owner, so concurrent writers on other nodes cannot drop each other's data.
type ChannelAutoResetState struct {
	ChannelId int `json:"channel_id" gorm:"primaryKey;autoIncrement:false"`
	// Mirrors the switch of the stored setting so the scheduler can find the
	// channels to evaluate without scanning channels.other_info.
	AutoEnabled bool   `json:"auto_enabled" gorm:"index"`
	LeaseOwner  string `json:"-" gorm:"type:varchar(64)"`
	LeaseUntil  int64  `json:"lease_until" gorm:"bigint"`
	// Set (unix seconds) right before a redemption request goes upstream and
	// cleared once its outcome is known; still set after a crash.
	InFlightSince  int64  `json:"in_flight_since" gorm:"bigint"`
	InFlightSource string `json:"in_flight_source" gorm:"type:varchar(16)"`
	// Last redemption from any source that may have used a credit.
	LastRedeemAt     int64  `json:"last_redeem_at" gorm:"bigint"`
	LastRedeemSource string `json:"last_redeem_source" gorm:"type:varchar(16)"`
	// JSON []int64: automatic redemptions that may have used a credit, last 7 days.
	AutoHistory  string `json:"-" gorm:"type:text"`
	FailCount    int    `json:"fail_count"`
	BackoffUntil int64  `json:"backoff_until" gorm:"bigint"`
	// JSON []string: alarms that already fired, last 48 hours.
	FiredAlarms    string `json:"-" gorm:"type:text"`
	LastAttemptAt  int64  `json:"last_attempt_at" gorm:"bigint"`
	LastResult     string `json:"last_result" gorm:"type:varchar(64)"`
	LastSuccess    bool   `json:"last_success"`
	LastHits       string `json:"-" gorm:"type:text"`
	LastSkipReason string `json:"last_skip_reason" gorm:"type:varchar(32)"`
	LastSkipAt     int64  `json:"last_skip_at" gorm:"bigint"`
	// Unix nanoseconds of the last write, so every conditional update changes
	// the row (MySQL reports unchanged rows as not affected).
	UpdatedAt int64 `json:"updated_at" gorm:"bigint"`
}

func ensureChannelAutoResetState(channelId int) error {
	return DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&ChannelAutoResetState{ChannelId: channelId}).Error
}

// GetChannelAutoResetState returns the stored state, or a zero state when the
// channel has none yet.
func GetChannelAutoResetState(channelId int) (*ChannelAutoResetState, error) {
	state := &ChannelAutoResetState{}
	err := DB.Where("channel_id = ?", channelId).Limit(1).Find(state).Error
	if err != nil {
		return nil, err
	}
	state.ChannelId = channelId
	return state, nil
}

// AcquireChannelAutoResetLease claims the redemption lease of channelId for
// owner until now+ttl. ok is false while another owner holds an unexpired
// lease. The row is created on first use; the claim is one conditional update.
func AcquireChannelAutoResetLease(channelId int, owner string, now time.Time, ttl time.Duration) (bool, error) {
	if err := ensureChannelAutoResetState(channelId); err != nil {
		return false, err
	}
	result := DB.Model(&ChannelAutoResetState{}).
		Where("channel_id = ? AND lease_until < ?", channelId, now.Unix()).
		Updates(map[string]any{
			"lease_owner": owner,
			"lease_until": now.Add(ttl).Unix(),
			"updated_at":  time.Now().UnixNano(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// UpdateChannelAutoResetStateAsOwner writes updates only while owner still
// holds an unexpired lease at now.
func UpdateChannelAutoResetStateAsOwner(channelId int, owner string, now time.Time, updates map[string]any) error {
	updates["updated_at"] = time.Now().UnixNano()
	result := DB.Model(&ChannelAutoResetState{}).
		Where("channel_id = ? AND lease_owner = ? AND lease_until >= ?", channelId, owner, now.Unix()).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrChannelAutoResetLeaseLost
	}
	return nil
}

// ReleaseChannelAutoResetLease drops owner's lease and writes updates with it.
// A lease already taken over by another owner is left alone.
func ReleaseChannelAutoResetLease(channelId int, owner string, updates map[string]any) error {
	if updates == nil {
		updates = map[string]any{}
	}
	updates["lease_owner"] = ""
	updates["lease_until"] = 0
	updates["updated_at"] = time.Now().UnixNano()
	return DB.Model(&ChannelAutoResetState{}).
		Where("channel_id = ? AND lease_owner = ?", channelId, owner).
		Updates(updates).Error
}

// FinishChannelAutoResetRedemption writes updates and releases the lease in
// one statement, only while owner is still the lease holder (expired or not).
// It reports false when another redemption has taken the lease over since;
// nothing is written then.
func FinishChannelAutoResetRedemption(channelId int, owner string, updates map[string]any) (bool, error) {
	updates["lease_owner"] = ""
	updates["lease_until"] = 0
	updates["updated_at"] = time.Now().UnixNano()
	result := DB.Model(&ChannelAutoResetState{}).
		Where("channel_id = ? AND lease_owner = ?", channelId, owner).
		Updates(updates)
	return result.RowsAffected == 1, result.Error
}

// RaiseChannelLastRedeem records a redemption at `at` as the last one unless
// a later one is already recorded.
func RaiseChannelLastRedeem(channelId int, at int64, source string) error {
	return DB.Model(&ChannelAutoResetState{}).
		Where("channel_id = ? AND last_redeem_at < ?", channelId, at).
		Updates(map[string]any{"last_redeem_at": at, "last_redeem_source": source, "updated_at": time.Now().UnixNano()}).Error
}

// UpdateChannelAutoResetStateFields writes columns that no redemption owns
// (the switch mirror, the last skip reason).
func UpdateChannelAutoResetStateFields(channelId int, updates map[string]any) error {
	if err := ensureChannelAutoResetState(channelId); err != nil {
		return err
	}
	updates["updated_at"] = time.Now().UnixNano()
	return DB.Model(&ChannelAutoResetState{}).Where("channel_id = ?", channelId).Updates(updates).Error
}

// ListChannelAutoResetEnabledIds returns the channels whose switch is on.
func ListChannelAutoResetEnabledIds() ([]int, error) {
	var ids []int
	err := DB.Model(&ChannelAutoResetState{}).Where("auto_enabled = ?", true).Pluck("channel_id", &ids).Error
	return ids, err
}

func DeleteChannelAutoResetState(channelId int) error {
	return DB.Where("channel_id = ?", channelId).Delete(&ChannelAutoResetState{}).Error
}

// MutateChannelOtherInfo rewrites other_info with fn using a compare-and-swap
// on the stored text, so a concurrent writer on another node is never
// overwritten; it retries a few times on contention. fn returns false to
// leave the row unchanged.
func MutateChannelOtherInfo(channelId int, fn func(info map[string]any) bool) error {
	lock := GetChannelPollingLock(channelId)
	lock.Lock()
	defer lock.Unlock()
	for range 5 {
		channel := &Channel{}
		if err := DB.Select("id", "other_info").First(channel, "id = ?", channelId).Error; err != nil {
			return err
		}
		before := channel.OtherInfo
		info := channel.GetOtherInfo()
		if !fn(info) {
			return nil
		}
		channel.SetOtherInfo(info)
		target := channel.OtherInfo
		if target == before {
			return nil
		}
		result := DB.Model(&Channel{}).Where("id = ? AND other_info = ?", channelId, before).Update("other_info", target)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		// MySQL counts a row whose value did not change as not affected;
		// the stored text already being the target is a success.
		current := &Channel{}
		if err := DB.Select("id", "other_info").First(current, "id = ?", channelId).Error; err == nil && current.OtherInfo == target {
			return nil
		}
	}
	return errors.New("other_info changed concurrently, retry later")
}
