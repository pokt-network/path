package solana

import (
	"context"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/stretchr/testify/require"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
)

// Endpoint addresses shaped like production ones: <supplier>-<url>. The domain is what
// path_qos_filter_rejection_total is keyed on, and what an operator reads in a dashboard.
const (
	freshEndpointAddr    = protocol.EndpointAddr("pokt1fresh-https://a001.op-alpha.example")
	trailingEndpointAddr = protocol.EndpointAddr("pokt1trail-https://b001.op-beta.example")
)

// makeValidatableStore builds a store whose endpoints all pass validateBasic, so the only
// thing separating them is how far their block height trails the perceived height.
//
// makeEndpointStore (store_test.go) deliberately omits the getHealth observation so every
// endpoint fails validateBasic — that is what the least-stale fallback tests need, and the
// opposite of what these tests need.
func makeValidatableStore(t *testing.T, perceived uint64, heights map[protocol.EndpointAddr]uint64) *EndpointStore {
	t.Helper()
	logger := polylog.Ctx(context.Background())

	endpoints := make(map[protocol.EndpointAddr]endpoint, len(heights))
	for addr, h := range heights {
		endpoints[addr] = endpoint{
			SolanaGetHealthResponse: &qosobservations.SolanaGetHealthResponse{
				Result: resultGetHealthOK,
			},
			SolanaGetEpochInfoResponse: &qosobservations.SolanaGetEpochInfoResponse{
				BlockHeight: h,
				Epoch:       1,
			},
		}
	}

	ss := &ServiceState{
		logger:               logger,
		serviceID:            "solana",
		perceivedBlockHeight: perceived,
		perceivedEpoch:       1,
	}
	return &EndpointStore{
		logger:       logger,
		serviceState: ss,
		endpoints:    endpoints,
	}
}

// selectedSet runs the production selection caller and returns what it handed back.
//
// Asserting here rather than on filterValidEndpoints or on the ServiceState fields: the only
// question that matters is whether selection still returns the endpoint. Three drain bugs
// shipped with passing tests that asserted on a helper's own output.
func selectedSet(t *testing.T, es *EndpointStore, available protocol.EndpointAddrList) map[protocol.EndpointAddr]bool {
	t.Helper()
	picked, err := es.SelectMultipleWithArchival(available, uint(len(available)), false)
	require.NoError(t, err)

	out := make(map[protocol.EndpointAddr]bool, len(picked))
	for _, addr := range picked {
		out[addr] = true
	}
	return out
}

// Test_SyncAllowance_TrailingEndpointStaysSelectable reproduces the production failure.
//
// perceivedBlockHeight is a MAX over observations. An endpoint carrying user traffic re-reports
// continuously and sits at the perceived height; an endpoint refreshed only by health checks
// trails it by a handful of blocks. Under the pre-fix strict comparison the trailing endpoint
// was filtered out, which denied it the traffic that would have refreshed it — Solana locked
// onto a single operator with the alternatives scoring 100 and receiving nothing.
func Test_SyncAllowance_TrailingEndpointStaysSelectable(t *testing.T) {
	const perceived = uint64(418_160_000)

	es := makeValidatableStore(t, perceived, map[protocol.EndpointAddr]uint64{
		freshEndpointAddr: perceived,
		// One block behind — the entire margin the old code needed to exclude it.
		trailingEndpointAddr: perceived - 1,
	})

	selected := selectedSet(t, es, protocol.EndpointAddrList{freshEndpointAddr, trailingEndpointAddr})

	require.True(t, selected[freshEndpointAddr], "endpoint at the perceived height must be selectable")
	require.True(t, selected[trailingEndpointAddr],
		"endpoint 1 block behind must be selectable: at ~2.5 blocks/s no endpoint can hold the max")
}

// Test_SyncAllowance_BoundsHowFarAnEndpointMayTrail proves the allowance is genuinely consulted
// rather than the check having been removed.
//
// Without this the fix is indistinguishable from deleting the block-height filter, and a revert
// of the allowance plumbing would leave Test_SyncAllowance_TrailingEndpointStaysSelectable green.
func Test_SyncAllowance_BoundsHowFarAnEndpointMayTrail(t *testing.T) {
	const perceived = uint64(418_160_000)

	es := makeValidatableStore(t, perceived, map[protocol.EndpointAddr]uint64{
		freshEndpointAddr:    perceived,
		trailingEndpointAddr: perceived - 10,
	})
	es.serviceState.SetSyncAllowance(5)

	available := protocol.EndpointAddrList{freshEndpointAddr, trailingEndpointAddr}
	selected := selectedSet(t, es, available)

	// The fresh endpoint survives, so the filtered set is non-empty and the least-stale
	// fallback does NOT run. Absence below is therefore a real exclusion, not a fallback
	// happening to rank the trailing endpoint last.
	require.True(t, selected[freshEndpointAddr])
	require.False(t, selected[trailingEndpointAddr],
		"10 blocks behind against an allowance of 5 must be excluded")
}

