package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var localizedContentEndpoints = []struct {
	name       string
	path       string
	defaultKey string
	i18nKey    string
	handler    gin.HandlerFunc
}{
	{"notice", "/api/notice", "Notice", "site_content_i18n.notice", GetNotice},
	{"about", "/api/about", "About", "site_content_i18n.about", GetAbout},
	{"home page", "/api/home_page_content", "HomePageContent", "site_content_i18n.home_page_content", GetHomePageContent},
	{"footer", "/api/footer", "Footer", "site_content_i18n.footer", GetFooter},
	{"user agreement", "/api/user-agreement", "legal.user_agreement", "site_content_i18n.user_agreement", GetUserAgreement},
	{"privacy policy", "/api/privacy-policy", "legal.privacy_policy", "site_content_i18n.privacy_policy", GetPrivacyPolicy},
}

func setupLocalizedContentTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	originalDB, originalLogDB := model.DB, model.LOG_DB
	originalFooter := common.Footer
	originalLegal := *system_setting.GetLegalSettings()

	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB, model.LOG_DB = db, db
	model.InitOptionMap()

	t.Cleanup(func() {
		for _, endpoint := range localizedContentEndpoints {
			require.NoError(t, model.UpdateOption(endpoint.i18nKey, "{}"))
		}
		common.Footer = originalFooter
		*system_setting.GetLegalSettings() = originalLegal
		model.DB, model.LOG_DB = originalDB, originalLogDB
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
}

func getLocalizedContent(t *testing.T, handler gin.HandlerFunc, target string, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if ifNoneMatch != "" {
		ctx.Request.Header.Set("If-None-Match", ifNoneMatch)
	}
	handler(ctx)
	// The engine flushes a body-less status (304) after the chain; do it here.
	ctx.Writer.WriteHeaderNow()
	return recorder
}

func localizedContentData(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Success bool   `json:"success"`
		Data    string `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	return payload.Data
}

func TestLocalizedSiteContentEndpoints(t *testing.T) {
	setupLocalizedContentTest(t)

	for _, endpoint := range localizedContentEndpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			require.NoError(t, model.UpdateOption(endpoint.defaultKey, "default "+endpoint.name))
			require.NoError(t, model.UpdateOption(endpoint.i18nKey, "{}"))

			// No overrides: every language gets the default content.
			assert.Equal(t, "default "+endpoint.name, localizedContentData(t, getLocalizedContent(t, endpoint.handler, endpoint.path+"?lang=en", "")))

			require.NoError(t, model.UpdateOption(endpoint.i18nKey, `{"en":"english","zh-TW":"traditional","fr":"   ","ja":""}`))
			cases := []struct {
				query string
				want  string
			}{
				{"", "default " + endpoint.name},
				{"?lang=", "default " + endpoint.name},
				{"?lang=en", "english"},
				{"?lang=en-US", "english"},
				{"?lang=de", "default " + endpoint.name},
				{"?lang=not%20a%20tag!", "default " + endpoint.name},
				{"?lang=fr", "default " + endpoint.name},
				{"?lang=ja", "default " + endpoint.name},
				{"?lang=zh-TW", "traditional"},
				{"?lang=zhTW", "traditional"},
				{"?lang=zh-HK", "traditional"},
				// zh-CN has no override and falls back to the other Chinese script.
				{"?lang=zh-CN", "traditional"},
				{"?lang=zhCN", "traditional"},
			}
			for _, tc := range cases {
				got := localizedContentData(t, getLocalizedContent(t, endpoint.handler, endpoint.path+tc.query, ""))
				assert.Equal(t, tc.want, got, "query %q", tc.query)
			}

			require.NoError(t, model.UpdateOption(endpoint.i18nKey, `{"zh-CN":"simplified","zh-TW":"traditional"}`))
			assert.Equal(t, "simplified", localizedContentData(t, getLocalizedContent(t, endpoint.handler, endpoint.path+"?lang=zh-SG", "")))
			assert.Equal(t, "traditional", localizedContentData(t, getLocalizedContent(t, endpoint.handler, endpoint.path+"?lang=zh-Hant", "")))
		})
	}
}

func TestLocalizedSiteContentETagIsBoundToLanguage(t *testing.T) {
	setupLocalizedContentTest(t)
	require.NoError(t, model.UpdateOption("About", "default about"))
	require.NoError(t, model.UpdateOption("site_content_i18n.about", `{"en":"english"}`))

	plain := getLocalizedContent(t, GetAbout, "/api/about", "")
	en := getLocalizedContent(t, GetAbout, "/api/about?lang=en", "")
	fr := getLocalizedContent(t, GetAbout, "/api/about?lang=fr", "")
	de := getLocalizedContent(t, GetAbout, "/api/about?lang=de", "")
	etags := map[string]string{
		"plain": plain.Header().Get("ETag"),
		"en":    en.Header().Get("ETag"),
		"fr":    fr.Header().Get("ETag"),
		"de":    de.Header().Get("ETag"),
	}
	for name, etag := range etags {
		require.NotEmpty(t, etag, name)
	}
	// fr and de both fall back to the same default body, yet keep their own validators.
	assert.Len(t, map[string]bool{etags["plain"]: true, etags["en"]: true, etags["fr"]: true, etags["de"]: true}, 4)
	assert.Equal(t, etags["en"], getLocalizedContent(t, GetAbout, "/api/about?lang=en", "").Header().Get("ETag"), "same language and content keeps its ETag")
	assert.Equal(t, etags["en"], getLocalizedContent(t, GetAbout, "/api/about?lang=EN", "").Header().Get("ETag"), "language tags are normalized")
	assert.Equal(t, "Accept-Encoding", en.Header().Get("Vary"))

	assert.Equal(t, http.StatusNotModified, getLocalizedContent(t, GetAbout, "/api/about?lang=en", etags["en"]).Code)
	assert.Equal(t, http.StatusNotModified, getLocalizedContent(t, GetAbout, "/api/about?lang=fr", etags["fr"]).Code)
	assert.Equal(t, http.StatusOK, getLocalizedContent(t, GetAbout, "/api/about?lang=fr", etags["en"]).Code)
	assert.Equal(t, http.StatusOK, getLocalizedContent(t, GetAbout, "/api/about?lang=fr", etags["de"]).Code)
	assert.Equal(t, http.StatusOK, getLocalizedContent(t, GetAbout, "/api/about", etags["fr"]).Code)
	assert.Equal(t, http.StatusOK, getLocalizedContent(t, GetAbout, "/api/about?lang=en", etags["plain"]).Code)

	require.NoError(t, model.UpdateOption("site_content_i18n.about", `{"en":"english v2"}`))
	assert.Equal(t, http.StatusOK, getLocalizedContent(t, GetAbout, "/api/about?lang=en", etags["en"]).Code, "edited content issues a new ETag")
}

func TestSiteContentI18nOptionValidationAndReload(t *testing.T) {
	setupLocalizedContentTest(t)
	require.NoError(t, model.UpdateOption("site_content_i18n.privacy_policy", `{"en":"<p>Policy</p>","zh-TW":"隱私"}`))

	for _, tc := range []struct {
		key   string
		value string
	}{
		{"site_content_i18n.privacy_policy", `not json`},
		{"site_content_i18n.privacy_policy", ``},
		{"site_content_i18n.privacy_policy", `null`},
		{"site_content_i18n.privacy_policy", `["en"]`},
		{"site_content_i18n.privacy_policy", `{"en":1}`},
		{"site_content_i18n.privacy_policy", `{"zh_tw":"x"}`},
		{"site_content_i18n.privacy_policy", `{"zh-tw":"x"}`},
		{"site_content_i18n.privacy_policy", `{"not a tag":"x"}`},
		{"site_content_i18n.unknown", `{}`},
	} {
		assert.Error(t, model.UpdateOption(tc.key, tc.value), "%s=%s", tc.key, tc.value)
	}

	var stored model.Option
	require.NoError(t, model.DB.Where(&model.Option{Key: "site_content_i18n.privacy_policy"}).First(&stored).Error)
	assert.JSONEq(t, `{"en":"<p>Policy</p>","zh-TW":"隱私"}`, stored.Value, "rejected updates leave the stored value untouched")

	// Reload from the database twice, as on restart and on SyncOptions.
	for range 2 {
		model.InitOptionMap()
		got := localizedContentData(t, getLocalizedContent(t, GetPrivacyPolicy, "/api/privacy-policy?lang=zh-TW", ""))
		assert.Equal(t, "隱私", got)
		assert.Equal(t, "<p>Policy</p>", localizedContentData(t, getLocalizedContent(t, GetPrivacyPolicy, "/api/privacy-policy?lang=en", "")))
	}

	common.OptionMapRWMutex.RLock()
	exported := common.OptionMap["site_content_i18n.privacy_policy"]
	common.OptionMapRWMutex.RUnlock()
	assert.JSONEq(t, `{"en":"<p>Policy</p>","zh-TW":"隱私"}`, exported, "admin options expose the stored map")

	// Removing a language drops it instead of merging into the previous map.
	require.NoError(t, model.UpdateOption("site_content_i18n.privacy_policy", `{"zh-TW":"隱私"}`))
	assert.Empty(t, localizedContentData(t, getLocalizedContent(t, GetPrivacyPolicy, "/api/privacy-policy?lang=en", "")))
}

func TestStatusReportsLocalizedOnlyLegalDocumentsAndFooter(t *testing.T) {
	setupLocalizedContentTest(t)
	require.NoError(t, model.UpdateOption("legal.user_agreement", ""))
	require.NoError(t, model.UpdateOption("legal.privacy_policy", ""))

	status := func() map[string]any {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/status", nil)
		GetStatus(ctx)
		var payload struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
		return payload.Data
	}

	data := status()
	assert.Equal(t, false, data["user_agreement_enabled"])
	assert.Equal(t, false, data["privacy_policy_enabled"])
	assert.Equal(t, false, data["footer_i18n_enabled"])

	require.NoError(t, model.UpdateOption("site_content_i18n.user_agreement", `{"en":"terms"}`))
	require.NoError(t, model.UpdateOption("site_content_i18n.privacy_policy", `{"en":"  "}`))
	require.NoError(t, model.UpdateOption("site_content_i18n.footer", `{"ja":"フッター"}`))
	data = status()
	assert.Equal(t, true, data["user_agreement_enabled"])
	assert.Equal(t, false, data["privacy_policy_enabled"], "a blank override does not enable the document")
	assert.Equal(t, true, data["footer_i18n_enabled"])
}
