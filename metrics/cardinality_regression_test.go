package metrics

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

// Regression tests for the 2026-08-12 metric cardinality incident, in which
// path metrics reached 63% of PNF's entire Prometheus TSDB and took it to 89%
// of its memory ceiling.
//
// Every assertion here goes through the PRODUCTION emit helper and reads the
// label values the Prometheus collector actually received. Asserting on
// SanitizeDomainLabel / SanitizeMethodLabel directly would prove only that a
// helper the author wrote returns what the author expected — the exact mistake
// that let three drain bugs ship green (see CLAUDE.md, "Testing Changes That
// Affect Routing"). A sanitizer that is never called on the emit path passes
// its own unit tests perfectly.

// collectLabelValues gathers a CounterVec and returns the set of values seen
// for one label name across every child series.
func collectLabelValues(t *testing.T, c interface {
	Collect(chan<- prometheus.Metric)
}, labelName string,
) map[string]struct{} {
	t.Helper()
	ch := make(chan prometheus.Metric, 1<<16)
	c.Collect(ch)
	close(ch)

	out := map[string]struct{}{}
	for m := range ch {
		var pb dto.Metric
		require.NoError(t, m.Write(&pb))
		for _, lp := range pb.GetLabel() {
			if lp.GetName() == labelName {
				out[lp.GetValue()] = struct{}{}
			}
		}
	}
	return out
}

// Test_ProbationEvent_SupplierAddressNeverBecomesDomain is the core F1
// regression: 4,172 of 4,608 distinct `domain` values in production were raw
// bech32 supplier addresses, making path_probation_events_total 27% of the
// whole TSDB.
//
// The path is subtle and is why the bug was invisible: reputation/selector.go
// guards with `if err != nil || domain == ""`, but ExtractDomainOrHost does not
// FAIL on a bare supplier address — a dotless host takes the
// isPrivateOrInternalDomain branch and is returned verbatim with a nil error.
// The fallback never fires, so the address arrives here looking like a
// successful extraction.
func Test_ProbationEvent_SupplierAddressNeverBecomesDomain(t *testing.T) {
	ProbationEventsTotal.Reset()

	const addr = "pokt1ylsjqcl0yunve78etutw660a327avc26fxrlfr"
	RecordProbationEvent(addr, "json_rpc", "arb-one", ProbationEventEntered)

	got := collectLabelValues(t, ProbationEventsTotal, LabelDomain)
	require.NotContains(t, got, addr,
		"raw supplier address reached the `domain` label; it must collapse to a sentinel")
	require.Contains(t, got, DomainSupplierAddr)
}

// Test_RealDomainsSurviveSanitization is the counterweight: the fix must not
// collapse the 434 legitimate domains it exists to preserve. `domain` carries
// 202 references across our Grafana dashboards — over-collapsing here would be
// indistinguishable, from the dashboard's side, from PNF dropping the label.
func Test_RealDomainsSurviveSanitization(t *testing.T) {
	ProbationEventsTotal.Reset()

	// Includes hosts with a `1` in a bech32-looking position and a dotless
	// internal hostname, both of which must NOT be mistaken for addresses.
	keep := []string{"nodefleet.net", "example.co.uk", "web31.io", "relayminer1", "88.198.50.175"}
	for _, d := range keep {
		RecordProbationEvent(d, "json_rpc", "eth", ProbationEventEntered)
	}

	got := collectLabelValues(t, ProbationEventsTotal, LabelDomain)
	for _, d := range keep {
		require.Contains(t, got, d, "legitimate domain was collapsed by the sanitizer")
	}
}

