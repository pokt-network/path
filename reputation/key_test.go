package reputation

import (
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/protocol"
)

// =============================================================================
// Happy Path Tests
// =============================================================================

func TestKeyBuilder_PerEndpoint(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularityEndpoint)
	require.IsType(t, &EndpointKeyBuilder{}, builder)

	serviceID := protocol.ServiceID("eth")
	endpointAddr := protocol.EndpointAddr("pokt1abc123-https://node.example.com")

	key := builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_JSON_RPC)

	require.Equal(t, serviceID, key.ServiceID)
	require.Equal(t, endpointAddr, key.EndpointAddr)
	require.Equal(t, "eth:pokt1abc123-https://node.example.com:json_rpc", key.String())
}

func TestKeyBuilder_PerDomain(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularityDomain)
	require.IsType(t, &DomainKeyBuilder{}, builder)

	serviceID := protocol.ServiceID("eth")
	endpointAddr := protocol.EndpointAddr("pokt1abc123-https://rm-01.eu.nodefleet.net")

	key := builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_JSON_RPC)

	require.Equal(t, serviceID, key.ServiceID)
	// Should extract domain: nodefleet.net
	require.Equal(t, protocol.EndpointAddr("nodefleet.net"), key.EndpointAddr)
	require.Equal(t, "eth:nodefleet.net:json_rpc", key.String())
}

func TestKeyBuilder_PerDomain_SameDomainDifferentSubdomains(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularityDomain)

	serviceID := protocol.ServiceID("eth")
	endpoint1 := protocol.EndpointAddr("pokt1abc123-https://rm-01.eu.nodefleet.net")
	endpoint2 := protocol.EndpointAddr("pokt1xyz789-https://rm-02.us.nodefleet.net")
	endpoint3 := protocol.EndpointAddr("pokt1def456-https://api.nodefleet.net:8545")

	key1 := builder.BuildKey(serviceID, endpoint1, sharedtypes.RPCType_JSON_RPC)
	key2 := builder.BuildKey(serviceID, endpoint2, sharedtypes.RPCType_JSON_RPC)
	key3 := builder.BuildKey(serviceID, endpoint3, sharedtypes.RPCType_JSON_RPC)

	// All should produce the same key (same domain)
	require.Equal(t, key1, key2, "Same domain should produce same key")
	require.Equal(t, key1, key3, "Same domain should produce same key")
	require.Equal(t, "eth:nodefleet.net:json_rpc", key1.String())
}

func TestKeyBuilder_PerDomain_DifferentDomains(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularityDomain)

	serviceID := protocol.ServiceID("eth")
	endpoint1 := protocol.EndpointAddr("pokt1abc-https://node.nodefleet.net")
	endpoint2 := protocol.EndpointAddr("pokt1xyz-https://relay.pokt.network")

	key1 := builder.BuildKey(serviceID, endpoint1, sharedtypes.RPCType_JSON_RPC)
	key2 := builder.BuildKey(serviceID, endpoint2, sharedtypes.RPCType_JSON_RPC)

	// Different domains should produce different keys
	require.NotEqual(t, key1, key2)
	require.Equal(t, "eth:nodefleet.net:json_rpc", key1.String())
	require.Equal(t, "eth:pokt.network:json_rpc", key2.String())
}

func TestKeyBuilder_PerSupplier(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularitySupplier)
	require.IsType(t, &SupplierKeyBuilder{}, builder)

	serviceID := protocol.ServiceID("eth")
	endpointAddr := protocol.EndpointAddr("pokt1abc123-https://node.example.com")

	key := builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_JSON_RPC)

	require.Equal(t, serviceID, key.ServiceID)
	require.Equal(t, protocol.EndpointAddr("pokt1abc123"), key.EndpointAddr)
	require.Equal(t, "eth:pokt1abc123:json_rpc", key.String())
}

func TestKeyBuilder_PerSupplier_SameSupplierDifferentURLs(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularitySupplier)

	serviceID := protocol.ServiceID("eth")
	endpoint1 := protocol.EndpointAddr("pokt1abc123-https://node1.example.com")
	endpoint2 := protocol.EndpointAddr("pokt1abc123-https://node2.example.com")

	key1 := builder.BuildKey(serviceID, endpoint1, sharedtypes.RPCType_JSON_RPC)
	key2 := builder.BuildKey(serviceID, endpoint2, sharedtypes.RPCType_JSON_RPC)

	// Both should produce the same key (same supplier)
	require.Equal(t, key1, key2)
	require.Equal(t, "eth:pokt1abc123:json_rpc", key1.String())
}

