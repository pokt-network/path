package cosmos

import (
	"errors"
	"math/rand"
	"time"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
)

var (
	errEmptyEndpointListObs               = errors.New("received empty list of endpoints to select from")
	errOutsideSyncAllowanceBlockNumberObs = errors.New("endpoint block number is outside sync allowance")
)

// TODO_UPNEXT(@adshmh): make the invalid response timeout duration configurable
// It is set to 30 minutes because that is the session time as of #321.
const invalidResponseTimeout = 30 * time.Minute

/* -------------------- QoS Valid Endpoint Selector -------------------- */
// This section contains methods for the `serviceState` struct
// but are kept in a separate file for clarity and readability.

// serviceState provides the endpoint selection capability required
// by the protocol package for handling a service request.
var _ protocol.EndpointSelector = &serviceState{}

// Select returns an endpoint address matching an entry from the list of available endpoints.
// available endpoints are filtered based on their validity first.
// A random endpoint is then returned from the filtered list of valid endpoints.
func (ss *serviceState) Select(availableEndpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	logger := ss.logger.With("method", "Select")

	logger.Debug().Msgf("filtering %d available endpoints.", len(availableEndpoints))

	filteredEndpointsAddr, err := ss.filterValidEndpoints(availableEndpoints)
	if err != nil {
		logger.Error().Err(err).Msg("error filtering endpoints")
		return protocol.EndpointAddr(""), err
	}

	if len(filteredEndpointsAddr) == 0 {
		logger.Warn().Msgf("SELECTING A RANDOM ENDPOINT because all endpoints failed validation from: %s", availableEndpoints.String())
		randomAvailableEndpointAddr := availableEndpoints[rand.Intn(len(availableEndpoints))]
		return randomAvailableEndpointAddr, nil
	}

	logger.Debug().Msgf("filtered %d endpoints from %d available endpoints", len(filteredEndpointsAddr), len(availableEndpoints))

	// TODO_FUTURE: consider ranking filtered endpoints, e.g. based on latency, rather than randomization.
	selectedEndpointAddr := filteredEndpointsAddr[rand.Intn(len(filteredEndpointsAddr))]
	return selectedEndpointAddr, nil
}

// SelectMultiple returns multiple endpoint addresses from the list of valid endpoints.
// Valid endpoints are determined by filtering the available endpoints based on their
// validity criteria. If numEndpoints is 0, it defaults to 1.
func (ss *serviceState) SelectMultiple(allAvailableEndpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	return ss.SelectMultipleWithArchival(allAvailableEndpoints, numEndpoints, false)
}

// SelectMultipleWithArchival returns multiple endpoint addresses with optional archival filtering.
// CosmosSDK does not have an archival concept, so the requiresArchival parameter is ignored
// and this method delegates to the standard endpoint selection logic.
func (ss *serviceState) SelectMultipleWithArchival(allAvailableEndpoints protocol.EndpointAddrList, numEndpoints uint, _ bool) (protocol.EndpointAddrList, error) {
	logger := ss.logger.With("method", "SelectMultipleWithArchival").With("num_endpoints", numEndpoints)
	logger.Debug().Msgf("filtering %d available endpoints to select up to %d.", len(allAvailableEndpoints), numEndpoints)

	filteredEndpointsAddr, err := ss.filterValidEndpoints(allAvailableEndpoints)
	if err != nil {
		logger.Error().Err(err).Msg("error filtering endpoints")
		return nil, err
	}

	// Select random endpoints as fallback
	if len(filteredEndpointsAddr) == 0 {
		logger.Warn().Msg("SELECTING RANDOM ENDPOINTS because all endpoints failed validation.")
		return selector.RandomSelectMultiple(allAvailableEndpoints, numEndpoints), nil
	}

	// Select up to numEndpoints endpoints from filtered list
	logger.Debug().Msgf("filtered %d endpoints from %d available endpoints", len(filteredEndpointsAddr), len(allAvailableEndpoints))
	return selector.SelectEndpointsWithDiversity(logger, filteredEndpointsAddr, numEndpoints), nil
}

// filterValidEndpoints returns the subset of available endpoints that are valid
// according to previously processed observations.
func (ss *serviceState) filterValidEndpoints(availableEndpoints protocol.EndpointAddrList) (protocol.EndpointAddrList, error) {
	ss.endpointStore.endpointsMu.RLock()
	defer ss.endpointStore.endpointsMu.RUnlock()

	logger := ss.logger.With("method", "filterValidEndpoints").With("qos_instance", "cosmossdk")

	if len(availableEndpoints) == 0 {
		return nil, errEmptyEndpointListObs
	}

	logger.Debug().Msgf("About to filter through %d available endpoints", len(availableEndpoints))

	// TODO_FUTURE: use service-specific metrics to add an endpoint ranking method
	// which can be used to assign a rank/score to a valid endpoint to guide endpoint selection.
	var filteredEndpointsAddr protocol.EndpointAddrList
	for _, availableEndpointAddr := range availableEndpoints {
		logger := logger.With("endpoint_addr", availableEndpointAddr)
		logger.Debug().Msg("processing endpoint")

		endpoint, found := ss.endpointStore.endpoints[availableEndpointAddr]
		if !found {
			// It is valid for an endpoint to not be in the store yet (e.g., first request,
			// no observations collected). Treat it as a fresh endpoint and allow it.
			// It will be added to the store once observations are collected.
			logger.Warn().
				Str("service_id", string(ss.serviceQoSConfig.GetServiceID())).
				Uint64("sync_allowance", ss.serviceQoSConfig.getSyncAllowance()).
				Msg("🔍 Sync allowance check SKIPPED (endpoint not yet in store - fresh endpoint)")
			filteredEndpointsAddr = append(filteredEndpointsAddr, availableEndpointAddr)
			continue
		}

		if err := ss.basicEndpointValidation(endpoint); err != nil {
			logger.Warn().Err(err).Msgf("⚠️ SKIPPING %s endpoint because it failed basic validation: %v", availableEndpointAddr, err)
			continue
		}

		filteredEndpointsAddr = append(filteredEndpointsAddr, availableEndpointAddr)
		logger.Debug().Msgf("endpoint %s passed validation", availableEndpointAddr)
	}

	return filteredEndpointsAddr, nil
}
