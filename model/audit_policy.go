package model

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/audit_policy_setting"

	"gorm.io/gorm"
)

var (
	auditPolicyMutex sync.Mutex

	ErrAuditPolicyConflict = common.NewMessage("The audit policy was changed by someone else. Reload and try again")
)

// AuditPolicyStoredConfig reads the saved rule table. saved is false when no
// table is stored and the rules are derived from the engine settings.
func AuditPolicyStoredConfig() (config audit_policy_setting.Config, saved bool, err error) {
	var option Option
	if err := DB.Where(&Option{Key: audit_policy_setting.OptionKey}).Limit(1).Find(&option).Error; err != nil {
		return config, false, err
	}
	if option.Value == "" {
		return audit_policy_setting.Current().Config.Clone(), false, nil
	}
	config, err = audit_policy_setting.ParseConfig(option.Value)
	return config, err == nil, err
}

// SaveAuditPolicy stores a validated table when the stored version still
// equals expectedVersion, then bumps the version. A nil config removes the
// saved table, so the rules are derived from the engine settings again. The
// process mutex serializes writers on one node; the version check inside the
// transaction rejects a concurrent save from another node.
func SaveAuditPolicy(config *audit_policy_setting.Config, expectedVersion int64) (audit_policy_setting.Config, error) {
	if config != nil {
		if err := config.Normalize(); err != nil {
			return *config, err
		}
	}
	auditPolicyMutex.Lock()
	defer auditPolicyMutex.Unlock()
	// Create the row first so the locking read below always has a row to lock.
	_ = DB.Where(Option{Key: audit_policy_setting.OptionKey}).Attrs(Option{Value: ""}).FirstOrCreate(&Option{}).Error
	value := ""
	var saved audit_policy_setting.Config
	err := DB.Transaction(func(tx *gorm.DB) error {
		var stored Option
		if err := lockForUpdate(tx).Where(&Option{Key: audit_policy_setting.OptionKey}).Limit(1).Find(&stored).Error; err != nil {
			return err
		}
		storedVersion := int64(0)
		if stored.Value != "" {
			if storedConfig, err := audit_policy_setting.ParseConfig(stored.Value); err == nil {
				storedVersion = storedConfig.ConfigVersion
			}
		}
		if storedVersion != expectedVersion {
			return ErrAuditPolicyConflict
		}
		if config != nil {
			saved = *config
			saved.ConfigVersion = expectedVersion + 1
			encoded, err := common.Marshal(saved)
			if err != nil {
				return err
			}
			value = string(encoded)
		}
		return tx.Model(&Option{}).Where(&Option{Key: audit_policy_setting.OptionKey}).Update("value", value).Error
	})
	if err != nil {
		return saved, err
	}
	return saved, updateOptionMap(audit_policy_setting.OptionKey, value)
}