// Test_SyncAllowance_DefaultExcludesGenuinelyStale confirms the default is a real bound and not
// an effective disable.
func Test_SyncAllowance_DefaultExcludesGenuinelyStale(t *testing.T) {
	const perceived = uint64(418_160_000)

	es := makeValidatableStore(t, perceived, map[protocol.EndpointAddr]uint64{
		freshEndpointAddr: perceived,
		// Well past defaultSolanaBlockNumberSyncAllowance — roughly 20 minutes behind.
		trailingEndpointAddr: perceived - 3000,
	})

	selected := selectedSet(t, es, protocol.EndpointAddrList{freshEndpointAddr, trailingEndpointAddr})

	require.True(t, selected[freshEndpointAddr])
	require.False(t, selected[trailingEndpointAddr],
		"3000 blocks behind must still be excluded under the default allowance")
}

// Test_SyncAllowance_SurvivesTheConfiguredValue checks the boundary in both directions using
// the value solana actually carries in the external health-check rules.
func Test_SyncAllowance_SurvivesTheConfiguredValue(t *testing.T) {
	const perceived = uint64(418_160_000)
	const configured = uint64(750)

	atLimit := protocol.EndpointAddr("pokt1atlimit-https://c001.op-gamma.example")
	pastLimit := protocol.EndpointAddr("pokt1pastlimit-https://d001.op-delta.example")

	es := makeValidatableStore(t, perceived, map[protocol.EndpointAddr]uint64{
		freshEndpointAddr: perceived,
		atLimit:           perceived - configured,
		pastLimit:         perceived - configured - 1,
	})
	es.serviceState.SetSyncAllowance(configured)

	selected := selectedSet(t, es, protocol.EndpointAddrList{freshEndpointAddr, atLimit, pastLimit})

	require.True(t, selected[atLimit], "exactly at the allowance is inside it")
	require.False(t, selected[pastLimit], "one block past the allowance is outside it")
}

// Test_SetSyncAllowance_IsReachableFromHealthCheckConfig is the regression test for the actual
// defect.
//
// The health check executor applies a service's configured sync_allowance by asserting the QoS
// instance to `interface{ SetSyncAllowance(uint64) }`. Solana did not implement that method, so
// `sync_allowance: 750` was read, applied to the health check's own sync check, and silently
// dropped for endpoint selection — a configured value that reached one consumer and not the
// other, with nothing anywhere reporting the gap.
func Test_SetSyncAllowance_IsReachableFromHealthCheckConfig(t *testing.T) {
	logger := polylog.Ctx(context.Background())
	serviceState := &ServiceState{logger: logger, serviceID: "solana"}
	qosInstance := any(&QoS{
		logger:        logger,
		ServiceState:  serviceState,
		EndpointStore: &EndpointStore{logger: logger, serviceState: serviceState},
	})

	// Byte-for-byte the assertion in gateway/health_check_executor.go.
	setter, ok := qosInstance.(interface{ SetSyncAllowance(uint64) })
	require.True(t, ok, "solana QoS must satisfy the interface the health check executor asserts on")

	setter.SetSyncAllowance(750)
	require.Equal(t, uint64(750), serviceState.getSyncAllowance(),
		"the configured value must reach the state that ValidateEndpoint reads")
}

// Test_SyncAllowance_UnconfiguredFallsBackToDefault covers the startup window and the case where
// external rules fail to load: 0 means "not configured", never "strict".
func Test_SyncAllowance_UnconfiguredFallsBackToDefault(t *testing.T) {
	ss := &ServiceState{logger: polylog.Ctx(context.Background()), serviceID: "solana"}

	require.Equal(t, uint64(defaultSolanaBlockNumberSyncAllowance), ss.getSyncAllowance())

	ss.SetSyncAllowance(0)
	require.Equal(t, uint64(defaultSolanaBlockNumberSyncAllowance), ss.getSyncAllowance(),
		"0 must not re-enable the strict comparison this fix exists to remove")
}
