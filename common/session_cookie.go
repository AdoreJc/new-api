package common

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// NormalizeOrigin validates and canonicalizes a browser origin. Only an exact
// scheme/host/effective-port match is meaningful; paths and wildcards are not
// accepted for authentication cookie endpoints.
func NormalizeOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" || strings.ContainsAny(raw, "\r\n") {
		return "", fmt.Errorf("origin is empty or invalid")
	}
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid origin: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return "", fmt.Errorf("origin scheme must be http or https")
	}
	if parsedURL.Host == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" || (parsedURL.Path != "" && parsedURL.Path != "/") {
		return "", fmt.Errorf("origin must contain only scheme and host")
	}
	hostname := strings.ToLower(parsedURL.Hostname())
	if hostname == "" || strings.Contains(hostname, "*") {
		return "", fmt.Errorf("origin host is empty")
	}
	port := parsedURL.Port()
	normalizedHost := hostname
	if strings.Contains(hostname, ":") {
		normalizedHost = "[" + hostname + "]"
	}
	if port == "" || (parsedURL.Scheme == "http" && port == "80") || (parsedURL.Scheme == "https" && port == "443") {
		return parsedURL.Scheme + "://" + normalizedHost, nil
	}
	return parsedURL.Scheme + "://" + net.JoinHostPort(hostname, port), nil
}

// RequestBrowserOrigin returns the normalized origin of the page that sent a
// browser request, from a single Origin header or else a single Referer.
func RequestBrowserOrigin(request *http.Request) (string, bool) {
	originValues := request.Header.Values("Origin")
	if len(originValues) > 1 {
		return "", false
	}
	if len(originValues) == 1 {
		if strings.Contains(originValues[0], ",") {
			return "", false
		}
		origin, err := NormalizeOrigin(originValues[0])
		return origin, err == nil
	}
	refererValues := request.Header.Values("Referer")
	if len(refererValues) != 1 {
		return "", false
	}
	referer, err := url.Parse(strings.TrimSpace(refererValues[0]))
	if err != nil || referer.Scheme == "" || referer.Host == "" || referer.User != nil {
		return "", false
	}
	origin, err := NormalizeOrigin(referer.Scheme + "://" + referer.Host)
	return origin, err == nil
}

func InitSessionCookieSettings() error {
	secureRaw := strings.TrimSpace(os.Getenv("SESSION_COOKIE_SECURE"))
	trustedURLsRaw := strings.TrimSpace(os.Getenv("SESSION_COOKIE_TRUSTED_URL"))

	SessionCookieSecure = false
	SessionCookieTrustedURLs = nil

	if secureRaw == "" || strings.EqualFold(secureRaw, "false") {
		if trustedURLsRaw != "" {
			return fmt.Errorf("SESSION_COOKIE_TRUSTED_URL requires SESSION_COOKIE_SECURE=true")
		}
		return nil
	}

	if !strings.EqualFold(secureRaw, "true") {
		return fmt.Errorf("SESSION_COOKIE_SECURE must be true or false")
	}

	if trustedURLsRaw == "" {
		return fmt.Errorf("SESSION_COOKIE_SECURE=true requires SESSION_COOKIE_TRUSTED_URL")
	}

	trustedURLs := strings.SplitSeq(trustedURLsRaw, ",")
	for trustedURL := range trustedURLs {
		trustedURL = strings.TrimSpace(trustedURL)
		if trustedURL == "" {
			return fmt.Errorf("SESSION_COOKIE_TRUSTED_URL contains an empty URL")
		}
		normalizedOrigin, err := NormalizeOrigin(trustedURL)
		if err != nil {
			return fmt.Errorf("invalid SESSION_COOKIE_TRUSTED_URL: %w", err)
		}
		if !strings.HasPrefix(normalizedOrigin, "https://") {
			return fmt.Errorf("SESSION_COOKIE_TRUSTED_URL must contain only https URLs with hosts")
		}
		SessionCookieTrustedURLs = append(SessionCookieTrustedURLs, normalizedOrigin)
	}

	SessionCookieSecure = true
	return nil
}
