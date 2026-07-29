package selector

import (
	"math/rand"
	"strings"
	"sync/atomic"

	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/protocol"
)

// concentrationCapEpsilon guards the water-filling loop against floating-point
// rounding when comparing an operator's weight to the cap.
const concentrationCapEpsilon = 1e-9

// SelectWithConcentrationCap picks one endpoint from validEndpoints, biased so that
// no single operator (eTLD+1) exceeds maxOperatorShare of the selection probability.
//
// Below the cap, behavior is identical to a flat random pick (share still tracks an
// operator's endpoint-count). Only the probability mass that a dominant operator holds
// *above* the cap is redistributed — proportionally, via water-filling — to the
// under-cap operators. This bounds the blast radius of any single operator failing
// (e.g. a supplier that holds most of a service's endpoints) without over-correcting
// toward thin operators the way a two-stage uniform pick would.
//
// Selection is two-step: pick an operator by its (capped) weight, then pick an endpoint
// uniformly within that operator.
//
// The cap is DISABLED (byte-for-byte a flat random pick) when:
//   - maxOperatorShare <= 0 or >= 1 (the config off-switch), or
//   - there are 0 or 1 endpoints.
//
// Endpoints whose eTLD+1 cannot be resolved are each treated as their own singleton
// operator (never merged into one bucket — that would fabricate concentration).
//
// serviceID is used only to attribute the reshape metric emitted when the cap actually
// alters the selection distribution.
func SelectWithConcentrationCap(
	serviceID protocol.ServiceID,
	validEndpoints protocol.EndpointAddrList,
	maxOperatorShare float64,
) protocol.EndpointAddr {
	n := len(validEndpoints)
	if n == 0 {
		return protocol.EndpointAddr("")
	}

	// Disabled / trivial: preserve the exact prior behavior (flat random pick).
	if n == 1 || maxOperatorShare <= 0 || maxOperatorShare >= 1 {
		selected := validEndpoints[rand.Intn(n)]
		// Instrumented too: "the cap was disabled" is one of the explanations the
		// selection metrics exist to distinguish, and it is invisible from the outside.
		recordSelectionPool(serviceID, validEndpoints, selected)
		return selected
	}

	// Group the pool into operators and, within each, backend URLs. `units` is the
	// currency the cap is denominated in: distinct backend URLs when dedup is on, raw
	// supplier registrations when it is off (see groupPool).
	pools, totalUnits, maxUnits := groupPool(validEndpoints)
	m := len(pools)

	// Counts for the selection metric, expressed in the same currency the weighting used,
	// so the dashboards show the basis the decision was actually made on.
	counts := make(map[string]int, m)
	for _, p := range pools {
		counts[p.key] = p.units()
	}

	// The cap reshapes only when some operator exceeds it (units_i > cap*totalUnits) or the
	// pool is too concentrated for the cap to be satisfiable (cap*m <= 1 ->
	// uniform-over-operators). A single operator, or a dominant share already at/under the
	// cap, is a no-op: the capped weighted pick reduces to a flat pick over units, so take
	// that directly.
	infeasible := maxOperatorShare*float64(m) <= 1.0
	if m == 1 || (!infeasible && float64(maxUnits) <= maxOperatorShare*float64(totalUnits)) {
		selected, opKey := pickUniformOverUnits(pools, totalUnits)
		// Instrumented: "the cap was a no-op" is the majority of selections and the case a
		// reshape-only metric cannot see, so it is exactly where a skew would hide.
		// m == 1 means the pool reached the selector already collapsed to one operator.
		metrics.RecordSelectionPool(string(serviceID), metrics.SelectionPathConcentrationCap, counts, totalUnits, opKey)
		return selected
	}

	// Phase 2 - reshape. The distribution is actually being altered here, so record it.
	metrics.RecordConcentrationCapReshaped(string(serviceID))

	weights := make([]float64, m)
	if infeasible {
		// No assignment can hold every operator under the cap -> best achievable is
		// uniform-over-operators (max share 1/m).
		for i := range weights {
			weights[i] = 1.0 / float64(m)
		}
	} else {
		// Per-unit-uniform (units_i/totalUnits) start, then water-fill the over-cap mass down.
		for i, p := range pools {
			weights[i] = float64(p.units()) / float64(totalUnits)
		}
		waterFillToCap(weights, maxOperatorShare)
	}

	// Weighted pick of an operator, then a unit within it, then a supplier within the unit.
	chosen := pools[weightedPick(weights)]
	selected := chosen.pickEndpoint()
	metrics.RecordSelectionPool(string(serviceID), metrics.SelectionPathConcentrationCap, counts, totalUnits, chosen.key)
	return selected
}

