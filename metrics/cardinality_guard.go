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
// Sized to cover ~10× the realistic active series for the per-supplier
// metrics introduced alongside this guard:
//
//	~1000 active suppliers × ~70 services × small fan-out (signal_type, role,
//	reason) ≈ 70K active series in the worst case.
//
// 100K leaves headroom without letting a runaway label leak burn unbounded
// memory in the Prometheus client.
const DefaultSeriesLimit = 100_000

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
