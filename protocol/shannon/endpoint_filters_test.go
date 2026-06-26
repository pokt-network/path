package shannon

import (
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

// cfgEndpoint is a fully-configurable endpoint mock for filter tests.
type cfgEndpoint struct {
	addr      protocol.EndpointAddr
	supplier  string
	sessionID string
}

var _ endpoint = (*cfgEndpoint)(nil)

func (e *cfgEndpoint) Addr() protocol.EndpointAddr       { return e.addr }
func (e *cfgEndpoint) PublicURL() string                 { return string(e.addr) }
func (e *cfgEndpoint) WebsocketURL() (string, error)     { return "", nil }
func (e *cfgEndpoint) Supplier() string                  { return e.supplier }
func (e *cfgEndpoint) GetURL(sharedtypes.RPCType) string { return string(e.addr) }
func (e *cfgEndpoint) IsFallback() bool                  { return false }
func (e *cfgEndpoint) Session() *sessiontypes.Session {
	return &sessiontypes.Session{SessionId: e.sessionID}
}

// makeEndpoints builds a working map keyed by "supplier-url" addresses.
func makeEndpoints(suppliers ...string) (map[protocol.EndpointAddr]endpoint, map[string]protocol.EndpointAddr) {
	eps := make(map[protocol.EndpointAddr]endpoint, len(suppliers))
	bySupplier := make(map[string]protocol.EndpointAddr, len(suppliers))
	for _, s := range suppliers {
		addr := protocol.EndpointAddr(s + "-https://" + s + ".example.com")
		eps[addr] = &cfgEndpoint{addr: addr, supplier: s, sessionID: "sess1"}
		bySupplier[s] = addr
	}
	return eps, bySupplier
}

func testLogger() polylog.Logger {
	return polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn")))
}

func suppliersIn(eps map[protocol.EndpointAddr]endpoint) map[string]bool {
	out := make(map[string]bool, len(eps))
	for _, ep := range eps {
		out[ep.Supplier()] = true
	}
	return out
}

func TestRemoveBlockedSuppliers(t *testing.T) {
	eps, _ := makeEndpoints("a", "b", "c")
	removed := removeBlockedSuppliers(eps, []string{"b"}, testLogger())
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	got := suppliersIn(eps)
	if got["b"] || !got["a"] || !got["c"] {
		t.Fatalf("unexpected survivors: %v", got)
	}

	// Empty blocklist is a no-op.
	eps2, _ := makeEndpoints("a", "b")
	if r := removeBlockedSuppliers(eps2, nil, testLogger()); r != 0 || len(eps2) != 2 {
		t.Fatalf("empty blocklist changed state: removed=%d len=%d", r, len(eps2))
	}
}

func TestRetainAllowedSuppliers(t *testing.T) {
	eps, by := makeEndpoints("a", "b", "c")
	removed := retainAllowedSuppliers(eps, []string{"a"}, protocol.EndpointAddr(""), testLogger())
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	if got := suppliersIn(eps); !got["a"] || got["b"] || got["c"] {
		t.Fatalf("unexpected survivors: %v", got)
	}

	// Requested endpoint is always kept even if its supplier is not allowed.
	eps2, by2 := makeEndpoints("a", "b", "c")
	removed = retainAllowedSuppliers(eps2, []string{"a"}, by2["b"], testLogger())
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if got := suppliersIn(eps2); !got["a"] || !got["b"] || got["c"] {
		t.Fatalf("requested endpoint not kept: %v (requested=%s)", got, by["b"])
	}
}

func TestRemoveBlacklistedSuppliers(t *testing.T) {
	isBlacklisted := func(s string) bool { return s == "b" }

	eps, _ := makeEndpoints("a", "b", "c")
	removed := removeBlacklistedSuppliers(eps, protocol.EndpointAddr(""), isBlacklisted, testLogger())
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if got := suppliersIn(eps); got["b"] {
		t.Fatalf("blacklisted supplier survived: %v", got)
	}

	// Requested endpoint bypasses the blacklist.
	eps2, by2 := makeEndpoints("a", "b", "c")
	removed = removeBlacklistedSuppliers(eps2, by2["b"], isBlacklisted, testLogger())
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 (requested bypasses blacklist)", removed)
	}
	if got := suppliersIn(eps2); !got["b"] {
		t.Fatalf("requested blacklisted endpoint should be kept: %v", got)
	}
}

func TestFilterExhaustedSuppliers_Partial(t *testing.T) {
	isExhausted := func(ep endpoint) bool { return ep.Supplier() == "b" }

	eps, _ := makeEndpoints("a", "b", "c")
	skipped, applied := filterExhaustedSuppliers(eps, protocol.EndpointAddr(""), isExhausted, testLogger())
	if skipped != 1 || !applied {
		t.Fatalf("skipped=%d applied=%v, want 1/true", skipped, applied)
	}
	if got := suppliersIn(eps); got["b"] || len(eps) != 2 {
		t.Fatalf("exhausted supplier not removed: %v", got)
	}
}

func TestFilterExhaustedSuppliers_AllExhaustedKept(t *testing.T) {
	isExhausted := func(ep endpoint) bool { return true }

	eps, _ := makeEndpoints("a", "b", "c")
	skipped, applied := filterExhaustedSuppliers(eps, protocol.EndpointAddr(""), isExhausted, testLogger())
	if skipped != 3 || applied {
		t.Fatalf("skipped=%d applied=%v, want 3/false (safety net)", skipped, applied)
	}
	if len(eps) != 3 {
		t.Fatalf("safety net should keep all 3 endpoints, got %d", len(eps))
	}
}

func TestFilterExhaustedSuppliers_RequestedKept(t *testing.T) {
	isExhausted := func(ep endpoint) bool { return true }

	eps, by := makeEndpoints("a", "b", "c")
	skipped, applied := filterExhaustedSuppliers(eps, by["a"], isExhausted, testLogger())
	// "a" is the requested endpoint (skipped via continue), so only b and c
	// qualify as exhausted; removing them does not empty the pool.
	if skipped != 2 || !applied {
		t.Fatalf("skipped=%d applied=%v, want 2/true", skipped, applied)
	}
	if got := suppliersIn(eps); !got["a"] || len(eps) != 1 {
		t.Fatalf("requested endpoint must survive: %v", got)
	}
}

func TestFilterExhaustedSuppliers_NoneExhausted(t *testing.T) {
	isExhausted := func(ep endpoint) bool { return false }

	eps, _ := makeEndpoints("a", "b")
	skipped, applied := filterExhaustedSuppliers(eps, protocol.EndpointAddr(""), isExhausted, testLogger())
	if skipped != 0 || applied || len(eps) != 2 {
		t.Fatalf("skipped=%d applied=%v len=%d, want 0/false/2", skipped, applied, len(eps))
	}
}
