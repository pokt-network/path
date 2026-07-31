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
	SetDefaultMaxOperatorShare(0.45)
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
	withSelectionConfig(t, services, DefaultBackendRegistrationWeightCap, 0.45, func() {
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
		limit := 0.45
		if float64(operators)*0.45 <= 1.0 {
			limit = infeasibleCapFallbackShare
		}
		for op, share := range after[svc] {
			// Single-operator pools have nowhere to redistribute to; 100% is not a cap failure.
			if operators <= 1 {
				continue
			}
			if share > limit+0.02 {
				t.Errorf("%s: operator %s holds %.1f%% after, above the %.0f%% ceiling for a %d-operator pool",
					svc, op, share*100, limit*100, operators)
			}
		}
	}

	// 3. NO SMALL OPERATOR IS OVERLOADED. The failure mode of tightening a cap is dumping load
	//    onto a thin operator that cannot absorb it. Flag any operator whose share more than
	//    doubles AND lands above a fifth of the service.
	for _, m := range moves {
		if m.bef > 0 && m.aft > 2*m.bef && m.aft > 0.20 {
			t.Errorf("%s: operator %s goes %.1f%% -> %.1f%% (>2x, above 20%%) - verify it has the capacity before shipping",
				m.svc, m.op, m.bef*100, m.aft*100)
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

// Test_ProductionDryRun_BlastRadiusPerMachine measures the number the per-operator table cannot
// show: the share of a service that depends on ONE backend URL.
//
// Capping an operator improves per-operator concentration and can worsen this at the same time.
// The displaced share is redistributed proportionally to weight, and weight counts stacked
// registrations, so an operator running a single machine behind several registrations absorbs a
// large slice of it onto that one machine. Per-operator risk falls while per-machine risk rises.
//
// This is the metric that answers "how much of the service dies if one box dies", which is the
// actual reason the cap exists.
func Test_ProductionDryRun_BlastRadiusPerMachine(t *testing.T) {
	pools := loadProductionPools(t)
	services := make([]protocol.ServiceID, 0, len(pools))
	names := make([]string, 0, len(pools))
	for svc := range pools {
		services = append(services, protocol.ServiceID(svc))
		names = append(names, svc)
	}
	sort.Strings(names)

	worstBackendShare := func(pool protocol.EndpointAddrList, svc protocol.ServiceID) (float64, string) {
		counts := map[string]int{}
		const draws = 20_000
		for i := 0; i < draws; i++ {
			sel := PickBackendUniformForService(svc, pool)
			if sel == "" {
				continue
			}
			counts[backendKey(sel)]++
		}
		var top float64
		var which string
		for b, c := range counts {
			if s := float64(c) / float64(draws); s > top {
				top, which = s, b
			}
		}
		return top, which
	}

	before := map[string]float64{}
	withSelectionConfig(t, services, 1, 0.65, func() {
		for _, svc := range names {
			before[svc], _ = worstBackendShare(pools[svc], protocol.ServiceID(svc))
		}
	})

	after := map[string]float64{}
	afterWhich := map[string]string{}
	withSelectionConfig(t, services, DefaultBackendRegistrationWeightCap, 0.45, func() {
		for _, svc := range names {
			after[svc], afterWhich[svc] = worstBackendShare(pools[svc], protocol.ServiceID(svc))
		}
	})
	t.Cleanup(restoreShippedDefaults)

	type row struct {
		svc            string
		bef, aft, diff float64
		machine        string
	}
	var rows []row
	for _, svc := range names {
		rows = append(rows, row{svc, before[svc], after[svc], after[svc] - before[svc], afterWhich[svc]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].diff > rows[j].diff })

	t.Logf("=== single-machine blast radius: 12 services where it grows most ===")
	t.Logf("%-24s %8s %8s %9s", "service", "before", "after", "change")
	worsened := 0
	for i, r := range rows {
		if r.diff > 0.001 {
			worsened++
		}
		if i < 12 {
			t.Logf("%-24s %7.1f%% %7.1f%% %+8.1fpp", r.svc, r.bef*100, r.aft*100, r.diff*100)
		}
	}
	t.Logf("services where single-machine exposure grows at all: %d of %d", worsened, len(names))

	// A single machine carrying more than a third of a service is the concentration this cap
	// exists to prevent — but the gate must fire on what THIS CHANGE causes, not on a level the
	// pool already had. A thin two-operator pool where one machine already carried a third is a
	// staking shortage; failing on it every run would train the next reader to ignore this test.
	//
	// So: fail only when the change both leaves a machine above the threshold AND materially
	// worsened it. Pre-existing exposure is logged instead, because it is worth seeing.
	const machineExposureCeiling = 0.34
	const materialWorsening = 0.02
	for _, r := range rows {
		operators := map[string]bool{}
		for _, ep := range pools[r.svc] {
			operators[operatorKey(ep)] = true
		}
		// A single-operator pool has nowhere to redistribute to; its exposure is a staking
		// problem, not a selection one.
		if len(operators) < 2 {
			continue
		}
		switch {
		case r.aft > machineExposureCeiling && r.diff > materialWorsening:
			t.Errorf("%s: this change pushes one machine (%s) to %.1f%% of the service, up from "+
				"%.1f%% - per-operator concentration improved while per-machine concentration did not",
				r.svc, r.machine, r.aft*100, r.bef*100)
		case r.aft > machineExposureCeiling:
			t.Logf("PRE-EXISTING: %s already depends on one machine for %.1f%% (%.1f%% before this "+
				"change) - a staking shortage, not something selection can fix",
				r.svc, r.aft*100, r.bef*100)
		}
	}
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

	withSelectionConfig(t, services, DefaultBackendRegistrationWeightCap, 0.45, func() {
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
	afterReach := reach(DefaultBackendRegistrationWeightCap, 0.45)
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

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

var _ = fmt.Sprintf
