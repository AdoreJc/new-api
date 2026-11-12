package model

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"

	"gorm.io/gorm"
)

// PromptAuditEvent is one flagged prompt audit result. Passing results are
// never stored. FullPrompt is empty when full prompt storage is turned off.
type PromptAuditEvent struct {
	Id                int64  `json:"id" gorm:"primaryKey"`
	CreatedAt         int64  `json:"created_at" gorm:"bigint;index;index:idx_pae_decision_created,priority:2"`
	RequestId         string `json:"request_id" gorm:"type:varchar(64);index"`
	UserId            int    `json:"user_id" gorm:"index"`
	Username          string `json:"username" gorm:"type:varchar(64)"`
	TokenId           int    `json:"token_id" gorm:"index"`
	TokenName         string `json:"token_name" gorm:"type:varchar(255)"`
	UsingGroup        string `json:"using_group" gorm:"type:varchar(64);index"`
	ChannelId         int    `json:"channel_id"`
	RelayFormat       string `json:"relay_format" gorm:"type:varchar(32)"`
	Endpoint          string `json:"endpoint" gorm:"type:varchar(128)"`
	ModelName         string `json:"model_name" gorm:"type:varchar(255)"`
	Mode              string `json:"mode" gorm:"type:varchar(16)"`
	Decision          string `json:"decision" gorm:"type:varchar(16);index:idx_pae_decision_created,priority:1"`
	RiskLevel         string `json:"risk_level" gorm:"type:varchar(16)"`
	Action            string `json:"action" gorm:"type:varchar(16)"`
	Safety            string `json:"safety" gorm:"type:varchar(16)"`
	Categories        string `json:"categories" gorm:"type:text"`
	MatchedScanners   string `json:"matched_scanners" gorm:"type:text"`
	UnknownCategories string `json:"unknown_categories" gorm:"type:text"`
	GuardEndpointId   string `json:"guard_endpoint_id" gorm:"type:varchar(64)"`
	ScannerVersion    string `json:"scanner_version" gorm:"type:varchar(255)"`
	ConfigVersion     int64  `json:"config_version"`
	ChunkTotal        int    `json:"chunk_total"`
	ChunksScanned     int    `json:"chunks_scanned"`
	LatencyMs         int    `json:"latency_ms"`
	PromptHash        string `json:"prompt_hash" gorm:"type:varchar(64);index"`
	RedactedPreview   string `json:"redacted_preview" gorm:"type:varchar(512)"`
	PromptLength      int    `json:"prompt_length"`
	MessageCount      int    `json:"message_count"`
	// FullPrompt is longtext on MySQL: a 64K-character prompt can exceed the
	// 65535-byte TEXT limit there.
	FullPrompt LongText `json:"full_prompt,omitempty"`
}

// fitColumns shortens variable-length columns to their declared size so an
// unusual path, model or user name never fails the insert. MySQL and
// PostgreSQL count varchar length in characters.
func (e *PromptAuditEvent) fitColumns() {
	for _, field := range []struct {
		value *string
		limit int
	}{
		{&e.RequestId, 64}, {&e.Username, 64}, {&e.TokenName, 255}, {&e.UsingGroup, 64},
		{&e.RelayFormat, 32}, {&e.Endpoint, 128}, {&e.ModelName, 255}, {&e.Mode, 16},
		{&e.Decision, 16}, {&e.RiskLevel, 16}, {&e.Action, 16}, {&e.Safety, 16},
		{&e.GuardEndpointId, 64}, {&e.ScannerVersion, 255}, {&e.PromptHash, 64}, {&e.RedactedPreview, 512},
	} {
		if utf8.RuneCountInString(*field.value) > field.limit {
			*field.value = string([]rune(*field.value)[:field.limit])
		}
	}
}

// PromptAuditEventFilter narrows event listing and filtered deletion.
type PromptAuditEventFilter struct {
	Decision   string `form:"decision" json:"decision"`
	RiskLevel  string `form:"risk_level" json:"risk_level"`
	UserId     int    `form:"user_id" json:"user_id"`
	TokenId    int    `form:"token_id" json:"token_id"`
	Group      string `form:"group" json:"group"`
	ModelName  string `form:"model_name" json:"model_name"`
	RequestId  string `form:"request_id" json:"request_id"`
	PromptHash string `form:"prompt_hash" json:"prompt_hash"`
	Keyword    string `form:"keyword" json:"keyword"`
	StartTime  int64  `form:"start_time" json:"start_time"`
	EndTime    int64  `form:"end_time" json:"end_time"`
}

