package model

import (
	"sync"
	"time"
)

// Codex (ChatGPT OAuth) channels whose account hit its usage limit are kept
// out of channel selection until the limit resets, so a shared pool stops
// sending every request to an exhausted account first. The state lives in
// process memory: each node learns the reset from its own 429.
var codexChannelCooldowns sync.Map // channel id -> time.Time

// CoolDownCodexChannel keeps the channel out of selection until until. A later
// call never shortens an existing cooldown.
func CoolDownCodexChannel(channelID int, until time.Time) {
	if channelID <= 0 {
		return
	}
	if current, ok := codexChannelCooldowns.Load(channelID); ok && current.(time.Time).After(until) {
		return
	}
	codexChannelCooldowns.Store(channelID, until)
}

// ClearCodexChannelCooldown lets the channel be selected again at once: the
// account was authorized again or its key replaced, an admin enabled it, the
// usage window was reset, or it answered successfully below its limit.
func ClearCodexChannelCooldown(channelID int) {
	codexChannelCooldowns.Delete(channelID)
}

// CodexChannelCooldownUntil returns when the channel's cooldown ends, if it is
// cooling down.
func CodexChannelCooldownUntil(channelID int, now time.Time) (time.Time, bool) {
	if !CodexChannelCoolingDown(channelID, now) {
		return time.Time{}, false
	}
	value, ok := codexChannelCooldowns.Load(channelID)
	if !ok {
		return time.Time{}, false
	}
	return value.(time.Time), true
}

// CodexChannelCoolingDown reports whether the channel is still cooling down.
func CodexChannelCoolingDown(channelID int, now time.Time) bool {
	value, ok := codexChannelCooldowns.Load(channelID)
	if !ok {
		return false
	}
	if now.Before(value.(time.Time)) {
		return true
	}
	codexChannelCooldowns.CompareAndDelete(channelID, value)
	return false
}

// withoutCoolingCodexChannelIDs drops cooling channels from a candidate list.
// When every candidate is cooling the list is kept, so the request still
// reaches a channel and gets the upstream's own limit answer rather than "no
// channel available". The input slice is never mutated.
func withoutCoolingCodexChannelIDs(ids []int) []int {
	now := time.Now()
	var kept []int
	for i, id := range ids {
		if !CodexChannelCoolingDown(id, now) {
			if kept != nil {
				kept = append(kept, id)
			}
			continue
		}
		if kept == nil {
			kept = append(make([]int, 0, len(ids)), ids[:i]...)
		}
	}
	if kept == nil {
		return ids
	}
	if len(kept) == 0 {
		return ids
	}
	return kept
}

// withoutCoolingCodexAbilities is withoutCoolingCodexChannelIDs for the
// database selection path.
func withoutCoolingCodexAbilities(abilities []Ability) []Ability {
	now := time.Now()
	kept := make([]Ability, 0, len(abilities))
	for _, ability := range abilities {
		if !CodexChannelCoolingDown(ability.ChannelId, now) {
			kept = append(kept, ability)
		}
	}
	if len(kept) == 0 || len(kept) == len(abilities) {
		return abilities
	}
	return kept
}
