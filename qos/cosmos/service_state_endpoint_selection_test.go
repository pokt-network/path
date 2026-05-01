package cosmos

import (
	"context"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// makeServiceState builds a serviceState with three endpoints at the supplied block heights.
// Used by the fallback tests below.
func makeServiceState(t *testing.T, perceived, syncAllowance uint64, heights map[protocol.EndpointAddr]uint64) *serviceState {
	t.Helper()
	logger := polylog.Ctx(context.Background())

	cfg := &simpleCosmosConfig{
		serviceID: "test-service",
		supportedAPIs: map[sharedtypes.RPCType]struct{}{
			sharedtypes.RPCType_JSON_RPC: {},
		},
	}
	cfg.syncAllowance.Store(syncAllowance)

	endpoints := make(map[protocol.EndpointAddr]endpoint, len(heights))
	for addr, h := range heights {
		hCopy := h
		endpoints[addr] = endpoint{
			checkCometBFTStatus: endpointCheckCometBFTStatus{
				latestBlockHeight: &hCopy,
				expiresAt:         time.Now().Add(1 * time.Hour),
			},
		}
	}

	ss := &serviceState{
		logger:           logger,
		serviceQoSConfig: cfg,
		endpointStore: &endpointStore{
			logger:    logger,
			endpoints: endpoints,
		},
		perceivedBlockNumber: perceived,
	}
	return ss
}

// TestSelectLeastStaleEndpoints_Cosmos verifies the cosmos fallback prefers
// least-stale endpoints when all candidates fail validation.
func TestSelectLeastStaleEndpoints_Cosmos(t *testing.T) {
	const perceived = uint64(1000)
	const syncAllowance = uint64(5)

	leastAddr := protocol.EndpointAddr("pokt1least-https://least.example.com")
	midAddr := protocol.EndpointAddr("pokt1mid-https://mid.example.com")
	worstAddr := protocol.EndpointAddr("pokt1worst-https://worst.example.com")

	ss := makeServiceState(t, perceived, syncAllowance, map[protocol.EndpointAddr]uint64{
		leastAddr: perceived - 8,   // 8 behind
		midAddr:   perceived - 50,  // 50 behind
		worstAddr: perceived - 500, // 500 behind
	})

	available := protocol.EndpointAddrList{leastAddr, midAddr, worstAddr}

	const trials = 100
	picks := map[protocol.EndpointAddr]int{}
	for i := 0; i < trials; i++ {
		out := ss.selectLeastStaleEndpoints(available, 1)
		require.Len(t, out, 1)
		picks[out[0]]++
	}

	require.Equal(t, trials, picks[leastAddr],
		"least-stale endpoint should be the only pick when asking for 1; got=%v", picks)
	require.Zero(t, picks[midAddr])
	require.Zero(t, picks[worstAddr])

	// Asking for 2: top-2 least-stale, never the worst.
	multi := ss.selectLeastStaleEndpoints(available, 2)
	require.Len(t, multi, 2)
	set := map[protocol.EndpointAddr]bool{multi[0]: true, multi[1]: true}
	require.True(t, set[leastAddr])
	require.True(t, set[midAddr])
	require.False(t, set[worstAddr])
}

// TestSelectLeastStaleEndpoints_Cosmos_DeprioritizesNoData verifies that
// endpoints not yet in the store rank below endpoints with known (even stale)
// block heights.
func TestSelectLeastStaleEndpoints_Cosmos_DeprioritizesNoData(t *testing.T) {
	const perceived = uint64(1000)
	const syncAllowance = uint64(5)
	knownAddr := protocol.EndpointAddr("pokt1known-https://known.example.com")
	unknownAddr := protocol.EndpointAddr("pokt1unknown-https://unknown.example.com")

	ss := makeServiceState(t, perceived, syncAllowance, map[protocol.EndpointAddr]uint64{
		knownAddr: perceived - 100, // known but stale
	})
	// unknownAddr deliberately not in store

	available := protocol.EndpointAddrList{knownAddr, unknownAddr}

	const trials = 50
	picks := map[protocol.EndpointAddr]int{}
	for i := 0; i < trials; i++ {
		out := ss.selectLeastStaleEndpoints(available, 1)
		require.Len(t, out, 1)
		picks[out[0]]++
	}
	require.Equal(t, trials, picks[knownAddr],
		"known-stale endpoint should always rank above no-data endpoint; picks=%v", picks)
}

// TestSelectLeastStaleEndpoints_Cosmos_FallbackWhenNoChainContext verifies
// degradation to random selection when perceivedBlock is unset (cold start).
func TestSelectLeastStaleEndpoints_Cosmos_FallbackWhenNoChainContext(t *testing.T) {
	const syncAllowance = uint64(5)
	addrA := protocol.EndpointAddr("pokt1a-https://a.example.com")
	addrB := protocol.EndpointAddr("pokt1b-https://b.example.com")
	ss := makeServiceState(t, 0, syncAllowance, map[protocol.EndpointAddr]uint64{
		addrA: 100,
		addrB: 200,
	})
	available := protocol.EndpointAddrList{addrA, addrB}

	out := ss.selectLeastStaleEndpoints(available, 1)
	require.Len(t, out, 1)
	require.Contains(t, []protocol.EndpointAddr{addrA, addrB}, out[0])
}
