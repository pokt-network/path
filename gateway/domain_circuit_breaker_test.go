package gateway

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

func testCircuitBreakerLogger() polylog.Logger {
	return polyzero.NewLogger(polyzero.WithOutput(os.Stderr))
}

// readCircuitBreakerGauge returns the current value of the per-(serviceID, domain)
// circuit-breaker state gauge. Used by metric-transition tests.
func readCircuitBreakerGauge(serviceID, domain string) float64 {
	return testutil.ToFloat64(metrics.DomainCircuitBreakerState.WithLabelValues(serviceID, domain))
}

// breakDomain drives a domain past the failure-rate gate so it is actually removed from the
// pool. A single MarkBroken no longer breaks anything by design: the trigger is a failure
// RATE, not a first error, because first-error breaking removed high-volume operators
// sustaining >99% success. Tests that care about cache/TTL/Redis mechanics rather than the
// gate itself use this to reach the broken state.
func breakDomain(cb *DomainCircuitBreaker, ctx context.Context, serviceID, domain, reason string) {
	for i := 0; i < defaultMinFailures; i++ {
		cb.MarkBroken(ctx, serviceID, domain, reason)
	}
}

func TestDomainCircuitBreaker_MarkAndGet(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	ctx := context.Background()

	// Initially no broken domains
	domains := cb.GetBrokenDomains(ctx, "eth")
	if len(domains) != 0 {
		t.Fatalf("expected no broken domains, got %d", len(domains))
	}

	// Mark a domain as broken
	breakDomain(cb, ctx, "eth", "rel.spacebelt.xyz", "test")

	// Should now appear in broken domains
	domains = cb.GetBrokenDomains(ctx, "eth")
	if !domains["rel.spacebelt.xyz"] {
		t.Fatal("expected rel.spacebelt.xyz to be broken")
	}

	// Different service should not have it
	domains = cb.GetBrokenDomains(ctx, "poly")
	if len(domains) != 0 {
		t.Fatalf("expected no broken domains for poly, got %d", len(domains))
	}
}

func TestDomainCircuitBreaker_MultipleDomains(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	ctx := context.Background()

	breakDomain(cb, ctx, "eth", "broken1.example.com", "test")
	breakDomain(cb, ctx, "eth", "broken2.example.com", "test")

	domains := cb.GetBrokenDomains(ctx, "eth")
	if len(domains) != 2 {
		t.Fatalf("expected 2 broken domains, got %d", len(domains))
	}
	if !domains["broken1.example.com"] || !domains["broken2.example.com"] {
		t.Fatal("expected both domains to be broken")
	}
}

func TestDomainCircuitBreaker_LocalOnlyMode(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger()) // nil Redis = local-only
	ctx := context.Background()

	// Should work without Redis
	breakDomain(cb, ctx, "eth", "broken.example.com", "test")
	domains := cb.GetBrokenDomains(ctx, "eth")
	if !domains["broken.example.com"] {
		t.Fatal("expected broken.example.com to be broken in local-only mode")
	}

	// Mark another domain for a different service
	breakDomain(cb, ctx, "poly", "broken2.example.com", "test")
	domains = cb.GetBrokenDomains(ctx, "poly")
	if !domains["broken2.example.com"] {
		t.Fatal("expected broken2.example.com to be broken for poly")
	}

	// Original service still has its domain
	domains = cb.GetBrokenDomains(ctx, "eth")
	if !domains["broken.example.com"] {
		t.Fatal("expected broken.example.com still broken for eth")
	}
}

func TestDomainCircuitBreaker_TTLExpiry(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 50 * time.Millisecond  // Short TTL for testing
	cb.cacheTTL = 10 * time.Millisecond    // Short cache TTL so refresh happens quickly
	ctx := context.Background()

	breakDomain(cb, ctx, "eth", "expired.example.com", "test")

	// Should be broken immediately
	domains := cb.GetBrokenDomains(ctx, "eth")
	if !domains["expired.example.com"] {
		t.Fatal("expected domain to be broken immediately after marking")
	}

	// Wait for TTL to expire + cache to go stale
	time.Sleep(70 * time.Millisecond)

	// After TTL, domain should be expired (refreshLocal cleans it up)
	domains = cb.GetBrokenDomains(ctx, "eth")
	if domains["expired.example.com"] {
		t.Fatal("expected domain to be expired after TTL")
	}
}

