package metrics

import (
	"hash/fnv"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Admission
// =============================================================================

func TestCardinalityGuard_AdmitsRepeatedTuples(t *testing.T) {
	g := newCardinalityGuard("test", 5)
	for range 100 {
		require.True(t, g.allow("a", "b"))
	}
	require.Equal(t, int64(1), g.count.Load(), "repeated tuples must not inflate count")
}

func TestCardinalityGuard_DropsPastLimit(t *testing.T) {
	g := newCardinalityGuard("test", 3)
	require.True(t, g.allow("svc", "1"))
	require.True(t, g.allow("svc", "2"))
	require.True(t, g.allow("svc", "3"))
	require.False(t, g.allow("svc", "4"), "tuple 4 must be dropped")
	require.False(t, g.allow("svc", "5"), "tuple 5 must be dropped")

	// Previously-seen tuples still admitted.
	require.True(t, g.allow("svc", "1"))
	require.True(t, g.allow("svc", "2"))
	require.Equal(t, int64(3), g.count.Load())
}

func TestCardinalityGuard_NilReceiver(t *testing.T) {
	var g *cardinalityGuard
	require.True(t, g.allow("anything"))
}

func TestCardinalityGuard_Concurrent(t *testing.T) {
	g := newCardinalityGuard("test", 50)

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			g.allow("supplier-"+strconv.Itoa(n), "svc")
		}(i)
	}
	wg.Wait()

	require.LessOrEqual(t, g.count.Load(), int64(50), "count must never exceed limit")
}

// =============================================================================
// Idle eviction
// =============================================================================

// fakeClock is a concurrency-safe test clock. sweepLocked reads it under addMu,
// but the concurrency test advances it from another goroutine.
type fakeClock struct{ nanos atomic.Int64 }

func newFakeClock() *fakeClock {
	c := &fakeClock{}
	c.nanos.Store(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *fakeClock) now() time.Time      { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *fakeClock) add(d time.Duration) { c.nanos.Add(int64(d)) }

// evictionRecorder captures the label tuples a guard asked to delete.
type evictionRecorder struct {
	mu      sync.Mutex
	deleted [][]string
}

func (r *evictionRecorder) deleteSeries(lv []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, append([]string(nil), lv...))
}

func (r *evictionRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deleted)
}

// newTestGuard builds a guard with idle eviction, a fake clock and a recording
// deleter.
func newTestGuard(t *testing.T, limit int64, window time.Duration) (*cardinalityGuard, *fakeClock, *evictionRecorder) {
	t.Helper()
	clock := newFakeClock()
	rec := &evictionRecorder{}
	g := newCardinalityGuard("test", limit).withEviction(window, rec.deleteSeries)
	g.now = clock.now
	return g, clock, rec
}

// seenLen counts the live entries in the seen-set, so tests can assert the
// count/seen invariant directly.
func seenLen(g *cardinalityGuard) int {
	n := 0
	g.seen.Range(func(_, _ any) bool { n++; return true })
	return n
}

// TestCardinalityGuard_EvictsIdleTupleAndDeletesSeries locks the core eviction
// contract: a tuple that goes unobserved for a full generation window is
// forgotten, its slot is returned, and its child series is deleted so the
// registry cannot grow past the cap.
func TestCardinalityGuard_EvictsIdleTupleAndDeletesSeries(t *testing.T) {
	const window = 15 * time.Minute
	g, clock, rec := newTestGuard(t, 10, window)

	require.True(t, g.allow("gone", "eth", "ok"))
	require.Equal(t, int64(1), g.count.Load())

	// First sweep after the window only closes the generation the tuple was
	// admitted in — nothing is evicted yet (eviction age is 1x-2x the window).
	clock.add(window + time.Second)
	g.sweep(false)
	require.Equal(t, int64(1), g.count.Load(), "a tuple must survive its admission window")
	require.Zero(t, rec.count())

	// Second sweep: the tuple missed a full window, so it goes.
	clock.add(window + time.Second)
	g.sweep(false)
	require.Equal(t, int64(0), g.count.Load(), "idle tuple must release its slot")
	require.Equal(t, 0, seenLen(g), "count must stay equal to len(seen)")
	require.Equal(t, [][]string{{"gone", "eth", "ok"}}, rec.deleted,
		"the evicted tuple's series must be deleted, with the exact label values")
}