// dedupeOperatorShareByBackendURL controls whether an operator's selection share is
// measured in distinct BACKEND URLs rather than in supplier registrations.
//
// Why it exists: several suppliers can register against the SAME backend URL, and the
// concentration cap was counting each registration as an independent endpoint. An operator
// fronting 6 backends behind 25 supplier registrations was therefore scored at 25/N rather
// than 6/N - inflating its share far past what its actual infrastructure represents, and
// past the cap, while the endpoints it was "spread" across were the same 6 machines. This
// is the selection-side counterpart of the health-check backend-URL dedup.
//
// Deduping is a NO-OP for operators that register one supplier per URL: distinct-URL counts
// equal registration counts, so the weights and the pick are identical. The behavior only
// changes for operators that stack registrations behind shared URLs, which is the case it
// targets.
//
// TRADE-OFF, deliberate: a supplier registration carries its own per-session service
// allowance, so N registrations behind one URL do represent more serviceable capacity than
// one. Deduping weights that backend by its failure domain (one machine) rather than by its
// stake. That is the correct basis for a cap whose purpose is blast radius, but it does mean
// a heavily-staked shared backend receives less traffic than its allowance alone would
// justify. The pick still resolves to a specific supplier (see backendGroup.pick), so the
// per-supplier allowance is spread across the registrations behind the chosen URL rather
// than pinned to one of them.
//
// Default ON. Set PATH_OPERATOR_SHARE_BY_BACKEND_URL=false to restore registration-counted
// shares without a redeploy of the selection logic.
var dedupeOperatorShareByBackendURL atomic.Bool

func init() {
	dedupeOperatorShareByBackendURL.Store(true)
}

// SetOperatorShareBackendURLDedup toggles backend-URL-denominated operator shares.
func SetOperatorShareBackendURLDedup(enabled bool) {
	dedupeOperatorShareByBackendURL.Store(enabled)
}

// OperatorShareBackendURLDedupEnabled reports the current setting.
func OperatorShareBackendURLDedupEnabled() bool {
	return dedupeOperatorShareByBackendURL.Load()
}

// backendGroup is the set of supplier registrations that share one backend URL - i.e. one
// failure domain, addressable through several distinct on-chain suppliers.
type backendGroup struct {
	endpoints protocol.EndpointAddrList
}

// pick returns one supplier registration for this backend, chosen uniformly.
//
// The pick MUST resolve to a specific supplier: a relay is signed against a supplier's
// session and each supplier carries its own per-session service allowance. Spreading
// uniformly across the registrations behind a URL also spreads allowance consumption over
// them instead of exhausting one while its siblings sit unused.
func (b *backendGroup) pick() protocol.EndpointAddr {
	return b.endpoints[rand.Intn(len(b.endpoints))]
}

// operatorPool is one operator (eTLD+1) and the backends it fronts.
type operatorPool struct {
	key      string
	backends []*backendGroup
	entries  int // total supplier registrations across this operator's backends
}

// units returns the operator's weight currency: distinct backends when dedup is on, raw
// supplier registrations when it is off.
func (p *operatorPool) units() int {
	if dedupeOperatorShareByBackendURL.Load() {
		return len(p.backends)
	}
	return p.entries
}

// pickEndpoint chooses uniformly within the operator, in whatever currency is in force:
// backend-then-supplier when deduping (so a URL with many registrations does not outweigh
// its siblings), else uniformly over every registration.
func (p *operatorPool) pickEndpoint() protocol.EndpointAddr {
	if dedupeOperatorShareByBackendURL.Load() {
		return p.backends[rand.Intn(len(p.backends))].pick()
	}
	idx := rand.Intn(p.entries)
	for _, b := range p.backends {
		if idx < len(b.endpoints) {
			return b.endpoints[idx]
		}
		idx -= len(b.endpoints)
	}
	// Unreachable while entries is the sum of backend sizes; fall back rather than panic.
	return p.backends[len(p.backends)-1].pick()
}

// groupPool buckets endpoints by operator and, within each operator, by backend URL.
// Returns the operator pools in first-seen order (deterministic given the input), the total
// number of weight units across all operators, and the largest single operator's units.
func groupPool(validEndpoints protocol.EndpointAddrList) (pools []*operatorPool, totalUnits, maxUnits int) {
	byOperator := make(map[string]*operatorPool)
	byBackend := make(map[string]map[string]*backendGroup)

	for _, ep := range validEndpoints {
		opKey := operatorKey(ep)
		pool, seen := byOperator[opKey]
		if !seen {
			pool = &operatorPool{key: opKey}
			byOperator[opKey] = pool
			byBackend[opKey] = make(map[string]*backendGroup)
			pools = append(pools, pool)
		}
		pool.entries++

		bKey := backendKey(ep)
		group, exists := byBackend[opKey][bKey]
		if !exists {
			group = &backendGroup{}
			byBackend[opKey][bKey] = group
			pool.backends = append(pool.backends, group)
		}
		group.endpoints = append(group.endpoints, ep)
	}

	for _, p := range pools {
		u := p.units()
		totalUnits += u
		if u > maxUnits {
			maxUnits = u
		}
	}
	return pools, totalUnits, maxUnits
}