// Test_ObservationPipeline_AttackerMethodsAreBounded is the F2 regression.
//
// This asserts the property that actually matters and that SanitizeMethodLabel
// alone does NOT provide: an unauthenticated client varying the JSON-RPC method
// or REST path cannot mint unbounded series. The sanitizer normalizes the SHAPE
// of a value but cannot bound the SET — `/aaa`, `/aab`, … are all well-formed
// static route segments and survive it verbatim. Only the guard bounds them.
func Test_ObservationPipeline_AttackerMethodsAreBounded(t *testing.T) {
	ObservationPipeline.Reset()
	// Swap the guard POINTER rather than copying the struct: cardinalityGuard
	// embeds a sync.Map, so assigning through it copies a lock (govet copylocks).
	saved := observationPipelineGuard
	t.Cleanup(func() { observationPipelineGuard = saved })
	observationPipelineGuard = newCardinalityGuard("test_observation_pipeline", 100)

	// Far more distinct methods than the cap, in the shape a scanner produces.
	for i := 0; i < 5000; i++ {
		RecordObservation("nodefleet.net", "json_rpc", "eth", NetworkTypeCosmos,
			SanitizeMethodLabel(NetworkTypeCosmos, fmt.Sprintf("/probe%d/logon.html", i)), "ok")
	}

	got := collectLabelValues(t, ObservationPipeline, LabelMethod)
	require.LessOrEqual(t, len(got), 100,
		"attacker-varied method values were not bounded by the cardinality guard")
}

// Test_ObservationPipeline_InjectionPayloadsCannotPanic covers the payloads PNF
// found live in the TSDB (CRLF header injection, XSS, path traversal,
// template injection). client_golang panics inside WithLabelValues on non-UTF-8
// label values, so a single malformed request reaching an unsanitized label is
// a remote crash, not just untidy data — the 2026-06-15 incident.
func Test_ObservationPipeline_InjectionPayloadsCannotPanic(t *testing.T) {
	ObservationPipeline.Reset()

	payloads := []string{
		"/\r\n\r\n<script>alert(1)</script>",
		"/\r\nSet-Cookie: x=1",
		"/%2e%2e%2f%2e%2e%2f%2e%2e%2fetc/passwd",
		"/..%252f..%252f..%252fetc/passwd",
		"/${13337*31337}",
		"/+CSCOE+/logon.html",
		"/\xff\xfe invalid utf8",
	}
	for _, p := range payloads {
		require.NotPanics(t, func() {
			RecordObservation("\xff\xfe.example", "json_rpc", "cosmoshub", NetworkTypeCosmos,
				SanitizeMethodLabel(NetworkTypeCosmos, p), "ok")
		}, "payload %q panicked on the metrics path", p)
	}
}

// Test_RPCTypeFallback_SupplierLabelDropped is the F3 regression: `supplier`
// carried 3,289 values against the metric's own 9 domains × 12 service_ids,
// producing essentially all of its 201,068 series.
//
// The helper still ACCEPTS a supplier argument so no caller had to change;
// this asserts the value does not reach the collector.
func Test_RPCTypeFallback_SupplierLabelDropped(t *testing.T) {
	RPCTypeFallbackTotal.Reset()

	const supplier = "pokt1vmy9q5ljvs39n78ygwa85t9rsffncf90xqp2lp"
	RecordRPCTypeFallback("example.net", supplier, "cosmoshub", "COMET_BFT", "JSON_RPC")

	require.Empty(t, collectLabelValues(t, RPCTypeFallbackTotal, LabelSupplier),
		"supplier is still being emitted as a label")
}

// =============================================================================
// Follow-up round: F5 (histogram label multiplication) and F6 (label-set churn),
// reported 2026-08-12 22:40Z after 7.6h on the F1/F2/F3 fix.
//
// The first round bounded label VALUE SETS. This round bounds two things a
// sanitizer and a guard both miss:
//   F5 — a label on a HISTOGRAM costs ~12 series per tuple, so a 20× label pair
//        multiplies 12× harder there than on the counter beside it.
//   F6 — a guard caps the LIVE registry; it cannot cap the number of distinct
//        series Prometheus retains when a label's value set rotates over time.
// =============================================================================

