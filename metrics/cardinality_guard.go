package metrics

import (
	"hash/fnv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// DefaultSeriesLimit caps the number of distinct label tuples a guarded metric
// will accept before dropping further new combinations. The first observation
// of any tuple under the limit is permitted; subsequent observations of the
// same tuple are always permitted. Past the limit, novel tuples are dropped
// and counted in MetricsLabelDropped.
//
// IMPORTANT: this is a tuple cap, NOT a series cap. Histogram metrics emit
// ~12 series per tuple (one per bucket plus _sum and _count), so the
// effective series cap for a histogram is roughly 12× the tuple cap. A 100K
// tuple cap on a histogram created 945K series in production (audit
// 2026-04-28), exhausting the Prometheus client heap. When wiring the guard
// to a new histogram, either pass a tighter per-metric limit or design the
// metric's labels so the realistic tuple count is well under the cap.
//
// 25K tuples = ~25K series for counters / ~300K series for histograms. Sized
// to cover realistic active per-supplier workloads (~1000 suppliers × ~5
// active services × small fan-out) without exposing the heap to a runaway
// label leak.
const DefaultSeriesLimit = 25_000

// MetricsLabelDropped counts label tuples that were dropped by a cardinality
// guard. The `metric` label identifies which guarded metric breached its cap.
//
// Self-referentially bounded: there are only a handful of guarded metrics, so
// this CounterVec's own cardinality is naturally tiny (≤ N guarded metrics).
var MetricsLabelDropped = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "metrics_label_dropped_total",
		Help: "Number of label tuples dropped by per-metric cardinality guards. metric=name of the guarded metric.",
	},
	[]string{"metric"},
)

// cardinalityGuard is a per-metric label-tuple counter. allow() reports
// whether a given label tuple should be admitted to the underlying metric.
// Tuples seen before are always admitted via a lock-free fast path; novel
// tuples take a mutex to serialize the cap check with the seen-set update.
//
// The slow path is rare in steady state — once a workload's active suppliers
// are all in the seen-set, every call returns from the fast path.
type cardinalityGuard struct {
	name  string
	limit int64
	seen  sync.Map
	count atomic.Int64
	addMu sync.Mutex
}

func newCardinalityGuard(name string, limit int64) *cardinalityGuard {
	return &cardinalityGuard{name: name, limit: limit}
}

// allow returns true if the label tuple is admitted into the underlying metric.
// The tuple identity is hashed; collisions are extremely improbable at the
// scales that matter (FNV-64 on a small label set), and a collision merely
// admits one extra tuple — it does not under-count drops.
func (g *cardinalityGuard) allow(labelValues ...string) bool {
	if g == nil {
		return true
	}
	h := hashLabelValues(labelValues)
	if _, ok := g.seen.Load(h); ok {
		return true
	}

	g.addMu.Lock()
	defer g.addMu.Unlock()
	if _, ok := g.seen.Load(h); ok {
		return true
	}
	if g.count.Load() >= g.limit {
		MetricsLabelDropped.WithLabelValues(g.name).Inc()
		return false
	}
	g.seen.Store(h, struct{}{})
	g.count.Add(1)
	return true
}

func hashLabelValues(labelValues []string) uint64 {
	h := fnv.New64a()
	for _, v := range labelValues {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// Guards for per-supplier metrics introduced in the metrics audit. Sized to
// the same DefaultSeriesLimit; can be tuned per-metric later if any one
// dominates real-world series counts.
var (
	supplierSignalGuard     = newCardinalityGuard("supplier_signal_total", DefaultSeriesLimit)
	supplierReputationGuard = newCardinalityGuard("supplier_reputation_score", DefaultSeriesLimit)
	hedgeSupplierGuard      = newCardinalityGuard("hedge_supplier_latency_seconds", DefaultSeriesLimit)
	qosFilterRejectionGuard = newCardinalityGuard("qos_filter_rejection_total", DefaultSeriesLimit)
)
