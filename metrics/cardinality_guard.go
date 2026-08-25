package metrics

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
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
// Every currently guarded metric is a counter or a gauge (1 series per tuple)
// AND is guarded on its full label tuple, so tuple cap == series cap for all
// of them. Keep that property when wiring a new guard: eviction deletes the
// child series for exactly the tuple it evicted, so a guard key coarser than
// the metric's label set would leave series behind and let the registry grow
// past the cap.
//
// 25K tuples = ~25K series for counters / ~300K series for histograms. Sized
// to cover realistic active per-supplier workloads (~1000 suppliers × ~5
// active services × small fan-out) without exposing the heap to a runaway
// label leak.
//
// ⭐ WHAT THIS CANNOT BOUND, and why no cap value fixes it (measured 2026-08-12)
//
// A guard bounds the number of label tuples LIVE IN THIS PROCESS'S REGISTRY. It
// does NOT bound the number of distinct series the scraping Prometheus has to
// store, and those two numbers diverge without limit when a label's value set
// rotates over time.
//
// path_supplier_signal_total was guarded, honored its guard at ~26% of the cap
// (6,523 live tuples on one pod), and was still one of the two largest sources of
// series in the entire gateway job: 60,674 DISTINCT series over that pod's 7.7h
// life. path_supplier_reputation_score was worse — 4,510 live, 74,639 distinct,
// 16.5×, ~232K series/pod/day, each retained for the full 6-day window. The
// control is path_relay_latency_seconds_bucket at exactly 1.0×: its labels are
// persistent, so its registry count and its TSDB cost are the same number.
//
// Eviction is NOT the cause and removing it would NOT help: evicting a tuple and
// later re-admitting it recreates the SAME label set, hence the same Prometheus
// series with a gap in it, never a new one. The distinct-series count is
// identical with or without eviction — eviction only decides whether the cost
// lands on this pod's heap as well.
//
// So a metric can sit at 20% of its cap forever and still be the most expensive
// thing in the TSDB. The only thing that bounds the series stream is the size of
// each label's VALUE SET. Concretely, when adding a label:
//
//   - Bounded by our config (service_id, rpc_type, reason, status class, role):
//     safe. The set is fixed at deploy time.
//   - Bounded by the operator set (domain / eTLD+1): safe. 13 values measured
//     fleet-wide, and it grows only when a new operator joins.
//   - Bounded by the CHAIN (supplier address): NOT safe at any cap value. ~5,200
//     suppliers and growing with the network, rotating in and out of sessions
//     every ~20 blocks. Aggregate to domain, or serve the per-supplier question
//     from /ready/<service>?detailed=true, which is a point lookup rather than a
//     retained timeseries.
//   - Client-controlled (method, path): only a guard makes this survivable, and
//     then only because the cap converts unbounded minting into a bounded cost
//     plus a WARN. See observationPipelineGuard.
//
// Diagnostic, per pod (see the PNF follow-up report for the full recipe):
//
//	count(count_over_time(<metric>{pod="<pod>"}[10m]))   # live
//	count(count_over_time(<metric>{pod="<pod>"}[8h]))    # distinct over 8h
//
// A ratio above ~1.5 means the label set rotates and the metric costs multiples
// of what its instantaneous count suggests.
const DefaultSeriesLimit = 25_000

// seriesLimitEnvVar overrides DefaultSeriesLimit at process start.
//
// Rationale for the knob: a saturated guard is an operational problem
// (per-supplier data becomes an arbitrary subset of the fleet) whose only
// correct remedy is either fewer labels or a bigger cap. Without this, raising
// the cap needs a code change and a redeploy — during an incident where
// path_metrics_label_dropped_total is already in the tens of millions. Default
// behavior is unchanged when the variable is unset or unparseable.
const seriesLimitEnvVar = "PATH_METRICS_SERIES_LIMIT"

// defaultSeriesLimit is the effective per-guard cap: DefaultSeriesLimit unless
// overridden via seriesLimitEnvVar. Read once at package init.
var defaultSeriesLimit = seriesLimitFromEnv()

// rejectedSeriesLimit holds the raw value of an explicitly-set seriesLimitEnvVar
// that could not be used, so the fallback can be reported rather than being
// silently identical to not setting it at all. Package init has no logger yet,
// so InitCardinalityGuards surfaces it: someone raising the cap mid-incident
// must not be left believing a typo took effect.
var rejectedSeriesLimit string

