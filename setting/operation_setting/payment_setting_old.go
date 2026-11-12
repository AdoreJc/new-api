/**
此文件为旧版支付设置文件，如需增加新的参数、变量等，请在 payment_setting.go 中添加
This file is the old version of the payment settings file. If you need to add new parameters, variables, etc., please add them in payment_setting.go
*/

package operation_setting

import (
	"slices"

	"github.com/QuantumNous/new-api/common"
)

var PayAddress = ""
var CustomCallbackAddress = ""
var EpayId = ""
var EpayKey = ""
var Price = 7.3
var MinTopUp = 1
var USDExchangeRate = 7.3

var PayMethods = []map[string]string{
	{
		"name": "支付宝",
		"icon": "SiAlipay",
		"type": "alipay",
	},
	{
		"name": "微信",
		"icon": "SiWechat",
		"type": "wxpay",
	},
	{
		"name":      "自定义1",
		"icon":      "LuCreditCard",
		"type":      "custom1",
		"min_topup": "50",
	},
}

func UpdatePayMethodsByJsonString(jsonString string) error {
	PayMethods = make([]map[string]string, 0)
	return common.Unmarshal([]byte(jsonString), &PayMethods)
}

func PayMethods2JsonString() string {
	jsonBytes, err := common.Marshal(PayMethods)
	if err != nil {
		return "[]"
	}
	return string(jsonBytes)
}

// Payment providers a PayMethods entry can be routed through. An entry without
// a "provider" field is an Epay method, which is how every entry behaved before
// the field existed.
const (
	PayMethodProviderEpay   = "epay"
	PayMethodProviderStripe = "stripe"
)

// PayMethods types that may be routed through Stripe Checkout instead of Epay.
const (
	PayMethodTypeAlipay = "alipay"
	PayMethodTypeWxpay  = "wxpay"
)

var stripeCheckoutPayMethodTypes = []string{PayMethodTypeAlipay, PayMethodTypeWxpay}

// PayMethodProvider returns the provider a PayMethods entry is routed through.
func PayMethodProvider(payMethod map[string]string) string {
	if payMethod["provider"] == PayMethodProviderStripe {
		return PayMethodProviderStripe
	}
	return PayMethodProviderEpay
}

// IsPayMethodEnabled reports whether a PayMethods entry accepts new orders. An
// entry without "enabled", which is how every entry looked before the field
// existed, is enabled; only the value "false" switches it off.
func IsPayMethodEnabled(payMethod map[string]string) bool {
	return payMethod["enabled"] != "false"
}

// IsPayMethodTypeDisabled reports whether PayMethods lists methodType and every
// entry of that type is switched off. Gateways with a built-in entry (stripe,
// creem, waffo, waffo_pancake) use it to keep that entry hidden as well.
func IsPayMethodTypeDisabled(methodType string) bool {
	listed := false
	for _, payMethod := range PayMethods {
		if payMethod["type"] != methodType {
			continue
		}
		if IsPayMethodEnabled(payMethod) {
			return false
		}
		listed = true
	}
	return listed
}

// ContainsPayMethod reports whether method is an enabled Epay payment method.
// Entries routed through Stripe are excluded, so the Epay order endpoints
// reject them, and so are switched-off entries.
func ContainsPayMethod(method string) bool {
	for _, payMethod := range PayMethods {
		if payMethod["type"] == method && PayMethodProvider(payMethod) == PayMethodProviderEpay && IsPayMethodEnabled(payMethod) {
			return true
		}
	}
	return false
}

// HasEnabledEpayPayMethods reports whether at least one enabled payment method
// is routed through Epay.
func HasEnabledEpayPayMethods() bool {
	for _, payMethod := range PayMethods {
		if PayMethodProvider(payMethod) == PayMethodProviderEpay && IsPayMethodEnabled(payMethod) {
			return true
		}
	}
	return false
}

// HasEpayPayMethods reports whether at least one configured payment method is
// routed through Epay, switched off or not: callbacks for orders placed before
// a method was switched off must still be accepted.
func HasEpayPayMethods() bool {
	for _, payMethod := range PayMethods {
		if PayMethodProvider(payMethod) == PayMethodProviderEpay {
			return true
		}
	}
	return false
}

// ContainsStripePayMethod reports whether method is an enabled alipay or wxpay
// entry configured to pay through Stripe Checkout.
func ContainsStripePayMethod(method string) bool {
	if !slices.Contains(stripeCheckoutPayMethodTypes, method) {
		return false
	}
	for _, payMethod := range PayMethods {
		if payMethod["type"] == method && PayMethodProvider(payMethod) == PayMethodProviderStripe && IsPayMethodEnabled(payMethod) {
			return true
		}
	}
	return false
}

// ValidatePayMethodsJsonString checks a PayMethods option value before it is
// saved: provider must be empty, epay or stripe; only alipay and wxpay may use
// stripe; enabled must be empty, "true" or "false"; and a type routed through Stripe may appear only once, because the
// payment endpoints look methods up by type.
func ValidatePayMethodsJsonString(jsonString string) error {
	var payMethods []map[string]string
	if err := common.Unmarshal([]byte(jsonString), &payMethods); err != nil {
		return common.NewMessage("Payment methods must be a JSON array of objects with string values")
	}
	typeCount := make(map[string]int, len(payMethods))
	stripeTypes := make([]string, 0)
	for _, payMethod := range payMethods {
		methodType := payMethod["type"]
		typeCount[methodType]++
		switch payMethod["enabled"] {
		case "", "true", "false":
		default:
			return common.NewMessage("The enabled field of payment method {{type}} must be true or false", map[string]any{"type": methodType})
		}
		switch payMethod["provider"] {
		case "", PayMethodProviderEpay:
		case PayMethodProviderStripe:
			if !slices.Contains(stripeCheckoutPayMethodTypes, methodType) {
				return common.NewMessage("Only alipay and wxpay payment methods can use the Stripe channel, got {{type}}", map[string]any{"type": methodType})
			}
			stripeTypes = append(stripeTypes, methodType)
		default:
			return common.NewMessage("Unsupported payment channel {{provider}} for payment method {{type}}", map[string]any{"provider": payMethod["provider"], "type": methodType})
		}
	}
	for _, methodType := range stripeTypes {
		if typeCount[methodType] > 1 {
			return common.NewMessage("Payment method {{type}} uses the Stripe channel and must be configured only once", map[string]any{"type": methodType})
		}
	}
	return nil
}
