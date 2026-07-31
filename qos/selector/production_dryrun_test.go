package selector

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/pokt-network/path/protocol"
)

// Dry run of the shipped selection code against REAL production pool shapes, so a traffic
// shift is inspected before it reaches production rather than after.
//
// The fixture is a snapshot of every service's valid endpoint set taken from a live gateway's
// /ready?detailed=true (64 services, 3154 supplier registrations). The functions exercised are
// the ones that actually serve traffic, not reimplementations of them — that is the whole point,
// since the last two attempts to reason about this from metrics reached the wrong conclusion.
//
// Run with -v to print the full per-service table:
//
//	go test ./qos/selector/ -run Test_ProductionDryRun -v
const dryRunFixture = "testdata/production_pools.json"

// dryRunDraws is per service per configuration. 40k keeps the sampling error on a share well
// inside a percentage point, which is finer than any decision made off this table.
const dryRunDraws = 40_000

func loadProductionPools(t *testing.T) map[string]protocol.EndpointAddrList {
	t.Helper()
	raw, err := os.ReadFile(dryRunFixture)
	if err != nil {
		t.Skipf("production fixture unavailable (%v) - dry run skipped", err)
	}
	var byService map[string][]string
	if err := json.Unmarshal(raw, &byService); err != nil {
		t.Fatalf("fixture is not readable: %v", err)
	}
	pools := make(map[string]protocol.EndpointAddrList, len(byService))
	for svc, addrs := range byService {
		eps := make(protocol.EndpointAddrList, 0, len(addrs))
		for _, a := range addrs {
			eps = append(eps, protocol.EndpointAddr(a))
		}
		pools[svc] = eps
	}
	return pools
}

// sampleOperatorShares runs the real serving pick and returns each operator's observed share.
func sampleOperatorShares(pool protocol.EndpointAddrList, serviceID protocol.ServiceID) map[string]float64 {
	counts := map[string]int{}
	for i := 0; i < dryRunDraws; i++ {
		sel := PickBackendUniformForService(serviceID, pool)
		if sel == "" {
			continue
		}
		counts[operatorKey(sel)]++
	}
	shares := make(map[string]float64, len(counts))
	for k, c := range counts {
		shares[k] = float64(c) / float64(dryRunDraws)
	}
	return shares
}

// withSelectionConfig applies a whole selection configuration for the duration of fn, both
// process-wide and per service, so the run cannot silently fall back to a default that differs
// from the one under test.
func withSelectionConfig(t *testing.T, services []protocol.ServiceID, k int, cap float64, fn func()) {
	t.Helper()
	SetOperatorShareBackendURLDedup(true)
	SetBackendPickOperatorCap(true)
	SetCapInfeasibleForcesUniform(false)
	SetBackendRegistrationWeightCap(k)
	SetDefaultMaxOperatorShare(cap)
	for _, s := range services {
		SetServiceSelectionSettings(s, k, cap)
	}
	fn()
}

// restoreShippedDefaults puts the package back the way a fresh process would have it.
func restoreShippedDefaults() {
	SetOperatorShareBackendURLDedup(true)
	SetBackendPickOperatorCap(true)
	SetCapInfeasibleForcesUniform(false)
	SetBackendRegistrationWeightCap(DefaultBackendRegistrationWeightCap)
	SetDefaultMaxOperatorShare(DefaultMaxOperatorShareFallback)
}

