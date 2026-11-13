package model

import (
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/constant"
)

// channelAutoResetLeaseType names the system_task_locks row that holds the
// automatic reset-credit redemption of one channel. The row is shared by all
// nodes, so at most one node redeems for a channel at a time.
func channelAutoResetLeaseType(channelId int) string {
	return "auto_reset_credit:" + strconv.Itoa(channelId)
}

// AcquireChannelAutoResetLease claims the automatic redemption of channelId
// for owner until now+ttl. ok is false while another owner holds an unexpired
// lease. It reuses the system task lock table (insert, or a conditional update
// of an expired row), which behaves the same on SQLite, MySQL and PostgreSQL.
func AcquireChannelAutoResetLease(channelId int, owner string, now time.Time, ttl time.Duration) (bool, error) {
	ok, _, err := acquireSystemTaskLock(channelAutoResetLeaseType(channelId), owner, owner, now.Unix(), now.Add(ttl).Unix())
	return ok, err
}

// ReleaseChannelAutoResetLease drops the lease of owner; a lease that already
// expired and was taken over by another owner is left alone.
func ReleaseChannelAutoResetLease(owner string) error {
	return ReleaseSystemTaskLock(owner, owner)
}

// ListChannelAutoResetCandidates returns the reset-credit capable channels
// whose other_info mentions the automatic reset setting, with only the
// columns the scheduler needs to decide whether the switch is on.
func ListChannelAutoResetCandidates() ([]*Channel, error) {
	var channels []*Channel
	err := DB.Select("id", "type", "status", "other_info").
		Where("type IN ?", []int{constant.ChannelTypeCodex, constant.ChannelTypeClaudeOAuth}).
		Where("other_info LIKE ?", "%auto_reset_credit%").
		Find(&channels).Error
	return channels, err
}
