package selector

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// bandShares runs the band selector n times and returns the observed share per operator plus
// the observed outcome counts. Every draw is asserted to be a member of the band: the retry and
// hedge paths must never be handed an endpoint they did not offer, and never an empty address.
func bandShares(
	t *testing.T,
	band protocol.EndpointAddrList,
	maxShare float64,
	n int,
) (map[string]float64, map[BandCapOutcome]int) {
	t.Helper()
	counts := map[string]int{}
	outcomes := map[BandCapOutcome]int{}
	for i := 0; i < n; i++ {
		sel, outcome := SelectBandWithConcentrationCap(band, maxShare)
		require.NotEmpty(t, sel, "a band pick must always resolve to an endpoint")
		require.Contains(t, band, sel, "a band pick must return a member of the band")
		counts[operatorKey(sel)]++
		outcomes[outcome]++
	}
	shares := make(map[string]float64, len(counts))
	for k, c := range counts {
		shares[k] = float64(c) / float64(n)
	}
	return shares, outcomes
}

// The leak this change exists to close: a band that is diverse in MACHINES can still be
// dominated by one operator, and the pre-existing backend-uniform pick did nothing about that.
// A retry or hedge is specifically an attempt to reach infrastructure other than the one that
// just failed, so an uncapped operator share there is worse than on the primary path.
func TestSelectBandWithConcentrationCap_CapsDominantOperatorInBand(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	// One operator with 8 distinct backends against two solo operators. Uncapped, the big
	// operator takes 8/10 of the band.
	band, ops := makeOperatorPool([]int{8, 1, 1})
	uncapped, _ := bandShares(t, band, 0, 40_000)
	c.InDelta(0.8, uncapped[ops[0]], 0.02, "uncapped, the dominant operator takes its backend share")

	capped, outcomes := bandShares(t, band, 0.5, 40_000)
	c.InDelta(0.5, capped[ops[0]], 0.02, "the cap must bound the dominant operator's band share")
	c.InDelta(0.25, capped[ops[1]], 0.02, "redistributed mass must reach the other operators")
	c.InDelta(0.25, capped[ops[2]], 0.02, "redistributed mass must reach the other operators")
	c.Equal(40_000, outcomes[BandCapReshaped], "every draw over the cap must report as reshaped")
}

// A band whose operators are all already under the cap must be left exactly as it was — the
// change is a no-op on the overwhelming majority of bands, and says so in its outcome.
func TestSelectBandWithConcentrationCap_NoOpWhenUnderCap(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	band, ops := makeOperatorPool([]int{2, 2, 2})
	shares, outcomes := bandShares(t, band, 0.65, 30_000)

	c.Equal(30_000, outcomes[BandCapNoOp], "no operator exceeds the cap: nothing to reshape")
	for _, op := range ops {
		c.InDelta(1.0/3.0, shares[op], 0.02, "shares must stay proportional when the cap does not bind")
	}
}

// THE DEGRADATION CASE, and the documented answer to "what if the cap would leave a retry with
// no candidate": every candidate belongs to one operator, so honoring the cap would mean
// excluding all of them. The pick stays uncapped and reports BandCapNoRoom so the degradation is
// counted rather than inferred. This is the NORMAL case for a retry — a retry has already
// excluded the operators it tried — which is exactly why it must not be treated as an error.
func TestSelectBandWithConcentrationCap_SingleOperatorBandDegradesUncapped(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	band, ops := makeOperatorPool([]int{4})
	shares, outcomes := bandShares(t, band, 0.5, 5_000)

	c.Equal(5_000, outcomes[BandCapNoRoom], "a single-operator band must report the degraded outcome")
	c.InDelta(1.0, shares[ops[0]], 0.0001, "the only operator present must still receive every pick")
}

// The cap must never be able to starve a retry. It reweights the band and never filters it, so
// for every band — including ones the cap cannot satisfy at all — a candidate comes back.
func TestSelectBandWithConcentrationCap_NeverStarvesTheBand(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	// Shapes chosen to hit every branch: single endpoint, single operator, feasible cap,
	// and an infeasible cap (cap*operators <= 1, i.e. no assignment can hold every operator
	// under it) — the last is where a filtering implementation would return nothing.
	shapes := [][]int{{1}, {5}, {3, 1}, {9, 1}, {1, 1}, {20, 1, 1}}
	for _, shape := range shapes {
		band, _ := makeOperatorPool(shape)
		for _, maxShare := range []float64{0.1, 0.3, 0.5, 0.65, 0.9} {
			for i := 0; i < 200; i++ {
				sel, _ := SelectBandWithConcentrationCap(band, maxShare)
				c.NotEmpty(sel, "shape %v at cap %v returned no candidate", shape, maxShare)
				c.Contains(band, sel, "shape %v at cap %v returned a non-member", shape, maxShare)
			}
		}
	}

	// An empty band is the caller's own problem, reported explicitly rather than papered over.
	sel, outcome := SelectBandWithConcentrationCap(protocol.EndpointAddrList{}, 0.5)
	c.Empty(sel)
	c.Equal(BandCapNoCandidates, outcome)
}

