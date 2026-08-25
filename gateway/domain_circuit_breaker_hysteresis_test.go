package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/pokt-network/path/metrics"
)

// driveAtRate feeds the gate `total` outcomes with `failPct` of them failing, interleaved so
// the running rate is representative rather than front-loaded with failures. Returns whether
// the domain is broken at the end, read through the PRODUCTION caller.
//
// Asserting through GetBrokenDomains rather than shouldBreak is deliberate: shouldBreak is
// where the change lives, so a test that calls it directly would pass on a version whose
// verdict never reaches selection.
func driveAtRate(t *testing.T, cb *DomainCircuitBreaker, ctx context.Context, serviceID, domain string, total, failPct int) bool {
	t.Helper()
	acc := 0
	for i := 0; i < total; i++ {
		acc += failPct
		if acc >= 100 {
			acc -= 100
			cb.MarkBroken(ctx, serviceID, domain, "retry: simulated")
			// Stop at the break. Continuing would keep feeding the window while the domain
			// is broken, and MarkBroken short-circuits as "duplicate" in that state without
			// recording the failure — so successes would accumulate unopposed and poison the
			// NEXT episode's rate. Production cannot reach that state: a broken domain is
			// filtered out of selection and receives nothing.
			if cb.GetBrokenDomains(ctx, serviceID)[domain] {
				return true
			}
		} else {
			cb.RecordSuccess(serviceID, domain)
		}
	}
	return cb.GetBrokenDomains(ctx, serviceID)[domain]
}

// A domain whose failure rate sits just above the break threshold must be removed ONCE, and
// must NOT be removed again every time its TTL lets it back in.
//
// This is the measured production case (solana, 2026-08-21): an operator's relay-miner hosts
// sat at 78.3-79.7% success against an 80% line and flapped — broken 71-86% of the time,
// while one of them served 40 consecutive probes with zero errors at latency level with the
// operator carrying 99% of the service.
func TestCircuitBreaker_MarginalDomainDoesNotFlap(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 20 * time.Millisecond
	cb.cacheTTL = time.Millisecond
	cb.failureWindow = time.Hour // one window for the whole test; isolate hysteresis from rollover
	ctx := context.Background()

	const domain = "marginal.example.com"

	// 21% failure — just past the 20% threshold, the shape of the marginal hosts.
	if !driveAtRate(t, cb, ctx, "solana", domain, 200, 21) {
		t.Fatal("a domain over the threshold must break the first time")
	}

	// Let the break expire, exactly as it does in production.
	time.Sleep(cb.defaultTTL + 10*time.Millisecond)
	if cb.GetBrokenDomains(ctx, "solana")[domain] {
		t.Fatal("break did not expire; test cannot measure re-break")
	}

	// Same behaviour again. It is still marginal, not newly worse.
	if driveAtRate(t, cb, ctx, "solana", domain, 200, 21) {
		t.Fatal("marginal domain re-broke after readmission: this is the production flap — " +
			"it never gets the traffic to prove itself and is held out for escalating TTLs")
	}
}

// The margin must not disarm the breaker. A domain that is genuinely far past the line has to
// break again after readmission, and has to escalate — otherwise this trades one bug for a
// worse one.
//
// Production shape: two hosts at 49.7% and 58.6% success, against the same 80% line as the
// marginal hosts above. The whole point of the margin is that these two populations separate.
func TestCircuitBreaker_BadlyBrokenDomainStillRebreaks(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 20 * time.Millisecond
	cb.cacheTTL = time.Millisecond
	cb.failureWindow = time.Hour
	ctx := context.Background()

	const domain = "genuinely-broken.example.com"

	if !driveAtRate(t, cb, ctx, "solana", domain, 200, 50) {
		t.Fatal("50% failure must break")
	}

	time.Sleep(cb.defaultTTL + 10*time.Millisecond)
	if cb.GetBrokenDomains(ctx, "solana")[domain] {
		t.Fatal("break did not expire; test cannot measure re-break")
	}

	if !driveAtRate(t, cb, ctx, "solana", domain, 200, 50) {
		t.Fatal("a domain at 50% failure must STILL break after readmission — the hysteresis " +
			"margin is meant to spare marginal domains, not broken ones")
	}

	cb.mu.RLock()
	state := cb.cache["solana"].domains[domain]
	cb.mu.RUnlock()
	if state.hitCount != 2 {
		t.Fatalf("re-break must escalate: hitCount=%d, want 2", state.hitCount)
	}
}