// TestCardinalityGuard_ActiveTupleSurvivesSweeps is the other half of the
// contract: a tuple that keeps being observed must never be evicted, no matter
// how many sweeps run. A regression here would delete live series and produce a
// counter reset on every sweep.
func TestCardinalityGuard_ActiveTupleSurvivesSweeps(t *testing.T) {
	const window = 15 * time.Minute
	g, clock, rec := newTestGuard(t, 10, window)

	for range 20 {
		require.True(t, g.allow("busy", "eth", "ok"))
		clock.add(window + time.Second)
		g.sweep(false)
	}

	require.Equal(t, int64(1), g.count.Load())
	require.Zero(t, rec.count(), "an actively observed tuple must never be evicted")
}

// TestCardinalityGuard_SaturationRecoversAfterFleetTurnover is the regression
// test for the production defect: admission is first-come, so before eviction a
// saturated pod made every supplier first seen afterwards invisible for the
// pod's entire lifetime — including suppliers that had already left the network
// but still held slots.
func TestCardinalityGuard_SaturationRecoversAfterFleetTurnover(t *testing.T) {
	const window = 15 * time.Minute
	g, clock, rec := newTestGuard(t, 2, window)

	// The "startup fleet" fills every slot.
	require.True(t, g.allow("departed-a", "eth", "ok"))
	require.True(t, g.allow("departed-b", "eth", "ok"))

	// A supplier that joins later is invisible while the slots are held.
	require.False(t, g.allow("newcomer", "eth", "ok"), "guard must be saturated")

	// The startup fleet leaves the network: no further observations for it. One
	// window closes the admission generation, the next reclaims the slots. The
	// slow path itself triggers the sweep, so no janitor is needed.
	clock.add(window + time.Second)
	require.False(t, g.allow("newcomer", "eth", "ok"))
	clock.add(window + time.Second)
	require.True(t, g.allow("newcomer", "eth", "ok"),
		"a departed fleet must not hold slots forever")

	require.Equal(t, 2, rec.count(), "both departed tuples' series must be deleted")
	require.Equal(t, int64(1), g.count.Load())
	require.Equal(t, 1, seenLen(g))
}

// TestCardinalityGuard_EvictedTupleIsReadmitted covers a tuple that returns
// after eviction: it must be admitted again (as a fresh series) rather than
// treated as still-present.
func TestCardinalityGuard_EvictedTupleIsReadmitted(t *testing.T) {
	const window = 15 * time.Minute
	g, clock, rec := newTestGuard(t, 10, window)

	require.True(t, g.allow("flapper", "eth", "ok"))
	clock.add(2*window + time.Second)
	g.sweep(true)
	g.sweep(true)
	require.Equal(t, 1, rec.count())
	require.Equal(t, int64(0), g.count.Load())

	require.True(t, g.allow("flapper", "eth", "ok"), "returning tuple must be re-admitted")
	require.Equal(t, int64(1), g.count.Load())
}

// TestCardinalityGuard_SweepRateLimited asserts the once-per-window rate limit.
// Without it, a saturated guard (which takes the slow path on every dropped
// observation — 4000+/s in production) would sweep the whole seen-set per call.
func TestCardinalityGuard_SweepRateLimited(t *testing.T) {
	const window = 15 * time.Minute
	g, clock, _ := newTestGuard(t, 10, window)

	// Arm the timer, then confirm the generation only advances once per window.
	g.sweep(false)
	require.Equal(t, uint32(0), g.gen.Load(), "the arming sweep must not advance the generation")

	for range 100 {
		g.sweep(false)
	}
	require.Equal(t, uint32(0), g.gen.Load(), "sweeps inside the window must be no-ops")

	clock.add(window + time.Second)
	g.sweep(false)
	require.Equal(t, uint32(1), g.gen.Load())

	for range 100 {
		g.sweep(false)
	}
	require.Equal(t, uint32(1), g.gen.Load(), "still rate-limited after the first real sweep")

	// force bypasses the limiter (used by tests only).
	g.sweep(true)
	require.Equal(t, uint32(2), g.gen.Load())
}

