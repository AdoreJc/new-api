package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
)

// serveLocalizedSiteContent serves a public site-content document in the
// language named by ?lang= (a BCP-47 tag or the interface codes zhCN/zhTW).
// A request without a valid lang gets the default content with the same
// ETag as before localization, so older clients are unaffected; Accept-Language
// is deliberately not consulted, which keeps the response a function of the
// URL alone and needs no Vary: Accept-Language. A request with lang gets the
// best non-blank override (see system_setting.LocalizedSiteContent) or the
// default content, under an ETag bound to the requested language.
func serveLocalizedSiteContent(c *gin.Context, field system_setting.SiteContentField, defaultContent string) {
	lang := system_setting.NormalizeContentLanguage(c.Query("lang"))
	if lang == "" {
		serveRevalidatedJSON(c, defaultContent)
		return
	}
	content := defaultContent
	if localized, ok := system_setting.LocalizedSiteContent(field, lang); ok {
		content = localized
	}
	serveRevalidatedJSONVariant(c, content, "lang="+lang)
}

// GetFooter serves the footer HTML for ?lang=. The default footer still
// ships in /api/status as footer_html; the frontend calls this endpoint only
// when /api/status reports footer_i18n_enabled.
func GetFooter(c *gin.Context) {
	common.OptionMapRWMutex.RLock()
	footer := common.OptionMap["Footer"]
	common.OptionMapRWMutex.RUnlock()
	serveLocalizedSiteContent(c, system_setting.SiteContentFooter, footer)
}
