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

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

const defaultMaxTTL = 30 * time.Minute

// Failure-rate gate defaults.
//
// The trigger used to be first-error: a single failed batch item removed the whole hostname
// from the pool. That is volume-sensitive rather than quality-sensitive — the operator with
// the most endpoints receives the most traffic, so it reaches its first error soonest after
// every TTL expiry and is effectively locked out permanently. Measured in production: an
// operator sustaining 99.26% success was broken ~240 times in 3 hours, removing 90% of one
// service's pool, because 45 of its 50 endpoints sat behind 7 hostnames.
//
// Gating on RATE instead of count fixes that: a domain must fail both enough times to be
// statistically meaningful (minFailures) and often enough to be genuinely unhealthy
// (failureRateThreshold) before it is removed.
const (
	// defaultFailureWindow is the sliding window over which failures and successes are
	// compared. Short enough to react to a real outage, long enough that a burst of
	// concurrent batch items does not constitute a trend.
	defaultFailureWindow = 30 * time.Second
	// defaultMinFailures is the floor below which no rate is trusted. Without it, the
	// first failure on a quiet domain is a 100% failure rate.
	defaultMinFailures = 5
	// defaultFailureRateThreshold is the fraction of attempts that must fail before a
	// domain is removed. Set well above the error rate healthy high-volume operators
	// sustain (<1%) and well below what a genuinely broken host produces (~100%).
	defaultFailureRateThreshold = 0.20
	// defaultEscalationMemory is how long a domain's break history is remembered after it
	// recovers. Escalation is meant to punish an domain that breaks AGAIN after being let
	// back in; without memory across expiry every episode looks like a first offence.
	defaultEscalationMemory = 60 * time.Minute
)

// classifyCircuitBreakReason maps the free-text reason string passed to
// MarkBroken into a bounded category, suitable for use as a Prometheus label.
// The raw reason contains response snippets, error messages, and status codes
// (high cardinality) — for metrics we only need the prefix bucket.
//
// Keep in sync with the metrics.CircuitBreakReason* constants and with the
// reason strings produced by MarkBroken call sites in
// gateway/http_request_context_handle_request.go.
func classifyCircuitBreakReason(reason string) string {
	// Match by prefix; reasons are typically formatted as "<category>: <details>"
	// or "<category>_<detail>: ...". Order matters for prefixes that share leading
	// substrings (parallel_retry vs retry).
	switch {
	case strings.HasPrefix(reason, "parallel_retry"):
		return metrics.CircuitBreakReasonParallelRetry
	case strings.HasPrefix(reason, "batch_transport"):
		return metrics.CircuitBreakReasonBatchTransport
	case strings.HasPrefix(reason, "batch_heuristic"):
		return metrics.CircuitBreakReasonBatchHeuristic
	case strings.HasPrefix(reason, "retry"):
		return metrics.CircuitBreakReasonRetry
	case strings.HasPrefix(reason, "heuristic"):
		return metrics.CircuitBreakReasonHeuristic
	default:
		return metrics.CircuitBreakReasonUnknown
	}
}

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

	// Failure-rate gate. Guarded by statsMu, kept separate from mu so recording an
	// outcome never contends with the GetBrokenDomains read path.
	failureWindow        time.Duration
	minFailures          int
	failureRateThreshold float64
	escalationMemory     time.Duration
	statsMu              sync.Mutex
	stats                map[string]map[string]*domainOutcomeWindow // serviceID -> domain
}

// domainOutcomeWindow is a sliding count of relay outcomes for one domain, plus the memory
// of its last break episode.
//
// windowStart/failures/successes reset together once the window elapses, so the counts always
// describe recent behavior rather than lifetime totals — a domain that failed heavily hours
// ago must not stay gated on that history.
//
// lastEpisodeAt/lastHitCount survive the window and the break's own expiry: they are what
// makes escalation mean "broke again after we let it back in" instead of "was marked twice
// during one incident".
type domainOutcomeWindow struct {
	windowStart time.Time
	failures    int
	successes   int

	lastEpisodeAt time.Time
	lastHitCount  int
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
		redisClient:          redisClient,
		logger:               logger,
		keyPrefix:            "path:gw:circuit:",
		defaultTTL:           1 * time.Minute,
		maxTTL:               defaultMaxTTL,
		cacheTTL:             5 * time.Second,
		cache:                make(map[string]*circuitCacheEntry),
		failureWindow:        defaultFailureWindow,
		minFailures:          defaultMinFailures,
		failureRateThreshold: defaultFailureRateThreshold,
		escalationMemory:     defaultEscalationMemory,
		stats:                make(map[string]map[string]*domainOutcomeWindow),
	}
}

// RecordSuccess reports a successful relay against a domain, forming the denominator of the
// failure-rate gate. Without it the gate can only see failures, and any failure looks like a
// 100% failure rate — which is the first-error behavior this replaces.
func (cb *DomainCircuitBreaker) RecordSuccess(serviceID, domain string) {
	if cb == nil || domain == "" {
		return
	}
	cb.statsMu.Lock()
	defer cb.statsMu.Unlock()
	w := cb.windowLocked(serviceID, domain, time.Now())
	w.successes++
}

