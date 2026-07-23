package reputation

import (
	"context"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"
)

// newRateTestService builds a reputation service backed by the in-memory mock store,
// with the standard defaults (InitialScore 80, MinThreshold 30). The rate-cooldown
// constants (CriticalRateThreshold=0.30, CriticalRateMinObservations=20, alpha=0.05)
// are package constants, so these tests exercise the shipped defaults directly.
func newRateTestService(t *testing.T) (*service, context.Context) {
	t.Helper()
	ctx := context.Background()
	store := newMockStorage()
	t.Cleanup(func() { _ = store.Close() })

	config := Config{Enabled: true, InitialScore: 80, MinThreshold: 30}
	config.HydrateDefaults()

	svcIface := NewService(config, store)
	require.NoError(t, svcIface.Start(ctx))
	t.Cleanup(func() { _ = svcIface.Stop() })

	return svcIface.(*service), ctx
}

// TestRateCooldown_VolumeIndependence_TheBug is the core test: it reproduces the exact
// "each success helps them, so bad QoS can't be filtered" scenario and proves the rate
// detector closes it.
//
// Pattern: strictly alternating critical, success, critical, success... (a sustained 50%
// critical rate). Under the consecutive-strike system this NEVER trips: each success
// decays 3 strikes, so a critical (+1) followed by a success (-3, floored at 0) keeps
// CriticalStrikes pinned at 0/1 — it can never climb to the threshold of 5. A high-volume
// endpoint bleeding this steady rate is therefore invisible to the burst filter no matter
// how long it runs. The rate detector, keyed on the sustained RATE rather than a
// success-refillable counter, must cool it down.
func TestRateCooldown_VolumeIndependence_TheBug(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("eth", "chronic-bad", sharedtypes.RPCType_JSON_RPC)

	for i := 0; i < 60; i++ {
		if i%2 == 0 {
			require.NoError(t, svc.RecordSignal(ctx, key, NewCriticalErrorSignal("5xx", 100*time.Millisecond)))
		} else {
			require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond)))
		}
	}

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)

	// The endpoint is cooled down...
	require.True(t, score.IsInCooldown(), "sustained 50%% critical rate must trip the rate cooldown")
	// ...and it was NOT the burst-strike path: strikes never reached the threshold,
	// proving the RATE detector fired where the counter-based one structurally cannot.
	require.Less(t, score.CriticalStrikes, DefaultStrikeThreshold,
		"burst strikes must stay below threshold on alternating C/S — the cooldown here came from the rate detector, not the strike counter")
}

// TestRateCooldown_HealthCheckSignalsExcluded verifies that a sustained CRITICAL rate made
// up entirely of health-check probes does NOT trip the rate cooldown — a hard bench must
// reflect user experience, not a strict/flaky probe (e.g. a Solana getBlockHeight sync check
// failing an endpoint that serves user reads at 99.8%). The same critical rate from user
// traffic WOULD trip it (see TestRateCooldown_VolumeIndependence_TheBug), so this isolates
// the health-check exclusion.
func TestRateCooldown_HealthCheckSignalsExcluded(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("solana", "probe-fails-users-fine", sharedtypes.RPCType_JSON_RPC)

	// 60 signals at a 50% critical rate — but every one is a health-check probe.
	for i := 0; i < 60; i++ {
		var sig Signal
		if i%2 == 0 {
			sig = NewCriticalErrorSignal("getBlockHeight sync check", 100*time.Millisecond)
		} else {
			sig = NewSuccessSignal(100 * time.Millisecond)
		}
		sig.IsHealthCheck = true
		require.NoError(t, svc.RecordSignal(ctx, key, sig))
	}

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.False(t, score.IsInCooldown(),
		"health-check-only critical rate must NOT trip the rate cooldown")
	require.Equal(t, 0.0, score.RecentCriticalRate,
		"health-check signals must not move the user-traffic critical-rate EWMA")
}

// TestRateCooldown_HealthyEndpointNeverTrips verifies a high-volume endpoint with only a
// tiny, well-spaced critical rate (~2.5%) is never cooled — the rate EWMA decays back down
// between rare failures and stays far below the threshold. This is the false-positive guard:
// serving lots of good traffic must not, on its own, put an endpoint at risk.
func TestRateCooldown_HealthyEndpointNeverTrips(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("eth", "healthy-highvol", sharedtypes.RPCType_JSON_RPC)

	// 200 requests, one critical every 40 (2.5% rate), rest fast successes.
	for i := 0; i < 200; i++ {
		if i%40 == 0 {
			require.NoError(t, svc.RecordSignal(ctx, key, NewCriticalErrorSignal("blip", 100*time.Millisecond)))
		} else {
			require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(50*time.Millisecond)))
		}
	}

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.False(t, score.IsInCooldown(), "a healthy 2.5%% critical rate must never trip the rate cooldown")
	require.Less(t, score.RecentCriticalRate, CriticalRateThreshold,
		"EWMA must stay well below threshold for a healthy endpoint")
}