// TestCardinalityGuard_NoEvictionWithoutDeleter locks the safety rule: eviction
// without series deletion would let a re-admitted tuple add a second series
// while holding one slot, so the registry would creep past the cap. A guard with
// no deleter must keep the original saturate-forever behavior instead.
func TestCardinalityGuard_NoEvictionWithoutDeleter(t *testing.T) {
	clock := newFakeClock()
	g := newCardinalityGuard("test", 1)
	g.now = clock.now
	g.evictAfter = 15 * time.Minute // window set, but deleteSeries left nil

	require.True(t, g.allow("held"))
	require.False(t, g.allow("blocked"))

	clock.add(10 * time.Hour)
	g.sweep(true)
	g.sweep(true)

	require.Equal(t, int64(1), g.count.Load(), "no deleter must mean no eviction")
	require.False(t, g.allow("blocked"))
}

// TestCardinalityGuard_EvictionDisabledByZeroWindow covers the other disable
// path (window <= 0), which is how an unconfigured guard behaves.
func TestCardinalityGuard_EvictionDisabledByZeroWindow(t *testing.T) {
	rec := &evictionRecorder{}
	clock := newFakeClock()
	g := newCardinalityGuard("test", 1).withEviction(0, rec.deleteSeries)
	g.now = clock.now

	require.True(t, g.allow("held"))
	clock.add(10 * time.Hour)
	g.sweep(true)
	require.Equal(t, int64(1), g.count.Load())
	require.Zero(t, rec.count())
}

// TestCardinalityGuard_DropAccountingAndOneTimeWarn asserts the two behaviors
// that must survive eviction: every dropped tuple is still counted in
// MetricsLabelDropped, and the trip WARN fires exactly once even if the guard
// saturates, recovers via eviction and saturates again.
func TestCardinalityGuard_DropAccountingAndOneTimeWarn(t *testing.T) {
	const window = 15 * time.Minute
	g, clock, _ := newTestGuard(t, 1, window)
	g.name = "test_drop_accounting" // isolated series in the shared drop counter

	before := testutil.ToFloat64(MetricsLabelDropped.WithLabelValues(g.name))

	require.True(t, g.allow("first"))
	require.False(t, g.allow("second"))
	require.False(t, g.allow("third"))
	require.Equal(t, before+2, testutil.ToFloat64(MetricsLabelDropped.WithLabelValues(g.name)),
		"every dropped tuple must be accounted")
	require.True(t, g.warned, "the trip WARN must be latched on first saturation")

	// Recover through eviction, then saturate again.
	clock.add(2*window + time.Second)
	g.sweep(true)
	g.sweep(true)
	require.Equal(t, int64(0), g.count.Load())

	require.True(t, g.allow("fourth"))
	require.False(t, g.allow("fifth"))
	require.Equal(t, before+3, testutil.ToFloat64(MetricsLabelDropped.WithLabelValues(g.name)))
	require.True(t, g.warned, "the WARN latch must never be re-armed")
}

// TestCardinalityGuard_EvictionMetricCounts checks the new eviction counter,
// which is what distinguishes "the live tuple count exceeds the cap" from "the
// guard is churning at the boundary".
func TestCardinalityGuard_EvictionMetricCounts(t *testing.T) {
	const window = 15 * time.Minute
	g, clock, _ := newTestGuard(t, 10, window)
	g.name = "test_eviction_metric"

	before := testutil.ToFloat64(MetricsLabelEvicted.WithLabelValues(g.name))
	for i := range 4 {
		require.True(t, g.allow("supplier-"+strconv.Itoa(i)))
	}
	clock.add(2*window + time.Second)
	g.sweep(true)
	g.sweep(true)

	require.Equal(t, before+4, testutil.ToFloat64(MetricsLabelEvicted.WithLabelValues(g.name)))
}

// TestCardinalityGuard_CountTracksSeenUnderConcurrentSweeps hammers the
// invariant that makes the cap meaningful: count == len(seen) and count <= limit
// while admissions, fast-path touches and sweeps all race.
func TestCardinalityGuard_CountTracksSeenUnderConcurrentSweeps(t *testing.T) {
	const window = time.Second
	g, clock, _ := newTestGuard(t, 64, window)

	stop := make(chan struct{})
	var sweeper, writers sync.WaitGroup

	// Sweeper: advances the clock and sweeps continuously.
	sweeper.Add(1)
	go func() {
		defer sweeper.Done()
		for {
			select {
			case <-stop:
				return
			default:
				clock.add(window + time.Millisecond)
				g.sweep(false)
			}
		}
	}()

	// Writers: a mix of novel tuples (slow path) and repeats (fast-path touch).
	for w := range 8 {
		writers.Add(1)
		go func(w int) {
			defer writers.Done()
			for i := range 500 {
				g.allow("supplier-"+strconv.Itoa(w*500+i), "eth")
				g.allow("hot-supplier", "eth")
			}
		}(w)
	}

	writers.Wait()
	close(stop)
	sweeper.Wait()

	require.LessOrEqual(t, g.count.Load(), int64(64), "count must never exceed the limit")
	require.GreaterOrEqual(t, g.count.Load(), int64(0), "count must never go negative")
	require.Equal(t, int(g.count.Load()), seenLen(g), "count must equal len(seen)")
}

