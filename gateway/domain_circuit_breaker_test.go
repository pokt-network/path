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
