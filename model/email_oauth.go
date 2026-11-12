package model

import (
	"slices"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"

	"gorm.io/gorm"
)

// AuthFlowPurposeEmailOAuth is the one-time state of a sender mailbox authorization.
const AuthFlowPurposeEmailOAuth = "email_oauth"

func init() {
	common.EmailOAuthRefreshTokenStore = ReplaceEmailOAuthRefreshToken
}

// emailOAuthWriteMutex makes each authorization write (database and option
// map together) atomic with respect to the others, so a rotation and a
// re-authorization cannot interleave and leave the map with one grant's
// token and another grant's mailbox. Lock order: the common token state,
// then this mutex, then OptionMapRWMutex. Never take it while holding
// OptionMapRWMutex, and reset the token cache only after releasing it.
var emailOAuthWriteMutex sync.Mutex

// emailOAuthAfterDatabaseWrite runs between a write and its option map
// update; tests use it to interleave writes.
var emailOAuthAfterDatabaseWrite func()

var emailDeliveryOptionKeys = []string{
	"EmailProvider", "ResendApiKey", "ResendFrom",
	common.EmailOAuthProviderOption, common.EmailOAuthClientIdOption,
	common.EmailOAuthClientSecretOption, common.EmailOAuthTenantOption,
	common.EmailOAuthFromOption,
}

// emailOAuthAppKeys identify the OAuth client a stored refresh token belongs to.
var emailOAuthAppKeys = []string{common.EmailOAuthProviderOption, common.EmailOAuthClientIdOption, common.EmailOAuthTenantOption}

// validateEmailDeliveryOption keeps the active sending method able to send.
// Inactive drafts are checked when their method is selected.
func validateEmailDeliveryOption(key, value string) error {
	switch key {
	case common.EmailOAuthRefreshTokenOption, common.EmailOAuthEmailOption, common.EmailOAuthScopesOption,
		common.EmailOAuthSecretConfiguredOption, common.EmailOAuthSharedSendGrantedOption:
		return common.NewMessage("This setting is managed by the mailbox authorization flow")
	case common.EmailOAuthFromOption:
		if err := common.ValidateEmailOAuthSender(value); err != nil {
			return err
		}
	}
	if !slices.Contains(emailDeliveryOptionKeys, key) {
		return nil
	}
	if key == "EmailProvider" && value != "smtp" && value != "resend" && value != "oauth" {
		return common.NewMessage("Email sending method must be SMTP, Resend, or OAuth")
	}
	if key == common.EmailOAuthProviderOption && value != common.EmailOAuthProviderGoogle && value != common.EmailOAuthProviderMicrosoft {
		return common.ErrEmailOAuthAppInvalid
	}
	common.OptionMapRWMutex.RLock()
	settings := make(map[string]string, len(emailDeliveryOptionKeys)+2)
	for _, name := range slices.Concat(emailDeliveryOptionKeys, []string{common.EmailOAuthRefreshTokenOption, common.EmailOAuthEmailOption}) {
		settings[name] = common.OptionMap[name]
	}
	common.OptionMapRWMutex.RUnlock()
	if slices.Contains(emailOAuthAppKeys, key) && settings[common.EmailOAuthRefreshTokenOption] != "" &&
		strings.TrimSpace(settings[key]) != strings.TrimSpace(value) {
		return common.NewMessage("Remove the mailbox authorization before changing the OAuth app")
	}
	settings[key] = value
	switch settings["EmailProvider"] {
	case "resend":
		return common.ValidateResendConfiguration(settings["ResendApiKey"], settings["ResendFrom"])
	case "oauth":
		return common.ValidateEmailOAuthActivation(settings)
	}
	return nil
}

// SaveEmailOAuthAuthorization writes the refresh token, mailbox and granted
// scopes together, replacing any previous authorization.
func SaveEmailOAuthAuthorization(grant common.EmailOAuthGrant) error {
	if err := storeEmailOAuthAuthorization(grant); err != nil {
		return err
	}
	common.ResetEmailOAuthTokenCache()
	return nil
}

// storeEmailOAuthAuthorization is the serialized part of an authorization
// write; the token cache reset stays outside the lock.
func storeEmailOAuthAuthorization(grant common.EmailOAuthGrant) error {
	emailOAuthWriteMutex.Lock()
	defer emailOAuthWriteMutex.Unlock()
	values := map[string]string{
		common.EmailOAuthRefreshTokenOption: grant.RefreshToken,
		common.EmailOAuthEmailOption:        grant.Email,
		common.EmailOAuthScopesOption:       grant.Scopes,
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		for key, value := range values {
			// Assign with a map so an empty value is written too.
			if err := tx.Where(Option{Key: key}).Assign(map[string]any{"value": value}).FirstOrCreate(&Option{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if emailOAuthAfterDatabaseWrite != nil {
		emailOAuthAfterDatabaseWrite()
	}
	for key, value := range values {
		if err := updateOptionMap(key, value); err != nil {
			return err
		}
	}
	return nil
}

// ClearEmailOAuthAuthorization removes the mailbox authorization. It is
// refused while OAuth is the sending method, because email would stop.
func ClearEmailOAuthAuthorization() error {
	common.OptionMapRWMutex.RLock()
	provider := common.OptionMap["EmailProvider"]
	common.OptionMapRWMutex.RUnlock()
	if provider == "oauth" {
		return common.NewMessage("Select another email sending method before removing the mailbox authorization")
	}
	return SaveEmailOAuthAuthorization(common.EmailOAuthGrant{})
}

// ReplaceEmailOAuthRefreshToken saves a rotated refresh token only while the
// stored token is still the one that was refreshed, so a rotation from an
// old grant cannot overwrite a newer authorization.
func ReplaceEmailOAuthRefreshToken(previous, next string) error {
	if previous == "" || next == "" {
		return common.ErrEmailOAuthAuthorizationChanged
	}
	emailOAuthWriteMutex.Lock()
	defer emailOAuthWriteMutex.Unlock()
	result := DB.Model(&Option{}).Where(&Option{Key: common.EmailOAuthRefreshTokenOption, Value: previous}).Update("value", next)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return common.ErrEmailOAuthAuthorizationChanged
	}
	if emailOAuthAfterDatabaseWrite != nil {
		emailOAuthAfterDatabaseWrite()
	}
	return updateOptionMap(common.EmailOAuthRefreshTokenOption, next)
}
