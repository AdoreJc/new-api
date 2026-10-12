package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	testEpayKey            = "subscription-epay-test-key"
	testEpaySubscriptionNo = "SUBUSR1NOTEST123"
)

// setupSubscriptionEpayTest wires an in-memory database and the settings the
// Epay subscription handlers read: price per USD, Epay credentials, callback
// address, payment compliance and the available payment methods.
func setupSubscriptionEpayTest(t *testing.T, pricePerUSD float64) {
	t.Helper()
	originalDB, originalLogDB := model.DB, model.LOG_DB
	originalMainDatabaseType, originalLogDatabaseType := common.MainDatabaseType(), common.LogDatabaseType()
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	// Shared-cache in-memory database: order completion reads the plan outside
	// the completing transaction, so the fixture needs more than one connection.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	require.NoError(t, database.AutoMigrate(
		&model.User{},
		&model.SubscriptionPlan{},
		&model.SubscriptionOrder{},
		&model.UserSubscription{},
		&model.TopUp{},
		&model.Log{},
	))
	model.DB, model.LOG_DB = database, database

	originalRedisEnabled := common.RedisEnabled
	originalPrice := operation_setting.Price
	originalEpayId := operation_setting.EpayId
	originalEpayKey := operation_setting.EpayKey
	originalPayAddress := operation_setting.PayAddress
	originalPayMethods := operation_setting.PayMethods
	originalServerAddress := system_setting.ServerAddress
	paymentSetting := operation_setting.GetPaymentSetting()
	originalPaymentSetting := *paymentSetting

	common.RedisEnabled = false
	operation_setting.Price = pricePerUSD
	operation_setting.EpayId = "1000"
	operation_setting.EpayKey = testEpayKey
	operation_setting.PayAddress = "https://epay.example.com"
	operation_setting.PayMethods = []map[string]string{{"name": "Alipay", "type": "alipay"}}
	system_setting.ServerAddress = "https://api.example.com"
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion

	t.Cleanup(func() {
		model.DB, model.LOG_DB = originalDB, originalLogDB
		common.SetDatabaseTypes(originalMainDatabaseType, originalLogDatabaseType)
		_ = sqlDB.Close()
		common.RedisEnabled = originalRedisEnabled
		operation_setting.Price = originalPrice
		operation_setting.EpayId = originalEpayId
		operation_setting.EpayKey = originalEpayKey
		operation_setting.PayAddress = originalPayAddress
		operation_setting.PayMethods = originalPayMethods
		system_setting.ServerAddress = originalServerAddress
		*paymentSetting = originalPaymentSetting
	})
}

func createEpayTestPlan(t *testing.T, priceUSD float64) *model.SubscriptionPlan {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.User{Id: 1, Username: "epay-user", Group: "default", Status: common.UserStatusEnabled}).Error)
	plan := &model.SubscriptionPlan{
		Title:         "Pro",
		PriceAmount:   priceUSD,
		DurationUnit:  "month",
		DurationValue: 1,
		Enabled:       true,
		TotalAmount:   1000,
	}
	plan.NormalizeDefaults()
	require.NoError(t, model.DB.Create(plan).Error)
	model.InvalidateSubscriptionPlanCache(plan.Id)
	return plan
}

func postEpayForm(t *testing.T, handler gin.HandlerFunc, path string, params map[string]string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handler(c)
	return recorder.Body.String()
}

func signedEpayCallback(t *testing.T, money string) map[string]string {
	t.Helper()
	params := map[string]string{
		"pid":          "1000",
		"trade_no":     "EPAY20261105",
		"out_trade_no": testEpaySubscriptionNo,
		"type":         "alipay",
		"name":         "SUB:Pro",
		"money":        money,
		"trade_status": epay.StatusTradeSuccess,
	}
	signed, err := epay.GenerateParamsWithError(params, testEpayKey)
	require.NoError(t, err)
	return signed
}

func TestPayMoneyFromUSD_UsesPricePerUSD(t *testing.T) {
	originalPrice := operation_setting.Price
	t.Cleanup(func() { operation_setting.Price = originalPrice })

	testCases := []struct {
		name        string
		pricePerUSD float64
		usd         float64
		expected    string
	}{
		{name: "default CNY price converts 10 USD to 73.00", pricePerUSD: 7.3, usd: 10, expected: "73.00"},
		{name: "price of 1 keeps the USD amount", pricePerUSD: 1, usd: 10, expected: "10.00"},
		{name: "fractional result rounds to cents", pricePerUSD: 7.333, usd: 1, expected: "7.33"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			operation_setting.Price = tc.pricePerUSD
			money := payMoneyFromUSD(decimal.NewFromFloat(tc.usd)).Round(2)
			assert.Equal(t, tc.expected, money.StringFixed(2))
		})
	}
}