// TestCardinalityGuard_FastPathIsAllocationFree locks the hot-path contract for
// the liveness stamp added by eviction: an already-seen tuple must still take a
// lock-free, allocation-cheap path (an atomic load, and one atomic store per
// generation). Measured at 0 allocs/op; the ceiling is 1 so a change in Go's
// escape analysis for the sync.Map key does not fail the build.
func TestCardinalityGuard_FastPathIsAllocationFree(t *testing.T) {
	g := newCardinalityGuard("test", 10)
	require.True(t, g.allow("pokt1supplier", "eth", "ok"))

	allocs := testing.AllocsPerRun(500, func() { boolSink = g.allow("pokt1supplier", "eth", "ok") })
	require.LessOrEqualf(t, allocs, 1.0, "fast path must stay allocation-cheap, got %v allocs/op", allocs)
}

// =============================================================================
// Hashing
// =============================================================================

var (
	hashSink uint64
	boolSink bool
)

// TestHashLabelValues_MatchesFNV pins the hand-rolled hash to the hash/fnv
// digest it replaced, so the allocation optimization cannot silently change
// tuple identity.
func TestHashLabelValues_MatchesFNV(t *testing.T) {
	reference := func(labelValues []string) uint64 {
		h := fnv.New64a()
		for _, v := range labelValues {
			_, _ = h.Write([]byte(v))
			_, _ = h.Write([]byte{0})
		}
		return h.Sum64()
	}

	for _, tuple := range [][]string{
		{},
		{""},
		{"", ""},
		{"pokt1abc", "eth", "ok"},
		{"pokt1abc", "eth", "error"},
		{"a", "bc"},
		{"ab", "c"}, // separator must keep these distinct
		{"domain.example", "json_rpc", "eth", "block_height", "critical_error"},
	} {
		require.Equalf(t, reference(tuple), hashLabelValues(tuple), "tuple %q", tuple)
	}

	require.NotEqual(t, hashLabelValues([]string{"a", "bc"}), hashLabelValues([]string{"ab", "c"}))
}

// TestHashLabelValues_NoAllocations keeps the hot path allocation-free: this
// runs once per recorded signal (thousands per second in production).
func TestHashLabelValues_NoAllocations(t *testing.T) {
	tuple := []string{"pokt1supplier", "eth", "ok"}
	allocs := testing.AllocsPerRun(200, func() { hashSink = hashLabelValues(tuple) })
	require.Zero(t, allocs, "hashLabelValues must not allocate")
}

// =============================================================================
// Limit configuration
// =============================================================================

func TestSeriesLimitFromEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
		val  string
		want int64
	}{
		{name: "unset", want: DefaultSeriesLimit},
		{name: "empty", set: true, val: "", want: DefaultSeriesLimit},
		{name: "whitespace", set: true, val: "   ", want: DefaultSeriesLimit},
		{name: "override", set: true, val: "100000", want: 100_000},
		{name: "padded", set: true, val: " 50 ", want: 50},
		{name: "zero rejected", set: true, val: "0", want: DefaultSeriesLimit},
		{name: "negative rejected", set: true, val: "-5", want: DefaultSeriesLimit},
		{name: "garbage rejected", set: true, val: "lots", want: DefaultSeriesLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(seriesLimitEnvVar, tc.val)
			}
			require.Equal(t, tc.want, seriesLimitFromEnv())
		})
	}
}

// TestPackageGuards_AllEvictable guards the wiring: a package guard without a
// deleter would saturate permanently again, silently.
func TestPackageGuards_AllEvictable(t *testing.T) {
	guards := packageGuards()
	require.NotEmpty(t, guards)
	for _, g := range guards {
		require.NotNilf(t, g.deleteSeries, "guard %q has no series deleter, so it can never evict", g.name)
		require.Positivef(t, g.evictAfter, "guard %q has no idle window", g.name)
		require.Equalf(t, defaultSeriesLimit, g.limit, "guard %q does not use the configured limit", g.name)
	}
}
