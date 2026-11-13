package controller

import (
	"cmp"
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/promptaudit"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// promptAuditConfigResponse never carries Guard tokens, only whether an
// endpoint has one.
type promptAuditConfigResponse struct {
	Config   prompt_audit_setting.Config `json:"config"`
	HasToken map[string]bool             `json:"has_token"`
	Scanners []string                    `json:"scanners"`
}

type promptAuditTokenChange struct {
	Value string `json:"value"`
	Clear bool   `json:"clear"`
}

type promptAuditConfigRequest struct {
	Config                prompt_audit_setting.Config       `json:"config"`
	TokenChanges          map[string]promptAuditTokenChange `json:"token_changes"`
	ExpectedConfigVersion int64                             `json:"expected_config_version"`
}

func buildPromptAuditConfigResponse(config prompt_audit_setting.Config, tokens map[string]string) promptAuditConfigResponse {
	hasToken := make(map[string]bool, len(config.Endpoints))
	for _, endpoint := range config.Endpoints {
		hasToken[endpoint.ID] = tokens[endpoint.ID] != ""
	}
	return promptAuditConfigResponse{Config: config, HasToken: hasToken, Scanners: prompt_audit_setting.ScannerIDs}
}

func GetPromptAuditConfig(c *gin.Context) {
	config, tokens, err := model.PromptAuditStoredConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, buildPromptAuditConfigResponse(config, tokens))
}

// UpdatePromptAuditConfig saves the configuration. A stored token is kept only
// while its endpoint keeps the same address, so changing the address cannot
// send the old token to a new host.
func UpdatePromptAuditConfig(c *gin.Context) {
	var request promptAuditConfigRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	config := request.Config
	if err := config.Normalize(); err != nil {
		common.ApiError(c, err)
		return
	}
	stored, storedTokens, err := model.PromptAuditStoredConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	tokens := make(map[string]string, len(config.Endpoints))
	tokenChanges := make([]string, 0)
	for _, endpoint := range config.Endpoints {
		change := request.TokenChanges[endpoint.ID]
		value := strings.TrimSpace(change.Value)
		switch {
		case change.Clear:
			tokenChanges = append(tokenChanges, endpoint.ID+":cleared")
		case value != "":
			if len(value) > 4096 {
				common.ApiError(c, prompt_audit_setting.ErrInvalidEndpoint)
				return
			}
			tokens[endpoint.ID] = value
			tokenChanges = append(tokenChanges, endpoint.ID+":set")
		case storedTokens[endpoint.ID] != "" && promptAuditEndpointBaseURL(stored, endpoint.ID) == endpoint.BaseURL:
			tokens[endpoint.ID] = storedTokens[endpoint.ID]
		case storedTokens[endpoint.ID] != "":
			tokenChanges = append(tokenChanges, endpoint.ID+":dropped_on_address_change")
		}
	}
	config.UpdatedAt = time.Now().Unix()
	config.UpdatedBy = c.GetInt("id")
	saved, err := model.SavePromptAuditConfig(config, tokens, request.ExpectedConfigVersion)
	if errors.Is(err, model.ErrPromptAuditConfigConflict) {
		common.ApiErrorStatus(c, http.StatusConflict, err)
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "option.update", map[string]any{
		"key": prompt_audit_setting.OptionKey, "enabled": saved.Enabled, "all_groups": saved.AllGroups,
		"groups": strings.Join(saved.Groups, ","), "endpoints": len(saved.Endpoints),
		"store_full_prompt": saved.StoreFullPrompt, "retention_days": saved.RetentionDays,
		"scan_scope": saved.ScanScope, "strict_jailbreak_mapping": saved.StrictJailbreakMapping,
		"mode": saved.Mode, "fail_policy": saved.FailPolicy, "store_pass_events": saved.StorePassEvents,
		"config_version": saved.ConfigVersion, "token_changes": strings.Join(tokenChanges, ","),
	})
	common.ApiSuccess(c, buildPromptAuditConfigResponse(saved, tokens))
}

func promptAuditEndpointBaseURL(config prompt_audit_setting.Config, id string) string {
	for _, endpoint := range config.Endpoints {
		if endpoint.ID == id {
			return endpoint.BaseURL
		}
	}
	return ""
}

type promptAuditProbeRequest struct {
	Endpoint prompt_audit_setting.Endpoint `json:"endpoint"`
	Token    string                        `json:"token"`
}

// ProbePromptAuditEndpoint sends a test prompt to an endpoint, using the
// stored token only when the endpoint id and address both match.
func ProbePromptAuditEndpoint(c *gin.Context) {
	var request promptAuditProbeRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	probe := prompt_audit_setting.Config{Mode: prompt_audit_setting.ModeAsync, AllGroups: true, WorkerCount: 1, QueueCapacity: 1, RetentionDays: 1,
		Endpoints: []prompt_audit_setting.Endpoint{request.Endpoint}}
	probe.Endpoints[0].ID = strings.TrimSpace(probe.Endpoints[0].ID)
	if probe.Endpoints[0].ID == "" {
		probe.Endpoints[0].ID = "probe"
	}
	if err := probe.Normalize(); err != nil {
		common.ApiError(c, err)
		return
	}
	endpoint := probe.Endpoints[0]
	token := strings.TrimSpace(request.Token)
	if token == "" {
		stored, storedTokens, err := model.PromptAuditStoredConfig()
		if err != nil {
			common.ApiError(c, err)
			return
		}
		if promptAuditEndpointBaseURL(stored, endpoint.ID) == endpoint.BaseURL {
			token = storedTokens[endpoint.ID]
		}
	}
	common.ApiSuccess(c, promptaudit.Probe(c.Request.Context(), endpoint, token))
}

