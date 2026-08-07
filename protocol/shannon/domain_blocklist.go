package shannon

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/gateway"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
)

// envBlockedDomains appends ban entries at pod-restart speed, without a config rollout.
// Entries are comma-separated: "domain" bans every RPC type, "domain:type1|type2" bans
// only those types. Example:
//
//	PATH_BLOCKED_DOMAINS=rpcgate.xyz:websocket,spacebelt.xyz:websocket,evil.example
//
// Env entries are UNIONED with the blocked_domains config list — the env var can widen
// a ban but never narrow one.
const envBlockedDomains = "PATH_BLOCKED_DOMAINS"

// maxDomainDecisionCacheEntries bounds decisionCache as a safety net; endpoint URLs are
// a bounded set so this ceiling is never expected to be reached (same rationale as
// rawIPCache in endpoint_policy.go).
const maxDomainDecisionCacheEntries = 1 << 16 // 65536

// domainBlocklist is the compiled form of the gateway-operator (domain, rpc_type)
// blocklist — the nuclear ban. An endpoint whose URL matches a blocked domain is removed
// from EVERY path that hands out endpoints: primary selection (HTTP and WebSocket,
// which also covers retry/hedge/batch and the WebSocket rebind, since they all draw from
// the pool this filters), fallback endpoints, and health checks.
//
// Contrast with the two things it deliberately is not:
//   - blocked_suppliers: keyed on supplier address, per-service. Supplier addresses
//     rotate with sessions and one operator holds many; a domain names the operator's
//     infrastructure directly.
//   - admin drains: temporary (5h cap), per-service, reputation-overlay based, and they
//     yield when the pool would empty. This blocklist is permanent (config-driven),
//     fleet-wide across all services, and does NOT yield.
//
// A nil *domainBlocklist is valid and blocks nothing; all methods are nil-safe.
type domainBlocklist struct {
	// blocked maps a lowercase domain — either an eTLD+1 ("rpcgate.xyz") or an exact
	// hostname ("s019.rpcgate.xyz") — to the set of banned RPC types.
	// A nil set bans every RPC type.
	blocked map[string]map[sharedtypes.RPCType]struct{}

	// decisionCache memoizes rawURL -> matched blocklist key ("" = no match). The filter
	// runs per endpoint per selection on the relay hot path, and the uncached path does a
	// url.Parse. The blocklist is immutable after startup, so entries never invalidate.
	decisionCache      sync.Map // map[string]string
	decisionCacheCount atomic.Int64
}

// newDomainBlocklist compiles config entries into a matcher. Returns (nil, nil) when the
// list is empty. Returns an error — refusing to boot — on an empty domain or an unknown
// rpc_type: silently narrowing or dropping a nuclear ban is worse than failing loudly.
func newDomainBlocklist(entries []gateway.BlockedDomainConfig) (*domainBlocklist, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	mapper := gateway.NewRPCTypeMapper()
	blocked := make(map[string]map[sharedtypes.RPCType]struct{}, len(entries))

	for _, e := range entries {
		domain := strings.ToLower(strings.TrimSpace(e.Domain))
		if domain == "" {
			return nil, fmt.Errorf("blocked_domains: entry with an empty domain")
		}

		existing, seen := blocked[domain]

		// No rpc_types = ban everything, absorbing any narrower entry for the domain.
		if len(e.RPCTypes) == 0 {
			blocked[domain] = nil
			continue
		}
		// Already banned for everything; a narrower entry cannot un-ban.
		if seen && existing == nil {
			continue
		}

		set := existing
		if set == nil {
			set = make(map[sharedtypes.RPCType]struct{}, len(e.RPCTypes))
		}
		for _, t := range e.RPCTypes {
			rpcType, err := mapper.ParseRPCType(strings.TrimSpace(t))
			if err != nil {
				return nil, fmt.Errorf("blocked_domains: domain %q: %w", domain, err)
			}
			set[rpcType] = struct{}{}
		}
		blocked[domain] = set
	}

	return &domainBlocklist{blocked: blocked}, nil
}

// parseBlockedDomainsEnv parses the PATH_BLOCKED_DOMAINS value into config entries.
// Malformed pieces are not silently dropped here — an empty domain surfaces as an error
// from newDomainBlocklist.
func parseBlockedDomainsEnv(raw string) []gateway.BlockedDomainConfig {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var entries []gateway.BlockedDomainConfig
	for _, piece := range strings.Split(raw, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}
		domain, typesStr, hasTypes := strings.Cut(piece, ":")
		entry := gateway.BlockedDomainConfig{Domain: strings.TrimSpace(domain)}
		if hasTypes {
			for _, t := range strings.Split(typesStr, "|") {
				if t = strings.TrimSpace(t); t != "" {
					entry.RPCTypes = append(entry.RPCTypes, t)
				}
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

// IsBlocked reports whether an endpoint at rawURL is banned from serving rpcType.
// Matching is on the URL — never on EndpointAddr or supplier address — so a ban survives
// session rollovers by construction: an endpoint rotated into a session at a blocked
// domain is banned the moment it appears (the lesson of drain bug 2).
func (b *domainBlocklist) IsBlocked(rawURL string, rpcType sharedtypes.RPCType) bool {
	if b == nil || rawURL == "" {
		return false
	}

	key := b.matchKey(rawURL)
	if key == "" {
		return false
	}

	set := b.blocked[key]
	if set == nil {
		return true // banned for every RPC type
	}
	_, banned := set[rpcType]
	return banned
}

// matchKey resolves rawURL to the blocklist key it matches ("" = none), memoized.
func (b *domainBlocklist) matchKey(rawURL string) string {
	if v, ok := b.decisionCache.Load(rawURL); ok {
		return v.(string)
	}

	key := b.computeMatchKey(rawURL)

	if b.decisionCacheCount.Load() < maxDomainDecisionCacheEntries {
		if _, loaded := b.decisionCache.LoadOrStore(rawURL, key); !loaded {
			b.decisionCacheCount.Add(1)
		}
	}
	return key
}

// computeMatchKey checks the exact hostname first (most specific), then the eTLD+1.
func (b *domainBlocklist) computeMatchKey(rawURL string) string {
	if parsed, err := url.Parse(rawURL); err == nil {
		if host := strings.ToLower(parsed.Hostname()); host != "" {
			if _, ok := b.blocked[host]; ok {
				return host
			}
		}
	}
	if domain, err := shannonmetrics.ExtractDomainOrHost(rawURL); err == nil {
		domain = strings.ToLower(domain)
		if _, ok := b.blocked[domain]; ok {
			return domain
		}
	}
	return ""
}

// configuredEntries returns the compiled (domain, rpc_type) pairs for startup logging and
// the path_blocked_domains_configured gauge, sorted for stable output. An all-types ban
// is reported as rpc_type "all".
func (b *domainBlocklist) configuredEntries() [][2]string {
	if b == nil {
		return nil
	}
	mapper := gateway.NewRPCTypeMapper()
	var out [][2]string
	for domain, set := range b.blocked {
		if set == nil {
			out = append(out, [2]string{domain, "all"})
			continue
		}
		for rpcType := range set {
			out = append(out, [2]string{domain, mapper.FormatRPCType(rpcType)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}
