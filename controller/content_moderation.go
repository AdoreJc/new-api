package controller

import (
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/contentmoderation"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var errContentModerationKeysRequired = common.NewMessage("Add at least one moderation API key before turning on content moderation")

type contentModerationKeyView struct {
	ID string `json:"id"`
}

// contentModerationConfigResponse never carries API keys, only their ids.
type contentModerationConfigResponse struct {
	Config            content_moderation_setting.Config `json:"config"`
	Keys              []contentModerationKeyView        `json:"keys"`
	Categories        []string                          `json:"categories"`
	DefaultThresholds map[string]float64                `json:"default_thresholds"`
}

type contentModerationKeyChanges struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
	Clear  bool     `json:"clear"`
}

type contentModerationConfigRequest struct {
	Config                content_moderation_setting.Config `json:"config"`
	KeyChanges            contentModerationKeyChanges       `json:"key_changes"`
	ExpectedConfigVersion int64                             `json:"expected_config_version"`
}

func buildContentModerationConfigResponse(config content_moderation_setting.Config, keys []string) contentModerationConfigResponse {
	views := make([]contentModerationKeyView, 0, len(keys))
	for _, key := range keys {
		views = append(views, contentModerationKeyView{ID: content_moderation_setting.KeyID(key)})
	}
	return contentModerationConfigResponse{
		Config: config, Keys: views, Categories: content_moderation_setting.Categories,
		DefaultThresholds: content_moderation_setting.DefaultThresholds(),
	}
}

func GetContentModerationConfig(c *gin.Context) {
	config, keys, err := model.ContentModerationStoredConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, buildContentModerationConfigResponse(config, keys))
}

// contentModerationAllowlistDiff lists user ids added to and removed from
// the allowlist, for the audit log.
func contentModerationAllowlistDiff(before, after []int) (added, removed []int) {
	for _, id := range after {
		if !slices.Contains(before, id) {
			added = append(added, id)
		}
	}
	for _, id := range before {
		if !slices.Contains(after, id) {
			removed = append(removed, id)
		}
	}
	return added, removed
}

func joinIDs(ids []int) string {
	values := make([]string, 0, len(ids))
	for _, id := range ids {
		values = append(values, strconv.Itoa(id))
	}
	return strings.Join(values, ",")
}

