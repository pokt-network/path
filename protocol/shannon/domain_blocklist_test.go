package shannon

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
)

// =============================================================================
// Domain blocklist — the nuclear ban
// =============================================================================
//
// Everything below asserts through the PRODUCTION callers — getSessionsUniqueEndpoints,
// getUniqueEndpoints, GetEndpointsForHealthCheck — not through the blocklist's own maps.
// Four drain bugs shipped with passing tests because the tests asserted on state the
// author wrote instead of the value the caller receives; see selection_harness_test.go.

func compileBlocklist(t *testing.T, entries ...gateway.BlockedDomainConfig) *domainBlocklist {
	t.Helper()
	bl, err := newDomainBlocklist(entries)
	require.NoError(t, err)
	return bl
}

// --- matcher units --------------------------------------------------------------------

func TestDomainBlocklist_MatchesETLDPlusOneAndExactHost(t *testing.T) {
	bl := compileBlocklist(t,
		gateway.BlockedDomainConfig{Domain: "rpcgate.xyz"},
		gateway.BlockedDomainConfig{Domain: "n1.kalorius.tech"},
	)

	// eTLD+1 entry matches every host under it.
	require.True(t, bl.IsBlocked("https://s019.rpcgate.xyz", sharedtypes.RPCType_JSON_RPC))
	require.True(t, bl.IsBlocked("wss://other.rpcgate.xyz/ws", sharedtypes.RPCType_WEBSOCKET))

	// Exact-hostname entry matches only that host.
	require.True(t, bl.IsBlocked("https://n1.kalorius.tech", sharedtypes.RPCType_JSON_RPC))
	require.False(t, bl.IsBlocked("https://n2.kalorius.tech", sharedtypes.RPCType_JSON_RPC),
		"an exact-hostname entry must not ban the operator's other hosts")

	require.False(t, bl.IsBlocked("https://f019.spacebelt.xyz", sharedtypes.RPCType_JSON_RPC))
}

