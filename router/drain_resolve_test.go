package router

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

func drainTestEndpoints() []protocol.EndpointDetails {
	return []protocol.EndpointDetails{
		{Address: "pokt1aaa-https://f019.spacebelt.xyz", SupplierAddress: "pokt1aaa", URL: "https://f019.spacebelt.xyz"},
		{Address: "pokt1bbb-https://f026.spacebelt.xyz", SupplierAddress: "pokt1bbb", URL: "https://f026.spacebelt.xyz"},
		{Address: "pokt1ccc-https://r001.rpcgate.xyz", SupplierAddress: "pokt1ccc", URL: "https://r001.rpcgate.xyz"},
		{Address: "pokt1ddd-https://node.kalorius.tech", SupplierAddress: "pokt1ddd", URL: "https://node.kalorius.tech"},
	}
}

// An operator is named by its eTLD+1 on a dashboard, so that must be the accepted input.
func TestResolveDrainDomain_ByOperatorDomain(t *testing.T) {
	domain, matched := resolveDrainDomain(drainTestEndpoints(), "spacebelt.xyz")

	require.Equal(t, "spacebelt.xyz", domain)
	require.Equal(t, 2, matched, "both spacebelt backends must resolve")
}

// A hostname must still bench the whole OPERATOR, not just that machine. Benching one
// hostname would be defeated the moment a session rotated in a sibling machine — the same
// class of bug as keying the drain on endpoint addresses.
func TestResolveDrainDomain_HostnameBenchesTheOperator(t *testing.T) {
	domain, matched := resolveDrainDomain(drainTestEndpoints(), "f019.spacebelt.xyz")

	require.Equal(t, "spacebelt.xyz", domain,
		"a hostname must widen to the operator, or a rotation defeats the drain")
	require.Equal(t, 1, matched)
}

func TestResolveDrainDomain_ByFullURL(t *testing.T) {
	domain, matched := resolveDrainDomain(drainTestEndpoints(), "https://r001.rpcgate.xyz/some/path")

	require.Equal(t, "rpcgate.xyz", domain, "a full URL must reduce to its operator domain")
	require.Equal(t, 1, matched)
}

// A target naming nothing resolves to no domain here; the handler then falls back to the
// literal registrable domain so the drain still applies to endpoints that rotate in later.
func TestResolveDrainDomain_UnknownTarget(t *testing.T) {
	domain, matched := resolveDrainDomain(drainTestEndpoints(), "typo.example")

	require.Zero(t, matched)
	require.Empty(t, domain)
}

func TestResolveDrainDomain_CaseInsensitive(t *testing.T) {
	domain, matched := resolveDrainDomain(drainTestEndpoints(), "SpaceBelt.XYZ")
	require.Equal(t, "spacebelt.xyz", domain)
	require.Equal(t, 2, matched)
}

func TestRegistrableDomain(t *testing.T) {
	require.Equal(t, "spacebelt.xyz", registrableDomain("f019.spacebelt.xyz"))
	require.Equal(t, "example.com", registrableDomain("rm-01.eu.example.com"))
	require.Equal(t, "example.com", registrableDomain("example.com"))
	require.Equal(t, "localhost", registrableDomain("localhost"))
}
