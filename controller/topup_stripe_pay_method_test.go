package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configureStripeForTest enables Stripe top-up with fake credentials and sets
// the pay methods; everything is restored when the test ends.
func configureStripeForTest(t *testing.T, enabled bool, payMethods []map[string]string) {
	t.Helper()
	confirmPaymentComplianceForTest(t)
	originalAPISecret := setting.StripeApiSecret
	originalWebhookSecret := setting.StripeWebhookSecret
	originalPriceID := setting.StripePriceId
	originalPromotionCodes := setting.StripePromotionCodesEnabled
	originalPayMethods := operation_setting.PayMethods
	originalServerAddress := system_setting.ServerAddress
	t.Cleanup(func() {
		setting.StripeApiSecret = originalAPISecret
		setting.StripeWebhookSecret = originalWebhookSecret
		setting.StripePriceId = originalPriceID
		setting.StripePromotionCodesEnabled = originalPromotionCodes
		operation_setting.PayMethods = originalPayMethods
		system_setting.ServerAddress = originalServerAddress
	})
	setting.StripeApiSecret, setting.StripeWebhookSecret, setting.StripePriceId = "", "", ""
	if enabled {
		setting.StripeApiSecret = "sk_test_123"
		setting.StripeWebhookSecret = "whsec_test"
		setting.StripePriceId = "price_topup"
	}
	setting.StripePromotionCodesEnabled = false
	operation_setting.PayMethods = payMethods
	system_setting.ServerAddress = "https://api.example.com"
}

func stripeRoutedPayMethods() []map[string]string {
	return []map[string]string{
		{"name": "Alipay", "type": "alipay", "provider": "stripe"},
		{"name": "WeChat", "type": "wxpay", "provider": "stripe"},
		{"name": "Custom", "type": "custom1"},
	}
}

func TestValidatePayMethodsJsonString(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr string
	}{
		{"legacy entries without provider", `[{"name":"Alipay","type":"alipay"},{"name":"Custom","type":"custom1","min_topup":"50"}]`, ""},
		{"explicit epay provider", `[{"name":"Alipay","type":"alipay","provider":"epay"}]`, ""},
		{"alipay and wxpay through stripe", `[{"name":"Alipay","type":"alipay","provider":"stripe"},{"name":"WeChat","type":"wxpay","provider":"stripe"}]`, ""},
		{"custom method through stripe", `[{"name":"Custom","type":"custom1","provider":"stripe"}]`, "Only alipay and wxpay payment methods can use the Stripe channel, got custom1"},
		{"unknown provider", `[{"name":"Alipay","type":"alipay","provider":"paypal"}]`, "Unsupported payment channel paypal for payment method alipay"},
		{"stripe type configured twice", `[{"name":"A","type":"alipay","provider":"stripe"},{"name":"B","type":"alipay"}]`, "Payment method alipay uses the Stripe channel and must be configured only once"},
		{"non-string value", `[{"name":"Custom","type":"custom1","min_topup":50}]`, "Payment methods must be a JSON array of objects with string values"},
		{"enabled switch values", `[{"name":"Alipay","type":"alipay","enabled":"false"},{"name":"Custom","type":"custom1","enabled":"true"}]`, ""},
		{"boolean enabled value", `[{"name":"Alipay","type":"alipay","enabled":false}]`, "Payment methods must be a JSON array of objects with string values"},
		{"unknown enabled value", `[{"name":"Alipay","type":"alipay","enabled":"no"}]`, "The enabled field of payment method alipay must be true or false"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := operation_setting.ValidatePayMethodsJsonString(tc.value)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.wantErr, err.Error())
		})
	}
}

