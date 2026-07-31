package selector

import (
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/protocol"
)

// concentrationCapEpsilon guards the water-filling loop against floating-point
// rounding when comparing an operator's weight to the cap.
const concentrationCapEpsilon = 1e-9

// DefaultBackendRegistrationWeightCap is K, a cap on how much selection weight the supplier
// registrations behind ONE backend URL may accumulate: weight(backend) = min(registrations, K).
//
// SHIPPED UNCAPPED (0): a provider's share is proportional to the registrations it holds, and
// how it arranges those registrations across its own machines is not a routing input.
//
// WHY REGISTRATIONS ARE THE UNIT. Each supplier registration carries its own per-session
// service allowance, so a provider's ability to serve is set by how many registrations it
// holds — it is the thing the operator bought, and the thing the chain settles on. Weighting by
// distinct backend URL instead routes traffic by infrastructure shape: it sends work to
// providers who lack the allowance to serve it and starves providers who have it. Measured
// across all 64 production pools, the share of traffic allocated beyond what the receiving
// provider's allowance covers:
//
//	machine-weighted (K=1)      mean 33.4%   worst 51.4%
//	equal share per provider    mean 43.2%   worst 67.3%
//	registration-proportional   mean  0.0%   worst  0.0%
//
// Registration-proportional is zero by construction, because share and allowance are then
// denominated in the same unit.
//
// The case that settled it: on one service a provider held 19 of 50 registrations — more than
// any other provider on that service — and received 7.3% of its traffic, while a provider
// holding 15 received 45%, purely because the first ran two machines and the second fifteen.
//
// CONCENTRATION IS THE CAP'S JOB, NOT THE BASIS'S. Weighting by machines suppressed the largest
// provider only as a side effect, and paid for it by misallocating a third of all traffic. The
// per-operator share cap (max_operator_share) is the mechanism that stops one provider owning a
// session; it is applied on top of this basis and is the only thing that should be tuned for
// that purpose.
//
// K > 0 remains available as a lever — PATH_BACKEND_REGISTRATION_WEIGHT_CAP=1 restores
// machine-weighted selection, 2 gives a bounded middle — but neither is the default.
const DefaultBackendRegistrationWeightCap = 0

// DefaultMaxOperatorShareFallback is the per-operator concentration cap used for a service
// whose resolved configuration has not been published to this package (tests, and any binary
// that does not run the QoS bootstrap). It MUST track gateway.DefaultMaxOperatorShare, which
// is the config-layer source of truth; a test in the gateway package asserts they are equal.
const DefaultMaxOperatorShareFallback = 0.50

// backendWeightCap holds the process-wide K applied to services that do not configure their
// own. Settable at startup from PATH_BACKEND_REGISTRATION_WEIGHT_CAP so the weighting basis
// can be A/B-ed across environments without a code deploy.
var backendWeightCap atomic.Int32

// applyOperatorCapOnBackendPick controls whether the service-scoped backend pick — the one
// that chooses the endpoint that actually serves a request — applies the per-operator
// concentration cap on top of the weighting basis.
//
// It is a separate switch from the basis so the two halves of this change can be canaried
// independently: with it off, selection is registration-weighted but uncapped, which isolates
// whether a traffic shift came from the new basis or from the cap. Default ON; set
// PATH_PRIMARY_PICK_OPERATOR_CAP=false to disable.
var applyOperatorCapOnBackendPick atomic.Bool

// infeasibleCapFallbackShare is the cap applied to a pool whose configured cap cannot be
// satisfied by ANY assignment (maxOperatorShare * operatorCount <= 1). It is the default this
// fleet ran under before the cap was tightened, and it is feasible for every pool with two or
// more operators (0.65 * 2 > 1), so it always resolves the infeasibility it is invoked for.
//
// THE EDGE CASE IT EXISTS FOR. A cap of 0.50 is infeasible for every service whose valid pool
// spans exactly two operators — 0.50 * 2 <= 1 — and production has 9 of those. The two obvious
// treatments are both bad:
//
//   - Fall back to uniform-over-operators (the historical behavior). For two operators that is
//     a hard 50/50 however the pool is actually shaped, raising the smaller operator's traffic
//     by 25-67% on 9 services at the same instant (a service whose smaller operator runs 3
//     machines and one whose smaller operator runs 15 both jump from ~30% to 50%). It is
//     correct for blast radius and a genuine capacity risk, and nothing about the deploy ramps
//     it.
//
//   - Drop the cap entirely. That is a REGRESSION: those pools are capped today at 0.65, and
//     lowering the configured cap to 0.50 would silently stop capping them altogether.
//
// So neither the tightest nor the loosest treatment is acceptable, and the clamp that first
// suggests itself — effective cap = max(configured, 1/m) — is not a middle ground either: with
// a cap of exactly 1/m the only feasible assignment IS the uniform one, so that clamp is
// provably identical to forcing uniform-over-operators.
//
// The treatment shipped is therefore: where the tightened cap is satisfiable (three or more
// operators) it applies; where it is not (two operators) the previous 0.65 cap stays in force.
// Those services keep exactly the capping they have today — no shock, no regression — and they
// still receive the weighting-basis change.
const infeasibleCapFallbackShare = 0.65

