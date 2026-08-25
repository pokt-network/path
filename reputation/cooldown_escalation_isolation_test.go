package reputation

import (
	"context"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"
)

// seedScore installs a starting Score directly, to reach a state that otherwise needs more
// than an hour of wall-clock to reproduce (the escalation windows are DefaultMaxCooldown
// wide and the service reads time.Now() with no injectable clock).
//
// This is a PRECONDITION only. Every assertion below is on the bench the endpoint receives
// afterwards, never on a field written here.
func seedScore(t *testing.T, svc *service, key EndpointKey, score Score) {
	t.Helper()
	svc.mu.Lock()
	svc.setScoreLocked(key, score)
	svc.mu.Unlock()
}

// remaining returns how long the endpoint's bench still has to run.
func remaining(t *testing.T, svc *service, ctx context.Context, key EndpointKey) time.Duration {
	t.Helper()
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	return time.Until(score.CooldownUntil)
}

// driveUntilTrip feeds a 1-in-`oneIn` protocol-violation stream ONE SIGNAL AT A TIME and
// stops the instant `stop` reports the trip under test has happened.
//
// Feeding a fixed-size batch does not work: a trip resets the detector's EWMA to zero and
// the loop keeps going, so a single 4000-request run at 1% trips FIVE times and escalates
// on each one. Any assertion about a first offence has to stop at the first offence.
func driveUntilTrip(t *testing.T, svc *service, ctx context.Context, key EndpointKey, oneIn int, stop func(Score) bool) Score {
	t.Helper()
	const maxSignals = 200000
	for i := 0; i < maxSignals; i++ {
		var err error
		if oneIn > 0 && i%oneIn == 0 {
			err = svc.RecordSignal(ctx, key, violationSignal())
		} else {
			err = svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond))
		}
		require.NoError(t, err)

		score, err := svc.GetScore(ctx, key)
		require.NoError(t, err)
		if stop(score) {
			return score
		}
	}
	t.Fatalf("detector never tripped within %d signals", maxSignals)
	return Score{}
}

// tripCountAtLeast builds a stop predicate for the invalid-rate detector. Only valid on a
// key whose count starts at zero.
func tripCountAtLeast(n int) func(Score) bool {
	return func(s Score) bool { return s.InvalidRateCooldownCount >= n }
}

// benchExtendedBeyond stops as soon as the endpoint's bench runs longer than d — i.e. a
// rate detector has extended it past whatever was already in force. Used where the
// escalation counters are seeded non-zero and so cannot themselves signal a fresh trip.
func benchExtendedBeyond(d time.Duration) func(Score) bool {
	return func(s Score) bool { return time.Until(s.CooldownUntil) > d }
}

// TestInvalidRateEscalation_IgnoresCooldownsOtherDetectorsSet is the fix-3 test.
//
// Score.InvalidRateCooldownCount is documented as "kept separate from RateCooldownCount so
// the two detectors escalate independently and a trip of one cannot be misread as a trip of
// the other". The COUNTER was separate; the timestamp it escalated against was not — both
// detectors compared against the shared Score.CooldownUntil, which the strike system also
// writes. So the intent in that comment was never actually implemented.
//
// Note the sign: time.Since() on a cooldown that is still in force is NEGATIVE, hence
// always below DefaultMaxCooldown, so ANY bench in force made the very next trip read as
// consecutive. On a service where endpoints are benched often for unrelated reasons — the
// exact population this detector is aimed at — a first offence was benched at the escalated
// duration immediately.
//
// Asserted on the bench the endpoint actually receives, not on the counter: the counter is
// what the author wrote, the duration is what selection lives with.
func TestInvalidRateEscalation_IgnoresCooldownsOtherDetectorsSet(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("solana", "stale-history-plus-foreign-bench", sharedtypes.RPCType_JSON_RPC)

	// The production shape this reproduces: an endpoint on a churn-heavy service whose
	// invalid-rate history is old enough that its escalation must RESET, but which is
	// benched right now by an unrelated mechanism (here the strike system).
	seedScore(t, svc, key, Score{
		Value:        80,
		LastUpdated:  time.Now(),
		SuccessCount: 5000,
		ErrorCount:   50,

		// Ran clean for this detector far longer than DefaultMaxCooldown, so its next trip
		// is a first offence again.
		InvalidRateCooldownCount: 3,
		InvalidRateCooldownUntil: time.Now().Add(-2 * time.Hour),

		// A bench earned by the strike system, still in force.
		CooldownUntil: time.Now().Add(5 * time.Minute),
	})

	// Stop the moment the bench is extended past the seeded 5m — the seeded count of 3
	// means the counter itself cannot mark a fresh trip.
	score := driveUntilTrip(t, svc, ctx, key, 100, benchExtendedBeyond(6*time.Minute)) // 1 in 100 = 1%
	require.True(t, score.IsInCooldown(), "the invalid-rate detector must have tripped")

	// A first offence benches for one DefaultRateCooldown (10m), which exceeds the 5m strike
	// bench and is therefore visible in CooldownUntil. Escalating off the foreign bench
	// instead resumes the stale count at 4 and benches for 40m.
	require.InDelta(t, DefaultRateCooldown.Seconds(), remaining(t, svc, ctx, key).Seconds(), 30,
		"a cooldown earned by another detector must not resume this detector's stale escalation")
	require.Equal(t, 1, score.InvalidRateCooldownCount,
		"this detector had run clean past DefaultMaxCooldown, so its count must reset to 1")
}

