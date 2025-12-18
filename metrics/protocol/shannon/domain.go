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
func ExtractDomainOrHost(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("malformed URL: %w", err)
	}

	host := parsedURL.Hostname()
	if host == "" {
		return "", fmt.Errorf("empty host in URL")
	}

	// Try to get effective TLD+1 first
	etld, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err == nil {
		return etld, nil
	}

	// Fallback cases when publicsuffix fails
	return fallbackDomainExtraction(host)
}

// fallbackDomainExtraction handles cases where publicsuffix.EffectiveTLDPlusOne fails
func fallbackDomainExtraction(host string) (string, error) {
	// Check if it's an IP address
	if ip := net.ParseIP(host); ip != nil {
		return host, nil // Return the IP as-is
	}

	// Check for localhost variants
	if isLocalhost(host) {
		return host, nil
	}

	// Check for private/internal domains (no dots, or .local, etc.)
	if isPrivateOrInternalDomain(host) {
		return host, nil
	}

	// For other cases, try to extract a reasonable domain
	// This handles cases like "relayminer1" or custom internal hostnames
	parts := strings.Split(host, ".")
	if len(parts) == 1 {
		// Single hostname without dots (like "relayminer1")
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
