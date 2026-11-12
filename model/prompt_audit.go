package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	// ScanIncomplete marks findings kept although a later chunk failed to scan.
	ScanIncomplete bool `json:"scan_incomplete"`
	// HitChunks is a JSON list of the chunks that were not passed: their
	// position in scan order, the roles of their text, decision and categories.
	// It never holds prompt text.
	HitChunks       string `json:"hit_chunks" gorm:"type:text"`
	LatencyMs       int    `json:"latency_ms"`
	PromptHash      string `json:"prompt_hash" gorm:"type:varchar(64);index"`
	RedactedPreview string `json:"redacted_preview" gorm:"type:varchar(512)"`
	PromptLength    int    `json:"prompt_length"`
	MessageCount    int    `json:"message_count"`
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

// Keyword search modes and fields. Plain mode matches a case-insensitive
// substring; regex mode matches an RE2 expression in Go, never in the
// database, so SQLite, MySQL and PostgreSQL return the same rows.
const (
	PromptAuditKeywordModePlain       = "plain"
	PromptAuditKeywordModeRegex       = "regex"
	PromptAuditKeywordFieldFullPrompt = "full_prompt"
	// PromptAuditKeywordMaxLength bounds a scanned search pattern in characters.
	PromptAuditKeywordMaxLength = 512
)

// promptAuditKeywordFields are the searchable columns. Without a selection a
// search uses every one except full_prompt, which must be chosen explicitly.
var promptAuditKeywordFields = []string{"request_id", "username", "token_name", "redacted_preview", "model_name", PromptAuditKeywordFieldFullPrompt}

var (
	ErrPromptAuditInvalidKeywordMode  = common.NewMessage("Unknown keyword search mode")
	ErrPromptAuditInvalidKeywordField = common.NewMessage("Unknown keyword search field")
	ErrPromptAuditKeywordTooLong      = common.NewMessage("The search pattern is longer than {{max}} characters", map[string]any{"max": PromptAuditKeywordMaxLength})
	// ErrPromptAuditScanFilterDelete rejects filtered deletion of a search
	// whose matches are only known after scanning rows in Go.
	ErrPromptAuditScanFilterDelete = common.NewMessage("Regex and full prompt searches cannot delete by filter because their matches are found by scanning. Select the results and delete them instead")
	// ErrPromptAuditScanFilterList keeps a scanned keyword out of the
	// paginated SQL listing, which would ignore it.
	ErrPromptAuditScanFilterList = common.NewMessage("Regex and full prompt searches are paged with a cursor")
)

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
	// KeywordMode is plain (default) or regex.
	KeywordMode string `form:"keyword_mode" json:"keyword_mode"`
	// KeywordFields is a comma-separated subset of promptAuditKeywordFields.
	KeywordFields string `form:"keyword_fields" json:"keyword_fields"`
	// CaseSensitive applies to regex mode; plain mode always ignores case.
	CaseSensitive bool  `form:"case_sensitive" json:"case_sensitive"`
	StartTime     int64 `form:"start_time" json:"start_time"`
	EndTime       int64 `form:"end_time" json:"end_time"`
}

// SearchFields returns the validated keyword fields, defaulting to every
// field except full_prompt.
func (f PromptAuditEventFilter) SearchFields() ([]string, error) {
	fields := make([]string, 0, len(promptAuditKeywordFields))
	for field := range strings.SplitSeq(f.KeywordFields, ",") {
		field = strings.TrimSpace(field)
		if field == "" || slices.Contains(fields, field) {
			continue
		}
		if !slices.Contains(promptAuditKeywordFields, field) {
			return nil, ErrPromptAuditInvalidKeywordField
		}
		fields = append(fields, field)
	}
	if len(fields) == 0 {
		fields = append(fields, promptAuditKeywordFields[:len(promptAuditKeywordFields)-1]...)
	}
	return fields, nil
}