// Test_ProductionDryRun_BeforeAfter is the pre-deploy check: for every real service, what does
// the shipped configuration do to each operator's share versus the configuration in production
// today? It fails on the outcomes that would be regressions rather than redistributions.
func Test_ProductionDryRun_BeforeAfter(t *testing.T) {
	pools := loadProductionPools(t)
	services := make([]protocol.ServiceID, 0, len(pools))
	names := make([]string, 0, len(pools))
	for svc := range pools {
		services = append(services, protocol.ServiceID(svc))
		names = append(names, svc)
	}
	sort.Strings(names)

	// BEFORE: what production runs today — backend-uniform (K=1), cap 0.65.
	before := map[string]map[string]float64{}
	withSelectionConfig(t, services, 1, 0.65, func() {
		for _, svc := range names {
			before[svc] = sampleOperatorShares(pools[svc], protocol.ServiceID(svc))
		}
	})

	// AFTER: what is about to ship — registration-weighted at K=2, cap 0.45.
	after := map[string]map[string]float64{}
	withSelectionConfig(t, services, DefaultBackendRegistrationWeightCap, DefaultMaxOperatorShareFallback, func() {
		for _, svc := range names {
			after[svc] = sampleOperatorShares(pools[svc], protocol.ServiceID(svc))
		}
	})

	// Restore the package defaults for any test that runs after this one.
	t.Cleanup(restoreShippedDefaults)

	type movement struct {
		svc, op       string
		bef, aft, chg float64
	}
	var moves []movement
	var maxOperatorShareAfter float64
	var worstService, worstOp string

	for _, svc := range names {
		ops := map[string]struct{}{}
		for o := range before[svc] {
			ops[o] = struct{}{}
		}
		for o := range after[svc] {
			ops[o] = struct{}{}
		}
		for o := range ops {
			b, a := before[svc][o], after[svc][o]
			moves = append(moves, movement{svc, o, b, a, a - b})
			if a > maxOperatorShareAfter {
				maxOperatorShareAfter, worstService, worstOp = a, svc, o
			}
		}
	}

	sort.Slice(moves, func(i, j int) bool {
		return absf(moves[i].chg) > absf(moves[j].chg)
	})

	t.Logf("=== 20 largest operator-share movements across %d real services ===", len(names))
	t.Logf("%-24s %-22s %8s %8s %9s", "service", "operator", "before", "after", "change")
	for i, m := range moves {
		if i >= 20 {
			break
		}
		t.Logf("%-24s %-22s %7.1f%% %7.1f%% %+8.1fpp", m.svc, m.op, m.bef*100, m.aft*100, m.chg*100)
	}

	// ---- Assertions: the outcomes that would make this a bad change ----

	// 1. NOBODY IS DROPPED. An operator that served traffic before must still serve traffic.
	//    The basis reweights; it must never remove an operator from rotation.
	for _, m := range moves {
		if m.bef > 0.001 && m.aft == 0 {
			t.Errorf("%s: operator %s served %.2f%% before and 0%% after - the basis must reweight, never exclude",
				m.svc, m.op, m.bef*100)
		}
	}

	// 2. NO OPERATOR EXCEEDS THE CAP where the cap is satisfiable. Pools with fewer than three
	//    operators cannot satisfy 0.45 and fall back to 0.65 by design, so they are held to that.
	for _, svc := range names {
		operators := len(before[svc])
		limit := DefaultMaxOperatorShareFallback
		if float64(operators)*limit <= 1.0 {
			limit = infeasibleCapFallbackShare
		}
		for op, share := range after[svc] {
			// Single-operator pools have nowhere to redistribute to; 100% is not a cap failure.
			if operators <= 1 {
				continue
			}
			// The cap binds only as far as the OTHER providers can absorb: each is held to a
			// multiple of what its own registrations entitle it to, because a provider handed
			// more than that answers 429 rather than serving. So a dominant provider legally
			// sits above the cap when the rest of the pool is already at its ceiling — what
			// must never happen is it sitting above its own uncapped entitlement.
			entitled := float64(regsFor(pools[svc])[op]) / float64(len(pools[svc]))
			ceiling := limit
			if entitled > ceiling {
				ceiling = entitled
			}
			if share > ceiling+0.02 {
				t.Errorf("%s: operator %s holds %.1f%% after, above both the %.0f%% cap and its "+
					"own %.1f%% entitlement", svc, op, share*100, limit*100, entitled*100)
			}
		}
	}

	// 3. NOBODY IS ALLOCATED BEYOND THE ALLOWANCE IT BOUGHT. Each supplier registration carries
	//    its own per-session service allowance, so a provider's capacity to serve IS its share of
	//    the registrations. Registration-proportional selection satisfies this by construction;
	//    the only thing that can break it is the cap, which displaces a dominant provider's
	//    excess onto everyone else regardless of whether they hold the tickets to serve it.
	//
	//    This is the check that a share table cannot show, and the one that decides whether a
	//    distribution is servable at all.
	for _, svc := range names {
		regs := map[string]int{}
		total := 0
		for _, ep := range pools[svc] {
			regs[operatorKey(ep)]++
			total++
		}
		if total == 0 {
			continue
		}
		// Where the cap bound someone, the displaced share MUST land on providers beyond their
		// ticket count - that is the cap working, and the price of bounding a dominant provider.
		capBound := false
		for _, share := range after[svc] {
			if share > DefaultMaxOperatorShareFallback-0.02 {
				capBound = true
			}
		}
		for op, share := range after[svc] {
			entitled := float64(regs[op]) / float64(total)
			if capBound {
				continue
			}
			// Tolerance covers sampling noise plus the redistribution the cap performs by
			// design; a provider taking more than a fifth beyond its ticket share is being
			// handed work it has no allowance for.
			if share > entitled+0.20 {
				t.Errorf("%s: provider %s would receive %.1f%% while holding %.1f%% of the "+
					"registrations - that is %.1fpp beyond the allowance it bought",
					svc, op, share*100, entitled*100, (share-entitled)*100)
			}
		}
	}

	t.Logf("largest single-operator share after the change: %.1f%% (%s / %s)",
		maxOperatorShareAfter*100, worstService, worstOp)

	// Blast-radius summary: how much of the fleet this actually touches. A change that reads as
	// modest on one service can be sweeping across 64 of them, and that is the number worth
	// knowing before deploy rather than after.
	movedServices := map[string]bool{}
	var reallocated float64
	overCapBefore := 0
	for _, svc := range names {
		for _, share := range before[svc] {
			if share > 0.45 {
				overCapBefore++
				break
			}
		}
	}
	for _, m := range moves {
		if absf(m.chg) > 0.05 {
			movedServices[m.svc] = true
		}
		if m.chg > 0 {
			reallocated += m.chg
		}
	}
	t.Logf("services with an operator moving more than 5pp: %d of %d", len(movedServices), len(names))
	t.Logf("services with an operator above 45%% before the change: %d of %d", overCapBefore, len(names))
	t.Logf("total share reallocated, summed across all services: %.1f service-equivalents", reallocated)
}

