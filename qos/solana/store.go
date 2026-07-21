package solana

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
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

	// maxOperatorShareBits holds a float64 (via math.Float64bits) for the per-operator
	// (eTLD+1) concentration cap. 0 (the zero value) disables the cap. Set dynamically
	// from configuration via QoS.SetMaxOperatorShare.
	maxOperatorShareBits atomic.Uint64
}

// getMaxOperatorShare returns the configured per-operator concentration cap (0 = disabled).
func (es *EndpointStore) getMaxOperatorShare() float64 {
	return math.Float64frombits(es.maxOperatorShareBits.Load())
}

// SetMaxOperatorShare dynamically sets the per-operator (eTLD+1) concentration cap used
// during endpoint selection. A value <= 0 or >= 1 disables the cap (flat random pick).
// Promoted to the Solana QoS via its embedded *EndpointStore.
func (es *EndpointStore) SetMaxOperatorShare(maxOperatorShare float64) {
	es.maxOperatorShareBits.Store(math.Float64bits(maxOperatorShare))
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

	// No valid endpoints -> least-stale fallback (was random). See selectLeastStaleEndpoints.
	if len(filteredEndpointsAddr) == 0 {
		picked := es.selectLeastStaleEndpoints(allAvailableEndpoints, 1)
		if len(picked) == 0 {
			return protocol.EndpointAddr(""), errors.New("no endpoints available for fallback selection")
		}
		logger.Warn().Msg("STANDARD_FALLBACK_LEAST_STALE: all endpoints failed validation; picked least-stale.")
		return picked[0], nil
	}

	// Select from the valid candidates, applying the per-operator (eTLD+1) concentration
	// cap when configured. Disabled (getMaxOperatorShare() <= 0 or >= 1) → byte-for-byte
	// the prior flat random pick.
	// TODO_FUTURE: consider ranking filtered endpoints, e.g. based on latency, rather than randomization.
	return selector.SelectWithConcentrationCap(filteredEndpointsAddr, es.getMaxOperatorShare()), nil
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

	// Least-stale fallback (was random). See selectLeastStaleEndpoints.
	if len(filteredEndpointsAddr) == 0 {
		picked := es.selectLeastStaleEndpoints(allAvailableEndpoints, numEndpoints)
		logger.Warn().
			Int("returned", len(picked)).
			Int("total", len(allAvailableEndpoints)).
			Msg("STANDARD_FALLBACK_LEAST_STALE: all endpoints failed validation; ranked by blocks-behind")
		return picked, nil
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

// selectLeastStaleEndpoints picks endpoints ranked by "blocks behind perceived height"
// when all endpoints have failed normal validation.
//
// This replaces the previous fully-random fallback, which concentrated traffic on
// stable-but-stale single-endpoint suppliers — random selection treated a far-behind
// endpoint exactly the same as one a few blocks behind. With ranking, traffic flows
// preferentially to the least-stale candidates while preserving randomness within ties.
//
// Mirrors qos/evm/endpoint_selection.go::selectLeastStaleEndpoints. Solana uses
// SolanaGetEpochInfoResponse.BlockHeight as the per-endpoint block height source.
func (es *EndpointStore) selectLeastStaleEndpoints(availableEndpoints protocol.EndpointAddrList, numEndpoints uint) protocol.EndpointAddrList {
	if numEndpoints == 0 {
		numEndpoints = 1
	}
	if len(availableEndpoints) == 0 {
		return nil
	}

	es.serviceState.serviceStateLock.RLock()
	perceivedBlock := es.serviceState.perceivedBlockHeight
	es.serviceState.serviceStateLock.RUnlock()

	// No chain context to rank against: degrade to prior random behavior.
	if perceivedBlock == 0 {
		return selector.RandomSelectMultiple(availableEndpoints, numEndpoints)
	}

	type scored struct {
		addr         protocol.EndpointAddr
		blocksBehind uint64
		hasData      bool
	}
	scoredEps := make([]scored, 0, len(availableEndpoints))

	es.endpointsMu.RLock()
	for _, addr := range availableEndpoints {
		s := scored{addr: addr, blocksBehind: math.MaxUint64}
		ep, found := es.endpoints[addr]
		// SolanaGetEpochInfoResponse can be nil for fresh endpoints — treat as no-data.
		if found && ep.SolanaGetEpochInfoResponse != nil && ep.BlockHeight > 0 {
			s.hasData = true
			h := ep.BlockHeight
			if h >= perceivedBlock {
				s.blocksBehind = 0
			} else {
				s.blocksBehind = perceivedBlock - h
			}
		}
		scoredEps = append(scoredEps, s)
	}
	es.endpointsMu.RUnlock()

	// Random tie-break: shuffle then stable-sort. With-data candidates always rank above
	// no-data ones — a known stale endpoint is safer than an unknown one.
	rand.Shuffle(len(scoredEps), func(i, j int) { scoredEps[i], scoredEps[j] = scoredEps[j], scoredEps[i] })
	sort.SliceStable(scoredEps, func(i, j int) bool {
		if scoredEps[i].hasData != scoredEps[j].hasData {
			return scoredEps[i].hasData
		}
		return scoredEps[i].blocksBehind < scoredEps[j].blocksBehind
	})

	n := int(numEndpoints)
	if n > len(scoredEps) {
		n = len(scoredEps)
	}
	out := make(protocol.EndpointAddrList, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, scoredEps[i].addr)
	}
	return out
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