func seriesLimitFromEnv() int64 {
	raw := strings.TrimSpace(os.Getenv(seriesLimitEnvVar))
	if raw == "" {
		return DefaultSeriesLimit
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		rejectedSeriesLimit = raw
		return DefaultSeriesLimit
	}
	return n
}

// defaultGuardIdleWindow is the liveness window used for idle eviction: a label
// tuple that goes unobserved for a full window becomes eligible for eviction,
// so the actual eviction age of a tuple is between 1× and 2× this value.
//
// Sized above a Shannon session (20 blocks) so a supplier that is merely idle
// between sessions keeps its slot, while a supplier that has left the network
// releases its slot — and its now-permanently-flat series — within ~30 minutes.
//
// Trade-off: an evicted tuple that later reappears is re-admitted as a FRESH
// child series starting at zero, which Prometheus reads as a counter reset. It
// is only evicted after a full window with no observations, so no increase is
// lost from any rate() window shorter than the eviction age.
const defaultGuardIdleWindow = 15 * time.Minute

// guardSweepTick is how often the janitor asks each guard to sweep. The guard
// itself rate-limits to one real sweep per idle window, so this only controls
// how promptly a due sweep runs; it does not control the eviction age.
const guardSweepTick = time.Minute

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

// MetricsLabelEvicted counts label tuples reclaimed by a cardinality guard
// because they went unobserved for a full idle window (their child series is
// deleted at the same time). Pairs with MetricsLabelDropped: sustained drops
// alongside near-zero evictions means the live tuple count genuinely exceeds
// the cap (raise PATH_METRICS_SERIES_LIMIT or cut a label), whereas drops
// alongside heavy evictions means the guard is churning at the boundary.
//
// Self-referentially bounded, same as MetricsLabelDropped.
var MetricsLabelEvicted = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: MetricPrefix + "metrics_label_evicted_total",
		Help: "Number of label tuples evicted (and their series deleted) by per-metric cardinality guards after going unobserved for a full idle window. metric=name of the guarded metric.",
	},
	[]string{"metric"},
)

// guardLogger emits a one-time WARN when a cardinality guard first trips. Set
// once at startup via InitCardinalityGuards, before any traffic; read-only
// afterwards, so a plain package var is safe (no concurrent writer). Nil until
// set — guards simply skip the WARN in that window (they cannot trip before a
// pod serves traffic anyway).
var guardLogger polylog.Logger

// janitorOnce ensures the sweep janitor is started at most once.
var janitorOnce sync.Once

// InitCardinalityGuards brings the cardinality guards up: it wires the logger
// used for the one-time guard-tripped WARN, reports an unusable series-limit
// override, and starts the guard sweep janitor. Call once during metrics startup.
//
// The janitor lives for the process lifetime (one goroutine, one ticker) and is
// deliberately started here rather than at package init so that unit tests,
// which never call this, run with no background sweeper and fully deterministic
// guards.
func InitCardinalityGuards(l polylog.Logger) {
	guardLogger = l

	// Package init had no logger to complain with, so an override that could not
	// be parsed is reported here instead of behaving exactly like an unset one.
	if rejectedSeriesLimit != "" && l != nil {
		l.Warn().
			Str("env_var", seriesLimitEnvVar).
			Str("ignored_value", rejectedSeriesLimit).
			Int64("using_limit", defaultSeriesLimit).
			Msg("cardinality series-limit override is not a positive integer and was ignored; the default limit is in effect")
	}

	janitorOnce.Do(func() { go sweepGuardsForever() })
}

// packageGuards is the fixed set of guards the janitor sweeps. Deliberately a
// literal rather than a registry populated by newCardinalityGuard: test-local
// guards must not end up on a process-wide list.
func packageGuards() []*cardinalityGuard {
	return []*cardinalityGuard{
		hedgeSupplierGuard,
		qosFilterRejectionGuard,
		healthCheckStatusGuard,
		probationEventsGuard,
		observationPipelineGuard,
		circuitBreakerEventsGuard,
		circuitBreakerOutcomeGuard,
		rpcTypeFallbackGuard,
	}
}

func sweepGuardsForever() {
	ticker := time.NewTicker(guardSweepTick)
	defer ticker.Stop()
	for range ticker.C {
		for _, g := range packageGuards() {
			g.sweep(false)
		}
	}
}