// capInfeasibleForcesUniform restores the historical uniform-over-operators fallback for a cap
// no assignment can satisfy, instead of the infeasibleCapFallbackShare clamp described above.
//
// Default OFF — not because the new behavior is being held back, but because forcing 50/50 is
// a *different, stronger* intervention than the cap that was configured, and it lands on 9
// services at once. Set PATH_SELECTION_CAP_INFEASIBLE_UNIFORM=true to canary it once the
// headroom on those services' smaller operators is known.
var capInfeasibleForcesUniform atomic.Bool

// serviceSelectionSettings is the per-service selection configuration this package needs but
// cannot read directly: the config lives in the gateway layer, and the selection helpers are
// called from four QoS implementations that would otherwise each have to thread it through.
// It is published once at startup (cmd bootstrap) and read lock-free on the hot path.
type serviceSelectionSettings struct {
	// backendWeightCap is the service's K override; 0 means "unset, use the process-wide K".
	backendWeightCap int
	// maxOperatorShare is the service's resolved per-operator concentration cap.
	maxOperatorShare float64
}

var (
	// serviceSelectionSettingsMu serializes the copy-on-write publish; reads never take it.
	serviceSelectionSettingsMu sync.Mutex
	serviceSelectionSettingsBy atomic.Pointer[map[protocol.ServiceID]serviceSelectionSettings]
	// defaultMaxOperatorShare applies to a service with no published settings.
	defaultMaxOperatorShare AtomicFloat64
)

// init sets the shipped-on defaults. Every one of these is a live behavior, not an opt-in: the
// env overrides beside them exist to turn a behavior OFF on a running deployment, so a bad
// canary can be reverted without waiting on an image build.
func init() {
	dedupeOperatorShareByBackendURL.Store(true)
	backendWeightCap.Store(DefaultBackendRegistrationWeightCap)
	applyOperatorCapOnBackendPick.Store(true)
	capInfeasibleForcesUniform.Store(false)
	defaultMaxOperatorShare.Store(DefaultMaxOperatorShareFallback)
}

// SetBackendRegistrationWeightCap sets the process-wide K. Values below 1 are clamped to 1
// (backend-uniform); registration-proportional weighting is expressed as a large K rather than
// as 0, so that "unset" stays distinguishable from "uncapped".
func SetBackendRegistrationWeightCap(k int) {
	// 0 is the shipped value and means uncapped, i.e. registration-proportional. Negatives are
	// meaningless and collapse to it rather than to the machine-weighted extreme.
	if k < 0 {
		k = 0
	}
	backendWeightCap.Store(int32(k))
}

// BackendRegistrationWeightCap reports the process-wide K.
func BackendRegistrationWeightCap() int { return int(backendWeightCap.Load()) }

// SetBackendPickOperatorCap toggles the per-operator cap on the service-scoped backend pick.
func SetBackendPickOperatorCap(enabled bool) { applyOperatorCapOnBackendPick.Store(enabled) }

// BackendPickOperatorCapEnabled reports the current setting.
func BackendPickOperatorCapEnabled() bool { return applyOperatorCapOnBackendPick.Load() }

// SetCapInfeasibleForcesUniform toggles the uniform-over-operators fallback for a cap no
// assignment can satisfy. See capInfeasibleForcesUniform for why it ships off.
func SetCapInfeasibleForcesUniform(enabled bool) { capInfeasibleForcesUniform.Store(enabled) }

// CapInfeasibleForcesUniformEnabled reports the current setting.
func CapInfeasibleForcesUniformEnabled() bool { return capInfeasibleForcesUniform.Load() }

