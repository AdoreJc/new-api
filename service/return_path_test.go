package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentReturnURLUsesSuppliedDefaultDashboardPath(t *testing.T) {
	previousAddress := system_setting.ServerAddress
	system_setting.ServerAddress = "https://dashboard.example.com/"
	t.Cleanup(func() { system_setting.ServerAddress = previousAddress })

	assert.Equal(t, "https://dashboard.example.com/wallet", PaymentReturnURL("/wallet"))
}

func TestParseAlternateServerAddresses(t *testing.T) {
	addresses, err := system_setting.ParseAlternateServerAddresses(" https://Line2.Example.com/ ,\nhttps://line3.example.com:443\r\nhttp://10.0.0.8:3000,https://line2.example.com")
	require.NoError(t, err)
	assert.Equal(t, []string{"https://line2.example.com", "https://line3.example.com", "http://10.0.0.8:3000"}, addresses)

	for _, invalid := range []string{"line2.example.com", "https://line2.example.com/app", "https://*.example.com", "ftp://line2.example.com"} {
		_, err := system_setting.ParseAlternateServerAddresses(invalid)
		assert.Error(t, err, invalid)
	}
}

func TestRequestServerAddressOnlySelectsConfiguredLines(t *testing.T) {
	previousAddress, previousAlternates := system_setting.ServerAddress, system_setting.AlternateServerAddresses
	system_setting.ServerAddress = "https://main.example.com/console/"
	system_setting.AlternateServerAddresses = []string{"https://line2.example.com", "http://10.0.0.8:3000"}
	t.Cleanup(func() {
		system_setting.ServerAddress, system_setting.AlternateServerAddresses = previousAddress, previousAlternates
	})

	tests := []struct {
		name     string
		host     string
		origin   string
		referer  string
		expected string
	}{
		{name: "origin of alternate line", host: "internal:3000", origin: "https://line2.example.com", expected: "https://line2.example.com"},
		{name: "referer of alternate line", host: "internal:3000", referer: "https://line2.example.com/user/reset", expected: "https://line2.example.com"},
		{name: "host of alternate line", host: "line2.example.com", expected: "https://line2.example.com"},
		{name: "http alternate line with port", host: "10.0.0.8:3000", expected: "http://10.0.0.8:3000"},
		{name: "primary keeps its path", host: "main.example.com", expected: "https://main.example.com/console"},
		{name: "unconfigured origin falls back to host", host: "line2.example.com", origin: "https://attacker.example", expected: "https://line2.example.com"},
		{name: "unconfigured host falls back to primary", host: "attacker.example", expected: "https://main.example.com/console"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/reset_password", nil)
			request.Host = test.host
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			if test.referer != "" {
				request.Header.Set("Referer", test.referer)
			}
			assert.Equal(t, test.expected, RequestServerAddress(request))
		})
	}
}
