package controller

import (
	"bytes"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const paymentChannelDisabled = "This payment channel is disabled"

// configurePaymentGatewaysForTest configures every gateway with fake
// credentials, leaves every administrator switch on and sets the pay methods;
// everything is restored when the test ends.
func configurePaymentGatewaysForTest(t *testing.T, payMethods []map[string]string) {
	t.Helper()
	configureStripeForTest(t, true, payMethods)
	originalEpay := []string{operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey}
	originalCreem := []string{setting.CreemApiKey, setting.CreemProducts, setting.CreemWebhookSecret}
	originalWaffo := []string{setting.WaffoApiKey, setting.WaffoPrivateKey, setting.WaffoPublicCert}
	originalWaffoSandbox := setting.WaffoSandbox
	originalPancake := []string{setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey, setting.WaffoPancakeProductID}
	originalSwitches := []bool{operation_setting.EpayEnabled, setting.StripeEnabled, setting.CreemEnabled, setting.WaffoEnabled, setting.WaffoPancakeEnabled}
	t.Cleanup(func() {
		operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = originalEpay[0], originalEpay[1], originalEpay[2]
		setting.CreemApiKey, setting.CreemProducts, setting.CreemWebhookSecret = originalCreem[0], originalCreem[1], originalCreem[2]
		setting.WaffoApiKey, setting.WaffoPrivateKey, setting.WaffoPublicCert = originalWaffo[0], originalWaffo[1], originalWaffo[2]
		setting.WaffoSandbox = originalWaffoSandbox
		setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey, setting.WaffoPancakeProductID = originalPancake[0], originalPancake[1], originalPancake[2]
		operation_setting.EpayEnabled, setting.StripeEnabled, setting.CreemEnabled, setting.WaffoEnabled, setting.WaffoPancakeEnabled =
			originalSwitches[0], originalSwitches[1], originalSwitches[2], originalSwitches[3], originalSwitches[4]
	})
	operation_setting.PayAddress, operation_setting.EpayId, operation_setting.EpayKey = "https://pay.example.com", "epay_id", "epay_key"
	setting.CreemApiKey, setting.CreemProducts, setting.CreemWebhookSecret = "creem_api_key", `[{"productId":"prod_123"}]`, "creem_secret"
	setting.WaffoSandbox = false
	setting.WaffoApiKey, setting.WaffoPrivateKey, setting.WaffoPublicCert = "api", "private", "public"
	setting.WaffoPancakeMerchantID, setting.WaffoPancakePrivateKey, setting.WaffoPancakeProductID = "merchant", "private", "product"
	operation_setting.EpayEnabled, setting.StripeEnabled, setting.CreemEnabled, setting.WaffoEnabled, setting.WaffoPancakeEnabled = true, true, true, true, true
}

type topUpInfoForTest struct {
	EnableOnlineTopUp       bool                `json:"enable_online_topup"`
	EnableStripeTopUp       bool                `json:"enable_stripe_topup"`
	EnableCreemTopUp        bool                `json:"enable_creem_topup"`
	EnableWaffoTopUp        bool                `json:"enable_waffo_topup"`
	EnableWaffoPancakeTopUp bool                `json:"enable_waffo_pancake_topup"`
	PayMethods              []map[string]string `json:"pay_methods"`
}

func getTopUpInfoForTest(t *testing.T) (topUpInfoForTest, []string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/user/topup/info", nil)
	GetTopUpInfo(c)
	var body struct {
		Data topUpInfoForTest `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &body))
	types := make([]string, 0, len(body.Data.PayMethods))
	for _, method := range body.Data.PayMethods {
		types = append(types, method["type"])
	}
	return body.Data, types
}

func TestGetTopUpInfoHidesSwitchedOffGatewaysAndMethods(t *testing.T) {
	payMethods := []map[string]string{
		{"name": "Alipay", "type": "alipay"},
		{"name": "WeChat", "type": "wxpay", "provider": "stripe"},
		{"name": "Custom", "type": "custom1", "enabled": "false"},
		{"name": "Custom 2", "type": "custom2", "enabled": "true"},
	}
	cases := []struct {
		name         string
		switchOff    func()
		methods      []map[string]string
		wantTypes    []string
		wantDisabled []string
	}{
		{
			name:      "legacy config without switches keeps every configured gateway",
			switchOff: func() {},
			wantTypes: []string{"alipay", "wxpay", "custom2", "stripe", model.PaymentMethodWaffoPancake, model.PaymentMethodWaffo},
		},
		{
			name:         "epay switched off hides epay methods only",
			switchOff:    func() { operation_setting.EpayEnabled = false },
			wantTypes:    []string{"wxpay", "stripe", model.PaymentMethodWaffoPancake, model.PaymentMethodWaffo},
			wantDisabled: []string{"epay"},
		},
		{
			name:         "stripe switched off hides stripe and stripe-routed methods",
			switchOff:    func() { setting.StripeEnabled = false },
			wantTypes:    []string{"alipay", "custom2", model.PaymentMethodWaffoPancake, model.PaymentMethodWaffo},
			wantDisabled: []string{"stripe"},
		},
		{
			name:         "creem switched off",
			switchOff:    func() { setting.CreemEnabled = false },
			wantTypes:    []string{"alipay", "wxpay", "custom2", "stripe", model.PaymentMethodWaffoPancake, model.PaymentMethodWaffo},
			wantDisabled: []string{"creem"},
		},
		{
			name:         "waffo switched off",
			switchOff:    func() { setting.WaffoEnabled = false },
			wantTypes:    []string{"alipay", "wxpay", "custom2", "stripe", model.PaymentMethodWaffoPancake},
			wantDisabled: []string{"waffo"},
		},
		{
			name:         "waffo pancake switched off",
			switchOff:    func() { setting.WaffoPancakeEnabled = false },
			wantTypes:    []string{"alipay", "wxpay", "custom2", "stripe", model.PaymentMethodWaffo},
			wantDisabled: []string{"waffo_pancake"},
		},
		{
			name:      "switched-off built-in entries hide the built-in methods",
			switchOff: func() {},
			methods: append(payMethods,
				map[string]string{"name": "Stripe", "type": "stripe", "enabled": "false"},
				map[string]string{"name": "Waffo Pancake", "type": "waffo_pancake", "enabled": "false"},
				map[string]string{"name": "Creem", "type": "creem", "enabled": "false"},
			),
			wantTypes:    []string{"alipay", "wxpay", "custom2", model.PaymentMethodWaffo},
			wantDisabled: []string{"waffo_pancake", "creem"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			methods := payMethods
			if tc.methods != nil {
				methods = tc.methods
			}
			configurePaymentGatewaysForTest(t, methods)
			tc.switchOff()

			info, types := getTopUpInfoForTest(t)

			assert.Equal(t, tc.wantTypes, types)
			disabled := make([]string, 0)
			for gateway, enabled := range map[string]bool{
				"epay": info.EnableOnlineTopUp, "stripe": info.EnableStripeTopUp, "creem": info.EnableCreemTopUp,
				"waffo": info.EnableWaffoTopUp, "waffo_pancake": info.EnableWaffoPancakeTopUp,
			} {
				if !enabled {
					disabled = append(disabled, gateway)
				}
			}
			assert.ElementsMatch(t, tc.wantDisabled, disabled)
		})
	}
}

func TestOrderEndpointsRejectSwitchedOffGateways(t *testing.T) {
	cases := []struct {
		name      string
		switchOff func()
		handler   gin.HandlerFunc
		body      string
	}{
		{"epay wallet", func() { operation_setting.EpayEnabled = false }, RequestEpay, `{"amount":10,"payment_method":"alipay"}`},
		{"epay subscription", func() { operation_setting.EpayEnabled = false }, SubscriptionRequestEpay, `{"plan_id":1,"payment_method":"alipay"}`},
		{"stripe wallet", func() { setting.StripeEnabled = false }, RequestStripePay, `{"amount":10,"payment_method":"stripe"}`},
		{"stripe subscription", func() { setting.StripeEnabled = false }, SubscriptionRequestStripePay, `{"plan_id":1}`},
		{"creem wallet", func() { setting.CreemEnabled = false }, RequestCreemPay, `{"product_id":"prod_123","payment_method":"creem"}`},
		{"creem subscription", func() { setting.CreemEnabled = false }, SubscriptionRequestCreemPay, `{"plan_id":1}`},
		{"waffo pancake wallet", func() { setting.WaffoPancakeEnabled = false }, RequestWaffoPancakePay, `{"amount":10}`},
		{"waffo pancake subscription", func() { setting.WaffoPancakeEnabled = false }, SubscriptionRequestWaffoPancakePay, `{"plan_id":1}`},
		{
			"waffo wallet with its pay method switched off",
			func() {
				operation_setting.PayMethods = []map[string]string{{"name": "Waffo", "type": "waffo", "enabled": "false"}}
			},
			RequestWaffoPay, `{"amount":10,"pay_method_index":0}`,
		},
		{
			"creem wallet with its pay method switched off",
			func() {
				operation_setting.PayMethods = []map[string]string{{"name": "Creem", "type": "creem", "enabled": "false"}}
			},
			RequestCreemPay, `{"product_id":"prod_123","payment_method":"creem"}`,
		},
		{
			"creem subscription with its pay method switched off",
			func() {
				operation_setting.PayMethods = []map[string]string{{"name": "Creem", "type": "creem", "enabled": "false"}}
			},
			SubscriptionRequestCreemPay, `{"plan_id":1}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configurePaymentGatewaysForTest(t, []map[string]string{{"name": "Alipay", "type": "alipay"}})
			// The switch is checked before anything is read or written: a nil
			// database makes any attempt to create an order fail the test.
			originalDB := model.DB
			t.Cleanup(func() { model.DB = originalDB })
			model.DB = nil
			tc.switchOff()

			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/pay", bytes.NewBufferString(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("id", 1)
			require.NotPanics(t, func() { tc.handler(c) })

			assert.Contains(t, recorder.Body.String(), paymentChannelDisabled)
		})
	}
}