// pickUniformOverUnits picks a unit uniformly across the whole pool (not per operator), then
// resolves it to a supplier. With dedup off this is exactly a flat random pick over every
// registration - byte-for-byte the pre-cap behavior. With dedup on it is a flat random pick
// over distinct backends, which is the same statement in the deduped currency.
func pickUniformOverUnits(pools []*operatorPool, totalUnits int) (protocol.EndpointAddr, string) {
	if totalUnits <= 0 {
		return protocol.EndpointAddr(""), ""
	}
	idx := rand.Intn(totalUnits)
	for _, p := range pools {
		u := p.units()
		if idx >= u {
			idx -= u
			continue
		}
		if dedupeOperatorShareByBackendURL.Load() {
			return p.backends[idx].pick(), p.key
		}
		return p.pickEndpoint(), p.key
	}
	last := pools[len(pools)-1]
	return last.pickEndpoint(), last.key
}

// backendKey returns the backend-URL identity of an endpoint: the failure domain several
// supplier registrations can share. Endpoint addresses are "<supplier>-<url>", so the URL is
// taken from the first "http" onward. Normalized (lowercased, trailing "/" stripped) so that
// registrations differing only in trailing slash or case are recognized as the same backend.
//
// Falls back to the full address when no URL can be found, keeping such endpoints as their
// own singleton backends rather than merging them - merging would fabricate deduplication.
func backendKey(ep protocol.EndpointAddr) string {
	addr := string(ep)
	idx := strings.Index(addr, "http")
	if idx == -1 {
		return addr
	}
	url := strings.TrimRight(strings.ToLower(addr[idx:]), "/")
	if url == "" {
		return addr
	}
	return url
}

// BackendKey exposes the backend-URL identity of an endpoint to other packages. Callers that
// need to reason about the shared failure domain — every supplier registration fronting one
// machine — must use this rather than comparing endpoint addresses, which treats sibling
// registrations at the same URL as unrelated.
func BackendKey(ep protocol.EndpointAddr) string { return backendKey(ep) }

// OperatorKey exposes the operator (eTLD+1) bucket of an endpoint to other packages, so
// operator-scoped decisions outside this package use the same bucketing the concentration cap
// and the operator-uniform selector use.
func OperatorKey(ep protocol.EndpointAddr) string { return operatorKey(ep) }

// recordSelectionPool resolves the candidate pool's operator composition and records one
// selection. Used only on the paths that exit before Phase 1 has built the counts map; the
// others pass their already-computed counts to metrics.RecordSelectionPool directly rather
// than resolving every operator key a second time.
func recordSelectionPool(
	serviceID protocol.ServiceID,
	validEndpoints protocol.EndpointAddrList,
	selected protocol.EndpointAddr,
) {
	counts := make(map[string]int, len(validEndpoints))
	for _, ep := range validEndpoints {
		counts[operatorKey(ep)]++
	}
	metrics.RecordSelectionPool(string(serviceID), metrics.SelectionPathConcentrationCap, counts, len(validEndpoints), operatorKey(selected))
}

// SelectOperatorUniform picks one endpoint so that every operator (eTLD+1) present is equally
// likely, then an endpoint uniformly within the chosen operator. Unlike
// SelectWithConcentrationCap — which is endpoint-count-weighted and only trims the mass a
// dominant operator holds *above* the cap — this gives each operator an identical share
// regardless of how many endpoints it runs. It is the strongest per-operator spread, used
// for the WebSocket rebind path where distributing connections across providers matters more
// than matching endpoint capacity.
//
// Trade-off: a thin operator receives the same share as a large one, so a single-endpoint
// operator absorbs a full 1/m of the load. Callers that cannot tolerate overloading a small
// provider should use the concentration cap instead.
//
// Endpoints whose eTLD+1 cannot be resolved are each their own singleton operator (via
// operatorKey), never merged. serviceID is currently unused but kept for signature symmetry
// with SelectWithConcentrationCap and future metric attribution.
func SelectOperatorUniform(
	serviceID protocol.ServiceID,
	validEndpoints protocol.EndpointAddrList,
) protocol.EndpointAddr {
	_ = serviceID
	n := len(validEndpoints)
	if n == 0 {
		return protocol.EndpointAddr("")
	}
	if n == 1 {
		return validEndpoints[0]
	}

	// Operator-uniform is already immune to registration-stacking AT THE OPERATOR LEVEL —
	// every operator gets 1/m regardless of how many endpoints it registers. The skew it
	// still had was INSIDE an operator: picking uniformly over registrations gave a backend
	// with 7 supplier records 7x the traffic of a sibling with 1, even though both are one
	// machine. groupPool's per-operator pick spreads over distinct backends first, then over
	// the supplier registrations behind the chosen backend (preserving supplier identity for
	// signing and spreading per-supplier service allowance).
	pools, _, _ := groupPool(validEndpoints)
	return pools[rand.Intn(len(pools))].pickEndpoint()
}

