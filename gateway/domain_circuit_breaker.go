package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/redis/go-redis/v9"

	"github.com/pokt-network/path/protocol"
)

const defaultMaxTTL = 30 * time.Minute

// DomainCircuitBreaker tracks broken domains across pods via Redis.
// When any pod discovers a domain is returning errors, it marks it broken
// so all pods skip that domain on initial attempts for the TTL window.
//
// Repeated breaks on the same domain escalate the TTL exponentially:
// hit 1: 1min, hit 2: 2min, hit 3: 4min, hit 4: 8min, hit 5: 16min, hit 6+: 30min (cap).
//
// Hot path cost: zero Redis calls (reads from local cache).
// Cache is lazily refreshed from Redis every cacheTTL (5s default).
type DomainCircuitBreaker struct {
	redisClient *redis.Client  // nil = local-only mode (no cross-pod sharing)
	logger      polylog.Logger // logger for circuit break events
	keyPrefix   string         // "path:gw:circuit:"
	defaultTTL  time.Duration  // base TTL for first break (1m default)
	maxTTL      time.Duration  // maximum TTL cap (30m default)
	cacheTTL    time.Duration  // how often to refresh local cache from Redis (5s default)
	mu          sync.RWMutex
	cache       map[string]*circuitCacheEntry
}

// brokenDomainState tracks break state for a single domain.
type brokenDomainState struct {
	expiry   time.Time
	hitCount int    // number of times this domain has been marked broken
	reason   string // why the domain was last marked broken (for diagnostics)
}

// circuitCacheEntry holds the cached broken domains for a single service.
type circuitCacheEntry struct {
	// domains maps domain name to its break state (expiry + hit count).
	// Uses time.Time for sub-second precision in local cache.
	// Redis stores "unixSeconds:hitCount" (sufficient for production TTLs).
	domains   map[string]brokenDomainState
	refreshAt time.Time
}

// NewDomainCircuitBreaker creates a new circuit breaker.
// Pass nil for redisClient to run in local-only mode (no cross-pod sharing).
func NewDomainCircuitBreaker(redisClient *redis.Client, logger polylog.Logger) *DomainCircuitBreaker {
	return &DomainCircuitBreaker{
		redisClient: redisClient,
		logger:      logger,
		keyPrefix:   "path:gw:circuit:",
		defaultTTL:  1 * time.Minute,
		maxTTL:      defaultMaxTTL,
		cacheTTL:    5 * time.Second,
		cache:       make(map[string]*circuitCacheEntry),
	}
}

// escalatedTTL calculates the TTL for the given hit count using exponential backoff.
// hit 1: baseTTL, hit 2: 2*baseTTL, hit 3: 4*baseTTL, etc., capped at maxTTL.
func (cb *DomainCircuitBreaker) escalatedTTL(hitCount int) time.Duration {
	if hitCount <= 1 {
		return cb.defaultTTL
	}
	shift := hitCount - 1
	if shift > 5 {
		shift = 5
	}
	ttl := cb.defaultTTL * (1 << shift)
	if ttl > cb.maxTTL {
		ttl = cb.maxTTL
	}
	return ttl
}

// MarkBroken marks a domain as broken for the given service.
// If the domain is already broken, the hit count is incremented and the TTL escalates.
// Updates local cache immediately and writes to Redis fire-and-forget.
// The reason parameter is logged to help diagnose why domains are being circuit-broken.
func (cb *DomainCircuitBreaker) MarkBroken(ctx context.Context, serviceID, domain, reason string) {
	now := time.Now()

	// Update local cache immediately
	cb.mu.Lock()
	entry, ok := cb.cache[serviceID]
	if !ok {
		entry = &circuitCacheEntry{
			domains:   make(map[string]brokenDomainState),
			refreshAt: now.Add(cb.cacheTTL),
		}
		cb.cache[serviceID] = entry
	}

	// Escalate hit count if domain is already broken (not yet expired)
	hitCount := 1
	if existing, exists := entry.domains[domain]; exists && existing.expiry.After(now) {
		hitCount = existing.hitCount + 1
	}

	ttl := cb.escalatedTTL(hitCount)
	expiry := now.Add(ttl)
	entry.domains[domain] = brokenDomainState{expiry: expiry, hitCount: hitCount, reason: reason}
	cb.mu.Unlock()

	// Always log circuit break events at error level for production visibility.
	// This is critical for diagnosing why domains get locked out.
	cb.logger.Error().
		Str("service_id", serviceID).
		Str("domain", domain).
		Str("reason", reason).
		Int("hit_count", hitCount).
		Dur("ttl", ttl).
		Time("expiry", expiry).
		Msg("Circuit breaker: domain marked broken")

	// Write to Redis (fire-and-forget) — format: "unixSeconds:hitCount:reason"
	// The reason is truncated to 200 chars to keep Redis values manageable.
	if cb.redisClient != nil {
		truncatedReason := reason
		if len(truncatedReason) > 200 {
			truncatedReason = truncatedReason[:200]
		}
		val := fmt.Sprintf("%d:%d:%s", expiry.Unix(), hitCount, truncatedReason)
		cb.redisClient.HSet(ctx, cb.keyPrefix+serviceID, domain, val)
	}
}

// GetBrokenDomains returns the set of broken domains for a service.
// Reads from local cache if fresh, otherwise refreshes from Redis.
// Hot path cost: zero Redis calls when cache is fresh.
func (cb *DomainCircuitBreaker) GetBrokenDomains(ctx context.Context, serviceID string) map[string]bool {
	cb.mu.RLock()
	entry, ok := cb.cache[serviceID]
	if ok && time.Now().Before(entry.refreshAt) {
		result := filterExpiredDomains(entry.domains)
		cb.mu.RUnlock()
		return result
	}
	cb.mu.RUnlock()

	// Cache is stale or missing — refresh
	if cb.redisClient == nil {
		return cb.refreshLocal(serviceID)
	}
	return cb.refreshFromRedis(ctx, serviceID)
}

