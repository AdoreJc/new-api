package controller

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/auditpolicy"
	"github.com/QuantumNous/new-api/setting/audit_policy_setting"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"

	"github.com/gin-gonic/gin"
)

type auditPolicyResponse struct {
	Config audit_policy_setting.Config `json:"config"`
	// Saved is false while the rules are derived from the engine settings.
	Saved bool `json:"saved"`
	// Engine modes are the master switches the rules work under.
	PromptAuditEnabled    bool     `json:"prompt_audit_enabled"`
	PromptAuditMode       string   `json:"prompt_audit_mode"`
	ContentModerationMode string   `json:"content_moderation_mode"`
	PromptAuditCategories []string `json:"prompt_audit_categories"`
	ModerationCategories  []string `json:"content_moderation_categories"`
	RelayFormats          []string `json:"relay_formats"`
	PromptAuditScanScopes []string `json:"prompt_audit_scan_scopes"`
	ContentKeywordModes   []string `json:"content_moderation_keyword_modes"`
}

func buildAuditPolicyResponse(config audit_policy_setting.Config, saved bool) auditPolicyResponse {
	pa := prompt_audit_setting.Current().Config
	return auditPolicyResponse{
		Config: config, Saved: saved,
		PromptAuditEnabled: pa.Enabled, PromptAuditMode: pa.Mode,
		ContentModerationMode: content_moderation_setting.Current().Config.Mode,
		PromptAuditCategories: prompt_audit_setting.ScannerIDs,
		ModerationCategories:  content_moderation_setting.Categories,
		RelayFormats:          []string{"openai", "claude", "gemini", "openai_responses", "openai_responses_compaction", "openai_image", auditpolicy.FormatResponsesWebSocket},
		PromptAuditScanScopes: prompt_audit_setting.ScanScopes,
		ContentKeywordModes: []string{content_moderation_setting.KeywordModeAPIOnly, content_moderation_setting.KeywordModeKeywordOnly,
			content_moderation_setting.KeywordModeKeywordAndAPI, content_moderation_setting.KeywordModeKeywordGated},
	}
}

func GetAuditPolicy(c *gin.Context) {
	config, saved, err := model.AuditPolicyStoredConfig()
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, buildAuditPolicyResponse(config, saved))
}

type auditPolicyRequest struct {
	Config                audit_policy_setting.Config `json:"config"`
	ExpectedConfigVersion int64                       `json:"expected_config_version"`
}

// UpdateAuditPolicy saves the rule table. Rules are saved whole and in
// order; every save is recorded in the audit log.
func UpdateAuditPolicy(c *gin.Context) {
	var request auditPolicyRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	config := request.Config
	config.UpdatedAt = time.Now().Unix()
	config.UpdatedBy = c.GetInt("id")
	saved, err := model.SaveAuditPolicy(&config, request.ExpectedConfigVersion)
	if !respondAuditPolicySaveError(c, err) {
		return
	}
	ids := make([]string, 0, len(saved.Rules))
	for _, rule := range saved.Rules {
		state := "on"
		if !rule.Enabled {
			state = "off"
		}
		ids = append(ids, rule.ID+":"+state)
	}
	verified := 0
	for _, entry := range saved.Allowlist {
		if entry.Verified {
			verified++
		}
	}
	recordManageAudit(c, "audit_policy.update", map[string]any{
		"config_version": saved.ConfigVersion, "rules": strings.Join(ids, ","),
		"allowlist": len(saved.Allowlist), "verified": verified, "blocklist": len(saved.Blocklist),
	})
	common.ApiSuccess(c, buildAuditPolicyResponse(saved, true))
}

// ResetAuditPolicy removes the saved table, so the rules are derived from
// the engine settings again.
func ResetAuditPolicy(c *gin.Context) {
	var request struct {
		ExpectedConfigVersion int64 `json:"expected_config_version"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	if _, err := model.SaveAuditPolicy(nil, request.ExpectedConfigVersion); !respondAuditPolicySaveError(c, err) {
		return
	}
	recordManageAudit(c, "audit_policy.reset", map[string]any{"previous_config_version": request.ExpectedConfigVersion})
	common.ApiSuccess(c, buildAuditPolicyResponse(audit_policy_setting.Current().Config, false))
}

func respondAuditPolicySaveError(c *gin.Context, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, model.ErrAuditPolicyConflict):
		common.ApiErrorStatus(c, http.StatusConflict, err)
	default:
		common.ApiError(c, err)
	}
	return false
}

type auditPolicyTestRequest struct {
	UserID  int    `json:"user_id"`
	TokenID int    `json:"token_id"`
	Group   string `json:"group"`
	Model   string `json:"model"`
	Format  string `json:"format"`
}

// TestAuditPolicy shows which rule a request would match and what each
// engine would do, without checking anything.
func TestAuditPolicy(c *gin.Context) {
	var request auditPolicyTestRequest
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return
	}
	subject := audit_policy_setting.Subject{
		UserID: request.UserID, TokenID: request.TokenID, Group: strings.TrimSpace(request.Group),
		Model: strings.TrimSpace(request.Model), Format: strings.TrimSpace(request.Format),
	}
	common.ApiSuccess(c, auditpolicy.Explain(subject))
}
