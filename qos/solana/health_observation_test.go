package solana

import (
	"context"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

const healthCheckedAddr = protocol.EndpointAddr("pokt1hc-https://a001.op-alpha.example")

// newQoSForHealthTest builds a QoS whose endpoint store starts empty, the way a pod's does
// after a restart. Solana's store cannot be rebuilt from Redis (it needs live health and epoch
// data), so this is the state every deploy starts from.
func newQoSForHealthTest(t *testing.T) *QoS {
	t.Helper()
	logger := polylog.Ctx(context.Background())

	serviceState := &ServiceState{logger: logger, serviceID: "solana"}
	endpointStore := &EndpointStore{
		logger:       logger,
		serviceState: serviceState,
		endpoints:    map[protocol.EndpointAddr]endpoint{},
	}
	return &QoS{logger: logger, ServiceState: serviceState, EndpointStore: endpointStore}
}

// feedHealthCheck replays what the health-check pipeline produces: the executor hands the raw
// response to the extractor via ExtractedData.ExtractAll, then the result reaches QoS through
// UpdateFromExtractedData. Going through ExtractAll rather than hand-filling the struct is the
// point — the defect lived in which fields that pipeline populates.
func feedHealthCheck(t *testing.T, q *QoS, addr protocol.EndpointAddr, request, response string) {
	t.Helper()

	data := qostypes.NewExtractedData(addr, 200, []byte(response), 0)
	data.ExtractAll(NewSolanaDataExtractor(), []byte(request))
	require.NoError(t, q.UpdateFromExtractedData(addr, data))
}

// Test_HealthCheckAlone_MakesEndpointSelectable is the regression test for an endpoint that
// health checks could never rescue.
//
// Solana's validator requires a getHealth observation (errNoGetHealthObs). The health-check
// path wrote only a block height, so an endpoint whose observations came solely from health
// checks stayed permanently invalid — the configured getHealth probe ran, passed, and
// populated nothing the validator reads. Permanently invalid means no user traffic, and user
// traffic was the only other source of a health observation.
func Test_HealthCheckAlone_MakesEndpointSelectable(t *testing.T) {
	q := newQoSForHealthTest(t)

	// Exactly the two probes configured for solana in pnf_path_rules.yaml.
	feedHealthCheck(t, q, healthCheckedAddr,
		`{"jsonrpc":"2.0","id":1,"method":"getHealth"}`,
		`{"jsonrpc":"2.0","id":1,"result":"ok"}`)
	feedHealthCheck(t, q, healthCheckedAddr,
		`{"jsonrpc":"2.0","id":1,"method":"getBlockHeight"}`,
		`{"jsonrpc":"2.0","id":1,"result":418160000}`)

	// Assert through the production selection caller. The endpoint IS in the store, so the
	// "not found, treat as fresh" bypass cannot account for a pass here.
	stored, found := q.endpoints[healthCheckedAddr]
	require.True(t, found, "health checks must put the endpoint in the store")
	require.NotNil(t, stored.SolanaGetHealthResponse, "health check must record a health observation")

	picked, err := q.SelectMultipleWithArchival(protocol.EndpointAddrList{healthCheckedAddr}, 1, false)
	require.NoError(t, err)
	require.Equal(t, protocol.EndpointAddrList{healthCheckedAddr}, picked)

	require.NoError(t, q.ServiceState.ValidateEndpoint(healthCheckedAddr, stored),
		"an endpoint fed only by health checks must be valid")
}

// Test_HealthCheckAlone_UnhealthyIsStillRejected guards the other direction: the fix must not
// turn "we now record health" into "everything is healthy". A node reporting it is behind has
// been observed, and observed-bad is not the same as observed-good.
func Test_HealthCheckAlone_UnhealthyIsStillRejected(t *testing.T) {
	q := newQoSForHealthTest(t)

	feedHealthCheck(t, q, healthCheckedAddr,
		`{"jsonrpc":"2.0","id":1,"method":"getHealth"}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32005,"message":"Node is behind by 42 slots"}}`)
	feedHealthCheck(t, q, healthCheckedAddr,
		`{"jsonrpc":"2.0","id":1,"method":"getBlockHeight"}`,
		`{"jsonrpc":"2.0","id":1,"result":418160000}`)

	stored := q.endpoints[healthCheckedAddr]
	require.NotNil(t, stored.SolanaGetHealthResponse)
	require.Equal(t, resultGetHealthSyncing, stored.Result)
	require.Error(t, q.ServiceState.ValidateEndpoint(healthCheckedAddr, stored),
		"an endpoint that reported itself behind must stay invalid")
}

// Test_BlockHeightResponse_DoesNotForgeAHealthObservation is the narrow guard on the method
// gate in IsSyncing. getBlockHeight returns a bare number; the pre-gate code ran that through
// a `result == "ok"` test. With SyncCheckPerformed now derived from whether IsSyncing errors,
// an ungated version would mint a health observation out of a block-height response — claiming
// evidence nobody gathered, which is worse than having none.
func Test_BlockHeightResponse_DoesNotForgeAHealthObservation(t *testing.T) {
	q := newQoSForHealthTest(t)

	feedHealthCheck(t, q, healthCheckedAddr,
		`{"jsonrpc":"2.0","id":1,"method":"getBlockHeight"}`,
		`{"jsonrpc":"2.0","id":1,"result":418160000}`)

	stored := q.endpoints[healthCheckedAddr]
	require.Nil(t, stored.SolanaGetHealthResponse,
		"a block-height response must not be recorded as a health observation")
	require.Equal(t, uint64(418160000), stored.BlockHeight, "the block height must still be recorded")
}

// Test_HealthOnlyObservation_DoesNotClobberBlockHeight covers the write ordering. A health-only
// observation carries block height 0; writing that would erase a real height locally and, via
// the per-endpoint Redis write, across every replica.
func Test_HealthOnlyObservation_DoesNotClobberBlockHeight(t *testing.T) {
	q := newQoSForHealthTest(t)

	feedHealthCheck(t, q, healthCheckedAddr,
		`{"jsonrpc":"2.0","id":1,"method":"getBlockHeight"}`,
		`{"jsonrpc":"2.0","id":1,"result":418160000}`)
	feedHealthCheck(t, q, healthCheckedAddr,
		`{"jsonrpc":"2.0","id":1,"method":"getHealth"}`,
		`{"jsonrpc":"2.0","id":1,"result":"ok"}`)

	require.Equal(t, uint64(418160000), q.endpoints[healthCheckedAddr].BlockHeight,
		"a health-only observation must leave the block height untouched")
}

// Test_UnobservedEpoch_DoesNotInvalidate covers the epoch half.
//
// Epoch is only ever set by a getEpochInfo response from user traffic — the health-check path
// builds a SolanaGetEpochInfoResponse carrying just a block height, leaving Epoch at 0. When 0
// was treated as invalid it re-created the same trap: an endpoint benched for a field nothing
// routinely supplies, and therefore never given the traffic that would supply it.
func Test_UnobservedEpoch_DoesNotInvalidate(t *testing.T) {
	q := newQoSForHealthTest(t)
	q.ServiceState.perceivedEpoch = 1018

	feedHealthCheck(t, q, healthCheckedAddr,
		`{"jsonrpc":"2.0","id":1,"method":"getHealth"}`,
		`{"jsonrpc":"2.0","id":1,"result":"ok"}`)
	feedHealthCheck(t, q, healthCheckedAddr,
		`{"jsonrpc":"2.0","id":1,"method":"getBlockHeight"}`,
		`{"jsonrpc":"2.0","id":1,"result":418160000}`)

	stored := q.endpoints[healthCheckedAddr]
	require.Zero(t, stored.Epoch, "health checks supply no epoch — this is the case under test")
	require.NoError(t, q.ServiceState.ValidateEndpoint(healthCheckedAddr, stored),
		"an unobserved epoch means 'not measured', never 'behind'")
}

// Test_EpochLag_ToleratesOneEpoch covers the tolerance and its boundary.
//
// perceivedEpoch is a max over observations, so at a rollover whichever endpoint reports first
// puts every other endpoint an epoch behind through no fault of its own — the same
// max-versus-strict shape as the block height check. Two epochs behind is real staleness.
func Test_EpochLag_ToleratesOneEpoch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		epoch       uint64
		expectValid bool
	}{
		{name: "current epoch", epoch: 1018, expectValid: true},
		{name: "one epoch behind (rollover skew)", epoch: 1017, expectValid: true},
		{name: "two epochs behind", epoch: 1016, expectValid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := newQoSForHealthTest(t)
			q.ServiceState.perceivedEpoch = 1018

			feedHealthCheck(t, q, healthCheckedAddr,
				`{"jsonrpc":"2.0","id":1,"method":"getHealth"}`,
				`{"jsonrpc":"2.0","id":1,"result":"ok"}`)
			// A real epoch only ever arrives via getEpochInfo from user traffic; health checks
			// supply none. Feed one directly to exercise the comparison.
			feedHealthCheck(t, q, healthCheckedAddr,
				`{"jsonrpc":"2.0","id":1,"method":"getEpochInfo"}`,
				`{"jsonrpc":"2.0","id":1,"result":{"blockHeight":418160000,"absoluteSlot":440100000,"epoch":1018}}`)

			stored := q.endpoints[healthCheckedAddr]
			require.NotNil(t, stored.SolanaGetEpochInfoResponse)
			stored.SolanaGetEpochInfoResponse.Epoch = tc.epoch

			err := q.ServiceState.ValidateEndpoint(healthCheckedAddr, stored)
			if tc.expectValid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
