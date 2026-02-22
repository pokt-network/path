package gateway

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/pokt-network/path/protocol"
)

// DomainCircuitBreaker tracks broken domains across pods via Redis.
// When any pod discovers a domain is returning errors, it marks it broken
// so all pods skip that domain on initial attempts for the TTL window.
//
// Hot path cost: zero Redis calls (reads from local cache).
// Cache is lazily refreshed from Redis every cacheTTL (5s default).
type DomainCircuitBreaker struct {
	redisClient *redis.Client // nil = local-only mode (no cross-pod sharing)
	keyPrefix   string        // "path:gw:circuit:"
	defaultTTL  time.Duration // how long a domain stays broken (1m default)
	cacheTTL    time.Duration // how often to refresh local cache from Redis (5s default)
	mu          sync.RWMutex
	cache       map[string]*circuitCacheEntry
}

// circuitCacheEntry holds the cached broken domains for a single service.
type circuitCacheEntry struct {
	// domains maps domain name to its expiry time.
	// Uses time.Time for sub-second precision in local cache.
	// Redis stores unix seconds (sufficient for production TTLs).
	domains   map[string]time.Time
	refreshAt time.Time
}

// NewDomainCircuitBreaker creates a new circuit breaker.
// Pass nil for redisClient to run in local-only mode (no cross-pod sharing).
func NewDomainCircuitBreaker(redisClient *redis.Client) *DomainCircuitBreaker {
	return &DomainCircuitBreaker{
		redisClient: redisClient,
		keyPrefix:   "path:gw:circuit:",
		defaultTTL:  1 * time.Minute,
		cacheTTL:    5 * time.Second,
		cache:       make(map[string]*circuitCacheEntry),
	}
}

// MarkBroken marks a domain as broken for the given service.
// Updates local cache immediately and writes to Redis fire-and-forget.
func (cb *DomainCircuitBreaker) MarkBroken(ctx context.Context, serviceID, domain string) {
	expiry := time.Now().Add(cb.defaultTTL)

	// Update local cache immediately
	cb.mu.Lock()
	entry, ok := cb.cache[serviceID]
	if !ok {
		entry = &circuitCacheEntry{
			domains:   make(map[string]time.Time),
			refreshAt: time.Now().Add(cb.cacheTTL),
		}
		cb.cache[serviceID] = entry
	}
	entry.domains[domain] = expiry
	cb.mu.Unlock()

	// Write to Redis (fire-and-forget)
	if cb.redisClient != nil {
		cb.redisClient.HSet(ctx, cb.keyPrefix+serviceID, domain, strconv.FormatInt(expiry.Unix(), 10))
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
func filterExpiredDomains(domains map[string]time.Time) map[string]bool {
	now := time.Now()
	result := make(map[string]bool)
	for domain, expiry := range domains {
		if expiry.After(now) {
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
	for domain, expiry := range entry.domains {
		if !expiry.After(now) {
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
	domains := make(map[string]time.Time)
	var expiredFields []string

	for domain, expiryStr := range vals {
		expiryUnix, parseErr := strconv.ParseInt(expiryStr, 10, 64)
		if parseErr != nil {
			expiredFields = append(expiredFields, domain)
			continue
		}
		if expiryUnix > nowUnix {
			domains[domain] = time.Unix(expiryUnix, 0)
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
		for domain, expiry := range existingEntry.domains {
			if expiry.After(now) {
				if redisExpiry, exists := domains[domain]; !exists || expiry.After(redisExpiry) {
					domains[domain] = expiry
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
