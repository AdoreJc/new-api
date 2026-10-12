package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentReturnPathUsesDefaultDashboardRoutes(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://dashboard.example.com/"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/pay", nil)

	assert.Equal(
		t,
		"https://dashboard.example.com/wallet?pay=success",
		paymentReturnPath(c, "/wallet?pay=success"),
	)
	assert.Equal(
		t,
		"https://dashboard.example.com/usage-logs",
		paymentReturnPath(c, "/usage-logs"),
	)
}

func TestPaymentReturnPathFollowsConfiguredLineOnly(t *testing.T) {
	previousAddress, previousAlternates := system_setting.ServerAddress, system_setting.AlternateServerAddresses
	system_setting.ServerAddress = "https://main.example.com"
	system_setting.AlternateServerAddresses = []string{"https://line2.example.com"}
	t.Cleanup(func() {
		system_setting.ServerAddress, system_setting.AlternateServerAddresses = previousAddress, previousAlternates
	})

	for _, test := range []struct {
		name     string
		host     string
		origin   string
		expected string
	}{
		{name: "alternate line by origin", host: "internal:3000", origin: "https://line2.example.com", expected: "https://line2.example.com/wallet"},
		{name: "alternate line by host", host: "line2.example.com", expected: "https://line2.example.com/wallet"},
		{name: "primary line", host: "main.example.com", origin: "https://main.example.com", expected: "https://main.example.com/wallet"},
		{name: "unconfigured host and origin", host: "attacker.example", origin: "https://attacker.example", expected: "https://main.example.com/wallet"},
		{name: "suffix of a configured host", host: "line2.example.com.attacker.example", expected: "https://main.example.com/wallet"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/api/user/stripe/pay", nil)
			c.Request.Host = test.host
			if test.origin != "" {
				c.Request.Header.Set("Origin", test.origin)
			}
			returnURL := paymentReturnPath(c, "/wallet")
			assert.Equal(t, test.expected, returnURL)

			// Every Stripe Checkout (wallet, recurring plan, one-time plan) sends
			// the browser back to the URL built for the request.
			topUp := newStripeTopUpCheckoutParams("ref", "", "", "alipay", 1, returnURL, returnURL)
			assert.Equal(t, test.expected, *topUp.SuccessURL)
			assert.Equal(t, test.expected, *topUp.CancelURL)
			plan := &model.SubscriptionPlan{Title: "Pro", PriceAmount: 10, StripePriceId: "price_recurring"}
			recurring := newStripeSubscriptionCheckoutParams("sub_ref", "", "", plan.StripePriceId, returnURL)
			assert.Equal(t, test.expected, *recurring.SuccessURL)
			assert.Equal(t, test.expected, *recurring.CancelURL)
			oneTime, err := newStripeSubscriptionPaymentCheckoutParams("sub_ref", "", "", "wxpay", plan, returnURL)
			require.NoError(t, err)
			assert.Equal(t, test.expected, *oneTime.SuccessURL)
			assert.Equal(t, test.expected, *oneTime.CancelURL)
		})
	}
}
