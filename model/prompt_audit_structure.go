package model

import (
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Request structure statistics of prompt audit. The three tables hold only
// hashes, counts, lengths, times and categories: never prompt text, excerpts
// or previews.

const (
	// PromptAuditFingerprintUserCap is how many distinct users are counted
	// exactly per fingerprint; a count at the cap means "at least that many".
	PromptAuditFingerprintUserCap = 50
	// PromptAuditFingerprintMaxRows bounds the fingerprint table; new
	// fingerprints are not stored while it is full.
	PromptAuditFingerprintMaxRows = 100000

	promptAuditStructureBatchSize = 200
)

// PromptAuditFingerprint aggregates the requests that share one normalized
// system prompt. Fingerprint is a SHA-256 hex digest, or "none" for requests
// without a system prompt.
type PromptAuditFingerprint struct {
	Id          int64  `json:"id" gorm:"primaryKey"`
	Fingerprint string `json:"fingerprint" gorm:"type:varchar(64);uniqueIndex"`
	// PrefixHash is the digest of the leading part of the normalized prompt.
	PrefixHash   string `json:"prefix_hash" gorm:"type:varchar(64)"`
	FirstSeenAt  int64  `json:"first_seen_at" gorm:"bigint"`
	LastSeenAt   int64  `json:"last_seen_at" gorm:"bigint;index"`
	RequestCount int64  `json:"request_count" gorm:"index"`
	// UserCount is exact up to PromptAuditFingerprintUserCap.
	UserCount         int    `json:"user_count" gorm:"index"`
	SystemLength      int    `json:"system_length"`
	LastRequestFormat string `json:"last_request_format" gorm:"type:varchar(32)"`
	LastClientClass   string `json:"last_client_class" gorm:"type:varchar(16)"`
}

// PromptAuditFingerprintUser records that a user sent a fingerprint. At most
// PromptAuditFingerprintUserCap rows are kept per fingerprint.
type PromptAuditFingerprintUser struct {
	Id          int64  `json:"id" gorm:"primaryKey"`
	Fingerprint string `json:"fingerprint" gorm:"type:varchar(64);uniqueIndex:idx_pafu_fingerprint_user,priority:1"`
	UserId      int    `json:"user_id" gorm:"uniqueIndex:idx_pafu_fingerprint_user,priority:2"`
}

// PromptAuditStructureDaily counts structural signals per day, request format
// and client class. Every counter is a number of requests, except the ones
// documented as sums.
type PromptAuditStructureDaily struct {
	Id int64 `json:"-" gorm:"primaryKey"`
	// StatDay is the server-local date as yyyymmdd.
	StatDay       int    `json:"stat_day" gorm:"uniqueIndex:idx_pasd_day_format_client,priority:1"`
	RequestFormat string `json:"request_format,omitempty" gorm:"type:varchar(32);uniqueIndex:idx_pasd_day_format_client,priority:2"`
	ClientClass   string `json:"client_class,omitempty" gorm:"type:varchar(16);uniqueIndex:idx_pasd_day_format_client,priority:3"`

	Requests             int64 `json:"requests"`
	WithSystem           int64 `json:"with_system"`
	WithDeveloper        int64 `json:"with_developer"`
	WithHistoryUser      int64 `json:"with_history_user"`
	WithHistoryAssistant int64 `json:"with_history_assistant"`
	// TrailingAssistant: the conversation ends with an assistant message.
	TrailingAssistant int64 `json:"trailing_assistant"`
	WithAgentContext  int64 `json:"with_agent_context"`
	WithTool          int64 `json:"with_tool"`
	// AssistantTurns and UserTurns are sums over requests.
	AssistantTurns       int64 `json:"assistant_turns"`
	UserTurns            int64 `json:"user_turns"`
	AssistantTurns10Plus int64 `json:"assistant_turns_10_plus" gorm:"column:assistant_turns_10_plus"`
	MidSystem            int64 `json:"mid_system"`
	ControlTokens        int64 `json:"control_tokens"`
	RoleMarkers          int64 `json:"role_markers"`
	InvisibleRequests    int64 `json:"invisible_requests"`
	// InvisibleChars and EncodedRuns are sums over requests.
	InvisibleChars  int64 `json:"invisible_chars"`
	EncodedRequests int64 `json:"encoded_requests"`
	EncodedRuns     int64 `json:"encoded_runs"`
	// System prompt length buckets in characters; requests without a system
	// prompt are in none of them.
	SystemUnder1k  int64 `json:"system_under_1k" gorm:"column:system_under_1k"`
	SystemUnder4k  int64 `json:"system_under_4k" gorm:"column:system_under_4k"`
	SystemUnder16k int64 `json:"system_under_16k" gorm:"column:system_under_16k"`
	SystemUnder64k int64 `json:"system_under_64k" gorm:"column:system_under_64k"`
	System64kPlus  int64 `json:"system_64k_plus" gorm:"column:system_64k_plus"`
}

func (PromptAuditStructureDaily) TableName() string { return "prompt_audit_structure_daily" }

// counters returns every counter by column name.
func (d *PromptAuditStructureDaily) counters() map[string]int64 {
	return map[string]int64{
		"requests": d.Requests, "with_system": d.WithSystem, "with_developer": d.WithDeveloper,
		"with_history_user": d.WithHistoryUser, "with_history_assistant": d.WithHistoryAssistant,
		"trailing_assistant": d.TrailingAssistant, "with_agent_context": d.WithAgentContext, "with_tool": d.WithTool,
		"assistant_turns": d.AssistantTurns, "user_turns": d.UserTurns, "assistant_turns_10_plus": d.AssistantTurns10Plus,
		"mid_system": d.MidSystem, "control_tokens": d.ControlTokens, "role_markers": d.RoleMarkers,
		"invisible_requests": d.InvisibleRequests, "invisible_chars": d.InvisibleChars,
		"encoded_requests": d.EncodedRequests, "encoded_runs": d.EncodedRuns,
		"system_under_1k": d.SystemUnder1k, "system_under_4k": d.SystemUnder4k, "system_under_16k": d.SystemUnder16k,
		"system_under_64k": d.SystemUnder64k, "system_64k_plus": d.System64kPlus,
	}
}

// PromptAuditFingerprintDelta is what one flush adds to a fingerprint.
type PromptAuditFingerprintDelta struct {
	Fingerprint   string
	PrefixHash    string
	FirstSeenAt   int64
	LastSeenAt    int64
	Requests      int64
	UserIds       []int
	SystemLength  int
	RequestFormat string
	ClientClass   string
}

// ApplyPromptAuditStructureStats adds a batch of in-memory counters to the
// tables in one transaction, so SQLite takes its write lock once. Counters
// are added with column arithmetic, which keeps several nodes correct. It
// returns how many new fingerprints were left out because the table is full.
func ApplyPromptAuditStructureStats(fingerprints []*PromptAuditFingerprintDelta, daily []*PromptAuditStructureDaily) (int, error) {
	skipped := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		skipped = 0
		known := make(map[string]int, len(fingerprints))
		for chunk := range slices.Chunk(fingerprints, promptAuditStructureBatchSize) {
			names := make([]string, 0, len(chunk))
			for _, delta := range chunk {
				names = append(names, delta.Fingerprint)
			}
			var rows []PromptAuditFingerprint
			if err := tx.Select("fingerprint", "user_count").Where("fingerprint IN ?", names).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				known[row.Fingerprint] = row.UserCount
			}
		}
		// The table size is read once, and only when a fingerprint is new.
		tableRows := int64(-1)
		for _, delta := range fingerprints {
			usage := map[string]any{
				"request_count": gorm.Expr("request_count + ?", delta.Requests), "last_seen_at": delta.LastSeenAt,
				"prefix_hash": delta.PrefixHash, "system_length": delta.SystemLength,
				"last_request_format": delta.RequestFormat, "last_client_class": delta.ClientClass,
			}
			userCount, exists := known[delta.Fingerprint]
			if !exists {
				if tableRows < 0 {
					if err := tx.Model(&PromptAuditFingerprint{}).Count(&tableRows).Error; err != nil {
						return err
					}
				}
				if tableRows >= PromptAuditFingerprintMaxRows {
					skipped++
					continue
				}
				created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&PromptAuditFingerprint{
					Fingerprint: delta.Fingerprint, PrefixHash: delta.PrefixHash, FirstSeenAt: delta.FirstSeenAt, LastSeenAt: delta.LastSeenAt,
					RequestCount: delta.Requests, SystemLength: delta.SystemLength,
					LastRequestFormat: delta.RequestFormat, LastClientClass: delta.ClientClass,
				})
				if created.Error != nil {
					return created.Error
				}
				tableRows += created.RowsAffected
				// No row was inserted when another node created it first.
				exists = created.RowsAffected == 0
			}
			if exists {
				if err := tx.Model(&PromptAuditFingerprint{}).Where("fingerprint = ?", delta.Fingerprint).Updates(usage).Error; err != nil {
					return err
				}
			}
			if userCount >= PromptAuditFingerprintUserCap || len(delta.UserIds) == 0 {
				continue
			}
			var stored []int
			if err := tx.Model(&PromptAuditFingerprintUser{}).Where("fingerprint = ?", delta.Fingerprint).Pluck("user_id", &stored).Error; err != nil {
				return err
			}
			added := make([]PromptAuditFingerprintUser, 0, len(delta.UserIds))
			for _, userId := range delta.UserIds {
				if len(stored)+len(added) >= PromptAuditFingerprintUserCap {
					break
				}
				if !slices.Contains(stored, userId) {
					added = append(added, PromptAuditFingerprintUser{Fingerprint: delta.Fingerprint, UserId: userId})
				}
			}
			if len(added) > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&added).Error; err != nil {
					return err
				}
			}
			if total := len(stored) + len(added); total != userCount {
				if err := tx.Model(&PromptAuditFingerprint{}).Where("fingerprint = ?", delta.Fingerprint).Update("user_count", total).Error; err != nil {
					return err
				}
			}
		}
		for _, counters := range daily {
			added := make(map[string]any)
			for column, value := range counters.counters() {
				if value != 0 {
					added[column] = gorm.Expr(column+" + ?", value)
				}
			}
			if len(added) == 0 {
				continue
			}
			key := tx.Model(&PromptAuditStructureDaily{}).Where("stat_day = ? AND request_format = ? AND client_class = ?",
				counters.StatDay, counters.RequestFormat, counters.ClientClass)
			updated := key.Session(&gorm.Session{}).Updates(added)
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected > 0 {
				continue
			}
			row := *counters
			row.Id = 0
			created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
			if created.Error != nil {
				return created.Error
			}
			if created.RowsAffected == 0 {
				if err := key.Session(&gorm.Session{}).Updates(added).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	return skipped, err
}

// Fingerprint ranking orders.
const (
	PromptAuditFingerprintSortUsers    = "users"
	PromptAuditFingerprintSortRequests = "requests"
)

// ListPromptAuditFingerprints returns one page of fingerprints ranked by
// distinct users (the default) or by requests.
func ListPromptAuditFingerprints(sort string, offset, limit int) ([]*PromptAuditFingerprint, int64, error) {
	var total int64
	if err := DB.Model(&PromptAuditFingerprint{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	order := "user_count desc, request_count desc, id desc"
	if sort == PromptAuditFingerprintSortRequests {
		order = "request_count desc, user_count desc, id desc"
	}
	items := make([]*PromptAuditFingerprint, 0, limit)
	err := DB.Order(order).Offset(offset).Limit(limit).Find(&items).Error
	return items, total, err
}

// PromptAuditStructureFilter narrows the daily signal counters. Days are
// yyyymmdd and inclusive; empty format or client class means all of them.
type PromptAuditStructureFilter struct {
	StartDay      int    `form:"start_day"`
	EndDay        int    `form:"end_day"`
	RequestFormat string `form:"request_format"`
	ClientClass   string `form:"client_class"`
}

// ListPromptAuditStructureDaily returns the counters summed per day, newest
// day first.
func ListPromptAuditStructureDaily(filter PromptAuditStructureFilter) ([]*PromptAuditStructureDaily, error) {
	columns := make([]string, 0, 24)
	for column := range (&PromptAuditStructureDaily{}).counters() {
		columns = append(columns, column)
	}
	slices.Sort(columns)
	selected := "stat_day"
	for _, column := range columns {
		selected += ", SUM(" + column + ") AS " + column
	}
	query := DB.Model(&PromptAuditStructureDaily{}).Select(selected)
	if filter.StartDay > 0 {
		query = query.Where("stat_day >= ?", filter.StartDay)
	}
	if filter.EndDay > 0 {
		query = query.Where("stat_day <= ?", filter.EndDay)
	}
	if filter.RequestFormat != "" {
		query = query.Where("request_format = ?", filter.RequestFormat)
	}
	if filter.ClientClass != "" {
		query = query.Where("client_class = ?", filter.ClientClass)
	}
	items := make([]*PromptAuditStructureDaily, 0)
	err := query.Group("stat_day").Order("stat_day desc").Scan(&items).Error
	return items, err
}

// CleanupPromptAuditStructureStats deletes fingerprints last seen before
// seenBefore together with their user rows, and daily counters of days before
// dayBefore (yyyymmdd). It returns the number of fingerprints deleted.
func CleanupPromptAuditStructureStats(seenBefore int64, dayBefore int) (int64, error) {
	var deleted int64
	for {
		var names []string
		if err := DB.Model(&PromptAuditFingerprint{}).Where("last_seen_at < ?", seenBefore).Order("id").
			Limit(promptAuditStructureBatchSize).Pluck("fingerprint", &names).Error; err != nil {
			return deleted, err
		}
		if len(names) == 0 {
			break
		}
		if err := DB.Where("fingerprint IN ?", names).Delete(&PromptAuditFingerprintUser{}).Error; err != nil {
			return deleted, err
		}
		result := DB.Where("fingerprint IN ?", names).Delete(&PromptAuditFingerprint{})
		deleted += result.RowsAffected
		if result.Error != nil {
			return deleted, result.Error
		}
		if result.RowsAffected == 0 {
			break
		}
	}
	return deleted, DB.Where("stat_day < ?", dayBefore).Delete(&PromptAuditStructureDaily{}).Error
}