// guardEntry is the per-tuple state a guard keeps for an admitted label tuple.
//
// lastGen is the liveness generation in which the tuple was last observed. The
// hot path stores into it at most once per generation per tuple (an atomic load
// plus, rarely, an atomic store) — no lock, no allocation.
//
// labels is the tuple's label values, retained so eviction can delete exactly
// the child series this tuple created. Copied once, on admission, on the slow
// path.
type guardEntry struct {
	lastGen atomic.Uint32
	labels  []string
}

// cardinalityGuard is a per-metric label-tuple counter. allow() reports
// whether a given label tuple should be admitted to the underlying metric.
// Tuples seen before are always admitted via a lock-free fast path; novel
// tuples take a mutex to serialize the cap check with the seen-set update.
//
// The slow path is rare in steady state — once a workload's active suppliers
// are all in the seen-set, every call returns from the fast path.
//
// Admission is first-come, so without eviction a saturated guard would freeze
// its represented subset to whichever tuples happened to arrive first, for the
// pod's entire lifetime — including suppliers that have since left the network
// but still hold slots (observed in production: 175M dropped tuples on an
// 11-hour-old pod, whose per-supplier data was therefore an arbitrary subset
// rather than a sample). Idle eviction fixes that: a tuple unobserved for a
// full generation window is dropped from the seen-set, its slot is returned to
// the pool, and its child series is deleted from the collector so the registry
// never grows past the cap.
type cardinalityGuard struct {
	name  string
	limit int64

	// evictAfter is the liveness window for idle eviction. Zero disables
	// eviction (guard degrades to the original saturate-forever behavior).
	evictAfter time.Duration

	// deleteSeries removes the child series for an evicted label tuple from the
	// underlying collector. Eviction REQUIRES it: forgetting a tuple without
	// deleting its series would let a re-admitted tuple create a second series
	// while occupying one slot, and the registry would creep past the cap —
	// exactly the failure the guard exists to prevent. A nil deleter therefore
	// disables eviction rather than breaking the cap.
	deleteSeries func(labelValues []string)

	// now is the clock; a field so tests can drive sweeps deterministically.
	now func() time.Time

	// seen maps label-tuple hash -> *guardEntry. Mutated (Store/Delete) only
	// while holding addMu, which keeps count exactly equal to len(seen).
	seen  sync.Map
	count atomic.Int64

	// gen is the current liveness generation, bumped at the end of every sweep.
	gen atomic.Uint32

	addMu     sync.Mutex
	warned    bool      // guarded by addMu; ensures the trip WARN fires exactly once
	nextSweep time.Time // guarded by addMu; earliest time the next sweep may run
}

func newCardinalityGuard(name string, limit int64) *cardinalityGuard {
	return &cardinalityGuard{name: name, limit: limit, now: time.Now}
}

// withEviction enables idle eviction on the guard. deleteSeries receives the
// evicted tuple's label values in the same order they were passed to allow(),
// which must be the same order the metric declares its labels in.
func (g *cardinalityGuard) withEviction(window time.Duration, deleteSeries func(labelValues []string)) *cardinalityGuard {
	g.evictAfter = window
	g.deleteSeries = deleteSeries
	return g
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
	if v, ok := g.seen.Load(h); ok {
		g.touch(v.(*guardEntry))
		return true
	}

	g.addMu.Lock()
	defer g.addMu.Unlock()
	if v, ok := g.seen.Load(h); ok {
		g.touch(v.(*guardEntry))
		return true
	}
	// Reclaim idle slots before deciding to drop, so a guard that is only
	// saturated by departed tuples admits this one instead of rejecting it.
	// Internally rate-limited to one sweep per idle window.
	g.sweepLocked(false)
	if g.count.Load() >= g.limit {
		// Fire a single WARN the first time this guard saturates. Past this
		// point the metric silently stops counting novel tuples and looks
		// identical to a healthy one on the wire; the log (and the
		// path_metrics_label_dropped_total counter) are the only signals that
		// it has gone incomplete.
		if !g.warned {
			g.warned = true
			if guardLogger != nil {
				guardLogger.Warn().
					Str("metric", g.name).
					Int64("series_limit", g.limit).
					Str("limit_env_var", seriesLimitEnvVar).
					Msg("cardinality guard tripped: metric is now incomplete, novel label tuples are being dropped. Idle tuples are evicted every ~15m, so this can recover on its own; if path_metrics_label_dropped_total{metric} stays high while path_metrics_label_evicted_total{metric} stays low, the live tuple count exceeds the cap — raise PATH_METRICS_SERIES_LIMIT or drop a label.")
			}
		}
		MetricsLabelDropped.WithLabelValues(g.name).Inc()
		return false
	}

	e := &guardEntry{labels: append(make([]string, 0, len(labelValues)), labelValues...)}
	e.lastGen.Store(g.gen.Load())
	g.seen.Store(h, e)
	g.count.Add(1)
	return true
}

