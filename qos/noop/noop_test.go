package noop

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

func newTestQoS() *NoOpQoS {
	logger := polyzero.NewLogger()
	return NewNoOpQoSService(logger, "test-service")
}

func TestNoOpQoS_DefaultBehavior_RandomSelection(t *testing.T) {
	// When syncAllowance is 0 (default), all endpoints should pass filtering.
	qos := newTestQoS()

	endpoints := protocol.EndpointAddrList{
		protocol.EndpointAddr("ep1"),
		protocol.EndpointAddr("ep2"),
		protocol.EndpointAddr("ep3"),
	}

	sel := qos.newFilteringSelector()
	selected, err := sel.Select(endpoints)
	require.NoError(t, err)
	require.Contains(t, endpoints, selected)
}

func TestNoOpQoS_DefaultBehavior_SelectMultiple(t *testing.T) {
	qos := newTestQoS()

	endpoints := protocol.EndpointAddrList{
		protocol.EndpointAddr("ep1"),
		protocol.EndpointAddr("ep2"),
		protocol.EndpointAddr("ep3"),
	}

	sel := qos.newFilteringSelector()
	selected, err := sel.SelectMultiple(endpoints, 2)
	require.NoError(t, err)
	require.Len(t, selected, 2)
}

func TestNoOpQoS_FilteringDisabled_WhenSyncAllowanceZero(t *testing.T) {
	qos := newTestQoS()

	// Set up some endpoints with block heights
	require.NoError(t, qos.UpdateFromExtractedData("ep1", &qostypes.ExtractedData{BlockHeight: 100}))
	require.NoError(t, qos.UpdateFromExtractedData("ep2", &qostypes.ExtractedData{BlockHeight: 50}))

	// syncAllowance is 0 (default), so ep2 should NOT be filtered despite being behind
	endpoints := protocol.EndpointAddrList{"ep1", "ep2"}
	sel := qos.newFilteringSelector()
	filtered := sel.filterValidEndpoints(endpoints)
	require.Len(t, filtered, 2)
}

func TestNoOpQoS_FiltersStaleEndpoints(t *testing.T) {
	qos := newTestQoS()
	qos.SetSyncAllowance(5) // Allow 5 blocks behind

	require.NoError(t, qos.UpdateFromExtractedData("ep-good", &qostypes.ExtractedData{BlockHeight: 100}))
	require.NoError(t, qos.UpdateFromExtractedData("ep-stale", &qostypes.ExtractedData{BlockHeight: 90}))

	// perceived = 100, min allowed = 100-5 = 95
	// ep-good (100) passes, ep-stale (90) fails
	endpoints := protocol.EndpointAddrList{"ep-good", "ep-stale"}
	sel := qos.newFilteringSelector()
	filtered := sel.filterValidEndpoints(endpoints)

	require.Len(t, filtered, 1)
	require.Equal(t, protocol.EndpointAddr("ep-good"), filtered[0])
}

func TestNoOpQoS_FreshEndpointAllowed(t *testing.T) {
	qos := newTestQoS()
	qos.SetSyncAllowance(5)

	require.NoError(t, qos.UpdateFromExtractedData("ep-known", &qostypes.ExtractedData{BlockHeight: 100}))

	// "ep-fresh" is not in the store yet — should be allowed through
	endpoints := protocol.EndpointAddrList{"ep-known", "ep-fresh"}
	sel := qos.newFilteringSelector()
	filtered := sel.filterValidEndpoints(endpoints)

	require.Len(t, filtered, 2)
	require.Contains(t, filtered, protocol.EndpointAddr("ep-known"))
	require.Contains(t, filtered, protocol.EndpointAddr("ep-fresh"))
}

