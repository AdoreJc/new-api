package setting

var StripeApiSecret = ""
var StripeWebhookSecret = ""
var StripePriceId = ""
var StripeUnitPrice = 8.0
var StripeMinTopUp = 1
var StripePromotionCodesEnabled = false

// StripeEnabled is the administrator switch for new Stripe orders. Webhooks
// for existing orders are processed whatever its value.
var StripeEnabled = true