func TestSubscriptionRequestEpay_ChargesConvertedMoney(t *testing.T) {
	setupSubscriptionEpayTest(t, 7.3)
	plan := createEpayTestPlan(t, 10)

	envelope := postJSON(t, SubscriptionRequestEpay, "/api/subscription/pay/epay", `{"plan_id":1,"payment_method":"alipay"}`, 1, nil)
	require.Equal(t, "success", envelope.Message)
	assert.Equal(t, "73.00", envelope.Data["money"])
	assert.Equal(t, "https://api.example.com/api/subscription/epay/notify", envelope.Data["notify_url"])
	assert.NotEmpty(t, envelope.Data["sign"])

	var order model.SubscriptionOrder
	require.NoError(t, model.DB.Where("plan_id = ?", plan.Id).First(&order).Error)
	assert.Equal(t, common.TopUpStatusPending, order.Status)
	assert.Equal(t, model.PaymentProviderEpay, order.PaymentProvider)
	assert.InDelta(t, 73, order.Money, 1e-9)
	assert.Equal(t, order.TradeNo, envelope.Data["out_trade_no"])
}

func TestSubscriptionRequestEpay_PriceOfOneKeepsUSDAmount(t *testing.T) {
	setupSubscriptionEpayTest(t, 1)
	createEpayTestPlan(t, 10)

	envelope := postJSON(t, SubscriptionRequestEpay, "/api/subscription/pay/epay", `{"plan_id":1,"payment_method":"alipay"}`, 1, nil)
	require.Equal(t, "success", envelope.Message)
	assert.Equal(t, "10.00", envelope.Data["money"])
}

