package metrics

import (
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
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