// TestInvalidRateEscalation_StillEscalatesOnItsOwnRepeatTrips is the other half. A fix that
// simply stopped escalating would also pass the test above while removing the backoff that
// benches a persistent offender progressively longer.
func TestInvalidRateEscalation_StillEscalatesOnItsOwnRepeatTrips(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("solana", "two-consecutive-violations", sharedtypes.RPCType_JSON_RPC)

	first := driveUntilTrip(t, svc, ctx, key, 100, tripCountAtLeast(1))
	require.True(t, first.IsInCooldown())
	require.Equal(t, 1, first.InvalidRateCooldownCount)
	require.InDelta(t, DefaultRateCooldown.Seconds(), remaining(t, svc, ctx, key).Seconds(), 30)

	// The trip reset RecentInvalidRate to 0, so the endpoint has to re-accumulate the same
	// sustained rate before it can trip again.
	second := driveUntilTrip(t, svc, ctx, key, 100, tripCountAtLeast(2))
	require.Equal(t, 2, second.InvalidRateCooldownCount,
		"a repeat trip of this detector, landing within DefaultMaxCooldown of its own previous bench, must escalate")
	require.InDelta(t, (2 * DefaultRateCooldown).Seconds(), remaining(t, svc, ctx, key).Seconds(), 30,
		"the second consecutive invalid-rate trip must bench for 2 x DefaultRateCooldown")
}

// TestCriticalRateEscalation_IgnoresCooldownsOtherDetectorsSet is the symmetric case. The
// critical-rate detector had the same defect first; the invalid-rate detector inherited it
// by copying the block. Fixing only the newer one would leave the older one escalating off
// benches it did not earn.
func TestCriticalRateEscalation_IgnoresCooldownsOtherDetectorsSet(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("eth", "stale-rate-history-plus-foreign-bench", sharedtypes.RPCType_JSON_RPC)

	// Mirror image of the test above: stale critical-rate history that must reset, plus a
	// bench in force that this detector did not earn (here from the invalid-rate detector).
	seedScore(t, svc, key, Score{
		Value:             80,
		LastUpdated:       time.Now(),
		SuccessCount:      5000,
		ErrorCount:        50,
		RateCooldownCount: 3,
		RateCooldownUntil: time.Now().Add(-2 * time.Hour),
		CooldownUntil:     time.Now().Add(5 * time.Minute),
	})

	// Drive a sustained 50% critical rate, carrying no protocol violations, so only the
	// critical-rate detector can fire.
	var score Score
	for i := 0; i < 2000; i++ {
		var sig Signal
		if i%2 == 0 {
			sig = NewCriticalErrorSignal("timeout", 100*time.Millisecond)
		} else {
			sig = NewSuccessSignal(100 * time.Millisecond)
		}
		require.NoError(t, svc.RecordSignal(ctx, key, sig))

		var err error
		score, err = svc.GetScore(ctx, key)
		require.NoError(t, err)
		if time.Until(score.CooldownUntil) > 6*time.Minute {
			break
		}
	}
	require.Equal(t, 1, score.RateCooldownCount,
		"a bench earned by the invalid-rate detector is not a trip of the critical-rate detector")
	require.InDelta(t, DefaultRateCooldown.Seconds(), remaining(t, svc, ctx, key).Seconds(), 30,
		"a first critical-rate trip must bench for one DefaultRateCooldown, not the escalated duration")
}
