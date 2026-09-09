package shannon

import (
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// heavyRate is comfortably above websocketRebalanceHeavyFPS; idle connections registered
// via registerN never advance their counter and so sample at zero.
const heavyRate = 400

// settleRates registers-then-samples so every entry has a usable rate, mirroring what the
// production sampler does on its first tick.
func settleRates(r *websocketConnRegistry) {
	r.sampleRates(time.Now().Add(time.Second))
}

// registerHeavy adds n heavy connections on a domain.
func registerHeavy(r *websocketConnRegistry, svc protocol.ServiceID, domain string, n int) []*fakeController {
	ctrls := make([]*fakeController, n)
	for i := range ctrls {
		ctrls[i] = registerAtRate(r, svc, domain, heavyRate)
	}
	return ctrls
}

func Test_rebalance_MovesHeavyConnectionOffTheOperatorHoldingThemAll(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	// Three heavy connections on one operator, none on the other — the shape this loop
	// exists to correct. The second operator also holds idle sockets, so a connection
	// split alone would look healthy.
	heavy := registerHeavy(r, "svc", "bigop.net", 3)
	idle := registerN(r, "svc", "smallop.xyz", 4)
	settleRates(r)

	load, ok := r.mostHeavilyLoadedDomain("svc")
	c.True(ok, "3-vs-0 heavy connections must be actionable")
	c.Equal("bigop.net", load.domain)
	c.Equal(3, load.heavy)

	newHeavyConnRebalancer(r).rebalanceOnce(polylog.Ctx(t.Context()))

	moved := 0
	for _, ctrl := range heavy {
		moved += ctrl.callCount()
	}
	c.Equal(1, moved, "exactly one heavy connection moves per pass")
	for _, ctrl := range idle {
		c.Equal(0, ctrl.callCount(), "idle connections on the under-loaded operator must never move")
	}
}

func Test_rebalance_SingleHeavyConnectionNeverPingPongs(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	// One heavy connection and two operators: no arrangement is better than any other,
	// so moving it only hands the concentration to the receiver and re-triggers the loop
	// in the opposite direction. This is the anti-thrash guarantee.
	heavy := registerHeavy(r, "svc", "bigop.net", 1)
	other := registerN(r, "svc", "smallop.xyz", 2)
	settleRates(r)

	_, ok := r.mostHeavilyLoadedDomain("svc")
	c.False(ok, "a spread of one must not be actionable")

	newHeavyConnRebalancer(r).rebalanceOnce(polylog.Ctx(t.Context()))
	c.Equal(0, heavy[0].callCount())
	c.Equal(0, other[0].callCount())
}

func Test_rebalance_DoesNothingWhenOnlyOneOperatorIsAvailable(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	// "Unless there are no options": every connection is on one operator, so a tumble
	// can only re-select that same operator — a rebind and a replayed subscription for
	// no change in placement.
	heavy := registerHeavy(r, "svc", "bigop.net", 5)
	settleRates(r)

	_, ok := r.mostHeavilyLoadedDomain("svc")
	c.False(ok, "a single bound operator leaves nowhere to move to")

	newHeavyConnRebalancer(r).rebalanceOnce(polylog.Ctx(t.Context()))
	for _, ctrl := range heavy {
		c.Equal(0, ctrl.callCount())
	}
}

func Test_rebalance_LeavesAnAlreadyBalancedServiceAlone(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	registerHeavy(r, "svc", "bigop.net", 2)
	registerHeavy(r, "svc", "smallop.xyz", 2)
	settleRates(r)

	_, ok := r.mostHeavilyLoadedDomain("svc")
	c.False(ok, "an even split of heavy connections must not be actionable")
}

func Test_rebalance_IgnoresIdleConnectionsWhenMeasuringLoad(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	// The whole premise: socket count and load disagree. One operator holds far more
	// SOCKETS while the other holds every heavy connection, so a count-based view would
	// move connections off the wrong operator.
	registerN(r, "svc", "bigop.net", 20)
	registerHeavy(r, "svc", "smallop.xyz", 3)
	settleRates(r)

	load, ok := r.mostHeavilyLoadedDomain("svc")
	c.True(ok)
	c.Equal("smallop.xyz", load.domain, "load is measured in frames, not sockets")
}

func Test_rebalance_PicksTheHeaviestConnectionOnTheOverloadedOperator(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	// Within the chosen operator the move must be spent on its busiest connection —
	// moving a barely-heavy one leaves the concentration in place for another cycle.
	slower := registerAtRate(r, "svc", "bigop.net", heavyRate)
	fastest := registerAtRate(r, "svc", "bigop.net", heavyRate*10)
	registerN(r, "svc", "smallop.xyz", 1)
	settleRates(r)

	newHeavyConnRebalancer(r).rebalanceOnce(polylog.Ctx(t.Context()))

	c.Equal(1, fastest.callCount(), "the busiest connection is the one that moves")
	c.Equal(0, slower.callCount())
}

func Test_rebalance_StopsActingWhenMovesRelocateNothing(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	// A registry whose connections never change domain, standing in for the case where
	// re-selection keeps returning the same operator (the avoid-set excludes one endpoint
	// address, so a rebind can land on a sibling registration of the same operator).
	// Without a stall detector this is an unbounded rebind-and-replay loop, which is the
	// thing that would make default-ON indefensible.
	heavy := registerHeavy(r, "svc", "bigop.net", 3)
	registerN(r, "svc", "smallop.xyz", 1)
	settleRates(r)

	b := newHeavyConnRebalancer(r)
	for i := 0; i < 10; i++ {
		b.rebalanceOnce(polylog.Ctx(t.Context()))
	}

	moves := 0
	for _, ctrl := range heavy {
		moves += ctrl.callCount()
	}
	c.Equal(websocketRebalanceMaxStalled, moves,
		"an unchanging distribution must cost a bounded number of moves, not one per pass")
}

func Test_rebalance_ResumesAfterTheDistributionChanges(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	heavy := registerHeavy(r, "svc", "bigop.net", 3)
	registerN(r, "svc", "smallop.xyz", 1)
	settleRates(r)

	b := newHeavyConnRebalancer(r)
	for i := 0; i < 6; i++ {
		b.rebalanceOnce(polylog.Ctx(t.Context()))
	}
	stalledAt := 0
	for _, ctrl := range heavy {
		stalledAt += ctrl.callCount()
	}
	c.Equal(websocketRebalanceMaxStalled, stalledAt, "precondition: the loop has stalled")

	// A move finally lands: one heavy connection is now on the other operator. The
	// picture has changed, so the loop must be willing to act again rather than staying
	// stalled forever.
	registerHeavy(r, "svc", "smallop.xyz", 1)
	registerHeavy(r, "svc", "bigop.net", 2)
	settleRates(r)

	b.rebalanceOnce(polylog.Ctx(t.Context()))

	total := 0
	for _, ctrl := range heavy {
		total += ctrl.callCount()
	}
	c.Greater(total, stalledAt, "a changed distribution must clear the stall")
}

func Test_rebalance_BalancedServiceClearsAnEarlierStall(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	registerHeavy(r, "svc", "bigop.net", 3)
	registerN(r, "svc", "smallop.xyz", 1)
	settleRates(r)

	b := newHeavyConnRebalancer(r)
	for i := 0; i < 5; i++ {
		b.rebalanceOnce(polylog.Ctx(t.Context()))
	}
	// The counter keeps incrementing while the loop skips, so assert the threshold was
	// crossed rather than pinning an exact value that depends on the pass count.
	c.GreaterOrEqual(b.stalled["svc"], websocketRebalanceMaxStalled, "precondition: stalled")

	// The service becomes balanced on its own. Its stall memory must not persist into
	// the next imbalance, which is a new situation deserving a full budget of attempts.
	registerHeavy(r, "svc", "smallop.xyz", 3)
	settleRates(r)

	b.rebalanceOnce(polylog.Ctx(t.Context()))
	c.Equal(0, b.stalled["svc"])
	c.NotContains(b.lastSeen, protocol.ServiceID("svc"))
}
