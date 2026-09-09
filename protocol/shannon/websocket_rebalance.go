package shannon

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

// Heavy-connection rebalancing.
//
// The per-operator concentration cap governs how many websocket CONNECTIONS an operator
// is handed. Settlement, however, is per FRAME: every endpoint→client frame is a
// reward-eligible relay. Those two currencies diverge badly because per-connection frame
// rate is heavy-tailed — measured on one live service, connection rates run p50 ~0.3
// frames/s against p99 ~800, so a single subscriber can be worth thousands of the idle
// sockets beside it.
//
// The consequence is that a service can hold a near-even CONNECTION split while one
// operator collects essentially all of the frames, purely because the heavy subscribers
// happened to land there. Measured over 6h on a two-operator service: connections split
// 54/46 while frames split 98/2. No cap on selection can correct this, for two reasons —
// a connection's rate is unknowable at selection time, and a websocket connection binds
// one endpoint for its entire lifetime, so a single draw fixes the split for as long as
// the client stays up.
//
// This loop closes that gap from the other side: it watches where the HEAVY connections
// actually ended up and forces a re-draw when they pile onto one operator.
//
// Deliberately NOT a frame-share cap. Moving the single busiest connection off the
// leading operator would just relocate the concentration to whoever receives it, and the
// loop would then move it back — a permanent ping-pong that pays a rebind and a
// subscription replay every cycle. Balancing the COUNT of heavy connections has the
// anti-thrash property built in: with only one heavy connection in a service there is no
// arrangement that improves on any other, and the spread test below never fires.

// Rebalancing bounds. Package-level vars (not consts) so tests can drive the loop
// deterministically; production mutates them only through the env overrides below.
var (
	// websocketRebalanceInterval is how often each service is examined. Deliberately far
	// slower than the 15s rate sampler: acting on a rate that has not settled would move
	// connections on noise, and every move costs a rebind plus a replayed subscription.
	websocketRebalanceInterval = 5 * time.Minute

	// websocketRebalanceHeavyFPS is the frames/sec at or above which a connection counts
	// as heavy — i.e. worth balancing rather than worth ignoring.
	//
	// ponytail: a fixed threshold, not a per-service quantile. A quantile would adapt to
	// each service's own traffic shape, but it also makes "heavy" relative, so a service
	// where every connection is idle would still nominate a "heaviest" tenth and churn
	// them for no gain. Revisit if services appear whose real subscriptions sit an order
	// of magnitude either side of this.
	websocketRebalanceHeavyFPS = 25.0

	// websocketRebalanceMinSpread is how many more heavy connections the leading operator
	// must hold than the least-loaded one before anything moves.
	//
	// Two, not one, and this is the load-bearing constant. At a spread of one the only
	// possible move hands the imbalance to the receiver, which re-triggers the loop in
	// the opposite direction forever. At two there is always an arrangement strictly
	// better than the current one, so every move this loop makes is an improvement.
	websocketRebalanceMinSpread = 2
)

// heavyConnRebalancer owns the rebalancing policy. Split from websocketConnRegistry
// deliberately: the registry owns which connections exist and where they are bound, this
// owns what to do about it. The per-service memory below is the reason it needs to be a
// type at all — deciding whether a move WORKED requires remembering the previous pass.
//
// All fields are touched only by the single goroutine started by run(), so no locking:
// the registry does its own under its mutex.
type heavyConnRebalancer struct {
	reg *websocketConnRegistry

	// lastSeen is the leading operator observed on the previous actionable pass, per
	// service, plus how many consecutive passes it has failed to change.
	lastSeen map[protocol.ServiceID]heavyLoad
	stalled  map[protocol.ServiceID]int
}

func newHeavyConnRebalancer(reg *websocketConnRegistry) *heavyConnRebalancer {
	return &heavyConnRebalancer{
		reg:      reg,
		lastSeen: make(map[protocol.ServiceID]heavyLoad),
		stalled:  make(map[protocol.ServiceID]int),
	}
}

