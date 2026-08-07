package shannon

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// fakeController is a websockets.BridgeController that records Tumble calls and can
// refuse them, standing in for a bridge with a tumble already queued. It also records
// Close calls, and can block in Close to stand in for a bridge whose peer never answers
// the close handshake.
type fakeController struct {
	mu      sync.Mutex
	calls   int
	refuse  bool
	closes  int
	closeAt chan struct{} // when non-nil, Close blocks until it is closed
}

func (f *fakeController) Tumble() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return !f.refuse
}

func (f *fakeController) Close(string) {
	f.mu.Lock()
	block := f.closeAt
	f.mu.Unlock()

	if block != nil {
		<-block
	}

	f.mu.Lock()
	f.closes++
	f.mu.Unlock()
}

func (f *fakeController) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

func (f *fakeController) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// registerN adds n idle connections (no traffic) on the given domain and returns their
// controllers.
func registerN(r *websocketConnRegistry, svc protocol.ServiceID, domain string, n int) []*fakeController {
	ctrls := make([]*fakeController, n)
	for i := range ctrls {
		ctrls[i] = &fakeController{}
		// A fresh pointer per connection: the registry keys on the request context
		// pointer, which is unique per live connection.
		r.register(svc, &websocketRequestContext{}, ctrls[i], domain, "supplier-"+domain, func() uint64 { return 0 })
	}
	return ctrls
}

// registerAtRate adds one connection on the given domain whose delivered-frame counter
// advances by framesPerSample on every sampling pass, and returns its controller.
func registerAtRate(
	r *websocketConnRegistry,
	svc protocol.ServiceID,
	domain string,
	framesPerSample uint64,
) *fakeController {
	ctrl := &fakeController{}
	var count uint64
	r.register(svc, &websocketRequestContext{}, ctrl, domain, "supplier-"+domain, func() uint64 {
		count += framesPerSample
		return count
	})
	return ctrl
}

func Test_websocketTumble_DomainFilterMovesOnlyThatOperator(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	big := registerN(r, "bsc", "bigop.net", 4)
	small := registerN(r, "bsc", "smallop.xyz", 1)

	result := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc", Domain: "bigop.net"})

	c.Equal(5, result.Total, "every live connection is counted, filtered or not")
	c.Equal(4, result.Matched)
	c.Equal(4, result.Tumbled)
	c.Equal(0, result.Skipped)
	c.Equal(map[string]int{"bigop.net": 4}, result.ByDomain)
	c.Equal(map[string]int{"bigop.net": 4, "smallop.xyz": 1}, result.DomainCounts,
		"the before-picture must cover all domains so the operator can see what they acted on")

	for _, ctrl := range big {
		c.Equal(1, ctrl.callCount(), "every matched connection is tumbled exactly once")
	}
	c.Equal(0, small[0].callCount(), "a connection outside the domain filter must never move")
}

func Test_websocketTumble_MaxSpendsItselfOnTheMostConcentratedOperator(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	big := registerN(r, "bsc", "bigop.net", 5)
	small := registerN(r, "bsc", "smallop.xyz", 2)

	// No domain filter: the cap alone must steer the work toward the dominant operator,
	// which is the point of a partial tumble. Ordering is requested explicitly — these
	// connections are idle, so throughput ordering has nothing to discriminate on and the
	// assertion would otherwise pass only by the alphabetical tie-break.
	result := r.tumble(protocol.WebsocketTumbleRequest{
		ServiceID: "bsc", Max: 3, OrderBy: protocol.TumbleOrderConnections,
	})

	c.Equal(7, result.Matched, "no domain filter means every connection is eligible")
	c.Equal(3, result.Tumbled, "Max caps the number actually moved")
	c.Equal(map[string]int{"bigop.net": 3}, result.ByDomain,
		"the cap is spent on the most concentrated operator, not spread arbitrarily")

	moved := 0
	for _, ctrl := range big {
		moved += ctrl.callCount()
	}
	c.Equal(3, moved)
	for _, ctrl := range small {
		c.Equal(0, ctrl.callCount(), "the small operator keeps its connections while the cap holds")
	}
}

