package solana

import (
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
)

// defaultEndpointTTL is how long an endpoint entry is kept without being seen
// in an active session. Sessions last ~1 hour, so 2x gives buffer.
const defaultEndpointTTL = 2 * time.Hour

// EndpointStore provides the endpoint selection capability required
// by the protocol package for handling a service request.
var _ protocol.EndpointSelector = &EndpointStore{}

// EndpointStore maintains QoS data on the set of available endpoints
// for the Solana blockchain service.
// It performs several tasks:
// - Endpoint selection based on the quality data available
// - Application of endpoints' observations to update the data on endpoints.
type EndpointStore struct {
	logger polylog.Logger

	serviceState *ServiceState

	endpointsMu sync.RWMutex
	endpoints   map[protocol.EndpointAddr]endpoint
}

// Select returns a random endpoint address from the list of valid endpoints.
// Valid endpoints are determined by filtering the available endpoints based on their
// validity criteria.
func (es *EndpointStore) Select(allAvailableEndpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	logger := es.logger.With(
		"qos", "Solana",
		"method", "Select",
		"num_endpoints", len(allAvailableEndpoints),
	)

	logger.Debug().Msg("filtering available endpoints.")

	filteredEndpointsAddr, err := es.filterValidEndpoints(allAvailableEndpoints)
	if err != nil {
		logger.Error().Err(err).Msg("error filtering endpoints: service request will fail.")
		return protocol.EndpointAddr(""), err
	}

	// No valid endpoints -> select a random endpoint
	if len(filteredEndpointsAddr) == 0 {
		logger.Warn().Msg("SELECTING A RANDOM ENDPOINT because all endpoints failed validation.")
		randomAvailableEndpointAddr := allAvailableEndpoints[rand.Intn(len(allAvailableEndpoints))]
		return randomAvailableEndpointAddr, nil
	}

	// TODO_FUTURE: consider ranking filtered endpoints, e.g. based on latency, rather than randomization.
	return filteredEndpointsAddr[rand.Intn(len(filteredEndpointsAddr))], nil
}

// SelectMultiple returns multiple endpoint addresses from the list of valid endpoints.
// Valid endpoints are determined by filtering the available endpoints based on their
// validity criteria. If numEndpoints is 0, it defaults to 1.
func (es *EndpointStore) SelectMultiple(
	allAvailableEndpoints protocol.EndpointAddrList,
	numEndpoints uint,
) (protocol.EndpointAddrList, error) {
	return es.SelectMultipleWithArchival(allAvailableEndpoints, numEndpoints, false)
}

// SelectMultipleWithArchival returns multiple endpoint addresses with optional archival filtering.
// Solana does not have an archival concept, so the requiresArchival parameter is ignored
// and this method delegates to the standard endpoint selection logic.
func (es *EndpointStore) SelectMultipleWithArchival(
	allAvailableEndpoints protocol.EndpointAddrList,
	numEndpoints uint,
	_ bool,
) (protocol.EndpointAddrList, error) {
	logger := es.logger.With(
		"qos", "Solana",
		"method", "SelectMultipleWithArchival",
		"num_endpoints_available", len(allAvailableEndpoints),
		"num_endpoints", numEndpoints,
	)
	logger.Debug().Msgf("filtering available endpoints to select up to %d.", numEndpoints)

	// Filter valid endpoints
	filteredEndpointsAddr, err := es.filterValidEndpoints(allAvailableEndpoints)
	if err != nil {
		logger.Error().Err(err).Msg("error filtering endpoints: service request will fail.")
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

// touchEndpoints updates the lastSeen timestamp for each endpoint address present in the store.
// Called after filtering to mark endpoints that appeared in an active session.
func (es *EndpointStore) touchEndpoints(addrs protocol.EndpointAddrList) {
	now := time.Now()
	es.endpointsMu.Lock()
	defer es.endpointsMu.Unlock()
	for _, addr := range addrs {
		if ep, found := es.endpoints[addr]; found {
			ep.lastSeen = now
			es.endpoints[addr] = ep
		}
	}
}

// sweepStaleEndpoints removes endpoints whose lastSeen is older than ttl (and non-zero).
// Returns the list of removed endpoint addresses for Redis cleanup.
func (es *EndpointStore) sweepStaleEndpoints(ttl time.Duration) []protocol.EndpointAddr {
	cutoff := time.Now().Add(-ttl)
	es.endpointsMu.Lock()
	defer es.endpointsMu.Unlock()

	var removed []protocol.EndpointAddr
	for addr, ep := range es.endpoints {
		if ep.lastSeen.IsZero() {
			continue
		}
		if ep.lastSeen.Before(cutoff) {
			delete(es.endpoints, addr)
			removed = append(removed, addr)
		}
	}
	return removed
}

// filterValidEndpoints returns the subset of available endpoints that are valid according to previously processed observations.
func (es *EndpointStore) filterValidEndpoints(allAvailableEndpoints protocol.EndpointAddrList) (protocol.EndpointAddrList, error) {
	es.endpointsMu.RLock()

	logger := es.logger.With(
		"method", "filterEndpoints",
		"qos_instance", "solana",
		"num_endpoints", len(allAvailableEndpoints),
	)

	if len(allAvailableEndpoints) == 0 {
		es.endpointsMu.RUnlock()
		return nil, errors.New("received empty list of endpoints to select from")
	}

	logger.Debug().Msg("About to filter available endpoints.")

	// TODO_FUTURE: rank the endpoints based on some service-specific metric.
	// For example: latency rather than making a single selection.
	var filteredEndpointsAddr protocol.EndpointAddrList
	for _, availableEndpointAddr := range allAvailableEndpoints {
		logger := logger.With("endpoint_addr", availableEndpointAddr)

		logger.Debug().Msg("Processing endpoint")

		endpoint, found := es.endpoints[availableEndpointAddr]
		if !found {
			// It is valid for an endpoint to not be in the store yet (e.g., first request,
			// no observations collected). Treat it as a fresh endpoint and allow it.
			// It will be added to the store once observations are collected.
			logger.Debug().Msg("endpoint not yet in store, treating as fresh endpoint")
			filteredEndpointsAddr = append(filteredEndpointsAddr, availableEndpointAddr)
			continue
		}

		if err := es.serviceState.ValidateEndpoint(endpoint); err != nil {
			logger.Warn().Err(err).Msgf("⚠️ SKIPPING endpoint because it failed validation: %s", availableEndpointAddr)
			continue
		}

		filteredEndpointsAddr = append(filteredEndpointsAddr, availableEndpointAddr)
		logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msgf("✅ endpoint passed validation: %s", availableEndpointAddr)
	}

	es.endpointsMu.RUnlock()

	// Touch endpoints to update lastSeen for stale endpoint cleanup.
	// Uses a separate WLock call to avoid changing the read-heavy filtering path.
	es.touchEndpoints(allAvailableEndpoints)

	return filteredEndpointsAddr, nil
}
