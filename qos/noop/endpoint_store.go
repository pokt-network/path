package noop

import (
	"sync"

	"github.com/pokt-network/path/protocol"
)

// endpoint holds per-endpoint state for block height tracking.
type endpointState struct {
	// blockHeight is the latest block height reported by this endpoint.
	blockHeight uint64
}

// endpointStore provides thread-safe storage for per-endpoint block height data.
type endpointStore struct {
	mu        sync.RWMutex
	endpoints map[protocol.EndpointAddr]endpointState
}

// newEndpointStore creates a new endpointStore.
func newEndpointStore() *endpointStore {
	return &endpointStore{
		endpoints: make(map[protocol.EndpointAddr]endpointState),
	}
}
