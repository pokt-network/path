package shannon

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

// cloneEndpoints (defined in endpoint_filters_bench_test.go) returns a shallow copy so a test
// can call chooseRebindEndpoint — which deletes preferredAddr on the avoidPreferred path —
// without mutating the shared fixture.

// anyAddr returns one address from the map (map order is unspecified; callers that need a
// specific endpoint construct it directly instead).
func anyAddr(m map[protocol.EndpointAddr]endpoint) protocol.EndpointAddr {
	for a := range m {
		return a
	}
	return ""
}

// Test_chooseRebindEndpoint_CapDisabled_ReusesOriginal verifies the seamless tier-1 path:
// with the cap disabled (0 or >= 1) and the original endpoint still present, the rebind
// reuses it and reports a non-different (seamless) outcome.
func Test_chooseRebindEndpoint_CapDisabled_ReusesOriginal(t *testing.T) {
	c := require.New(t)

	endpoints, _ := buildOperatorEndpoints([]int{2, 2, 2})
	preferred := anyAddr(endpoints)

	for _, disabled := range []float64{0.0, 1.0, -1.0} {
		ep, different, reason, err := chooseRebindEndpoint(
			testLogger(), "svc", cloneEndpoints(endpoints), preferred, false, disabled, false, noScore,
		)
		c.NoError(err)
		c.Empty(reason)
		c.Equal(preferred, ep.Addr(), "cap=%v: must reuse the original endpoint", disabled)
		c.False(different, "cap=%v: reuse is a seamless (non-different) outcome", disabled)
	}
}

// Test_chooseRebindEndpoint_CapDisabled_OriginalRotatedOut verifies that even with the cap
// disabled, an original supplier that dropped out of the new session falls back to a
// best-available endpoint (differentSupplier=true) rather than erroring.
func Test_chooseRebindEndpoint_CapDisabled_OriginalRotatedOut(t *testing.T) {
	c := require.New(t)

	endpoints, _ := buildOperatorEndpoints([]int{1, 1, 1})
	ghost := protocol.EndpointAddr("pokt1ghost-https://n.ghost.tech") // not in the session

	ep, different, reason, err := chooseRebindEndpoint(
		testLogger(), "svc", cloneEndpoints(endpoints), ghost, false, 0.0, false, noScore,
	)
	c.NoError(err)
	c.Empty(reason)
	c.NotNil(ep)
	c.NotEqual(ghost, ep.Addr())
	c.True(different, "original rotated out → different supplier")
	_, inSet := endpoints[ep.Addr()]
	c.True(inSet, "fallback must be a member of the new session")
}

// Test_chooseRebindEndpoint_CapEngaged_SpreadsAcrossOperators is the core Option-A assertion:
// with the cap engaged and a tied-best band spanning multiple operators, repeated rebinds do
// NOT pin the original supplier — they spread across operators, and the differentSupplier
// flag tracks whether the pick moved off the original endpoint.
func Test_chooseRebindEndpoint_CapEngaged_SpreadsAcrossOperators(t *testing.T) {
	c := require.New(t)

	// 3 operators, all endpoints non-fallback and equally scored → the whole set is the band.
	endpoints, _ := buildOperatorEndpoints([]int{2, 2, 2})
	preferred := anyAddr(endpoints)
	preferredOp := opOfAddr(string(preferred))

	seenOps := map[string]bool{}
	rotatedOff := false
	for i := 0; i < 400; i++ {
		ep, different, reason, err := chooseRebindEndpoint(
			testLogger(), "svc", cloneEndpoints(endpoints), preferred, false, 0.5, false, noScore,
		)
		c.NoError(err)
		c.Empty(reason)
		_, inSet := endpoints[ep.Addr()]
		c.True(inSet, "pick must be a member of the session")
		c.Equal(ep.Addr() != preferred, different, "differentSupplier must equal (chosen != original)")
		seenOps[opOfAddr(string(ep.Addr()))] = true
		if ep.Addr() != preferred {
			rotatedOff = true
		}
	}
	c.GreaterOrEqual(len(seenOps), 2, "cap-engaged rebind must spread across operators, not pin one")
	c.True(rotatedOff, "cap-engaged rebind must sometimes rotate off the original supplier")
	c.Contains(seenOps, preferredOp, "original operator must remain eligible (not excluded)")
}

