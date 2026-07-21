package solana

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"
)

// TestSetMaxOperatorShare_RoundTrip verifies the per-operator concentration cap round-trips
// through the atomic float64-bits storage on the Solana EndpointStore (promoted onto QoS),
// and that the Solana QoS satisfies the SetMaxOperatorShare interface used by cmd/qos.go.
func TestSetMaxOperatorShare_RoundTrip(t *testing.T) {
	logger := polyzero.NewLogger()
	qos := NewSimpleQoSInstance(logger, "solana")

	// Default: disabled (0).
	require.Equal(t, 0.0, qos.getMaxOperatorShare())

	// Satisfies the setter interface cmd/qos.go asserts.
	var _ interface{ SetMaxOperatorShare(float64) } = qos

	for _, v := range []float64{0.4, 0.5, 0.75, 0.999} {
		qos.SetMaxOperatorShare(v)
		require.Equal(t, v, qos.getMaxOperatorShare())
	}

	qos.SetMaxOperatorShare(0)
	require.Equal(t, 0.0, qos.getMaxOperatorShare())
}