func TestDomainCircuitBreaker_CacheRefresh(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.cacheTTL = 20 * time.Millisecond // Short cache TTL
	ctx := context.Background()

	breakDomain(cb, ctx, "eth", "domain1.example.com", "test")

	// First read caches the result
	domains := cb.GetBrokenDomains(ctx, "eth")
	if !domains["domain1.example.com"] {
		t.Fatal("expected domain1 to be broken")
	}

	// Wait for cache to go stale
	time.Sleep(30 * time.Millisecond)

	// Mark another domain — this goes into cache directly
	breakDomain(cb, ctx, "eth", "domain2.example.com", "test")

	// Next read should trigger refresh and include both
	domains = cb.GetBrokenDomains(ctx, "eth")
	if !domains["domain1.example.com"] {
		t.Fatal("expected domain1 to still be broken after refresh")
	}
	if !domains["domain2.example.com"] {
		t.Fatal("expected domain2 to be broken after refresh")
	}
}

func TestFilterEndpointsByBrokenDomains(t *testing.T) {
	endpoints := protocol.EndpointAddrList{
		"pokt1abc-https://rel.spacebelt.xyz:443",
		"pokt1def-https://rel.spacebelt.xyz:443",
		"pokt1ghi-https://kleomedes.example.com:443",
	}

	brokenDomains := map[string]bool{
		"rel.spacebelt.xyz": true,
	}

	filtered := filterEndpointsByBrokenDomains(endpoints, brokenDomains)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 endpoint after filtering, got %d", len(filtered))
	}
	if string(filtered[0]) != "pokt1ghi-https://kleomedes.example.com:443" {
		t.Fatalf("expected kleomedes endpoint, got %s", filtered[0])
	}
}

func TestFilterEndpointsByBrokenDomains_AllBroken(t *testing.T) {
	endpoints := protocol.EndpointAddrList{
		"pokt1abc-https://rel.spacebelt.xyz:443",
		"pokt1def-https://rel.spacebelt.xyz:443",
	}

	brokenDomains := map[string]bool{
		"rel.spacebelt.xyz": true,
	}

	filtered := filterEndpointsByBrokenDomains(endpoints, brokenDomains)
	// All endpoints are from broken domain — returns empty
	// Caller is responsible for graceful degradation (keeping original list)
	if len(filtered) != 0 {
		t.Fatalf("expected 0 endpoints when all are broken, got %d", len(filtered))
	}
}

func TestFilterEndpointsByBrokenDomains_NoBroken(t *testing.T) {
	endpoints := protocol.EndpointAddrList{
		"pokt1abc-https://healthy1.example.com:443",
		"pokt1def-https://healthy2.example.com:443",
	}

	brokenDomains := map[string]bool{}

	filtered := filterEndpointsByBrokenDomains(endpoints, brokenDomains)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 endpoints when none are broken, got %d", len(filtered))
	}
}

func TestDomainCircuitBreaker_ConcurrentAccess(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	ctx := context.Background()

	// Concurrent writes and reads should not panic
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			cb.MarkBroken(ctx, "eth", "domain.example.com", "test")
			cb.GetBrokenDomains(ctx, "eth")
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should have the domain
	domains := cb.GetBrokenDomains(ctx, "eth")
	if !domains["domain.example.com"] {
		t.Fatal("expected domain to be broken after concurrent access")
	}
}

// Escalation must count EPISODES, not marks.
//
// The old behavior escalated whenever MarkBroken was called while the domain was already
// broken. Batch items fail concurrently on separate goroutines, so a single incident produced
// ~89 marks in production — six is enough to pin the TTL at the 30-minute cap. One transient
// burst therefore removed a domain for the maximum duration.
func TestDomainCircuitBreaker_DuplicateMarksDoNotEscalate(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 10 * time.Second
	ctx := context.Background()

	domain := "concurrent-burst.example.com"

	// One episode: enough failures to pass the gate, then a burst of further marks
	// representing the other batch items of the same incident failing simultaneously.
	breakDomain(cb, ctx, "eth", domain, "batch_transport_error: boom")
	for i := 0; i < 200; i++ {
		cb.MarkBroken(ctx, "eth", domain, "batch_transport_error: boom")
	}

	cb.mu.RLock()
	state := cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()

	if state.hitCount != 1 {
		t.Fatalf("one incident must be one episode: hitCount=%d, want 1", state.hitCount)
	}
	if got := cb.escalatedTTL(state.hitCount); got != cb.defaultTTL {
		t.Fatalf("TTL escalated on duplicate marks: got %v, want base %v", got, cb.defaultTTL)
	}
}