func TestKeyBuilder_DefaultsToPerEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		granularity string
	}{
		{"empty string", ""},
		{"unknown value", "unknown"},
		{"invalid value", "per-invalid"},
		{"typo", "perr-endpoint"},
		{"removed per-service", "per-service"}, // per-service was removed, should default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewKeyBuilder(tt.granularity)
			require.IsType(t, &EndpointKeyBuilder{}, builder)

			// Should behave like per-endpoint
			serviceID := protocol.ServiceID("eth")
			endpointAddr := protocol.EndpointAddr("pokt1abc-https://node.com")

			key := builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_JSON_RPC)
			require.Equal(t, "eth:pokt1abc-https://node.com:json_rpc", key.String())
		})
	}
}

// =============================================================================
// Error Path Tests
// =============================================================================

func TestKeyBuilder_MalformedEndpointAddr_PerSupplier(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularitySupplier)

	tests := []struct {
		name         string
		endpointAddr string
		expectedKey  string
	}{
		{
			name:         "no dash separator",
			endpointAddr: "pokt1abc123https://node.com",
			expectedKey:  "eth:pokt1abc123https://node.com:json_rpc", // Falls back to full addr
		},
		{
			name:         "just supplier address",
			endpointAddr: "pokt1abc123",
			expectedKey:  "eth:pokt1abc123:json_rpc", // Falls back to full addr (same as supplier)
		},
		{
			name:         "just URL",
			endpointAddr: "https://node.com",
			expectedKey:  "eth:https://node.com:json_rpc", // Falls back to full addr
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serviceID := protocol.ServiceID("eth")
			endpointAddr := protocol.EndpointAddr(tt.endpointAddr)

			key := builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_JSON_RPC)
			require.Equal(t, tt.expectedKey, key.String())
		})
	}
}

func TestKeyBuilder_MalformedEndpointAddr_PerDomain(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularityDomain)

	tests := []struct {
		name         string
		endpointAddr string
		expectedKey  string
	}{
		{
			name:         "no dash separator",
			endpointAddr: "pokt1abc123https://node.com",
			expectedKey:  "eth:pokt1abc123https://node.com:json_rpc", // Falls back to full addr
		},
		{
			name:         "just supplier address",
			endpointAddr: "pokt1abc123",
			expectedKey:  "eth:pokt1abc123:json_rpc", // Falls back to full addr
		},
		{
			name:         "malformed URL",
			endpointAddr: "pokt1abc-not-a-url",
			expectedKey:  "eth:pokt1abc-not-a-url:json_rpc", // Falls back to full addr
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serviceID := protocol.ServiceID("eth")
			endpointAddr := protocol.EndpointAddr(tt.endpointAddr)

			key := builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_JSON_RPC)
			require.Equal(t, tt.expectedKey, key.String())
		})
	}
}

func TestKeyBuilder_EmptyServiceID(t *testing.T) {
	builders := []struct {
		name    string
		builder KeyBuilder
	}{
		{"per-endpoint", NewKeyBuilder(KeyGranularityEndpoint)},
		{"per-domain", NewKeyBuilder(KeyGranularityDomain)},
		{"per-supplier", NewKeyBuilder(KeyGranularitySupplier)},
	}

	for _, bb := range builders {
		t.Run(bb.name, func(t *testing.T) {
			serviceID := protocol.ServiceID("")
			endpointAddr := protocol.EndpointAddr("pokt1abc-https://node.com")

			key := bb.builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_JSON_RPC)
			require.Equal(t, protocol.ServiceID(""), key.ServiceID)
			// Should not panic, should create a valid key
			_ = key.String()
		})
	}
}

func TestKeyBuilder_EmptyEndpointAddr(t *testing.T) {
	tests := []struct {
		name        string
		granularity string
		expectedKey string
	}{
		{
			name:        "per-endpoint with empty addr",
			granularity: KeyGranularityEndpoint,
			expectedKey: "eth::json_rpc",
		},
		{
			name:        "per-domain with empty addr",
			granularity: KeyGranularityDomain,
			expectedKey: "eth::json_rpc", // Falls back to empty (no URL to parse)
		},
		{
			name:        "per-supplier with empty addr",
			granularity: KeyGranularitySupplier,
			expectedKey: "eth::json_rpc", // Falls back to empty (no dash to parse)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewKeyBuilder(tt.granularity)
			serviceID := protocol.ServiceID("eth")
			endpointAddr := protocol.EndpointAddr("")

			key := builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_JSON_RPC)
			require.Equal(t, tt.expectedKey, key.String())
		})
	}
}