func TestNoOpQoS_FallbackWhenAllFiltered(t *testing.T) {
	qos := newTestQoS()
	qos.SetSyncAllowance(2) // Very tight allowance

	require.NoError(t, qos.UpdateFromExtractedData("ep1", &qostypes.ExtractedData{BlockHeight: 100}))
	require.NoError(t, qos.UpdateFromExtractedData("ep2", &qostypes.ExtractedData{BlockHeight: 90}))
	require.NoError(t, qos.UpdateFromExtractedData("ep3", &qostypes.ExtractedData{BlockHeight: 91}))

	// perceived = 100, min = 98. All observed endpoints are below 98.
	// But ep1 at 100 passes.
	// Let's make a case where ALL truly fail:
	// Only include ep2 and ep3 in the available list
	endpoints := protocol.EndpointAddrList{"ep2", "ep3"}
	sel := qos.newFilteringSelector()

	// filterValidEndpoints should return empty
	filtered := sel.filterValidEndpoints(endpoints)
	require.Empty(t, filtered)

	// But Select should still return one via fallback
	selected, err := sel.Select(endpoints)
	require.NoError(t, err)
	require.Contains(t, endpoints, selected)
}

func TestNoOpQoS_NoPerceivedBlock_PassesAll(t *testing.T) {
	qos := newTestQoS()
	qos.SetSyncAllowance(5)

	// No block height data at all — perceivedBlockHeight is 0
	endpoints := protocol.EndpointAddrList{"ep1", "ep2"}
	sel := qos.newFilteringSelector()
	filtered := sel.filterValidEndpoints(endpoints)

	require.Len(t, filtered, 2)
}

func TestNoOpQoS_UpdateFromExtractedData(t *testing.T) {
	qos := newTestQoS()

	// Nil data should be no-op
	require.NoError(t, qos.UpdateFromExtractedData("ep1", nil))
	require.Equal(t, uint64(0), qos.GetPerceivedBlockNumber())

	// Zero/negative block height should be no-op
	require.NoError(t, qos.UpdateFromExtractedData("ep1", &qostypes.ExtractedData{BlockHeight: 0}))
	require.Equal(t, uint64(0), qos.GetPerceivedBlockNumber())
	require.NoError(t, qos.UpdateFromExtractedData("ep1", &qostypes.ExtractedData{BlockHeight: -1}))
	require.Equal(t, uint64(0), qos.GetPerceivedBlockNumber())

	// Valid block height should update
	require.NoError(t, qos.UpdateFromExtractedData("ep1", &qostypes.ExtractedData{BlockHeight: 100}))
	require.Equal(t, uint64(100), qos.GetPerceivedBlockNumber())

	// Higher block height should update perceived
	require.NoError(t, qos.UpdateFromExtractedData("ep2", &qostypes.ExtractedData{BlockHeight: 200}))
	require.Equal(t, uint64(200), qos.GetPerceivedBlockNumber())

	// Lower block height should not update perceived
	require.NoError(t, qos.UpdateFromExtractedData("ep3", &qostypes.ExtractedData{BlockHeight: 150}))
	require.Equal(t, uint64(200), qos.GetPerceivedBlockNumber())
}

func TestNoOpQoS_SetSyncAllowance(t *testing.T) {
	qos := newTestQoS()

	require.Equal(t, uint64(0), qos.syncAllowance.Load())

	qos.SetSyncAllowance(10)
	require.Equal(t, uint64(10), qos.syncAllowance.Load())

	qos.SetSyncAllowance(0)
	require.Equal(t, uint64(0), qos.syncAllowance.Load())
}

func TestNoOpQoS_ConsumeExternalBlockHeight(t *testing.T) {
	qos := newTestQoS()

	// First, set a perceived block height from a supplier
	require.NoError(t, qos.UpdateFromExtractedData("ep1", &qostypes.ExtractedData{BlockHeight: 100}))

	heights := make(chan int64, 5)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use 0 grace period to simplify testing — the default (60s) would require waiting
	// We pass 1ms to avoid using the default 60s grace
	qos.ConsumeExternalBlockHeight(ctx, heights, 1*time.Millisecond)

	// Wait for grace period to elapse
	time.Sleep(10 * time.Millisecond)

	// Send a higher external block — should raise perceived
	heights <- 200
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, uint64(200), qos.GetPerceivedBlockNumber())

	// Send a lower external block — should NOT lower perceived
	heights <- 150
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, uint64(200), qos.GetPerceivedBlockNumber())

	// Negative/zero should be ignored
	heights <- 0
	heights <- -1
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, uint64(200), qos.GetPerceivedBlockNumber())
}