func GetPromptAuditRuntime(c *gin.Context) {
	common.ApiSuccess(c, promptaudit.Runtime())
}

// GetPromptAuditEvents lists events. A plain keyword on the default fields
// pages with offset and total; a regex or full prompt search scans rows in Go
// and pages with a cursor instead (see searchPromptAuditEvents).
func GetPromptAuditEvents(c *gin.Context) {
	var filter model.PromptAuditEventFilter
	if err := c.ShouldBindQuery(&filter); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if err := filter.Validate(); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	pageInfo := common.GetPageQuery(c)
	if scans, _ := filter.Scans(); scans {
		searchPromptAuditEvents(c, filter, pageInfo.GetPageSize())
		return
	}
	events, total, err := model.ListPromptAuditEvents(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(events)
	common.ApiSuccess(c, pageInfo)
}

// searchPromptAuditEvents runs a scanned search below the cursor query
// parameter. The scan limits come from PROMPT_AUDIT_SEARCH_MAX_SCAN_ROWS and
// PROMPT_AUDIT_SEARCH_TIME_BUDGET_SECONDS. A search of full prompts is
// recorded in the audit log with its pattern but never its matches.
func searchPromptAuditEvents(c *gin.Context, filter model.PromptAuditEventFilter, pageSize int) {
	cursor, err := strconv.ParseInt(c.DefaultQuery("cursor", "0"), 10, 64)
	if err != nil || cursor < 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if _, err := filter.KeywordPattern(); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	fields, _ := filter.SearchFields()
	if slices.Contains(fields, model.PromptAuditKeywordFieldFullPrompt) {
		recordManageAudit(c, "prompt_audit.events_search", map[string]any{
			"mode": cmp.Or(filter.KeywordMode, model.PromptAuditKeywordModePlain), "fields": strings.Join(fields, ","),
			"keyword": strings.TrimSpace(filter.Keyword), "case_sensitive": filter.CaseSensitive,
			"start_time": filter.StartTime, "end_time": filter.EndTime, "cursor": cursor,
		})
	}
	limits := model.PromptAuditScanLimits{
		MaxRows:  common.GetEnvOrDefault("PROMPT_AUDIT_SEARCH_MAX_SCAN_ROWS", model.PromptAuditScanDefaultMaxRows),
		Deadline: time.Now().Add(time.Duration(common.GetEnvOrDefault("PROMPT_AUDIT_SEARCH_TIME_BUDGET_SECONDS", int(model.PromptAuditScanDefaultBudget/time.Second))) * time.Second),
	}
	page, err := model.ScanPromptAuditEvents(c.Request.Context(), filter, cursor, pageSize, limits)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, page)
}

func promptAuditEventID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return 0, false
	}
	return id, true
}

// GetPromptAuditEvent returns one event including the full prompt; each view
// is recorded in the audit log.
func GetPromptAuditEvent(c *gin.Context) {
	id, ok := promptAuditEventID(c)
	if !ok {
		return
	}
	event, err := model.GetPromptAuditEvent(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("Prompt audit event not found"))
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "prompt_audit.event_view", map[string]any{"id": id, "has_full_prompt": event.FullPrompt != ""})
	common.ApiSuccess(c, event)
}

func DeletePromptAuditEvent(c *gin.Context) {
	id, ok := promptAuditEventID(c)
	if !ok {
		return
	}
	deletePromptAuditEvents(c, []int64{id})
}

