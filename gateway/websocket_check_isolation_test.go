package gateway

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alitto/pond/v2"
	"github.com/stretchr/testify/require"
)

// The incident this guards against, measured twice on canary in one evening.
//
// A health-check cycle ends with group.Wait(), so it cannot finish until every job it
// submitted does. A websocket check that reads a response blocks until the endpoint answers
// or the timeout expires — and an endpoint whose failure mode is SILENCE takes the full
// timeout. Submitted into the shared group, one such endpoint sets a floor under the entire
// cycle and throttles every other check with it: websocket checks fell to 3.5/s and json_rpc,
// which shares the cycle, dropped from ~397/s to ~112/s.
//
// The property that matters is therefore not "the websocket check is fast" — it isn't, and no
// probe design makes it reliably fast against a silent endpoint. It is that a slow websocket
// check cannot delay the cycle at all.
func Test_websocketChecks_CannotDelayTheSharedCycle(t *testing.T) {
	c := require.New(t)

	const (
		slowCheck = 200 * time.Millisecond // stands in for an endpoint that never answers
		fastCheck = time.Millisecond
	)

	main := pond.NewPool(32)
	ws := pond.NewPool(
		DefaultWebsocketCheckWorkers,
		pond.WithQueueSize(DefaultWebsocketCheckQueueSize),
		pond.WithNonBlocking(true),
	)
	t.Cleanup(func() { ws.StopAndWait(); main.StopAndWait() })

	var fastDone atomic.Int32

	// The cycle: many fast json_rpc-style jobs, plus websocket jobs dispatched to the
	// isolated pool and deliberately NOT waited on.
	group := main.NewGroup()
	for i := 0; i < 20; i++ {
		group.Submit(func() {
			time.Sleep(fastCheck)
			fastDone.Add(1)
		})
	}
	for i := 0; i < 10; i++ {
		ws.TrySubmit(func() { time.Sleep(slowCheck) })
	}

	start := time.Now()
	c.NoError(group.Wait())
	cycle := time.Since(start)

	c.Equal(int32(20), fastDone.Load(), "every fast check must still run")
	c.Less(cycle, slowCheck,
		"the cycle must finish without waiting on the slow websocket checks; "+
			"if this fails, websocket checks are back on the shared group and one silent "+
			"endpoint can throttle every check in the gateway")
}

// Saturation must SKIP, not queue. A queue of slow checks reintroduces the stall it exists to
// avoid — it just defers it. A skipped check costs one refresh interval.
func Test_websocketCheckPool_SaturationSkipsRatherThanQueues(t *testing.T) {
	c := require.New(t)

	// One worker and a tiny bounded queue. WithQueueSize is what makes refusal possible at
	// all: pond's default queue is Unbounded, so TrySubmit would accept indefinitely and
	// silently rebuild the backlog this pool exists to prevent.
	const queue = 4
	ws := pond.NewPool(1, pond.WithQueueSize(queue), pond.WithNonBlocking(true))

	release := make(chan struct{})
	// Release every blocked task before the pool is torn down, or StopAndWait deadlocks.
	t.Cleanup(func() { close(release); ws.StopAndWait() })

	var started sync.WaitGroup
	started.Add(1)
	_, ok := ws.TrySubmit(func() {
		started.Done()
		<-release
	})
	c.True(ok, "the first submission must be accepted")
	started.Wait() // the single worker is now occupied

	// Fill the bounded queue, then confirm the next submission is REFUSED rather than
	// queued or blocking. The caller here stands in for the health-check cycle.
	for i := 0; i < queue; i++ {
		_, accepted := ws.TrySubmit(func() { <-release })
		c.True(accepted, "queue slot %d should be accepted", i)
	}

	_, accepted := ws.TrySubmit(func() { <-release })
	c.False(accepted,
		"a saturated pool must REFUSE so the cycle skips this endpoint for one interval; "+
			"accepting would rebuild the backlog and stall the cycle later instead of now")
}

// The isolated pool must be bounded. An unbounded one turns a service-wide websocket outage
// into unbounded goroutine growth.
func Test_websocketCheckWorkers_IsBounded(t *testing.T) {
	c := require.New(t)
	c.Positive(DefaultWebsocketCheckWorkers, "the websocket check pool must be bounded")
	c.LessOrEqual(DefaultWebsocketCheckWorkers, 256,
		"sized for isolation, not throughput - a websocket check can block for its whole timeout")
}
