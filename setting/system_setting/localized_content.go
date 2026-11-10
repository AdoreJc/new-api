package system_setting

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
)

// SiteContentI18nSettings stores per-language overrides of the public site
// content. The legacy single-value options (Notice, About, HomePageContent,
// Footer, legal.user_agreement, legal.privacy_policy) remain the default
// content; each map here is keyed by a canonical BCP-47 tag ("en", "zh-CN",
// "zh-TW") and an empty or missing entry means "use the default content".
//
// Each field is persisted as one JSON option, e.g.
// site_content_i18n.about = {"en":"...","zh-TW":"..."}.
type SiteContentI18nSettings struct {
	Notice          map[string]string `json:"notice"`
	About           map[string]string `json:"about"`
	HomePageContent map[string]string `json:"home_page_content"`
	Footer          map[string]string `json:"footer"`
	UserAgreement   map[string]string `json:"user_agreement"`
	PrivacyPolicy   map[string]string `json:"privacy_policy"`
}

// SiteContentField names one localized site-content document; the value is
// its key inside the site_content_i18n option namespace.
type SiteContentField string

const (
	SiteContentNotice          SiteContentField = "notice"
	SiteContentAbout           SiteContentField = "about"
	SiteContentHomePageContent SiteContentField = "home_page_content"
	SiteContentFooter          SiteContentField = "footer"
	SiteContentUserAgreement   SiteContentField = "user_agreement"
	SiteContentPrivacyPolicy   SiteContentField = "privacy_policy"
)

const siteContentI18nOptionPrefix = "site_content_i18n."

// maxContentLanguageLength bounds the ?lang= value and override keys; the
// longest tag we ever need is far shorter (e.g. "zh-Hant-TW").
const maxContentLanguageLength = 35

var siteContentI18n = SiteContentI18nSettings{
	Notice:          map[string]string{},
	About:           map[string]string{},
	HomePageContent: map[string]string{},
	Footer:          map[string]string{},
	UserAgreement:   map[string]string{},
	PrivacyPolicy:   map[string]string{},
}

func init() {
	config.GlobalConfig.Register("site_content_i18n", &siteContentI18n)
}

// siteContentOverrides returns the override map of field. Callers must hold
// common.OptionMapRWMutex: option updates replace these maps while holding
// its write lock (model.updateOptionMap -> handleConfigUpdate).
func siteContentOverrides(field SiteContentField) map[string]string {
	switch field {
	case SiteContentNotice:
		return siteContentI18n.Notice
	case SiteContentAbout:
		return siteContentI18n.About
	case SiteContentHomePageContent:
		return siteContentI18n.HomePageContent
	case SiteContentFooter:
		return siteContentI18n.Footer
	case SiteContentUserAgreement:
		return siteContentI18n.UserAgreement
	case SiteContentPrivacyPolicy:
		return siteContentI18n.PrivacyPolicy
	}
	return nil
}

// NormalizeContentLanguage turns a requested language into the canonical
// BCP-47 form used as override keys: lowercase language, title-case script,
// uppercase region ("zh_tw" -> "zh-TW", "zh-hant-hk" -> "zh-Hant-HK"). The
// frontend's interface codes zhCN / zhTW are accepted as aliases. It returns
// "" for an empty or malformed tag.
func NormalizeContentLanguage(raw string) string {
	tag := strings.ReplaceAll(strings.TrimSpace(raw), "_", "-")
	if tag == "" || len(tag) > maxContentLanguageLength {
		return ""
	}
	switch strings.ToLower(tag) {
	case "zhcn":
		return "zh-CN"
	case "zhtw":
		return "zh-TW"
	}

	subtags := strings.Split(tag, "-")
	var builder strings.Builder
	for i, subtag := range subtags {
		if subtag == "" || len(subtag) > 8 || !isASCIIAlphanumeric(subtag) {
			return ""
		}
		if i == 0 {
			if len(subtag) < 2 || len(subtag) > 3 || !isASCIILetters(subtag) {
				return ""
			}
			builder.WriteString(strings.ToLower(subtag))
			continue
		}
		builder.WriteByte('-')
		switch {
		case len(subtag) == 2 && isASCIILetters(subtag):
			builder.WriteString(strings.ToUpper(subtag))
		case len(subtag) == 4 && isASCIILetters(subtag):
			builder.WriteString(strings.ToUpper(subtag[:1]) + strings.ToLower(subtag[1:]))
		default:
			builder.WriteString(strings.ToLower(subtag))
		}
	}
	return builder.String()
}

