package model

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// withPromptAuditEventDB points DB at a fresh prompt_audit_events table on
// SQLite, or on MySQL / PostgreSQL when TEST_MYSQL_DSN / TEST_POSTGRES_DSN
// are set, and stores events oldest first.
func withPromptAuditEventDB(t *testing.T, dialect string, events []*PromptAuditEvent) {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(t.TempDir() + "/prompt_audit.db")
	case "mysql":
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("TEST_MYSQL_DSN is not configured")
		}
		driver = mysql.Open(dsn)
	case "postgres":
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		driver = postgres.Open(dsn)
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Migrator().DropTable(&PromptAuditEvent{}))
	require.NoError(t, db.AutoMigrate(&PromptAuditEvent{}))
	previous := DB
	DB = db
	t.Cleanup(func() {
		DB = previous
		_ = db.Migrator().DropTable(&PromptAuditEvent{})
		if connection, err := db.DB(); err == nil {
			_ = connection.Close()
		}
	})
	if len(events) > 0 {
		stored, err := CreatePromptAuditEvents(events)
		require.NoError(t, err)
		require.Equal(t, len(events), stored)
	}
}

func promptAuditTestDialects(t *testing.T, run func(t *testing.T, dialect string)) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) { run(t, dialect) })
	}
}

func promptAuditUsernames(events []*PromptAuditEvent) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Username)
	}
	return names
}

func TestScanPromptAuditEventsReadsKeysetBatchesWithinLimits(t *testing.T) {
	previousBatch := promptAuditScanBatchRows
	promptAuditScanBatchRows = 2
	t.Cleanup(func() { promptAuditScanBatchRows = previousBatch })
	promptAuditTestDialects(t, func(t *testing.T, dialect string) {
		events := []*PromptAuditEvent{
			{CreatedAt: 1, Decision: "flag", Username: "a1", ModelName: "m-secret"},
			{CreatedAt: 2, Decision: "critical", Username: "b2", ModelName: "m"},
			{CreatedAt: 3, Decision: "flag", Username: "c3", ModelName: "m-secret"},
			{CreatedAt: 4, Decision: "flag", Username: "d4", ModelName: "m"},
			{CreatedAt: 5, Decision: "flag", Username: "e5", ModelName: "m-SECRET"},
		}
		withPromptAuditEventDB(t, dialect, events)
		var scanSQL []string
		require.NoError(t, DB.Callback().Query().After("gorm:query").Register("test:capture_scan_sql", func(tx *gorm.DB) {
			scanSQL = append(scanSQL, tx.Statement.SQL.String())
		}))
		filter := PromptAuditEventFilter{Keyword: "secret", KeywordMode: PromptAuditKeywordModeRegex, KeywordFields: "model_name", Decision: "flag"}
		limits := PromptAuditScanLimits{MaxRows: 100, Deadline: time.Now().Add(time.Minute)}

		page, err := ScanPromptAuditEvents(context.Background(), filter, 0, 10, limits)
		require.NoError(t, err)
		assert.Equal(t, []string{"e5", "c3", "a1"}, promptAuditUsernames(page.Items), "matches across 2-row batches, newest first")
		assert.False(t, page.Truncated)
		assert.Equal(t, 4, page.Scanned, "only rows passing the SQL filter are read")
		require.NotEmpty(t, scanSQL)
		// MySQL keeps running a query after its context ends, so only there
		// the server is told the deadline; the hint runs on the real server.
		assert.Equal(t, dialect == "mysql", strings.Contains(scanSQL[0], "MAX_EXECUTION_TIME("), scanSQL[0])

		var seen []string
		cursor := int64(0)
		limits.MaxRows = 1
		for range len(events) {
			page, err = ScanPromptAuditEvents(context.Background(), filter, cursor, 10, limits)
			require.NoError(t, err)
			assert.LessOrEqual(t, page.Scanned, 1)
			seen = append(seen, promptAuditUsernames(page.Items)...)
			if !page.Truncated {
				break
			}
			assert.Equal(t, PromptAuditScanStopScanLimit, page.StopReason)
			if cursor > 0 {
				assert.Less(t, page.NextCursor, cursor, "the cursor only moves down")
			}
			cursor = page.NextCursor
		}
		assert.Equal(t, []string{"e5", "c3", "a1"}, seen, "continued one-row scans neither repeat nor skip")

		expired := PromptAuditScanLimits{MaxRows: 100, Deadline: time.Now().Add(-time.Second)}
		page, err = ScanPromptAuditEvents(context.Background(), filter, events[3].Id, 10, expired)
		require.NoError(t, err, "a read cut off by the deadline is a truncated page, not an error")
		assert.True(t, page.Truncated)
		assert.Equal(t, PromptAuditScanStopTimeBudget, page.StopReason)
		assert.Equal(t, 0, page.Scanned)
		assert.Equal(t, events[3].Id, page.NextCursor, "nothing was read, so continuing retries from the same cursor")

		for _, fields := range []string{"password", "username;DROP TABLE prompt_audit_events", "id"} {
			_, err = ScanPromptAuditEvents(context.Background(), PromptAuditEventFilter{Keyword: "x", KeywordMode: PromptAuditKeywordModeRegex, KeywordFields: fields}, 0, 10, limits)
			assert.ErrorIs(t, err, ErrPromptAuditInvalidKeywordField, fields)
		}
		var count int64
		require.NoError(t, DB.Model(&PromptAuditEvent{}).Count(&count).Error)
		assert.Equal(t, int64(5), count)
	})
}

