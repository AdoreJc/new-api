package controller

import (
	"errors"
	"net/http"
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

func GetPromptAuditEvents(c *gin.Context) {
	var filter model.PromptAuditEventFilter
	if err := c.ShouldBindQuery(&filter); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	pageInfo := common.GetPageQuery(c)
	events, total, err := model.ListPromptAuditEvents(filter, pageInfo.GetStartIdx(), pageInfo.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(events)
	common.ApiSuccess(c, pageInfo)
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

func PreviewPromptAuditEventDeletion(c *gin.Context) {
	var filter model.PromptAuditEventFilter
	if err := common.DecodeJson(c.Request.Body, &filter); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	total, maxId, err := model.PreviewPromptAuditEventDeletion(filter)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"total": total, "max_id": maxId})
}

// DeletePromptAuditEventsByFilter deletes the events counted by a preview;
// max_id from that preview keeps newer events out of the deletion.
func DeletePromptAuditEventsByFilter(c *gin.Context) {
	var request struct {
		Filter model.PromptAuditEventFilter `json:"filter"`
		MaxId  int64                        `json:"max_id"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.MaxId <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Preview the deletion before confirming it"))
		return
	}
	deleted, err := model.DeletePromptAuditEventsByFilter(request.Filter, request.MaxId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "prompt_audit.events_delete", map[string]any{"count": deleted, "scope": "filter"})
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}