// Escalation must still punish a domain that breaks AGAIN after being let back in — that is
// the case exponential backoff exists for.
func TestDomainCircuitBreaker_EscalatesAcrossEpisodes(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 30 * time.Millisecond
	cb.maxTTL = 3200 * time.Millisecond
	cb.cacheTTL = 1 * time.Millisecond
	cb.failureWindow = time.Millisecond // roll the rate window fast so each episode is fresh
	ctx := context.Background()

	domain := "repeat-offender.example.com"

	for episode := 1; episode <= 3; episode++ {
		breakDomain(cb, ctx, "eth", domain, "test")

		cb.mu.RLock()
		state := cb.cache["eth"].domains[domain]
		cb.mu.RUnlock()
		if state.hitCount != episode {
			t.Fatalf("episode %d: hitCount=%d, want %d", episode, state.hitCount, episode)
		}

		// Let the break expire so the next round is a genuine re-offence.
		time.Sleep(cb.escalatedTTL(episode) + 20*time.Millisecond)
		cb.GetBrokenDomains(ctx, "eth") // drives expiry cleanup
	}
}

// Break history must survive the break's own expiry, or every re-offence looks like a first
// offence and the backoff never engages. The old code reset to 1 on expiry, which meant a
// chronically-broken domain was only ever removed for the base TTL.
func TestDomainCircuitBreaker_EscalationMemoryExpires(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 10 * time.Millisecond
	cb.cacheTTL = 1 * time.Millisecond
	cb.failureWindow = time.Millisecond
	// Generous memory for the "within memory" leg: the only thing under test there is that
	// escalation survives a TTL expiry, not any particular duration. A tight bound here
	// (e.g. 2x the sleep) makes the test fail whenever the sleep overshoots under -race or
	// load, which says nothing about the code.
	cb.escalationMemory = 10 * time.Second
	ctx := context.Background()

	domain := "forgiven.example.com"

	breakDomain(cb, ctx, "eth", domain, "test")
	time.Sleep(20 * time.Millisecond)
	cb.GetBrokenDomains(ctx, "eth")

	// Within memory → escalates.
	breakDomain(cb, ctx, "eth", domain, "test")
	cb.mu.RLock()
	state := cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 2 {
		t.Fatalf("within escalation memory: hitCount=%d, want 2", state.hitCount)
	}

	// Beyond memory → forgiven, back to a first offence. Shrink the memory rather than
	// sleeping it out, so this leg cannot be perturbed by scheduling either: any elapsed
	// time now exceeds it.
	cb.escalationMemory = time.Nanosecond
	time.Sleep(30 * time.Millisecond)
	cb.GetBrokenDomains(ctx, "eth")
	breakDomain(cb, ctx, "eth", domain, "test")
	cb.mu.RLock()
	state = cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 1 {
		t.Fatalf("beyond escalation memory: hitCount=%d, want 1 (forgiven)", state.hitCount)
	}
}

func TestDomainCircuitBreaker_EscalatedTTLValues(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 1 * time.Minute
	cb.maxTTL = 30 * time.Minute

	// Verify the exact TTL progression
	expected := []time.Duration{
		1 * time.Minute,  // hit 1
		2 * time.Minute,  // hit 2
		4 * time.Minute,  // hit 3
		8 * time.Minute,  // hit 4
		16 * time.Minute, // hit 5
		30 * time.Minute, // hit 6 (capped)
		30 * time.Minute, // hit 7 (still capped)
		30 * time.Minute, // hit 100 (still capped)
	}
	hits := []int{1, 2, 3, 4, 5, 6, 7, 100}

	for i, hitCount := range hits {
		ttl := cb.escalatedTTL(hitCount)
		if ttl != expected[i] {
			t.Errorf("hit %d: expected TTL=%v, got %v", hitCount, expected[i], ttl)
		}
	}
}

