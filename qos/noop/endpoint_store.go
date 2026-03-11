package noop

import (
	"sync"
	"time"

	"github.com/pokt-network/path/protocol"
)

// defaultEndpointTTL is how long an endpoint entry is kept without being seen
// in an active session. Sessions last ~1 hour, so 2x gives buffer.
const defaultEndpointTTL = 2 * time.Hour

// endpoint holds per-endpoint state for block height tracking.
type endpointState struct {
	// blockHeight is the latest block height reported by this endpoint.
	blockHeight uint64

	// lastSeen is updated when the endpoint appears in an active session.
	// Used by stale endpoint cleanup to remove entries that are no longer active.
	lastSeen time.Time
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

// touchEndpoints updates the lastSeen timestamp for each endpoint address present in the store.
// Called after filtering to mark endpoints that appeared in an active session.
func (es *endpointStore) touchEndpoints(addrs protocol.EndpointAddrList) {
	now := time.Now()
	es.mu.Lock()
	defer es.mu.Unlock()
	for _, addr := range addrs {
		if ep, found := es.endpoints[addr]; found {
			ep.lastSeen = now
			es.endpoints[addr] = ep
		}
	}
}

// sweepStaleEndpoints removes endpoints whose lastSeen is older than ttl (and non-zero).
// Returns the list of removed endpoint addresses for Redis cleanup.
func (es *endpointStore) sweepStaleEndpoints(ttl time.Duration) []protocol.EndpointAddr {
	cutoff := time.Now().Add(-ttl)
	es.mu.Lock()
	defer es.mu.Unlock()

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
