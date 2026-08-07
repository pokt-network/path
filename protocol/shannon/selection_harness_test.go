package shannon

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	apptypes "github.com/pokt-network/poktroll/x/application/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
)

// =============================================================================
// Selection-path test harness
// =============================================================================
//
// WHY THIS EXISTS.
//
// Three separate bugs shipped in the admin-drain feature, all with passing tests, all the
// same mistake: the tests asserted on something the author wrote rather than on the value
// the production caller receives.
//
//  1. The bench was written onto Score.CooldownUntil. refreshFromStorage overwrites the
//     cache from storage unconditionally, so it was erased within a refresh cycle. The test
//     asserted "CooldownUntil is set" — which was true, briefly.
//  2. The bench resolved the target to a fixed set of EndpointKeys. EndpointAddr embeds the
//     supplier address and sessions rotate their supplier set, so the keys went stale every
//     rollover. Every test used a static cache, so nothing rotated.
//  3. The filter deleted from the `endpoints` map passed in, while the function builds its
//     result by walking `cached`. Nothing was ever excluded. The tests asserted on helpers.
//
// In all three the real question was the same one nobody asked: DOES SELECTION STILL RETURN
// THIS ENDPOINT? There was no cheap way to ask it, so each fix grew unit tests around the
// edges instead, and each shipped broken while reporting success in production.
//
// This harness makes that question a single call. Anything that claims to change which
// endpoints are reachable — drains, cooldowns, thresholds, the operator-alias work — should
// be asserted through Survivors(), not through the state it happens to write.
type selectionScenario struct {
	t         *testing.T
	ctx       context.Context
	svc       reputation.ReputationService
	p         *Protocol
	serviceID protocol.ServiceID

	// urls is the backend URL of every endpoint in the scenario, in insertion order.
	urls []string
	// supplierOf maps a URL to the supplier address currently fronting it. RotateSuppliers
	// changes these without touching the URLs — which is exactly what a session rollover
	// does, and what defeated the key-snapshot implementation.
	supplierOf map[string]string
}

// harnessEndpoint keeps the supplier address and the backend URL separate, so a session
// rotation can be simulated by changing one without the other. mockEndpoint collapses them
// into a single field, which made rotation impossible to express — which is part of why the
// rotation bug was never caught.
type harnessEndpoint struct {
	supplier string
	url      string
}

var _ endpoint = (*harnessEndpoint)(nil)

func (e *harnessEndpoint) Addr() protocol.EndpointAddr {
	return protocol.EndpointAddr(e.supplier + "-" + e.url)
}
func (e *harnessEndpoint) PublicURL() string                    { return e.url }
func (e *harnessEndpoint) GetURL(_ sharedtypes.RPCType) string  { return e.url }
func (e *harnessEndpoint) WebsocketURL() (string, error)        { return e.url, nil }
func (e *harnessEndpoint) Supplier() string                     { return e.supplier }
func (e *harnessEndpoint) IsFallback() bool                     { return false }
func (e *harnessEndpoint) Session() *sessiontypes.Session {
	return &sessiontypes.Session{Header: &sessiontypes.SessionHeader{}, Application: &apptypes.Application{}}
}

// newSelectionScenario builds a scenario over the given backend URLs. Each URL gets a
// distinct supplier address, mirroring how a real session fronts one backend per supplier.
func newSelectionScenario(t *testing.T, serviceID string, urls ...string) *selectionScenario {
	t.Helper()

	ctx := context.Background()
	cfg := reputation.Config{Enabled: true, InitialScore: 80, MinThreshold: 30, StorageType: "memory"}
	cfg.HydrateDefaults()

	store := reputationstorage.NewMemoryStorage(cfg.RecoveryTimeout)
	svc := reputation.NewService(cfg, store)
	require.NoError(t, svc.Start(ctx))
	t.Cleanup(func() { _ = svc.Stop() })

	s := &selectionScenario{
		t: t, ctx: ctx, svc: svc,
		p:          &Protocol{logger: polyzero.NewLogger(), reputationService: svc},
		serviceID:  protocol.ServiceID(serviceID),
		urls:       append([]string(nil), urls...),
		supplierOf: make(map[string]string, len(urls)),
	}
	for i, u := range urls {
		s.supplierOf[u] = supplierAddrForIndex(i, 0)
	}
	return s
}

func supplierAddrForIndex(i, generation int) string {
	return "pokt1gen" + string(rune('a'+generation)) + "supplier" + string(rune('0'+i%10))
}