// =============================================================================
// Granularity Comparison Tests
// =============================================================================

func TestKeyBuilder_GranularityComparison(t *testing.T) {
	serviceID := protocol.ServiceID("eth")
	// Two endpoints from same supplier, same domain
	supplier1Endpoint1 := protocol.EndpointAddr("pokt1supplier1-https://rm-01.nodefleet.net")
	supplier1Endpoint2 := protocol.EndpointAddr("pokt1supplier1-https://rm-02.nodefleet.net")
	// Endpoint from different supplier, same domain
	supplier2SameDomain := protocol.EndpointAddr("pokt1supplier2-https://rm-03.nodefleet.net")
	// Endpoint from different supplier, different domain
	supplier2DiffDomain := protocol.EndpointAddr("pokt1supplier2-https://relay.pokt.network")

	endpointBuilder := NewKeyBuilder(KeyGranularityEndpoint)
	domainBuilder := NewKeyBuilder(KeyGranularityDomain)
	supplierBuilder := NewKeyBuilder(KeyGranularitySupplier)

	// Per-endpoint: all different
	key1e := endpointBuilder.BuildKey(serviceID, supplier1Endpoint1, sharedtypes.RPCType_JSON_RPC)
	key2e := endpointBuilder.BuildKey(serviceID, supplier1Endpoint2, sharedtypes.RPCType_JSON_RPC)
	key3e := endpointBuilder.BuildKey(serviceID, supplier2SameDomain, sharedtypes.RPCType_JSON_RPC)
	key4e := endpointBuilder.BuildKey(serviceID, supplier2DiffDomain, sharedtypes.RPCType_JSON_RPC)
	require.NotEqual(t, key1e, key2e, "per-endpoint: same supplier, different URLs should be different")
	require.NotEqual(t, key1e, key3e, "per-endpoint: different suppliers should be different")
	require.NotEqual(t, key1e, key4e, "per-endpoint: different suppliers should be different")

	// Per-domain: same domain = same key, regardless of supplier
	key1d := domainBuilder.BuildKey(serviceID, supplier1Endpoint1, sharedtypes.RPCType_JSON_RPC)
	key2d := domainBuilder.BuildKey(serviceID, supplier1Endpoint2, sharedtypes.RPCType_JSON_RPC)
	key3d := domainBuilder.BuildKey(serviceID, supplier2SameDomain, sharedtypes.RPCType_JSON_RPC)
	key4d := domainBuilder.BuildKey(serviceID, supplier2DiffDomain, sharedtypes.RPCType_JSON_RPC)
	require.Equal(t, key1d, key2d, "per-domain: same domain should produce same key")
	require.Equal(t, key1d, key3d, "per-domain: same domain should produce same key")
	require.NotEqual(t, key1d, key4d, "per-domain: different domains should be different")

	// Per-supplier: same supplier = same key
	key1s := supplierBuilder.BuildKey(serviceID, supplier1Endpoint1, sharedtypes.RPCType_JSON_RPC)
	key2s := supplierBuilder.BuildKey(serviceID, supplier1Endpoint2, sharedtypes.RPCType_JSON_RPC)
	key3s := supplierBuilder.BuildKey(serviceID, supplier2SameDomain, sharedtypes.RPCType_JSON_RPC)
	key4s := supplierBuilder.BuildKey(serviceID, supplier2DiffDomain, sharedtypes.RPCType_JSON_RPC)
	require.Equal(t, key1s, key2s, "per-supplier: same supplier should produce same key")
	require.NotEqual(t, key1s, key3s, "per-supplier: different suppliers should be different")
	require.Equal(t, key3s, key4s, "per-supplier: same supplier should produce same key")
}

func TestKeyBuilder_DifferentServicesAlwaysDifferent(t *testing.T) {
	endpointAddr := protocol.EndpointAddr("pokt1abc-https://node.example.com")
	ethService := protocol.ServiceID("eth")
	polyService := protocol.ServiceID("poly")

	builders := []struct {
		name    string
		builder KeyBuilder
	}{
		{"per-endpoint", NewKeyBuilder(KeyGranularityEndpoint)},
		{"per-domain", NewKeyBuilder(KeyGranularityDomain)},
		{"per-supplier", NewKeyBuilder(KeyGranularitySupplier)},
	}

	for _, bb := range builders {
		t.Run(bb.name, func(t *testing.T) {
			ethKey := bb.builder.BuildKey(ethService, endpointAddr, sharedtypes.RPCType_JSON_RPC)
			polyKey := bb.builder.BuildKey(polyService, endpointAddr, sharedtypes.RPCType_JSON_RPC)
			require.NotEqual(t, ethKey, polyKey, "different services should always produce different keys")
		})
	}
}

