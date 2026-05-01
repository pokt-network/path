package cosmos

import (
	"errors"
	"math"
	"math/rand"
	"sort"
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
		// Least-stale fallback (was random). See selectLeastStaleEndpoints docstring for
		// why random fallback was a bug — it concentrated traffic on stable-but-stale
		// single-endpoint suppliers.
		picked := ss.selectLeastStaleEndpoints(availableEndpoints, 1)
		if len(picked) == 0 {
			return protocol.EndpointAddr(""), errors.New("no endpoints available for fallback selection")
		}
		logger.Warn().Msgf("STANDARD_FALLBACK_LEAST_STALE: all endpoints failed validation; picked least-stale from: %s", availableEndpoints.String())
		return picked[0], nil
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

	// Least-stale fallback (was random). See selectLeastStaleEndpoints docstring.
	if len(filteredEndpointsAddr) == 0 {
		picked := ss.selectLeastStaleEndpoints(allAvailableEndpoints, numEndpoints)
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

// filterValidEndpoints returns the subset of available endpoints that are valid
// according to previously processed observations.
func (ss *serviceState) filterValidEndpoints(availableEndpoints protocol.EndpointAddrList) (protocol.EndpointAddrList, error) {
	ss.endpointStore.endpointsMu.RLock()

	logger := ss.logger.With("method", "filterValidEndpoints").With("qos_instance", "cosmossdk")

	if len(availableEndpoints) == 0 {
		ss.endpointStore.endpointsMu.RUnlock()
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

	ss.endpointStore.endpointsMu.RUnlock()

	// Touch endpoints to update lastSeen for stale endpoint cleanup.
	// Uses a separate WLock call to avoid changing the read-heavy filtering path.
	ss.endpointStore.touchEndpoints(availableEndpoints)

	return filteredEndpointsAddr, nil
}

// selectLeastStaleEndpoints picks endpoints ranked by "blocks behind perceived height"
// when all endpoints have failed normal validation.
//
// This replaces the previous fully-random fallback, which concentrated traffic on
// whichever single-endpoint suppliers happened to be present in the unfiltered pool.
// Random selection treated a far-behind endpoint exactly the same as one that was a
// few blocks behind. With ranking, traffic flows preferentially to least-stale
// candidates while preserving randomness within ties.
//
// Mirrors qos/evm/endpoint_selection.go::selectLeastStaleEndpoints. Cosmos uses the
// CometBFT status latestBlockHeight as the per-endpoint block height source.
func (ss *serviceState) selectLeastStaleEndpoints(availableEndpoints protocol.EndpointAddrList, numEndpoints uint) protocol.EndpointAddrList {
	if numEndpoints == 0 {
		numEndpoints = 1
	}
	if len(availableEndpoints) == 0 {
		return nil
	}

	syncAllowance := ss.serviceQoSConfig.getSyncAllowance()

	ss.serviceStateLock.RLock()
	perceivedBlock := ss.perceivedBlockNumber
	ss.serviceStateLock.RUnlock()

	// No chain context to rank against: degrade to prior random behavior.
	if syncAllowance == 0 || perceivedBlock == 0 {
		return selector.RandomSelectMultiple(availableEndpoints, numEndpoints)
	}

	type scored struct {
		addr         protocol.EndpointAddr
		blocksBehind uint64
		hasData      bool
	}
	scoredEps := make([]scored, 0, len(availableEndpoints))

	ss.endpointStore.endpointsMu.RLock()
	for _, addr := range availableEndpoints {
		s := scored{addr: addr, blocksBehind: math.MaxUint64}
		ep, found := ss.endpointStore.endpoints[addr]
		if found {
			if h, err := ep.checkCometBFTStatus.GetLatestBlockHeight(); err == nil && h > 0 {
				s.hasData = true
				if h >= perceivedBlock {
					s.blocksBehind = 0
				} else {
					s.blocksBehind = perceivedBlock - h
				}
			}
		}
		scoredEps = append(scoredEps, s)
	}
	ss.endpointStore.endpointsMu.RUnlock()

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
