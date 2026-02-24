package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/pokt-network/path/protocol"
)

func TestDomainCircuitBreaker_MarkAndGet(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil)
	ctx := context.Background()

	// Initially no broken domains
	domains := cb.GetBrokenDomains(ctx, "eth")
	if len(domains) != 0 {
		t.Fatalf("expected no broken domains, got %d", len(domains))
	}

	// Mark a domain as broken
	cb.MarkBroken(ctx, "eth", "rel.spacebelt.xyz")

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
	cb := NewDomainCircuitBreaker(nil)
	ctx := context.Background()

	cb.MarkBroken(ctx, "eth", "broken1.example.com")
	cb.MarkBroken(ctx, "eth", "broken2.example.com")

	domains := cb.GetBrokenDomains(ctx, "eth")
	if len(domains) != 2 {
		t.Fatalf("expected 2 broken domains, got %d", len(domains))
	}
	if !domains["broken1.example.com"] || !domains["broken2.example.com"] {
		t.Fatal("expected both domains to be broken")
	}
}

func TestDomainCircuitBreaker_LocalOnlyMode(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil) // nil Redis = local-only
	ctx := context.Background()

	// Should work without Redis
	cb.MarkBroken(ctx, "eth", "broken.example.com")
	domains := cb.GetBrokenDomains(ctx, "eth")
	if !domains["broken.example.com"] {
		t.Fatal("expected broken.example.com to be broken in local-only mode")
	}

	// Mark another domain for a different service
	cb.MarkBroken(ctx, "poly", "broken2.example.com")
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
	cb := NewDomainCircuitBreaker(nil)
	cb.defaultTTL = 50 * time.Millisecond  // Short TTL for testing
	cb.cacheTTL = 10 * time.Millisecond    // Short cache TTL so refresh happens quickly
	ctx := context.Background()

	cb.MarkBroken(ctx, "eth", "expired.example.com")

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
	cb := NewDomainCircuitBreaker(nil)
	cb.cacheTTL = 20 * time.Millisecond // Short cache TTL
	ctx := context.Background()

	cb.MarkBroken(ctx, "eth", "domain1.example.com")

	// First read caches the result
	domains := cb.GetBrokenDomains(ctx, "eth")
	if !domains["domain1.example.com"] {
		t.Fatal("expected domain1 to be broken")
	}

	// Wait for cache to go stale
	time.Sleep(30 * time.Millisecond)

	// Mark another domain — this goes into cache directly
	cb.MarkBroken(ctx, "eth", "domain2.example.com")

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
	cb := NewDomainCircuitBreaker(nil)
	ctx := context.Background()

	// Concurrent writes and reads should not panic
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			cb.MarkBroken(ctx, "eth", "domain.example.com")
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

func TestDomainCircuitBreaker_EscalatingTTL(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil)
	cb.defaultTTL = 100 * time.Millisecond
	cb.maxTTL = 3200 * time.Millisecond // cap at 32x base for testing
	cb.cacheTTL = 1 * time.Millisecond  // fast cache refresh
	ctx := context.Background()

	domain := "repeat-offender.example.com"

	// Hit 1: base TTL (100ms)
	cb.MarkBroken(ctx, "eth", domain)
	cb.mu.RLock()
	state := cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 1 {
		t.Fatalf("expected hitCount=1, got %d", state.hitCount)
	}

	// Hit 2: should escalate (200ms)
	cb.MarkBroken(ctx, "eth", domain)
	cb.mu.RLock()
	state = cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 2 {
		t.Fatalf("expected hitCount=2, got %d", state.hitCount)
	}

	// Hit 3: should escalate (400ms)
	cb.MarkBroken(ctx, "eth", domain)
	cb.mu.RLock()
	state = cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 3 {
		t.Fatalf("expected hitCount=3, got %d", state.hitCount)
	}

	// Hit 4: should escalate (800ms)
	cb.MarkBroken(ctx, "eth", domain)
	cb.mu.RLock()
	state = cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 4 {
		t.Fatalf("expected hitCount=4, got %d", state.hitCount)
	}

	// Hit 5: should escalate (1600ms)
	cb.MarkBroken(ctx, "eth", domain)
	cb.mu.RLock()
	state = cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 5 {
		t.Fatalf("expected hitCount=5, got %d", state.hitCount)
	}
}

func TestDomainCircuitBreaker_EscalatedTTLValues(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil)
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
	cb := NewDomainCircuitBreaker(nil)
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

func TestDomainCircuitBreaker_HitCountResetAfterExpiry(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil)
	cb.defaultTTL = 50 * time.Millisecond
	cb.maxTTL = 30 * time.Minute
	cb.cacheTTL = 1 * time.Millisecond
	ctx := context.Background()

	domain := "reset.example.com"

	// Hit 1 and 2
	cb.MarkBroken(ctx, "eth", domain)
	cb.MarkBroken(ctx, "eth", domain)
	cb.mu.RLock()
	state := cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 2 {
		t.Fatalf("expected hitCount=2, got %d", state.hitCount)
	}

	// Wait for TTL to expire (hit 2 TTL = 100ms)
	time.Sleep(120 * time.Millisecond)

	// Hit count should reset since the previous entry expired
	cb.MarkBroken(ctx, "eth", domain)
	cb.mu.RLock()
	state = cb.cache["eth"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 1 {
		t.Fatalf("expected hitCount to reset to 1 after expiry, got %d", state.hitCount)
	}
}

func TestParseRedisValue_NewFormat(t *testing.T) {
	expiry, hitCount, ok := parseRedisValue("1709000000:5")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if expiry != 1709000000 {
		t.Fatalf("expected expiry=1709000000, got %d", expiry)
	}
	if hitCount != 5 {
		t.Fatalf("expected hitCount=5, got %d", hitCount)
	}
}

func TestParseRedisValue_LegacyFormat(t *testing.T) {
	// Old format: just unix timestamp — should parse with hitCount=1
	expiry, hitCount, ok := parseRedisValue("1709000000")
	if !ok {
		t.Fatal("expected parse to succeed for legacy format")
	}
	if expiry != 1709000000 {
		t.Fatalf("expected expiry=1709000000, got %d", expiry)
	}
	if hitCount != 1 {
		t.Fatalf("expected hitCount=1 for legacy format, got %d", hitCount)
	}
}

func TestParseRedisValue_InvalidFormat(t *testing.T) {
	_, _, ok := parseRedisValue("not-a-number")
	if ok {
		t.Fatal("expected parse to fail for invalid format")
	}

	_, _, ok = parseRedisValue("abc:def")
	if ok {
		t.Fatal("expected parse to fail for invalid new format")
	}

	_, _, ok = parseRedisValue("")
	if ok {
		t.Fatal("expected parse to fail for empty string")
	}
}