// The off-switch must disable the cap completely, not soften it: the distribution has to match
// the pre-cap backend-uniform pick.
func TestSelectBandWithConcentrationCap_OffSwitchIsCompleteDisable(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	band, ops := makeOperatorPool([]int{8, 1, 1})
	reference := sampleShares(t, 40_000, func() protocol.EndpointAddr {
		return PickBackendUniform(band)
	}, func(ep protocol.EndpointAddr) string { return operatorKey(ep) })

	// Both disabling forms: <= 0 and >= 1.
	for _, off := range []float64{0, -1, 1, 1.5} {
		shares, outcomes := bandShares(t, band, off, 40_000)
		c.Equal(40_000, outcomes[BandCapDisabled], "maxShare %v must report the cap as disabled", off)
		for _, op := range ops {
			c.InDelta(reference[op], shares[op], 0.02,
				"maxShare %v must reproduce the uncapped distribution for %s", off, op)
		}
	}
}

// Two-stage selection must survive the cap: the result is always a concrete supplier
// registration (never a bare backend URL), because a relay is signed against a supplier's
// session and each supplier carries its own per-session service allowance. Stacked
// registrations behind one URL must not buy that URL extra traffic, and must still all be
// reachable so allowance consumption spreads over them.
func TestSelectBandWithConcentrationCap_ResolvesToConcreteSupplier(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	// stackedPool: one operator with 2 backends behind 8 registrations, one with 2 backends
	// behind 2 registrations. Uncapped by operator that is 8/10 vs 2/10 by registration and
	// 2/4 vs 2/4 by backend; capped at 0.4 the big operator must come down to 0.4.
	band := stackedPool()
	inBand := make(map[protocol.EndpointAddr]bool, len(band))
	for _, ep := range band {
		inBand[ep] = true
	}

	seenSuppliers := map[protocol.EndpointAddr]int{}
	operatorCounts := map[string]int{}
	const draws = 60_000
	for i := 0; i < draws; i++ {
		sel, _ := SelectBandWithConcentrationCap(band, 0.4)
		c.True(inBand[sel], "%q is not a supplier registration from the band", sel)
		seenSuppliers[sel]++
		operatorCounts[operatorKey(sel)]++
	}

	// Every registration is reachable — the second stage picks a supplier within the backend,
	// so allowance is spread across siblings rather than pinned to one of them.
	c.Len(seenSuppliers, len(band), "every supplier registration behind a chosen backend must be reachable")

	// cap*operators = 0.4*2 <= 1, so the cap is infeasible and the best achievable is
	// uniform-over-operators. Both operators land at ~0.5 rather than the big one at 0.5+.
	for op, count := range operatorCounts {
		c.InDelta(0.5, float64(count)/float64(draws), 0.02, "operator %s share", op)
	}
}

// With backend-URL dedup off (PATH_OPERATOR_SHARE_BY_BACKEND_URL=false) the band cap must fall
// back to registration-counted shares — the same currency the primary path uses in that mode —
// and must still return a concrete supplier.
func TestSelectBandWithConcentrationCap_HonorsRegistrationCountedMode(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(false)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	band := stackedPool()
	inBand := make(map[protocol.EndpointAddr]bool, len(band))
	for _, ep := range band {
		inBand[ep] = true
	}

	// Registration-counted, the stacked operator holds 8/10. Cap 0.6 with 2 operators is
	// infeasible (0.6*2 > 1 is false → 1.2 > 1, so feasible) → water-filling pulls it to 0.6.
	counts := map[string]int{}
	const draws = 40_000
	for i := 0; i < draws; i++ {
		sel, outcome := SelectBandWithConcentrationCap(band, 0.6)
		c.True(inBand[sel], "%q is not a supplier registration from the band", sel)
		c.Equal(BandCapReshaped, outcome)
		counts[operatorKey(sel)]++
	}
	c.InDelta(0.6, float64(counts["bigop.net"])/float64(draws), 0.02,
		"registration-counted share of the stacked operator must be capped, not its backend share")
}

// The outcome taxonomy has to be readable from a dashboard without knowing the config: only the
// cap VALUE being off may report "disabled". A band the cap is on for but cannot act on — one
// candidate, or one operator — must report the degraded outcome, or an enabled gate would look
// like a disabled one.
func TestSelectBandWithConcentrationCap_OutcomeDistinguishesOffFromNoRoom(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	single, _ := makeOperatorPool([]int{1})
	sel, outcome := SelectBandWithConcentrationCap(single, 0.5)
	c.Equal(single[0], sel, "a one-candidate band must return that candidate")
	c.Equal(BandCapNoRoom, outcome, "cap on with one candidate is no-room, not disabled")

	oneOperator, _ := makeOperatorPool([]int{4})
	_, outcome = SelectBandWithConcentrationCap(oneOperator, 0.5)
	c.Equal(BandCapNoRoom, outcome, "cap on with one operator is no-room, not disabled")

	// Only the cap value being off reports disabled — including for a one-candidate band, where
	// the two cases would otherwise be indistinguishable.
	for _, off := range []float64{0, -1, 1, 1.5} {
		_, outcome = SelectBandWithConcentrationCap(single, off)
		c.Equal(BandCapDisabled, outcome, "maxShare %v is the off-switch", off)
	}
}

// The band cap must run the SAME water-filling as the primary path, not a second copy that can
// drift from it. Both go through capPickOverPools, so identical inputs must produce identical
// distributions.
func TestSelectBandWithConcentrationCap_MatchesPrimaryPathDistribution(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	pool, ops := makeOperatorPool([]int{10, 1, 1, 1})
	const maxShare = 0.4

	primary := operatorShares(t, pool, maxShare, 60_000)
	band, _ := bandShares(t, pool, maxShare, 60_000)

	for _, op := range ops {
		c.InDelta(primary[op], band[op], 0.02,
			"band and primary paths must agree on operator %s's capped share", op)
	}
}
