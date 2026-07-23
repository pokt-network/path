package selector

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// TestSelectOperatorUniform_EqualPerOperator verifies each operator gets ~1/m of selections
// regardless of endpoint count: op0 owns 8 of 10 endpoints yet still lands ~1/3, unlike the
// endpoint-count-weighted concentration cap (which would give it ~80%).
func TestSelectOperatorUniform_EqualPerOperator(t *testing.T) {
	eps, operators := makeOperatorPool([]int{8, 1, 1})

	counts := map[string]int{}
	const draws = 3000
	for i := 0; i < draws; i++ {
		sel := SelectOperatorUniform("test", eps)
		require.NotEmpty(t, sel)
		require.Contains(t, eps, sel)
		counts[operatorOf(string(sel))]++
	}

	for _, op := range operators {
		share := float64(counts[op]) / float64(draws)
		require.InDelta(t, 1.0/3.0, share, 0.05, "operator %s share %.3f should be ~1/3", op, share)
	}
}

// TestSelectOperatorUniform_WithinOperatorUniform verifies that within the chosen operator,
// endpoints are picked uniformly — op0's two endpoints each land ~half of op0's share.
func TestSelectOperatorUniform_WithinOperatorUniform(t *testing.T) {
	eps, _ := makeOperatorPool([]int{2, 2})

	counts := map[string]int{}
	const draws = 4000
	for i := 0; i < draws; i++ {
		counts[string(SelectOperatorUniform("test", eps))]++
	}
	// 2 operators × 2 endpoints each → every endpoint ~1/4.
	for _, ep := range eps {
		require.InDelta(t, 0.25, float64(counts[string(ep)])/float64(draws), 0.05, "endpoint %s", ep)
	}
}

// TestSelectOperatorUniform_SingleAndEmpty covers boundaries.
func TestSelectOperatorUniform_SingleAndEmpty(t *testing.T) {
	require.Empty(t, SelectOperatorUniform("test", protocol.EndpointAddrList{}))

	one, _ := makeOperatorPool([]int{1})
	require.Equal(t, one[0], SelectOperatorUniform("test", one))
}