// Scans reports whether the keyword is matched by reading rows in Go: in
// regex mode, or when full_prompt is searched, whose LIKE would read the
// whole table without any bound.
func (f PromptAuditEventFilter) Scans() (bool, error) {
	if strings.TrimSpace(f.Keyword) == "" {
		return false, nil
	}
	switch f.KeywordMode {
	case "", PromptAuditKeywordModePlain:
	case PromptAuditKeywordModeRegex:
		return true, nil
	default:
		return false, ErrPromptAuditInvalidKeywordMode
	}
	fields, err := f.SearchFields()
	if err != nil {
		return false, err
	}
	return slices.Contains(fields, PromptAuditKeywordFieldFullPrompt), nil
}

// Validate checks the keyword mode and fields.
func (f PromptAuditEventFilter) Validate() error {
	if _, err := f.Scans(); err != nil {
		return err
	}
	_, err := f.SearchFields()
	return err
}

// KeywordPattern compiles the keyword for scanning. Plain mode becomes a
// literal, case-insensitive pattern; a regex that does not compile returns a
// message naming the RE2 error.
func (f PromptAuditEventFilter) KeywordPattern() (*regexp.Regexp, error) {
	keyword := strings.TrimSpace(f.Keyword)
	if utf8.RuneCountInString(keyword) > PromptAuditKeywordMaxLength {
		return nil, ErrPromptAuditKeywordTooLong
	}
	if f.KeywordMode != PromptAuditKeywordModeRegex {
		return regexp.Compile("(?i)" + regexp.QuoteMeta(keyword))
	}
	expression := keyword
	if !f.CaseSensitive {
		expression = "(?i)" + expression
	}
	pattern, err := regexp.Compile(expression)
	if err != nil {
		return nil, common.NewMessage("Invalid regular expression (RE2 syntax): {{error}}", map[string]any{"error": err.Error()})
	}
	return pattern, nil
}

const promptAuditDeleteBatchSize = 500

var promptAuditLikeEscaper = strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")

// apply adds the SQL conditions. A keyword that is matched by scanning is
// left to scanPromptAuditEvents; callers must not delete with such a filter.
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
		if scans, _ := f.Scans(); !scans {
			fields, err := f.SearchFields()
			if err != nil {
				// Fail closed: an invalid field must never widen a deletion.
				return tx.Where("1 = 0")
			}
			// LOWER on both sides keeps PostgreSQL case-insensitive like MySQL/SQLite.
			// % and _ are escaped so the keyword is a literal substring, as
			// it is when full_prompt is scanned; '!' avoids the backslash,
			// which MySQL and PostgreSQL quote differently.
			pattern := "%" + promptAuditLikeEscaper.Replace(strings.ToLower(keyword)) + "%"
			conditions := make([]string, 0, len(fields))
			args := make([]any, 0, len(fields))
			for _, field := range fields {
				conditions = append(conditions, "LOWER("+field+") LIKE ? ESCAPE '!'")
				args = append(args, pattern)
			}
			tx = tx.Where(strings.Join(conditions, " OR "), args...)
		}
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
// It serves SQL-only filters; a keyword matched by scanning goes through
// ScanPromptAuditEvents.
func ListPromptAuditEvents(filter PromptAuditEventFilter, offset, limit int) ([]*PromptAuditEvent, int64, error) {
	if err := filter.Validate(); err != nil {
		return nil, 0, err
	}
	if scans, _ := filter.Scans(); scans {
		return nil, 0, ErrPromptAuditScanFilterList
	}
	var total int64
	if err := filter.apply(DB.Model(&PromptAuditEvent{})).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	events := make([]*PromptAuditEvent, 0, limit)
	err := filter.apply(DB.Model(&PromptAuditEvent{})).Omit("full_prompt").
		Order("id desc").Offset(offset).Limit(limit).Find(&events).Error
	return events, total, err
}

// promptAuditMySQLMaxExecutionTime adds a MAX_EXECUTION_TIME optimizer hint
// (milliseconds, MySQL 5.7.8+) to a SELECT, as gorm.io/hints does.
type promptAuditMySQLMaxExecutionTime int64