func TestOrderEndpointsRejectSwitchedOffPayMethods(t *testing.T) {
	setupSubscriptionEpayTest(t, 7.3)
	plan := createEpayTestPlan(t, 10)
	configurePaymentGatewaysForTest(t, []map[string]string{
		{"name": "Alipay", "type": "alipay", "enabled": "false"},
		{"name": "WeChat", "type": "wxpay", "provider": "stripe", "enabled": "false"},
		{"name": "Stripe", "type": "stripe", "enabled": "false"},
	})
	gin.SetMode(gin.TestMode)
	post := func(handler gin.HandlerFunc, body string) string {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/pay", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("id", 1)
		handler(c)
		return recorder.Body.String()
	}
	planID := common.Interface2String(plan.Id)

	assert.Contains(t, post(RequestEpay, `{"amount":10,"payment_method":"alipay"}`), "Payment method does not exist")
	assert.Contains(t, post(SubscriptionRequestEpay, `{"plan_id":`+planID+`,"payment_method":"alipay"}`), "Payment method does not exist")
	assert.Contains(t, post(RequestStripePay, `{"amount":10,"payment_method":"wxpay"}`), "Unsupported payment channel")
	assert.Contains(t, post(RequestStripePay, `{"amount":10,"payment_method":"stripe"}`), "Unsupported payment channel")
	assert.Contains(t, post(SubscriptionRequestStripePay, `{"plan_id":`+planID+`,"payment_method":"wxpay"}`), "Unsupported payment channel")
	assert.Contains(t, post(SubscriptionRequestStripePay, `{"plan_id":`+planID+`}`), "Unsupported payment channel")

	var orders int64
	require.NoError(t, model.DB.Model(&model.TopUp{}).Count(&orders).Error)
	assert.Zero(t, orders)
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Count(&orders).Error)
	assert.Zero(t, orders)
}

