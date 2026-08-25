package reputation

import (
	"context"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"
)

// violationSignal builds the signal the protocol classifier produces for a zero-length
// payload: CRITICAL severity plus the protocol-violation flag.
func violationSignal() Signal {
	s := NewCriticalErrorSignal("empty_response", 100*time.Millisecond)
	s.IsProtocolViolation = true
	return s
}

// run drives n requests at the given violation rate (1 in every `oneIn`), returning the score.
func runAtRate(t *testing.T, svc *service, ctx context.Context, key EndpointKey, n, oneIn int) Score {
	t.Helper()
	for i := 0; i < n; i++ {
		var err error
		if oneIn > 0 && i%oneIn == 0 {
			err = svc.RecordSignal(ctx, key, violationSignal())
		} else {
			err = svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond))
		}
		require.NoError(t, err)
	}
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	return score
}

// TestInvalidRate_CatchesTheRateAdditiveScoringCannotReach is the reason this detector exists.
//
// Measured in production 2026-08-19: an endpoint returning zero-length payloads at ~0.2-0.9%
// of its traffic held a reputation score of 100 all day. Additive scoring cannot express that
// rate — at 1 violation per 1000 requests the endpoint earns +998 and loses -25, so the score
// pins at its ceiling no matter how long the behaviour continues. Raising the per-event
// penalty to FATAL (-50) does not change the sign either.
//
// A ~1% sustained violation rate must therefore trip a cooldown even though the endpoint is
// succeeding 99% of the time and its additive score is pegged at maximum.
func TestInvalidRate_CatchesTheRateAdditiveScoringCannotReach(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("solana", "empty-payloads-1pct", sharedtypes.RPCType_JSON_RPC)

	score := runAtRate(t, svc, ctx, key, 4000, 100) // 1 in 100 = 1%

	require.True(t, score.IsInCooldown(),
		"a sustained 1%% protocol-violation rate must trip the invalid-rate detector")
	require.Less(t, score.CriticalStrikes, DefaultStrikeThreshold,
		"strikes must stay below threshold — this bench must come from the RATE detector, not the burst counter")
	require.GreaterOrEqual(t, score.Value, float64(90),
		"the additive score should still be near its ceiling: that is precisely why the rate detector is needed")
}

// TestInvalidRate_QuietEndpointNotPenalized pins the noise floor. Every other domain on the
// fleet sat at ~0.00003% violations or exactly zero on 2026-08-19; none of them may be
// benched by this detector.
func TestInvalidRate_QuietEndpointNotPenalized(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("solana", "clean-endpoint", sharedtypes.RPCType_JSON_RPC)

	// 1 violation in 5000 requests = 0.02%, still ~250x the observed fleet noise floor.
	score := runAtRate(t, svc, ctx, key, 5000, 5000)

	require.False(t, score.IsInCooldown(),
		"an endpoint far below the threshold must never be benched by the invalid-rate detector")
}

// TestInvalidRate_RequiresConvergedSample guards against benching on a short unlucky burst.
// The EWMA needs ~1/alpha observations before it means anything; below that the detector must
// stay silent no matter what it sees.
func TestInvalidRate_RequiresConvergedSample(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("solana", "new-endpoint-bad-start", sharedtypes.RPCType_JSON_RPC)

	// 100% violations, but far fewer than InvalidRateMinObservations.
	score := runAtRate(t, svc, ctx, key, InvalidRateMinObservations/4, 1)

	require.Less(t, score.SuccessCount+score.ErrorCount, int64(InvalidRateMinObservations),
		"precondition: sample must be under the minimum")
	require.Equal(t, 0, score.InvalidRateCooldownCount,
		"the invalid-rate detector must not trip before the EWMA has converged")
}

// TestInvalidRate_HealthCheckProbesExcluded mirrors the critical-rate detector's exclusion.
// A hard bench must reflect what users receive; a probe that is stricter than user impact
// must not be able to cool an endpoint out of rotation on its own.
func TestInvalidRate_HealthCheckProbesExcluded(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("solana", "probe-only-violations", sharedtypes.RPCType_JSON_RPC)

	for i := 0; i < 4000; i++ {
		sig := NewSuccessSignal(100 * time.Millisecond)
		if i%10 == 0 { // 10% violation rate — far above threshold
			sig = violationSignal()
		}
		sig.IsHealthCheck = true
		require.NoError(t, svc.RecordSignal(ctx, key, sig))
	}

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 0, score.InvalidRateCooldownCount,
		"health-check probes must never trip the invalid-rate detector")
}

// TestInvalidRate_CriticalErrorsAloneDoNotTrip proves the two detectors stay separate. A
// sustained 5xx rate below CriticalRateThreshold is a transient the network absorbs; it must
// not reach the far lower invalid-rate threshold, or every endpoint with a 1% error rate
// would be benched.
func TestInvalidRate_CriticalErrorsAloneDoNotTrip(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("eth", "ordinary-5xx", sharedtypes.RPCType_JSON_RPC)

	for i := 0; i < 4000; i++ {
		if i%20 == 0 { // 5% critical rate — well under CriticalRateThreshold (0.30)
			require.NoError(t, svc.RecordSignal(ctx, key, NewCriticalErrorSignal("5xx", 100*time.Millisecond)))
		} else {
			require.NoError(t, svc.RecordSignal(ctx, key, NewSuccessSignal(100*time.Millisecond)))
		}
	}

	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Equal(t, 0, score.InvalidRateCooldownCount,
		"plain critical errors must not feed the protocol-violation detector")
	require.False(t, score.IsInCooldown(),
		"a 5%% 5xx rate is below both detectors' thresholds and must not bench")
}