// filterExpiredDomains returns only non-expired domains from the cache entry.
func filterExpiredDomains(domains map[string]brokenDomainState) map[string]bool {
	now := time.Now()
	result := make(map[string]bool)
	for domain, state := range domains {
		if state.expiry.After(now) {
			result[domain] = true
		}
	}
	return result
}

// refreshLocal refreshes the local cache by filtering expired entries.
// Used when Redis is not available (local-only mode).
func (cb *DomainCircuitBreaker) refreshLocal(serviceID string) map[string]bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entry, ok := cb.cache[serviceID]
	if !ok {
		return nil
	}

	// Remove expired entries
	now := time.Now()
	for domain, state := range entry.domains {
		if !state.expiry.After(now) {
			delete(entry.domains, domain)
		}
	}
	entry.refreshAt = now.Add(cb.cacheTTL)

	result := make(map[string]bool, len(entry.domains))
	for d := range entry.domains {
		result[d] = true
	}
	return result
}

// parseRedisValue parses a Redis value in one of three formats (backward compatible):
//   - "unixSeconds:hitCount:reason" (current format with diagnostics)
//   - "unixSeconds:hitCount" (legacy format without reason)
//   - "unixSeconds" (oldest format, hitCount defaults to 1)
func parseRedisValue(val string) (expiryUnix int64, hitCount int, reason string, ok bool) {
	parts := strings.SplitN(val, ":", 3)
	switch len(parts) {
	case 3:
		// Current format: "unixSeconds:hitCount:reason"
		expiry, err1 := strconv.ParseInt(parts[0], 10, 64)
		hits, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return 0, 0, "", false
		}
		return expiry, hits, parts[2], true
	case 2:
		// Legacy format: "unixSeconds:hitCount"
		expiry, err1 := strconv.ParseInt(parts[0], 10, 64)
		hits, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			return 0, 0, "", false
		}
		return expiry, hits, "", true
	default:
		// Oldest format: just "unixSeconds"
		expiry, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return 0, 0, "", false
		}
		return expiry, 1, "", true
	}
}

// refreshFromRedis fetches broken domains from Redis and updates local cache.
func (cb *DomainCircuitBreaker) refreshFromRedis(ctx context.Context, serviceID string) map[string]bool {
	key := cb.keyPrefix + serviceID
	vals, err := cb.redisClient.HGetAll(ctx, key).Result()
	if err != nil {
		// Redis error — fall back to local cache
		return cb.refreshLocal(serviceID)
	}

	now := time.Now()
	nowUnix := now.Unix()
	domains := make(map[string]brokenDomainState)
	var expiredFields []string

	for domain, redisVal := range vals {
		expiryUnix, hitCount, reason, parseOk := parseRedisValue(redisVal)
		if !parseOk {
			expiredFields = append(expiredFields, domain)
			continue
		}
		if expiryUnix > nowUnix {
			domains[domain] = brokenDomainState{
				expiry:   time.Unix(expiryUnix, 0),
				hitCount: hitCount,
				reason:   reason,
			}
		} else {
			expiredFields = append(expiredFields, domain)
		}
	}

	// Clean up expired fields in Redis (fire-and-forget)
	if len(expiredFields) > 0 {
		cb.redisClient.HDel(ctx, key, expiredFields...)
	}

	// Merge with local cache (local entries might be newer than Redis)
	cb.mu.Lock()
	existingEntry, ok := cb.cache[serviceID]
	if ok {
		for domain, state := range existingEntry.domains {
			if state.expiry.After(now) {
				if redisState, exists := domains[domain]; !exists || state.expiry.After(redisState.expiry) {
					domains[domain] = state
				}
			}
		}
	}
	cb.cache[serviceID] = &circuitCacheEntry{
		domains:   domains,
		refreshAt: now.Add(cb.cacheTTL),
	}
	cb.mu.Unlock()

	// Build result
	result := make(map[string]bool, len(domains))
	for d := range domains {
		result[d] = true
	}
	return result
}

// ClearService clears all circuit breaker state for a service, both in-memory and Redis.
// This is the only reliable way to reset circuit breaker state because refreshFromRedis
// merges local entries back, so a Redis DEL alone is insufficient.
func (cb *DomainCircuitBreaker) ClearService(ctx context.Context, serviceID string) int {
	cb.mu.Lock()
	entry, ok := cb.cache[serviceID]
	count := 0
	if ok {
		count = len(entry.domains)
		delete(cb.cache, serviceID)
	}
	cb.mu.Unlock()

	if cb.redisClient != nil {
		cb.redisClient.Del(ctx, cb.keyPrefix+serviceID)
	}

	cb.logger.Info().
		Str("service_id", serviceID).
		Int("cleared_domains", count).
		Msg("Circuit breaker: cleared all domains for service")

	return count
}

// filterEndpointsByBrokenDomains removes endpoints whose domain is in the broken set.
func filterEndpointsByBrokenDomains(endpoints protocol.EndpointAddrList, brokenDomains map[string]bool) protocol.EndpointAddrList {
	filtered := make(protocol.EndpointAddrList, 0, len(endpoints))
	for _, ep := range endpoints {
		domain := extractDomainFromEndpoint(ep)
		if domain != "" && brokenDomains[domain] {
			continue
		}
		filtered = append(filtered, ep)
	}
	return filtered
}
