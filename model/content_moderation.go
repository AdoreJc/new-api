package model

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Content moderation event actions.
const (
	ContentModerationActionAllow     = "allow"
	ContentModerationActionBlock     = "block"
	ContentModerationActionHashBlock = "hash_block"
	// ContentModerationActionKeywordBlock is a request blocked by a keyword
	// list without an engine call.
	ContentModerationActionKeywordBlock = "keyword_block"
	ContentModerationActionError        = "error"
	// ContentModerationActionReroute is a flagged request sent to another
	// model or group by an audit policy rule.
	ContentModerationActionReroute = "reroute"
	// ContentModerationActionUnavailable is a request blocked because the
	// engine failed and the rule's failure policy is closed.
	ContentModerationActionUnavailable = "fail_block"
	// ContentModerationPromptAuditCategory marks violations counted from a
	// prompt audit finding of a rule that counts toward disposal.
	ContentModerationPromptAuditCategory = "prompt_audit"

	// ContentModerationHashCategory marks events matched by the hash
	// blocklist instead of an engine call; they never count as violations.
	ContentModerationHashCategory = "hash"
	// ContentModerationKeywordCategory marks events flagged by a keyword list
	// alone (keyword_only mode).
	ContentModerationKeywordCategory = "keyword"
)

// ContentModerationEvent is one moderation result. Flagged results are always
// stored; passing results and failed calls only when record_non_hits is on.
// InputExcerpt is a short redacted excerpt, never the full input.
type ContentModerationEvent struct {
	Id              int64   `json:"id" gorm:"primaryKey"`
	CreatedAt       int64   `json:"created_at" gorm:"bigint;index;index:idx_cme_flagged_created,priority:2;index:idx_cme_user_created,priority:2"`
	RequestId       string  `json:"request_id" gorm:"type:varchar(64);index"`
	UserId          int     `json:"user_id" gorm:"index:idx_cme_user_created,priority:1"`
	Username        string  `json:"username" gorm:"type:varchar(64)"`
	TokenId         int     `json:"token_id" gorm:"index"`
	TokenName       string  `json:"token_name" gorm:"type:varchar(255)"`
	UsingGroup      string  `json:"using_group" gorm:"type:varchar(64);index"`
	ChannelId       int     `json:"channel_id"`
	RelayFormat     string  `json:"relay_format" gorm:"type:varchar(32)"`
	Endpoint        string  `json:"endpoint" gorm:"type:varchar(128)"`
	ModelName       string  `json:"model_name" gorm:"type:varchar(255)"`
	Mode            string  `json:"mode" gorm:"type:varchar(16)"`
	Action          string  `json:"action" gorm:"type:varchar(16)"`
	Flagged         bool    `json:"flagged" gorm:"index:idx_cme_flagged_created,priority:1"`
	Allowlisted     bool    `json:"allowlisted"`
	HighestCategory string  `json:"highest_category" gorm:"type:varchar(64)"`
	HighestScore    float64 `json:"highest_score"`
	// CategoryScores and Thresholds are JSON objects stored as text.
	CategoryScores string `json:"category_scores" gorm:"type:text"`
	Thresholds     string `json:"thresholds" gorm:"type:text"`
	InputHash      string `json:"input_hash" gorm:"type:varchar(64);index"`
	// KeywordList names the keyword list that matched. No field holds the
	// matched keyword itself; InputExcerpt is the usual redacted excerpt.
	KeywordList    string `json:"keyword_list" gorm:"type:varchar(64)"`
	InputExcerpt   string `json:"input_excerpt" gorm:"type:text"`
	InputLength    int    `json:"input_length"`
	ImageCount     int    `json:"image_count"`
	EngineModel    string `json:"engine_model" gorm:"type:varchar(255)"`
	LatencyMs      int    `json:"latency_ms"`
	QueueDelayMs   int    `json:"queue_delay_ms"`
	Error          string `json:"error" gorm:"type:varchar(255)"`
	ViolationCount int    `json:"violation_count"`
	AutoDisabled   bool   `json:"auto_disabled"`
	EmailSent      bool   `json:"email_sent"`
	ConfigVersion  int64  `json:"config_version"`
	// RuleId is the audit policy rule that sent the request to moderation.
	RuleId string `json:"rule_id" gorm:"type:varchar(64)"`
}

