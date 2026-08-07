package shannon

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pokt-network/path/metrics"
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

	// frames reports the connection's cumulative delivered-frame count. A closure rather
	// than a direct read of the request context so the sampler below is testable without
	// standing up a live connection.
	frames func() uint64

	// Rate sampling state, all guarded by the registry mutex.
	//
	// rate is an EWMA of delivered frames per second. It exists because a capped tumble
	// must be spent on the connections carrying LOAD, and socket count does not tell you
	// that: on live traffic a single firehose has been measured at 232 frames/s against
	// 2.2 frames/s for another connection on the same service — a 100x spread that
	// counting sockets renders invisible.
	//
	// Seeded so the first sample yields the connection's lifetime average (lastSample is
	// set to its registration time), which means a connection has a usable rate after one
	// tick rather than two.
	rate       float64
	lastFrames uint64
	lastSample time.Time
}

// sample folds one observation into the entry's frames-per-second EWMA. Caller holds the
// registry write lock.
func (e *websocketConnEntry) sample(now time.Time) {
	if e.frames == nil {
		return
	}
	current := e.frames()
	elapsed := now.Sub(e.lastSample).Seconds()
	if elapsed <= 0 {
		return
	}

	// The counter only ever grows; guard anyway so a replaced closure can never produce a
	// negative rate via unsigned wraparound.
	var delta uint64
	if current > e.lastFrames {
		delta = current - e.lastFrames
	}
	instant := float64(delta) / elapsed

	if e.lastFrames == 0 && e.rate == 0 {
		// First observation: instant IS the lifetime average, so take it as-is rather than
		// dragging it halfway to zero.
		e.rate = instant
	} else {
		e.rate = websocketRateEWMAAlpha*instant + (1-websocketRateEWMAAlpha)*e.rate
	}

	// A multiplicative decay approaches zero without ever reaching it, so an idle
	// connection halves its rate every pass forever and ends up in denormal territory: one
	// live idle-but-bound connection was observed reporting 2.67e-147 frames/s. Ranking
	// still sorted correctly, but the admin tumble response published that as JSON and no
	// consumer could test a connection for "no traffic". Snap to exactly zero instead.
	if e.rate < websocketRateEWMAFloor {
		e.rate = 0
	}

	e.lastFrames = current
	e.lastSample = now
}

// Rate-sampling bounds. Package-level vars (not consts) so tests can drive sampling
// deterministically; production never mutates them.
var (
	// websocketRateSampleInterval is how often every live connection's frames/sec is
	// resampled. One pass per pod (not per connection) over a map that holds tens of
	// entries, so the cost is irrelevant; the interval only bounds how stale a tumble's
	// ranking can be.
	websocketRateSampleInterval = 15 * time.Second

	// websocketRateEWMAAlpha weights the newest observation. 0.5 reacts within a couple of
	// samples — a firehose that starts or stops should change the ranking quickly, since
	// the whole point is to move whoever is heavy NOW, not whoever was heavy an hour ago.
	websocketRateEWMAAlpha = 0.5

	// websocketRateEWMAFloor is the frames/sec below which a connection is treated as
	// silent. Chosen from what the quantity means rather than from float mechanics: 1e-6
	// frames/s is one frame per eleven days, and the slowest thing a real subscription
	// delivers is a newHeads block — seconds apart on the fastest chains, minutes on the
	// slowest. Nothing legitimate lives between here and zero, so anything under it is
	// decay residue from an idle connection.
	websocketRateEWMAFloor = 1e-6
)

func newWebsocketConnRegistry() *websocketConnRegistry {
	return &websocketConnRegistry{
		conns: make(map[protocol.ServiceID]map[*websocketRequestContext]*websocketConnEntry),
	}
}

// startRateSampler runs the per-connection throughput sampler until ctx is done. Started
// explicitly (rather than from the constructor) so tests can build a registry and drive
// sampleRates by hand without a background goroutine.
func (r *websocketConnRegistry) startRateSampler(ctx context.Context) {
	if r == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(websocketRateSampleInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.sampleRates(time.Now())
			case <-ctx.Done():
				return
			}
		}
	}()
}

// sampleRates refreshes every live connection's frames/sec, and publishes each connection's
// rate as a histogram observation.
//
// The per-connection observation is the point: path_websocket_messages_total is a per-domain
// SUM, and a sum cannot tell "one firehose among a hundred idle sockets" apart from "a
// hundred ordinary subscribers". Emitting here rather than from a separate ticker reuses the
// pass that already computes the rate, so the distribution can never disagree with the
// ranking a tumble is spent on.
//
// Observations are collected under the lock but recorded after releasing it: a Prometheus
// histogram takes its own internal lock, and nesting that inside the registry mutex would put
// an unrelated subsystem on the critical path of every admin tumble and rebind.
func (r *websocketConnRegistry) sampleRates(now time.Time) {
	if r == nil {
		return
	}

	type rateSample struct {
		domain    string
		serviceID string
		rate      float64
	}
	var samples []rateSample

	r.mu.Lock()
	for serviceID, svc := range r.conns {
		for _, entry := range svc {
			entry.sample(now)
			samples = append(samples, rateSample{
				domain:    entry.domain,
				serviceID: string(serviceID),
				rate:      entry.rate,
			})
		}
	}
	r.mu.Unlock()

	for _, s := range samples {
		metrics.RecordWebsocketConnectionFrameRate(s.domain, s.serviceID, s.rate)
	}
}

