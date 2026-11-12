package controller

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/contentaudit"
	"github.com/QuantumNous/new-api/setting/content_audit_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const contentAuditMaxBatchDelete = 500

type contentAuditEndpointView struct {
	content_audit_setting.Endpoint
	HasToken bool `json:"has_token"`
}

type contentAuditConfigView struct {
	content_audit_setting.Config
	Endpoints []contentAuditEndpointView `json:"endpoints"`
}

type contentAuditEndpointInput struct {
	content_audit_setting.Endpoint
	Token      string `json:"token"`
	ClearToken bool   `json:"clear_token"`
}

type contentAuditConfigInput struct {
	content_audit_setting.Config
	Endpoints []contentAuditEndpointInput `json:"endpoints"`
}

// contentAuditConfigResponse never includes Guard tokens, only has_token.
func contentAuditConfigResponse(snapshot *content_audit_setting.Snapshot) gin.H {
	view := contentAuditConfigView{Config: snapshot.Config, Endpoints: make([]contentAuditEndpointView, 0, len(snapshot.Config.Endpoints))}
	for _, endpoint := range snapshot.Config.Endpoints {
		view.Endpoints = append(view.Endpoints, contentAuditEndpointView{Endpoint: endpoint, HasToken: snapshot.Tokens[endpoint.ID] != ""})
	}
	return gin.H{"config": view, "scanners": content_audit_setting.ScannerIDs}
}

func GetContentAuditConfig(c *gin.Context) {
	common.ApiSuccess(c, contentAuditConfigResponse(content_audit_setting.Current()))
}

