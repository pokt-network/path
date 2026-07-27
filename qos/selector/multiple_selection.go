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

	// First pass: Try to select endpoints with different TLDs
	for i := 0; i < int(numEndpoints) && len(remainingEndpoints) > 0; i++ {
		var selectedEndpoint protocol.EndpointAddr
		var err error

		// Try to find an endpoint with a different TLD
		if i > 0 && len(usedTLDs) > 0 {
			selectedEndpoint, err = selectEndpointWithDifferentTLD(remainingEndpoints, endpointTLDs, usedTLDs)
			if err != nil {
				// Fallback to random selection if no different TLD found
				selectedEndpoint = remainingEndpoints[rand.Intn(len(remainingEndpoints))]
				err = nil
			}
		} else {
			// First endpoint: use random selection
			selectedEndpoint = remainingEndpoints[rand.Intn(len(remainingEndpoints))]
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
	// This is the path every relay takes, so it is the one whose pool composition explains a
	// skewed traffic distribution. The primary (first) pick is the endpoint that serves the
	// request; with max_parallel_endpoints=1 it is the only one.
	if len(selectedEndpoints) > 0 {
		// Reuse endpointTLDs rather than calling operatorKey per endpoint: both resolve via
		// ExtractTLDFromEndpointAddr, and that parse is the expensive part. Apply the same
		// empty-string fallback operatorKey uses so an unresolvable address stays its own
		// singleton operator here too — merging them would fabricate concentration.
		counts := make(map[string]int, len(uniqueTLDs)+1)
		for _, ep := range availableEndpoints {
			k := endpointTLDs[ep]
			if k == "" {
				k = string(ep)
			}
			counts[k]++
		}
		selectedOp := endpointTLDs[selectedEndpoints[0]]
		if selectedOp == "" {
			selectedOp = string(selectedEndpoints[0])
		}
		metrics.RecordSelectionPool(
			string(serviceID),
			metrics.SelectionPathDiversity,
			counts,
			len(availableEndpoints),
			selectedOp,
		)
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

	// Select a random endpoint from the filtered list
	return endpointsWithDifferentTLDs[rand.Intn(len(endpointsWithDifferentTLDs))], nil
}
