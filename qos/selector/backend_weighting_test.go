package selector

import (
	"fmt"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

// ---------------------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------------------

// pinBackendWeightCap sets the process-wide K for one test and restores the shipped default.
func pinBackendWeightCap(t *testing.T, k int) {
	t.Helper()
	SetBackendRegistrationWeightCap(k)
	t.Cleanup(func() { SetBackendRegistrationWeightCap(DefaultBackendRegistrationWeightCap) })
}

// pinServiceSettings publishes one service's selection settings for the duration of a test.
func pinServiceSettings(t *testing.T, serviceID protocol.ServiceID, weightCap int, maxOperatorShare float64) {
	t.Helper()
	SetServiceSelectionSettings(serviceID, weightCap, maxOperatorShare)
	t.Cleanup(ResetServiceSelectionSettings)
}

// buildPool builds an endpoint list from a per-operator, per-backend registration layout:
// layout[operator][backend] = how many supplier registrations sit behind that backend URL.
//
// Addresses use the production "<supplier>-<url>" shape so the real operatorKey/backendKey
// parsing runs. Operators and suppliers are deliberately anonymous — the shapes are what the
// specification is about, not who runs them.
func buildPool(layout [][]int) protocol.EndpointAddrList {
	var eps protocol.EndpointAddrList
	for opIdx, backends := range layout {
		for backendIdx, registrations := range backends {
			for r := 0; r < registrations; r++ {
				eps = append(eps, protocol.EndpointAddr(fmt.Sprintf(
					"supplier%d%d%d-https://node%d.op%d.net", opIdx, backendIdx, r, backendIdx, opIdx,
				)))
			}
		}
	}
	return eps
}

func operatorDomain(opIdx int) string { return fmt.Sprintf("op%d.net", opIdx) }

// shareByOperator samples pick and returns the observed per-operator share.
func shareByOperator(t *testing.T, draws int, pick func() protocol.EndpointAddr) map[string]float64 {
	t.Helper()
	return sampleShares(t, draws, pick, operatorKeyForTest)
}

// shareByBackend samples pick and returns the observed per-backend-URL share.
func shareByBackend(t *testing.T, draws int, pick func() protocol.EndpointAddr) map[string]float64 {
	t.Helper()
	return sampleShares(t, draws, pick, func(ep protocol.EndpointAddr) string { return backendKey(ep) })
}

// productionShapedPool mirrors the measured registration/backend layout of a real service:
// five operators running 28/6, 15/4, 5/2, 1/1 and 1/1 registrations-per-backend.
//
// The per-backend split matters as much as the totals — it is what K acts on — so it is spelled
// out rather than derived: the large operator spreads its 28 registrations over 6 machines with
// at least 2 on each, the second spreads 15 over 4 machines with one of them holding a single
// registration, the third holds 5 over 2 machines, and the last two are solo registrations on
// solo machines.
func productionShapedPool() protocol.EndpointAddrList {
	return buildPool([][]int{
		{8, 6, 5, 4, 3, 2}, // 28 registrations, 6 backends
		{7, 5, 2, 1},       // 15 registrations, 4 backends
		{3, 2},             // 5 registrations, 2 backends
		{1},                // 1 registration, 1 backend
		{1},                // 1 registration, 1 backend
	})
}

// ---------------------------------------------------------------------------------------
// The specification
// ---------------------------------------------------------------------------------------

// This is the specification for the weighting change, on the measured production pool shape.
//
// Before (backend-uniform, K=1): 42.9 / 28.6 / 14.3 / 7.1 / 7.1, largest single machine 7.1%.
// After  (K=2):                  48.0 / 28.0 / 16.0 / 4.0 / 4.0, largest single machine 8.0%.
//
// The direction is the point: an operator that stakes 5 registrations behind 2 machines used to
// receive exactly what an operator staking 1 registration behind 1 machine received, because we
// allocated per machine while the chain pays per registration. At K=2 it receives four times as
// much — and the two solo operators fall from 7.1% to 4.0%, which is the cost of the change and
// the thing to watch on canary.
func Test_Weighting_ProductionShapeSpecification(t *testing.T) {
	c := require.New(t)
	pool := productionShapedPool()

	// Basis only: the cap is a separate change and must not perturb the numbers below.
	pinServiceSettings(t, "spec-svc", 0, 0)

	t.Run("K=1 is today's backend-uniform allocation", func(t *testing.T) {
		pinBackendWeightCap(t, 1)
		shares := shareByOperator(t, 200_000, func() protocol.EndpointAddr {
			return PickBackendUniformForService("spec-svc", pool)
		})
		// 14 distinct backends: 6 / 4 / 2 / 1 / 1.
		for i, want := range []float64{6.0 / 14, 4.0 / 14, 2.0 / 14, 1.0 / 14, 1.0 / 14} {
			c.InDelta(want, shares[operatorDomain(i)], 0.01, "operator %d at K=1", i)
		}
	})

	t.Run("K=2 is the shipped allocation", func(t *testing.T) {
		pinBackendWeightCap(t, 2)
		shares := shareByOperator(t, 200_000, func() protocol.EndpointAddr {
			return PickBackendUniformForService("spec-svc", pool)
		})
		// Weights: 12 / 7 / 4 / 1 / 1 = 25 units.
		for i, want := range []float64{0.48, 0.28, 0.16, 0.04, 0.04} {
			c.InDelta(want, shares[operatorDomain(i)], 0.01, "operator %d at K=2", i)
		}
	})

	t.Run("no single machine exceeds 8% of traffic at K=2", func(t *testing.T) {
		pinBackendWeightCap(t, 2)
		shares := shareByBackend(t, 200_000, func() protocol.EndpointAddr {
			return PickBackendUniformForService("spec-svc", pool)
		})
		c.Len(shares, 14, "the pool spans 14 distinct machines")
		for backend, share := range shares {
			c.LessOrEqual(share, 0.08+0.01,
				"one machine is one failure domain and must stay bounded: %s at %.3f", backend, share)
		}
	})
}

// ---------------------------------------------------------------------------------------
// The two ends of the K dial
// ---------------------------------------------------------------------------------------

// K=1 is the kill switch for the basis change, so its equivalence to the previous
// backend-uniform behavior is asserted directly rather than inferred: every distinct machine
// draws the same share regardless of how many registrations sit behind it.
func Test_BackendWeightCap_K1_ReproducesBackendUniform(t *testing.T) {
	c := require.New(t)
	pinBackendWeightCap(t, 1)
	pinServiceSettings(t, "k1-svc", 0, 0)

	pool := buildPool([][]int{{9, 1}, {4}, {1}})
	shares := shareByBackend(t, 120_000, func() protocol.EndpointAddr {
		return PickBackendUniformForService("k1-svc", pool)
	})

	c.Len(shares, 4, "4 distinct machines")
	for backend, share := range shares {
		c.InDelta(0.25, share, 0.015, "machine %s must draw 1/4 whatever it stakes", backend)
	}
}

// A K larger than any backend's registration count is registration-proportional: the other end
// of the dial, and the behavior the cap exists to prevent from being the default.
func Test_BackendWeightCap_LargeK_ReproducesRegistrationProportional(t *testing.T) {
	c := require.New(t)
	pinBackendWeightCap(t, 1000)
	pinServiceSettings(t, "bigk-svc", 0, 0)

	pool := buildPool([][]int{{9, 1}, {4}, {1}}) // 15 registrations total
	shares := shareByOperator(t, 120_000, func() protocol.EndpointAddr {
		return PickBackendUniformForService("bigk-svc", pool)
	})

	c.InDelta(10.0/15, shares[operatorDomain(0)], 0.015)
	c.InDelta(4.0/15, shares[operatorDomain(1)], 0.015)
	c.InDelta(1.0/15, shares[operatorDomain(2)], 0.015)
}

// A per-service K overrides the process-wide one; 0 means "unset", so the process-wide value
// (the env-var lever) keeps working for every service that does not pin its own.
func Test_BackendWeightCap_PerServiceOverridesProcessWide(t *testing.T) {
	c := require.New(t)
	pinBackendWeightCap(t, 1)
	pool := buildPool([][]int{{4}, {1}})

	pinServiceSettings(t, "pinned-svc", 1000, 0)
	SetServiceSelectionSettings("unpinned-svc", 0, 0)

	pinned := shareByOperator(t, 80_000, func() protocol.EndpointAddr {
		return PickBackendUniformForService("pinned-svc", pool)
	})
	c.InDelta(0.80, pinned[operatorDomain(0)], 0.02, "per-service K must win over the process-wide K")

	unpinned := shareByOperator(t, 80_000, func() protocol.EndpointAddr {
		return PickBackendUniformForService("unpinned-svc", pool)
	})
	c.InDelta(0.50, unpinned[operatorDomain(0)], 0.02, "K=0 means unset, so the process-wide K applies")
}

// ---------------------------------------------------------------------------------------
// The operator cap, on the path that serves traffic
// ---------------------------------------------------------------------------------------

// The cap must bind on the serving pick — the whole point of the change is that it was
// previously applied only on a path carrying a rounding error's worth of traffic.
func Test_ServingPick_OperatorCapBinds(t *testing.T) {
	c := require.New(t)
	pinBackendWeightCap(t, 2)
	pinServiceSettings(t, "cap-svc", 0, 0.45)

	// Three operators so a 0.45 cap is satisfiable. Uncapped weights 6/1/1 → 75% / 12.5% / 12.5%.
	pool := buildPool([][]int{{4, 4, 4}, {1}, {1}})
	shares := shareByOperator(t, 200_000, func() protocol.EndpointAddr {
		return PickBackendUniformForService("cap-svc", pool)
	})

	// The cap pushes the dominant operator toward 0.45, but only as far as the others can
	// absorb. They hold 1 registration each of 14, so their allowance entitles them to 7.1% and
	// the displacement ceiling stops them at 3x that — 21.4%. The 0.30 of excess is therefore
	// only half absorbed and the dominant operator keeps the rest: moving it anyway would route
	// traffic to suppliers that answer 429.
	c.InDelta(0.572, shares[operatorDomain(0)], 0.02, "the cap binds only as far as the pool can absorb")
	c.InDelta(0.214, shares[operatorDomain(1)], 0.02, "held at 3x the share its registrations entitle it to")
	c.InDelta(0.214, shares[operatorDomain(2)], 0.02)
}

// A pool where nobody exceeds the cap must be left exactly on the weighting basis: a cap that
// perturbs an already-compliant distribution would be silently reweighting traffic for nothing.
func Test_ServingPick_OperatorCapIsNoOpWhenNobodyExceedsIt(t *testing.T) {
	c := require.New(t)
	pinBackendWeightCap(t, 2)
	pinServiceSettings(t, "nocap-svc", 0, 0.45)

	// Weights 2/2/1 → 40% / 40% / 20%, all under 45%.
	pool := buildPool([][]int{{3, 4}, {2, 5}, {6}})
	shares := shareByOperator(t, 200_000, func() protocol.EndpointAddr {
		return PickBackendUniformForService("nocap-svc", pool)
	})

	c.InDelta(0.40, shares[operatorDomain(0)], 0.02)
	c.InDelta(0.40, shares[operatorDomain(1)], 0.02)
	c.InDelta(0.20, shares[operatorDomain(2)], 0.02)
}

// The cap on the serving pick is separately switchable so that a canary can tell a traffic
// shift caused by the new basis apart from one caused by the cap.
func Test_ServingPick_OperatorCapOffSwitch(t *testing.T) {
	c := require.New(t)
	pinBackendWeightCap(t, 2)
	pinServiceSettings(t, "capoff-svc", 0, 0.45)
	SetBackendPickOperatorCap(false)
	t.Cleanup(func() { SetBackendPickOperatorCap(true) })

	pool := buildPool([][]int{{4, 4, 4}, {1}, {1}})
	shares := shareByOperator(t, 120_000, func() protocol.EndpointAddr {
		return PickBackendUniformForService("capoff-svc", pool)
	})

	c.InDelta(0.75, shares[operatorDomain(0)], 0.02,
		"with the cap switched off the basis must stand alone, uncapped")
}

// PickBackendUniform without a service applies the basis but not the cap: there is no service
// to resolve a configured cap from, and its production caller is the reputation-score band,
// whose cap scoping is a separate change.
func Test_PickBackendUniform_WithoutServiceAppliesBasisOnly(t *testing.T) {
	c := require.New(t)
	pinBackendWeightCap(t, 2)
	SetDefaultMaxOperatorShare(0.45)
	t.Cleanup(func() { SetDefaultMaxOperatorShare(DefaultMaxOperatorShareFallback) })

	pool := buildPool([][]int{{4, 4, 4}, {1}, {1}})
	shares := shareByOperator(t, 120_000, func() protocol.EndpointAddr {
		return PickBackendUniform(pool)
	})

	c.InDelta(0.75, shares[operatorDomain(0)], 0.02)
}

// ---------------------------------------------------------------------------------------
// Invariants that must hold whatever the flags say
// ---------------------------------------------------------------------------------------

// Every pick must name a supplier registration. A relay is signed against a supplier's session
// and each supplier carries its own per-session service allowance, so a pick that resolved only
// to a machine would have nothing to sign with.
func Test_EveryPickResolvesToAConcreteSupplier(t *testing.T) {
	c := require.New(t)
	pool := productionShapedPool()
	inPool := make(map[protocol.EndpointAddr]struct{}, len(pool))
	for _, ep := range pool {
		inPool[ep] = struct{}{}
	}

	for _, k := range []int{1, 2, 5, 1000} {
		for _, share := range []float64{0, 0.3, 0.45, 0.65, 1} {
			pinBackendWeightCap(t, k)
			pinServiceSettings(t, "supplier-svc", 0, share)
			for i := 0; i < 2_000; i++ {
				sel := PickBackendUniformForService("supplier-svc", pool)
				c.NotEmpty(sel, "K=%d cap=%v produced an empty pick", k, share)
				_, found := inPool[sel]
				c.True(found, "K=%d cap=%v produced an endpoint outside the pool: %s", k, share, sel)
			}
		}
	}
}

// Every registration behind a chosen machine must stay reachable: allowance is per supplier, so
// pinning one of several siblings would exhaust it while the others sat unused.
func Test_RegistrationsBehindAMachineAllStayReachable(t *testing.T) {
	c := require.New(t)
	pinBackendWeightCap(t, 2)
	pinServiceSettings(t, "spread-svc", 0, 0)

	pool := buildPool([][]int{{5}, {1}})
	shares := sampleShares(t, 120_000, func() protocol.EndpointAddr {
		return PickBackendUniformForService("spread-svc", pool)
	}, func(ep protocol.EndpointAddr) string { return string(ep) })

	c.Len(shares, 6, "every supplier registration must remain selectable")
	// The 5-registration machine draws 2/3 of the traffic (min(5,2) vs 1) and splits it evenly.
	for i := 0; i < 5; i++ {
		c.InDelta((2.0/3.0)/5, shares[string(pool[i])], 0.01)
	}
}

// The broadest revert must restore the pre-dedup behavior in full: registration-proportional
// weighting AND no cap on the serving pick.
func Test_BackendURLDedupOffSwitchRestoresFlatRegistrationPick(t *testing.T) {
	c := require.New(t)
	pinBackendWeightCap(t, 2)
	pinServiceSettings(t, "off-svc", 0, 0.45)
	SetOperatorShareBackendURLDedup(false)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	pool := buildPool([][]int{{4, 4, 4}, {1}, {1}}) // 14 registrations: 12 / 1 / 1
	shares := shareByOperator(t, 120_000, func() protocol.EndpointAddr {
		return PickBackendUniformForService("off-svc", pool)
	})

	c.InDelta(12.0/14, shares[operatorDomain(0)], 0.02,
		"the off-switch must restore a flat pick over registrations, uncapped")
}

// ---------------------------------------------------------------------------------------
// The serving path end to end
// ---------------------------------------------------------------------------------------

// SelectEndpointsWithDiversity's first pick IS the serving endpoint when
// max_parallel_endpoints=1, so the weighting and the cap have to be observable through it —
// not just through the helper it calls.
func Test_DiversitySelector_ServingPickIsWeightedAndCapped(t *testing.T) {
	c := require.New(t)
	logger := polyzero.NewLogger()
	pinBackendWeightCap(t, 2)
	pinServiceSettings(t, "diversity-svc", 0, 0.45)

	pool := buildPool([][]int{{4, 4, 4}, {1}, {1}})
	shares := shareByOperator(t, 120_000, func() protocol.EndpointAddr {
		selected := SelectEndpointsWithDiversity(logger, "diversity-svc", pool, 1)
		if len(selected) != 1 {
			t.Fatalf("expected exactly one endpoint, got %d", len(selected))
		}
		return selected[0]
	})

	// Same pool and therefore the same arithmetic as Test_ServingPick_OperatorCapBinds: the two
	// thin operators cap out at 3x their entitlement and the dominant one keeps the remainder.
	c.InDelta(0.572, shares[operatorDomain(0)], 0.02,
		"the endpoint that serves the request must be capped, not just the helper's output")
	c.InDelta(0.214, shares[operatorDomain(1)], 0.02)
	c.InDelta(0.214, shares[operatorDomain(2)], 0.02)
}

// poolSizeSample returns the last pool size observed on a (service, path) series.
func poolSizeSample(t *testing.T, serviceID, path string) float64 {
	t.Helper()
	observer, err := metrics.SelectionPoolSize.GetMetricWithLabelValues(serviceID, path)
	require.NoError(t, err)
	var m dto.Metric
	require.NoError(t, observer.(prometheus.Metric).Write(&m))
	require.NotNil(t, m.GetHistogram())
	require.EqualValues(t, 1, m.GetHistogram().GetSampleCount(), "expected exactly one observation")
	return m.GetHistogram().GetSampleSum()
}

// path_selection_pool_size must report the pool in the currency the decision was made in, not
// in raw registration counts. That is what makes a canary A/B readable off the existing
// dashboards: the same pool reports 50 under the broadest revert, 14 at K=1 and 25 at K=2.
func Test_Metrics_PoolSizeIsReportedInTheWeightingCurrency(t *testing.T) {
	c := require.New(t)
	logger := polyzero.NewLogger()
	// 50 supplier registrations across 14 machines — the measured production shape.
	pool := productionShapedPool()
	c.Len(pool, 50)

	pinServiceSettings(t, "currency-k1", 0, 0)
	pinBackendWeightCap(t, 1)
	SelectEndpointsWithDiversity(logger, "currency-k1", pool, 1)
	c.InDelta(14, poolSizeSample(t, "currency-k1", metrics.SelectionPathDiversity), 0.001,
		"K=1 reports distinct machines")

	pinServiceSettings(t, "currency-k2", 0, 0)
	SetBackendRegistrationWeightCap(2)
	SelectEndpointsWithDiversity(logger, "currency-k2", pool, 1)
	c.InDelta(25, poolSizeSample(t, "currency-k2", metrics.SelectionPathDiversity), 0.001,
		"K=2 reports the min(registrations, K) sum")

	pinServiceSettings(t, "currency-off", 0, 0)
	SetOperatorShareBackendURLDedup(false)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })
	SelectEndpointsWithDiversity(logger, "currency-off", pool, 1)
	c.InDelta(50, poolSizeSample(t, "currency-off", metrics.SelectionPathDiversity), 0.001,
		"the off-switch reports raw supplier registrations")
}

// The same selector with the cap switched off must fall back to the uncapped basis, so the
// canary A/B genuinely isolates the two halves.
func Test_DiversitySelector_ServingPickHonorsTheCapOffSwitch(t *testing.T) {
	c := require.New(t)
	logger := polyzero.NewLogger()
	pinBackendWeightCap(t, 2)
	pinServiceSettings(t, "diversity-off-svc", 0, 0.45)
	SetBackendPickOperatorCap(false)
	t.Cleanup(func() { SetBackendPickOperatorCap(true) })

	pool := buildPool([][]int{{4, 4, 4}, {1}, {1}})
	shares := shareByOperator(t, 120_000, func() protocol.EndpointAddr {
		return SelectEndpointsWithDiversity(logger, "diversity-off-svc", pool, 1)[0]
	})

	c.InDelta(0.75, shares[operatorDomain(0)], 0.02)
}