// touch marks a tuple as live in the current generation. Lock-free and
// allocation-free: one atomic load, plus one atomic store the first time the
// tuple is seen in a new generation.
func (g *cardinalityGuard) touch(e *guardEntry) {
	if gen := g.gen.Load(); e.lastGen.Load() != gen {
		e.lastGen.Store(gen)
	}
}

// sweep evicts tuples that were not observed during the current generation and
// then opens a new generation. force skips the once-per-window rate limit.
func (g *cardinalityGuard) sweep(force bool) {
	if g == nil {
		return
	}
	g.addMu.Lock()
	defer g.addMu.Unlock()
	g.sweepLocked(force)
}

// sweepLocked implements sweep; addMu must be held.
//
// Eviction policy is a two-window CLOCK approximation: entries carry the
// generation they were last observed in, a sweep evicts everything still
// carrying a generation older than the current one, then bumps the generation.
// A tuple therefore has to miss a full window to be evicted, which makes the
// eviction age 1×–2× evictAfter and costs the hot path only a generation stamp.
func (g *cardinalityGuard) sweepLocked(force bool) {
	// No eviction configured, or no way to delete the series it would orphan.
	if g.evictAfter <= 0 || g.deleteSeries == nil {
		return
	}

	now := g.now()
	if !force {
		// First slow-path call just arms the timer: sweeping immediately would
		// evict tuples that have had no chance to be observed twice.
		if g.nextSweep.IsZero() {
			g.nextSweep = now.Add(g.evictAfter)
			return
		}
		if now.Before(g.nextSweep) {
			return
		}
	}
	g.nextSweep = now.Add(g.evictAfter)

	// Entries stamped with the current generation were observed within this
	// window. gen only ever increases and is bumped after this Range completes,
	// so anything not equal to cur is strictly older, i.e. idle.
	cur := g.gen.Load()
	var evicted int64
	g.seen.Range(func(k, v any) bool {
		e := v.(*guardEntry)
		if e.lastGen.Load() == cur {
			return true
		}
		// A concurrent fast-path touch can mark this entry live between the
		// check and the delete. The worst case is one extra series churn: the
		// next observation takes the slow path and re-admits the tuple. It
		// cannot desync count from seen, because every seen mutation is made
		// under addMu.
		g.seen.Delete(k)
		g.deleteSeries(e.labels)
		evicted++
		return true
	})
	if evicted > 0 {
		g.count.Add(-evicted)
		MetricsLabelEvicted.WithLabelValues(g.name).Add(float64(evicted))
	}
	g.gen.Add(1)
}

// hashLabelValues hashes a label tuple with FNV-1a over the raw bytes of each
// value, separated by a 0 byte.
//
// Written out by hand rather than using hash/fnv: the constructor there boxes
// the state into a hash.Hash64 interface and every Write takes a []byte(string)
// conversion the compiler cannot prove non-escaping, which put multiple
// allocations on a path taken once per recorded signal.
func hashLabelValues(labelValues []string) uint64 {
	const (
		fnvOffset64 = uint64(14695981039346656037)
		fnvPrime64  = uint64(1099511628211)
	)
	h := fnvOffset64
	for _, v := range labelValues {
		for i := 0; i < len(v); i++ {
			h ^= uint64(v[i])
			h *= fnvPrime64
		}
		// Separator byte (0): XOR is a no-op, the multiply still mixes. Keeps
		// the digest identical to hash/fnv writing a trailing 0 per value.
		h *= fnvPrime64
	}
	return h
}