// endpointMap builds the map that selection is handed.
func (s *selectionScenario) endpointMap() map[protocol.EndpointAddr]endpoint {
	out := make(map[protocol.EndpointAddr]endpoint, len(s.urls))
	for _, u := range s.urls {
		ep := &harnessEndpoint{supplier: s.supplierOf[u], url: u}
		out[ep.Addr()] = ep
	}
	return out
}

// Survivors returns the backend URLs selection would still consider, sorted.
//
// THIS is the production-visible outcome. Assert on it rather than on scores, cooldowns,
// drain maps or gauges — all four have reported a bench that selection did not honour.
func (s *selectionScenario) Survivors(rpcType sharedtypes.RPCType) []string {
	s.t.Helper()
	filtered := s.p.filterByReputation(
		s.ctx, s.serviceID, s.endpointMap(), rpcType, polyzero.NewLogger(), "",
	)
	out := make([]string, 0, len(filtered))
	for _, ep := range filtered {
		out = append(out, ep.GetURL(rpcType))
	}
	sort.Strings(out)
	return out
}

// AssertServes fails unless selection returns exactly the given URLs.
func (s *selectionScenario) AssertServes(rpcType sharedtypes.RPCType, want ...string) {
	s.t.Helper()
	sort.Strings(want)
	require.Equal(s.t, want, s.Survivors(rpcType),
		"selection returned the wrong endpoint set for %s", rpcTypeName(rpcType))
}

// AssertExcluded fails unless every given URL is absent from selection.
func (s *selectionScenario) AssertExcluded(rpcType sharedtypes.RPCType, unwanted ...string) {
	s.t.Helper()
	got := s.Survivors(rpcType)
	for _, u := range unwanted {
		require.NotContains(s.t, got, u,
			"%s must not be selectable for %s — a bench that selection ignores is not a bench",
			u, rpcTypeName(rpcType))
	}
}

// AssertSelectable fails unless every given URL is present in selection.
func (s *selectionScenario) AssertSelectable(rpcType sharedtypes.RPCType, wanted ...string) {
	s.t.Helper()
	got := s.Survivors(rpcType)
	for _, u := range wanted {
		require.Contains(s.t, got, u, "%s must remain selectable for %s", u, rpcTypeName(rpcType))
	}
}

// Drain bans an operator, exactly as the admin endpoint does.
func (s *selectionScenario) Drain(domain string, rpcType sharedtypes.RPCType, d time.Duration) {
	s.t.Helper()
	s.svc.DrainDomain(s.ctx, reputation.DrainRequest{
		ServiceID: s.serviceID, Domain: domain, RPCType: rpcTypeName(rpcType), Duration: d,
	})
}

// Release lifts a ban.
func (s *selectionScenario) Release(domain string, rpcType sharedtypes.RPCType) {
	s.t.Helper()
	s.Drain(domain, rpcType, 0)
}

// DriveIntoCooldown makes an endpoint earn a real cooldown, so tests can tell an earned
// cooldown apart from an admin bench — they must never be conflated.
func (s *selectionScenario) DriveIntoCooldown(url string, rpcType sharedtypes.RPCType) {
	s.t.Helper()
	kb := s.svc.KeyBuilderForService(s.serviceID)
	key := kb.BuildKey(s.serviceID, protocol.EndpointAddr(s.supplierOf[url]+"-"+url), rpcType)
	for i := 0; i < 8; i++ {
		require.NoError(s.t, s.svc.RecordSignal(s.ctx, key,
			reputation.NewCriticalErrorSignal("service_error", 200*time.Millisecond)))
	}
}

// RotateSuppliers re-fronts every backend URL with fresh supplier addresses, leaving the
// URLs untouched.
//
// This is what a session rollover does, and it is the single most important thing this
// harness can express: any state keyed on EndpointAddr silently goes stale here. The
// key-snapshot drain passed every test until this was possible to write.
func (s *selectionScenario) RotateSuppliers(generation int) {
	s.t.Helper()
	for i, u := range s.urls {
		s.supplierOf[u] = supplierAddrForIndex(i, generation)
	}
}

func rpcTypeName(rpcType sharedtypes.RPCType) string {
	return strings.ToLower(rpcType.String())
}

// =============================================================================
// The three shipped bugs, as assertions
// =============================================================================

const (
	spacebeltA = "https://f019.spacebelt.xyz"
	spacebeltB = "https://f026.spacebelt.xyz"
	rpcgateA   = "https://r001.rpcgate.xyz"
	kaloriusA  = "https://n1.kalorius.tech"
)

