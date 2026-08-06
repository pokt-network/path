package selector

import (
	"fmt"
	"math/rand"
	"slices"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics"
	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/protocol"
)

// RandomSelectMultiple performs Fisher-Yates shuffle for random selection without replacement.
// This is a shared utility to avoid code duplication across QoS services.
// Ref: https://en.wikipedia.org/wiki/Fisher%E2%80%93Yates_shuffle
//
// Parameters:
// - endpoints: The list of endpoints to select from
// - numEndpoints: The number of endpoints to select
//
// Returns a new slice containing randomly selected endpoints.
// If numEndpoints is greater than len(endpoints), returns all endpoints.
func RandomSelectMultiple(
	endpoints protocol.EndpointAddrList,
	numEndpoints uint,
) protocol.EndpointAddrList {
	if int(numEndpoints) >= len(endpoints) {
		// Return a copy of all endpoints
		return slices.Clone(endpoints)
	}

	// Create a copy to avoid modifying the original slice
	endpointsCopy := slices.Clone(endpoints)

	// Fisher-Yates shuffle for random selection without replacement
	selectedEndpoints := make(protocol.EndpointAddrList, 0, numEndpoints)
	for i := 0; i < int(numEndpoints); i++ {
		j := rand.Intn(len(endpointsCopy)-i) + i
		endpointsCopy[i], endpointsCopy[j] = endpointsCopy[j], endpointsCopy[i]
		selectedEndpoints = append(selectedEndpoints, endpointsCopy[i])
	}

	return selectedEndpoints
}

