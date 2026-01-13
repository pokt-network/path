package shannon

import (
	"fmt"
	"strings"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
)

// GetEndpointTLDs extracts TLD information from endpoint addresses
func GetEndpointTLDs(endpoints protocol.EndpointAddrList) map[protocol.EndpointAddr]string {
	endpointTLDs := make(map[protocol.EndpointAddr]string)

	for _, endpointAddr := range endpoints {
		endpointTLDs[endpointAddr] = ExtractTLDFromEndpointAddr(string(endpointAddr))
	}

	return endpointTLDs
}

// ExtractTLDFromEndpointAddr extracts effective TLD+1 from endpoint address.
// Returns an empty string if the TLD cannot be determined.
func ExtractTLDFromEndpointAddr(addr string) string {
	// Try direct URL parsing first
	if etld, err := ExtractDomainOrHost(addr); err == nil {
		return etld
	}

	// Handle embedded URLs (e.g., "supplier-https://example.com")
	if idx := strings.Index(addr, "http"); idx != -1 {
		if etld, err := ExtractDomainOrHost(addr[idx:]); err == nil {
			return etld
		}
	}

	// Fallback: try adding https:// prefix for domain-like strings
	parts := strings.FieldsFunc(addr, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})

	for _, part := range parts {
		if strings.Contains(part, ".") && !strings.HasPrefix(part, "http") {
			if etld, err := ExtractDomainOrHost("https://" + part); err == nil {
				return etld
			}
		}
	}

	return ""
}

func LogEndpointTLDDiversity(logger polylog.Logger, endpoints protocol.EndpointAddrList) {
	logger = logger.
		With("method", "logEndpointTLDDiversity").
		With("num_endpoints", len(endpoints))

	// Count unique TLDs
	endpointTLDs := GetEndpointTLDs(endpoints)
	tldCounts := make(map[string]int)
	for _, tld := range endpointTLDs {
		if tld != "" {
			tldCounts[tld]++
		}
	}

	// Log TLD distribution
	tldDistribution := make([]string, 0, len(tldCounts))
	for tld, count := range tldCounts {
		tldDistribution = append(tldDistribution, fmt.Sprintf("%s=%d", tld, count))
	}
	logger.Debug().Msgf("Endpoint TLD diversity: %s", strings.Join(tldDistribution, ", "))
}
