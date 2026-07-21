package shannon

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/gateway"
)

// TestRequestContext_SatisfiesProtocolRequestContext_HealthCheck is a compile-time
// guarantee that *requestContext implements gateway.ProtocolRequestContext including the
// MarkAsHealthCheck method. If this stops compiling, the interface drifted.
func TestRequestContext_SatisfiesProtocolRequestContext_HealthCheck(t *testing.T) {
	var _ gateway.ProtocolRequestContext = (*requestContext)(nil)
}

// TestMarkAsHealthCheck_SetsFlag verifies that MarkAsHealthCheck flips the isHealthCheck
// bit, which is what gates the relays_total recording skip in handleEndpointSuccess /
// handleEndpointError (the health-check executor is the sole recorder, so recording here
// too would double-count the relay as request_type="normal").
func TestMarkAsHealthCheck_SetsFlag(t *testing.T) {
	rc := &requestContext{}
	require.False(t, rc.isHealthCheck, "default should be false (a normal relay is recorded by the protocol layer)")
	rc.MarkAsHealthCheck()
	require.True(t, rc.isHealthCheck, "MarkAsHealthCheck must set isHealthCheck=true so the protocol layer skips its relay metric")
}

// TestMarkAsHealthCheck_Idempotent verifies calling MarkAsHealthCheck twice is a no-op.
func TestMarkAsHealthCheck_Idempotent(t *testing.T) {
	rc := &requestContext{}
	rc.MarkAsHealthCheck()
	rc.MarkAsHealthCheck()
	require.True(t, rc.isHealthCheck)
}

// TestHealthCheck_IndependentOfHedgeRetry verifies the health-check flag is orthogonal to
// hedge/retry: a health-check relay is never also a user hedge/retry, and the flags don't
// interfere.
func TestHealthCheck_IndependentOfHedgeRetry(t *testing.T) {
	rc := &requestContext{}
	rc.MarkAsHealthCheck()
	require.True(t, rc.isHealthCheck)
	require.False(t, rc.isHedge)
	require.False(t, rc.isRetry)
}