func TestDomainBlocklist_RPCTypeScoping(t *testing.T) {
	bl := compileBlocklist(t,
		gateway.BlockedDomainConfig{Domain: "spacebelt.xyz", RPCTypes: []string{"websocket"}},
		gateway.BlockedDomainConfig{Domain: "rpcgate.xyz"}, // all types
	)

	require.True(t, bl.IsBlocked("wss://f019.spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET))
	require.False(t, bl.IsBlocked("https://f019.spacebelt.xyz", sharedtypes.RPCType_JSON_RPC),
		"a websocket-only ban must not take the operator's HTTP traffic with it")

	for _, rpcType := range []sharedtypes.RPCType{
		sharedtypes.RPCType_JSON_RPC, sharedtypes.RPCType_WEBSOCKET,
		sharedtypes.RPCType_REST, sharedtypes.RPCType_COMET_BFT,
	} {
		require.True(t, bl.IsBlocked("https://s019.rpcgate.xyz", rpcType),
			"an entry with no rpc_types must ban every type (got through on %s)", rpcType)
	}
}

func TestDomainBlocklist_AllTypesEntryAbsorbsNarrowerOne(t *testing.T) {
	// Order must not matter: (ws-only, all) and (all, ws-only) both mean "everything".
	for _, entries := range [][]gateway.BlockedDomainConfig{
		{{Domain: "rpcgate.xyz", RPCTypes: []string{"websocket"}}, {Domain: "rpcgate.xyz"}},
		{{Domain: "rpcgate.xyz"}, {Domain: "rpcgate.xyz", RPCTypes: []string{"websocket"}}},
	} {
		bl := compileBlocklist(t, entries...)
		require.True(t, bl.IsBlocked("https://s019.rpcgate.xyz", sharedtypes.RPCType_JSON_RPC),
			"a narrower entry must never un-ban a type the all-types entry covers")
	}
}

func TestDomainBlocklist_EnvParsing(t *testing.T) {
	entries := parseBlockedDomainsEnv(" rpcgate.xyz:websocket , spacebelt.xyz:websocket|json_rpc ,evil.example,, ")
	require.Equal(t, []gateway.BlockedDomainConfig{
		{Domain: "rpcgate.xyz", RPCTypes: []string{"websocket"}},
		{Domain: "spacebelt.xyz", RPCTypes: []string{"websocket", "json_rpc"}},
		{Domain: "evil.example"},
	}, entries)

	require.Nil(t, parseBlockedDomainsEnv(""))
	require.Nil(t, parseBlockedDomainsEnv("   "))
}

func TestDomainBlocklist_RejectsBadConfig(t *testing.T) {
	_, err := newDomainBlocklist([]gateway.BlockedDomainConfig{
		{Domain: "rpcgate.xyz", RPCTypes: []string{"websockets"}}, // typo
	})
	require.Error(t, err, "an unknown rpc_type must refuse to boot, not silently narrow the ban")

	_, err = newDomainBlocklist([]gateway.BlockedDomainConfig{{Domain: "  "}})
	require.Error(t, err, "an empty domain must refuse to boot")
}

func TestDomainBlocklist_NilBlocksNothing(t *testing.T) {
	var bl *domainBlocklist
	require.False(t, bl.IsBlocked("https://s019.rpcgate.xyz", sharedtypes.RPCType_JSON_RPC))

	empty, err := newDomainBlocklist(nil)
	require.NoError(t, err)
	require.Nil(t, empty)
}

// --- production caller: getSessionsUniqueEndpoints ------------------------------------

// blocklistScenario drives the REAL selection entry point (getSessionsUniqueEndpoints)
// with a session whose endpoint map is pre-seeded into the session cache — the same
// harnessEndpoint fixtures the selection harness uses, with the supplier address and
// backend URL kept separate so a session rollover can be simulated.
type blocklistScenario struct {
	t         *testing.T
	p         *Protocol
	serviceID protocol.ServiceID
	sessionID string
	urls      []string
	// supplierOf maps a URL to the supplier fronting it; RotateSuppliers swaps these.
	supplierOf map[string]string
}

func newBlocklistScenario(
	t *testing.T,
	serviceID string,
	entries []gateway.BlockedDomainConfig,
	urls ...string,
) *blocklistScenario {
	t.Helper()
	s := &blocklistScenario{
		t:          t,
		p:          &Protocol{logger: polyzero.NewLogger(), blockedDomains: compileBlocklist(t, entries...)},
		serviceID:  protocol.ServiceID(serviceID),
		sessionID:  "session-gen0",
		urls:       append([]string(nil), urls...),
		supplierOf: make(map[string]string, len(urls)),
	}
	for i, u := range urls {
		s.supplierOf[u] = supplierAddrForIndex(i, 0)
	}
	s.seedSession()
	return s
}

func (s *blocklistScenario) seedSession() {
	eps := make(map[protocol.EndpointAddr]endpoint, len(s.urls))
	for _, u := range s.urls {
		ep := &harnessEndpoint{supplier: s.supplierOf[u], url: u}
		eps[ep.Addr()] = ep
	}
	s.p.sessionEndpointsCache.Store(s.sessionID, eps)
}

// RotateSuppliers simulates a session rollover: same backend URLs, fresh supplier
// addresses, new session ID.
func (s *blocklistScenario) RotateSuppliers(generation int) {
	s.t.Helper()
	s.sessionID = "session-gen" + string(rune('0'+generation))
	for i, u := range s.urls {
		s.supplierOf[u] = supplierAddrForIndex(i, generation)
	}
	s.seedSession()
}

func (s *blocklistScenario) session() sessiontypes.Session {
	return sessiontypes.Session{
		SessionId:   s.sessionID,
		Header:      &sessiontypes.SessionHeader{SessionId: s.sessionID, ServiceId: string(s.serviceID)},
		Application: &apptypes.Application{Address: "pokt1app"},
	}
}

// survivors returns the backend URLs the production selection entry point still hands
// out, under the given call shape (allowlist / preferred endpoint / reputation flag).
func (s *blocklistScenario) survivors(
	rpcType sharedtypes.RPCType,
	filterByReputation bool,
	allowedSuppliers []string,
	preferredURL string,
) []string {
	s.t.Helper()
	var preferred protocol.EndpointAddr
	if preferredURL != "" {
		preferred = protocol.EndpointAddr(s.supplierOf[preferredURL] + "-" + preferredURL)
	}
	got, _, err := s.p.getSessionsUniqueEndpoints(
		context.Background(), s.serviceID, []sessiontypes.Session{s.session()},
		filterByReputation, rpcType, allowedSuppliers, preferred,
	)
	if err != nil {
		// An emptied pool is a legitimate outcome of a nuclear ban — the request fails
		// rather than being served by a banned operator. Anything else is a real error.
		require.ErrorIs(s.t, err, errProtocolContextSetupNoEndpoints)
		return nil
	}
	out := make([]string, 0, len(got))
	for _, ep := range got {
		out = append(out, ep.GetURL(rpcType))
	}
	sort.Strings(out)
	return out
}

func TestSelection_DomainBlocklistRemovesBannedOperator(t *testing.T) {
	s := newBlocklistScenario(t, "gnosis",
		[]gateway.BlockedDomainConfig{{Domain: "spacebelt.xyz", RPCTypes: []string{"websocket"}}},
		spacebeltA, spacebeltB, rpcgateA, kaloriusA)

	got := s.survivors(sharedtypes.RPCType_WEBSOCKET, true, nil, "")
	require.NotContains(t, got, spacebeltA)
	require.NotContains(t, got, spacebeltB)
	require.Contains(t, got, rpcgateA)
	require.Contains(t, got, kaloriusA)
}

func TestSelection_DomainBlocklistScopedToRPCType(t *testing.T) {
	s := newBlocklistScenario(t, "gnosis",
		[]gateway.BlockedDomainConfig{{Domain: "spacebelt.xyz", RPCTypes: []string{"websocket"}}},
		spacebeltA, kaloriusA)

	require.NotContains(t, s.survivors(sharedtypes.RPCType_WEBSOCKET, true, nil, ""), spacebeltA)
	require.Contains(t, s.survivors(sharedtypes.RPCType_JSON_RPC, true, nil, ""), spacebeltA,
		"a websocket ban must not take the operator's HTTP traffic with it")
}

// Target-Suppliers bypasses reputation (including drains — a documented, open gap).
// It must NOT bypass this blocklist: the filter runs before the allowlist, same as
// blocked_suppliers, so a banned operator is unreachable even when explicitly pinned.
func TestSelection_DomainBlocklistIgnoresTargetSuppliers(t *testing.T) {
	s := newBlocklistScenario(t, "gnosis",
		[]gateway.BlockedDomainConfig{{Domain: "spacebelt.xyz", RPCTypes: []string{"websocket"}}},
		spacebeltA, kaloriusA)

	got := s.survivors(sharedtypes.RPCType_WEBSOCKET, true, []string{s.supplierOf[spacebeltA]}, "")
	require.NotContains(t, got, spacebeltA,
		"Target-Suppliers must not resurrect a banned operator — this ban is nuclear")
}

// Drain bug 4's shape: every websocket rebind passes the endpoint it is already bound to
// as the preferred endpoint, and an exemption there means the ban never applies to the
// connections that matter. removeBlockedDomains takes no preferred endpoint at all; this
// pins that the full production path cannot re-pick a banned endpoint either.
func TestSelection_DomainBlocklistAppliesEvenToPreferredEndpoint(t *testing.T) {
	s := newBlocklistScenario(t, "gnosis",
		[]gateway.BlockedDomainConfig{{Domain: "spacebelt.xyz", RPCTypes: []string{"websocket"}}},
		spacebeltA, spacebeltB, rpcgateA, kaloriusA)

	got := s.survivors(sharedtypes.RPCType_WEBSOCKET, true, nil, spacebeltA)
	require.NotContains(t, got, spacebeltA)
	require.NotContains(t, got, spacebeltB)
	require.Contains(t, got, rpcgateA)
}

// Drain bug 2's shape: state keyed on EndpointAddr silently lifts at the next rollover,
// because the supplier set rotates while the backend URLs stay. The ban matches on the
// live URL, so it must survive any number of rotations.
func TestSelection_DomainBlocklistSurvivesSupplierRotation(t *testing.T) {
	s := newBlocklistScenario(t, "gnosis",
		[]gateway.BlockedDomainConfig{{Domain: "spacebelt.xyz", RPCTypes: []string{"websocket"}}},
		spacebeltA, rpcgateA, kaloriusA)

	require.NotContains(t, s.survivors(sharedtypes.RPCType_WEBSOCKET, true, nil, ""), spacebeltA)

	s.RotateSuppliers(1)
	require.NotContains(t, s.survivors(sharedtypes.RPCType_WEBSOCKET, true, nil, ""), spacebeltA)

	s.RotateSuppliers(2)
	require.NotContains(t, s.survivors(sharedtypes.RPCType_WEBSOCKET, true, nil, ""), spacebeltA)
}

// Health checks and leaderboard gathering call with filterByReputation=false, which
// skips reputation, drains, the supplier blacklist and session-exhaustion. The domain
// blocklist must NOT be among the things that flag turns off.
func TestSelection_DomainBlocklistAppliesToReputationFreeCalls(t *testing.T) {
	s := newBlocklistScenario(t, "gnosis",
		[]gateway.BlockedDomainConfig{{Domain: "spacebelt.xyz", RPCTypes: []string{"websocket"}}},
		spacebeltA, kaloriusA)

	require.NotContains(t, s.survivors(sharedtypes.RPCType_WEBSOCKET, false, nil, ""), spacebeltA,
		"filterByReputation=false must not bypass the domain blocklist")
}

// Unlike a drain — which yields rather than empty the pool, because a drain is a
// preference — the nuclear ban empties it. Serving a request from an operator the config
// says must never serve it is worse than failing the request.
func TestSelection_DomainBlocklistCanEmptyThePool(t *testing.T) {
	s := newBlocklistScenario(t, "gnosis",
		[]gateway.BlockedDomainConfig{{Domain: "spacebelt.xyz"}},
		spacebeltA, spacebeltB)

	require.Empty(t, s.survivors(sharedtypes.RPCType_JSON_RPC, true, nil, ""),
		"a nuclear ban must hold even when the banned operator is all that remains")
}

// --- production caller: getUniqueEndpoints (fallback endpoints) -----------------------

// Fallback endpoints are handed out raw by getUniqueEndpoints, bypassing every
// session-endpoint filter — the ban must cover them explicitly or it has a
// fallback-shaped hole.
func TestGetUniqueEndpoints_DomainBlocklistCoversFallbackEndpoints(t *testing.T) {
	p := &Protocol{
		logger: polyzero.NewLogger(),
		blockedDomains: compileBlocklist(t,
			gateway.BlockedDomainConfig{Domain: "spacebelt.xyz"}),
		serviceFallbackMap: map[protocol.ServiceID]serviceFallback{
			"gnosis": {
				Endpoints: map[protocol.EndpointAddr]endpoint{
					"fb-spacebelt": &harnessEndpoint{supplier: "fallback", url: spacebeltA},
					"fb-kalorius":  &harnessEndpoint{supplier: "fallback", url: kaloriusA},
				},
				SendAllTraffic: true,
			},
		},
	}

	got, _, err := p.getUniqueEndpoints(
		context.Background(), "gnosis", nil, true, sharedtypes.RPCType_JSON_RPC, nil, "")
	require.NoError(t, err)

	urls := make([]string, 0, len(got))
	for _, ep := range got {
		urls = append(urls, ep.GetURL(sharedtypes.RPCType_JSON_RPC))
	}
	require.NotContains(t, urls, spacebeltA, "send-all-traffic fallback must still honor the ban")
	require.Contains(t, urls, kaloriusA)

	// The shared config map must not have been mutated by the filter.
	require.Len(t, p.serviceFallbackMap["gnosis"].Endpoints, 2,
		"the filter must clone the shared fallback map, never mutate it")
}

func TestGetUniqueEndpoints_BannedFallbackCannotRescueEmptyPool(t *testing.T) {
	p := &Protocol{
		logger: polyzero.NewLogger(),
		blockedDomains: compileBlocklist(t,
			gateway.BlockedDomainConfig{Domain: "spacebelt.xyz"}),
		serviceFallbackMap: map[protocol.ServiceID]serviceFallback{
			"gnosis": {
				Endpoints: map[protocol.EndpointAddr]endpoint{
					"fb-spacebelt": &harnessEndpoint{supplier: "fallback", url: spacebeltA},
				},
			},
		},
	}

	// No sessions, and the only fallback is banned: the request must fail rather than
	// be served by the banned operator.
	_, _, err := p.getUniqueEndpoints(
		context.Background(), "gnosis", nil, true, sharedtypes.RPCType_JSON_RPC, nil, "")
	require.Error(t, err)
}

// --- production caller: NewProtocol (the wiring itself) -------------------------------

// Every scenario above hand-builds Protocol{blockedDomains: ...}, which leaves the one
// step none of them can see: does NewProtocol actually compile the config + env into the
// field selection reads? Deleting the constructor wiring would keep every other test
// green while shipping an inert ban — the exact shape of all four drain bugs.

type constructorFullNode struct{ FullNode }

func (f *constructorFullNode) GetAccountClient() *sdk.AccountClient { return &sdk.AccountClient{} }

// Any valid secp256k1 scalar works; this key exists only so newSigner constructs.
const constructorTestKeyHex = "0000000000000000000000000000000000000000000000000000000000000001"

func newProtocolViaConstructor(t *testing.T) (*Protocol, error) {
	t.Helper()
	// Keep the constructor goroutine-free: no throughput sampler, no reputation service.
	t.Setenv("PATH_WEBSOCKET_SESSION_REBIND", "false")
	return NewProtocol(context.Background(), polyzero.NewLogger(), GatewayConfig{
		GatewayAddress:       "pokt1gateway",
		GatewayPrivateKeyHex: constructorTestKeyHex,
		BlockedDomains: []gateway.BlockedDomainConfig{
			{Domain: "cfgonly.example", RPCTypes: []string{"json_rpc"}},
		},
	}, &constructorFullNode{})
}

func TestNewProtocol_WiresDomainBlocklistFromConfigAndEnv(t *testing.T) {
	t.Setenv(envBlockedDomains, "envonly.example:websocket")

	p, err := newProtocolViaConstructor(t)
	require.NoError(t, err)

	// Both sources must reach the compiled blocklist (env is a union with config).
	require.True(t, p.blockedDomains.IsBlocked("https://a.cfgonly.example", sharedtypes.RPCType_JSON_RPC))
	require.True(t, p.blockedDomains.IsBlocked("wss://a.envonly.example", sharedtypes.RPCType_WEBSOCKET))

	// And the constructed instance must actually EXCLUDE through real selection — the
	// field being set is necessary but not sufficient.
	eps := make(map[protocol.EndpointAddr]endpoint)
	for i, u := range []string{"wss://a.envonly.example", kaloriusA} {
		ep := &harnessEndpoint{supplier: supplierAddrForIndex(i, 0), url: u}
		eps[ep.Addr()] = ep
	}
	p.sessionEndpointsCache.Store("session-ctor", eps)
	session := sessiontypes.Session{
		SessionId:   "session-ctor",
		Header:      &sessiontypes.SessionHeader{SessionId: "session-ctor", ServiceId: "gnosis"},
		Application: &apptypes.Application{Address: "pokt1app"},
	}
	got, _, err := p.getSessionsUniqueEndpoints(
		context.Background(), "gnosis", []sessiontypes.Session{session},
		true, sharedtypes.RPCType_WEBSOCKET, nil, "")
	require.NoError(t, err)
	urls := make([]string, 0, len(got))
	for _, ep := range got {
		urls = append(urls, ep.GetURL(sharedtypes.RPCType_WEBSOCKET))
	}
	require.NotContains(t, urls, "wss://a.envonly.example",
		"a ban present only in the env var must exclude through the constructed Protocol")
	require.Contains(t, urls, kaloriusA)
}

func TestNewProtocol_RefusesToBootOnMalformedEnvBan(t *testing.T) {
	t.Setenv(envBlockedDomains, "envonly.example:websockets") // typo'd rpc type

	_, err := newProtocolViaConstructor(t)
	require.Error(t, err,
		"a malformed nuclear ban must refuse to boot, not silently drop the entry")
}

// --- production caller: GetEndpointsForHealthCheck ------------------------------------

// healthCheckFullNode serves a fixed session; block-height and shared-params queries
// fail, which GetEndpointsForHealthCheck treats leniently (no session-expiry filter).
type healthCheckFullNode struct {
	FullNode
	session sessiontypes.Session
}

func (f *healthCheckFullNode) IsInSessionRollover() bool { return false }
func (f *healthCheckFullNode) GetSession(_ context.Context, _ protocol.ServiceID, _ string) (sessiontypes.Session, error) {
	return f.session, nil
}

// Failing the height lookup exercises the lenient path: no session-expiry filtering.
func (f *healthCheckFullNode) GetCurrentBlockHeight(_ context.Context) (int64, error) {
	return 0, errors.New("no chain in this test")
}
func (f *healthCheckFullNode) GetSessionWithExtendedValidity(ctx context.Context, serviceID protocol.ServiceID, appAddr string) (sessiontypes.Session, error) {
	return f.GetSession(ctx, serviceID, appAddr)
}

func newHealthCheckBlocklistProtocol(t *testing.T, entries ...gateway.BlockedDomainConfig) *Protocol {
	t.Helper()
	session := sessiontypes.Session{
		SessionId: "session-hc",
		Header:    &sessiontypes.SessionHeader{SessionId: "session-hc", ServiceId: "gnosis", ApplicationAddress: "pokt1app"},
		// The delegation check runs against the session's embedded Application.
		Application: &apptypes.Application{
			Address:                   "pokt1app",
			DelegateeGatewayAddresses: []string{"pokt1gateway"},
		},
	}
	p := &Protocol{
		logger:         polyzero.NewLogger(),
		FullNode:       &healthCheckFullNode{session: session},
		gatewayMode:    protocol.GatewayModeCentralized,
		gatewayAddr:    "pokt1gateway",
		ownedApps:      map[protocol.ServiceID][]string{"gnosis": {"pokt1app"}},
		blockedDomains: compileBlocklist(t, entries...),
		unifiedServicesConfig: &gateway.UnifiedServicesConfig{
			Services: []gateway.ServiceConfig{{
				ID: "gnosis",
				HealthChecks: &gateway.ServiceHealthCheckOverride{
					Local: []gateway.HealthCheckConfig{
						{Name: "block", Type: gateway.HealthCheckTypeJSONRPC},
						{Name: "ws", Type: gateway.HealthCheckTypeWebSocket},
					},
				},
			}},
		},
	}

	eps := make(map[protocol.EndpointAddr]endpoint)
	for i, u := range []string{spacebeltA, kaloriusA} {
		ep := &harnessEndpoint{supplier: supplierAddrForIndex(i, 0), url: u}
		eps[ep.Addr()] = ep
	}
	p.sessionEndpointsCache.Store("session-hc", eps)
	return p
}

// Health checks are paid relays; an all-type ban must stop the probes entirely, not
// just the user traffic.
func TestGetEndpointsForHealthCheck_AllTypeBanExcludesEndpoint(t *testing.T) {
	p := newHealthCheckBlocklistProtocol(t,
		gateway.BlockedDomainConfig{Domain: "spacebelt.xyz"})

	infos, err := p.GetEndpointsForHealthCheck()("gnosis")
	require.NoError(t, err)
	require.NotEmpty(t, infos, "the unbanned endpoint must still be probed")

	for _, info := range infos {
		require.NotContains(t, string(info.Addr), "spacebelt.xyz",
			"a fully banned endpoint must receive no health-check probes at all")
	}
}

// A websocket-only ban is enforced by omitting WebSocketURL — the executor gates its
// websocket probe on WebSocketURL != "" — while HTTP probes continue.
func TestGetEndpointsForHealthCheck_WebsocketBanBlanksWebSocketURL(t *testing.T) {
	p := newHealthCheckBlocklistProtocol(t,
		gateway.BlockedDomainConfig{Domain: "spacebelt.xyz", RPCTypes: []string{"websocket"}})

	infos, err := p.GetEndpointsForHealthCheck()("gnosis")
	require.NoError(t, err)

	var sawSpacebelt, sawKalorius bool
	for _, info := range infos {
		switch info.HTTPURL {
		case spacebeltA:
			sawSpacebelt = true
			require.Empty(t, info.WebSocketURL,
				"a websocket ban must strip the WebSocket probe URL")
		case kaloriusA:
			sawKalorius = true
			require.NotEmpty(t, info.WebSocketURL,
				"the unbanned endpoint must keep its WebSocket probe")
		}
	}
	require.True(t, sawSpacebelt, "a websocket-only ban must not remove the endpoint's HTTP probes")
	require.True(t, sawKalorius)
}