func BatchDeletePromptAuditEvents(c *gin.Context) {
	var request struct {
		Ids []int64 `json:"ids"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || len(request.Ids) == 0 || len(request.Ids) > 500 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Select between 1 and 500 events"))
		return
	}
	deletePromptAuditEvents(c, request.Ids)
}

func deletePromptAuditEvents(c *gin.Context, ids []int64) {
	deleted, err := model.DeletePromptAuditEventsByIds(ids)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "prompt_audit.events_delete", map[string]any{"count": deleted, "scope": "ids"})
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}

// promptAuditDeleteConfirmTTL is how long a deletion preview can be confirmed.
const promptAuditDeleteConfirmTTL = 5 * time.Minute

var (
	errPromptAuditDeleteConfirmInvalid = common.NewMessage("Preview the deletion before confirming it")
	errPromptAuditDeleteConfirmExpired = common.NewMessage("The deletion preview expired. Preview the deletion again")
	errPromptAuditDeleteFilterChanged  = common.NewMessage("The filters changed after the preview. Preview the deletion again")
)

// promptAuditDeleteClaims bind a confirmation to the previewing root user,
// the filter hash and the snapshot id; they hold no prompt text.
type promptAuditDeleteClaims struct {
	FilterHash    string `json:"filter_hash"`
	SnapshotMaxId int64  `json:"snapshot_max_id"`
	AdminId       int    `json:"admin_id"`
	ExpiresAt     int64  `json:"expires_at"`
}

const promptAuditDeleteTokenPurpose = "prompt_audit.events_delete:"

func signPromptAuditDeleteClaims(claims promptAuditDeleteClaims) (string, error) {
	payload, err := common.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + common.GenerateHMAC(promptAuditDeleteTokenPurpose+encoded), nil
}

// verifyPromptAuditDeleteToken checks the signature and the issuing user;
// expiry and the filter are checked by the caller.
func verifyPromptAuditDeleteToken(token string, adminId int) (promptAuditDeleteClaims, bool) {
	var claims promptAuditDeleteClaims
	encoded, signature, ok := strings.Cut(strings.TrimSpace(token), ".")
	if !ok || !hmac.Equal([]byte(signature), []byte(common.GenerateHMAC(promptAuditDeleteTokenPurpose+encoded))) {
		return claims, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || common.Unmarshal(payload, &claims) != nil {
		return claims, false
	}
	return claims, claims.AdminId == adminId
}

// PreviewPromptAuditEventDeletion counts the events a filter would delete and
// issues a short-lived confirmation token bound to that filter and to the
// highest matching id, so events recorded later are never deleted.
func PreviewPromptAuditEventDeletion(c *gin.Context) {
	var filter model.PromptAuditEventFilter
	if err := common.DecodeJson(c.Request.Body, &filter); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	total, maxId, err := model.PreviewPromptAuditEventDeletion(filter)
	if promptAuditFilterRejected(err) {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	filterHash, err := filter.DeletionHash(maxId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	expiresAt := time.Now().Add(promptAuditDeleteConfirmTTL).Unix()
	token, err := signPromptAuditDeleteClaims(promptAuditDeleteClaims{FilterHash: filterHash, SnapshotMaxId: maxId, AdminId: c.GetInt("id"), ExpiresAt: expiresAt})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"matched_count": total, "snapshot_max_id": maxId, "filter_hash": filterHash,
		"confirmation_token": token, "expires_at": expiresAt})
}

// DeletePromptAuditEventsByFilter deletes, in batches, the events a preview
// counted. The confirmation must be unexpired, issued to the same user, and
// match the filter and snapshot sent now; otherwise the user previews again.
func DeletePromptAuditEventsByFilter(c *gin.Context) {
	var request struct {
		Filter            model.PromptAuditEventFilter `json:"filter"`
		SnapshotMaxId     int64                        `json:"snapshot_max_id"`
		FilterHash        string                       `json:"filter_hash"`
		ConfirmationToken string                       `json:"confirmation_token"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, errPromptAuditDeleteConfirmInvalid)
		return
	}
	claims, ok := verifyPromptAuditDeleteToken(request.ConfirmationToken, c.GetInt("id"))
	if !ok {
		common.ApiErrorStatus(c, http.StatusBadRequest, errPromptAuditDeleteConfirmInvalid)
		return
	}
	if time.Now().Unix() >= claims.ExpiresAt {
		common.ApiErrorStatus(c, http.StatusConflict, errPromptAuditDeleteConfirmExpired)
		return
	}
	filterHash, err := request.Filter.DeletionHash(request.SnapshotMaxId)
	if promptAuditFilterRejected(err) {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	if err != nil || filterHash != claims.FilterHash || request.FilterHash != claims.FilterHash || request.SnapshotMaxId != claims.SnapshotMaxId {
		common.ApiErrorStatus(c, http.StatusConflict, errPromptAuditDeleteFilterChanged)
		return
	}
	deleted, err := model.DeletePromptAuditEventsByFilter(request.Filter, claims.SnapshotMaxId)
	if promptAuditFilterRejected(err) {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	filter := request.Filter
	recordManageAudit(c, "prompt_audit.events_delete", map[string]any{
		"count": deleted, "scope": "filter", "snapshot_max_id": claims.SnapshotMaxId, "filter_hash": claims.FilterHash,
		"decision": filter.Decision, "risk_level": filter.RiskLevel, "user_id": filter.UserId, "token_id": filter.TokenId,
		"group": filter.Group, "model_name": filter.ModelName, "request_id": filter.RequestId, "prompt_hash": filter.PromptHash,
		"keyword": strings.TrimSpace(filter.Keyword), "keyword_fields": filter.KeywordFields,
		"start_time": filter.StartTime, "end_time": filter.EndTime,
	})
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}

// promptAuditFilterRejected reports a filter refused before any database
// work: an unknown keyword mode or field, or a scanned search sent to
// filtered deletion.
func promptAuditFilterRejected(err error) bool {
	return errors.Is(err, model.ErrPromptAuditInvalidKeywordMode) || errors.Is(err, model.ErrPromptAuditInvalidKeywordField) ||
		errors.Is(err, model.ErrPromptAuditScanFilterDelete)
}