// register adds a live connection. Called when the bridge hands over its controller.
func (r *websocketConnRegistry) register(
	serviceID protocol.ServiceID,
	wrc *websocketRequestContext,
	controller websockets.BridgeController,
	domain, supplier string,
	frames func() uint64,
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
		frames:     frames,
		// Seed the sampler clock at registration so the first sample measures this
		// connection's lifetime average rather than being discarded for lack of a
		// baseline — a connection is then rankable after one tick, not two.
		lastSample: time.Now(),
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

// shutdownAll closes every live bridge on this pod with a proper close handshake and
// returns how many finished before ctx expired.
//
// Exists because http.Server.Shutdown explicitly does NOT close hijacked connections, and
// every websocket is hijacked — so without this the process exits, every socket dies with
// the TCP connection, and both peers report an abnormal closure. Fleetwide that made every
// rollout emit a burst of 1006s across all services in the same second: the client cannot
// tell a deploy from a crash, and the endpoint operator sees a fault they did not cause.
func (r *websocketConnRegistry) shutdownAll(ctx context.Context, reason string) int {
	if r == nil {
		return 0
	}

	// Snapshot under the lock and release it BEFORE closing anything. bridge.Close runs
	// shutdown(), which calls AttachBridge(nil) → deregister → r.mu.Lock(). Closing while
	// holding even the read lock self-deadlocks. tumble() gets away with holding it only
	// because Tumble() is a non-blocking channel send; Close() is synchronous.
	r.mu.RLock()
	controllers := make([]websockets.BridgeController, 0, len(r.conns))
	for _, svc := range r.conns {
		for _, entry := range svc {
			controllers = append(controllers, entry.controller)
		}
	}
	r.mu.RUnlock()

	if len(controllers) == 0 {
		return 0
	}

	// Concurrently, because each close writes a frame to both peers under a one-second
	// deadline apiece. Serially that is seconds per connection, which on a busy replica
	// overruns the pod's termination grace period and gets the process SIGKILLed — the
	// exact abrupt teardown this is here to avoid.
	var closed atomic.Int64
	var wg sync.WaitGroup
	for _, c := range controllers {
		wg.Add(1)
		go func(c websockets.BridgeController) {
			defer wg.Done()
			c.Close(reason)
			closed.Add(1)
		}(c)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		// Report what actually completed rather than what was attempted: a shutdown that
		// timed out having closed half the connections must not read as a clean sweep.
	}

	return int(closed.Load())
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
	orderBy := req.OrderBy
	if orderBy != protocol.TumbleOrderConnections {
		// Default, and the deliberate one: rank by load, not by socket count.
		orderBy = protocol.TumbleOrderThroughput
	}

	result := protocol.WebsocketTumbleResult{
		ServiceID:         req.ServiceID,
		ByDomain:          make(map[string]int),
		DomainCounts:      make(map[string]int),
		DomainThroughput:  make(map[string]float64),
		TumbledThroughput: make(map[string]float64),
		OrderBy:           string(orderBy),
		DryRun:            req.DryRun,
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

	// Snapshot both distributions and collect the eligible connections. Both are reported
	// even on a dry run, which is what makes a dry run answer "who is actually carrying
	// this service" — a question connection counts alone cannot answer.
	type candidate struct {
		entry  *websocketConnEntry
		domain string
		rate   float64
	}
	var candidates []candidate
	for _, entry := range svc {
		result.DomainCounts[entry.domain]++
		result.DomainThroughput[entry.domain] += entry.rate
		if req.Domain != "" && entry.domain != req.Domain {
			continue
		}
		candidates = append(candidates, candidate{entry: entry, domain: entry.domain, rate: entry.rate})
	}
	result.Matched = len(candidates)

	// Order so a Max cap is spent where it shifts the most load.
	//
	// Primary key is the chosen per-domain concentration measure — throughput by default,
	// because moving the busiest OPERATOR is the point of a partial tumble. Secondary key
	// is the individual connection's rate, so within the chosen operator the cap moves its
	// heaviest connections first rather than an arbitrary idle one. Remaining ties break on
	// domain name to keep the operation deterministic.
	sort.SliceStable(candidates, func(i, j int) bool {
		ci, cj := candidates[i], candidates[j]
		if orderBy == protocol.TumbleOrderConnections {
			if a, b := result.DomainCounts[ci.domain], result.DomainCounts[cj.domain]; a != b {
				return a > b
			}
		} else {
			a, b := result.DomainThroughput[ci.domain], result.DomainThroughput[cj.domain]
			if a != b {
				return a > b
			}
		}
		if ci.rate != cj.rate {
			return ci.rate > cj.rate
		}
		return ci.domain < cj.domain
	})

	for _, c := range candidates {
		if req.Max > 0 && result.Tumbled >= req.Max {
			break
		}
		if req.DryRun {
			result.Tumbled++
			result.ByDomain[c.domain]++
			result.TumbledThroughput[c.domain] += c.rate
			continue
		}
		if c.entry.controller.Tumble() {
			result.Tumbled++
			result.ByDomain[c.domain]++
			result.TumbledThroughput[c.domain] += c.rate
		} else {
			result.Skipped++
		}
	}

	return result
}
