package shannon

import (
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// Test_WebsocketConnectionObservation_Duration guards the fix for the always-zero
// WebSocket connection duration metric. The closure observation must carry BOTH
// the real establishment timestamp and a close timestamp so the gateway can
// compute a non-zero duration; the establishment observation must carry only the
// establishment timestamp.
func Test_WebsocketConnectionObservation_Duration(t *testing.T) {
	logger := polyzero.NewLogger()
	ep := &mockEndpoint{addr: "supplier1-https://relay.example.com:443"}
	svc := protocol.ServiceID("eth")

	established := time.Now()
	closed := established.Add(42 * time.Second)

	// ESTABLISHED: established timestamp set, close timestamp absent.
	estObs := getWebsocketConnectionEstablishedObservation(logger, svc, ep, established)
	estConn := estObs.GetShannon().GetObservations()[0].GetWebsocketConnectionObservation()
	require.NotNil(t, estConn.GetConnectionEstablishedTimestamp())
	require.Nil(t, estConn.GetConnectionClosedTimestamp(), "establishment obs must not carry a close timestamp")

	// CLOSED: both timestamps set, and their delta is the real connection duration.
	// Before the fix, ConnectionClosedTimestamp was never set and the established
	// timestamp was re-stamped to now, so the computed duration was always 0.
	clsObs := getWebsocketConnectionClosedObservation(logger, svc, ep, established, closed)
	clsConn := clsObs.GetShannon().GetObservations()[0].GetWebsocketConnectionObservation()
	require.NotNil(t, clsConn.GetConnectionEstablishedTimestamp())
	require.NotNil(t, clsConn.GetConnectionClosedTimestamp(), "closure obs must carry a close timestamp")

	gotDur := clsConn.GetConnectionClosedTimestamp().AsTime().
		Sub(clsConn.GetConnectionEstablishedTimestamp().AsTime())
	require.Greater(t, gotDur.Seconds(), 0.0, "duration must be non-zero (regression guard for the always-0 bug)")
	require.InDelta(t, (42 * time.Second).Seconds(), gotDur.Seconds(), 0.001)
}