// A totally dead host — no successes at all — must break on the first episode AND on every
// readmission. This is a real production shape (one host had 0 successes in 82 lifetime
// attempts) and is the case an attempt-count floor would have wrongly spared; that idea was
// tried and dropped because of this test.
func TestCircuitBreaker_DeadHostBreaksWithNoSuccesses(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 20 * time.Millisecond
	cb.cacheTTL = time.Millisecond
	cb.failureWindow = time.Hour
	ctx := context.Background()

	const domain = "dead.example.com"

	breakDomain(cb, ctx, "solana", domain, "retry: dead")
	if !cb.GetBrokenDomains(ctx, "solana")[domain] {
		t.Fatal("100% failure must break with no successes recorded")
	}

	time.Sleep(cb.defaultTTL + 10*time.Millisecond)
	cb.GetBrokenDomains(ctx, "solana")

	breakDomain(cb, ctx, "solana", domain, "retry: dead")
	if !cb.GetBrokenDomains(ctx, "solana")[domain] {
		t.Fatal("a host with no successes must re-break; 100% is far past threshold+margin")
	}
}

// The margin applies only within escalationMemory. Once a domain has behaved long enough for
// its history to lapse, it is a first offender again and the ordinary threshold applies —
// otherwise the margin would be a permanent grant of leniency to anything that ever broke.
func TestCircuitBreaker_MarginLapsesWithEscalationMemory(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.defaultTTL = 10 * time.Millisecond
	cb.cacheTTL = time.Millisecond
	cb.failureWindow = time.Hour
	cb.escalationMemory = 30 * time.Millisecond
	ctx := context.Background()

	const domain = "lapsed.example.com"

	if !driveAtRate(t, cb, ctx, "solana", domain, 200, 21) {
		t.Fatal("first break expected")
	}

	// Outlive both the break and the escalation memory.
	time.Sleep(cb.escalationMemory + 20*time.Millisecond)
	cb.GetBrokenDomains(ctx, "solana")

	if !driveAtRate(t, cb, ctx, "solana", domain, 200, 21) {
		t.Fatal("after escalation memory lapses the domain is a first offender again and the " +
			"plain threshold must apply")
	}
}

// The gate's inputs must be observable at the granularity the gate DECIDES at.
//
// path_relays_total keys on eTLD+1, so an operator running several relay miners under one
// domain reports one blended rate and a per-host verdict cannot be checked against it. This
// asserts the new counter keys on the full hostname instead, and that both sides of the
// fraction are recorded — a numerator with no denominator is how the pre-rate-gate breaker
// made every single failure look like a 100% failure rate.
func TestCircuitBreaker_OutcomeMetricIsKeyedOnHostname(t *testing.T) {
	cb := NewDomainCircuitBreaker(nil, testCircuitBreakerLogger())
	cb.failureWindow = time.Hour
	ctx := context.Background()

	// Two hosts under ONE registrable domain, the shape that motivated the metric.
	const good = "host-a.relayminer.example.com"
	const bad = "host-b.relayminer.example.com"

	for i := 0; i < 12; i++ {
		cb.RecordSuccess("solana", good)
	}
	for i := 0; i < defaultMinFailures; i++ {
		cb.MarkBroken(ctx, "solana", bad, "retry: boom")
	}

	read := func(domain, outcome string) float64 {
		c, err := metrics.CircuitBreakerOutcomeTotal.GetMetricWithLabelValues("solana", domain, outcome)
		if err != nil {
			t.Fatalf("metric lookup failed for %s/%s: %v", domain, outcome, err)
		}
		var m dto.Metric
		if err := c.(prometheus.Metric).Write(&m); err != nil {
			t.Fatalf("metric write failed: %v", err)
		}
		return m.GetCounter().GetValue()
	}

	if got := read(good, metrics.CircuitBreakerOutcomeSuccess); got != 12 {
		t.Fatalf("successes for the healthy host: got %v, want 12 — the gate's DENOMINATOR "+
			"must be visible, not just its failures", got)
	}
	if got := read(bad, metrics.CircuitBreakerOutcomeFailure); got != float64(defaultMinFailures) {
		t.Fatalf("failures for the failing host: got %v, want %d", got, defaultMinFailures)
	}
	// The distinction the metric exists for: one host's failures must not be attributed to
	// the sibling sharing its registrable domain.
	if got := read(good, metrics.CircuitBreakerOutcomeFailure); got != 0 {
		t.Fatalf("healthy host was charged %v failures from its domain sibling — the counter "+
			"has collapsed to eTLD+1 and answers nothing path_relays_total does not", got)
	}
}