// Test_chooseRebindEndpoint_CapEngaged_SingleEndpointNeverFails verifies the 1-endpoint-pool
// safety: with the cap engaged and only the original endpoint present, selection lands back
// on it (seamless) and never errors — the original stays in the candidate set.
func Test_chooseRebindEndpoint_CapEngaged_SingleEndpointNeverFails(t *testing.T) {
	c := require.New(t)

	solo := newRCEndpoint("pokt1op0-https://n0.op0.tech", false)
	endpoints := map[protocol.EndpointAddr]endpoint{solo.Addr(): solo}

	ep, different, reason, err := chooseRebindEndpoint(
		testLogger(), "svc", cloneEndpoints(endpoints), solo.Addr(), false, 0.5, false, noScore,
	)
	c.NoError(err)
	c.Empty(reason)
	c.Equal(solo.Addr(), ep.Addr())
	c.False(different, "landing back on the sole original endpoint is seamless")
}

// Test_chooseRebindEndpoint_CapEngaged_StrictlyBestOriginalWins verifies that rotation only
// reshuffles the tied band: a strictly higher-scored original is the whole band, so it is
// still reused (seamless) even with the cap engaged.
func Test_chooseRebindEndpoint_CapEngaged_StrictlyBestOriginalWins(t *testing.T) {
	c := require.New(t)

	best := newRCEndpoint("pokt1op0-https://n0.op0.tech", false)
	other1 := newRCEndpoint("pokt1op1-https://n0.op1.tech", false)
	other2 := newRCEndpoint("pokt1op2-https://n0.op2.tech", false)
	endpoints := map[protocol.EndpointAddr]endpoint{
		best.Addr():   best,
		other1.Addr(): other1,
		other2.Addr(): other2,
	}
	scoreOf := scoreByAddr(map[string]float64{
		string(best.Addr()): 95, string(other1.Addr()): 40, string(other2.Addr()): 40,
	})

	for i := 0; i < 50; i++ {
		ep, different, reason, err := chooseRebindEndpoint(
			testLogger(), "svc", cloneEndpoints(endpoints), best.Addr(), false, 0.5, false, scoreOf,
		)
		c.NoError(err)
		c.Empty(reason)
		c.Equal(best.Addr(), ep.Addr(), "a strictly-best original is the whole band → reused")
		c.False(different)
	}
}

// Test_chooseRebindEndpoint_CapEngaged_OriginalRotatedOut verifies the tier-2 fallback under
// an engaged cap: the original is absent, so the pick is a different endpoint from the set.
func Test_chooseRebindEndpoint_CapEngaged_OriginalRotatedOut(t *testing.T) {
	c := require.New(t)

	endpoints, _ := buildOperatorEndpoints([]int{2, 2})
	ghost := protocol.EndpointAddr("pokt1ghost-https://n.ghost.tech")

	ep, different, reason, err := chooseRebindEndpoint(
		testLogger(), "svc", cloneEndpoints(endpoints), ghost, false, 0.5, false, noScore,
	)
	c.NoError(err)
	c.Empty(reason)
	c.NotNil(ep)
	c.True(different)
	_, inSet := endpoints[ep.Addr()]
	c.True(inSet)
}