// SetDefaultMaxOperatorShare publishes the concentration cap used for services with no
// per-service settings.
func SetDefaultMaxOperatorShare(share float64) { defaultMaxOperatorShare.Store(share) }

// SetServiceSelectionSettings publishes one service's resolved selection settings.
// weightCap of 0 means the service does not override the process-wide K.
func SetServiceSelectionSettings(serviceID protocol.ServiceID, weightCap int, maxOperatorShare float64) {
	serviceSelectionSettingsMu.Lock()
	defer serviceSelectionSettingsMu.Unlock()

	next := make(map[protocol.ServiceID]serviceSelectionSettings)
	if current := serviceSelectionSettingsBy.Load(); current != nil {
		for k, v := range *current {
			next[k] = v
		}
	}
	next[serviceID] = serviceSelectionSettings{backendWeightCap: weightCap, maxOperatorShare: maxOperatorShare}
	serviceSelectionSettingsBy.Store(&next)
}

// ResetServiceSelectionSettings clears every published per-service setting. Test-only helper;
// startup publishes settings once and never removes them.
func ResetServiceSelectionSettings() {
	serviceSelectionSettingsMu.Lock()
	defer serviceSelectionSettingsMu.Unlock()
	serviceSelectionSettingsBy.Store(nil)
}

// selectionSettingsFor resolves K and the concentration cap for a service, falling back to the
// process-wide values for a service that published neither.
func selectionSettingsFor(serviceID protocol.ServiceID) (weightCap int, maxOperatorShare float64) {
	weightCap = int(backendWeightCap.Load())
	maxOperatorShare = defaultMaxOperatorShare.Load()

	if published := serviceSelectionSettingsBy.Load(); published != nil {
		if s, found := (*published)[serviceID]; found {
			if s.backendWeightCap > 0 {
				weightCap = s.backendWeightCap
			}
			maxOperatorShare = s.maxOperatorShare
		}
	}
	return weightCap, maxOperatorShare
}

// effectiveWeightCap resolves the K to weight a pool with, honoring the backend-URL-dedup
// off-switch: with dedup disabled the weighting is registration-proportional (weightCap 0),
// which is byte-for-byte the behavior that predates backend-URL dedup.
func effectiveWeightCap(serviceID protocol.ServiceID) int {
	if !dedupeOperatorShareByBackendURL.Load() {
		return 0
	}
	k, _ := selectionSettingsFor(serviceID)
	return k
}

// backendWeight is the selection weight of ONE backend URL: min(registrations, weightCap).
// A weightCap <= 0 means uncapped, i.e. registration-proportional.
func backendWeight(registrations, weightCap int) int {
	if registrations <= 0 {
		return 0
	}
	if weightCap <= 0 || registrations < weightCap {
		return registrations
	}
	return weightCap
}

// SelectWithConcentrationCap picks one endpoint from validEndpoints, biased so that
// no single operator (eTLD+1) exceeds maxOperatorShare of the selection probability.
//
// Below the cap, behavior is identical to a pick on the weighting basis (share tracks the
// operator's weight units, min(registrations, K) summed over its backends). Only the
// probability mass that a dominant operator holds *above* the cap is redistributed —
// proportionally, via water-filling — to the under-cap operators. This bounds the blast radius
// of any single operator failing without over-correcting toward thin operators the way a
// two-stage uniform pick would.
//
// Selection is three-step: pick an operator by its (capped) weight, a backend within it by
// weight, then a supplier registration behind that backend uniformly.
//
// The cap is DISABLED (byte-for-byte a flat random pick over registrations) when:
//   - maxOperatorShare <= 0 or >= 1 (the config off-switch), or
//   - there are 0 or 1 endpoints.
//
// A cap that no assignment can satisfy degrades to infeasibleCapFallbackShare rather than to a
// forced uniform-over-operators split; see that constant.
//
// Endpoints whose eTLD+1 cannot be resolved are each treated as their own singleton
// operator (never merged into one bucket — that would fabricate concentration).
//
// NOTE ON REACH: this selector is called from one narrow path and shapes a rounding error's
// worth of production traffic (measured at 0.008 selections/s on a busy service, against ~2000
// selections/s across the paths that actually serve requests). The cap that matters is the one
// PickBackendUniformForService applies on the serving pick; this function shares its
// implementation so the two cannot drift.
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

	// Group the pool into operators and, within each, backend URLs, then weight each backend
	// by min(registrations, K). `units` is the currency the cap is denominated in.
	pools, totalUnits, maxUnits := groupPool(validEndpoints, effectiveWeightCap(serviceID))

	// Counts for the selection metric, expressed in the same currency the weighting used,
	// so the dashboards show the basis the decision was actually made on.
	counts := make(map[string]int, len(pools))
	for _, p := range pools {
		counts[p.key] = p.units
	}

	selected, opKey, reshaped := pickWeightedFromPools(pools, totalUnits, maxUnits, maxOperatorShare)
	if reshaped {
		// The distribution is actually being altered here, so record it.
		metrics.RecordConcentrationCapReshaped(string(serviceID))
	}
	// Instrumented on every path, including the no-op one: "the cap was a no-op" is the
	// majority of selections and the case a reshape-only metric cannot see, so it is exactly
	// where a skew would hide.
	metrics.RecordSelectionPool(string(serviceID), metrics.SelectionPathConcentrationCap, counts, totalUnits, opKey)
	return selected
}