// =============================================================================
// RPC Type Awareness Tests
// =============================================================================

func TestKeyBuilder_DifferentRPCTypesDifferentKeys(t *testing.T) {
	serviceID := protocol.ServiceID("eth")
	endpointAddr := protocol.EndpointAddr("pokt1abc-https://node.example.com")

	builders := []struct {
		name    string
		builder KeyBuilder
	}{
		{"per-endpoint", NewKeyBuilder(KeyGranularityEndpoint)},
		{"per-domain", NewKeyBuilder(KeyGranularityDomain)},
		{"per-supplier", NewKeyBuilder(KeyGranularitySupplier)},
	}

	for _, bb := range builders {
		t.Run(bb.name, func(t *testing.T) {
			// Same endpoint, different RPC types should produce different keys
			jsonRpcKey := bb.builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_JSON_RPC)
			websocketKey := bb.builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_WEBSOCKET)
			restKey := bb.builder.BuildKey(serviceID, endpointAddr, sharedtypes.RPCType_REST)

			// All keys should be different
			require.NotEqual(t, jsonRpcKey, websocketKey, "json_rpc and websocket should produce different keys")
			require.NotEqual(t, jsonRpcKey, restKey, "json_rpc and rest should produce different keys")
			require.NotEqual(t, websocketKey, restKey, "websocket and rest should produce different keys")

			// All keys should contain their RPC type in the string representation
			require.Contains(t, jsonRpcKey.String(), ":json_rpc", "key should contain json_rpc suffix")
			require.Contains(t, websocketKey.String(), ":websocket", "key should contain websocket suffix")
			require.Contains(t, restKey.String(), ":rest", "key should contain rest suffix")
		})
	}
}

func TestKeyBuilder_RPCTypeStringFormat(t *testing.T) {
	serviceID := protocol.ServiceID("eth")
	endpointAddr := protocol.EndpointAddr("pokt1abc-https://node.example.com")

	tests := []struct {
		rpcType          sharedtypes.RPCType
		expectedSuffix   string
		expectedContains string
	}{
		{sharedtypes.RPCType_JSON_RPC, ":json_rpc", "json_rpc"},
		{sharedtypes.RPCType_WEBSOCKET, ":websocket", "websocket"},
		{sharedtypes.RPCType_REST, ":rest", "rest"},
		{sharedtypes.RPCType_COMET_BFT, ":comet_bft", "comet_bft"},
		{sharedtypes.RPCType_GRPC, ":grpc", "grpc"},
	}

	for _, tt := range tests {
		t.Run(tt.rpcType.String(), func(t *testing.T) {
			builder := NewKeyBuilder(KeyGranularityEndpoint)
			key := builder.BuildKey(serviceID, endpointAddr, tt.rpcType)

			// Key should contain the RPC type
			require.Contains(t, key.String(), tt.expectedContains, "key should contain RPC type")

			// Key should end with the RPC type suffix (after endpoint address)
			require.Contains(t, key.String(), tt.expectedSuffix, "key should contain RPC type suffix")
		})
	}
}