// UpdateContentModerationConfig saves the configuration. Stored keys are kept
// only while the API address stays the same, so changing the address cannot
// send the old keys to a new host.
func UpdateContentModerationConfig(c *gin.Context) {
	// Fields left out of the request keep their defaults rather than zero
	// values, so a partial body cannot silently set the sample rate to 0.
	request := contentModerationConfigRequest{Config: content_moderation_setting.DefaultConfig()}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	config := request.Config
	if err := config.Normalize(); err != nil {
		common.ApiError(c, err)
		return
	}
	stored, storedKeys, err := model.ContentModerationStoredConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	keyChanges := make([]string, 0)
	keys := make([]string, 0, len(storedKeys)+len(request.KeyChanges.Add))
	switch {
	case request.KeyChanges.Clear:
		keyChanges = append(keyChanges, "cleared")
	case stored.BaseURL != config.BaseURL && len(storedKeys) > 0:
		keyChanges = append(keyChanges, "dropped_on_address_change")
	default:
		for _, key := range storedKeys {
			id := content_moderation_setting.KeyID(key)
			if slices.Contains(request.KeyChanges.Remove, id) {
				keyChanges = append(keyChanges, id+":removed")
				continue
			}
			keys = append(keys, key)
		}
	}
	for _, key := range content_moderation_setting.NormalizeKeys(request.KeyChanges.Add) {
		if len(key) > content_moderation_setting.MaxAPIKeyLength || strings.ContainsAny(key, " \t\r\n") {
			common.ApiError(c, content_moderation_setting.ErrInvalidAPIKey)
			return
		}
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
			keyChanges = append(keyChanges, content_moderation_setting.KeyID(key)+":added")
		}
	}
	if len(keys) > content_moderation_setting.MaxAPIKeys {
		common.ApiError(c, content_moderation_setting.ErrInvalidAPIKey)
		return
	}
	// keyword_only never calls the engine, so it runs without a key.
	if config.Mode != content_moderation_setting.ModeOff && len(keys) == 0 &&
		config.KeywordMode != content_moderation_setting.KeywordModeKeywordOnly {
		common.ApiError(c, errContentModerationKeysRequired)
		return
	}
	config.UpdatedAt = time.Now().Unix()
	config.UpdatedBy = c.GetInt("id")
	saved, err := model.SaveContentModerationConfig(config, keys, request.ExpectedConfigVersion)
	if errors.Is(err, model.ErrContentModerationConfigConflict) {
		common.ApiErrorStatus(c, http.StatusConflict, err)
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	added, removed := contentModerationAllowlistDiff(stored.AllowlistUserIDs, saved.AllowlistUserIDs)
	recordManageAudit(c, "option.update", map[string]any{
		"key": content_moderation_setting.OptionKey, "mode": saved.Mode, "base_url": saved.BaseURL, "model": saved.Model,
		"all_groups": saved.AllGroups, "groups": strings.Join(saved.Groups, ","), "sample_rate": saved.SampleRate,
		"hash_blocklist": saved.HashBlocklistEnabled, "email_on_hit": saved.EmailOnHit,
		"auto_disable": saved.AutoDisableEnabled, "disable_threshold": saved.DisableThreshold,
		"violation_window_hours": saved.ViolationWindowHours, "record_non_hits": saved.RecordNonHits,
		"allowlist_added": joinIDs(added), "allowlist_removed": joinIDs(removed),
		"config_version": saved.ConfigVersion, "key_changes": strings.Join(keyChanges, ","),
		"keyword_mode": saved.KeywordMode, "keyword_lists": strings.Join(saved.KeywordLists, ","),
	})
	if saved.KeywordMode != content_moderation_setting.KeywordModeAPIOnly {
		contentmoderation.ReloadKeywordLists()
	}
	common.ApiSuccess(c, buildContentModerationConfigResponse(saved, keys))
}

type contentModerationKeywordListView struct {
	Name      string `json:"name"`
	Count     int    `json:"count"`
	UpdatedAt int64  `json:"updated_at"`
	// Builtin marks the "default" list: the global sensitive words, edited
	// on the sensitive words settings page.
	Builtin bool `json:"builtin"`
}

// GetContentModerationKeywordLists lists the built-in list and the stored
// lists with their sizes, without the keywords.
func GetContentModerationKeywordLists(c *gin.Context) {
	lists, err := model.ListContentModerationKeywordLists()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	views := []contentModerationKeywordListView{{
		Name: content_moderation_setting.DefaultKeywordList, Builtin: true,
	}}
	for _, list := range lists {
		words, _ := content_moderation_setting.NormalizeKeywords(string(list.Words))
		views = append(views, contentModerationKeywordListView{Name: list.Name, Count: len(words), UpdatedAt: list.UpdatedAt})
	}
	common.ApiSuccess(c, views)
}

func contentModerationKeywordListName(c *gin.Context) (string, bool) {
	name := c.Param("name")
	if !content_moderation_setting.ValidKeywordListName(name) {
		common.ApiError(c, content_moderation_setting.ErrInvalidKeywordList)
		return "", false
	}
	if name == content_moderation_setting.DefaultKeywordList {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("The default list is the sensitive words list; edit it in the sensitive words settings"))
		return "", false
	}
	return name, true
}

// GetContentModerationKeywordList returns one stored list for editing.
func GetContentModerationKeywordList(c *gin.Context) {
	name, ok := contentModerationKeywordListName(c)
	if !ok {
		return
	}
	lists, err := model.ListContentModerationKeywordLists()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	for _, list := range lists {
		if list.Name == name {
			common.ApiSuccess(c, gin.H{"name": list.Name, "words": string(list.Words), "updated_at": list.UpdatedAt})
			return
		}
	}
	common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("Keyword list not found"))
}

