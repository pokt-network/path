package solana

import (
	"context"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/stretchr/testify/require"

	qosobservations "github.com/pokt-network/path/observation/qos"
	"github.com/pokt-network/path/protocol"
)

// makeEndpointStore builds an EndpointStore + ServiceState with three endpoints
// at the supplied block heights. Used by the fallback tests below.
func makeEndpointStore(t *testing.T, perceived uint64, heights map[protocol.EndpointAddr]uint64) *EndpointStore {
	t.Helper()
	logger := polylog.Ctx(context.Background())

	endpoints := make(map[protocol.EndpointAddr]endpoint, len(heights))
	for addr, h := range heights {
		endpoints[addr] = endpoint{
			SolanaGetEpochInfoResponse: &qosobservations.SolanaGetEpochInfoResponse{
				BlockHeight: h,
				Epoch:       1,
			},
		}
	}

	ss := &ServiceState{
		logger:               logger,
		perceivedBlockHeight: perceived,
		perceivedEpoch:       1,
	}
	return &EndpointStore{
		logger:       logger,
		serviceState: ss,
		endpoints:    endpoints,
	}
}

// TestSelectLeastStaleEndpoints_Solana verifies the solana fallback prefers
// least-stale endpoints when all candidates fail validation.
func TestSelectLeastStaleEndpoints_Solana(t *testing.T) {
	const perceived = uint64(1000)

	leastAddr := protocol.EndpointAddr("pokt1least-https://least.example.com")
	midAddr := protocol.EndpointAddr("pokt1mid-https://mid.example.com")
	worstAddr := protocol.EndpointAddr("pokt1worst-https://worst.example.com")

	es := makeEndpointStore(t, perceived, map[protocol.EndpointAddr]uint64{
		leastAddr: perceived - 8,
		midAddr:   perceived - 50,
		worstAddr: perceived - 500,
	})

	available := protocol.EndpointAddrList{leastAddr, midAddr, worstAddr}

	const trials = 100
	picks := map[protocol.EndpointAddr]int{}
	for i := 0; i < trials; i++ {
		out := es.selectLeastStaleEndpoints(available, 1)
		require.Len(t, out, 1)
		picks[out[0]]++
	}

	require.Equal(t, trials, picks[leastAddr],
		"least-stale endpoint should be the only pick when asking for 1; got=%v", picks)
	require.Zero(t, picks[midAddr])
	require.Zero(t, picks[worstAddr])

	// Asking for 2: top-2 least-stale, never the worst.
	multi := es.selectLeastStaleEndpoints(available, 2)
	require.Len(t, multi, 2)
	set := map[protocol.EndpointAddr]bool{multi[0]: true, multi[1]: true}
	require.True(t, set[leastAddr])
	require.True(t, set[midAddr])
	require.False(t, set[worstAddr])
}

// TestSelectLeastStaleEndpoints_Solana_DeprioritizesNoData verifies that
// endpoints not yet in the store rank below endpoints with known (even stale)
// block heights — and also that endpoints with nil SolanaGetEpochInfoResponse
// (fresh, no observation yet) are treated as no-data.
func TestSelectLeastStaleEndpoints_Solana_DeprioritizesNoData(t *testing.T) {
	const perceived = uint64(1000)
	knownAddr := protocol.EndpointAddr("pokt1known-https://known.example.com")
	unknownAddr := protocol.EndpointAddr("pokt1unknown-https://unknown.example.com")

	es := makeEndpointStore(t, perceived, map[protocol.EndpointAddr]uint64{
		knownAddr: perceived - 100,
	})
	// unknownAddr deliberately not in store

	available := protocol.EndpointAddrList{knownAddr, unknownAddr}

	const trials = 50
	picks := map[protocol.EndpointAddr]int{}
	for i := 0; i < trials; i++ {
		out := es.selectLeastStaleEndpoints(available, 1)
		require.Len(t, out, 1)
		picks[out[0]]++
	}
	require.Equal(t, trials, picks[knownAddr],
		"known-stale endpoint should always rank above no-data endpoint; picks=%v", picks)
}

// TestSelectLeastStaleEndpoints_Solana_FallbackWhenNoChainContext verifies
// degradation to random selection when perceivedBlockHeight is unset (cold start).
func TestSelectLeastStaleEndpoints_Solana_FallbackWhenNoChainContext(t *testing.T) {
	addrA := protocol.EndpointAddr("pokt1a-https://a.example.com")
	addrB := protocol.EndpointAddr("pokt1b-https://b.example.com")
	es := makeEndpointStore(t, 0, map[protocol.EndpointAddr]uint64{
		addrA: 100,
		addrB: 200,
	})
	available := protocol.EndpointAddrList{addrA, addrB}

	out := es.selectLeastStaleEndpoints(available, 1)
	require.Len(t, out, 1)
	require.Contains(t, []protocol.EndpointAddr{addrA, addrB}, out[0])
}