func UpdateContentAuditConfig(c *gin.Context) {
	var request struct {
		Config                contentAuditConfigInput `json:"config"`
		ExpectedConfigVersion int64                   `json:"expected_config_version"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	current := content_audit_setting.Current()
	cfg := request.Config.Config
	cfg.Endpoints = make([]content_audit_setting.Endpoint, 0, len(request.Config.Endpoints))
	tokens := map[string]string{}
	for _, endpoint := range request.Config.Endpoints {
		cfg.Endpoints = append(cfg.Endpoints, endpoint.Endpoint)
		id := strings.TrimSpace(endpoint.ID)
		token := strings.TrimSpace(endpoint.Token)
		switch {
		case token != "":
			tokens[id] = token
		case endpoint.ClearToken:
		default:
			// Keep a stored token only while the endpoint still points to the
			// same address, so a token is never sent to a different host.
			previous, ok := contentAuditStoredEndpoint(current, id)
			requestedBaseURL, err := content_audit_setting.NormalizeBaseURL(endpoint.BaseURL)
			if ok && err == nil && previous.BaseURL == requestedBaseURL && current.Tokens[id] != "" {
				tokens[id] = current.Tokens[id]
			}
		}
	}
	normalized, err := content_audit_setting.Normalize(cfg)
	if err != nil {
		common.ApiErrorT(c, "Invalid content audit configuration: {{error}}", map[string]any{"error": err.Error()})
		return
	}
	normalized.UpdatedAt = time.Now().Unix()
	normalized.UpdatedBy = c.GetInt("id")
	saved, err := model.SaveContentAuditConfig(normalized, tokens, request.ExpectedConfigVersion)
	recordContentAuditAudit(c, "content_audit.config_update", map[string]any{
		"success":        err == nil,
		"enabled":        normalized.Enabled,
		"config_version": saved.ConfigVersion,
		"endpoint_count": len(normalized.Endpoints),
	}, err == nil)
	if errors.Is(err, model.ErrContentAuditConfigConflict) {
		common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage("Content audit configuration changed elsewhere. Reload and try again."))
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, contentAuditConfigResponse(content_audit_setting.Current()))
}

func ProbeContentAuditEndpoint(c *gin.Context) {
	var request struct {
		Endpoint struct {
			ID      string `json:"id"`
			BaseURL string `json:"base_url"`
			Model   string `json:"model"`
			Token   string `json:"token"`
		} `json:"endpoint"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	baseURL, err := content_audit_setting.NormalizeBaseURL(request.Endpoint.BaseURL)
	if err != nil || strings.TrimSpace(request.Endpoint.Model) == "" {
		common.ApiErrorT(c, "Invalid content audit configuration: {{error}}", map[string]any{"error": "guard endpoint needs a valid base URL and model"})
		return
	}
	token := strings.TrimSpace(request.Endpoint.Token)
	current := content_audit_setting.Current()
	if previous, ok := contentAuditStoredEndpoint(current, strings.TrimSpace(request.Endpoint.ID)); token == "" && ok && previous.BaseURL == baseURL {
		token = current.Tokens[previous.ID]
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Duration(current.Config.TimeoutMS)*time.Millisecond+time.Second)
	defer cancel()
	endpoint := content_audit_setting.Endpoint{ID: request.Endpoint.ID, BaseURL: baseURL, Model: strings.TrimSpace(request.Endpoint.Model), Enabled: true}
	common.ApiSuccess(c, contentaudit.Probe(ctx, endpoint, token))
}

func GetContentAuditRuntime(c *gin.Context) {
	common.ApiSuccess(c, contentaudit.GetRuntime())
}

func GetContentAuditEvents(c *gin.Context) {
	page := common.GetPageQuery(c)
	if page.GetPageSize() > 100 {
		page.PageSize = 100
	}
	filter := model.ContentAuditEventFilter{
		Decision:   c.Query("decision"),
		Group:      strings.TrimSpace(c.Query("group")),
		RequestId:  strings.TrimSpace(c.Query("request_id")),
		PromptHash: strings.ToLower(strings.TrimSpace(c.Query("prompt_hash"))),
	}
	filter.UserId, _ = strconv.Atoi(c.Query("user_id"))
	filter.StartTimestamp, _ = strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	filter.EndTimestamp, _ = strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	events, total, err := model.GetContentAuditEvents(filter, page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	page.SetTotal(int(total))
	page.SetItems(events)
	common.ApiSuccess(c, page)
}

func GetContentAuditEvent(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	event, err := model.GetContentAuditEventById(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("Content audit event not found"))
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, event)
}

func DeleteContentAuditEvent(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	deleted, err := model.DeleteContentAuditEvents([]int64{id})
	recordContentAuditAudit(c, "content_audit.event_delete", map[string]any{"id": id}, err == nil && deleted > 0)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if deleted == 0 {
		common.ApiErrorStatus(c, http.StatusNotFound, common.NewMessage("Content audit event not found"))
		return
	}
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}

func BatchDeleteContentAuditEvents(c *gin.Context) {
	var request struct {
		Ids []int64 `json:"ids"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if len(request.Ids) == 0 || len(request.Ids) > contentAuditMaxBatchDelete {
		common.ApiErrorT(c, "Select 1 to 500 events")
		return
	}
	deleted, err := model.DeleteContentAuditEvents(request.Ids)
	recordContentAuditAudit(c, "content_audit.event_batch_delete", map[string]any{"count": deleted}, err == nil)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"deleted": deleted})
}

func contentAuditStoredEndpoint(snapshot *content_audit_setting.Snapshot, id string) (content_audit_setting.Endpoint, bool) {
	for _, endpoint := range snapshot.Config.Endpoints {
		if endpoint.ID == id {
			return endpoint, true
		}
	}
	return content_audit_setting.Endpoint{}, false
}

// recordContentAuditAudit writes an operation audit entry. Params never carry
// tokens or request text.
func recordContentAuditAudit(c *gin.Context, action string, params map[string]any, success bool) {
	auditInfo := &model.AuditRequestInfo{
		Method: c.Request.Method, Route: c.FullPath(), Path: c.FullPath(),
		Status: c.Writer.Status(), Success: success,
	}
	model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), auditContentEN(action, params), c.ClientIP(), action, params, auditOperatorInfo(c), auditInfo, c)
	markAuditLogged(c)
}