// operatorKey returns the operator bucket for an endpoint: its eTLD+1, or the endpoint
// address itself when the eTLD+1 cannot be resolved (so unresolvable endpoints stay
// singletons and are never merged, which would fabricate concentration).
func operatorKey(ep protocol.EndpointAddr) string {
	if k := shannonmetrics.ExtractTLDFromEndpointAddr(string(ep)); k != "" {
		return k
	}
	return string(ep)
}

// waterFillToCap clamps any weight above cap and redistributes the excess to the
// under-cap weights, proportionally to their current weight, until no weight exceeds
// the cap. The caller only invokes this when the cap is feasible (cap*len(weights) > 1)
// and some weight is over the cap, so total mass is preserved (≈ 1); the underSum guard
// keeps it safe even if that ever fails to hold. Operates in place.
func waterFillToCap(weights []float64, maxShare float64) {
	// At most len(weights) passes: each pass pins at least one new operator to the cap.
	for pass := 0; pass < len(weights); pass++ {
		var excess, underSum float64
		anyOver := false
		for _, w := range weights {
			switch {
			case w > maxShare+concentrationCapEpsilon:
				excess += w - maxShare
				anyOver = true
			case w < maxShare-concentrationCapEpsilon:
				underSum += w
			}
		}
		if !anyOver {
			return
		}
		// No under-cap operator to absorb the excess (only reachable if the caller's
		// feasibility guarantee is violated). Clamp what we can and stop rather than
		// dividing by zero.
		if underSum <= 0 {
			for i, w := range weights {
				if w > maxShare+concentrationCapEpsilon {
					weights[i] = maxShare
				}
			}
			return
		}
		// Redistribute excess to the under-cap operators, proportional to their weight.
		for i, w := range weights {
			if w > maxShare+concentrationCapEpsilon {
				weights[i] = maxShare
			} else if w < maxShare-concentrationCapEpsilon {
				weights[i] = w + excess*(w/underSum)
			}
		}
	}
}

// weightedPick returns an index chosen with probability proportional to weights[i].
// Assumes weights sum to ~1 and are non-negative. Falls back to the last index on
// floating-point shortfall.
func weightedPick(weights []float64) int {
	var total float64
	for _, w := range weights {
		total += w
	}
	target := rand.Float64() * total
	var cum float64
	for i, w := range weights {
		cum += w
		if target < cum {
			return i
		}
	}
	return len(weights) - 1
}

// PickBackendUniform picks one endpoint so that every distinct BACKEND URL in the pool is
// equally likely, then a supplier registration uniformly within the chosen backend.
//
// This is the dedup rule applied to a pool that has ALREADY been quality-filtered by the
// caller — the reputation-ranked retry/hedge band, where every member is within the score
// epsilon and therefore equally acceptable. Uniform-over-registrations there hands a backend
// with 7 supplier records 7x the traffic of an equally-scored sibling with 1, even though
// both are one machine: the band looks diverse while the traffic is not.
//
// Returns a specific supplier registration, never a bare URL — a relay is signed against a
// supplier's session and each supplier carries its own per-session service allowance.
//
// With backend-URL dedup disabled this is a flat uniform pick over the pool, i.e. exactly the
// prior behavior.
func PickBackendUniform(endpoints protocol.EndpointAddrList) protocol.EndpointAddr {
	n := len(endpoints)
	if n == 0 {
		return protocol.EndpointAddr("")
	}
	if n == 1 || !dedupeOperatorShareByBackendURL.Load() {
		return endpoints[rand.Intn(n)]
	}

	groups := make(map[string]*backendGroup, n)
	order := make([]*backendGroup, 0, n)
	for _, ep := range endpoints {
		k := backendKey(ep)
		g, seen := groups[k]
		if !seen {
			g = &backendGroup{}
			groups[k] = g
			order = append(order, g)
		}
		g.endpoints = append(g.endpoints, ep)
	}

	return order[rand.Intn(len(order))].pick()
}

// CountDistinctBackends returns how many distinct backend URLs a pool spans. Exported for
// observability: a band of 25 endpoints across 6 backends is far less diverse than its
// member count suggests, and that gap is the thing worth logging.
func CountDistinctBackends(endpoints protocol.EndpointAddrList) int {
	if len(endpoints) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(endpoints))
	for _, ep := range endpoints {
		seen[backendKey(ep)] = struct{}{}
	}
	return len(seen)
}
