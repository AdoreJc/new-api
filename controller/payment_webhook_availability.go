package controller

import (
	"strings"

	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// Each gateway has two checks:
//
//   - is<Gateway>WebhookEnabled: the gateway is configured well enough to verify
//     and settle callbacks. It ignores the administrator's gateway and
//     payment-method switches, so an order created before a switch was turned
//     off is still credited, expired or failed when its callback arrives.
//   - is<Gateway>TopUpEnabled: the gateway also accepts new orders, which needs
//     its administrator switch to be on as well.

func isPaymentComplianceConfirmed() bool {
	return operation_setting.IsPaymentComplianceConfirmed()
}

func isStripeTopUpEnabled() bool {
	return setting.StripeEnabled && isStripeWebhookEnabled()
}

func isStripeWebhookConfigured() bool {
	return strings.TrimSpace(setting.StripeWebhookSecret) != ""
}

func isStripeWebhookEnabled() bool {
	if !isPaymentComplianceConfirmed() {
		return false
	}
	return strings.TrimSpace(setting.StripeApiSecret) != "" &&
		strings.TrimSpace(setting.StripeWebhookSecret) != "" &&
		strings.TrimSpace(setting.StripePriceId) != ""
}

func isCreemTopUpEnabled() bool {
	return setting.CreemEnabled && isCreemConfigured()
}

func isCreemConfigured() bool {
	if !isPaymentComplianceConfirmed() {
		return false
	}
	products := strings.TrimSpace(setting.CreemProducts)
	return strings.TrimSpace(setting.CreemApiKey) != "" &&
		products != "" &&
		products != "[]"
}

func isCreemWebhookConfigured() bool {
	return strings.TrimSpace(setting.CreemWebhookSecret) != ""
}

func isCreemWebhookEnabled() bool {
	return isCreemConfigured() && isCreemWebhookConfigured()
}

// isWaffoTopUpEnabled uses the existing WaffoEnabled option as Waffo's gateway
// switch.
func isWaffoTopUpEnabled() bool {
	return setting.WaffoEnabled && isWaffoWebhookEnabled()
}

func isWaffoWebhookConfigured() bool {
	if setting.WaffoSandbox {
		return strings.TrimSpace(setting.WaffoSandboxApiKey) != "" &&
			strings.TrimSpace(setting.WaffoSandboxPrivateKey) != "" &&
			strings.TrimSpace(setting.WaffoSandboxPublicCert) != ""
	}

	return strings.TrimSpace(setting.WaffoApiKey) != "" &&
		strings.TrimSpace(setting.WaffoPrivateKey) != "" &&
		strings.TrimSpace(setting.WaffoPublicCert) != ""
}

func isWaffoWebhookEnabled() bool {
	return isPaymentComplianceConfirmed() && isWaffoWebhookConfigured()
}

func isWaffoPancakeTopUpEnabled() bool {
	return setting.WaffoPancakeEnabled && isWaffoPancakeWebhookEnabled()
}

func isWaffoPancakeWebhookConfigured() bool {
	// Presence-of-credentials = configured. Webhook public keys ship inside
	// the SDK; mode (test/prod) is read from each event.
	return strings.TrimSpace(setting.WaffoPancakeMerchantID) != "" &&
		strings.TrimSpace(setting.WaffoPancakePrivateKey) != "" &&
		strings.TrimSpace(setting.WaffoPancakeProductID) != ""
}

func isWaffoPancakeWebhookEnabled() bool {
	return isPaymentComplianceConfirmed() && isWaffoPancakeWebhookConfigured()
}

func isEpayTopUpEnabled() bool {
	return operation_setting.EpayEnabled &&
		isEpayWebhookEnabled() &&
		operation_setting.HasEnabledEpayPayMethods()
}

func isEpayWebhookConfigured() bool {
	return strings.TrimSpace(operation_setting.PayAddress) != "" &&
		strings.TrimSpace(operation_setting.EpayId) != "" &&
		strings.TrimSpace(operation_setting.EpayKey) != ""
}

func isEpayWebhookEnabled() bool {
	if !isPaymentComplianceConfirmed() {
		return false
	}
	return isEpayWebhookConfigured() && operation_setting.HasEpayPayMethods()
}