func TestPayMethodLookupsByProvider(t *testing.T) {
	configureStripeForTest(t, true, stripeRoutedPayMethods())

	assert.False(t, operation_setting.ContainsPayMethod("alipay"), "Epay must not accept a Stripe-routed method")
	assert.True(t, operation_setting.ContainsPayMethod("custom1"))
	assert.True(t, operation_setting.ContainsStripePayMethod("wxpay"))
	assert.False(t, operation_setting.ContainsStripePayMethod("custom1"))
	assert.True(t, isStripeCheckoutPaymentMethod("stripe"))
	assert.True(t, isStripeCheckoutPaymentMethod("alipay"))
	assert.False(t, isStripeCheckoutPaymentMethod("custom1"))

	operation_setting.PayMethods = []map[string]string{{"name": "Alipay", "type": "alipay", "provider": "stripe"}}
	assert.False(t, operation_setting.HasEpayPayMethods(), "Epay is not enabled by Stripe-routed methods alone")

	// Legacy entries without a provider stay Epay methods.
	operation_setting.PayMethods = []map[string]string{{"name": "Alipay", "type": "alipay"}}
	assert.True(t, operation_setting.ContainsPayMethod("alipay"))
	assert.True(t, operation_setting.HasEpayPayMethods())
	assert.False(t, isStripeCheckoutPaymentMethod("alipay"))

	// Stripe-routed methods are refused while Stripe top-up is not configured.
	configureStripeForTest(t, false, stripeRoutedPayMethods())
	assert.False(t, isStripeCheckoutPaymentMethod("alipay"))
}

func TestStripeTopUpCheckoutParamsByPaymentMethod(t *testing.T) {
	configureStripeForTest(t, true, stripeRoutedPayMethods())

	legacy := newStripeTopUpCheckoutParams("ref_1", "cus_1", "user@example.com", "stripe", 20, "", "")
	assert.Empty(t, legacy.PaymentMethodTypes, "the legacy Stripe entry keeps the Dashboard payment methods")
	assert.Nil(t, legacy.PaymentMethodOptions)
	require.Len(t, legacy.LineItems, 1)
	assert.Equal(t, "price_topup", *legacy.LineItems[0].Price)
	assert.Equal(t, int64(20), *legacy.LineItems[0].Quantity)
	assert.Equal(t, "payment", *legacy.Mode)
	assert.Equal(t, "cus_1", *legacy.Customer)
	assert.Nil(t, legacy.CustomerCreation)

	alipay := newStripeTopUpCheckoutParams("ref_2", "", "user@example.com", "alipay", 20, "", "")
	assert.Equal(t, []string{"alipay"}, stringValues(alipay.PaymentMethodTypes))
	assert.Nil(t, alipay.PaymentMethodOptions)
	assert.Equal(t, "price_topup", *alipay.LineItems[0].Price)
	assert.Equal(t, "user@example.com", *alipay.CustomerEmail)
	assert.Equal(t, "always", *alipay.CustomerCreation)

	wxpay := newStripeTopUpCheckoutParams("ref_3", "", "", "wxpay", 20, "", "")
	assert.Equal(t, []string{"wechat_pay"}, stringValues(wxpay.PaymentMethodTypes))
	require.NotNil(t, wxpay.PaymentMethodOptions)
	require.NotNil(t, wxpay.PaymentMethodOptions.WeChatPay)
	assert.Equal(t, "web", *wxpay.PaymentMethodOptions.WeChatPay.Client)
	assert.Equal(t, "payment", *wxpay.Mode)
}

