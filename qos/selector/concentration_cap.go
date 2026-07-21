package selector

import (
	"math/rand"

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
func SelectWithConcentrationCap(
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

	// Group endpoints by operator (eTLD+1). The eTLD+1 is extracted inline (rather than via
	// shannonmetrics.GetEndpointTLDs) to avoid materializing a throwaway map on this hot
	// path — the map would be built and then read exactly once, in this same loop.
	// Unresolvable eTLD+1 → singleton operator keyed by the endpoint address itself, so it
	// can never be merged with another.
	operatorEndpoints := make(map[string]protocol.EndpointAddrList)
	operatorOrder := make([]string, 0, n)
	for _, ep := range validEndpoints {
		key := shannonmetrics.ExtractTLDFromEndpointAddr(string(ep))
		if key == "" {
			key = string(ep)
		}
		if _, seen := operatorEndpoints[key]; !seen {
			operatorOrder = append(operatorOrder, key)
		}
		operatorEndpoints[key] = append(operatorEndpoints[key], ep)
	}

	m := len(operatorOrder)
	// Single operator: the cap has nothing to reshape. A flat random pick is a uniform
	// pick within that one operator — identical, and cheaper.
	if m == 1 {
		return validEndpoints[rand.Intn(n)]
	}

	// Per-operator weights. Start from per-endpoint-uniform (n_i/N): a weighted operator
	// pick followed by a uniform pick within the operator then reproduces a flat random
	// pick exactly — until the cap reshapes an over-cap operator's mass.
	weights := make([]float64, m)
	if maxOperatorShare*float64(m) <= 1.0 {
		// Infeasible (or exactly at the 1/M floor): no assignment can hold every operator
		// under the cap, so the best achievable is uniform-over-operators (max share 1/M).
		for i := range weights {
			weights[i] = 1.0 / float64(m)
		}
	} else {
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

// waterFillToCap clamps any weight above cap and redistributes the excess to the
// under-cap weights, proportionally to their current weight, until no weight exceeds
// the cap. The caller only invokes this when the cap is feasible (cap*len(weights) > 1),
// so total mass is preserved (≈ 1); the underSum guard keeps it safe even if that ever
// fails to hold. Operates in place.
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