// Test_RelayLatency_HistogramLabelsAreTopologyBounded is the F5 regression.
//
// path_relay_latency_seconds_bucket was 341,840 series fleet-wide, 31.6% of the
// entire gateway job and the largest single source of ongoing growth. Its labels
// were domain(13) × rpc_type(3) × service_id(61) × request_type(4) ×
// status_code(5) × reputation_signal(4). The last two contribute nothing that is
// queried from the histogram and multiply it 20× — at ~12 series per tuple.
//
// This asserts through RecordRelay, not on the metric declaration: the histogram
// must not gain a series when only status_code or reputation_signal varies, while
// the counter beside it must.
func Test_RelayLatency_HistogramLabelsAreTopologyBounded(t *testing.T) {
	RelaysTotal.Reset()
	RelayLatency.Reset()

	const (
		domain    = "relay-latency-labels.example"
		rpcType   = "json_rpc"
		serviceID = "eth"
	)

	// Same topology tuple, every combination of the two dropped labels.
	for _, statusCode := range []string{"2xx", "4xx", "5xx"} {
		for _, signal := range []string{SignalOK, "minor_error", "major_error", "critical_error"} {
			RecordRelay(domain, rpcType, serviceID, statusCode, signal, RelayTypeNormal, 0.1)
		}
	}

	require.Equal(t, 1, testutil.CollectAndCount(RelayLatency),
		"histogram must hold ONE tuple: status_code and reputation_signal must not reach it")
	require.Equal(t, 12, testutil.CollectAndCount(RelaysTotal),
		"the counter must keep the full outcome taxonomy (3 status_code × 4 reputation_signal)")

	// The labels the histogram DOES keep must still separate series, or the fix
	// would have flattened the metric into uselessness rather than narrowing it.
	RecordRelay(domain, "websocket", serviceID, "2xx", SignalOK, RelayTypeNormal, 0.1)
	RecordRelay(domain, rpcType, serviceID, "2xx", SignalOK, RelayTypeHedge, 0.1)
	RecordRelay("other-operator.example", rpcType, serviceID, "2xx", SignalOK, RelayTypeNormal, 0.1)
	RecordRelay(domain, rpcType, "poly", "2xx", SignalOK, RelayTypeNormal, 0.1)
	require.Equal(t, 5, testutil.CollectAndCount(RelayLatency),
		"domain, rpc_type, service_id and request_type must each still separate series")

	// Arity assertion: passing the counter's 6 labels to the histogram would panic.
	require.Empty(t, collectLabelValues(t, RelayLatency, LabelStatusCode))
	require.Empty(t, collectLabelValues(t, RelayLatency, LabelReputationSignal))
}

