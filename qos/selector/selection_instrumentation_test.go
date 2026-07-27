package selector

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

func candidateCount(t *testing.T, serviceID, domain string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.SelectionCandidateTotal.WithLabelValues(serviceID, domain, metrics.SelectionPathConcentrationCap))
}

func selectedCount(t *testing.T, serviceID, domain string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.SelectionSelectedTotal.WithLabelValues(serviceID, domain, metrics.SelectionPathConcentrationCap))
}

// The no-op path (no operator over the cap) is the majority of real selections and the one
// the reshape-only metric is blind to. If it were not instrumented, a skewed distribution
// would be indistinguishable from a correctly-behaving cap.
func TestSelectionInstrumentation_RecordsOnCapNoOpPath(t *testing.T) {
	const svc = "instr-noop"
	// Two operators, evenly split: no operator exceeds a 0.65 cap, so the cap is a no-op.
	eps := protocol.EndpointAddrList{
		"sup1-https://a.alpha-op.com", "sup2-https://b.alpha-op.com",
		"sup3-https://c.beta-op.com", "sup4-https://d.beta-op.com",
	}

	beforeA := candidateCount(t, svc, "alpha-op.com")
	beforeB := candidateCount(t, svc, "beta-op.com")

	const runs = 50
	for i := 0; i < runs; i++ {
		if got := SelectWithConcentrationCap(svc, eps, 0.65); got == "" {
			t.Fatal("selection returned empty")
		}
	}

	// Both operators are candidates on EVERY selection, so each gains exactly `runs`.
	if got := candidateCount(t, svc, "alpha-op.com") - beforeA; got != runs {
		t.Errorf("alpha candidate count = %v, want %d", got, runs)
	}
	if got := candidateCount(t, svc, "beta-op.com") - beforeB; got != runs {
		t.Errorf("beta candidate count = %v, want %d", got, runs)
	}

	// Wins must sum to the number of selections.
	wins := selectedCount(t, svc, "alpha-op.com") + selectedCount(t, svc, "beta-op.com")
	if wins != runs {
		t.Errorf("selected total = %v, want %d", wins, runs)
	}
}

// A pool that reached the selector already collapsed to one operator is the key diagnosis
// this metric enables: the cap is correctly a no-op, and the skew happened upstream.
// Candidate == selected for that operator is the signature.
func TestSelectionInstrumentation_SingleOperatorPoolIsVisible(t *testing.T) {
	const svc = "instr-single"
	eps := protocol.EndpointAddrList{
		"sup1-https://a.solo-op.com", "sup2-https://b.solo-op.com", "sup3-https://c.solo-op.com",
	}

	beforeC := candidateCount(t, svc, "solo-op.com")
	beforeS := selectedCount(t, svc, "solo-op.com")

	const runs = 20
	for i := 0; i < runs; i++ {
		SelectWithConcentrationCap(svc, eps, 0.65)
	}

	gotC := candidateCount(t, svc, "solo-op.com") - beforeC
	gotS := selectedCount(t, svc, "solo-op.com") - beforeS
	if gotC != runs || gotS != runs {
		t.Errorf("single-operator pool: candidate=%v selected=%v, want %d each", gotC, gotS, runs)
	}
}

// The disabled cap must still be observable — "someone turned the cap off" is otherwise
// indistinguishable from "the cap ran and found nothing to do".
func TestSelectionInstrumentation_RecordsWhenCapDisabled(t *testing.T) {
	const svc = "instr-disabled"
	eps := protocol.EndpointAddrList{
		"sup1-https://a.one-op.com", "sup2-https://b.two-op.com",
	}

	before := candidateCount(t, svc, "one-op.com")
	for i := 0; i < 10; i++ {
		SelectWithConcentrationCap(svc, eps, 0) // 0 = disabled
	}
	if got := candidateCount(t, svc, "one-op.com") - before; got != 10 {
		t.Errorf("disabled-cap path not instrumented: got %v, want 10", got)
	}
}