// SaveContentModerationKeywordList creates or replaces a list. The audit log
// records its name and size, never the keywords.
func SaveContentModerationKeywordList(c *gin.Context) {
	name, ok := contentModerationKeywordListName(c)
	if !ok {
		return
	}
	var request struct {
		Words string `json:"words"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	words, err := content_moderation_setting.NormalizeKeywords(request.Words)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	lists, err := model.ListContentModerationKeywordLists()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	exists := slices.ContainsFunc(lists, func(list *model.ContentModerationKeywordList) bool { return list.Name == name })
	if !exists && len(lists) >= content_moderation_setting.MaxKeywordLists {
		common.ApiError(c, content_moderation_setting.ErrInvalidKeywordList)
		return
	}
	if err := model.SaveContentModerationKeywordList(name, words, time.Now().Unix()); err != nil {
		common.ApiError(c, err)
		return
	}
	contentmoderation.ReloadKeywordLists()
	recordManageAudit(c, "content_moderation.keyword_list_save", map[string]any{"name": name, "count": len(words)})
	common.ApiSuccess(c, contentModerationKeywordListView{Name: name, Count: len(words), UpdatedAt: time.Now().Unix()})
}

func DeleteContentModerationKeywordList(c *gin.Context) {
	name, ok := contentModerationKeywordListName(c)
	if !ok {
		return
	}
	deleted, err := model.DeleteContentModerationKeywordList(name)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	contentmoderation.ReloadKeywordLists()
	recordManageAudit(c, "content_moderation.keyword_list_delete", map[string]any{"name": name, "count": deleted})
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}

// UpdateContentModerationAllowlist adds or removes one user without
// touching the rest of the configuration.
func UpdateContentModerationAllowlist(c *gin.Context) {
	var request struct {
		UserId int    `json:"user_id"`
		Action string `json:"action"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.UserId <= 0 ||
		(request.Action != "add" && request.Action != "remove") {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	config, keys, err := model.ContentModerationStoredConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	before := slices.Clone(config.AllowlistUserIDs)
	if request.Action == "add" {
		config.AllowlistUserIDs = append(config.AllowlistUserIDs, request.UserId)
	} else {
		config.AllowlistUserIDs = slices.DeleteFunc(config.AllowlistUserIDs, func(id int) bool { return id == request.UserId })
	}
	config.UpdatedAt = time.Now().Unix()
	config.UpdatedBy = c.GetInt("id")
	saved, err := model.SaveContentModerationConfig(config, keys, config.ConfigVersion)
	if errors.Is(err, model.ErrContentModerationConfigConflict) {
		common.ApiErrorStatus(c, http.StatusConflict, err)
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !slices.Equal(before, saved.AllowlistUserIDs) {
		recordManageAuditFor(c, request.UserId, "content_moderation.allowlist_"+request.Action, map[string]any{
			"target_user_id": request.UserId, "config_version": saved.ConfigVersion,
		})
	}
	common.ApiSuccess(c, buildContentModerationConfigResponse(saved, keys))
}

type contentModerationProbeRequest struct {
	BaseURL   string   `json:"base_url"`
	Model     string   `json:"model"`
	TimeoutMS int      `json:"timeout_ms"`
	Keys      []string `json:"keys"`
	Text      string   `json:"text"`
}

// ProbeContentModerationEngine sends a test input to the engine with each
// given key, or with the stored keys when no key is given and the address is
// the stored one.
func ProbeContentModerationEngine(c *gin.Context) {
	var request contentModerationProbeRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	stored, storedKeys, err := model.ContentModerationStoredConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	probe := stored.Clone()
	probe.Mode = content_moderation_setting.ModeOff
	probe.BaseURL, probe.Model, probe.TimeoutMS = request.BaseURL, request.Model, request.TimeoutMS
	if err := probe.Normalize(); err != nil {
		common.ApiError(c, err)
		return
	}
	keys := content_moderation_setting.NormalizeKeys(request.Keys)
	if len(keys) == 0 && probe.BaseURL == stored.BaseURL {
		keys = storedKeys
	}
	if len(keys) == 0 || len(keys) > content_moderation_setting.MaxAPIKeys {
		common.ApiError(c, content_moderation_setting.ErrInvalidAPIKey)
		return
	}
	text := strings.TrimSpace(request.Text)
	if text == "" {
		text = "Hello"
	}
	if utf8.RuneCountInString(text) > 2000 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Test text must be at most 2000 characters"))
		return
	}
	common.ApiSuccess(c, contentmoderation.Probe(c.Request.Context(), probe, keys, text))
}

func GetContentModerationRuntime(c *gin.Context) {
	common.ApiSuccess(c, contentmoderation.Runtime())
}

func GetContentModerationEvents(c *gin.Context) {
	var filter model.ContentModerationEventFilter
	if err := c.ShouldBindQuery(&filter); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	pageInfo := common.GetPageQuery(c)
	events, total, err := model.ListContentModerationEvents(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(events)
	common.ApiSuccess(c, pageInfo)
}

func contentModerationEventID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return 0, false
	}
	return id, true
}

func GetContentModerationEvent(c *gin.Context) {
	id, ok := contentModerationEventID(c)
	if !ok {
		return
	}
	event, err := model.GetContentModerationEvent(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("Content moderation event not found"))
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, event)
}