func TestPromptAuditPlainKeywordMatchesLiterallyOnSelectedFields(t *testing.T) {
	promptAuditTestDialects(t, func(t *testing.T, dialect string) {
		withPromptAuditEventDB(t, dialect, []*PromptAuditEvent{
			{CreatedAt: 1, Username: "a_b", TokenName: "100% key"},
			{CreatedAt: 2, Username: "axb", TokenName: "1000 key"},
			{CreatedAt: 3, Username: "Bang!x", RequestId: "a_b"},
		})
		cases := []struct {
			filter PromptAuditEventFilter
			want   []string
		}{
			{PromptAuditEventFilter{Keyword: "A_B", KeywordFields: "username"}, []string{"a_b"}},
			{PromptAuditEventFilter{Keyword: "a_b"}, []string{"Bang!x", "a_b"}},
			{PromptAuditEventFilter{Keyword: "100%", KeywordFields: "token_name"}, []string{"a_b"}},
			{PromptAuditEventFilter{Keyword: "g!x", KeywordFields: "username"}, []string{"Bang!x"}},
			{PromptAuditEventFilter{Keyword: "a_b", KeywordFields: "request_id"}, []string{"Bang!x"}},
		}
		for _, tc := range cases {
			events, total, err := ListPromptAuditEvents(tc.filter, 0, 10)
			require.NoError(t, err)
			assert.Equal(t, tc.want, promptAuditUsernames(events), "%+v", tc.filter)
			assert.Equal(t, int64(len(tc.want)), total)
		}

		count, maxId, err := PreviewPromptAuditEventDeletion(PromptAuditEventFilter{Keyword: "a_b", KeywordFields: "username"})
		require.NoError(t, err)
		require.Equal(t, int64(1), count)
		deleted, err := DeletePromptAuditEventsByFilter(PromptAuditEventFilter{Keyword: "a_b", KeywordFields: "username"}, maxId)
		require.NoError(t, err)
		assert.Equal(t, int64(1), deleted, "deletion uses the same literal match")

		_, _, err = ListPromptAuditEvents(PromptAuditEventFilter{Keyword: "x", KeywordFields: "password"}, 0, 10)
		assert.ErrorIs(t, err, ErrPromptAuditInvalidKeywordField)
		_, err = DeletePromptAuditEventsByFilter(PromptAuditEventFilter{Keyword: "x", KeywordFields: "password"}, maxId)
		assert.ErrorIs(t, err, ErrPromptAuditInvalidKeywordField, "an invalid field never widens a deletion")
	})
}

func TestPromptAuditDeletionHash(t *testing.T) {
	base := PromptAuditEventFilter{Decision: "flag", Keyword: "secret", KeywordFields: "username,model_name", PromptHash: "ABC", StartTime: 10}
	hash := func(filter PromptAuditEventFilter, snapshot int64) string {
		t.Helper()
		value, err := filter.DeletionHash(snapshot)
		require.NoError(t, err)
		return value
	}
	reference := hash(base, 9)

	equivalent := base
	equivalent.Keyword = "  secret "
	equivalent.KeywordMode = PromptAuditKeywordModePlain
	equivalent.KeywordFields = "model_name, username,username"
	equivalent.PromptHash = "abc"
	assert.Equal(t, reference, hash(equivalent, 9), "spacing, default mode, field order, duplicate fields and hash case do not matter")

	for name, changed := range map[string]PromptAuditEventFilter{
		"decision":   {Decision: "critical", Keyword: "secret", KeywordFields: "username,model_name", PromptHash: "abc", StartTime: 10},
		"keyword":    {Decision: "flag", Keyword: "secrets", KeywordFields: "username,model_name", PromptHash: "abc", StartTime: 10},
		"fields":     {Decision: "flag", Keyword: "secret", KeywordFields: "username", PromptHash: "abc", StartTime: 10},
		"start_time": {Decision: "flag", Keyword: "secret", KeywordFields: "username,model_name", PromptHash: "abc", StartTime: 11},
	} {
		assert.NotEqual(t, reference, hash(changed, 9), name)
	}
	assert.NotEqual(t, reference, hash(base, 10), "the snapshot id is bound")

	noKeyword := PromptAuditEventFilter{Decision: "flag", KeywordFields: "username", KeywordMode: PromptAuditKeywordModeRegex}
	assert.Equal(t, hash(PromptAuditEventFilter{Decision: "flag"}, 9), hash(noKeyword, 9), "keyword settings without a keyword change nothing")

	_, err := PromptAuditEventFilter{KeywordFields: "password"}.DeletionHash(9)
	assert.ErrorIs(t, err, ErrPromptAuditInvalidKeywordField)
}
