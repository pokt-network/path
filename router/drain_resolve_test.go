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

// An operator is named by its eTLD+1 on a dashboard, so that must be the accepted input —
// requiring node ids is what made the endpoint unusable in practice.
func TestResolveDrainIdentifiers_ByOperatorDomain(t *testing.T) {
	ids, matched := resolveDrainIdentifiers(drainTestEndpoints(), "spacebelt.xyz")

	require.Equal(t, 2, matched, "both spacebelt backends must resolve")
	require.Subset(t, ids, []string{
		"pokt1aaa", "pokt1bbb",
		"https://f019.spacebelt.xyz", "https://f026.spacebelt.xyz",
		"f019.spacebelt.xyz", "f026.spacebelt.xyz",
		"spacebelt.xyz",
		"pokt1aaa-https://f019.spacebelt.xyz",
	}, "every granularity a reputation key could use must be emitted")

	// Nothing belonging to another operator may leak in.
	for _, id := range ids {
		require.NotContains(t, id, "rpcgate")
		require.NotContains(t, id, "kalorius")
		require.NotEqual(t, "pokt1ccc", id)
		require.NotEqual(t, "pokt1ddd", id)
	}
}

func TestResolveDrainIdentifiers_BySingleHostname(t *testing.T) {
	ids, matched := resolveDrainIdentifiers(drainTestEndpoints(), "f019.spacebelt.xyz")

	require.Equal(t, 1, matched, "a hostname must select exactly one backend")
	require.Contains(t, ids, "pokt1aaa")
	require.NotContains(t, ids, "pokt1bbb", "a sibling backend must not be dragged in")
}

func TestResolveDrainIdentifiers_ByFullURL(t *testing.T) {
	ids, matched := resolveDrainIdentifiers(drainTestEndpoints(), "https://r001.rpcgate.xyz/some/path")

	require.Equal(t, 1, matched, "a full URL must reduce to its host")
	require.Contains(t, ids, "pokt1ccc")
}

// A target naming nothing must produce an empty set, so the drain benches nothing rather
// than falling back to something broader.
func TestResolveDrainIdentifiers_UnknownTargetResolvesToNothing(t *testing.T) {
	ids, matched := resolveDrainIdentifiers(drainTestEndpoints(), "typo.example")

	require.Zero(t, matched)
	require.Empty(t, ids)
}

func TestResolveDrainIdentifiers_CaseInsensitive(t *testing.T) {
	ids, matched := resolveDrainIdentifiers(drainTestEndpoints(), "SpaceBelt.XYZ")
	require.Equal(t, 2, matched)
	require.Contains(t, ids, "pokt1aaa")
}

func TestRegistrableDomain(t *testing.T) {
	require.Equal(t, "spacebelt.xyz", registrableDomain("f019.spacebelt.xyz"))
	require.Equal(t, "example.com", registrableDomain("rm-01.eu.example.com"))
	require.Equal(t, "example.com", registrableDomain("example.com"))
	require.Equal(t, "localhost", registrableDomain("localhost"))
}
