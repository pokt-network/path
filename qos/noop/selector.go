package noop

import (
	"errors"
	"math/rand"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
)

var (
	_ protocol.EndpointSelector = RandomEndpointSelector{}
	_ protocol.EndpointSelector = &filteringSelector{}
)

// RandomEndpointSelector returns a randomly selected endpoint from the set of available ones.
// It has no fields, since the endpoint selection is random.
type RandomEndpointSelector struct{}

// Select returns a randomly selected endpoint from the set of supplied endpoints.
func (RandomEndpointSelector) Select(endpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	if len(endpoints) == 0 {
		return protocol.EndpointAddr(""), errors.New("RandomEndpointSelector: an empty endpoint list was supplied to the selector")
	}

	selectedEndpointAddr := endpoints[rand.Intn(len(endpoints))]
	return selectedEndpointAddr, nil
}

// SelectMultiple returns multiple randomly selected endpoints from the set of supplied endpoints.
func (RandomEndpointSelector) SelectMultiple(endpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("RandomEndpointSelector: an empty endpoint list was supplied to the selector")
	}

	return selector.RandomSelectMultiple(endpoints, numEndpoints), nil
}

// SelectMultipleWithArchival returns multiple randomly selected endpoints with optional archival filtering.
// NoOp QoS does not have an archival concept, so requiresArchival is ignored.
func (RandomEndpointSelector) SelectMultipleWithArchival(endpoints protocol.EndpointAddrList, numEndpoints uint, _ bool) (protocol.EndpointAddrList, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("RandomEndpointSelector: an empty endpoint list was supplied to the selector")
	}

	return selector.RandomSelectMultiple(endpoints, numEndpoints), nil
}

// filteringSelector selects endpoints after filtering out those whose block height
// is too far behind the perceived block height. When filtering is disabled or no
// data is available, it falls back to random selection.
type filteringSelector struct {
	logger        polylog.Logger
	endpointStore *endpointStore
	qos           *NoOpQoS
}

// newFilteringSelector creates a filteringSelector bound to this NoOpQoS instance.
func (n *NoOpQoS) newFilteringSelector() *filteringSelector {
	return &filteringSelector{
		logger:        n.logger,
		endpointStore: n.endpointStore,
		qos:           n,
	}
}

// Select returns a single endpoint after applying block-height filtering.
// Falls back to random from the full list if all endpoints fail validation.
func (fs *filteringSelector) Select(endpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	if len(endpoints) == 0 {
		return protocol.EndpointAddr(""), errors.New("filteringSelector: an empty endpoint list was supplied to the selector")
	}

	filtered := fs.filterValidEndpoints(endpoints)

	if len(filtered) == 0 {
		fs.logger.Warn().Msg("All endpoints failed block height validation, falling back to random selection")
		return endpoints[rand.Intn(len(endpoints))], nil
	}

	return filtered[rand.Intn(len(filtered))], nil
}

// SelectMultiple returns multiple endpoints after applying block-height filtering.
// Falls back to random selection if all endpoints fail validation.
func (fs *filteringSelector) SelectMultiple(endpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	return fs.SelectMultipleWithArchival(endpoints, numEndpoints, false)
}

// SelectMultipleWithArchival returns multiple endpoints after applying block-height filtering.
// NoOp QoS does not have an archival concept, so requiresArchival is ignored.
func (fs *filteringSelector) SelectMultipleWithArchival(endpoints protocol.EndpointAddrList, numEndpoints uint, _ bool) (protocol.EndpointAddrList, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("filteringSelector: an empty endpoint list was supplied to the selector")
	}

	filtered := fs.filterValidEndpoints(endpoints)

	if len(filtered) == 0 {
		fs.logger.Warn().Msg("All endpoints failed block height validation, falling back to random selection")
		return selector.RandomSelectMultiple(endpoints, numEndpoints), nil
	}

	return selector.SelectEndpointsWithDiversity(fs.logger, filtered, numEndpoints), nil
}

// filterValidEndpoints returns the subset of endpoints that pass the block height
// sync allowance check. When syncAllowance is 0 or no perceived block height exists,
// all endpoints pass (filtering is disabled).
func (fs *filteringSelector) filterValidEndpoints(endpoints protocol.EndpointAddrList) protocol.EndpointAddrList {
	syncAllowance := fs.qos.syncAllowance.Load()

	// If sync allowance is 0, filtering is disabled — pass all endpoints
	if syncAllowance == 0 {
		return endpoints
	}

	fs.qos.serviceStateMu.RLock()
	perceivedBlock := fs.qos.perceivedBlockHeight
	fs.qos.serviceStateMu.RUnlock()

	// If we don't have a perceived block number yet, pass all endpoints
	if perceivedBlock == 0 {
		return endpoints
	}

	minAllowedBlock := perceivedBlock - syncAllowance

	fs.endpointStore.mu.RLock()
	defer fs.endpointStore.mu.RUnlock()

	var filtered protocol.EndpointAddrList
	for _, addr := range endpoints {
		ep, found := fs.endpointStore.endpoints[addr]
		if !found {
			// Fresh endpoint (not yet in store) — allow it through
			filtered = append(filtered, addr)
			continue
		}

		if ep.blockHeight == 0 {
			// No block height observation yet — allow it through
			filtered = append(filtered, addr)
			continue
		}

		if ep.blockHeight < minAllowedBlock {
			fs.logger.Debug().
				Str("endpoint", string(addr)).
				Uint64("endpoint_block", ep.blockHeight).
				Uint64("min_allowed", minAllowedBlock).
				Uint64("sync_allowance", syncAllowance).
				Msg("Endpoint filtered out — behind sync allowance")
			continue
		}

		filtered = append(filtered, addr)
	}

	return filtered
}