func TestNoOpQoS_ConsumeExternalBlockHeight_SkipsWhenNoSuppliers(t *testing.T) {
	qos := newTestQoS()
	// perceivedBlockHeight is 0 — no suppliers have reported

	heights := make(chan int64, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	qos.ConsumeExternalBlockHeight(ctx, heights, 1*time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	// External block should be ignored when perceivedBlockHeight is 0
	heights <- 500
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, uint64(0), qos.GetPerceivedBlockNumber())
}

func TestNoOpQoS_ConsumeExternalBlockHeight_GracePeriod(t *testing.T) {
	qos := newTestQoS()

	require.NoError(t, qos.UpdateFromExtractedData("ep1", &qostypes.ExtractedData{BlockHeight: 100}))

	heights := make(chan int64, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a longer grace period
	qos.ConsumeExternalBlockHeight(ctx, heights, 200*time.Millisecond)

	// Send during grace period — should be deferred
	heights <- 500
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, uint64(100), qos.GetPerceivedBlockNumber())

	// Wait for grace to elapse and send again
	time.Sleep(200 * time.Millisecond)
	heights <- 500
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, uint64(500), qos.GetPerceivedBlockNumber())
}

func TestNoOpQoS_EndpointWithZeroBlockHeight_Allowed(t *testing.T) {
	qos := newTestQoS()
	qos.SetSyncAllowance(5)

	// ep-good reports block 100
	require.NoError(t, qos.UpdateFromExtractedData("ep-good", &qostypes.ExtractedData{BlockHeight: 100}))
	// ep-zero has a zero block height stored (via a 0 BlockHeight data that gets ignored,
	// but simulate by directly writing to the store)
	qos.endpointStore.mu.Lock()
	qos.endpointStore.endpoints["ep-zero"] = endpointState{blockHeight: 0}
	qos.endpointStore.mu.Unlock()

	endpoints := protocol.EndpointAddrList{"ep-good", "ep-zero"}
	sel := qos.newFilteringSelector()
	filtered := sel.filterValidEndpoints(endpoints)

	// Both should pass — ep-zero has blockHeight=0 so check is skipped
	require.Len(t, filtered, 2)
}

func TestNoOpQoS_SelectReturnsErrorOnEmptyList(t *testing.T) {
	qos := newTestQoS()
	sel := qos.newFilteringSelector()

	_, err := sel.Select(protocol.EndpointAddrList{})
	require.Error(t, err)

	_, err = sel.SelectMultiple(protocol.EndpointAddrList{}, 1)
	require.Error(t, err)
}

func TestNoOpQoS_ParseHTTPRequest_PreservesDetectedRPCType(t *testing.T) {
	tests := []struct {
		name            string
		detectedRPCType sharedtypes.RPCType
	}{
		{
			name:            "JSON-RPC detected type is preserved",
			detectedRPCType: sharedtypes.RPCType_JSON_RPC,
		},
		{
			name:            "REST detected type is preserved",
			detectedRPCType: sharedtypes.RPCType_REST,
		},
		{
			name:            "UNKNOWN_RPC detected type is preserved",
			detectedRPCType: sharedtypes.RPCType_UNKNOWN_RPC,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qos := newTestQoS()

			httpReq := &http.Request{
				Method: http.MethodPost,
				URL:    &url.URL{Path: "/"},
				Body:   io.NopCloser(bytes.NewBufferString(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)),
			}

			reqCtx, ok := qos.ParseHTTPRequest(context.Background(), httpReq, tt.detectedRPCType)
			require.True(t, ok)

			payloads := reqCtx.GetServicePayloads()
			require.Len(t, payloads, 1)
			require.Equal(t, tt.detectedRPCType, payloads[0].RPCType,
				"payload RPCType should match the detected type, not be hardcoded to UNKNOWN_RPC")
		})
	}
}
