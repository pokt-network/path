package selector

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// Mirrors the real shape that motivated backend-URL dedup: one operator fronting a few
// machines behind many supplier registrations, against operators that register one supplier
// per machine.
func stackedPool() protocol.EndpointAddrList {
	var eps protocol.EndpointAddrList
	// bigop: 2 backends, 8 registrations (6 behind one URL, 2 behind the other).
	for i := 0; i < 6; i++ {
		eps = append(eps, protocol.EndpointAddr("pokt1big"+string(rune('a'+i))+"-https://one.bigop.net/"))
	}
	for i := 0; i < 2; i++ {
		eps = append(eps, protocol.EndpointAddr("pokt1bigx"+string(rune('a'+i))+"-https://two.bigop.net/"))
	}
	// smallop: 2 backends, 2 registrations.
	eps = append(eps, protocol.EndpointAddr("pokt1sm1-https://one.smallop.xyz/"))
	eps = append(eps, protocol.EndpointAddr("pokt1sm2-https://two.smallop.xyz/"))
	return eps
}

// sampleShares runs pick n times and returns the observed share per key function.
func sampleShares(
	t *testing.T,
	n int,
	pick func() protocol.EndpointAddr,
	keyOf func(protocol.EndpointAddr) string,
) map[string]float64 {
	t.Helper()
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		ep := pick()
		require.NotEmpty(t, ep, "a pick must always resolve to an endpoint")
		counts[keyOf(ep)]++
	}
	shares := make(map[string]float64, len(counts))
	for k, c := range counts {
		shares[k] = float64(c) / float64(n)
	}
	return shares
}

func Test_PickBackendUniform_EqualizesMachinesNotRegistrations(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })
	// K=1 is the backend-uniform basis this test was written for; it is now the kill switch
	// rather than the default. Test_BackendWeightCap_K1_ReproducesBackendUniform asserts the
	// equivalence directly.
	pinBackendWeightCap(t, 1)

	pool := stackedPool()
	shares := sampleShares(t, 60000, func() protocol.EndpointAddr {
		return PickBackendUniform(pool)
	}, func(ep protocol.EndpointAddr) string { return backendKey(ep) })

	// 4 distinct backends -> each ~25%, regardless of how many registrations sit behind it.
	c.Len(shares, 4, "pool spans 4 distinct backend URLs")
	for backend, share := range shares {
		c.InDelta(0.25, share, 0.02, "backend %s should get an equal share of traffic", backend)
	}
}

func Test_PickBackendUniform_StackingRegistrationsNoLongerBuysTraffic(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })
	pinBackendWeightCap(t, 1)

	pool := stackedPool()
	shares := sampleShares(t, 60000, func() protocol.EndpointAddr {
		return PickBackendUniform(pool)
	}, operatorKeyForTest)

	// bigop holds 8 of 10 registrations (80%) but only 2 of 4 backends (50%).
	c.InDelta(0.50, shares["bigop.net"], 0.02,
		"an operator's share must track its machines (2/4), not its registrations (8/10)")
	c.InDelta(0.50, shares["smallop.xyz"], 0.02)
}

// The shipped default sits between the two extremes: stacking still buys something (the second
// registration behind a machine) and stops buying anything after that.
func Test_PickBackendUniform_StackingBuysBoundedTrafficAtDefaultK(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })
	pool := stackedPool()
	sample := func() map[string]float64 {
		return sampleShares(t, 60000, func() protocol.EndpointAddr {
			return PickBackendUniform(pool)
		}, operatorKeyForTest)
	}

	// At the SHIPPED default (K=1) stacking buys nothing: both operators front 2 machines, so
	// both get half, regardless of bigop's 8 registrations against smallop's 2. This is the
	// configuration the fleet runs — a dry run over the real pools showed K>1 raises operator
	// concentration rather than lowering it, because the operators who stack hardest are the
	// large ones. See DefaultBackendRegistrationWeightCap.
	pinBackendWeightCap(t, DefaultBackendRegistrationWeightCap)
	atDefault := sample()
	c.InDelta(0.5, atDefault["bigop.net"], 0.02, "at K=1 stacking must buy nothing")
	c.InDelta(0.5, atDefault["smallop.xyz"], 0.02)

	// At K=2, the opt-in, stacking buys a BOUNDED increase. Weights: bigop min(6,2)+min(2,2) = 4;
	// smallop 1+1 = 2. So 66.7%/33.3% — above the 50% a machine count alone gives (it does stake
	// more), far below the 80% its registration count would have bought.
	pinBackendWeightCap(t, 2)
	atK2 := sample()
	c.InDelta(4.0/6.0, atK2["bigop.net"], 0.02,
		"at K=2 stacking must buy a bounded increase, not a proportional one")
	c.InDelta(2.0/6.0, atK2["smallop.xyz"], 0.02)
}