func TestWebhooksIgnoreGatewayAndPayMethodSwitches(t *testing.T) {
	configurePaymentGatewaysForTest(t, []map[string]string{
		{"name": "Alipay", "type": "alipay", "enabled": "false"},
		{"name": "Creem", "type": "creem", "enabled": "false"},
	})
	operation_setting.EpayEnabled, setting.StripeEnabled, setting.CreemEnabled, setting.WaffoEnabled, setting.WaffoPancakeEnabled = false, false, false, false, false

	assert.False(t, isEpayTopUpEnabled())
	assert.False(t, isStripeTopUpEnabled())
	assert.False(t, isCreemTopUpEnabled())
	assert.False(t, isWaffoTopUpEnabled())
	assert.False(t, isWaffoPancakeTopUpEnabled())

	assert.True(t, isEpayWebhookEnabled())
	assert.True(t, isStripeWebhookEnabled())
	assert.True(t, isCreemWebhookEnabled())
	assert.True(t, isWaffoWebhookEnabled())
	assert.True(t, isWaffoPancakeWebhookEnabled())
}

func TestEpayCallbacksCompleteExistingOrdersAfterSwitchOff(t *testing.T) {
	setupSubscriptionEpayTest(t, 7.3)
	plan := createEpayTestPlan(t, 10)
	require.NoError(t, (&model.SubscriptionOrder{
		UserId: 1, PlanId: plan.Id, Money: 73, TradeNo: testEpaySubscriptionNo,
		PaymentMethod: "alipay", PaymentProvider: model.PaymentProviderEpay, Status: common.TopUpStatusPending,
	}).Insert())
	const walletTradeNo = "USR1NOWALLETSWITCHOFF"
	require.NoError(t, model.DB.Create(&model.TopUp{
		UserId: 1, Amount: 10, Money: 73, TradeNo: walletTradeNo,
		PaymentMethod: "alipay", PaymentProvider: model.PaymentProviderEpay, Status: common.TopUpStatusPending,
	}).Error)

	// The administrator switches Epay and its only method off after both
	// orders were placed; the gateway still settles them.
	originalEpayEnabled := operation_setting.EpayEnabled
	t.Cleanup(func() { operation_setting.EpayEnabled = originalEpayEnabled })
	operation_setting.EpayEnabled = false
	operation_setting.PayMethods = []map[string]string{{"name": "Alipay", "type": "alipay", "enabled": "false"}}

	assert.Equal(t, "success", postEpayForm(t, SubscriptionEpayNotify, "/api/subscription/epay/notify", signedEpayCallback(t, "73.00")))
	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", testEpaySubscriptionNo).First(&order).Error)
	assert.Equal(t, common.TopUpStatusSuccess, order.Status)

	walletCallback, err := epay.GenerateParamsWithError(map[string]string{
		"pid": "1000", "trade_no": "EPAYWALLET1", "out_trade_no": walletTradeNo, "type": "alipay",
		"name": "TUC10", "money": "73.00", "trade_status": epay.StatusTradeSuccess,
	}, testEpayKey)
	require.NoError(t, err)
	assert.Equal(t, "success", postEpayForm(t, EpayNotify, "/api/user/epay/notify", walletCallback))
	topUp := model.GetTopUpByTradeNo(walletTradeNo)
	require.NotNil(t, topUp)
	assert.Equal(t, common.TopUpStatusSuccess, topUp.Status)
}