// windowLocked returns the outcome window for a domain, rolling it over if the current one
// has elapsed. Caller must hold statsMu.
func (cb *DomainCircuitBreaker) windowLocked(serviceID, domain string, now time.Time) *domainOutcomeWindow {
	byDomain, ok := cb.stats[serviceID]
	if !ok {
		byDomain = make(map[string]*domainOutcomeWindow)
		cb.stats[serviceID] = byDomain
	}
	w, ok := byDomain[domain]
	if !ok {
		w = &domainOutcomeWindow{windowStart: now}
		byDomain[domain] = w
		return w
	}
	if now.Sub(w.windowStart) > cb.failureWindow {
		// Roll the window. Episode memory deliberately survives — it is not part of the
		// rate calculation, it is what escalation counts.
		w.windowStart = now
		w.failures = 0
		w.successes = 0
	}
	return w
}

// shouldBreak records a failure and reports whether the domain's recent failure RATE justifies
// removing it from the pool, along with the hit count for this break episode.
//
// Both conditions must hold: at least minFailures in the window (so a single failure on a
// quiet domain is not a 100% rate) and a failure fraction at or above failureRateThreshold
// (so a high-volume domain with a low error rate is never removed, no matter how many raw
// failures that volume produces).
func (cb *DomainCircuitBreaker) shouldBreak(serviceID, domain string, now time.Time) (bool, int) {
	cb.statsMu.Lock()
	defer cb.statsMu.Unlock()

	w := cb.windowLocked(serviceID, domain, now)
	w.failures++

	total := w.failures + w.successes
	if w.failures < cb.minFailures || total == 0 {
		return false, 0
	}
	if float64(w.failures)/float64(total) < cb.failureRateThreshold {
		return false, 0
	}

	// Breaking. Escalate only if this domain broke recently BEFORE this episode — i.e. it
	// was let back in and failed again. Concurrent duplicate marks within one episode are
	// filtered upstream in MarkBroken and never reach here.
	hitCount := 1
	if !w.lastEpisodeAt.IsZero() && now.Sub(w.lastEpisodeAt) <= cb.escalationMemory {
		hitCount = w.lastHitCount + 1
	}
	w.lastEpisodeAt = now
	w.lastHitCount = hitCount

	// The window is consumed by the break: keep counting from scratch so the domain is
	// judged on behavior after it returns, not on the failures that removed it.
	w.windowStart = now
	w.failures = 0
	w.successes = 0

	return true, hitCount
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

	// Already broken? This trigger belongs to the episode that is already in effect.
	// Batch items fail concurrently on separate goroutines, so one incident produces many
	// of these — measured at ~89 per episode in production. Escalating on them drove the
	// TTL straight to the 30-minute cap (6 hits is enough) on the strength of a single
	// transient burst. Count it, do not escalate, do not extend the expiry.
	cb.mu.RLock()
	if entry, ok := cb.cache[serviceID]; ok {
		if existing, exists := entry.domains[domain]; exists && existing.expiry.After(now) {
			cb.mu.RUnlock()
			metrics.RecordCircuitBreakerEvent(serviceID, domain, classifyCircuitBreakReason(reason), metrics.CircuitBreakerEventDuplicate)
			return
		}
	}
	cb.mu.RUnlock()

	// Rate gate: a failure alone is not grounds for removing a domain from the pool.
	breakIt, hitCount := cb.shouldBreak(serviceID, domain, now)
	if !breakIt {
		metrics.RecordCircuitBreakerEvent(serviceID, domain, classifyCircuitBreakReason(reason), metrics.CircuitBreakerEventSuppressed)
		return
	}

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

	ttl := cb.escalatedTTL(hitCount)
	expiry := now.Add(ttl)
	entry.domains[domain] = brokenDomainState{expiry: expiry, hitCount: hitCount, reason: reason}
	cb.mu.Unlock()

	// Surface state via Prometheus gauge so dashboards can show "currently broken
	// domains" without needing Redis access. Idempotent — re-setting to 1 is fine.
	metrics.SetCircuitBreakerState(serviceID, domain, true)

	// Record a "broken" event with reason category so dashboards can show rate
	// of breaks decomposed by cause (retry / batch_transport / batch_heuristic /
	// parallel_retry / heuristic / unknown). One event per EPISODE — triggers that
	// the rate gate declined, or that arrived while the domain was already broken,
	// are counted under "suppressed" and "duplicate" instead. broken:recovered is
	// therefore ~1:1; it used to run ~89:1 purely from concurrent duplicates.
	metrics.RecordCircuitBreakerEvent(serviceID, domain, classifyCircuitBreakReason(reason), metrics.CircuitBreakerEventBroken)

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
	// The reason is truncated to 500 chars to keep Redis values manageable
	// while preserving diagnostic info like method names.
	if cb.redisClient != nil {
		truncatedReason := reason
		if len(truncatedReason) > 500 {
			truncatedReason = truncatedReason[:500]
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

	entry, ok := cb.cache[serviceID]
	if !ok {
		cb.mu.Unlock()
		return nil
	}

	// Remove expired entries; collect their names + reasons so we can drop the
	// gauge to 0 and record a "recovered" event outside the lock.
	now := time.Now()
	type expiredEntry struct {
		domain string
		reason string
	}
	var expired []expiredEntry
	for domain, state := range entry.domains {
		if !state.expiry.After(now) {
			expired = append(expired, expiredEntry{domain: domain, reason: state.reason})
			delete(entry.domains, domain)
		}
	}
	entry.refreshAt = now.Add(cb.cacheTTL)

	result := make(map[string]bool, len(entry.domains))
	for d := range entry.domains {
		result[d] = true
	}
	cb.mu.Unlock()

	for _, e := range expired {
		metrics.SetCircuitBreakerState(serviceID, e.domain, false)
		metrics.RecordCircuitBreakerEvent(serviceID, e.domain, classifyCircuitBreakReason(e.reason), metrics.CircuitBreakerEventRecovered)
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

	// Merge with local cache (local entries might be newer than Redis).
	// Capture pre-merge cache contents so we can drop the gauge to 0 for any
	// domain that was previously broken but is no longer present after merge.
	cb.mu.Lock()
	type expiredLocal struct {
		domain string
		reason string
	}
	var previouslyBroken []expiredLocal
	existingEntry, ok := cb.cache[serviceID]
	if ok {
		for domain, state := range existingEntry.domains {
			if state.expiry.After(now) {
				if redisState, exists := domains[domain]; !exists || state.expiry.After(redisState.expiry) {
					domains[domain] = state
				}
			} else {
				previouslyBroken = append(previouslyBroken, expiredLocal{domain: domain, reason: state.reason})
			}
		}
	}
	cb.cache[serviceID] = &circuitCacheEntry{
		domains:   domains,
		refreshAt: now.Add(cb.cacheTTL),
	}
	cb.mu.Unlock()

	// Drop gauge for previously-broken-but-now-expired domains, plus the explicit
	// expiredFields list we already collected from Redis. Done outside the lock.
	// We don't have a reason for expiredFields entries (parsed Redis value gave us
	// one, but for compactness we don't carry it through here) — they get the
	// "unknown" reason bucket on recovery, which is acceptable.
	for _, d := range expiredFields {
		metrics.SetCircuitBreakerState(serviceID, d, false)
		metrics.RecordCircuitBreakerEvent(serviceID, d, metrics.CircuitBreakReasonUnknown, metrics.CircuitBreakerEventRecovered)
	}
	for _, e := range previouslyBroken {
		metrics.SetCircuitBreakerState(serviceID, e.domain, false)
		metrics.RecordCircuitBreakerEvent(serviceID, e.domain, classifyCircuitBreakReason(e.reason), metrics.CircuitBreakerEventRecovered)
	}
	// Re-assert gauge=1 for currently-broken domains. Idempotent and ensures the
	// metric is correct on a fresh pod that lazily picks up Redis state without
	// going through MarkBroken locally. Note: we deliberately do NOT record a
	// "broken" event here — these aren't new transitions, just a metric resync
	// for a state that was already counted at MarkBroken time on whichever pod
	// originally fired it.
	for d := range domains {
		metrics.SetCircuitBreakerState(serviceID, d, true)
	}

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
	type clearedEntry struct {
		domain string
		reason string
	}
	var cleared []clearedEntry
	if ok {
		count = len(entry.domains)
		cleared = make([]clearedEntry, 0, count)
		for d, state := range entry.domains {
			cleared = append(cleared, clearedEntry{domain: d, reason: state.reason})
		}
		delete(cb.cache, serviceID)
	}
	cb.mu.Unlock()

	// An admin clear is an explicit "these domains are healthy again", so it must also drop
	// the escalation memory and the failure window. Otherwise the next single failure would
	// re-break at the previous hit count and the clear would appear not to have worked —
	// the same surprise as refreshFromRedis merging local state back.
	cb.statsMu.Lock()
	delete(cb.stats, serviceID)
	cb.statsMu.Unlock()

	if cb.redisClient != nil {
		cb.redisClient.Del(ctx, cb.keyPrefix+serviceID)
	}

	// Drop the gauge AND record a recovered event for every cleared domain. Each
	// admin-clear represents an explicit "operator forced these domains back to
	// healthy" action, distinct from natural TTL-driven recovery — the recovery
	// counter still captures it because operationally it's the same transition.
	for _, c := range cleared {
		metrics.SetCircuitBreakerState(serviceID, c.domain, false)
		metrics.RecordCircuitBreakerEvent(serviceID, c.domain, classifyCircuitBreakReason(c.reason), metrics.CircuitBreakerEventRecovered)
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
