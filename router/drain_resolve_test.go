package router

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

func drainTestEndpoints() []protocol.EndpointDetails {
	return []protocol.EndpointDetails{
		{Address: "pokt1aaa-https://f019.op-beta.example", SupplierAddress: "pokt1aaa", URL: "https://f019.op-beta.example"},
		{Address: "pokt1bbb-https://f026.op-beta.example", SupplierAddress: "pokt1bbb", URL: "https://f026.op-beta.example"},
		{Address: "pokt1ccc-https://r001.op-alpha.example", SupplierAddress: "pokt1ccc", URL: "https://r001.op-alpha.example"},
		{Address: "pokt1ddd-https://node.op-gamma.example", SupplierAddress: "pokt1ddd", URL: "https://node.op-gamma.example"},
	}
}

// An operator is named by its eTLD+1 on a dashboard, so that must be the accepted input.
func TestResolveDrainDomain_ByOperatorDomain(t *testing.T) {
	domain, matched := resolveDrainDomain(drainTestEndpoints(), "op-beta.example")

	require.Equal(t, "op-beta.example", domain)
	require.Equal(t, 2, matched, "both operator-beta backends must resolve")
}

// A hostname must still bench the whole OPERATOR, not just that machine. Benching one
// hostname would be defeated the moment a session rotated in a sibling machine — the same
// class of bug as keying the drain on endpoint addresses.
func TestResolveDrainDomain_HostnameBenchesTheOperator(t *testing.T) {
	domain, matched := resolveDrainDomain(drainTestEndpoints(), "f019.op-beta.example")

	require.Equal(t, "op-beta.example", domain,
		"a hostname must widen to the operator, or a rotation defeats the drain")
	require.Equal(t, 1, matched)
}

func TestResolveDrainDomain_ByFullURL(t *testing.T) {
	domain, matched := resolveDrainDomain(drainTestEndpoints(), "https://r001.op-alpha.example/some/path")

	require.Equal(t, "op-alpha.example", domain, "a full URL must reduce to its operator domain")
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
	domain, matched := resolveDrainDomain(drainTestEndpoints(), "Op-Beta.Example")
	require.Equal(t, "op-beta.example", domain)
	require.Equal(t, 2, matched)
}

func TestRegistrableDomain(t *testing.T) {
	require.Equal(t, "op-beta.example", registrableDomain("f019.op-beta.example"))
	require.Equal(t, "example.com", registrableDomain("rm-01.eu.example.com"))
	require.Equal(t, "example.com", registrableDomain("example.com"))
	require.Equal(t, "localhost", registrableDomain("localhost"))
}