// The reshape path must record too, so win-rate is comparable across all three paths.
func TestSelectionInstrumentation_RecordsOnReshapePath(t *testing.T) {
	const svc = "instr-reshape"
	// 4 of 5 endpoints on one operator: 0.8 > 0.65, so the cap reshapes.
	eps := protocol.EndpointAddrList{
		"s1-https://a.big-op.com", "s2-https://b.big-op.com",
		"s3-https://c.big-op.com", "s4-https://d.big-op.com",
		"s5-https://e.small-op.com",
	}

	beforeBig := candidateCount(t, svc, "big-op.com")
	beforeSmall := candidateCount(t, svc, "small-op.com")

	const runs = 40
	for i := 0; i < runs; i++ {
		SelectWithConcentrationCap(svc, eps, 0.65)
	}

	if got := candidateCount(t, svc, "big-op.com") - beforeBig; got != runs {
		t.Errorf("big-op candidate = %v, want %d", got, runs)
	}
	if got := candidateCount(t, svc, "small-op.com") - beforeSmall; got != runs {
		t.Errorf("small-op candidate = %v, want %d", got, runs)
	}
	wins := selectedCount(t, svc, "big-op.com") + selectedCount(t, svc, "small-op.com")
	if wins != runs {
		t.Errorf("selected total = %v, want %d", wins, runs)
	}
}

func candidateCountPath(t *testing.T, serviceID, domain, path string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.SelectionCandidateTotal.WithLabelValues(serviceID, domain, path))
}

func selectedCountPath(t *testing.T, serviceID, domain, path string) float64 {
	t.Helper()
	return testutil.ToFloat64(metrics.SelectionSelectedTotal.WithLabelValues(serviceID, domain, path))
}

// SelectEndpointsWithDiversity is the path every HTTP relay takes (via
// SelectMultipleWithArchival), so it is the one whose pool composition explains a skewed
// traffic distribution. Instrumenting only the concentration-cap path left this blind —
// which is why the cap's metrics showed a handful of selections against thousands of relays.
func TestSelectionInstrumentation_DiversityPathIsInstrumented(t *testing.T) {
	const svc = "instr-diversity"
	logger := polyzero.NewLogger()
	// Pool shaped like the real anomaly: one operator holds a small minority of endpoints.
	eps := protocol.EndpointAddrList{
		"s1-https://a.major-op.com", "s2-https://b.major-op.com",
		"s3-https://c.major-op.com", "s4-https://d.major-op.com",
		"s5-https://e.minor-op.com",
	}

	beforeMajor := candidateCountPath(t, svc, "major-op.com", metrics.SelectionPathDiversity)
	beforeMinor := candidateCountPath(t, svc, "minor-op.com", metrics.SelectionPathDiversity)

	const runs = 60
	for i := 0; i < runs; i++ {
		got := SelectEndpointsWithDiversity(logger, svc, eps, 1)
		if len(got) != 1 {
			t.Fatalf("expected 1 endpoint, got %d", len(got))
		}
	}

	// Every operator is a candidate on every selection.
	if got := candidateCountPath(t, svc, "major-op.com", metrics.SelectionPathDiversity) - beforeMajor; got != runs {
		t.Errorf("major-op candidate = %v, want %d", got, runs)
	}
	if got := candidateCountPath(t, svc, "minor-op.com", metrics.SelectionPathDiversity) - beforeMinor; got != runs {
		t.Errorf("minor-op candidate = %v, want %d", got, runs)
	}

	wins := selectedCountPath(t, svc, "major-op.com", metrics.SelectionPathDiversity) +
		selectedCountPath(t, svc, "minor-op.com", metrics.SelectionPathDiversity)
	if wins != runs {
		t.Errorf("selected total = %v, want %d", wins, runs)
	}
}

// The two selectors must not blend into one series: the whole point of the path label is
// that these carry wildly different traffic, and that fact should be queryable.
func TestSelectionInstrumentation_PathsAreDistinguishable(t *testing.T) {
	const svc = "instr-paths"
	logger := polyzero.NewLogger()
	eps := protocol.EndpointAddrList{
		"s1-https://a.split-op.com", "s2-https://b.other-op.com",
	}

	beforeDiv := candidateCountPath(t, svc, "split-op.com", metrics.SelectionPathDiversity)
	beforeCap := candidateCountPath(t, svc, "split-op.com", metrics.SelectionPathConcentrationCap)

	for i := 0; i < 7; i++ {
		SelectEndpointsWithDiversity(logger, svc, eps, 1)
	}
	for i := 0; i < 3; i++ {
		SelectWithConcentrationCap(svc, eps, 0.65)
	}

	if got := candidateCountPath(t, svc, "split-op.com", metrics.SelectionPathDiversity) - beforeDiv; got != 7 {
		t.Errorf("diversity path = %v, want 7", got)
	}
	if got := candidateCountPath(t, svc, "split-op.com", metrics.SelectionPathConcentrationCap) - beforeCap; got != 3 {
		t.Errorf("concentration_cap path = %v, want 3", got)
	}
}
