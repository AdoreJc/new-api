package model

import (
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/audit_policy_setting"
	"github.com/QuantumNous/new-api/setting/content_moderation_setting"
	"github.com/QuantumNous/new-api/setting/prompt_audit_setting"

	"gorm.io/gorm"
)

var (
	auditPolicyMutex sync.Mutex

	ErrAuditPolicyConflict = common.NewMessage("The audit policy was changed by someone else. Reload and try again")
)

// AuditPolicyStoredConfig reads the saved rule table. saved is false when no
// table is stored and the rules are derived from the engine settings. A
// stored table that fails validation is read as far as possible and its
// problems are returned, so an administrator can see and fix it.
func AuditPolicyStoredConfig() (config audit_policy_setting.Config, saved bool, problems []string, err error) {
	var option Option
	if err := DB.Where(&Option{Key: audit_policy_setting.OptionKey}).Limit(1).Find(&option).Error; err != nil {
		return config, false, nil, err
	}
	config, err = audit_policy_setting.ParseConfig(option.Value)
	if option.Value == "" || err == nil && config.Derived {
		derived := derivedAuditPolicy()
		derived.ConfigVersion = config.ConfigVersion
		return derived, false, nil, nil
	}
	if err != nil {
		config, problems = audit_policy_setting.ParseDegraded(option.Value)
	}
	return config, true, problems, nil
}

// derivedAuditPolicy derives the rules from the engine settings stored in
// the database, the source shared by all nodes, rather than from this
// node's possibly older copy. A stored engine setting that cannot be read
// falls back to this node's snapshot, which knows its degraded state.
func derivedAuditPolicy() audit_policy_setting.Config {
	paSnapshot := prompt_audit_setting.Current()
	pa, degraded := paSnapshot.Config, paSnapshot.Degraded
	if stored, _, err := PromptAuditStoredConfig(); err == nil {
		pa, degraded = stored, false
	}
	cm := content_moderation_setting.Current().Config
	if stored, _, err := ContentModerationStoredConfig(); err == nil {
		cm = stored
	}
	return audit_policy_setting.Derive(pa, degraded, cm)
}

// storedAuditPolicyVersion reads the version of a stored value, including a
// reset marker and a table that fails validation.
func storedAuditPolicyVersion(value string) int64 {
	if value == "" {
		return 0
	}
	if parsed, err := audit_policy_setting.ParseConfig(value); err == nil {
		return parsed.ConfigVersion
	}
	degraded, _ := audit_policy_setting.ParseDegraded(value)
	return degraded.ConfigVersion
}

// SaveAuditPolicy stores a validated table when the stored version still
// equals expectedVersion, then bumps the version. A nil config removes the
// saved table, so the rules are derived from the engine settings again; it
// stores a marker so the version keeps counting and an editor holding an
// older version cannot overwrite later changes. The process mutex
// serializes writers on one node; the version check inside the transaction
// rejects a concurrent save from another node.
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
	saved := audit_policy_setting.Config{Derived: true}
	if config != nil {
		saved = *config
	}
	value := ""
	err := DB.Transaction(func(tx *gorm.DB) error {
		var stored Option
		if err := lockForUpdate(tx).Where(&Option{Key: audit_policy_setting.OptionKey}).Limit(1).Find(&stored).Error; err != nil {
			return err
		}
		if storedAuditPolicyVersion(stored.Value) != expectedVersion {
			return ErrAuditPolicyConflict
		}
		saved.ConfigVersion = expectedVersion + 1
		encoded, err := common.Marshal(saved)
		if err != nil {
			return err
		}
		if len(encoded) > audit_policy_setting.MaxStoredBytes {
			return audit_policy_setting.ErrPolicyTooLarge
		}
		value = string(encoded)
		return tx.Model(&Option{}).Where(&Option{Key: audit_policy_setting.OptionKey}).Update("value", value).Error
	})
	if err != nil {
		return saved, err
	}
	return saved, updateOptionMap(audit_policy_setting.OptionKey, value)
}