// fitColumns shortens variable-length columns to their declared size so an
// unusual path, model or user name never fails the insert.
func (e *ContentModerationEvent) fitColumns() {
	for _, field := range []struct {
		value *string
		limit int
	}{
		{&e.RequestId, 64}, {&e.Username, 64}, {&e.TokenName, 255}, {&e.UsingGroup, 64},
		{&e.RelayFormat, 32}, {&e.Endpoint, 128}, {&e.ModelName, 255}, {&e.Mode, 16}, {&e.Action, 16},
		{&e.HighestCategory, 64}, {&e.InputHash, 64}, {&e.KeywordList, 64}, {&e.EngineModel, 255}, {&e.Error, 255}, {&e.RuleId, 64},
	} {
		if utf8.RuneCountInString(*field.value) > field.limit {
			*field.value = string([]rune(*field.value)[:field.limit])
		}
	}
}

// ContentModerationFlaggedHash is one entry of the hash blocklist: the
// SHA-256 of an input that was flagged before.
type ContentModerationFlaggedHash struct {
	InputHash string `json:"input_hash" gorm:"type:varchar(64);primaryKey"`
	CreatedAt int64  `json:"created_at" gorm:"bigint;index"`
}

// ContentModerationKeywordList is one named keyword list, one keyword per
// line. The built-in "default" list is the global sensitive words and is not
// stored here.
type ContentModerationKeywordList struct {
	Id        int      `json:"id"`
	Name      string   `json:"name" gorm:"type:varchar(64);uniqueIndex"`
	Words     LongText `json:"words"`
	UpdatedAt int64    `json:"updated_at" gorm:"bigint"`
	// Revision grows by one on every save, so changes within the same second
	// are still told apart.
	Revision int64 `json:"revision"`
}

// ListContentModerationKeywordLists returns every stored list by name.
func ListContentModerationKeywordLists() ([]*ContentModerationKeywordList, error) {
	var lists []*ContentModerationKeywordList
	err := DB.Order("name").Find(&lists).Error
	return lists, err
}

// SaveContentModerationKeywordList creates or replaces a list.
func SaveContentModerationKeywordList(name string, words []string, updatedAt int64) error {
	joined := LongText(strings.Join(words, "\n"))
	return DB.Transaction(func(tx *gorm.DB) error {
		var existing ContentModerationKeywordList
		if err := lockForUpdate(tx).Where("name = ?", name).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if existing.Id == 0 {
			return tx.Create(&ContentModerationKeywordList{Name: name, Words: joined, UpdatedAt: updatedAt, Revision: 1}).Error
		}
		return tx.Model(&ContentModerationKeywordList{}).Where("id = ?", existing.Id).Updates(map[string]any{
			"words": joined, "updated_at": updatedAt, "revision": gorm.Expr("COALESCE(revision, 0) + 1"),
		}).Error
	})
}

func DeleteContentModerationKeywordList(name string) (int64, error) {
	result := DB.Where("name = ?", name).Delete(&ContentModerationKeywordList{})
	return result.RowsAffected, result.Error
}

// ContentModerationKeywordListsVersion summarizes the stored lists so a node
// can tell, with one small query, whether its matchers are stale. Every save
// raises the revision sum; a delete changes the count and the id sum.
func ContentModerationKeywordListsVersion() (string, error) {
	var summary struct {
		Total     int64
		Revisions int64
		Ids       int64
	}
	err := DB.Model(&ContentModerationKeywordList{}).
		Select("COUNT(*) AS total, COALESCE(SUM(revision), 0) AS revisions, COALESCE(SUM(id), 0) AS ids").Scan(&summary).Error
	return fmt.Sprintf("%d/%d/%d", summary.Total, summary.Revisions, summary.Ids), err
}

