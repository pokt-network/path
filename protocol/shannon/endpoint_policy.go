package shannon

import (
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

// filterByEndpointPolicy applies operator-level endpoint security policies.
// Returns the filtered endpoints and the count of rejected endpoints.
// If no policies are enabled, returns the original endpoints unchanged.
func (p *Protocol) filterByEndpointPolicy(
	endpoints map[protocol.EndpointAddr]endpoint,
	rpcType sharedtypes.RPCType,
	logger polylog.Logger,
) map[protocol.EndpointAddr]endpoint {
	policy := p.endpointPolicy
	if !policy.RequireHTTPS && !policy.RequireDomain {
		return endpoints
	}

	filtered := make(map[protocol.EndpointAddr]endpoint, len(endpoints))
	rejectedHTTPS := 0
	rejectedDomain := 0

	for addr, ep := range endpoints {
		epURL := ep.GetURL(rpcType)
		if epURL == "" {
			epURL = ep.PublicURL()
		}

		if policy.RequireHTTPS && !isSecureURL(epURL) {
			rejectedHTTPS++
			logger.Debug().
				Str("endpoint", string(addr)).
				Str("url", epURL).
				Msg("Rejected endpoint: does not use HTTPS/WSS (endpoint_policy.require_https)")
			continue
		}

		if policy.RequireDomain && isRawIP(epURL) {
			rejectedDomain++
			logger.Debug().
				Str("endpoint", string(addr)).
				Str("url", epURL).
				Msg("Rejected endpoint: uses raw IP instead of domain (endpoint_policy.require_domain)")
			continue
		}

		filtered[addr] = ep
	}

	if rejectedHTTPS > 0 || rejectedDomain > 0 {
		logger.Info().
			Int("rejected_no_https", rejectedHTTPS).
			Int("rejected_raw_ip", rejectedDomain).
			Int("remaining", len(filtered)).
			Msg("Endpoint policy filtered out non-compliant endpoints")
	}

	return filtered
}

// isSecureURL returns true if the URL uses a secure scheme (https or wss).
func isSecureURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "https://") || strings.HasPrefix(rawURL, "wss://")
}

// maxRawIPCacheEntries bounds rawIPCache as a safety net; endpoint URLs are a
// bounded set so this ceiling is never expected to be reached.
const maxRawIPCacheEntries = 1 << 16 // 65536

// rawIPCache memoizes isRawIP results keyed by URL. The policy filter runs
// isRawIP for every endpoint on every request against a bounded set of static
// endpoint URLs, and the uncached path does url.Parse + net.ParseIP (a notable
// per-request allocation source in mainnet alloc profiles).
var (
	rawIPCache      sync.Map // map[string]bool: url -> isRawIP
	rawIPCacheCount atomic.Int64
)

// isRawIP returns true if the URL's host is an IP address rather than a domain
// name. Results are memoized; see rawIPCache.
func isRawIP(rawURL string) bool {
	if v, ok := rawIPCache.Load(rawURL); ok {
		return v.(bool)
	}

	result := computeIsRawIP(rawURL)

	if rawIPCacheCount.Load() < maxRawIPCacheEntries {
		if _, loaded := rawIPCache.LoadOrStore(rawURL, result); !loaded {
			rawIPCacheCount.Add(1)
		}
	}
	return result
}

// computeIsRawIP performs the uncached host check.
func computeIsRawIP(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	host := parsed.Hostname()
	if host == "" {
		return false
	}

	return net.ParseIP(host) != nil
}