// websocketRebalanceMaxStalled is how many consecutive ineffective moves are tolerated on
// one service before the loop stops acting on it.
//
// This is what bounds the cost of running default-ON. A tumble is a REQUEST to re-select,
// not a guarantee of landing elsewhere: the avoid-set excludes a single endpoint address,
// so a rebind can return to the same operator through a sibling registration, and on a
// service where that keeps happening an unbounded loop would pay a rebind and a replayed
// subscription every interval forever. After this many passes that changed nothing, the
// service is left alone until its distribution moves on its own — a client connects or
// leaves, or a rollover relocates something. No timer resumes it, because the thing worth
// waiting for is a change in the picture, not the passage of time.
var websocketRebalanceMaxStalled = 3

// run drives the rebalancer until ctx is done. Started explicitly (like startRateSampler)
// so tests can drive rebalanceOnce by hand.
func (b *heavyConnRebalancer) run(ctx context.Context, logger polylog.Logger) {
	if b == nil || b.reg == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(websocketRebalanceInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				b.rebalanceOnce(logger)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// rebalanceOnce examines every service this pod is serving and moves at most one heavy
// connection per service per pass.
//
// One per pass, not "enough to reach balance": a tumble is a request to re-select, not a
// guarantee of landing elsewhere, so the effect of a move can only be read on the next
// pass. Moving several at once would act on a picture already invalidated by the first.
func (b *heavyConnRebalancer) rebalanceOnce(logger polylog.Logger) {
	if b == nil || b.reg == nil {
		return
	}

	for _, serviceID := range b.reg.servicesWithConnections() {
		load, ok := b.reg.mostHeavilyLoadedDomain(serviceID)
		if !ok {
			// Balanced, or nothing worth moving. Forget any stall: the next imbalance is
			// a genuinely new situation and deserves its full budget of attempts.
			delete(b.lastSeen, serviceID)
			delete(b.stalled, serviceID)
			continue
		}

		// Unchanged leader AND unchanged heavy count means the previous move relocated
		// nothing. Any change at all — either field — clears the count.
		if prev, seen := b.lastSeen[serviceID]; seen && prev == load {
			b.stalled[serviceID]++
		} else {
			b.stalled[serviceID] = 0
		}
		b.lastSeen[serviceID] = load

		if b.stalled[serviceID] >= websocketRebalanceMaxStalled {
			metrics.RecordWebsocketRebalance(string(serviceID), load.domain, metrics.WSRebalanceStalled)
			logger.With(
				"service_id", string(serviceID),
				"domain", load.domain,
				"heavy_connections", load.heavy,
			).Warn().Msg("websocket heavy-connection rebalance stalled: moves are not relocating, leaving this service alone until its distribution changes")
			continue
		}

		result := b.reg.tumble(protocol.WebsocketTumbleRequest{
			ServiceID: string(serviceID),
			Domain:    load.domain,
			Max:       1,
			OrderBy:   protocol.TumbleOrderThroughput,
		})

		outcome := metrics.WSRebalanceMoved
		if result.Tumbled == 0 {
			// Every candidate refused — a tumble is already queued on each. Next pass
			// retries; nothing is stuck.
			outcome = metrics.WSRebalanceSkipped
		}
		metrics.RecordWebsocketRebalance(string(serviceID), load.domain, outcome)

		logger.With(
			"service_id", string(serviceID),
			"domain", load.domain,
			"heavy_connections", load.heavy,
			"tumbled", result.Tumbled,
			"skipped", result.Skipped,
		).Info().Msg("websocket heavy-connection rebalance")
	}
}

// servicesWithConnections snapshots the services holding at least one live connection, so
// the per-service work below takes the lock once per service rather than holding it
// across the whole pass.
func (r *websocketConnRegistry) servicesWithConnections() []protocol.ServiceID {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]protocol.ServiceID, 0, len(r.conns))
	for serviceID, svc := range r.conns {
		if len(svc) > 0 {
			out = append(out, serviceID)
		}
	}
	return out
}