// BUG 3 — the filter mutated a map its result was not built from, so nothing was excluded
// while the API and the gauge both reported the operator benched.
func TestSelection_DrainRemovesOperatorFromTheReturnedSet(t *testing.T) {
	s := newSelectionScenario(t, "gnosis", spacebeltA, spacebeltB, rpcgateA, kaloriusA)

	s.AssertServes(sharedtypes.RPCType_WEBSOCKET, spacebeltA, spacebeltB, rpcgateA, kaloriusA)

	s.Drain("spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET, time.Hour)
	s.AssertExcluded(sharedtypes.RPCType_WEBSOCKET, spacebeltA, spacebeltB)
	s.AssertSelectable(sharedtypes.RPCType_WEBSOCKET, rpcgateA, kaloriusA)
}

// BUG 2 — the bench was a snapshot of EndpointKeys, so a session rollover silently lifted it.
func TestSelection_DrainSurvivesSupplierRotation(t *testing.T) {
	s := newSelectionScenario(t, "gnosis", spacebeltA, rpcgateA, kaloriusA)

	s.Drain("spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET, time.Hour)
	s.AssertExcluded(sharedtypes.RPCType_WEBSOCKET, spacebeltA)

	// Session N+1: same backends, entirely new supplier addresses.
	s.RotateSuppliers(1)
	s.AssertExcluded(sharedtypes.RPCType_WEBSOCKET, spacebeltA)

	s.RotateSuppliers(2)
	s.AssertExcluded(sharedtypes.RPCType_WEBSOCKET, spacebeltA)
}

// A ban is scoped to one protocol: banning WebSocket must not take the operator's HTTP
// traffic with it.
func TestSelection_DrainIsScopedToRPCType(t *testing.T) {
	s := newSelectionScenario(t, "gnosis", spacebeltA, kaloriusA)

	s.Drain("spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET, time.Hour)
	s.AssertExcluded(sharedtypes.RPCType_WEBSOCKET, spacebeltA)
	s.AssertSelectable(sharedtypes.RPCType_JSON_RPC, spacebeltA)
}

func TestSelection_ReleaseRestoresSelectability(t *testing.T) {
	s := newSelectionScenario(t, "gnosis", spacebeltA, kaloriusA)

	s.Drain("spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET, time.Hour)
	s.AssertExcluded(sharedtypes.RPCType_WEBSOCKET, spacebeltA)

	s.Release("spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET)
	s.AssertSelectable(sharedtypes.RPCType_WEBSOCKET, spacebeltA)
}

// Banning every operator must yield rather than sever the service: a ban is an operator
// preference, not a correctness constraint.
func TestSelection_DrainNeverEmptiesThePool(t *testing.T) {
	s := newSelectionScenario(t, "gnosis", spacebeltA, rpcgateA)

	s.Drain("spacebelt.xyz", sharedtypes.RPCType_WEBSOCKET, time.Hour)
	s.Drain("rpcgate.xyz", sharedtypes.RPCType_WEBSOCKET, time.Hour)

	s.AssertServes(sharedtypes.RPCType_WEBSOCKET, spacebeltA, rpcgateA)
}

// An earned cooldown and an admin bench must both remove an endpoint from selection, and a
// release must lift only the bench — the two are independent by construction, and operators
// rely on that when reading dashboards during an experiment.
func TestSelection_EarnedCooldownAndAdminBenchAreIndependent(t *testing.T) {
	s := newSelectionScenario(t, "gnosis", spacebeltA, rpcgateA, kaloriusA)

	s.DriveIntoCooldown(rpcgateA, sharedtypes.RPCType_JSON_RPC)
	s.Drain("spacebelt.xyz", sharedtypes.RPCType_JSON_RPC, time.Hour)

	s.AssertExcluded(sharedtypes.RPCType_JSON_RPC, spacebeltA, rpcgateA)
	s.AssertSelectable(sharedtypes.RPCType_JSON_RPC, kaloriusA)

	// Lifting the bench must not rescue the endpoint that earned its cooldown.
	s.Release("spacebelt.xyz", sharedtypes.RPCType_JSON_RPC)
	s.AssertSelectable(sharedtypes.RPCType_JSON_RPC, spacebeltA)
	s.AssertExcluded(sharedtypes.RPCType_JSON_RPC, rpcgateA)
}
