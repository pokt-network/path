package shannon

import (
	"sort"
	"sync"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/websockets"
)

// websocketConnRegistry tracks the live websocket bridges this pod is serving, keyed by
// service, so an operator can force a subset of them to rebind onto different suppliers
// ("tumble") without restarting the pod.
//
// Why this exists: a websocket connection binds ONE endpoint for its entire lifetime and
// only moves at a session rollover or when the staleness watchdog fires. A long-lived
// high-volume subscriber that landed on one operator therefore stays there for hours,
// which shows up as a concentration that no amount of selection tuning can undo for
// already-established connections. Restarting the pod redistributes them, but it also
// drops every client and resets unrelated in-memory state. Tumbling moves connections
// while keeping clients connected.
//
// Registration is driven by the bridge itself through websockets.BridgeAttacher, so an
// entry exists exactly as long as its bridge is alive.
//
// Like the other admin surfaces (circuit-breaker clear, chain-state clear), this is
// PER-POD in-memory state: a tumble request must be issued to each pod separately.
type websocketConnRegistry struct {
	mu sync.RWMutex

	// conns maps a service to its live connections. The inner map is keyed by the
	// request context pointer, which is unique per connection and stable for its life.
	conns map[protocol.ServiceID]map[*websocketRequestContext]*websocketConnEntry
}

// websocketConnEntry is one live websocket connection's tumble handle plus the routing
// facts an operator filters on.
type websocketConnEntry struct {
	// controller forces this connection to rebind. Never nil while registered.
	controller websockets.BridgeController

	// domain is the registrable domain (eTLD+1) of the CURRENTLY bound endpoint. It is
	// re-written after every successful rebind, so it tracks where the connection
	// actually is rather than where it started.
	//
	// Held here rather than read from the request context on demand because the bound
	// endpoint is mutated on the bridge goroutine while admin requests arrive on HTTP
	// goroutines; keeping it under the registry mutex is what makes filtering race-free.
	domain string

	// supplier is the operator address of the currently bound endpoint, kept for the
	// response body so an operator can see exactly what moved.
	supplier string
}

func newWebsocketConnRegistry() *websocketConnRegistry {
	return &websocketConnRegistry{
		conns: make(map[protocol.ServiceID]map[*websocketRequestContext]*websocketConnEntry),
	}
}

// register adds a live connection. Called when the bridge hands over its controller.
func (r *websocketConnRegistry) register(
	serviceID protocol.ServiceID,
	wrc *websocketRequestContext,
	controller websockets.BridgeController,
	domain, supplier string,
) {
	if r == nil || wrc == nil || controller == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.conns[serviceID] == nil {
		r.conns[serviceID] = make(map[*websocketRequestContext]*websocketConnEntry)
	}
	r.conns[serviceID][wrc] = &websocketConnEntry{
		controller: controller,
		domain:     domain,
		supplier:   supplier,
	}
}

// deregister drops a connection, and the service's map once it holds none. Called when
// the bridge shuts down; must not leave dead bridges pinned.
func (r *websocketConnRegistry) deregister(serviceID protocol.ServiceID, wrc *websocketRequestContext) {
	if r == nil || wrc == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	svc, ok := r.conns[serviceID]
	if !ok {
		return
	}
	delete(svc, wrc)
	if len(svc) == 0 {
		delete(r.conns, serviceID)
	}
}

// updateBinding re-points an entry at the endpoint the connection just rebound onto, so
// a subsequent domain-filtered tumble matches on where the connection IS, not where it
// started. A no-op for connections that were never registered.
func (r *websocketConnRegistry) updateBinding(
	serviceID protocol.ServiceID,
	wrc *websocketRequestContext,
	domain, supplier string,
) {
	if r == nil || wrc == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.conns[serviceID][wrc]; ok {
		entry.domain = domain
		entry.supplier = supplier
	}
}

// tumble moves the requested connections onto different suppliers.
//
// The controllers are invoked while holding only a read lock and each Tumble() call is a
// non-blocking channel send, so a slow or wedged bridge cannot stall the admin request or
// block registration of new connections.
func (r *websocketConnRegistry) tumble(req protocol.WebsocketTumbleRequest) protocol.WebsocketTumbleResult {
	result := protocol.WebsocketTumbleResult{
		ServiceID:    req.ServiceID,
		ByDomain:     make(map[string]int),
		DomainCounts: make(map[string]int),
		DryRun:       req.DryRun,
	}
	if r == nil {
		return result
	}

	serviceID := protocol.ServiceID(req.ServiceID)

	r.mu.RLock()
	defer r.mu.RUnlock()

	svc := r.conns[serviceID]
	result.Total = len(svc)
	if result.Total == 0 {
		return result
	}

	// Snapshot the current distribution and collect the eligible connections.
	type candidate struct {
		entry  *websocketConnEntry
		domain string
	}
	var candidates []candidate
	for _, entry := range svc {
		result.DomainCounts[entry.domain]++
		if req.Domain != "" && entry.domain != req.Domain {
			continue
		}
		candidates = append(candidates, candidate{entry: entry, domain: entry.domain})
	}
	result.Matched = len(candidates)

	// Order by descending domain concentration so a Max cap is spent on the operators
	// that dominate, which is the whole point of a partial tumble. Ties broken by domain
	// name to keep the operation deterministic.
	sort.SliceStable(candidates, func(i, j int) bool {
		ci, cj := result.DomainCounts[candidates[i].domain], result.DomainCounts[candidates[j].domain]
		if ci != cj {
			return ci > cj
		}
		return candidates[i].domain < candidates[j].domain
	})

	for _, c := range candidates {
		if req.Max > 0 && result.Tumbled >= req.Max {
			break
		}
		if req.DryRun {
			result.Tumbled++
			result.ByDomain[c.domain]++
			continue
		}
		if c.entry.controller.Tumble() {
			result.Tumbled++
			result.ByDomain[c.domain]++
		} else {
			result.Skipped++
		}
	}

	return result
}