func TestOptionUpdateAuditRecordsPaymentSwitchChanges(t *testing.T) {
	admin, _ := setupAccessTokenAudit(t)
	configurePaymentGatewaysForTest(t, []map[string]string{
		{"name": "Alipay", "type": "alipay"},
		{"name": "WeChat", "type": "wxpay", "enabled": "false"},
		{"name": "Custom", "type": "custom1"},
	})
	common.OptionMapRWMutex.Lock()
	originalOptionMap := maps.Clone(common.OptionMap)
	if common.OptionMap == nil {
		common.OptionMap = map[string]string{}
	}
	common.OptionMap["StripeEnabled"] = "true"
	common.OptionMap["PayMethods"] = operation_setting.PayMethods2JsonString()
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		common.OptionMap = originalOptionMap
		common.OptionMapRWMutex.Unlock()
	})

	update := func(body string) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/option/", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("id", admin.Id)
		c.Set("role", common.RoleRootUser)
		UpdateOption(c)
		require.Contains(t, recorder.Body.String(), `"success":true`)
		var audit model.AuditLog
		require.NoError(t, model.LOG_DB.Where("action = ?", "option.update").Order("id desc").First(&audit).Error)
		require.NotNil(t, audit.Other.Op)
		raw, err := common.Marshal(audit.Other.Op.Params)
		require.NoError(t, err)
		var params map[string]any
		require.NoError(t, common.Unmarshal(raw, &params))
		return params
	}

	params := update(`{"key":"StripeEnabled","value":false}`)
	assert.Equal(t, "StripeEnabled", params["key"])
	assert.Equal(t, "true", params["old_value"])
	assert.Equal(t, "false", params["new_value"])
	assert.False(t, setting.StripeEnabled)

	// Only the switches that changed are recorded, by type and name; a method
	// that was added or kept its state is not.
	params = update(`{"key":"PayMethods","value":"[{\"name\":\"Alipay\",\"type\":\"alipay\",\"enabled\":\"false\"},{\"name\":\"WeChat\",\"type\":\"wxpay\"},{\"name\":\"Custom\",\"type\":\"custom1\"},{\"name\":\"New\",\"type\":\"custom2\",\"enabled\":\"false\"}]"}`)
	assert.Equal(t, "PayMethods", params["key"])
	assert.Equal(t, []any{
		map[string]any{"type": "alipay", "name": "Alipay", "old_value": "true", "new_value": "false"},
		map[string]any{"type": "wxpay", "name": "WeChat", "old_value": "false", "new_value": "true"},
	}, params["pay_method_enabled_changes"])
	assert.NotContains(t, params, "new_value")

	// Secret options keep recording only their key.
	params = update(`{"key":"StripeApiSecret","value":"sk_test_new_secret"}`)
	assert.Equal(t, map[string]any{"key": "StripeApiSecret"}, params)
}