// pickWeightedFromPools is the one weighted pick every capped selector shares:
//
//  1. BASIS — per-backend weights min(registrations, K), summed per operator (done by
//     groupPool, arriving here as operatorPool.units).
//  2. CAP — water-fill so no operator exceeds maxOperatorShare, scaling that operator's
//     backends down proportionally (the operator's internal split is untouched).
//  3. PICK — weighted pick of an operator, weighted pick of a backend inside it, uniform pick
//     of a supplier registration behind that backend.
//
// Step 3 ALWAYS resolves to a concrete supplier registration: a relay is signed against a
// supplier's session and each supplier carries its own per-session service allowance.
//
// reshaped reports whether step 2 actually altered the distribution, so the caller can
// attribute the reshape metric without recomputing the comparison.
func pickWeightedFromPools(
	pools []*operatorPool,
	totalUnits, maxUnits int,
	maxOperatorShare float64,
) (selected protocol.EndpointAddr, operator string, reshaped bool) {
	m := len(pools)
	if m == 0 || totalUnits <= 0 {
		return protocol.EndpointAddr(""), "", false
	}

	// m == 1 means the pool reached the selector already collapsed to one operator: there is
	// nothing to redistribute to, so the cap cannot apply however it is configured.
	capEnabled := m > 1 && maxOperatorShare > 0 && maxOperatorShare < 1

	// Resolve an infeasible cap before testing whether it binds — see
	// infeasibleCapFallbackShare. With the uniform fallback selected instead, the pool is
	// reshaped to uniform-over-operators regardless of how far over the cap it sits.
	if capEnabled && maxOperatorShare*float64(m) <= 1.0 {
		if capInfeasibleForcesUniform.Load() {
			weights := make([]float64, m)
			for i := range weights {
				weights[i] = 1.0 / float64(m)
			}
			chosen := pools[weightedPick(weights)]
			return chosen.pickEndpoint(), chosen.key, true
		}
		maxOperatorShare = infeasibleCapFallbackShare
	}

	// The cap is a no-op when no operator exceeds it: the capped weighted pick reduces to a
	// flat pick over weight units, so take that directly.
	if !capEnabled || float64(maxUnits) <= maxOperatorShare*float64(totalUnits) {
		sel, key := pickUniformOverUnits(pools, totalUnits)
		return sel, key, false
	}

	// Per-unit-uniform (units_i/totalUnits) start, then water-fill the over-cap mass down.
	weights := make([]float64, m)
	entitlements := make([]float64, m)
	totalRegistrations := 0
	for _, p := range pools {
		totalRegistrations += p.entries
	}
	for i, p := range pools {
		weights[i] = float64(p.units) / float64(totalUnits)
		if totalRegistrations > 0 {
			entitlements[i] = float64(p.entries) / float64(totalRegistrations)
		}
	}
	waterFillToCap(weights, maxOperatorShare, entitlements)

	chosen := pools[weightedPick(weights)]
	return chosen.pickEndpoint(), chosen.key, true
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
// The TRADE-OFF this originally made — a supplier registration carries its own per-session
// service allowance, so N registrations behind one URL DO represent more serviceable capacity
// than one, and counting the backend once ignored that — is now handled by the per-backend
// weight cap K rather than by an all-or-nothing dedup: weight(backend) = min(registrations, K).
// See DefaultBackendRegistrationWeightCap. The pick still resolves to a specific supplier (see
// backendGroup.pick), so the per-supplier allowance is spread across the registrations behind
// the chosen URL rather than pinned to one of them.
//
// Default ON. Set PATH_OPERATOR_SHARE_BY_BACKEND_URL=false to restore registration-counted
// shares without a redeploy of the selection logic; that is the broadest revert available and
// it also neutralizes K and the per-operator cap on the serving pick.
var dedupeOperatorShareByBackendURL atomic.Bool

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
	// weight is this backend's share of the selection, min(len(endpoints), K). Set by
	// groupPool once the group is complete; see backendWeight.
	weight int
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
	// units is this operator's weight: the sum of its backends' weights. Set by groupPool.
	units int
}