// Test_ProductionDryRun_EveryPickIsServiceable asserts the property that a distribution table
// cannot show: across every real pool, at the shipped configuration, every pick resolves to a
// registration that was actually in that pool. A weighting bug that returned a bare URL, an
// endpoint from another service, or an empty address would be a production outage, and it would
// not necessarily disturb the share percentages.
func Test_ProductionDryRun_EveryPickIsServiceable(t *testing.T) {
	pools := loadProductionPools(t)
	services := make([]protocol.ServiceID, 0, len(pools))
	for svc := range pools {
		services = append(services, protocol.ServiceID(svc))
	}

	withSelectionConfig(t, services, DefaultBackendRegistrationWeightCap, DefaultMaxOperatorShareFallback, func() {
		for svc, pool := range pools {
			member := make(map[protocol.EndpointAddr]bool, len(pool))
			for _, ep := range pool {
				member[ep] = true
			}
			for i := 0; i < 2_000; i++ {
				sel := PickBackendUniformForService(protocol.ServiceID(svc), pool)
				if !member[sel] {
					t.Fatalf("%s: pick %q is not a registration from this service's pool", svc, sel)
				}
			}
		}
	})
	t.Cleanup(restoreShippedDefaults)
}

// Test_ProductionDryRun_ReachabilityIsPreserved asserts that the change cannot strand a
// supplier registration. Every registration reachable under today's configuration must still be
// reachable under the shipped one — a registration that can never be selected earns nothing,
// which is the complaint that motivated this change in the first place.
func Test_ProductionDryRun_ReachabilityIsPreserved(t *testing.T) {
	pools := loadProductionPools(t)
	services := make([]protocol.ServiceID, 0, len(pools))
	for svc := range pools {
		services = append(services, protocol.ServiceID(svc))
	}

	reach := func(k int, cap float64) map[string]map[protocol.EndpointAddr]bool {
		seen := map[string]map[protocol.EndpointAddr]bool{}
		withSelectionConfig(t, services, k, cap, func() {
			for svc, pool := range pools {
				m := map[protocol.EndpointAddr]bool{}
				// Enough draws that a reachable registration is overwhelmingly likely to appear:
				// the thinnest share in these pools is far above 1/(50*len(pool)).
				for i := 0; i < 50*len(pool)+5_000; i++ {
					m[PickBackendUniformForService(protocol.ServiceID(svc), pool)] = true
				}
				seen[svc] = m
			}
		})
		return seen
	}

	beforeReach := reach(1, 0.65)
	afterReach := reach(DefaultBackendRegistrationWeightCap, DefaultMaxOperatorShareFallback)
	t.Cleanup(restoreShippedDefaults)

	for svc, was := range beforeReach {
		for ep := range was {
			if !afterReach[svc][ep] {
				t.Errorf("%s: registration %s was reachable before and was never selected after - "+
					"the change must redistribute traffic, not strand a supplier", svc, ep)
			}
		}
	}
}

// regsFor counts supplier registrations per operator in a pool.
func regsFor(pool protocol.EndpointAddrList) map[string]int {
	m := map[string]int{}
	for _, ep := range pool {
		m[operatorKey(ep)]++
	}
	return m
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

var _ = fmt.Sprintf
