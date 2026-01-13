package shannon

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

// ErrDomain is a fallback domain value used when domain extraction fails
const ErrDomain = "error"

// ExtractDomainOrHost extracts the effective TLD+1 from a URL.
// It falls back to a reasonable domain extraction for localhost, IP addresses, and other non-standard hosts.
// Note: Callers should log the error with full context (supplier, service_id, etc.)
func ExtractDomainOrHost(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("empty URL")
	}

	// If the URL doesn't have a scheme, url.Parse will treat it as a path
	// Prepend a dummy scheme to ensure proper parsing
	urlToParse := rawURL
	if !strings.Contains(rawURL, "://") {
		urlToParse = "https://" + rawURL
	}

	parsedURL, err := url.Parse(urlToParse)
	if err != nil {
		return "", fmt.Errorf("malformed URL: %w", err)
	}

	host := parsedURL.Hostname()
	if host == "" {
		return "", fmt.Errorf("empty host in URL: %q", rawURL)
	}

	// Check special cases first (IP addresses, localhost, internal domains)
	// These don't need TLD+1 extraction
	if ip := net.ParseIP(host); ip != nil {
		return host, nil // Return the IP as-is
	}
	if isLocalhost(host) {
		return host, nil
	}
	if isPrivateOrInternalDomain(host) {
		return host, nil
	}

	// Try to get effective TLD+1 for public domains
	etld, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err == nil {
		return etld, nil
	}

	// Fallback for cases where publicsuffix fails (e.g., unknown TLDs)
	return fallbackDomainExtraction(host)
}

// fallbackDomainExtraction handles cases where publicsuffix.EffectiveTLDPlusOne fails.
// Note: IP addresses, localhost, and internal domains are already handled before this is called.
func fallbackDomainExtraction(host string) (string, error) {
	parts := strings.Split(host, ".")

	// Single hostname without dots (like "relayminer1")
	if len(parts) == 1 {
		return host, nil
	}

	// If it has dots but publicsuffix failed, take the last two parts
	// This is a reasonable fallback for unknown TLDs
	if len(parts) >= 2 {
		return strings.Join(parts[len(parts)-2:], "."), nil
	}

	return host, nil
}

// isLocalhost checks if the host is a localhost variant
func isLocalhost(host string) bool {
	lowercase := strings.ToLower(host)
	return lowercase == "localhost" ||
		lowercase == "localhost.localdomain" ||
		strings.HasPrefix(lowercase, "localhost.")
}

// isPrivateOrInternalDomain checks for private/internal domain patterns
func isPrivateOrInternalDomain(host string) bool {
	lowercase := strings.ToLower(host)

	// Common internal TLDs
	internalTLDs := []string{".local", ".internal", ".corp", ".home", ".lan"}
	for _, tld := range internalTLDs {
		if strings.HasSuffix(lowercase, tld) {
			return true
		}
	}

	// Single label domains (no dots) are typically internal
	return !strings.Contains(host, ".")
}