func DeleteContentModerationEvent(c *gin.Context) {
	id, ok := contentModerationEventID(c)
	if !ok {
		return
	}
	deleteContentModerationEvents(c, []int64{id})
}

func BatchDeleteContentModerationEvents(c *gin.Context) {
	var request struct {
		Ids []int64 `json:"ids"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || len(request.Ids) == 0 || len(request.Ids) > 500 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Select between 1 and 500 events"))
		return
	}
	deleteContentModerationEvents(c, request.Ids)
}

func deleteContentModerationEvents(c *gin.Context, ids []int64) {
	deleted, err := model.DeleteContentModerationEventsByIds(ids)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "content_moderation.events_delete", map[string]any{"count": deleted, "scope": "ids"})
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}

func PreviewContentModerationEventDeletion(c *gin.Context) {
	var filter model.ContentModerationEventFilter
	if err := common.DecodeJson(c.Request.Body, &filter); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	total, maxId, err := model.PreviewContentModerationEventDeletion(filter)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"total": total, "max_id": maxId})
}

// DeleteContentModerationEventsByFilter deletes the events counted by a
// preview; max_id from that preview keeps newer events out of the deletion.
func DeleteContentModerationEventsByFilter(c *gin.Context) {
	var request struct {
		Filter model.ContentModerationEventFilter `json:"filter"`
		MaxId  int64                              `json:"max_id"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.MaxId <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Preview the deletion before confirming it"))
		return
	}
	deleted, err := model.DeleteContentModerationEventsByFilter(request.Filter, request.MaxId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "content_moderation.events_delete", map[string]any{"count": deleted, "scope": "filter"})
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}

var contentModerationHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DeleteContentModerationHash removes one input hash from the blocklist, so
// that input is checked by the engine again.
func DeleteContentModerationHash(c *gin.Context) {
	hash := strings.ToLower(strings.TrimSpace(c.Param("hash")))
	if !contentModerationHashPattern.MatchString(hash) {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	deleted, err := model.DeleteContentModerationFlaggedHash(hash)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	contentmoderation.ForgetHash(hash)
	recordManageAudit(c, "content_moderation.hash_delete", map[string]any{"hash": hash, "count": deleted})
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}

// ClearContentModerationHashes empties the hash blocklist.
func ClearContentModerationHashes(c *gin.Context) {
	deleted, err := model.ClearContentModerationFlaggedHashes()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	contentmoderation.ForgetAllHashes()
	recordManageAudit(c, "content_moderation.hashes_clear", map[string]any{"count": deleted})
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}