// pickEndpoint chooses within the operator in the weighting currency: a backend in proportion
// to its weight (min(registrations, K)), then a supplier registration uniformly behind it.
//
// With the backend-URL-dedup off-switch engaged every backend's weight IS its registration
// count, so this reduces to a flat pick over registrations - byte-for-byte the pre-dedup
// behavior.
func (p *operatorPool) pickEndpoint() protocol.EndpointAddr {
	if len(p.backends) == 0 {
		return protocol.EndpointAddr("")
	}
	if p.units <= 0 {
		return p.backends[len(p.backends)-1].pick()
	}
	idx := rand.Intn(p.units)
	for _, b := range p.backends {
		if idx < b.weight {
			return b.pick()
		}
		idx -= b.weight
	}
	// Unreachable while units is the sum of backend weights; fall back rather than panic.
	return p.backends[len(p.backends)-1].pick()
}

// groupPool buckets endpoints by operator and, within each operator, by backend URL, then
// weights each backend by min(registrations, weightCap). Returns the operator pools in
// first-seen order (deterministic given the input), the total weight units across all
// operators, and the largest single operator's units.
func groupPool(validEndpoints protocol.EndpointAddrList, weightCap int) (pools []*operatorPool, totalUnits, maxUnits int) {
	return groupPoolKeyed(validEndpoints, weightCap, nil)
}