// ContentModerationEventFilter narrows event listing and filtered deletion.
type ContentModerationEventFilter struct {
	Flagged   string `form:"flagged" json:"flagged"`
	Action    string `form:"action" json:"action"`
	Category  string `form:"category" json:"category"`
	UserId    int    `form:"user_id" json:"user_id"`
	TokenId   int    `form:"token_id" json:"token_id"`
	Group     string `form:"group" json:"group"`
	ModelName string `form:"model_name" json:"model_name"`
	RequestId string `form:"request_id" json:"request_id"`
	InputHash string `form:"input_hash" json:"input_hash"`
	Keyword   string `form:"keyword" json:"keyword"`
	StartTime int64  `form:"start_time" json:"start_time"`
	EndTime   int64  `form:"end_time" json:"end_time"`
}

const contentModerationDeleteBatchSize = 500

func (f ContentModerationEventFilter) apply(tx *gorm.DB) *gorm.DB {
	switch f.Flagged {
	case "true":
		tx = tx.Where("flagged = ?", true)
	case "false":
		tx = tx.Where("flagged = ?", false)
	}
	if f.Action != "" {
		tx = tx.Where("action = ?", f.Action)
	}
	if f.Category != "" {
		tx = tx.Where("highest_category = ?", f.Category)
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
	if f.InputHash != "" {
		tx = tx.Where("input_hash = ?", strings.ToLower(f.InputHash))
	}
	if keyword := strings.TrimSpace(f.Keyword); keyword != "" {
		// LOWER on both sides keeps PostgreSQL case-insensitive like MySQL/SQLite.
		// '!' escapes the wildcards so a typed % or _ matches itself; a
		// backslash escape would read differently on MySQL and PostgreSQL.
		escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(keyword))
		pattern := "%" + escaped + "%"
		tx = tx.Where("LOWER(request_id) LIKE ? ESCAPE '!' OR LOWER(username) LIKE ? ESCAPE '!' OR LOWER(token_name) LIKE ? ESCAPE '!' OR LOWER(input_excerpt) LIKE ? ESCAPE '!' OR LOWER(model_name) LIKE ? ESCAPE '!'",
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

// CreateContentModerationEvent stores one event.
func CreateContentModerationEvent(event *ContentModerationEvent) error {
	event.fitColumns()
	return DB.Create(event).Error
}

// CreateContentModerationEvents stores a batch in one insert. If the batch
// fails, each event is retried on its own so one bad row cannot lose the
// others. It returns how many events were stored and the last error.
func CreateContentModerationEvents(events []*ContentModerationEvent) (int, error) {
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

// CountContentModerationViolations counts the user's flagged events since
// the given time that count toward automatic disabling: allowlisted and
// hash-blocklist events (blocked or, in observe mode, only recorded) are
// excluded, and so is everything up to the user's
// last automatic disable, so a re-enabled user starts from zero.
func CountContentModerationViolations(userId int, since int64) (int64, error) {
	var lastDisabled int64
	err := DB.Model(&ContentModerationEvent{}).
		Where("user_id = ? AND auto_disabled = ?", userId, true).
		Select("COALESCE(MAX(created_at), 0)").Scan(&lastDisabled).Error
	if err != nil {
		return 0, err
	}
	var count int64
	err = DB.Model(&ContentModerationEvent{}).
		Where("user_id = ? AND flagged = ? AND allowlisted = ? AND action <> ? AND highest_category <> ?",
			userId, true, false, ContentModerationActionHashBlock, ContentModerationHashCategory).
		Where("created_at >= ? AND created_at > ?", since, lastDisabled).
		Count(&count).Error
	return count, err
}

// ListContentModerationEvents returns one page of events, newest first.
func ListContentModerationEvents(filter ContentModerationEventFilter, offset, limit int) ([]*ContentModerationEvent, int64, error) {
	var total int64
	if err := filter.apply(DB.Model(&ContentModerationEvent{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	events := make([]*ContentModerationEvent, 0, limit)
	err := filter.apply(DB.Model(&ContentModerationEvent{})).Order("id desc").Offset(offset).Limit(limit).Find(&events).Error
	return events, total, err
}

func GetContentModerationEvent(id int64) (*ContentModerationEvent, error) {
	var event ContentModerationEvent
	if err := DB.First(&event, id).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

func DeleteContentModerationEventsByIds(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result := DB.Where("id IN ?", ids).Delete(&ContentModerationEvent{})
	return result.RowsAffected, result.Error
}

// PreviewContentModerationEventDeletion counts the matching events and
// returns the highest matching id, which bounds a later deletion so events
// recorded after the preview are kept.
func PreviewContentModerationEventDeletion(filter ContentModerationEventFilter) (int64, int64, error) {
	var summary struct {
		Total int64
		MaxId int64
	}
	err := filter.apply(DB.Model(&ContentModerationEvent{})).Select("COUNT(*) AS total, COALESCE(MAX(id), 0) AS max_id").Scan(&summary).Error
	return summary.Total, summary.MaxId, err
}

// DeleteContentModerationEventsByFilter deletes matching events with
// id <= maxId in batches.
func DeleteContentModerationEventsByFilter(filter ContentModerationEventFilter, maxId int64) (int64, error) {
	return deleteContentModerationEventsInBatches(func() *gorm.DB {
		return filter.apply(DB.Model(&ContentModerationEvent{})).Where("id <= ?", maxId)
	})
}

// CleanupContentModerationEvents deletes flagged events created before
// hitBefore and other events created before nonHitBefore.
func CleanupContentModerationEvents(hitBefore, nonHitBefore int64) (int64, error) {
	hits, err := deleteContentModerationEventsInBatches(func() *gorm.DB {
		return DB.Model(&ContentModerationEvent{}).Where("flagged = ? AND created_at < ?", true, hitBefore)
	})
	if err != nil {
		return hits, err
	}
	nonHits, err := deleteContentModerationEventsInBatches(func() *gorm.DB {
		return DB.Model(&ContentModerationEvent{}).Where("flagged = ? AND created_at < ?", false, nonHitBefore)
	})
	return hits + nonHits, err
}

// deleteContentModerationEventsInBatches reads ids first because LIMIT on
// DELETE is not portable (the SQLite dialect drops it and would delete
// everything in one long lock).
func deleteContentModerationEventsInBatches(query func() *gorm.DB) (int64, error) {
	var deleted int64
	for {
		var ids []int64
		if err := query().Order("id").Limit(contentModerationDeleteBatchSize).Pluck("id", &ids).Error; err != nil {
			return deleted, err
		}
		if len(ids) == 0 {
			return deleted, nil
		}
		count, err := DeleteContentModerationEventsByIds(ids)
		deleted += count
		if err != nil {
			return deleted, err
		}
		if len(ids) < contentModerationDeleteBatchSize {
			return deleted, nil
		}
	}
}

// AddContentModerationFlaggedHash records a flagged input hash; an existing
// entry is left as it is.
func AddContentModerationFlaggedHash(inputHash string, createdAt int64) error {
	return DB.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&ContentModerationFlaggedHash{InputHash: inputHash, CreatedAt: createdAt}).Error
}

// ListContentModerationFlaggedHashes returns every blocklisted hash.
func ListContentModerationFlaggedHashes() ([]string, error) {
	var hashes []string
	err := DB.Model(&ContentModerationFlaggedHash{}).Pluck("input_hash", &hashes).Error
	return hashes, err
}

func DeleteContentModerationFlaggedHash(inputHash string) (int64, error) {
	result := DB.Where("input_hash = ?", inputHash).Delete(&ContentModerationFlaggedHash{})
	return result.RowsAffected, result.Error
}

// CleanupContentModerationFlaggedHashes deletes hashes recorded before the
// cutoff, then the oldest ones beyond maxRows, in batches of oldest first.
func CleanupContentModerationFlaggedHashes(before int64, maxRows int64) (int64, error) {
	var deleted int64
	for {
		var total, stale int64
		if err := DB.Model(&ContentModerationFlaggedHash{}).Count(&total).Error; err != nil {
			return deleted, err
		}
		if err := DB.Model(&ContentModerationFlaggedHash{}).Where("created_at < ?", before).Count(&stale).Error; err != nil {
			return deleted, err
		}
		batch := min(max(stale, total-maxRows), contentModerationDeleteBatchSize)
		if batch <= 0 {
			return deleted, nil
		}
		var hashes []string
		if err := DB.Model(&ContentModerationFlaggedHash{}).Order("created_at").Order("input_hash").
			Limit(int(batch)).Pluck("input_hash", &hashes).Error; err != nil {
			return deleted, err
		}
		if len(hashes) == 0 {
			return deleted, nil
		}
		result := DB.Where("input_hash IN ?", hashes).Delete(&ContentModerationFlaggedHash{})
		deleted += result.RowsAffected
		if result.Error != nil {
			return deleted, result.Error
		}
	}
}

// ContentModerationFlaggedHashesVersion summarizes the blocklist in one row
// (count and the oldest and newest entry times) so a node can skip reading
// the whole table when nothing changed.
func ContentModerationFlaggedHashesVersion() (string, error) {
	var summary struct {
		Total  int64
		Oldest int64
		Newest int64
	}
	err := DB.Model(&ContentModerationFlaggedHash{}).
		Select("COUNT(*) AS total, COALESCE(MIN(created_at), 0) AS oldest, COALESCE(MAX(created_at), 0) AS newest").Scan(&summary).Error
	return fmt.Sprintf("%d/%d/%d", summary.Total, summary.Oldest, summary.Newest), err
}

// ClearContentModerationFlaggedHashes deletes the whole blocklist.
func ClearContentModerationFlaggedHashes() (int64, error) {
	result := DB.Where("1 = 1").Delete(&ContentModerationFlaggedHash{})
	return result.RowsAffected, result.Error
}

var (
	contentModerationConfigMutex sync.Mutex

	ErrContentModerationConfigConflict = common.NewMessage("The content moderation settings were changed by someone else. Reload and try again")
)

// ContentModerationStoredConfig reads the configuration and API keys from
// the database, the source of truth shared by all nodes.
func ContentModerationStoredConfig() (content_moderation_setting.Config, []string, error) {
	values := map[string]string{}
	var options []Option
	keys := []string{content_moderation_setting.OptionKey, content_moderation_setting.KeysOptionKey}
	if err := DB.Where(map[string]any{"key": keys}).Find(&options).Error; err != nil {
		return content_moderation_setting.Config{}, nil, err
	}
	for _, option := range options {
		values[option.Key] = option.Value
	}
	config, err := content_moderation_setting.ParseConfig(values[content_moderation_setting.OptionKey])
	if err != nil {
		return content_moderation_setting.Config{}, nil, err
	}
	return config, content_moderation_setting.ParseKeys(values[content_moderation_setting.KeysOptionKey]), nil
}

// SaveContentModerationConfig stores a validated configuration and its API
// keys when the stored version still equals expectedVersion, then bumps the
// version. The process mutex serializes writers on one node; the version
// check inside the transaction rejects a concurrent save from another node.
func SaveContentModerationConfig(config content_moderation_setting.Config, apiKeys []string, expectedVersion int64) (content_moderation_setting.Config, error) {
	if err := config.Normalize(); err != nil {
		return config, err
	}
	contentModerationConfigMutex.Lock()
	defer contentModerationConfigMutex.Unlock()
	// Create the row first so the locking read below always has a row to lock.
	_ = DB.Where(Option{Key: content_moderation_setting.OptionKey}).Attrs(Option{Value: ""}).FirstOrCreate(&Option{}).Error
	var configJSON, keysJSON []byte
	err := DB.Transaction(func(tx *gorm.DB) error {
		var stored Option
		err := lockForUpdate(tx).Where(&Option{Key: content_moderation_setting.OptionKey}).Limit(1).Find(&stored).Error
		if err != nil {
			return err
		}
		storedConfig, err := content_moderation_setting.ParseConfig(stored.Value)
		if err != nil {
			storedConfig = content_moderation_setting.DefaultConfig()
		}
		if storedConfig.ConfigVersion != expectedVersion {
			return ErrContentModerationConfigConflict
		}
		config.ConfigVersion = expectedVersion + 1
		if configJSON, err = common.Marshal(config); err != nil {
			return err
		}
		if keysJSON, err = common.Marshal(content_moderation_setting.NormalizeKeys(apiKeys)); err != nil {
			return err
		}
		for key, value := range map[string]string{
			content_moderation_setting.OptionKey:     string(configJSON),
			content_moderation_setting.KeysOptionKey: string(keysJSON),
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
	if err := updateOptionMap(content_moderation_setting.KeysOptionKey, string(keysJSON)); err != nil {
		return config, err
	}
	return config, updateOptionMap(content_moderation_setting.OptionKey, string(configJSON))
}