// mostHeavilyLoadedDomain reports the operator holding disproportionately many heavy
// connections for a service, or ok=false when nothing should move.
//
// The spread test below is the only gate, and it covers both reasons to do nothing:
//   - A service whose heavy traffic is a single connection cannot be split by any
//     arrangement, so no move improves on the current one.
//   - "Unless there are no options": with a single operator bound, that operator is both
//     the most- and least-loaded, so the spread is zero and nothing moves. A tumble there
//     could only re-select the same operator, buying a rebind and a replayed subscription
//     for no change in placement. An explicit len(...) < 2 guard was written first and
//     removed — no test could tell it from its absence, because the spread test already
//     decides that case.
//
// ponytail: "is an option" is approximated by "currently holds at least one connection of
// this service on this pod". An operator with endpoints in the session but zero live
// connections here is therefore invisible, and the loop stays its hand when it could have
// acted. That failure direction is deliberate — doing nothing costs nothing, whereas
// tumbling toward an operator that cannot take the connection costs a rebind and lands
// back where it started. Plumb the selectable operator set in from the endpoint store if
// this proves too conservative in practice.
func (r *websocketConnRegistry) mostHeavilyLoadedDomain(serviceID protocol.ServiceID) (heavyLoad, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Every bound domain is a key, including those holding zero heavy connections: they
	// are the destinations a move would go to, so they must count in the spread.
	heavyByDomain := make(map[string]int)
	for _, entry := range r.conns[serviceID] {
		if _, seen := heavyByDomain[entry.domain]; !seen {
			heavyByDomain[entry.domain] = 0
		}
		if entry.rate >= websocketRebalanceHeavyFPS {
			heavyByDomain[entry.domain]++
		}
	}

	top, most, fewest := "", -1, -1
	for domain, count := range heavyByDomain {
		// Ties break on domain name so repeated passes over the same distribution pick
		// the same operator instead of oscillating with Go's map iteration order.
		if count > most || (count == most && domain < top) {
			top, most = domain, count
		}
		if fewest < 0 || count < fewest {
			fewest = count
		}
	}

	if most-fewest < websocketRebalanceMinSpread {
		return heavyLoad{}, false
	}
	return heavyLoad{domain: top, heavy: most}, true
}

// heavyLoad is the leading operator for a service and how many heavy connections it
// holds. The count is what makes a move's effect checkable on the next pass: if the same
// operator still holds the same number, the tumble did not relocate anything.
type heavyLoad struct {
	domain string
	heavy  int
}

// heavyConnRebalanceEnabled reports whether the rebalancer should run, and applies the
// env overrides.
//
// DEFAULT ON; PATH_WS_HEAVY_REBALANCE=false disables. Graduates to a YAML config field
// once validated, the same path PATH_WEBSOCKET_SESSION_REBIND took.
//
// Shipping on is defensible because the cost is bounded by construction rather than by
// the flag: at most one connection moves per service per interval, only when a strictly
// better arrangement exists, and a service whose moves stop relocating is abandoned after
// websocketRebalanceMaxStalled passes. The reachable endpoint set is untouched at every
// setting — this loop only asks connections to re-select from the pool selection already
// governs, so it can never make an endpoint unreachable.
func heavyConnRebalanceEnabled(logger polylog.Logger) bool {
	if os.Getenv("PATH_WS_HEAVY_REBALANCE") == "false" {
		logger.Info().Msg("websocket heavy-connection rebalance DISABLED via PATH_WS_HEAVY_REBALANCE=false")
		return false
	}

	if v := os.Getenv("PATH_WS_HEAVY_REBALANCE_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			websocketRebalanceInterval = d
		} else {
			logger.Warn().Msgf("ignoring invalid PATH_WS_HEAVY_REBALANCE_INTERVAL=%q", v)
		}
	}
	if v := os.Getenv("PATH_WS_HEAVY_REBALANCE_FPS"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			websocketRebalanceHeavyFPS = f
		} else {
			logger.Warn().Msgf("ignoring invalid PATH_WS_HEAVY_REBALANCE_FPS=%q", v)
		}
	}
	if v := os.Getenv("PATH_WS_HEAVY_REBALANCE_MIN_SPREAD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 2 {
			websocketRebalanceMinSpread = n
		} else {
			// Below 2 is rejected rather than clamped: a spread of 1 is the ping-pong
			// configuration, and silently correcting it would hide a real misconfiguration.
			logger.Warn().Msgf("ignoring PATH_WS_HEAVY_REBALANCE_MIN_SPREAD=%q (must be an integer >= 2)", v)
		}
	}
	return true
}