func (h promptAuditMySQLMaxExecutionTime) ModifyStatement(stmt *gorm.Statement) {
	selectClause := stmt.Clauses["SELECT"]
	selectClause.AfterNameExpression = clause.Expr{SQL: fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */", int64(h))}
	stmt.Clauses["SELECT"] = selectClause
}

func (promptAuditMySQLMaxExecutionTime) Build(clause.Builder) {}

// promptAuditMySQLQueryTimeout is ER_QUERY_TIMEOUT, returned when
// MAX_EXECUTION_TIME stops a query.
const promptAuditMySQLQueryTimeout = 3024

// Rows read per scan batch; tests lower them to exercise several batches.
var (
	promptAuditScanBatchRows           = 1000
	promptAuditScanFullPromptBatchRows = 100
)

// Defaults for one scanned search request.
const (
	PromptAuditScanDefaultMaxRows = 20000
	PromptAuditScanDefaultBudget  = 5 * time.Second
)

// PromptAuditScanLimits bound one scanned search request.
type PromptAuditScanLimits struct {
	// MaxRows is the most candidate rows read and matched.
	MaxRows int
	// Deadline ends reading and matching; zero means PromptAuditScanDefaultBudget from now.
	Deadline time.Time
}

// Reasons a scanned search stopped before reading every candidate row.
const (
	PromptAuditScanStopPageFull   = "page_full"
	PromptAuditScanStopScanLimit  = "scan_limit"
	PromptAuditScanStopTimeBudget = "time_budget"
)

// PromptAuditScanPage is one page of a scanned search. Items never carry the
// full prompt. When Truncated, rows below NextCursor (the id of the last row
// read) were not read yet; passing it back as the cursor continues there, so
// continued pages neither repeat nor skip a row.
type PromptAuditScanPage struct {
	Items      []*PromptAuditEvent `json:"items"`
	Truncated  bool                `json:"truncated"`
	NextCursor int64               `json:"next_cursor"`
	Scanned    int                 `json:"scanned"`
	StopReason string              `json:"stop_reason,omitempty"`
}

// searchText returns the value of a keyword search field.
func (e *PromptAuditEvent) searchText(field string) string {
	switch field {
	case "request_id":
		return e.RequestId
	case "username":
		return e.Username
	case "token_name":
		return e.TokenName
	case "redacted_preview":
		return e.RedactedPreview
	case "model_name":
		return e.ModelName
	case PromptAuditKeywordFieldFullPrompt:
		return string(e.FullPrompt)
	}
	return ""
}

// ScanPromptAuditEvents finds events whose searched fields match the keyword
// pattern. The other filters run in SQL; the remaining rows are read newest
// first in id batches (keyset, below cursor when it is set), with only id and
// the searched columns, and matched in Go. It stops once limit events match,
// MaxRows rows were read or the deadline passed. Every batch read is bounded
// by the deadline; when it passes NextCursor is the last id read, or the
// given cursor when none was, so continuing never skips a row.
func ScanPromptAuditEvents(ctx context.Context, filter PromptAuditEventFilter, cursor int64, limit int, limits PromptAuditScanLimits) (*PromptAuditScanPage, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	pattern, err := filter.KeywordPattern()
	if err != nil {
		return nil, err
	}
	fields, _ := filter.SearchFields()
	if limits.MaxRows <= 0 {
		limits.MaxRows = PromptAuditScanDefaultMaxRows
	}
	if limits.Deadline.IsZero() {
		limits.Deadline = time.Now().Add(PromptAuditScanDefaultBudget)
	}
	limit = max(limit, 1)
	// Full prompts can be 64K characters each, so read fewer per batch.
	batchSize := promptAuditScanBatchRows
	if slices.Contains(fields, PromptAuditKeywordFieldFullPrompt) {
		batchSize = promptAuditScanFullPromptBatchRows
	}
	columns := append([]string{"id"}, fields...)
	page := &PromptAuditScanPage{Items: []*PromptAuditEvent{}}
	hits := make([]int64, 0, limit)
scan:
	for {
		size := min(batchSize, limits.MaxRows-page.Scanned)
		query := filter.apply(DB.Model(&PromptAuditEvent{})).Select(columns)
		if cursor > 0 {
			query = query.Where("id < ?", cursor)
		}
		// Each batch read ends at the deadline; a read cut off by it ends the
		// page like a spent budget, so the search continues from cursor.
		batchCtx, cancel := context.WithDeadline(ctx, limits.Deadline)
		query = query.WithContext(batchCtx)
		if DB.Dialector.Name() == string(common.DatabaseTypeMySQL) {
			// The MySQL driver does not stop a running query when the context
			// ends, so the server is told to stop at the deadline itself.
			query = query.Clauses(promptAuditMySQLMaxExecutionTime(max(time.Until(limits.Deadline).Milliseconds(), 1)))
		}
		var rows []*PromptAuditEvent
		err := query.Order("id desc").Limit(size).Find(&rows).Error
		var mysqlErr *mysql.MySQLError
		timedOut := (batchCtx.Err() != nil && ctx.Err() == nil) ||
			(errors.As(err, &mysqlErr) && mysqlErr.Number == promptAuditMySQLQueryTimeout)
		cancel()
		if err != nil && timedOut {
			page.Truncated, page.NextCursor, page.StopReason = true, cursor, PromptAuditScanStopTimeBudget
			break
		}
		if err != nil {
			return nil, err
		}
		for i, row := range rows {
			cursor = row.Id
			page.Scanned++
			if slices.ContainsFunc(fields, func(field string) bool { return pattern.MatchString(row.searchText(field)) }) {
				hits = append(hits, row.Id)
			}
			var reason string
			switch {
			case len(hits) >= limit:
				reason = PromptAuditScanStopPageFull
			case page.Scanned >= limits.MaxRows:
				reason = PromptAuditScanStopScanLimit
			case time.Now().After(limits.Deadline):
				reason = PromptAuditScanStopTimeBudget
			}
			if reason == "" {
				continue
			}
			if i < len(rows)-1 || len(rows) == size {
				page.Truncated, page.NextCursor, page.StopReason = true, cursor, reason
			}
			break scan
		}
		if len(rows) < size {
			break
		}
	}
	if len(hits) > 0 {
		err = DB.WithContext(ctx).Omit("full_prompt").Where("id IN ?", hits).Order("id desc").Find(&page.Items).Error
	}
	return page, err
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

// DeletionHash identifies the filter and snapshot a deletion preview counted.
// The keyword settings are normalized the way apply reads them, so
// equivalent filters hash the same; any change to what would be deleted
// changes the hash.
func (f PromptAuditEventFilter) DeletionHash(snapshotMaxId int64) (string, error) {
	fields, err := f.SearchFields()
	if err != nil {
		return "", err
	}
	keyword := strings.TrimSpace(f.Keyword)
	mode := f.KeywordMode
	if mode == "" {
		mode = PromptAuditKeywordModePlain
	}
	slices.Sort(fields)
	if keyword == "" {
		fields, mode = nil, ""
	}
	payload, err := common.Marshal(struct {
		Filter        PromptAuditEventFilter `json:"filter"`
		SnapshotMaxId int64                  `json:"snapshot_max_id"`
	}{PromptAuditEventFilter{
		Decision: f.Decision, RiskLevel: f.RiskLevel,
		UserId: f.UserId, TokenId: f.TokenId, Group: f.Group, ModelName: f.ModelName, RequestId: f.RequestId,
		PromptHash: strings.ToLower(f.PromptHash), Keyword: keyword, KeywordMode: mode,
		KeywordFields: strings.Join(fields, ","), StartTime: f.StartTime, EndTime: f.EndTime,
	}, snapshotMaxId})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// PreviewPromptAuditEventDeletion counts the matching events and returns the
// highest matching id, which bounds a later DeletePromptAuditEventsByFilter so
// events recorded after the preview are not deleted.
func PreviewPromptAuditEventDeletion(filter PromptAuditEventFilter) (int64, int64, error) {
	if err := filter.deletable(); err != nil {
		return 0, 0, err
	}
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
	if err := filter.deletable(); err != nil {
		return 0, err
	}
	return deletePromptAuditEventsInBatches(func() *gorm.DB {
		return filter.apply(DB.Model(&PromptAuditEvent{})).Where("id <= ?", maxId)
	})
}

// deletable rejects filters whose keyword is matched by scanning: apply
// leaves that keyword out, so deleting with it would remove non-matching rows.
func (f PromptAuditEventFilter) deletable() error {
	if err := f.Validate(); err != nil {
		return err
	}
	if scans, _ := f.Scans(); scans {
		return ErrPromptAuditScanFilterDelete
	}
	return nil
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