// groupPoolKeyed is groupPool with an optional precomputed endpoint -> operator (eTLD+1) map.
// Resolving an operator key parses a URL and walks the public-suffix list, so a caller that
// already holds that map (the diversity selector does) passes it rather than paying for the
// same parse twice on the hot path. An empty value in the map means "unresolvable", which
// keeps that endpoint its own singleton operator exactly as operatorKey does.
func groupPoolKeyed(
	validEndpoints protocol.EndpointAddrList,
	weightCap int,
	operatorKeys map[protocol.EndpointAddr]string,
) (pools []*operatorPool, totalUnits, maxUnits int) {
	byOperator := make(map[string]*operatorPool)
	byBackend := make(map[string]map[string]*backendGroup)

	for _, ep := range validEndpoints {
		opKey := resolveOperatorKey(ep, operatorKeys)
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

	// Weights can only be assigned once every group is complete: a backend's weight depends on
	// how many registrations ended up behind it.
	for _, p := range pools {
		for _, b := range p.backends {
			b.weight = backendWeight(len(b.endpoints), weightCap)
			p.units += b.weight
		}
		totalUnits += p.units
		if p.units > maxUnits {
			maxUnits = p.units
		}
	}
	return pools, totalUnits, maxUnits
}

// resolveOperatorKey returns an endpoint's operator bucket, preferring a precomputed map.
func resolveOperatorKey(ep protocol.EndpointAddr, operatorKeys map[protocol.EndpointAddr]string) string {
	if operatorKeys != nil {
		if k, present := operatorKeys[ep]; present {
			if k == "" {
				return string(ep)
			}
			return k
		}
	}
	return operatorKey(ep)
}

// pickUniformOverUnits picks a weight unit uniformly across the whole pool (not per operator),
// then resolves it to a supplier. This is the uncapped weighted basis: an operator's share is
// units_i/totalUnits and a backend's share is its own weight/totalUnits.
func pickUniformOverUnits(pools []*operatorPool, totalUnits int) (protocol.EndpointAddr, string) {
	if totalUnits <= 0 || len(pools) == 0 {
		return protocol.EndpointAddr(""), ""
	}
	idx := rand.Intn(totalUnits)
	for _, p := range pools {
		if idx < p.units {
			return p.pickEndpoint(), p.key
		}
		idx -= p.units
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
	// machine. groupPool's per-operator pick spreads over the operator's backends weighted by
	// min(registrations, K), then over the supplier registrations behind the chosen backend
	// (preserving supplier identity for signing and spreading per-supplier service allowance).
	pools, _, _ := groupPool(validEndpoints, effectiveWeightCap(serviceID))
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

// DefaultDisplacementCeilingMultiple bounds how much of a capped provider's excess any other
// provider may be handed, as a multiple of the share its own registrations entitle it to.
//
// WHY IT EXISTS. The cap displaces a dominant provider's excess onto everyone under the cap,
// proportionally to their weight and with no regard for whether they hold the registrations to
// serve it. Each registration carries its own per-session allowance, so a provider handed many
// times its ticket share simply cannot serve the traffic. Measured on a 49-versus-1 pool the
// smaller provider was allocated 17.5x its allowance; across the real pools the worst case was
// 6.3x. Capping harder makes this worse, not better.
//
// Over-servicing is not catastrophic — a supplier past its allowance answers 429 and the
// request moves to another endpoint — so the ceiling is set for efficiency rather than safety:
// it stops the selector routing work that is predictably going to bounce.
//
// 3x keeps the cap effective (the dominant provider still lands near half of a service on
// average) while cutting the worst over-allocation from 6.3x to 3.1x. Tighter values buy little
// extra safety and give the dominant provider back several points of share.
const DefaultDisplacementCeilingMultiple = 3.0

// waterFillToCap clamps any weight above cap and redistributes the excess to the under-cap
// weights, proportionally to their current weight, until no weight exceeds the cap.
//
// entitlements[i] is the share operator i's REGISTRATIONS entitle it to — what its per-session
// allowance can actually serve. No operator is pushed above DefaultDisplacementCeilingMultiple
// times that. Excess nobody can absorb stays with the capped operator: at that point the pool
// has no one able to serve the displaced traffic, and moving it anyway only produces 429s.
//
// A nil entitlements slice disables the ceiling and restores pure proportional water-filling.
// Operates in place.
func waterFillToCap(weights []float64, maxShare float64, entitlements []float64) {
	ceiling := func(i int) float64 {
		if entitlements == nil {
			return 1.0
		}
		c := entitlements[i] * DefaultDisplacementCeilingMultiple
		// A provider is always allowed to keep what it already holds; the ceiling bounds what
		// it is GIVEN, and must never claw back its own entitled share.
		if c < weights[i] {
			return weights[i]
		}
		return c
	}

	// At most len(weights) passes: each pass pins at least one operator to the cap or its
	// ceiling, so the loop cannot cycle.
	for pass := 0; pass < len(weights); pass++ {
		var excess, roomSum float64
		anyOver := false
		for i, w := range weights {
			if w > maxShare+concentrationCapEpsilon {
				excess += w - maxShare
				anyOver = true
				continue
			}
			// Room is bounded by BOTH the cap and what this provider can serve.
			limit := maxShare
			if c := ceiling(i); c < limit {
				limit = c
			}
			if r := limit - w; r > concentrationCapEpsilon {
				roomSum += r
			}
		}
		if !anyOver {
			return
		}

		// Nobody has room to absorb the excess — every under-cap provider is already at its
		// ceiling. Clamping the dominant provider anyway would hand traffic to suppliers that
		// answer 429, so it keeps the remainder.
		if roomSum <= 0 {
			return
		}

		absorbed := excess
		if roomSum < absorbed {
			absorbed = roomSum
		}
		for i, w := range weights {
			if w > maxShare+concentrationCapEpsilon {
				// Give back whatever the pool could not absorb, in proportion to the overage.
				weights[i] = maxShare + (w-maxShare)*(excess-absorbed)/excess
				continue
			}
			limit := maxShare
			if c := ceiling(i); c < limit {
				limit = c
			}
			if r := limit - w; r > concentrationCapEpsilon {
				weights[i] = w + absorbed*(r/roomSum)
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

// backendPick is the outcome of a weighted backend pick together with the basis it was made
// on, so the caller can record the selection metric in the currency the decision actually used
// instead of in raw registration counts.
type backendPick struct {
	selected protocol.EndpointAddr
	// operator is the eTLD+1 bucket the selected endpoint belongs to.
	operator string
	// operatorUnits is each operator's weight; totalUnits is their sum.
	operatorUnits map[string]int
	totalUnits    int
	// capReshaped reports that the per-operator cap altered the distribution for this pick.
	capReshaped bool
}

// PickBackendUniform picks one endpoint weighted by backend URL rather than by supplier
// registration: a backend's weight is min(registrations behind it, K).
//
// This is the weighting rule applied to a pool that has ALREADY been quality-filtered by the
// caller — the reputation-ranked retry/hedge band, where every member is within the score
// epsilon and therefore equally acceptable. Uniform-over-registrations there hands a backend
// with 7 supplier records 7x the traffic of an equally-scored sibling with 1, even though
// both are one machine: the band looks diverse while the traffic is not.
//
// It applies the weighting BASIS only. The per-operator concentration cap needs a service to
// resolve its configured value from, so it is applied by PickBackendUniformForService — which
// is the same implementation, and is what the serving path calls.
//
// Returns a specific supplier registration, never a bare URL — a relay is signed against a
// supplier's session and each supplier carries its own per-session service allowance.
//
// With backend-URL dedup disabled this is a flat uniform pick over the pool, i.e. exactly the
// prior behavior.
func PickBackendUniform(endpoints protocol.EndpointAddrList) protocol.EndpointAddr {
	return pickBackend("", endpoints, nil, false).selected
}

// PickBackendUniformForService is PickBackendUniform plus the service's per-operator
// concentration cap: weights by backend (min(registrations, K)), sums them per operator,
// water-fills any operator over the cap, then picks operator -> backend -> supplier.
//
// This is the pick that decides where a request is actually served, so it is where the cap has
// to be applied. The cap it had been living in (SelectWithConcentrationCap) is reached from one
// narrow call path and shapes a rounding error's worth of production traffic; this one does not.
func PickBackendUniformForService(
	serviceID protocol.ServiceID,
	endpoints protocol.EndpointAddrList,
) protocol.EndpointAddr {
	return pickBackend(serviceID, endpoints, nil, true).selected
}

// pickBackend is the shared implementation. operatorKeys is an optional precomputed
// endpoint -> eTLD+1 map (see groupPoolKeyed); applyOperatorCap requests the per-operator cap
// on top of the weighting basis.
func pickBackend(
	serviceID protocol.ServiceID,
	endpoints protocol.EndpointAddrList,
	operatorKeys map[protocol.EndpointAddr]string,
	applyOperatorCap bool,
) backendPick {
	if len(endpoints) == 0 {
		return backendPick{}
	}

	// The dedup off-switch restores the pre-dedup behavior in full: registration-proportional
	// weighting AND no cap on this path, which together are a flat random pick over the pool.
	dedupOn := dedupeOperatorShareByBackendURL.Load()

	var maxOperatorShare float64
	if dedupOn && applyOperatorCap && applyOperatorCapOnBackendPick.Load() {
		_, maxOperatorShare = selectionSettingsFor(serviceID)
	}

	pools, totalUnits, maxUnits := groupPoolKeyed(endpoints, effectiveWeightCap(serviceID), operatorKeys)
	selected, operator, reshaped := pickWeightedFromPools(pools, totalUnits, maxUnits, maxOperatorShare)

	counts := make(map[string]int, len(pools))
	for _, p := range pools {
		counts[p.key] = p.units
	}
	return backendPick{
		selected:      selected,
		operator:      operator,
		operatorUnits: counts,
		totalUnits:    totalUnits,
		capReshaped:   reshaped,
	}
}

// BandCapOutcome reports what the per-operator concentration cap did to one retry/hedge band
// pick. It exists so the caller can attribute the decision to its own path and, above all, so
// the degraded case is countable instead of silent.
type BandCapOutcome int

const (
	// BandCapNoCandidates: the band was empty. Nothing was selected; the caller must fall back.
	BandCapNoCandidates BandCapOutcome = iota
	// BandCapDisabled: maxOperatorShare itself is off (<= 0 or >= 1), so the pick is the
	// uncapped backend-uniform one — byte-for-byte the pre-cap behavior. Reserved strictly for
	// the cap VALUE being off, so the metric can prove the configured state from the outside;
	// a band the cap merely cannot act on reports BandCapNoRoom instead.
	BandCapDisabled
	// BandCapNoRoom: the cap is on, but there is nowhere to redistribute over-cap mass to —
	// every candidate in the band belongs to ONE operator, or the band holds one candidate.
	// Degraded to the uncapped pick. This is the expected outcome for most retries, not an
	// error: a retry excludes the operators it has already tried, so its remaining band is
	// frequently single-operator.
	BandCapNoRoom
	// BandCapNoOp: the cap is on and the band spans several operators, but none exceeds the
	// cap — the capped pick reduces exactly to the uncapped one.
	BandCapNoOp
	// BandCapReshaped: the cap is on and actually altered the pick's distribution.
	BandCapReshaped
)

// String returns the metric label value for an outcome.
func (o BandCapOutcome) String() string {
	switch o {
	case BandCapNoCandidates:
		return "no_candidates"
	case BandCapDisabled:
		return "disabled"
	case BandCapNoRoom:
		return "degraded_no_room"
	case BandCapNoOp:
		return "no_op"
	case BandCapReshaped:
		return "reshaped"
	default:
		return "unknown"
	}
}

// SelectBandWithConcentrationCap picks one endpoint from an ALREADY quality-filtered band —
// the reputation-ranked retry/hedge band, whose members are all within the score epsilon and
// therefore equally acceptable — applying the same per-operator (eTLD+1) concentration cap and
// water-filling that governs primary selection.
//
// Why the band needs it at all: the band pick was uniform over distinct backend URLs, which
// bounds a single MACHINE's share but not a single OPERATOR's. An operator fronting most of the
// band's backends therefore took most of the retries and hedges, so concentration leaked
// through the two paths whose entire purpose is to reach different infrastructure than the
// attempt that just failed.
//
// The cap is applied as a REWEIGHTING, never as a filter. No candidate is ever removed from the
// band, so a retry can never be starved of somewhere to go: the worst case is that the weights
// are the same ones it would have used anyway. When the band holds a single operator the cap
// has no room to redistribute and the pick degrades to the uncapped one, reported as
// BandCapNoRoom so the degradation is visible rather than inferred.
//
// Selection stays two-stage and always resolves to a concrete supplier registration
// (operator -> backend URL -> supplier), because a relay is signed against a supplier's session
// and each supplier carries its own per-session service allowance. With backend-URL dedup
// disabled (PATH_OPERATOR_SHARE_BY_BACKEND_URL=false) the shares are denominated in supplier
// registrations instead, exactly as on the primary path.
//
// Emits no metrics: only the caller knows which path (retry, hedge, batch item) it is on, and
// that attribution is the point of the outcome return.
func SelectBandWithConcentrationCap(
	band protocol.EndpointAddrList,
	maxOperatorShare float64,
) (protocol.EndpointAddr, BandCapOutcome) {
	n := len(band)
	if n == 0 {
		return protocol.EndpointAddr(""), BandCapNoCandidates
	}

	// Cap value disabled (the config off-switch): take the uncapped pick without paying for
	// the grouping.
	if maxOperatorShare <= 0 || maxOperatorShare >= 1 {
		return PickBackendUniform(band), BandCapDisabled
	}

	// One candidate: nothing to spread. Reported as no-room rather than disabled — the cap is
	// configured on and the dashboards must not read that as an off gate.
	if n == 1 {
		return band[0], BandCapNoRoom
	}

	// Process-wide K, not a per-service one: this selector is handed a band and a cap value by
	// its caller and never a serviceID, so there is nothing here to resolve a per-service
	// override from. A service that overrides backend_registration_weight_cap therefore gets
	// that K on its primary pick and the fleet-wide K on its band picks. Acceptable while the
	// band cap is opt-in per service; thread the serviceID through if that changes.
	pools, totalUnits, maxUnits := groupPool(band, effectiveWeightCap(""))
	if len(pools) <= 1 {
		// Single operator: capping it would mean excluding every candidate. Degrade to the
		// uncapped pick — a retry with no candidate is strictly worse than a retry that lands
		// on the only operator still available.
		return PickBackendUniform(band), BandCapNoRoom
	}

	selected, _, reshaped := pickWeightedFromPools(pools, totalUnits, maxUnits, maxOperatorShare)
	if selected == "" {
		// Defensive: pickWeightedFromPools always returns a band member for a non-empty grouping.
		return PickBackendUniform(band), BandCapNoRoom
	}
	if reshaped {
		return selected, BandCapReshaped
	}
	return selected, BandCapNoOp
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