func TestKeyBuilder_SameEndpointDifferentRPCTypes(t *testing.T) {
	serviceID := protocol.ServiceID("eth")
	builder := NewKeyBuilder(KeyGranularityEndpoint)

	// Simulate Supplier A and B scenario from the plan:
	// Both use the same URL, but may have different RPC type reliability
	supplierA := protocol.EndpointAddr("pokt1supplierA-https://node.example.com")
	supplierB := protocol.EndpointAddr("pokt1supplierB-https://node.example.com")

	// Both suppliers, JSON-RPC endpoint
	supplierA_JsonRpc := builder.BuildKey(serviceID, supplierA, sharedtypes.RPCType_JSON_RPC)
	supplierB_JsonRpc := builder.BuildKey(serviceID, supplierB, sharedtypes.RPCType_JSON_RPC)

	// Both suppliers, WebSocket endpoint
	supplierA_Websocket := builder.BuildKey(serviceID, supplierA, sharedtypes.RPCType_WEBSOCKET)
	supplierB_Websocket := builder.BuildKey(serviceID, supplierB, sharedtypes.RPCType_WEBSOCKET)

	// Supplier A: JSON-RPC key should differ from WebSocket key
	require.NotEqual(t, supplierA_JsonRpc, supplierA_Websocket,
		"Supplier A should have different keys for json_rpc vs websocket")

	// Supplier B: JSON-RPC key should differ from WebSocket key
	require.NotEqual(t, supplierB_JsonRpc, supplierB_Websocket,
		"Supplier B should have different keys for json_rpc vs websocket")

	// Different suppliers should have different keys (even for same RPC type)
	require.NotEqual(t, supplierA_JsonRpc, supplierB_JsonRpc,
		"Different suppliers should have different json_rpc keys")
	require.NotEqual(t, supplierA_Websocket, supplierB_Websocket,
		"Different suppliers should have different websocket keys")

	// Verify key formats
	require.Equal(t, "eth:pokt1supplierA-https://node.example.com:json_rpc", supplierA_JsonRpc.String())
	require.Equal(t, "eth:pokt1supplierA-https://node.example.com:websocket", supplierA_Websocket.String())
	require.Equal(t, "eth:pokt1supplierB-https://node.example.com:json_rpc", supplierB_JsonRpc.String())
	require.Equal(t, "eth:pokt1supplierB-https://node.example.com:websocket", supplierB_Websocket.String())
}

func TestKeyBuilder_DomainSameURLDifferentRPCTypes(t *testing.T) {
	serviceID := protocol.ServiceID("eth")
	builder := NewKeyBuilder(KeyGranularityDomain)

	// Different suppliers, same domain, different RPC types
	endpoint1 := protocol.EndpointAddr("pokt1abc-https://rm-01.nodefleet.net")
	endpoint2 := protocol.EndpointAddr("pokt1xyz-https://rm-02.nodefleet.net")

	// JSON-RPC keys
	jsonKey1 := builder.BuildKey(serviceID, endpoint1, sharedtypes.RPCType_JSON_RPC)
	jsonKey2 := builder.BuildKey(serviceID, endpoint2, sharedtypes.RPCType_JSON_RPC)

	// WebSocket keys
	wsKey1 := builder.BuildKey(serviceID, endpoint1, sharedtypes.RPCType_WEBSOCKET)
	wsKey2 := builder.BuildKey(serviceID, endpoint2, sharedtypes.RPCType_WEBSOCKET)

	// Same domain, same RPC type → same key
	require.Equal(t, jsonKey1, jsonKey2, "Same domain with json_rpc should produce same key")
	require.Equal(t, wsKey1, wsKey2, "Same domain with websocket should produce same key")

	// Same domain, different RPC type → different key
	require.NotEqual(t, jsonKey1, wsKey1, "Same domain with different RPC types should produce different keys")

	// Verify key format includes RPC type
	require.Equal(t, "eth:nodefleet.net:json_rpc", jsonKey1.String())
	require.Equal(t, "eth:nodefleet.net:websocket", wsKey1.String())
}

func TestKeyBuilder_SupplierSameSupplierDifferentRPCTypes(t *testing.T) {
	serviceID := protocol.ServiceID("eth")
	builder := NewKeyBuilder(KeyGranularitySupplier)

	// Same supplier, different URLs
	endpoint1 := protocol.EndpointAddr("pokt1abc-https://node1.example.com")
	endpoint2 := protocol.EndpointAddr("pokt1abc-https://node2.example.com")

	// JSON-RPC keys
	jsonKey1 := builder.BuildKey(serviceID, endpoint1, sharedtypes.RPCType_JSON_RPC)
	jsonKey2 := builder.BuildKey(serviceID, endpoint2, sharedtypes.RPCType_JSON_RPC)

	// WebSocket keys
	wsKey1 := builder.BuildKey(serviceID, endpoint1, sharedtypes.RPCType_WEBSOCKET)
	wsKey2 := builder.BuildKey(serviceID, endpoint2, sharedtypes.RPCType_WEBSOCKET)

	// Same supplier, same RPC type → same key (regardless of URL)
	require.Equal(t, jsonKey1, jsonKey2, "Same supplier with json_rpc should produce same key")
	require.Equal(t, wsKey1, wsKey2, "Same supplier with websocket should produce same key")

	// Same supplier, different RPC type → different key
	require.NotEqual(t, jsonKey1, wsKey1, "Same supplier with different RPC types should produce different keys")

	// Verify key format includes RPC type
	require.Equal(t, "eth:pokt1abc:json_rpc", jsonKey1.String())
	require.Equal(t, "eth:pokt1abc:websocket", wsKey1.String())
}