// Test_SupplierLabelIsGone is the F6 regression, and the only test here that is a
// property of the whole package rather than of one metric.
//
// Six metrics carried a raw `supplier` label: 303,309 series in a 10-minute
// window. The supplier set is ~5,200 on chain and grows with the NETWORK, not
// with our traffic, and it rotates every session — so these metrics minted
// multiples of their live count in distinct series every day (measured on one
// pod over 7.7h: supplier_reputation_score 16.5×, qos_filter_rejection 19.4×,
// supplier_signal 9.3×, against a 1.0× control).
//
// A cardinality guard cannot fix that. Two of the six were guarded, honored their
// caps, and were still among the largest series sources in the job.
//
// ⭐ Each metric is populated THROUGH ITS PRODUCTION Record* HELPER first, then
// the registry is walked. That order is load-bearing and was got wrong once here:
// Gather() reports the labels of CHILD SERIES, so a vec with no children reports
// no labels at all. A registry walk on its own passes whatever the label set is —
// the revert check (restore `supplier` on both re-keyed metrics, expect a
// failure) came back green, which is the same class of mistake as asserting on a
// helper's return value instead of the caller's.
//
// The walk is kept as a second net over everything else the suite has populated,
// so a NEW metric that both carries a supplier label and gets exercised anywhere
// in this package fails too.
//
// The exemptions are the metrics where the supplier IS the subject — a specific
// account you have to name to act on it — rather than a way of naming an
// operator. PNF's ask draws exactly this line: aggregate to domain where the
// metric is an AGGREGATE SIGNAL. All three are also tiny in practice, which is
// the corroborating evidence rather than the reason.
func Test_SupplierLabelIsGone(t *testing.T) {
	allowed := map[string]struct{}{
		// 313 series fleet-wide. The allowance it reports is per (supplier,
		// session) — aggregating to domain would destroy the quantity.
		MetricPrefix + "supplier_exhausted_total": {},
		// 0 series in production. Fires when a supplier ACCOUNT has never signed a
		// transaction, so the address is the actionable payload; our dashboard
		// queries it `by(supplier)` for precisely that reason.
		MetricPrefix + "supplier_nil_pubkey_total": {},
		// 0 series in production. Same shape: names the account whose cached pubkey
		// was invalidated or recovered.
		MetricPrefix + "supplier_pubkey_cache_events_total": {},
	}

	// Populate every metric that used to carry `supplier`, via the production
	// helper, so the registry walk below actually has children to inspect. A
	// supplier address is passed wherever the helper still accepts one: if it ever
	// reaches a label again, the walk sees it.
	const leakedSupplier = "pokt1supplierlabelregression"
	RecordSupplierBlacklist("supplier-label-gone.example", leakedSupplier, "eth", BlacklistReasonSignatureError)
	RecordQoSFilterRejection("supplier-label-gone.example", "eth", QoSFilterReasonBlockHeightLag)
	RecordHedgeSupplierOutcome("supplier-label-gone.example", HedgeRoleWinner, 0.1)
	RecordHealthCheck("supplier-label-gone.example", leakedSupplier, "json_rpc", "eth", "block_height", SignalOK)
	RecordRPCTypeFallback("supplier-label-gone.example", leakedSupplier, "eth", "COMET_BFT", "JSON_RPC")
	RecordRelay("supplier-label-gone.example", "json_rpc", "eth", "2xx", SignalOK, RelayTypeNormal, 0.1)

	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)

	var offenders []string
	populated := map[string]struct{}{}
	for _, fam := range families {
		name := fam.GetName()
		if !strings.HasPrefix(name, MetricPrefix) {
			continue
		}
		if len(fam.GetMetric()) > 0 {
			populated[name] = struct{}{}
		}
		if _, ok := allowed[name]; ok {
			continue
		}
		for _, m := range fam.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == LabelSupplier {
					offenders = append(offenders, name)
				}
			}
		}
	}

	require.Empty(t, offenders,
		"these metrics carry a raw `supplier` label; aggregate to `domain` or serve the "+
			"per-supplier question from /ready/<service>?detailed=true")

	// Guard the guard: if a helper above stops emitting, the walk silently stops
	// covering that metric and this test decays into asserting nothing.
	for _, name := range []string{
		MetricPrefix + "supplier_blacklist_total",
		MetricPrefix + "qos_filter_rejection_total",
		MetricPrefix + "hedge_supplier_outcome_total",
		MetricPrefix + "health_check_status_total",
		MetricPrefix + "rpc_type_fallback_total",
		MetricPrefix + "relays_total",
	} {
		require.Containsf(t, populated, name,
			"%s was not populated, so the label walk did not actually inspect it", name)
	}
}

// Test_RemovedSupplierMetricsStayRemoved pins the two deletions.
//
// Both were guarded AND honored their guard AND were still enormous, which is the
// counter-intuitive part worth a test: someone reading only the guard code would
// reasonably conclude they were safe to re-add.
//
// Detected by REGISTRATION COLLISION, not by walking Gather(). Gather() reports
// only families that have at least one child series, so a re-added vec that no
// test happens to populate would be invisible to a registry walk — the metric
// would be back, minting series in production, with this test green. Registering
// a same-named collector answers "is this name taken" regardless of children.
func Test_RemovedSupplierMetricsStayRemoved(t *testing.T) {
	removed := []string{
		MetricPrefix + "supplier_reputation_score", // 4,510 live vs 74,639 distinct/7.7h/pod
		MetricPrefix + "supplier_signal_total",     // 6,523 live vs 60,674 distinct/7.7h/pod
	}

	for _, name := range removed {
		probe := prometheus.NewCounter(prometheus.CounterOpts{
			Name: name,
			Help: "registration probe",
		})
		err := prometheus.DefaultRegisterer.Register(probe)

		var already prometheus.AlreadyRegisteredError
		require.Falsef(t, errors.As(err, &already),
			"%s is registered again; it was removed for unbounded label-set churn (a guard "+
				"cannot bound it — see DefaultSeriesLimit). Per-operator reading is "+
				"path_reputation_mean_score; per-supplier is /ready/<service>?detailed=true", name)
		require.NoErrorf(t, err, "unexpected error probing %s", name)

		// Leave the registry as found, or the probe itself becomes a phantom metric
		// for every later test in this package.
		prometheus.DefaultRegisterer.Unregister(probe)
	}
}
