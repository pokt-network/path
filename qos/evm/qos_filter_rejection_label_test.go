package evm

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

// Test_BasicEndpointValidation_RejectionKeysOnDomain asserts the CALL SITE, not
// the metric.
//
// path_qos_filter_rejection_total was re-keyed from `supplier` to `domain` on
// 2026-08-12 (1,271 series live in a 10-minute window vs 24,708 distinct minted
// over one pod's 7.7h life — 19.4× churn, the worst of any gateway metric). The
// re-key compiles silently if a caller keeps passing the supplier address: both
// parameters are plain strings.
//
// So the label NAME being `domain` proves nothing. This asserts on the label
// VALUE the production caller emits: an operator domain, never the
// DomainSupplierAddr sentinel that SanitizeDomainLabel produces from a bech32
// address. That distinction is the entire fix — the metric's whole purpose is to
// collapse many suppliers onto one operator.
func Test_BasicEndpointValidation_RejectionKeysOnDomain(t *testing.T) {
	metrics.QoSFilterRejectionTotal.Reset()

	const (
		supplier = "pokt1ylsjqcl0yunve78etutw660a327avc26fxrlfr"
		// Two endpoints, same operator, different suppliers and subdomains: the
		// pair that must collapse onto ONE series.
		addrA = protocol.EndpointAddr(supplier + "-https://relayminer.eu.operator-under-test.example")
		addrB = protocol.EndpointAddr("pokt1othersupplieraddresshere0000000000000-https://other.us.operator-under-test.example")
	)

	ss := &serviceState{
		logger:           polyzero.NewLogger(),
		serviceQoSConfig: NewEVMServiceQoSConfig("test-service", "1", nil),
	}

	// An endpoint with no block-number observation is rejected with
	// QoSFilterReasonBlockHeightUnknown, which is the shortest production path
	// into RecordQoSFilterRejection.
	require.Error(t, ss.basicEndpointValidation(addrA, endpoint{}, false))
	require.Error(t, ss.basicEndpointValidation(addrB, endpoint{}, false))

	domains := emittedLabelValues(t, metrics.QoSFilterRejectionTotal, "domain")

	require.NotContains(t, domains, supplier,
		"the call site is passing the supplier address where a domain is expected")
	require.NotContains(t, domains, metrics.DomainSupplierAddr,
		"the call site passed a bech32 address; the sanitizer caught it, but the label is "+
			"now a sentinel instead of the operator it is supposed to name")
	require.Equal(t, map[string]struct{}{"operator-under-test.example": {}}, domains,
		"two suppliers of one operator must collapse onto exactly one domain series")
}

// emittedLabelValues reads back the values the Prometheus collector actually
// received for one label, across every child series of a vec.
func emittedLabelValues(t *testing.T, c prometheus.Collector, labelName string) map[string]struct{} {
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