// The constraint the user called out: a relay is signed against a supplier's session and
// each supplier carries its own per-session service allowance, so a pick must always name a
// supplier — never collapse to a bare URL — and must spread across the registrations behind
// the chosen backend rather than pinning one.
func Test_PickBackendUniform_ResolvesToASupplierAndSpreadsWithinBackend(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })
	pinBackendWeightCap(t, 1)

	pool := stackedPool()
	shares := sampleShares(t, 60000, func() protocol.EndpointAddr {
		return PickBackendUniform(pool)
	}, func(ep protocol.EndpointAddr) string { return string(ep) })

	// Every one of the 10 supplier registrations must be reachable — none stranded.
	c.Len(shares, len(pool), "every supplier registration must remain selectable")

	// The 6 registrations behind one.bigop.net split that backend's 25%: ~4.17% each.
	c.InDelta(0.25/6, shares["pokt1biga-https://one.bigop.net/"], 0.01,
		"allowance must spread across the registrations behind a backend, not pin one")
	// The single registration on a solo backend takes that backend's whole 25%.
	c.InDelta(0.25, shares["pokt1sm1-https://one.smallop.xyz/"], 0.02)
}

func Test_PickBackendUniform_DisabledIsFlatOverRegistrations(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(false)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	pool := stackedPool()
	shares := sampleShares(t, 60000, func() protocol.EndpointAddr {
		return PickBackendUniform(pool)
	}, operatorKeyForTest)

	// Off-switch must restore the prior behavior exactly: share tracks registration count.
	c.InDelta(0.80, shares["bigop.net"], 0.02,
		"with dedup disabled, share must track registrations (8/10) as before")
}

func Test_ConcentrationCap_DedupCanRemoveTheNeedToReshape(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })
	pinBackendWeightCap(t, 1)

	pool := stackedPool()
	// bigop is 80% by registration — over a 0.65 cap — but only 50% by backend, under it.
	shares := sampleShares(t, 60000, func() protocol.EndpointAddr {
		return SelectWithConcentrationCap("svc", pool, 0.65)
	}, operatorKeyForTest)

	c.InDelta(0.50, shares["bigop.net"], 0.02,
		"deduped share is already under the cap, so no water-filling should distort it")
}

func Test_ConcentrationCap_StillCapsWhenDedupedShareExceedsIt(t *testing.T) {
	c := require.New(t)
	SetOperatorShareBackendURLDedup(true)
	t.Cleanup(func() { SetOperatorShareBackendURLDedup(true) })

	// bigop genuinely runs 9 distinct machines; smallop runs 1. Dedup does not rescue that.
	var pool protocol.EndpointAddrList
	for i := 0; i < 9; i++ {
		pool = append(pool, protocol.EndpointAddr("pokt1b"+string(rune('a'+i))+"-https://m"+string(rune('a'+i))+".bigop.net/"))
	}
	pool = append(pool, protocol.EndpointAddr("pokt1s1-https://one.smallop.xyz/"))

	shares := sampleShares(t, 60000, func() protocol.EndpointAddr {
		return SelectWithConcentrationCap("svc", pool, 0.65)
	}, operatorKeyForTest)

	c.InDelta(0.65, shares["bigop.net"], 0.02,
		"real machine-level dominance must still be capped at maxOperatorShare")
}

func Test_backendKey_NormalizesTrailingSlashAndCase(t *testing.T) {
	c := require.New(t)
	// Same machine registered with and without a trailing slash, and with mixed case, must
	// resolve to one backend — otherwise dedup silently under-counts duplication.
	a := backendKey("pokt1a-https://Node.BigOp.net/")
	b := backendKey("pokt1b-https://node.bigop.net")
	c.Equal(a, b, "trailing slash and case must not split one backend into two")

	// Distinct paths on one host stay distinct: they may be genuinely different endpoints.
	c.NotEqual(backendKey("pokt1a-https://h.bigop.net/v1"), backendKey("pokt1a-https://h.bigop.net/v2"))

	// No resolvable URL -> the address is its own singleton backend, never merged.
	c.Equal("weird-addr-no-url", backendKey("weird-addr-no-url"))
}

func Test_CountDistinctBackends(t *testing.T) {
	c := require.New(t)
	c.Equal(0, CountDistinctBackends(nil))
	c.Equal(4, CountDistinctBackends(stackedPool()),
		"10 registrations across 4 machines must report 4")
}

// operatorKeyForTest exposes the production operator bucketing to these tests. The existing
// operatorOf helper in concentration_cap_test.go takes a string and re-derives the eTLD+1
// itself; this one goes through the real code path.
func operatorKeyForTest(ep protocol.EndpointAddr) string { return operatorKey(ep) }