const promptAuditDeleteBatchSize = 500

func (f PromptAuditEventFilter) apply(tx *gorm.DB) *gorm.DB {
	if f.Decision != "" {
		tx = tx.Where("decision = ?", f.Decision)
	}
	if f.RiskLevel != "" {
		tx = tx.Where("risk_level = ?", f.RiskLevel)
	}
	if f.UserId > 0 {
		tx = tx.Where("user_id = ?", f.UserId)
	}
	if f.TokenId > 0 {
		tx = tx.Where("token_id = ?", f.TokenId)
	}
	if f.Group != "" {
		tx = tx.Where("using_group = ?", f.Group)
	}
	if f.ModelName != "" {
		tx = tx.Where("model_name = ?", f.ModelName)
	}
	if f.RequestId != "" {
		tx = tx.Where("request_id = ?", f.RequestId)
	}
	if f.PromptHash != "" {
		tx = tx.Where("prompt_hash = ?", strings.ToLower(f.PromptHash))
	}
	if keyword := strings.TrimSpace(f.Keyword); keyword != "" {
		// LOWER on both sides keeps PostgreSQL case-insensitive like MySQL/SQLite.
		pattern := "%" + strings.ToLower(keyword) + "%"
		tx = tx.Where("LOWER(request_id) LIKE ? OR LOWER(username) LIKE ? OR LOWER(token_name) LIKE ? OR LOWER(redacted_preview) LIKE ? OR LOWER(model_name) LIKE ?",
			pattern, pattern, pattern, pattern, pattern)
	}
	if f.StartTime > 0 {
		tx = tx.Where("created_at >= ?", f.StartTime)
	}
	if f.EndTime > 0 {
		tx = tx.Where("created_at <= ?", f.EndTime)
	}
	return tx
}

// CreatePromptAuditEvents stores a batch in one insert. If the batch fails,
// each event is retried on its own so one bad row cannot lose the others. It
// returns how many events were stored and the last error.
func CreatePromptAuditEvents(events []*PromptAuditEvent) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	for _, event := range events {
		event.fitColumns()
	}
	if err := DB.Create(&events).Error; err == nil {
		return len(events), nil
	}
	stored := 0
	var lastErr error
	for _, event := range events {
		event.Id = 0
		if err := DB.Create(event).Error; err != nil {
			lastErr = err
			continue
		}
		stored++
	}
	return stored, lastErr
}