func TestStripeSubscriptionCheckoutParams(t *testing.T) {
	configureStripeForTest(t, true, stripeRoutedPayMethods())
	plan := &model.SubscriptionPlan{Title: "Pro", PriceAmount: 19.99, Currency: "USD", StripePriceId: "price_recurring"}

	recurring := newStripeSubscriptionCheckoutParams("sub_ref_1", "", "", plan.StripePriceId)
	assert.Equal(t, "subscription", *recurring.Mode)
	assert.Equal(t, "price_recurring", *recurring.LineItems[0].Price)
	assert.Empty(t, recurring.PaymentMethodTypes)

	oneTime, err := newStripeSubscriptionPaymentCheckoutParams("sub_ref_2", "", "", "wxpay", plan)
	require.NoError(t, err)
	assert.Equal(t, "payment", *oneTime.Mode, "WeChat Pay cannot back a Stripe subscription")
	require.Len(t, oneTime.LineItems, 1)
	item := oneTime.LineItems[0]
	assert.Nil(t, item.Price, "a recurring price cannot be used in payment mode")
	require.NotNil(t, item.PriceData)
	assert.Equal(t, "usd", *item.PriceData.Currency)
	assert.Equal(t, int64(1999), *item.PriceData.UnitAmount)
	assert.Equal(t, "Pro", *item.PriceData.ProductData.Name)
	assert.Equal(t, int64(1), *item.Quantity)
	assert.Equal(t, []string{"wechat_pay"}, stringValues(oneTime.PaymentMethodTypes))
	assert.Equal(t, "web", *oneTime.PaymentMethodOptions.WeChatPay.Client)

	plan.PriceAmount = 0.004
	_, err = newStripeSubscriptionPaymentCheckoutParams("sub_ref_3", "", "", "alipay", plan)
	require.Error(t, err)
	assert.Equal(t, "Plan amount is too low", err.Error())
}

func TestGetTopUpInfoListsStripeRoutedMethodsOnlyWhenStripeIsConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	payMethodsByType := func() map[string]map[string]any {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/user/topup/info", nil)
		GetTopUpInfo(c)
		var body struct {
			Data struct {
				PayMethods []map[string]any `json:"pay_methods"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
		result := make(map[string]map[string]any)
		for _, method := range body.Data.PayMethods {
			result[method["type"].(string)] = method
		}
		return result
	}

	configureStripeForTest(t, false, stripeRoutedPayMethods())
	methods := payMethodsByType()
	assert.NotContains(t, methods, "alipay")
	assert.NotContains(t, methods, "wxpay")
	assert.NotContains(t, methods, "stripe")
	require.Contains(t, methods, "custom1")
	assert.Equal(t, "epay", methods["custom1"]["provider"])

	configureStripeForTest(t, true, stripeRoutedPayMethods())
	methods = payMethodsByType()
	require.Contains(t, methods, "alipay")
	assert.Equal(t, "stripe", methods["alipay"]["provider"])
	assert.Equal(t, "stripe", methods["wxpay"]["provider"])
	assert.Equal(t, "stripe", methods["stripe"]["provider"])
	assert.Equal(t, "epay", methods["custom1"]["provider"])
	// The response is built from copies: the configured entries stay untouched.
	assert.NotContains(t, operation_setting.PayMethods[2], "provider")
	assert.Len(t, operation_setting.PayMethods, 3)
}

func TestEpayOrdersRejectStripeRoutedPayMethods(t *testing.T) {
	setupSubscriptionEpayTest(t, 7.3)
	plan := createEpayTestPlan(t, 10)
	operation_setting.PayMethods = []map[string]string{{"name": "Alipay", "type": "alipay", "provider": "stripe"}}
	gin.SetMode(gin.TestMode)

	post := func(handler gin.HandlerFunc, path string, body string) string {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("id", 1)
		handler(c)
		return recorder.Body.String()
	}

	walletBody := post(RequestEpay, "/api/user/pay", `{"amount":10,"payment_method":"alipay"}`)
	assert.Contains(t, walletBody, "Payment method does not exist")
	subscriptionBody := post(SubscriptionRequestEpay, "/api/subscription/epay/pay", `{"plan_id":`+common.Interface2String(plan.Id)+`,"payment_method":"alipay"}`)
	assert.Contains(t, subscriptionBody, "Payment method does not exist")

	var orders int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&orders).Error)
	assert.Zero(t, orders)
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Count(&orders).Error)
	assert.Zero(t, orders)
}

func stringValues(values []*string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, *value)
	}
	return result
}
