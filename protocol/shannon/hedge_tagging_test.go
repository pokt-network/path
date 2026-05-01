package shannon

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/gateway"
)

// TestRequestContext_SatisfiesProtocolRequestContext is a compile-time guarantee
// that *requestContext implements gateway.ProtocolRequestContext, including the
// new MarkAsHedge method. If this stops compiling, the interface drifted.
func TestRequestContext_SatisfiesProtocolRequestContext(t *testing.T) {
	var _ gateway.ProtocolRequestContext = (*requestContext)(nil)
}

// TestMarkAsHedge_SetsFlag verifies that MarkAsHedge flips the isHedge bit, which
// is what later gates the relay-type tagging and the latency-penalty skip in
// handleEndpointSuccess / handleEndpointError.
func TestMarkAsHedge_SetsFlag(t *testing.T) {
	rc := &requestContext{}
	require.False(t, rc.isHedge, "default should be false (non-hedge)")
	rc.MarkAsHedge()
	require.True(t, rc.isHedge, "MarkAsHedge must set isHedge=true so downstream relay/reputation paths can branch on it")
}

// TestMarkAsHedge_Idempotent verifies that calling MarkAsHedge twice is a no-op
// (no panic, flag stays true). This matters because hedge.go currently calls it
// once but a future caller might call it defensively.
func TestMarkAsHedge_Idempotent(t *testing.T) {
	rc := &requestContext{}
	rc.MarkAsHedge()
	rc.MarkAsHedge()
	require.True(t, rc.isHedge)
}
