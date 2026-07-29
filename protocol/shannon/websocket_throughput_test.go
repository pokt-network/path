package shannon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// sampleTwice advances the registry's rate sampler by two ticks `gap` apart, which is what a
// live pod does on its timer. Two passes because the first establishes each entry's baseline.
func sampleTwice(r *websocketConnRegistry, gap time.Duration) {
	start := time.Now()
	r.sampleRates(start.Add(gap))
	r.sampleRates(start.Add(2 * gap))
}

// The reason this exists. Connection count and throughput routinely disagree: measured on live
// bsc traffic, one operator held 3 of 5 connections at 155 frames/s each while another held a
// single connection at 232 frames/s. Ordering by socket count spends a capped tumble on the
// wrong operator entirely.
func Test_websocketTumble_ThroughputOrderingBeatsSocketCount(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	// manyIdle: 4 sockets, almost no traffic. heavyOne: 1 socket carrying everything.
	idle := make([]*fakeController, 0, 4)
	for i := 0; i < 4; i++ {
		idle = append(idle, registerAtRate(r, "bsc", "manyidle.net", 1))
	}
	heavy := registerAtRate(r, "bsc", "heavyone.xyz", 1000)

	sampleTwice(r, time.Second)

	// Default ordering (throughput) must move the heavy operator first.
	result := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc", Max: 1})

	c.Equal("throughput", result.OrderBy, "throughput must be the default ordering")
	c.Equal(map[string]int{"heavyone.xyz": 1}, result.ByDomain,
		"a capped tumble must move the operator carrying the load, not the one holding the most sockets")
	c.Equal(1, heavy.callCount())
	for _, ctrl := range idle {
		c.Equal(0, ctrl.callCount(), "idle sockets must not absorb the cap")
	}

	// The old behaviour remains available and picks the opposite operator, which is the
	// clearest possible demonstration that the two orderings genuinely disagree.
	r2 := newWebsocketConnRegistry()
	idle2 := make([]*fakeController, 0, 4)
	for i := 0; i < 4; i++ {
		idle2 = append(idle2, registerAtRate(r2, "bsc", "manyidle.net", 1))
	}
	heavy2 := registerAtRate(r2, "bsc", "heavyone.xyz", 1000)
	sampleTwice(r2, time.Second)

	byCount := r2.tumble(protocol.WebsocketTumbleRequest{
		ServiceID: "bsc", Max: 1, OrderBy: protocol.TumbleOrderConnections,
	})
	c.Equal("connections", byCount.OrderBy)
	c.Equal(map[string]int{"manyidle.net": 1}, byCount.ByDomain,
		"connection ordering picks the many-socket operator — the behaviour throughput ordering replaces")
	c.Equal(0, heavy2.callCount())
	_ = idle2
}

// Within the chosen operator the cap must move its heaviest connections first, otherwise a
// partial tumble can move an operator's idle sockets and leave its firehose in place.
func Test_websocketTumble_MovesTheHeaviestConnectionWithinAnOperator(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	quiet := registerAtRate(r, "bsc", "op.net", 1)
	loud := registerAtRate(r, "bsc", "op.net", 500)
	sampleTwice(r, time.Second)

	result := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc", Max: 1})

	c.Equal(1, result.Tumbled)
	c.Equal(1, loud.callCount(), "the busiest connection must be the one that moves")
	c.Equal(0, quiet.callCount())
}

// A dry run must expose the throughput distribution, since that is the question connection
// counts cannot answer — "who is actually carrying this service".
func Test_websocketTumble_DryRunReportsTheThroughputDistribution(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	registerAtRate(r, "bsc", "heavy.net", 300)
	registerAtRate(r, "bsc", "light.xyz", 10)
	registerAtRate(r, "bsc", "light.xyz", 10)
	sampleTwice(r, time.Second)

	result := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc", DryRun: true})

	c.True(result.DryRun)
	c.Equal(map[string]int{"heavy.net": 1, "light.xyz": 2}, result.DomainCounts)

	// Sockets say light.xyz dominates 2:1; throughput says heavy.net dominates ~15:1.
	c.InDelta(300, result.DomainThroughput["heavy.net"], 1)
	c.InDelta(20, result.DomainThroughput["light.xyz"], 1)
	c.Greater(result.DomainThroughput["heavy.net"], result.DomainThroughput["light.xyz"],
		"the throughput view must contradict the socket view here — that is the whole point")

	// And it reports how much load WOULD move, not just how many sockets.
	c.InDelta(300, result.TumbledThroughput["heavy.net"], 1)
}

// The EWMA must track a connection that goes quiet, so a tumble moves whoever is heavy NOW
// rather than whoever was heavy an hour ago.
func Test_connEntryRate_FollowsTrafficUpAndDown(t *testing.T) {
	c := require.New(t)

	var count uint64
	perSample := uint64(100)
	e := &websocketConnEntry{
		frames:     func() uint64 { count += perSample; return count },
		lastSample: time.Now(),
	}
	base := e.lastSample

	// Ramp up: 100 frames per 1s sample → 100/s.
	for i := 1; i <= 4; i++ {
		e.sample(base.Add(time.Duration(i) * time.Second))
	}
	c.InDelta(100, e.rate, 1, "a steady 100 frames/s must read as ~100/s")

	// Go silent. The EWMA must decay toward zero rather than pinning the old value.
	perSample = 0
	for i := 5; i <= 10; i++ {
		e.sample(base.Add(time.Duration(i) * time.Second))
	}
	c.Less(e.rate, 5.0, "a connection that stopped must decay out of the ranking")
}

// Rate accounting must not be fooled by the sampler's own timing.
func Test_connEntryRate_IgnoresNonAdvancingClock(t *testing.T) {
	c := require.New(t)

	now := time.Now()
	e := &websocketConnEntry{frames: func() uint64 { return 500 }, lastSample: now}

	e.sample(now) // zero elapsed — a divide-by-zero if not guarded
	c.Zero(e.rate)

	e.sample(now.Add(-time.Second)) // clock went backwards
	c.Zero(e.rate)

	e.sample(now.Add(time.Second))
	c.InDelta(500, e.rate, 1)
}

// A connection with no counter wired must not panic or poison the ranking.
func Test_connEntryRate_NilCounterIsInert(t *testing.T) {
	c := require.New(t)
	e := &websocketConnEntry{lastSample: time.Now()}
	c.NotPanics(func() { e.sample(time.Now().Add(time.Second)) })
	c.Zero(e.rate)
}

// A brand-new connection must be rankable after ONE sampling pass, not two — otherwise a
// firehose is invisible to a tumble for the first 30 seconds of its life.
func Test_connEntryRate_UsableAfterASingleSample(t *testing.T) {
	c := require.New(t)
	r := newWebsocketConnRegistry()

	registerAtRate(r, "bsc", "fast.net", 250)
	r.sampleRates(time.Now().Add(time.Second))

	result := r.tumble(protocol.WebsocketTumbleRequest{ServiceID: "bsc", DryRun: true})
	c.Greater(result.DomainThroughput["fast.net"], 0.0,
		"one pass must be enough — the entry is seeded with its registration time")
}