// Test_chooseRebindEndpoint_AvoidPreferred_ExcludesOriginal verifies the stall-escape path:
// avoidPreferred drops the original endpoint and forces a different one.
func Test_chooseRebindEndpoint_AvoidPreferred_ExcludesOriginal(t *testing.T) {
	c := require.New(t)

	endpoints, _ := buildOperatorEndpoints([]int{2, 2, 2})
	preferred := anyAddr(endpoints)

	for i := 0; i < 100; i++ {
		ep, different, reason, err := chooseRebindEndpoint(
			testLogger(), "svc", cloneEndpoints(endpoints), preferred, true, 0.5, false, noScore,
		)
		c.NoError(err)
		c.Empty(reason)
		c.NotEqual(preferred, ep.Addr(), "avoidPreferred must never reselect the stalling endpoint")
		c.True(different)
	}
}

// Test_chooseRebindEndpoint_AvoidPreferred_OnlyOriginal_Errors verifies that when the
// stalling endpoint is the session's only endpoint, the escape fails with the
// no-endpoints reason so the bridge closes the client.
func Test_chooseRebindEndpoint_AvoidPreferred_OnlyOriginal_Errors(t *testing.T) {
	c := require.New(t)

	solo := newRCEndpoint("pokt1op0-https://n0.op0.tech", false)
	endpoints := map[protocol.EndpointAddr]endpoint{solo.Addr(): solo}

	ep, different, reason, err := chooseRebindEndpoint(
		testLogger(), "svc", cloneEndpoints(endpoints), solo.Addr(), true, 0.5, false, noScore,
	)
	c.Error(err)
	c.Nil(ep)
	c.False(different)
	c.Equal(metrics.WSRebindFailedNoEndpoints, reason)
}

// Test_chooseRebindEndpoint_OperatorUniform_EqualizesOperatorShare verifies the operator-
// uniform strategy: each operator gets ~equal share regardless of endpoint count. op0 owns
// 8 of 10 endpoints, yet lands ~1/3 — the opposite of the endpoint-weighted cap, which would
// concentrate on op0.
func Test_chooseRebindEndpoint_OperatorUniform_EqualizesOperatorShare(t *testing.T) {
	c := require.New(t)

	endpoints, ops := buildOperatorEndpoints([]int{8, 1, 1})
	preferred := anyAddr(endpoints)

	counts := map[string]int{}
	const N = 3000
	for i := 0; i < N; i++ {
		ep, _, reason, err := chooseRebindEndpoint(
			testLogger(), "svc", cloneEndpoints(endpoints), preferred, false, 0.65, true, noScore,
		)
		c.NoError(err)
		c.Empty(reason)
		counts[opOfAddr(string(ep.Addr()))]++
	}
	for _, op := range ops {
		share := float64(counts[op]) / float64(N)
		c.InDelta(1.0/3.0, share, 0.06, "operator %s share %.3f should be ~1/3 under operator-uniform", op, share)
	}
}

// Test_chooseRebindEndpoint_OperatorUniform_RotatesWithCapDisabled verifies operator-uniform
// forces rotation even when the concentration cap is disabled (where the cap path would pin
// the original). The original stays eligible, so it is sometimes re-picked but never pinned.
func Test_chooseRebindEndpoint_OperatorUniform_RotatesWithCapDisabled(t *testing.T) {
	c := require.New(t)

	endpoints, _ := buildOperatorEndpoints([]int{2, 2, 2})
	preferred := anyAddr(endpoints)

	seenOps := map[string]bool{}
	rotatedOff := false
	for i := 0; i < 300; i++ {
		ep, different, reason, err := chooseRebindEndpoint(
			testLogger(), "svc", cloneEndpoints(endpoints), preferred, false, 0.0 /* cap off */, true /* operator-uniform */, noScore,
		)
		c.NoError(err)
		c.Empty(reason)
		c.Equal(ep.Addr() != preferred, different)
		seenOps[opOfAddr(string(ep.Addr()))] = true
		if ep.Addr() != preferred {
			rotatedOff = true
		}
	}
	c.GreaterOrEqual(len(seenOps), 2, "operator-uniform must rotate even with the cap disabled")
	c.True(rotatedOff, "operator-uniform must sometimes rotate off the original supplier")
}