// ListPromptAuditEvents returns one page of events without the full prompt.
func ListPromptAuditEvents(filter PromptAuditEventFilter, offset, limit int) ([]*PromptAuditEvent, int64, error) {
	var total int64
	if err := filter.apply(DB.Model(&PromptAuditEvent{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	events := make([]*PromptAuditEvent, 0, limit)
	err := filter.apply(DB.Model(&PromptAuditEvent{})).Omit("full_prompt").
		Order("id desc").Offset(offset).Limit(limit).Find(&events).Error
	return events, total, err
}

func GetPromptAuditEvent(id int64) (*PromptAuditEvent, error) {
	var event PromptAuditEvent
	if err := DB.First(&event, id).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

func DeletePromptAuditEventsByIds(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := DB.Where("id IN ?", ids).Delete(&PromptAuditEvent{})
	return result.RowsAffected, result.Error
}

// PreviewPromptAuditEventDeletion counts the matching events and returns the
// highest matching id, which bounds a later DeletePromptAuditEventsByFilter so
// events recorded after the preview are not deleted.
func PreviewPromptAuditEventDeletion(filter PromptAuditEventFilter) (int64, int64, error) {
	var summary struct {
		Total int64
		MaxId int64
	}
	err := filter.apply(DB.Model(&PromptAuditEvent{})).Select("COUNT(*) AS total, COALESCE(MAX(id), 0) AS max_id").Scan(&summary).Error
	return summary.Total, summary.MaxId, err
}

// DeletePromptAuditEventsByFilter deletes matching events with id <= maxId in
// batches. Ids are read first because LIMIT on DELETE is not portable (the
// SQLite dialect drops it and would delete everything in one long lock).
func DeletePromptAuditEventsByFilter(filter PromptAuditEventFilter, maxId int64) (int64, error) {
	return deletePromptAuditEventsInBatches(func() *gorm.DB {
		return filter.apply(DB.Model(&PromptAuditEvent{})).Where("id <= ?", maxId)
	})
}

// CleanupPromptAuditEvents deletes events created before the cutoff.
func CleanupPromptAuditEvents(before int64) (int64, error) {
	return deletePromptAuditEventsInBatches(func() *gorm.DB {
		return DB.Model(&PromptAuditEvent{}).Where("created_at < ?", before)
	})
}

func deletePromptAuditEventsInBatches(query func() *gorm.DB) (int64, error) {
	var deleted int64
	for {
		var ids []int64
		if err := query().Order("id").Limit(promptAuditDeleteBatchSize).Pluck("id", &ids).Error; err != nil {
			return deleted, err
		}
		if len(ids) == 0 {
			return deleted, nil
		}
		count, err := DeletePromptAuditEventsByIds(ids)
		deleted += count
		if err != nil {
			return deleted, err
		}
		if len(ids) < promptAuditDeleteBatchSize {
			return deleted, nil
		}
	}
}

var (
	promptAuditConfigMutex sync.Mutex

	ErrPromptAuditConfigConflict = common.NewMessage("The prompt audit settings were changed by someone else. Reload and try again")
)

// PromptAuditStoredConfig reads the configuration and tokens from the
// database, the source of truth shared by all nodes.
func PromptAuditStoredConfig() (prompt_audit_setting.Config, map[string]string, error) {
	values := map[string]string{}
	var options []Option
	if err := DB.Where(map[string]any{"key": []string{prompt_audit_setting.OptionKey, prompt_audit_setting.TokensOptionKey}}).Find(&options).Error; err != nil {
		return prompt_audit_setting.Config{}, nil, err
	}
	for _, option := range options {
		values[option.Key] = option.Value
	}
	config, err := prompt_audit_setting.ParseConfig(values[prompt_audit_setting.OptionKey])
	if err != nil {
		return prompt_audit_setting.Config{}, nil, err
	}
	return config, prompt_audit_setting.ParseTokens(values[prompt_audit_setting.TokensOptionKey]), nil
}

// SavePromptAuditConfig stores a validated configuration and its tokens when
// the stored version still equals expectedVersion, then bumps the version.
// The process mutex serializes writers on one node; the version check inside
// the transaction rejects a concurrent save from another node.
func SavePromptAuditConfig(config prompt_audit_setting.Config, tokens map[string]string, expectedVersion int64) (prompt_audit_setting.Config, error) {
	if err := config.Normalize(); err != nil {
		return config, err
	}
	promptAuditConfigMutex.Lock()
	defer promptAuditConfigMutex.Unlock()
	// Create the row first so the locking read below always has a row to lock;
	// a concurrent first save on another node then waits instead of passing
	// the version check too. A duplicate-key error means another node won.
	_ = DB.Where(Option{Key: prompt_audit_setting.OptionKey}).Attrs(Option{Value: ""}).FirstOrCreate(&Option{}).Error
	var configJSON, tokensJSON []byte
	err := DB.Transaction(func(tx *gorm.DB) error {
		var stored Option
		err := lockForUpdate(tx).Where(&Option{Key: prompt_audit_setting.OptionKey}).Limit(1).Find(&stored).Error
		if err != nil {
			return err
		}
		storedConfig, err := prompt_audit_setting.ParseConfig(stored.Value)
		if err != nil {
			storedConfig = prompt_audit_setting.DefaultConfig()
		}
		if storedConfig.ConfigVersion != expectedVersion {
			return ErrPromptAuditConfigConflict
		}
		config.ConfigVersion = expectedVersion + 1
		if configJSON, err = common.Marshal(config); err != nil {
			return err
		}
		if tokensJSON, err = common.Marshal(tokens); err != nil {
			return err
		}
		for key, value := range map[string]string{
			prompt_audit_setting.OptionKey:       string(configJSON),
			prompt_audit_setting.TokensOptionKey: string(tokensJSON),
		} {
			if err := tx.Where(Option{Key: key}).Assign(map[string]any{"value": value}).FirstOrCreate(&Option{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return config, err
	}
	if err := updateOptionMap(prompt_audit_setting.TokensOptionKey, string(tokensJSON)); err != nil {
		return config, err
	}
	return config, updateOptionMap(prompt_audit_setting.OptionKey, string(configJSON))
}
