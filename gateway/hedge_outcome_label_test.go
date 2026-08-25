package gateway

import (
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

// Test_HedgeOutcome_KeysOnDomain asserts the CALL SITE, through recordWinner and
// recordLoser, not through the metric helper.
//
// path_hedge_supplier_outcome_total was re-keyed from `supplier` to `domain` on
// 2026-08-12 (49,231 series fleet-wide, 1.8× churn, scaling with the chain's
// ~5,200-supplier set rather than with our traffic). hedgeResult carries BOTH a
// supplierAddr and an endpointAddr, both strings, so passing the wrong one
// compiles and the label keeps its name — the metric-side test cannot see it.
//
// The distinguishing observable is the label VALUE: SanitizeDomainLabel collapses
// a bech32 address to DomainSupplierAddr, so a call site handing over
// supplierAddr shows up as the sentinel rather than as the operator.
func Test_HedgeOutcome_KeysOnDomain(t *testing.T) {
	metrics.HedgeSupplierOutcomeTotal.Reset()

	const (
		supplierA = "pokt1ylsjqcl0yunve78etutw660a327avc26fxrlfr"
		supplierB = "pokt1othersupplieraddresshere0000000000000"
	)
	// Same operator, different suppliers and subdomains: must collapse to one
	// domain across both the winner and the loser branch.
	winner := hedgeResult{
		endpointAddr: protocol.EndpointAddr(supplierA + "-https://relayminer.eu.hedge-operator.example"),
		supplierAddr: supplierA,
		duration:     120 * time.Millisecond,
	}
	loser := hedgeResult{
		endpointAddr: protocol.EndpointAddr(supplierB + "-https://other.us.hedge-operator.example"),
		supplierAddr: supplierB,
		duration:     400 * time.Millisecond,
		isHedge:      true,
	}

	hr := &hedgeRacer{logger: polyzero.NewLogger(), rc: &requestContext{}}
	hr.recordWinner(winner)
	hr.recordLoser(loser)

	domains := hedgeEmittedLabelValues(t, metrics.HedgeSupplierOutcomeTotal, "domain")

	require.NotContains(t, domains, supplierA,
		"the winner call site is passing the supplier address where a domain is expected")
	require.NotContains(t, domains, supplierB,
		"the loser call site is passing the supplier address where a domain is expected")
	require.NotContains(t, domains, metrics.DomainSupplierAddr,
		"a call site passed a bech32 address; the sanitizer caught it, but the label is now a "+
			"sentinel instead of the operator it is supposed to name")
	require.Equal(t, map[string]struct{}{"hedge-operator.example": {}}, domains,
		"winner and loser on the same operator must collapse onto one domain")

	// Both roles must still be distinct series — the fix narrows the metric, it
	// must not flatten win-rate into a single number.
	require.Equal(t, 1.0, testutilToFloat(t, metrics.HedgeSupplierOutcomeTotal, "hedge-operator.example", metrics.HedgeRoleWinner))
	require.Equal(t, 1.0, testutilToFloat(t, metrics.HedgeSupplierOutcomeTotal, "hedge-operator.example", metrics.HedgeRoleLoser))
}

func hedgeEmittedLabelValues(t *testing.T, c prometheus.Collector, labelName string) map[string]struct{} {
	t.Helper()
	ch := make(chan prometheus.Metric, 1<<12)
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

func testutilToFloat(t *testing.T, vec *prometheus.CounterVec, labelValues ...string) float64 {
	t.Helper()
	var pb dto.Metric
	require.NoError(t, vec.WithLabelValues(labelValues...).Write(&pb))
	return pb.GetCounter().GetValue()
}
