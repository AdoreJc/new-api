package service

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

func PaymentReturnURL(suffix string) string {
	base := strings.TrimRight(system_setting.ServerAddress, "/")
	return base + suffix
}

// RequestServerAddress returns the website address a browser request came
// through, so users stay on the line they opened. The page origin and then the
// Host header are only matched against the configured addresses; anything
// else falls back to ServerAddress, so request headers never inject a host.
func RequestServerAddress(request *http.Request) string {
	if origin, ok := common.RequestBrowserOrigin(request); ok {
		if address := system_setting.MatchServerAddress(origin); address != "" {
			return address
		}
	}
	for _, scheme := range []string{"https://", "http://"} {
		if address := system_setting.MatchServerAddress(scheme + request.Host); address != "" {
			return address
		}
	}
	return strings.TrimRight(system_setting.ServerAddress, "/")
}
