package model

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/content_audit_setting"
	"gorm.io/gorm"
)

const (
	ContentAuditDecisionWarn  = "warn"
	ContentAuditDecisionBlock = "block"

	contentAuditDeleteBatch = 500
)

var ErrContentAuditConfigConflict = errors.New("content audit configuration changed concurrently")

var contentAuditConfigMutex sync.Mutex

// ContentAuditStringList is stored as a JSON array in a TEXT column so every
// supported database keeps the same representation.
type ContentAuditStringList []string

func (list ContentAuditStringList) Value() (driver.Value, error) {
	if list == nil {
		list = ContentAuditStringList{}
	}
	encoded, err := common.Marshal([]string(list))
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

func (list *ContentAuditStringList) Scan(value any) error {
	var raw []byte
	switch typed := value.(type) {
	case nil:
		*list = ContentAuditStringList{}
		return nil
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	default:
		return fmt.Errorf("unsupported content audit list type %T", value)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		*list = ContentAuditStringList{}
		return nil
	}
	decoded := []string{}
	if err := common.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*list = decoded
	return nil
}

// ContentAuditEvent records one Guard finding. It deliberately holds no
// request text: only labels, versions, a hash and a short redacted preview.
type ContentAuditEvent struct {
	Id                int64                  `json:"id" gorm:"primaryKey"`
	CreatedAt         int64                  `json:"created_at" gorm:"type:bigint;index;index:idx_cae_decision_created,priority:2"`
	RequestId         string                 `json:"request_id" gorm:"type:varchar(64);index"`
	UserId            int                    `json:"user_id" gorm:"index"`
	Username          string                 `json:"username" gorm:"type:varchar(64)"`
	TokenId           int                    `json:"token_id" gorm:"index"`
	TokenName         string                 `json:"token_name" gorm:"type:varchar(255)"`
	UsingGroup        string                 `json:"using_group" gorm:"type:varchar(64);index"`
	ChannelId         int                    `json:"channel_id"`
	RelayFormat       string                 `json:"relay_format" gorm:"type:varchar(32)"`
	Endpoint          string                 `json:"endpoint" gorm:"type:varchar(128)"`
	ModelName         string                 `json:"model_name" gorm:"type:varchar(255)"`
	Decision          string                 `json:"decision" gorm:"type:varchar(16);index:idx_cae_decision_created,priority:1"`
	Safety            string                 `json:"safety" gorm:"type:varchar(16)"`
	RiskLevel         string                 `json:"risk_level" gorm:"type:varchar(16)"`
	Score             float64                `json:"score"`
	Categories        ContentAuditStringList `json:"categories" gorm:"type:text"`
	MatchedScanners   ContentAuditStringList `json:"matched_scanners" gorm:"type:text"`
	UnknownCategories ContentAuditStringList `json:"unknown_categories" gorm:"type:text"`
	GuardEndpointId   string                 `json:"guard_endpoint_id" gorm:"type:varchar(64)"`
	ScannerVersion    string                 `json:"scanner_version" gorm:"type:varchar(128)"`
	ConfigVersion     int64                  `json:"config_version"`
	ChunkTotal        int                    `json:"chunk_total"`
	LatencyMs         int                    `json:"latency_ms"`
	PromptHash        string                 `json:"prompt_hash" gorm:"type:varchar(64);index"`
	RedactedPreview   string                 `json:"redacted_preview" gorm:"type:varchar(128)"`
	PromptLength      int                    `json:"prompt_length"`
	MessageCount      int                    `json:"message_count"`
}

type ContentAuditEventFilter struct {
	Decision       string
	UserId         int
	Group          string
	RequestId      string
	PromptHash     string
	StartTimestamp int64
	EndTimestamp   int64
}

func CreateContentAuditEvents(events []*ContentAuditEvent) error {
	if len(events) == 0 {
		return nil
	}
	return DB.CreateInBatches(events, 100).Error
}

func GetContentAuditEvents(filter ContentAuditEventFilter, start, limit int) ([]*ContentAuditEvent, int64, error) {
	query := DB.Model(&ContentAuditEvent{})
	if filter.Decision != "" {
		query = query.Where("decision = ?", filter.Decision)
	}
	if filter.UserId > 0 {
		query = query.Where("user_id = ?", filter.UserId)
	}
	if filter.Group != "" {
		query = query.Where("using_group = ?", filter.Group)
	}
	if filter.RequestId != "" {
		query = query.Where("request_id = ?", filter.RequestId)
	}
	if filter.PromptHash != "" {
		query = query.Where("prompt_hash = ?", filter.PromptHash)
	}
	if filter.StartTimestamp > 0 {
		query = query.Where("created_at >= ?", filter.StartTimestamp)
	}
	if filter.EndTimestamp > 0 {
		query = query.Where("created_at <= ?", filter.EndTimestamp)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	events := make([]*ContentAuditEvent, 0)
	err := query.Order("id desc").Offset(start).Limit(limit).Find(&events).Error
	return events, total, err
}

func GetContentAuditEventById(id int64) (*ContentAuditEvent, error) {
	event := &ContentAuditEvent{}
	if err := DB.First(event, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return event, nil
}

func DeleteContentAuditEvents(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := DB.Where("id IN ?", ids).Delete(&ContentAuditEvent{})
	return result.RowsAffected, result.Error
}

// DeleteContentAuditEventsBefore removes events older than the cutoff in small
// batches. It selects ids first because the SQLite dialect drops LIMIT from
// DELETE statements, which would otherwise hold the write lock for one large
// delete.
func DeleteContentAuditEventsBefore(cutoff int64) (int64, error) {
	var deleted int64
	for {
		var ids []int64
		err := DB.Model(&ContentAuditEvent{}).Where("created_at < ?", cutoff).
			Order("id asc").Limit(contentAuditDeleteBatch).Pluck("id", &ids).Error
		if err != nil {
			return deleted, err
		}
		if len(ids) == 0 {
			return deleted, nil
		}
		affected, err := DeleteContentAuditEvents(ids)
		deleted += affected
		if err != nil {
			return deleted, err
		}
		if len(ids) < contentAuditDeleteBatch {
			return deleted, nil
		}
	}
}

// SaveContentAuditConfig writes the configuration and the Guard token map in
// one transaction. expectedVersion must match the stored version; the saved
// configuration gets the next version.
func SaveContentAuditConfig(cfg content_audit_setting.Config, tokens map[string]string, expectedVersion int64) (content_audit_setting.Config, error) {
	contentAuditConfigMutex.Lock()
	defer contentAuditConfigMutex.Unlock()

	var encodedConfig, encodedTokens string
	err := DB.Transaction(func(tx *gorm.DB) error {
		var stored []Option
		if err := lockForUpdate(tx).Where(&Option{Key: content_audit_setting.ConfigOptionKey}).Find(&stored).Error; err != nil {
			return err
		}
		var storedVersion int64
		if len(stored) > 0 && strings.TrimSpace(stored[0].Value) != "" {
			previous := struct {
				ConfigVersion int64 `json:"config_version"`
			}{}
			if err := common.UnmarshalJsonStr(stored[0].Value, &previous); err == nil {
				storedVersion = previous.ConfigVersion
			}
		}
		if storedVersion != expectedVersion {
			return ErrContentAuditConfigConflict
		}
		cfg.ConfigVersion = storedVersion + 1
		configBytes, err := common.Marshal(cfg)
		if err != nil {
			return err
		}
		tokenBytes, err := common.Marshal(tokens)
		if err != nil {
			return err
		}
		encodedConfig, encodedTokens = string(configBytes), string(tokenBytes)
		for key, value := range map[string]string{
			content_audit_setting.ConfigOptionKey: encodedConfig,
			content_audit_setting.TokensOptionKey: encodedTokens,
		} {
			// Assign with a map so an empty value is written too.
			if err := tx.Where(Option{Key: key}).Assign(map[string]any{"value": value}).FirstOrCreate(&Option{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return content_audit_setting.Config{}, err
	}
	if err := updateOptionMap(content_audit_setting.TokensOptionKey, encodedTokens); err != nil {
		return content_audit_setting.Config{}, err
	}
	if err := updateOptionMap(content_audit_setting.ConfigOptionKey, encodedConfig); err != nil {
		return content_audit_setting.Config{}, err
	}
	return cfg, nil
}