// TestRateCooldown_NoTripOnTinySample verifies the minimum-observations guard: an endpoint
// whose very first signals are critical is not cooled on a tiny, statistically-meaningless
// sample. (Kept below the burst threshold so this isolates the rate path, not the strike
// path.)
func TestRateCooldown_NoTripOnTinySample(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("eth", "fresh", sharedtypes.RPCType_JSON_RPC)

	// 3 criticals as the first 3 signals: EWMA is still low and total observations (3)
	// are far under CriticalRateMinObservations (20).
	for i := 0; i < 3; i++ {
		require.NoError(t, svc.RecordSignal(ctx, key, NewCriticalErrorSignal("early", 100*time.Millisecond)))
	}

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.False(t, score.IsInCooldown(), "must not cool an endpoint on a tiny early sample")
	require.Less(t, score.CriticalStrikes, DefaultStrikeThreshold, "burst path must not have fired either")
}

// TestRateCooldown_EWMATracksAndPersists verifies the RecentCriticalRate EWMA is actually
// updated on the score (and therefore written to storage — mockStorage round-trips the
// Score by value, exercising the same field the Redis serializer persists). A run of
// criticals below the min-observations gate should raise the EWMA without yet tripping.
func TestRateCooldown_EWMATracksAndPersists(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("eth", "ewma", sharedtypes.RPCType_JSON_RPC)

	// A few criticals raise the EWMA above zero; kept under both the burst threshold and
	// the min-observations gate so nothing trips and the EWMA is observable.
	for i := 0; i < 4; i++ {
		require.NoError(t, svc.RecordSignal(ctx, key, NewCriticalErrorSignal("x", 100*time.Millisecond)))
	}

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Greater(t, score.RecentCriticalRate, 0.0, "EWMA must reflect observed criticals")
	require.Less(t, score.RecentCriticalRate, CriticalRateThreshold, "should not have tripped yet")
	require.False(t, score.IsInCooldown())
}

// TestRateCooldown_EscalatesOnRepeatTrips verifies the escalating backoff: a persistently
// bad endpoint that re-trips shortly after its previous cooldown gets a longer bench each
// time (base one session → double → …), while a one-off trip only costs the base.
func TestRateCooldown_EscalatesOnRepeatTrips(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("eth", "repeat-offender", sharedtypes.RPCType_JSON_RPC)

	// feedUntilTrip drives a sustained high critical rate until the trip counter advances,
	// then returns the cooldown duration that trip produced.
	feedUntilTrip := func(wantCount int) time.Duration {
		for i := 0; i < 400; i++ {
			if i%2 == 0 {
				require.NoError(t, svc.RecordSignal(ctx, key, NewCriticalErrorSignal("5xx", 100*time.Millisecond)))
			} else {
				require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond)))
			}
			s, err := svc.GetScore(ctx, key)
			require.NoError(t, err)
			if s.RateCooldownCount == wantCount {
				return s.CooldownRemaining()
			}
		}
		t.Fatalf("endpoint never reached rate-cooldown trip #%d", wantCount)
		return 0
	}

	first := feedUntilTrip(1)
	second := feedUntilTrip(2)

	// Linear escalation: first ≈ base (10m), second ≈ 2× base (20m). Allow slack for the
	// microseconds of elapsed wall-clock in the loop.
	require.InDelta(t, DefaultRateCooldown.Minutes(), first.Minutes(), 1.0,
		"first trip should bench for ~the base cooldown")
	require.InDelta(t, (2 * DefaultRateCooldown).Minutes(), second.Minutes(), 1.5,
		"second consecutive trip should bench for ~2× the base (linear escalation)")
	require.Greater(t, second, first,
		"a repeat offender must be benched longer than the first offense")
}

// TestRateCooldown_ResetsAfterTrip verifies the EWMA is reset when the cooldown fires, so a
// recovered endpoint must re-accumulate sustained failures to trip again rather than
// re-flapping on its first post-cooldown request.
func TestRateCooldown_ResetsAfterTrip(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("eth", "reset", sharedtypes.RPCType_JSON_RPC)

	// Drive it to a trip with a sustained high rate. End on a success so the final EWMA
	// update after the reset is a decay (0 * (1-a) + a*0 = 0), leaving it at exactly 0.
	tripped := false
	for i := 0; i < 60 && !tripped; i++ {
		if i%2 == 0 {
			require.NoError(t, svc.RecordSignal(ctx, key, NewCriticalErrorSignal("5xx", 100*time.Millisecond)))
		} else {
			require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond)))
		}
		s, err := svc.GetScore(ctx, key)
		require.NoError(t, err)
		tripped = s.IsInCooldown()
	}
	require.True(t, tripped, "expected the endpoint to trip within the sustained run")

	// Feed one success (a non-critical): EWMA decays from its post-reset value toward 0 and
	// must remain below threshold — no immediate re-trip.
	require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond)))
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Less(t, score.RecentCriticalRate, CriticalRateThreshold,
		"EWMA must be reset low after a trip so the endpoint does not immediately re-flap")
}