// SelectEndpointsWithDiversity selects endpoints with TLD diversity preference.
//
// This helper is useful and necessary when used in conjunction with parallel requests.
// When multiple parallel requests are fired off, it is wasteful to send them all to the same TLD.
// Sending the request to different providers increases the likelihood of a successful request
// being returned to the user.
func SelectEndpointsWithDiversity(
	logger polylog.Logger,
	serviceID protocol.ServiceID,
	availableEndpoints protocol.EndpointAddrList,
	numEndpoints uint,
) protocol.EndpointAddrList {
	// Get endpoint TLDs to extract TLD information
	endpointTLDs := shannonmetrics.GetEndpointTLDs(availableEndpoints)

	// Count unique TLDs for logging
	uniqueTLDs := make(map[string]struct{})
	for _, tld := range endpointTLDs {
		if tld != "" {
			uniqueTLDs[tld] = struct{}{}
		}
	}

	logger.Debug().Msgf("[Parallel Requests] Endpoint selection: %d available endpoints across %d unique TLDs, selecting up to %d endpoints",
		len(availableEndpoints), len(uniqueTLDs), numEndpoints)

	var selectedEndpoints protocol.EndpointAddrList
	usedTLDs := make(map[string]struct{})
	remainingEndpoints := slices.Clone(availableEndpoints)

	// firstPick carries the weighting basis the SERVING pick was made on, for the metric below.
	var firstPick backendPick

	// First pass: Try to select endpoints with different TLDs
	for i := 0; i < int(numEndpoints) && len(remainingEndpoints) > 0; i++ {
		var selectedEndpoint protocol.EndpointAddr
		var err error

		// Try to find an endpoint with a different TLD
		if i > 0 && len(usedTLDs) > 0 {
			selectedEndpoint, err = selectEndpointWithDifferentTLD(serviceID, remainingEndpoints, endpointTLDs, usedTLDs)
			if err != nil {
				// No unused TLD left: fall back to the whole remaining pool, still on the
				// weighted backend basis rather than registration-uniform. No operator cap
				// here: these are the EXTRA parallel endpoints, and the pass above has
				// already spread them across operators by construction.
				selectedEndpoint = pickBackend(serviceID, remainingEndpoints, endpointTLDs, false).selected
				err = nil
			}
		} else {
			// First endpoint. NOTE: no TLD logic applies to this pick — the diverse pass
			// starts at i > 0 — so with max_parallel_endpoints=1 this IS the serving
			// endpoint, chosen from the whole pool. It is therefore the pick that decides
			// where production traffic goes, and the one that carries both halves of the
			// weighting: backends weighted by min(registrations, K) so that staking more
			// suppliers earns more traffic without one machine buying unbounded share, and
			// the per-operator concentration cap on top of that.
			pick := pickBackend(serviceID, remainingEndpoints, endpointTLDs, true)
			if i == 0 {
				firstPick = pick
			}
			selectedEndpoint = pick.selected
		}

		if err != nil {
			logger.Warn().Err(err).Msgf("Failed to select endpoint %d, stopping selection", i+1)
			break
		}

		selectedEndpoints = append(selectedEndpoints, selectedEndpoint)

		// Track the TLD of the selected endpoint
		if tld, exists := endpointTLDs[selectedEndpoint]; exists {
			usedTLDs[tld] = struct{}{}
			logger.Debug().Msgf("[Parallel Requests] Selected endpoint with TLD: %s (endpoint: %s)", tld, selectedEndpoint)
		}

		// Remove the selected endpoint from the remaining pool
		newRemainingEndpoints := make(protocol.EndpointAddrList, 0, len(remainingEndpoints)-1)
		for _, endpoint := range remainingEndpoints {
			if endpoint != selectedEndpoint {
				newRemainingEndpoints = append(newRemainingEndpoints, endpoint)
			}
		}
		remainingEndpoints = newRemainingEndpoints
	}

	// Record the candidate pool this selector saw and which operator won the PRIMARY pick.
	//
	// The path label distinguishes the two very different ways this function is called:
	//
	//   - numEndpoints < len(availableEndpoints): a real narrowing. The first pick is the
	//     endpoint that serves the request (with max_parallel_endpoints=1 it is the only one).
	//   - numEndpoints >= len(availableEndpoints): every endpoint is returned, so the caller
	//     is using this as a QoS validation filter and DISCARDS the ordering — the batch-item
	//     and retry paths both do exactly that, then pick via selectTopRankedEndpoint. The
	//     "winner" here never wins anything.
	//
	// Recording both under one label made a discarded pick look like a real decision, which
	// inverted the apparent traffic attribution on batch-heavy services.
	selectionPath := metrics.SelectionPathDiversity
	if int(numEndpoints) >= len(availableEndpoints) {
		selectionPath = metrics.SelectionPathFilter
	}
	if len(selectedEndpoints) > 0 && firstPick.totalUnits > 0 {
		// The pool composition is reported in the WEIGHTING currency, not in raw registration
		// counts, so path_selection_pool_size shows the basis the decision was actually made
		// on — and so a canary A/B is readable straight off that series: the same pool reports
		// its registration count under the off-switch, its distinct-backend count at K=1, and
		// the min(registrations, K) sum at K=2.
		//
		// firstPick already grouped the pool (reusing endpointTLDs, so the eTLD+1 parse is not
		// paid twice), which is why these counts are taken from it rather than recomputed.
		metrics.RecordSelectionPool(
			string(serviceID),
			selectionPath,
			firstPick.operatorUnits,
			firstPick.totalUnits,
			firstPick.operator,
		)
		// Only a real narrowing reshapes anything that matters: on the filter path the caller
		// discards this ordering and picks again, so counting a reshape there would inflate
		// the counter with decisions that never happen.
		if firstPick.capReshaped && selectionPath == metrics.SelectionPathDiversity {
			metrics.RecordConcentrationCapReshaped(string(serviceID), selectionPath)
		}
	}

	// Count fallback selections (endpoints without TLD diversity)
	fallbackSelections := 0
	for _, endpoint := range selectedEndpoints {
		if tld, exists := endpointTLDs[endpoint]; exists && tld != "" {
			// Count how many endpoints use this TLD
			tldCount := 0
			for _, otherEndpoint := range selectedEndpoints {
				if otherTLD, exists := endpointTLDs[otherEndpoint]; exists && otherTLD == tld {
					tldCount++
				}
			}
			if tldCount > 1 {
				fallbackSelections++
			}
		}
	}

	logger.Debug().Msgf("[Parallel Requests] TLD diversity achieved: %d endpoints across %d different TLDs (diversity: %.1f%%, duplicate TLDs: %d)",
		len(selectedEndpoints), len(usedTLDs),
		float64(len(usedTLDs))/float64(len(selectedEndpoints))*100, fallbackSelections)
	return selectedEndpoints
}

// selectEndpointWithDifferentTLD attempts to select an endpoint with a TLD that hasn't been used yet
func selectEndpointWithDifferentTLD(
	serviceID protocol.ServiceID,
	availableEndpoints protocol.EndpointAddrList,
	endpointTLDs map[protocol.EndpointAddr]string,
	usedTLDs map[string]struct{},
) (protocol.EndpointAddr, error) {
	// Filter endpoints to only those with different TLDs
	var endpointsWithDifferentTLDs protocol.EndpointAddrList

	for _, endpoint := range availableEndpoints {
		if tld, exists := endpointTLDs[endpoint]; exists {
			if _, exists := usedTLDs[tld]; !exists {
				endpointsWithDifferentTLDs = append(endpointsWithDifferentTLDs, endpoint)
			}
		} else {
			// If we can't determine TLD, include it anyway
			endpointsWithDifferentTLDs = append(endpointsWithDifferentTLDs, endpoint)
		}
	}

	if len(endpointsWithDifferentTLDs) == 0 {
		return "", fmt.Errorf("no endpoints with different TLDs available")
	}

	// Select from the filtered list on the weighted backend basis. The TLD filter has already
	// guaranteed operator diversity — so no operator cap is applied here — and this makes the
	// choice WITHIN the surviving operators track machines-and-stake rather than raw supplier
	// registrations.
	return pickBackend(serviceID, endpointsWithDifferentTLDs, endpointTLDs, false).selected, nil
}