func TestSubscriptionRequestEpay_RejectsConvertedAmountBelowOneCent(t *testing.T) {
	setupSubscriptionEpayTest(t, 7.3)
	createEpayTestPlan(t, 0.001)

	envelope := postJSON(t, SubscriptionRequestEpay, "/api/subscription/pay/epay", `{"plan_id":1,"payment_method":"alipay"}`, 1, nil)
	assert.False(t, envelope.Success)
	assert.Equal(t, "Plan amount is too low", envelope.Message)

	var count int64
	require.NoError(t, model.DB.Model(&model.SubscriptionOrder{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestSubscriptionEpayNotify_VerifiesCallbackMoney(t *testing.T) {
	setupSubscriptionEpayTest(t, 7.3)
	plan := createEpayTestPlan(t, 10)
	order := &model.SubscriptionOrder{
		UserId:          1,
		PlanId:          plan.Id,
		Money:           73,
		TradeNo:         testEpaySubscriptionNo,
		PaymentMethod:   "alipay",
		PaymentProvider: model.PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, order.Insert())

	// A signed callback carrying the un-converted USD price must not complete the order.
	body := postEpayForm(t, SubscriptionEpayNotify, "/api/subscription/epay/notify", signedEpayCallback(t, "10.00"))
	assert.Equal(t, "fail", body)
	var stored model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", testEpaySubscriptionNo).First(&stored).Error)
	assert.Equal(t, common.TopUpStatusPending, stored.Status)
	var subscriptions int64
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Count(&subscriptions).Error)
	assert.Zero(t, subscriptions)

	// The gateway reports the converted amount: the order completes and the
	// mirrored top-up row and log carry the same amount.
	body = postEpayForm(t, SubscriptionEpayNotify, "/api/subscription/epay/notify", signedEpayCallback(t, "73.00"))
	assert.Equal(t, "success", body)
	require.NoError(t, model.DB.Where("trade_no = ?", testEpaySubscriptionNo).First(&stored).Error)
	assert.Equal(t, common.TopUpStatusSuccess, stored.Status)
	assert.InDelta(t, 73, stored.Money, 1e-9)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Count(&subscriptions).Error)
	assert.EqualValues(t, 1, subscriptions)
	var topup model.TopUp
	require.NoError(t, model.DB.Where("trade_no = ?", testEpaySubscriptionNo).First(&topup).Error)
	assert.InDelta(t, 73, topup.Money, 1e-9)
	var log model.Log
	require.NoError(t, model.DB.Where("user_id = ? AND type = ?", 1, model.LogTypeTopup).First(&log).Error)
	assert.Contains(t, log.Content, "73.00")

	// A duplicate callback with the right amount stays idempotent.
	body = postEpayForm(t, SubscriptionEpayNotify, "/api/subscription/epay/notify", signedEpayCallback(t, "73.00"))
	assert.Equal(t, "success", body)
	require.NoError(t, model.DB.Model(&model.UserSubscription{}).Count(&subscriptions).Error)
	assert.EqualValues(t, 1, subscriptions)
}

func TestSubscriptionEpayReturn_RedirectsToFailOnMoneyMismatch(t *testing.T) {
	setupSubscriptionEpayTest(t, 7.3)
	plan := createEpayTestPlan(t, 10)
	order := &model.SubscriptionOrder{
		UserId:          1,
		PlanId:          plan.Id,
		Money:           73,
		TradeNo:         testEpaySubscriptionNo,
		PaymentMethod:   "alipay",
		PaymentProvider: model.PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, order.Insert())

	gin.SetMode(gin.TestMode)
	form := url.Values{}
	for key, value := range signedEpayCallback(t, "10.00") {
		form.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/subscription/epay/return?"+form.Encode(), nil)
	SubscriptionEpayReturn(c)
	assert.Equal(t, http.StatusFound, recorder.Code)
	assert.Contains(t, recorder.Header().Get("Location"), "pay=fail")

	var stored model.SubscriptionOrder
	require.NoError(t, model.DB.Where("trade_no = ?", testEpaySubscriptionNo).First(&stored).Error)
	assert.Equal(t, common.TopUpStatusPending, stored.Status)
}

func TestSubscriptionEpayReturnFollowsAlternateLineAndNotifyStays(t *testing.T) {
	setupSubscriptionEpayTest(t, 7.3)
	createEpayTestPlan(t, 10)
	previousAlternates := system_setting.AlternateServerAddresses
	system_setting.AlternateServerAddresses = []string{"https://line2.example.com"}
	t.Cleanup(func() { system_setting.AlternateServerAddresses = previousAlternates })
	gin.SetMode(gin.TestMode)

	for _, test := range []struct {
		name     string
		host     string
		origin   string
		expected string
	}{
		{name: "alternate line", host: "line2.example.com", origin: "https://line2.example.com", expected: "https://line2.example.com"},
		{name: "primary line", host: "api.example.com", origin: "https://api.example.com", expected: "https://api.example.com"},
		{name: "unconfigured host and origin", host: "attacker.example", origin: "https://attacker.example", expected: "https://api.example.com"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/subscription/pay/epay", strings.NewReader(`{"plan_id":1,"payment_method":"alipay"}`))
			c.Request.Host = test.host
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("Origin", test.origin)
			c.Set("id", 1)
			SubscriptionRequestEpay(c)
			var envelope apiEnvelope
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &envelope))
			require.Equal(t, "success", envelope.Message)
			assert.Equal(t, test.expected+"/api/subscription/epay/return", envelope.Data["return_url"])
			assert.Equal(t, "https://api.example.com/api/subscription/epay/notify", envelope.Data["notify_url"])

			// The return handler sends the browser to the console of the line it came back through.
			recorder = httptest.NewRecorder()
			c, _ = gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/subscription/epay/return", nil)
			c.Request.Host = test.host
			SubscriptionEpayReturn(c)
			assert.Equal(t, test.expected+"/wallet?pay=fail", recorder.Header().Get("Location"))
		})
	}

	// A custom callback address still receives the return of every non-alternate request.
	previousCallback := operation_setting.CustomCallbackAddress
	operation_setting.CustomCallbackAddress = "https://callback.example.com"
	t.Cleanup(func() { operation_setting.CustomCallbackAddress = previousCallback })
	envelope := postJSON(t, SubscriptionRequestEpay, "/api/subscription/pay/epay", `{"plan_id":1,"payment_method":"alipay"}`, 1, nil)
	assert.Equal(t, "https://callback.example.com/api/subscription/epay/return", envelope.Data["return_url"])
	assert.Equal(t, "https://callback.example.com/api/subscription/epay/notify", envelope.Data["notify_url"])
}