// Guards for per-supplier metrics introduced in the metrics audit. Each is
// keyed on its metric's FULL label tuple, in declaration order, so eviction can
// delete precisely the series it reclaims (see DefaultSeriesLimit).
var (
	// supplierSignalGuard and supplierReputationGuard were removed along with
	// path_supplier_signal_total and path_supplier_reputation_score (2026-08-12).
	// Both metrics honored their cap and were still among the largest series
	// sources in the gateway job — see the finding recorded on
	// DefaultSeriesLimit. Their remaining two peers below are now keyed on
	// `domain` rather than `supplier`, so they are backstops rather than working
	// caps.

	hedgeSupplierGuard = newCardinalityGuard("hedge_supplier_latency_seconds", defaultSeriesLimit).
				withEviction(defaultGuardIdleWindow, func(lv []string) {
			HedgeSupplierOutcomeTotal.DeleteLabelValues(lv...)
		})

	qosFilterRejectionGuard = newCardinalityGuard("qos_filter_rejection_total", defaultSeriesLimit).
				withEviction(defaultGuardIdleWindow, func(lv []string) {
			QoSFilterRejectionTotal.DeleteLabelValues(lv...)
		})

	// healthCheckStatusGuard is a backstop, not a working cap: after dropping
	// the `supplier` label the realistic tuple count for
	// path_health_check_status_total is domain × rpc_type × service_id ×
	// health_check_name × reputation_signal, well under the cap. It exists so a
	// label leak (e.g. suppliers registering bare IPs, which become distinct
	// `domain` values) cannot reproduce the 200K+ series this metric carried in
	// production while completely unguarded.
	healthCheckStatusGuard = newCardinalityGuard("health_check_status_total", defaultSeriesLimit).
				withEviction(defaultGuardIdleWindow, func(lv []string) {
			HealthCheckStatus.DeleteLabelValues(lv...)
		})

	// Guards added after the 2026-08-12 cardinality regression, in which these
	// four metrics contributed ~3.0M series (63% of PNF's entire TSDB) and took
	// Prometheus to 89% of its memory ceiling. All four shipped unguarded.
	//
	// Two distinct label-source defects fed them, both fixed separately
	// (SanitizeDomainLabel, SanitizeMethodLabel). These guards exist so that
	// neither fix is load-bearing: a future label source that leaks unbounded
	// values costs a capped number of series and a WARN, not a monitoring
	// outage. That is the actual lesson of the regression — the sanitizers are
	// hygiene, the guard is the bound.

	// probationEventsGuard — 2,354,499 series in production (92× growth, 27% of
	// the TSDB) because `domain` carried raw supplier addresses. Realistic tuple
	// count post-fix is domain × rpc_type × service_id × event, far under cap.
	probationEventsGuard = newCardinalityGuard("probation_events_total", defaultSeriesLimit).
				withEviction(defaultGuardIdleWindow, func(lv []string) {
			ProbationEventsTotal.DeleteLabelValues(lv...)
		})

	// observationPipelineGuard — the ONLY hard bound on the `method` label, and
	// the only one of these four guards that is load-bearing rather than a
	// backstop. `method` is attacker-controlled: it carries the JSON-RPC method
	// name or the REST URL path, so any unauthenticated client can mint a fresh
	// series per request by varying it. SanitizeMethodLabel normalizes the
	// SHAPE of a value but cannot bound the SET — `/aaa`, `/aab`, … all survive
	// as legitimate-looking static route segments. Only this cap converts a
	// remote resource-exhaustion vector into a bounded cost plus a WARN.
	observationPipelineGuard = newCardinalityGuard("observation_pipeline_total", defaultSeriesLimit).
					withEviction(defaultGuardIdleWindow, func(lv []string) {
			ObservationPipeline.DeleteLabelValues(lv...)
		})

	// circuitBreakerEventsGuard — 233,269 series (49× growth).
	circuitBreakerEventsGuard = newCardinalityGuard("circuit_breaker_events_total", defaultSeriesLimit).
					withEviction(defaultGuardIdleWindow, func(lv []string) {
			DomainCircuitBreakerEventsTotal.DeleteLabelValues(lv...)
		})

	// circuitBreakerOutcomeGuard — same service_id x domain pair as its sibling above, minus
	// reason_category x event. Guarded on the same principle, not on an observed incident.
	circuitBreakerOutcomeGuard = newCardinalityGuard("circuit_breaker_outcome_total", defaultSeriesLimit).
					withEviction(defaultGuardIdleWindow, func(lv []string) {
			CircuitBreakerOutcomeTotal.DeleteLabelValues(lv...)
		})

	// rpcTypeFallbackGuard — backstop only. The real fix was dropping the
	// `supplier` label (3,289 values doing essentially all of the metric's
	// 201,068-series multiplication against 9 domains × 12 service_ids).
	rpcTypeFallbackGuard = newCardinalityGuard("rpc_type_fallback_total", defaultSeriesLimit).
				withEviction(defaultGuardIdleWindow, func(lv []string) {
			RPCTypeFallbackTotal.DeleteLabelValues(lv...)
		})
)