func Test_websocketTumble_DryRunMovesNothing(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	ctrls := registerN(r, "bsc", "bigop.net", 3)

	result := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc", DryRun: true})

	c.True(result.DryRun)
	c.Equal(3, result.Tumbled, "a dry run reports what WOULD move")
	c.Equal(map[string]int{"bigop.net": 3}, result.DomainCounts)
	for _, ctrl := range ctrls {
		c.Equal(0, ctrl.callCount(), "a dry run must not touch a single connection")
	}
}

func Test_websocketTumble_RefusedTumbleCountsAsSkippedNotMoved(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	ctrls := registerN(r, "bsc", "bigop.net", 3)
	ctrls[0].refuse = true // stands in for a bridge that already has a tumble queued

	result := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc"})

	c.Equal(3, result.Matched)
	c.Equal(2, result.Tumbled, "a refused tumble must not be reported as moved")
	c.Equal(1, result.Skipped)
	c.Equal(map[string]int{"bigop.net": 2}, result.ByDomain)
}

func Test_websocketTumble_DeregisteredConnectionIsNeverTumbled(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	ctrl := &fakeController{}
	wrc := &websocketRequestContext{}
	r.register("bsc", wrc, ctrl, "bigop.net", "supplier-1", func() uint64 { return 0 })
	r.deregister("bsc", wrc)

	result := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc"})

	c.Equal(0, result.Total, "a shut-down bridge must not stay pinned in the registry")
	c.Equal(0, ctrl.callCount(), "a dead bridge must never be handed a tumble")
}

func Test_websocketTumble_MatchesWhereTheConnectionIsNowNotWhereItStarted(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	ctrl := &fakeController{}
	wrc := &websocketRequestContext{}
	r.register("bsc", wrc, ctrl, "bigop.net", "supplier-1", func() uint64 { return 0 })

	// The connection rebinds onto a different operator, as a rollover or a prior tumble
	// would do. A later domain-filtered tumble must follow it.
	r.updateBinding("bsc", wrc, "smallop.xyz", "supplier-2")

	stale := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc", Domain: "bigop.net"})
	c.Equal(0, stale.Matched, "the old binding must not still match after a rebind")
	c.Equal(0, ctrl.callCount())

	current := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc", Domain: "smallop.xyz"})
	c.Equal(1, current.Matched, "the connection must match its CURRENT operator")
	c.Equal(1, ctrl.callCount())
}

func Test_websocketRateEWMA_IdleConnectionDecaysToExactlyZero(t *testing.T) {
	c := require.New(t)

	var frames uint64
	e := &websocketConnEntry{frames: func() uint64 { return frames }}

	base := time.Now()
	e.lastSample = base

	// One burst of real traffic, then silence: 30 frames over 15s is the shape of a live
	// subscription that stops delivering while staying bound.
	frames = 30
	e.sample(base.Add(websocketRateSampleInterval))
	c.Greater(e.rate, 0.0, "a connection that delivered frames must report a rate")

	// Halving at alpha=0.5 from 2 frames/s needs ~21 passes to cross 1e-6; 40 leaves margin
	// without being so many that a missing floor would underflow to zero on its own (that
	// takes ~1000).
	for i := 2; i <= 41; i++ {
		e.sample(base.Add(time.Duration(i) * websocketRateSampleInterval))
	}

	c.Equal(0.0, e.rate,
		"an idle connection must read exactly 0 so consumers can test for no traffic, not a denormal")
}

func Test_websocketTumble_UnknownServiceIsANoOp(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()
	registerN(r, "bsc", "bigop.net", 2)

	result := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "eth"})

	c.Equal(0, result.Total)
	c.Equal(0, result.Tumbled)
	c.NotNil(result.ByDomain, "maps must be non-nil so the JSON response is {} rather than null")
	c.NotNil(result.DomainCounts)
}