func TestDomainCircuitBreaker_TTLCapAt30Min(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 1 * time.Minute
	cb.maxTTL = 30 * time.Minute

	// hit 6: 1min * 2^5 = 32min → capped at 30min
	ttl := cb.escalatedTTL(6)
	if ttl != 30*time.Minute {
		t.Fatalf("expected 30min cap, got %v", ttl)
	}

	// hit 10: still capped
	ttl = cb.escalatedTTL(10)
	if ttl != 30*time.Minute {
		t.Fatalf("expected 30min cap for hit 10, got %v", ttl)
	}
}

func TestParseRedisValue_CurrentFormat(t *testing.T) {
	// Current format: "unixSeconds:hitCount:reason"
	expiry, hitCount, reason, ok := parseRedisValue("1709000000:5:heuristic: html_error_page")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if expiry != 1709000000 {
		t.Fatalf("expected expiry=1709000000, got %d", expiry)
	}
	if hitCount != 5 {
		t.Fatalf("expected hitCount=5, got %d", hitCount)
	}
	if reason != "heuristic: html_error_page" {
		t.Fatalf("expected reason='heuristic: html_error_page', got %q", reason)
	}
}

func TestParseRedisValue_ReasonWithColons(t *testing.T) {
	// Reason itself may contain colons (e.g., "retry: heuristic: ... | status=200 | response=...")
	expiry, hitCount, reason, ok := parseRedisValue("1709000000:3:retry: heuristic: bad | status=200")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if expiry != 1709000000 {
		t.Fatalf("expected expiry=1709000000, got %d", expiry)
	}
	if hitCount != 3 {
		t.Fatalf("expected hitCount=3, got %d", hitCount)
	}
	if reason != "retry: heuristic: bad | status=200" {
		t.Fatalf("expected full reason with colons, got %q", reason)
	}
}

func TestParseRedisValue_LegacyFormatWithHitCount(t *testing.T) {
	// Legacy format: "unixSeconds:hitCount" (no reason)
	expiry, hitCount, reason, ok := parseRedisValue("1709000000:5")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if expiry != 1709000000 {
		t.Fatalf("expected expiry=1709000000, got %d", expiry)
	}
	if hitCount != 5 {
		t.Fatalf("expected hitCount=5, got %d", hitCount)
	}
	if reason != "" {
		t.Fatalf("expected empty reason for legacy format, got %q", reason)
	}
}

func TestParseRedisValue_OldestFormat(t *testing.T) {
	// Oldest format: just unix timestamp — should parse with hitCount=1
	expiry, hitCount, reason, ok := parseRedisValue("1709000000")
	if !ok {
		t.Fatal("expected parse to succeed for oldest format")
	}
	if expiry != 1709000000 {
		t.Fatalf("expected expiry=1709000000, got %d", expiry)
	}
	if hitCount != 1 {
		t.Fatalf("expected hitCount=1 for oldest format, got %d", hitCount)
	}
	if reason != "" {
		t.Fatalf("expected empty reason for oldest format, got %q", reason)
	}
}

func TestParseRedisValue_InvalidFormat(t *testing.T) {
	_, _, _, ok := parseRedisValue("not-a-number")
	if ok {
		t.Fatal("expected parse to fail for invalid format")
	}

	_, _, _, ok = parseRedisValue("abc:def")
	if ok {
		t.Fatal("expected parse to fail for invalid new format")
	}

	_, _, _, ok = parseRedisValue("")
	if ok {
		t.Fatal("expected parse to fail for empty string")
	}
}

// TestClassifyCircuitBreakReason verifies that the free-text reason strings
// passed to MarkBroken get bucketed into the correct bounded category for use
// as a Prometheus label. Order-sensitive prefix matches must be stable —
// "parallel_retry" must NOT match the "retry" branch.
func TestClassifyCircuitBreakReason(t *testing.T) {
	cases := []struct {
		raw      string
		expected string
	}{
		{"retry: heuristic_html | status=502 | response=<html>...", metrics.CircuitBreakReasonRetry},
		{"batch_transport_error: connection refused", metrics.CircuitBreakReasonBatchTransport},
		{"batch_heuristic: empty_array | status=200 | response=[]", metrics.CircuitBreakReasonBatchHeuristic},
		{"parallel_retry: timeout after 5s", metrics.CircuitBreakReasonParallelRetry},
		{"heuristic: structured error response", metrics.CircuitBreakReasonHeuristic},
		{"completely unknown reason format", metrics.CircuitBreakReasonUnknown},
		{"", metrics.CircuitBreakReasonUnknown},
	}
	for _, c := range cases {
		got := classifyCircuitBreakReason(c.raw)
		if got != c.expected {
			t.Errorf("classifyCircuitBreakReason(%q) = %q, want %q", c.raw, got, c.expected)
		}
	}
}

