package evm

import (
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

// Test_ArchivalTTL_UnverifiedPathDoesNotOutliveVerifiedPath pins the invariant that both
// sources of archival status share one lifetime, asserted through the production caller
// rather than on the constant.
//
// The two had drifted 16x apart, in the worst direction. The health-check path asserts an
// exact expected historical value from the rules file, so a node that ignores the block
// parameter and answers from current state FAILS it — and that verified mark expired in 30
// minutes. The user-traffic path cannot assert a value, since the query is whatever a
// client happened to send, so it granted archival status on any successful archival-method
// call — and that UNVERIFIED mark lasted 8 hours.
//
// Measured in production 2026-08-20: four endpoints on one operator were marked archival
// while returning current state for every block asked, including one 256x past the chain
// tip. The health-check rule for the service they served had been deleted, leaving only
// the unverified 8h path to promote them.
//
// The old code carried a comment asserting the two values matched. They did not. A comment
// cannot hold this invariant; reading the stored expiry back can.
func Test_ArchivalTTL_UnverifiedPathDoesNotOutliveVerifiedPath(t *testing.T) {
	qos := NewSimpleQoSInstance(polyzero.NewLogger(), protocol.ServiceID("eth"))
	endpointAddr := protocol.EndpointAddr("pokt1supplier-https://archival.example.com/rpc")

	before := time.Now()
	require.NoError(t, qos.UpdateFromExtractedData(endpointAddr, &qostypes.ExtractedData{
		ArchivalCheckPerformed: true,
		IsArchival:             true,
	}))

	qos.endpointStore.endpointsMu.RLock()
	stored := qos.endpointStore.endpoints[endpointAddr]
	qos.endpointStore.endpointsMu.RUnlock()

	require.True(t, stored.checkArchival.isArchival, "precondition: the endpoint must have been promoted")

	granted := stored.checkArchival.expiresAt.Sub(before)
	require.InDelta(t, gateway.ArchivalStatusTTL.Seconds(), granted.Seconds(), 30,
		"a user-traffic archival confirmation must grant the SAME lifetime as a verified "+
			"health-check confirmation; weaker evidence must never outlive stronger evidence")
	require.LessOrEqual(t, granted, time.Hour,
		"an unverified archival mark lasting hours lets one lucky response hold an endpoint "+
			"in the archival pool long after it stopped being re-confirmed")
}