// contentLanguageCandidates lists the override keys tried for a canonical
// tag, most specific first: the tag itself, then its primary language
// ("en-US" -> "en"). Chinese variants also fall back to each other, preferring
// the same script (Traditional: zh-TW then zh-CN; otherwise zh-CN then zh-TW),
// because a reader of either script understands the other far better than a
// default that may be written in another language. Other languages fall back
// straight to the default content.
func contentLanguageCandidates(lang string) []string {
	candidates := []string{lang}
	add := func(tag string) {
		if tag != "" && !slices.Contains(candidates, tag) {
			candidates = append(candidates, tag)
		}
	}
	primary, rest, _ := strings.Cut(lang, "-")
	add(primary)
	if primary != "zh" {
		return candidates
	}
	traditional := false
	for subtag := range strings.SplitSeq(rest, "-") {
		switch subtag {
		case "Hant", "TW", "HK", "MO":
			traditional = true
		}
	}
	if traditional {
		add("zh-TW")
		add("zh-CN")
	} else {
		add("zh-CN")
		add("zh-TW")
	}
	return candidates
}

// LocalizedSiteContent returns the non-blank override of field for lang (a
// canonical tag from NormalizeContentLanguage), following
// contentLanguageCandidates. ok is false when the default content applies.
// It takes common.OptionMapRWMutex's read lock, so callers must not already
// hold that lock (read locks are not reentrant).
func LocalizedSiteContent(field SiteContentField, lang string) (content string, ok bool) {
	if lang == "" {
		return "", false
	}
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	overrides := siteContentOverrides(field)
	for _, candidate := range contentLanguageCandidates(lang) {
		if value := overrides[candidate]; strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	return "", false
}

// HasLocalizedSiteContent reports whether field has any non-blank override.
// Like LocalizedSiteContent, call it without holding common.OptionMapRWMutex.
func HasLocalizedSiteContent(field SiteContentField) bool {
	common.OptionMapRWMutex.RLock()
	defer common.OptionMapRWMutex.RUnlock()
	for _, value := range siteContentOverrides(field) {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// ValidateSiteContentI18nOption rejects a site_content_i18n.* option update
// whose field is unknown, whose value is not a JSON object of strings, or
// whose keys are not canonical language tags. Rejecting (rather than
// ignoring) matters because the config loader silently keeps the previous
// map on a parse failure, which would leave memory and the database out of
// sync. Keys of other options are ignored.
func ValidateSiteContentI18nOption(key, value string) error {
	field, found := strings.CutPrefix(key, siteContentI18nOptionPrefix)
	if !found {
		return nil
	}
	switch SiteContentField(field) {
	case SiteContentNotice, SiteContentAbout, SiteContentHomePageContent,
		SiteContentFooter, SiteContentUserAgreement, SiteContentPrivacyPolicy:
	default:
		return common.NewMessage("Unknown localized content field: {{field}}", map[string]any{"field": field})
	}
	var overrides map[string]string
	if err := common.UnmarshalJsonStr(value, &overrides); err != nil || overrides == nil {
		return common.NewMessage("Localized content must be a JSON object that maps language codes to text")
	}
	for lang := range overrides {
		if NormalizeContentLanguage(lang) != lang {
			return common.NewMessage("Invalid language code in localized content: {{lang}}", map[string]any{"lang": lang})
		}
	}
	return nil
}

func isASCIILetters(value string) bool {
	for i := range len(value) {
		c := value[i] | 0x20
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func isASCIIAlphanumeric(value string) bool {
	for i := range len(value) {
		c := value[i]
		if (c|0x20 >= 'a' && c|0x20 <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}