// TestCircuitBreakerEventsCounter verifies that broken/recovered transitions
// increment the events counter with the correct reason_category bucket.
func TestCircuitBreakerEventsCounter(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 50 * time.Millisecond
	ctx := context.Background()
	const serviceID = "events-test-svc"
	const domain = "events-test.example.com"

	// Capture pre-state of the counter for the expected (service, domain, retry, broken)
	// label combination so the test is hermetic against other tests' increments.
	preBroken := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventBroken,
	))
	preRecovered := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventRecovered,
	))

	preSuppressed := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventSuppressed,
	))
	preDuplicate := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventDuplicate,
	))

	const reason = "retry: heuristic_html | status=502 | response=oops"

	// Triggers below the failure-rate gate are counted as "suppressed", not "broken" —
	// otherwise a mis-tuned gate silently swallowing real failures is invisible.
	for i := 0; i < defaultMinFailures-1; i++ {
		cb.MarkBroken(ctx, serviceID, domain, reason)
	}
	if got := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventSuppressed,
	)) - preSuppressed; got != defaultMinFailures-1 {
		t.Fatalf("expected %d suppressed events, got %v", defaultMinFailures-1, got)
	}
	if got := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventBroken,
	)) - preBroken; got != 0 {
		t.Fatalf("suppressed triggers must not count as breaks, got %v", got)
	}

	// The trigger that crosses the gate records exactly one broken event: one episode.
	cb.MarkBroken(ctx, serviceID, domain, reason)
	postBroken := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventBroken,
	))
	if postBroken-preBroken != 1 {
		t.Fatalf("expected broken counter to increment by 1, got delta=%v", postBroken-preBroken)
	}

	// Further triggers while already broken are duplicates of the same episode. Counting
	// them separately is what keeps broken:recovered ~1:1 — it used to run ~89:1 in
	// production purely from concurrent batch items marking the same incident.
	for i := 0; i < 10; i++ {
		cb.MarkBroken(ctx, serviceID, domain, reason)
	}
	if got := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventDuplicate,
	)) - preDuplicate; got != 10 {
		t.Fatalf("expected 10 duplicate events, got %v", got)
	}
	if got := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventBroken,
	)) - preBroken; got != 1 {
		t.Fatalf("duplicate triggers must not count as new breaks, got %v", got)
	}

	// ClearService should record one recovered event with the same reason_category
	cb.ClearService(ctx, serviceID)
	postRecovered := testutil.ToFloat64(metrics.DomainCircuitBreakerEventsTotal.WithLabelValues(
		serviceID, domain, metrics.CircuitBreakReasonRetry, metrics.CircuitBreakerEventRecovered,
	))
	if postRecovered-preRecovered != 1 {
		t.Fatalf("expected recovered counter to increment by 1, got delta=%v", postRecovered-preRecovered)
	}
}

// TestDomainCircuitBreaker_MetricGaugeTransitions verifies that the circuit-breaker
// state gauge moves between 0 and 1 correctly: set to 1 on MarkBroken, dropped to 0
// on ClearService, and dropped to 0 by refreshLocal once a TTL expires. We test
// directly against the Prometheus client metric rather than scraping /metrics so the
// test stays hermetic.
func TestDomainCircuitBreaker_MetricGaugeTransitions(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 50 * time.Millisecond
	ctx := context.Background()
	const serviceID = "metric-test-svc"
	const domain = "metric-test-domain.example.com"

	// Initial state: gauge should be 0 (or absent — the Set on first MarkBroken creates it).
	if v := readCircuitBreakerGauge(serviceID, domain); v != 0 {
		t.Fatalf("expected gauge=0 initially, got %v", v)
	}

	// MarkBroken → gauge should flip to 1
	breakDomain(cb, ctx, serviceID, domain, "test_reason")
	if v := readCircuitBreakerGauge(serviceID, domain); v != 1 {
		t.Fatalf("expected gauge=1 after MarkBroken, got %v", v)
	}

	// ClearService → gauge drops to 0 for cleared domains
	cb.ClearService(ctx, serviceID)
	if v := readCircuitBreakerGauge(serviceID, domain); v != 0 {
		t.Fatalf("expected gauge=0 after ClearService, got %v", v)
	}

	// MarkBroken again, then wait for TTL expiry, then refresh — gauge should drop to 0
	breakDomain(cb, ctx, serviceID, domain, "test_reason_2")
	if v := readCircuitBreakerGauge(serviceID, domain); v != 1 {
		t.Fatalf("expected gauge=1 after second MarkBroken, got %v", v)
	}

	time.Sleep(60 * time.Millisecond)
	// Force the cache entry to look stale so GetBrokenDomains takes the refresh path.
	// In production, the same effect happens automatically once cacheTTL elapses
	// (default 5s) after the entry was last refreshed.
	cb.mu.Lock()
	if entry, ok := cb.cache[serviceID]; ok {
		entry.refreshAt = time.Now().Add(-time.Second)
	}
	cb.mu.Unlock()
	cb.GetBrokenDomains(ctx, serviceID)
	if v := readCircuitBreakerGauge(serviceID, domain); v != 0 {
		t.Fatalf("expected gauge=0 after TTL expiry + refresh, got %v", v)
	}
}

