package system_setting

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
)

// alternateServerAddresses holds the normalized origins of the extra website
// lines users may open besides ServerAddress. Only these exact origins may
// replace ServerAddress in links and OAuth redirects built for a request.
// Requests read it while the option sync replaces it, hence the atomic pointer.
var alternateServerAddresses atomic.Pointer[[]string]

// AlternateServerAddresses returns the configured alternate origins. The
// returned slice must not be modified.
func AlternateServerAddresses() []string {
	if addresses := alternateServerAddresses.Load(); addresses != nil {
		return *addresses
	}
	return nil
}

// SetAlternateServerAddresses replaces the alternate origins with the result
// of ParseAlternateServerAddresses.
func SetAlternateServerAddresses(addresses []string) {
	alternateServerAddresses.Store(&addresses)
}

// IsAlternateServerAddress reports whether origin is exactly one of the
// configured alternate origins. ServerAddress itself is not one of them.
func IsAlternateServerAddress(origin string) bool {
	return slices.Contains(AlternateServerAddresses(), origin)
}

// ParseAlternateServerAddresses accepts origins separated by commas or line
// breaks. Paths and wildcards are rejected and duplicates dropped, so a
// request can only select an address the administrator listed verbatim.
func ParseAlternateServerAddresses(value string) ([]string, error) {
	addresses := []string{}
	for _, item := range strings.FieldsFunc(value, func(c rune) bool { return c == ',' || c == '\n' || c == '\r' }) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		origin, err := common.NormalizeOrigin(item)
		if err != nil {
			return nil, fmt.Errorf("invalid alternate server address %q: %w", item, err)
		}
		if !slices.Contains(addresses, origin) {
			addresses = append(addresses, origin)
		}
	}
	return addresses, nil
}

// MatchServerAddress returns the configured website address whose origin is
// origin: ServerAddress itself (keeping any configured path) or one of the
// alternate addresses. It returns "" when origin is not configured.
func MatchServerAddress(origin string) string {
	origin, err := common.NormalizeOrigin(origin)
	if err != nil {
		return ""
	}
	primary := strings.TrimRight(ServerAddress, "/")
	if parsed, err := url.Parse(primary); err == nil {
		if primaryOrigin, err := common.NormalizeOrigin(parsed.Scheme + "://" + parsed.Host); err == nil && primaryOrigin == origin {
			return primary
		}
	}
	if IsAlternateServerAddress(origin) {
		return origin
	}
	return ""
}

// IsConfiguredServerAddress reports whether address is exactly ServerAddress
// or one of the alternate addresses, ignoring a trailing slash.
func IsConfiguredServerAddress(address string) bool {
	address = strings.TrimRight(address, "/")
	return address != "" && (address == strings.TrimRight(ServerAddress, "/") || IsAlternateServerAddress(address))
}
