package reputation

import (
	"context"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"
)

// healthCheckViolationSignal is the signal a health-check probe produces when an endpoint
// returns a zero-length payload: identical to the user-traffic one except that it is
// stamped as probe-originated.
func healthCheckViolationSignal() Signal {
	s := violationSignal()
	s.IsHealthCheck = true
	return s
}

// healthCheckSuccessSignal is a probe that passed.
func healthCheckSuccessSignal() Signal {
	s := NewSuccessSignal(100 * time.Millisecond)
	s.IsHealthCheck = true
	return s
}

// runAtRateHealthCheck mirrors runAtRate but stamps every signal as probe-originated.
func runAtRateHealthCheck(t *testing.T, svc *service, ctx context.Context, key EndpointKey, n, oneIn int) Score {
	t.Helper()
	for i := 0; i < n; i++ {
		var err error
		if oneIn > 0 && i%oneIn == 0 {
			err = svc.RecordSignal(ctx, key, healthCheckViolationSignal())
		} else {
			err = svc.RecordSignal(ctx, key, healthCheckSuccessSignal())
		}
		require.NoError(t, err)
	}
	score, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	return score
}

// TestHealthCheckSignals_CannotTripInvalidRateDetector is the outcome test for the
// contamination bug.
//
// Both rate detectors are wrapped in `if !signal.IsHealthCheck` so a probe can never bench
// an endpoint on its own — a strict or flaky check (a Solana getBlockHeight sync check, a
// CometBFT probe with the wrong payload shape) must not cool an endpoint that serves user
// reads perfectly. That guard was intact; what was missing was the STAMP. Only the
// health-check executor's own three RecordSignal call sites set the flag, while a probe
// ALSO reaches reputation twice through the protocol layer — once from the relay itself
// and once from ApplyHTTPObservations — and neither of those stamped it.
//
// The consequence is a self-sustaining loop, not a one-off penalty: a benched endpoint
// receives no user traffic, so probes become its ONLY signal, so its rate EWMAs are 100%
// probe-derived, so it re-benches itself on the next probe failure. Measured on canary
// 2026-08-20: every solana endpoint tripping roughly twice an hour against a ~12-endpoint
// pool, with the pool-collapse guard firing 19.3x the control environment to keep the
// service served at all.
//
// The identical stream at the identical rate is asserted twice — once stamped, once not —
// so the control proves the harness actually reaches the detector.
func TestHealthCheckSignals_CannotTripInvalidRateDetector(t *testing.T) {
	svc, ctx := newRateTestService(t)

	probeKey := NewEndpointKey("solana", "probe-only-violations", sharedtypes.RPCType_JSON_RPC)
	probeScore := runAtRateHealthCheck(t, svc, ctx, probeKey, 4000, 100) // 1 in 100 = 1%

	require.False(t, probeScore.IsInCooldown(),
		"a 1%% violation rate seen ONLY by health-check probes must not bench the endpoint: "+
			"a benched endpoint gets no user traffic, so probes become its only signal and it re-benches itself forever")

	// Control: the same stream, unstamped, must bench. Without this the test above passes
	// on any harness that never reaches the detector at all.
	userKey := NewEndpointKey("solana", "user-traffic-violations", sharedtypes.RPCType_JSON_RPC)
	userScore := runAtRate(t, svc, ctx, userKey, 4000, 100)
	require.True(t, userScore.IsInCooldown(),
		"control: the same violation rate on USER traffic must still trip the detector")
}

// TestHealthCheckSignals_CannotTripCriticalRateDetector is the same assertion for the
// older volume-independent critical-rate detector. That detector shipped with the
// `!IsHealthCheck` guard and has been silently contaminated by the protocol layer for as
// long as it has existed; the invalid-rate detector only made the contamination visible by
// tripping at a threshold three orders of magnitude lower.
func TestHealthCheckSignals_CannotTripCriticalRateDetector(t *testing.T) {
	svc, ctx := newRateTestService(t)

	// A sustained 50% critical rate — far above CriticalRateThreshold (0.30) — seen only by
	// probes. Alternating so the strike counter (which decays 3 per success) never reaches
	// DefaultStrikeThreshold and cannot be the thing doing the benching.
	probeKey := NewEndpointKey("eth", "probe-only-criticals", sharedtypes.RPCType_JSON_RPC)
	for i := 0; i < 200; i++ {
		var sig Signal
		if i%2 == 0 {
			sig = NewCriticalErrorSignal("timeout", 100*time.Millisecond)
			sig.IsHealthCheck = true
		} else {
			sig = healthCheckSuccessSignal()
		}
		require.NoError(t, svc.RecordSignal(ctx, probeKey, sig))
	}
	probeScore, err := svc.GetScore(ctx, probeKey)
	require.NoError(t, err)
	require.False(t, probeScore.IsInCooldown(),
		"a 50%% critical rate seen ONLY by health-check probes must not bench the endpoint")

	// Control: identical stream, unstamped.
	userKey := NewEndpointKey("eth", "user-traffic-criticals", sharedtypes.RPCType_JSON_RPC)
	for i := 0; i < 200; i++ {
		var sig Signal
		if i%2 == 0 {
			sig = NewCriticalErrorSignal("timeout", 100*time.Millisecond)
		} else {
			sig = NewSuccessSignal(100 * time.Millisecond)
		}
		require.NoError(t, svc.RecordSignal(ctx, userKey, sig))
	}
	userScore, err := svc.GetScore(ctx, userKey)
	require.NoError(t, err)
	require.True(t, userScore.IsInCooldown(),
		"control: the same critical rate on USER traffic must still trip the detector")
}

// TestHealthCheckSignals_StillMoveTheAdditiveScore guards the scope of the exclusion.
//
// Only the volume-independent RATE detectors ignore probes. A probe result must still move
// Value — that is how an endpoint recovers from a bench when it is receiving no user
// traffic, and removing it would strand every benched endpoint permanently. A fix that
// made health checks inert everywhere would look like this test failing.
func TestHealthCheckSignals_StillMoveTheAdditiveScore(t *testing.T) {
	svc, ctx := newRateTestService(t)
	key := NewEndpointKey("eth", "probe-moves-score", sharedtypes.RPCType_JSON_RPC)

	// An endpoint reputation has never seen has no stored score, so the baseline is the
	// configured initial score rather than a read.
	const initialScore = 80.0

	for i := 0; i < 10; i++ {
		sig := NewCriticalErrorSignal("timeout", 100*time.Millisecond)
		sig.IsHealthCheck = true
		require.NoError(t, svc.RecordSignal(ctx, key, sig))
	}

	after, err := svc.GetScore(ctx, key)
	require.NoError(t, err)
	require.Less(t, after.Value, initialScore,
		"health-check failures must still penalise the additive score — the rate detectors are the only thing that excludes them")
	require.Equal(t, int64(10), after.ErrorCount,
		"probe results must still be counted; excluding them from the counters would corrupt every rate's denominator")
}