// The production regression this gate exists for.
//
// An operator sustaining 99.26% success on a batch-heavy service was circuit-broken ~240
// times in 3 hours, removing 90% of one service's endpoint pool (45 of 50 endpoints sat
// behind 7 hostnames). The trigger was first-error: any single failed batch item removed the
// whole hostname. That is volume-sensitive, not quality-sensitive — the operator with the
// most endpoints receives the most traffic, so it reaches its first error soonest after every
// TTL expiry and is effectively locked out permanently.
func TestDomainCircuitBreaker_HighVolumeLowErrorRateIsNotBroken(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.failureWindow = time.Hour // one window for the whole test
	ctx := context.Background()

	const domain = "high-volume.example.com"

	// 10,000 relays at the measured 0.74% failure rate.
	for i := 0; i < 10000; i++ {
		if i%135 == 0 {
			cb.MarkBroken(ctx, "blast", domain, "batch_transport_error: transient")
		} else {
			cb.RecordSuccess("blast", domain)
		}
	}

	if broken := cb.GetBrokenDomains(ctx, "blast"); broken[domain] {
		t.Fatalf("a domain succeeding >99%% of the time must not be removed from the pool")
	}
}

// The gate must not become a way for a genuinely dead host to keep serving.
func TestDomainCircuitBreaker_GenuinelyFailingDomainIsBroken(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	ctx := context.Background()

	const domain = "dead-host.example.com"
	for i := 0; i < defaultMinFailures; i++ {
		cb.MarkBroken(ctx, "blast", domain, "batch_transport_error: connection refused")
	}

	if broken := cb.GetBrokenDomains(ctx, "blast"); !broken[domain] {
		t.Fatalf("a domain failing every request must be removed from the pool")
	}
}

// Below minFailures nothing breaks, so a lone failure on a quiet domain is not a 100% rate.
func TestDomainCircuitBreaker_SingleFailureDoesNotBreak(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	ctx := context.Background()

	cb.MarkBroken(ctx, "blast", "quiet.example.com", "batch_transport_error: one-off")

	if broken := cb.GetBrokenDomains(ctx, "blast"); broken["quiet.example.com"] {
		t.Fatal("a single failure must not remove a domain from the pool")
	}
}

// A domain that recovers must be judged on its behavior after it returns, not on the failures
// that removed it — otherwise the counts that caused one break also cause the next.
func TestDomainCircuitBreaker_WindowResetsAfterBreak(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 10 * time.Millisecond
	cb.cacheTTL = time.Millisecond
	cb.failureWindow = time.Hour
	ctx := context.Background()

	const domain = "recovering.example.com"
	breakDomain(cb, ctx, "blast", domain, "test")

	time.Sleep(20 * time.Millisecond)
	cb.GetBrokenDomains(ctx, "blast") // drive expiry

	// Healthy again: one isolated failure among many successes must not re-break it.
	for i := 0; i < 500; i++ {
		cb.RecordSuccess("blast", domain)
	}
	cb.MarkBroken(ctx, "blast", domain, "test")

	if broken := cb.GetBrokenDomains(ctx, "blast"); broken[domain] {
		t.Fatal("stale pre-break failures must not count toward the next break")
	}
}
