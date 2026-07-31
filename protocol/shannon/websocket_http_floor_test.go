package shannon

import (
	"context"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
)

// newFloorTestProtocol builds a Protocol backed by in-memory reputation storage.
func newFloorTestProtocol(t *testing.T, ctx context.Context) (*Protocol, reputation.ReputationService) {
	t.Helper()
	config := reputation.Config{
		Enabled:         true,
		InitialScore:    80,
		MinThreshold:    30,
		RecoveryTimeout: 5 * time.Minute,
		StorageType:     "memory",
	}
	config.HydrateDefaults()

	store := reputationstorage.NewMemoryStorage(config.RecoveryTimeout)
	svc := reputation.NewService(config, store)
	require.NoError(t, svc.Start(ctx))
	t.Cleanup(func() { _ = svc.Stop() })

	return &Protocol{logger: polyzero.NewLogger(), reputationService: svc}, svc
}

// tankHTTPScore drives an endpoint's json_rpc score below the threshold while leaving its
// :websocket score untouched — the exact production shape, since a websocket score receives
// no negative signal without an active probe.
func tankHTTPScore(t *testing.T, ctx context.Context, svc reputation.ReputationService, serviceID protocol.ServiceID, addr protocol.EndpointAddr) {
	t.Helper()
	key := svc.KeyBuilderForService(serviceID).BuildKey(serviceID, addr, sharedtypes.RPCType_JSON_RPC)
	for i := 0; i < 4; i++ {
		require.NoError(t, svc.RecordSignal(ctx, key, reputation.NewCriticalErrorSignal("service_error", 200*time.Millisecond)))
	}
	score, _ := svc.GetScore(ctx, key)
	require.Less(t, score.Value, float64(30), "precondition: json_rpc score must be below threshold")
}

// An endpoint proven bad over HTTP must not be selectable for WebSocket, even though its
// :websocket score is untouched at initial_score.
func TestWebsocketHTTPFloor_ExcludesHTTPBadEndpoint(t *testing.T) {
	ctx := context.Background()
	p, svc := newFloorTestProtocol(t, ctx)
	serviceID := protocol.ServiceID("test-service")

	const bad = protocol.EndpointAddr("supplier1-https://bad.example.com")
	const good = protocol.EndpointAddr("supplier2-https://good.example.com")
	endpoints := map[protocol.EndpointAddr]endpoint{
		bad:  &mockEndpoint{addr: bad},
		good: &mockEndpoint{addr: good},
	}
	tankHTTPScore(t, ctx, svc, serviceID, bad)

	filtered := p.filterByReputation(ctx, serviceID, endpoints, sharedtypes.RPCType_WEBSOCKET, polyzero.NewLogger(), "")

	require.NotContains(t, filtered, bad, "HTTP-bad endpoint must be excluded from websocket selection")
	require.Contains(t, filtered, good)
}

// The same bad HTTP score must NOT affect json_rpc filtering beyond its normal effect, and
// must not leak into other rpc types' behaviour. Sanity check that the floor is websocket-only.
func TestWebsocketHTTPFloor_DoesNotApplyToNonWebsocket(t *testing.T) {
	ctx := context.Background()
	p, svc := newFloorTestProtocol(t, ctx)
	serviceID := protocol.ServiceID("test-service")

	const addr = protocol.EndpointAddr("supplier1-https://bad.example.com")
	endpoints := map[protocol.EndpointAddr]endpoint{addr: &mockEndpoint{addr: addr}}
	tankHTTPScore(t, ctx, svc, serviceID, addr)

	// REST filtering consults only the :rest key, which has no signals, so the endpoint stays.
	filtered := p.filterByReputation(ctx, serviceID, endpoints, sharedtypes.RPCType_REST, polyzero.NewLogger(), "")
	require.Contains(t, filtered, addr, "the floor must not apply to non-websocket rpc types")
}

// requestedEndpointAddr is race protection: a pre-selected endpoint must never be dropped out
// from under the caller, or the caller errors with "Selected endpoint is not available".
func TestWebsocketHTTPFloor_HonorsRequestedEndpointEscapeHatch(t *testing.T) {
	ctx := context.Background()
	p, svc := newFloorTestProtocol(t, ctx)
	serviceID := protocol.ServiceID("test-service")

	const bad = protocol.EndpointAddr("supplier1-https://bad.example.com")
	endpoints := map[protocol.EndpointAddr]endpoint{bad: &mockEndpoint{addr: bad}}
	tankHTTPScore(t, ctx, svc, serviceID, bad)

	filtered := p.filterByReputation(ctx, serviceID, endpoints, sharedtypes.RPCType_WEBSOCKET, polyzero.NewLogger(), bad)
	require.Contains(t, filtered, bad, "pre-selected endpoint must survive the floor")
}

// The floor must never empty the pool: when every endpoint is HTTP-bad, the pool-collapse
// guard has to keep the least-bad tier rather than dropping selection to a reputation-blind
// fallback (which previously made a fully-degraded service worse).
func TestWebsocketHTTPFloor_PoolCollapseGuardStillApplies(t *testing.T) {
	ctx := context.Background()
	p, svc := newFloorTestProtocol(t, ctx)
	serviceID := protocol.ServiceID("test-service")

	addrs := []protocol.EndpointAddr{
		"supplier1-https://a.example.com",
		"supplier2-https://b.example.com",
	}
	endpoints := map[protocol.EndpointAddr]endpoint{}
	for _, a := range addrs {
		endpoints[a] = &mockEndpoint{addr: a}
		tankHTTPScore(t, ctx, svc, serviceID, a)
	}

	filtered := p.filterByReputation(ctx, serviceID, endpoints, sharedtypes.RPCType_WEBSOCKET, polyzero.NewLogger(), "")
	require.NotEmpty(t, filtered, "floor must not collapse the pool to empty")
}

// With the floor disabled the HTTP score must be ignored entirely.
func TestWebsocketHTTPFloor_DisabledViaConfig(t *testing.T) {
	ctx := context.Background()
	p, svc := newFloorTestProtocol(t, ctx)
	serviceID := protocol.ServiceID("test-service")

	disabled := false
	p.unifiedServicesConfig = &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{ID: serviceID, WebsocketHTTPScoreFloor: &disabled},
		},
	}

	const bad = protocol.EndpointAddr("supplier1-https://bad.example.com")
	const good = protocol.EndpointAddr("supplier2-https://good.example.com")
	endpoints := map[protocol.EndpointAddr]endpoint{
		bad:  &mockEndpoint{addr: bad},
		good: &mockEndpoint{addr: good},
	}
	tankHTTPScore(t, ctx, svc, serviceID, bad)

	filtered := p.filterByReputation(ctx, serviceID, endpoints, sharedtypes.RPCType_WEBSOCKET, polyzero.NewLogger(), "")
	require.Contains(t, filtered, bad, "floor disabled: HTTP score must be ignored")
	require.Contains(t, filtered, good)
}
