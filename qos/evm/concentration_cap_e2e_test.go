package evm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
)

// TestSelectWithMetadata_ConcentrationCap_EndToEnd exercises the full production path —
// config → getMaxOperatorShare → SelectWithMetadata → SelectWithConcentrationCap — rather
// than the selector helper in isolation. It builds a real EVM QoS instance, enables the
// cap via SetMaxOperatorShare, and confirms that a dominant operator's realized selection
// share is bounded by the cap and that the reshape metric fires. The existing
// endpoint_selection tests all run with the static config (cap == 0), so this is the only
// coverage of the cap flowing end-to-end through Select.
func TestSelectWithMetadata_ConcentrationCap_EndToEnd(t *testing.T) {
	logger := polyzero.NewLogger()

	// Real production QoS instance (simpleServiceConfig, syncAllowance 0 → block-height
	// filtering off) with an empty endpoint store: every supplied endpoint is "fresh" and
	// passes validation.
	qos := NewSimpleQoSInstance(logger, "eth")
	qos.SetMaxOperatorShare(0.4)

	// Dominant operator op0 holds 10 endpoints; three singletons. Flat random would give
	// op0 10/13 ≈ 0.77; the cap must pull it to ~0.40. Addresses use the production
	// "supplier-https://host" form so the eTLD+1 resolves.
	keyCounts := map[string]int{"op0.tech": 10, "op1.tech": 1, "op2.tech": 1, "op3.tech": 1}
	var available protocol.EndpointAddrList
	for dom, count := range keyCounts {
		for k := 0; k < count; k++ {
			available = append(available, protocol.EndpointAddr(
				fmt.Sprintf("pokt1%s%d-https://node%d.%s", strings.TrimSuffix(dom, ".tech"), k, k, dom),
			))
		}
	}

	before := testutil.ToFloat64(metrics.ConcentrationCapReshapedTotal.WithLabelValues("eth"))

	counts := map[string]int{}
	const draws = 60_000
	for i := 0; i < draws; i++ {
		res, err := qos.SelectWithMetadata(available, false, "req")
		require.NoError(t, err)
		require.NotEmpty(t, res.SelectedEndpoint)
		counts[operatorOfAddr(string(res.SelectedEndpoint))]++
	}

	// Dominant operator bounded near the cap, not its 0.77 key-share.
	require.InDelta(t, 0.40, float64(counts["op0.tech"])/float64(draws), 0.03,
		"end-to-end: dominant operator's selection share should be capped near 0.40, got %.3f",
		float64(counts["op0.tech"])/float64(draws))

	// The reshape metric fired for this service (the cap actually bit on every draw here).
	after := testutil.ToFloat64(metrics.ConcentrationCapReshapedTotal.WithLabelValues("eth"))
	require.Greater(t, after-before, 0.0, "concentration_cap_reshaped_total must increment when the cap reshapes")
}

// TestSelectWithMetadata_ConcentrationCap_DisabledByDefault confirms that the static test
// config path (cap unset → 0) leaves selection as a flat key-weighted random pick, so the
// cap is opt-in via SetMaxOperatorShare.
func TestSelectWithMetadata_ConcentrationCap_DisabledByDefault(t *testing.T) {
	logger := polyzero.NewLogger()
	qos := NewSimpleQoSInstance(logger, "eth-disabled") // no SetMaxOperatorShare → cap 0

	var available protocol.EndpointAddrList
	for _, dom := range []string{"big.tech", "big.tech", "big.tech", "solo.tech"} {
		available = append(available, protocol.EndpointAddr(
			fmt.Sprintf("pokt1%d-https://n.%s", len(available), dom),
		))
	}

	counts := map[string]int{}
	const draws = 40_000
	for i := 0; i < draws; i++ {
		res, err := qos.SelectWithMetadata(available, false, "req")
		require.NoError(t, err)
		counts[operatorOfAddr(string(res.SelectedEndpoint))]++
	}
	// 3 of 4 endpoints are big.tech → flat random gives it ~0.75 (cap disabled).
	require.InDelta(t, 0.75, float64(counts["big.tech"])/float64(draws), 0.03,
		"disabled cap should leave a flat key-weighted distribution")
}

// operatorOfAddr extracts the trailing "name.tech" eTLD+1 from a test endpoint address.
func operatorOfAddr(addr string) string {
	segs := strings.Split(addr, ".")
	if len(segs) < 2 {
		return addr
	}
	return segs[len(segs)-2] + "." + segs[len(segs)-1]
}
