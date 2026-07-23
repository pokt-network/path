package selector

import (
	"math/rand"

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
		return validEndpoints[rand.Intn(n)]
	}

	// Phase 1 — cheap pass: resolve each endpoint's operator (eTLD+1) once, count endpoints
	// per operator into an int map (no per-operator slices), and remember each endpoint's
	// key so Phase 2 never has to resolve it again. This is enough to decide whether the cap
	// does anything: the common case — no single operator over the cap — exits here with a
	// flat random pick, never building the per-operator grouping or the weight vector.
	// Unresolvable eTLD+1 → singleton operator keyed by the endpoint address (operatorKey).
	keys := make([]string, n)
	counts := make(map[string]int)
	maxCount := 0
	for i, ep := range validEndpoints {
		k := operatorKey(ep)
		keys[i] = k
		counts[k]++
		if counts[k] > maxCount {
			maxCount = counts[k]
		}
	}
	m := len(counts)

	// The cap reshapes only when some operator exceeds it (n_i > cap·N) or the pool is too
	// concentrated for the cap to be satisfiable (cap·m ≤ 1 → uniform-over-operators). A
	// single operator, or a dominant share already at/under the cap, is a no-op: the capped
	// weighted pick would reduce exactly to a flat random pick, so take that directly.
	infeasible := maxOperatorShare*float64(m) <= 1.0
	if m == 1 || (!infeasible && float64(maxCount) <= maxOperatorShare*float64(n)) {
		return validEndpoints[rand.Intn(n)]
	}

	// Phase 2 — reshape. Build the per-operator endpoint grouping (reusing the keys resolved
	// in Phase 1) and the capped weights. The distribution is actually being altered here,
	// so record it.
	metrics.RecordConcentrationCapReshaped(string(serviceID))
	operatorEndpoints := make(map[string]protocol.EndpointAddrList, m)
	operatorOrder := make([]string, 0, m)
	for i, ep := range validEndpoints {
		k := keys[i]
		if _, seen := operatorEndpoints[k]; !seen {
			operatorOrder = append(operatorOrder, k)
		}
		operatorEndpoints[k] = append(operatorEndpoints[k], ep)
	}

	weights := make([]float64, m)
	if infeasible {
		// No assignment can hold every operator under the cap → best achievable is
		// uniform-over-operators (max share 1/m).
		for i := range weights {
			weights[i] = 1.0 / float64(m)
		}
	} else {
		// Per-endpoint-uniform (n_i/N) start, then water-fill the over-cap mass down.
		for i, key := range operatorOrder {
			weights[i] = float64(len(operatorEndpoints[key])) / float64(n)
		}
		waterFillToCap(weights, maxOperatorShare)
	}

	// Weighted pick of an operator, then uniform pick of an endpoint within it.
	opIdx := weightedPick(weights)
	eps := operatorEndpoints[operatorOrder[opIdx]]
	return eps[rand.Intn(len(eps))]
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

	operatorEndpoints := make(map[string]protocol.EndpointAddrList)
	operatorOrder := make([]string, 0)
	for _, ep := range validEndpoints {
		k := operatorKey(ep)
		if _, seen := operatorEndpoints[k]; !seen {
			operatorOrder = append(operatorOrder, k)
		}
		operatorEndpoints[k] = append(operatorEndpoints[k], ep)
	}

	// Uniform over operators, then uniform within the chosen operator.
	eps := operatorEndpoints[operatorOrder[rand.Intn(len(operatorOrder))]]
	return eps[rand.Intn(len(eps))]
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
