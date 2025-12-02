package reputation

import (
	"testing"

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

	key := builder.BuildKey(serviceID, endpointAddr)

	require.Equal(t, serviceID, key.ServiceID)
	require.Equal(t, endpointAddr, key.EndpointAddr)
	require.Equal(t, "eth:pokt1abc123-https://node.example.com", key.String())
}

func TestKeyBuilder_PerSupplier(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularitySupplier)
	require.IsType(t, &SupplierKeyBuilder{}, builder)

	serviceID := protocol.ServiceID("eth")
	endpointAddr := protocol.EndpointAddr("pokt1abc123-https://node.example.com")

	key := builder.BuildKey(serviceID, endpointAddr)

	require.Equal(t, serviceID, key.ServiceID)
	require.Equal(t, protocol.EndpointAddr("pokt1abc123"), key.EndpointAddr)
	require.Equal(t, "eth:pokt1abc123", key.String())
}

func TestKeyBuilder_PerSupplier_SameSupplierDifferentURLs(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularitySupplier)

	serviceID := protocol.ServiceID("eth")
	endpoint1 := protocol.EndpointAddr("pokt1abc123-https://node1.example.com")
	endpoint2 := protocol.EndpointAddr("pokt1abc123-https://node2.example.com")

	key1 := builder.BuildKey(serviceID, endpoint1)
	key2 := builder.BuildKey(serviceID, endpoint2)

	// Both should produce the same key (same supplier)
	require.Equal(t, key1, key2)
	require.Equal(t, "eth:pokt1abc123", key1.String())
}

func TestKeyBuilder_PerService(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularityService)
	require.IsType(t, &ServiceKeyBuilder{}, builder)

	serviceID := protocol.ServiceID("eth")
	endpointAddr := protocol.EndpointAddr("pokt1abc123-https://node.example.com")

	key := builder.BuildKey(serviceID, endpointAddr)

	require.Equal(t, serviceID, key.ServiceID)
	require.Equal(t, protocol.EndpointAddr(""), key.EndpointAddr)
	require.Equal(t, "eth:", key.String())
}

func TestKeyBuilder_PerService_AllEndpointsSameKey(t *testing.T) {
	builder := NewKeyBuilder(KeyGranularityService)

	serviceID := protocol.ServiceID("eth")
	endpoint1 := protocol.EndpointAddr("pokt1abc123-https://node1.example.com")
	endpoint2 := protocol.EndpointAddr("pokt1xyz789-https://node2.example.com")

	key1 := builder.BuildKey(serviceID, endpoint1)
	key2 := builder.BuildKey(serviceID, endpoint2)

	// Both should produce the same key (same service)
	require.Equal(t, key1, key2)
	require.Equal(t, "eth:", key1.String())
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewKeyBuilder(tt.granularity)
			require.IsType(t, &EndpointKeyBuilder{}, builder)

			// Should behave like per-endpoint
			serviceID := protocol.ServiceID("eth")
			endpointAddr := protocol.EndpointAddr("pokt1abc-https://node.com")

			key := builder.BuildKey(serviceID, endpointAddr)
			require.Equal(t, "eth:pokt1abc-https://node.com", key.String())
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
			expectedKey:  "eth:pokt1abc123https://node.com", // Falls back to full addr
		},
		{
			name:         "just supplier address",
			endpointAddr: "pokt1abc123",
			expectedKey:  "eth:pokt1abc123", // Falls back to full addr (same as supplier)
		},
		{
			name:         "just URL",
			endpointAddr: "https://node.com",
			expectedKey:  "eth:https://node.com", // Falls back to full addr
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serviceID := protocol.ServiceID("eth")
			endpointAddr := protocol.EndpointAddr(tt.endpointAddr)

			key := builder.BuildKey(serviceID, endpointAddr)
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
		{"per-supplier", NewKeyBuilder(KeyGranularitySupplier)},
		{"per-service", NewKeyBuilder(KeyGranularityService)},
	}

	for _, bb := range builders {
		t.Run(bb.name, func(t *testing.T) {
			serviceID := protocol.ServiceID("")
			endpointAddr := protocol.EndpointAddr("pokt1abc-https://node.com")

			key := bb.builder.BuildKey(serviceID, endpointAddr)
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
			expectedKey: "eth:",
		},
		{
			name:        "per-supplier with empty addr",
			granularity: KeyGranularitySupplier,
			expectedKey: "eth:", // Falls back to empty (no dash to parse)
		},
		{
			name:        "per-service with empty addr",
			granularity: KeyGranularityService,
			expectedKey: "eth:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewKeyBuilder(tt.granularity)
			serviceID := protocol.ServiceID("eth")
			endpointAddr := protocol.EndpointAddr("")

			key := builder.BuildKey(serviceID, endpointAddr)
			require.Equal(t, tt.expectedKey, key.String())
		})
	}
}

// =============================================================================
// Granularity Comparison Tests
// =============================================================================

func TestKeyBuilder_GranularityComparison(t *testing.T) {
	serviceID := protocol.ServiceID("eth")
	supplier1Endpoint1 := protocol.EndpointAddr("pokt1supplier1-https://node1.com")
	supplier1Endpoint2 := protocol.EndpointAddr("pokt1supplier1-https://node2.com")
	supplier2Endpoint1 := protocol.EndpointAddr("pokt1supplier2-https://node1.com")

	endpointBuilder := NewKeyBuilder(KeyGranularityEndpoint)
	supplierBuilder := NewKeyBuilder(KeyGranularitySupplier)
	serviceBuilder := NewKeyBuilder(KeyGranularityService)

	// Per-endpoint: all different
	key1e := endpointBuilder.BuildKey(serviceID, supplier1Endpoint1)
	key2e := endpointBuilder.BuildKey(serviceID, supplier1Endpoint2)
	key3e := endpointBuilder.BuildKey(serviceID, supplier2Endpoint1)
	require.NotEqual(t, key1e, key2e, "per-endpoint: same supplier, different URLs should be different")
	require.NotEqual(t, key1e, key3e, "per-endpoint: different suppliers should be different")

	// Per-supplier: same supplier = same key
	key1s := supplierBuilder.BuildKey(serviceID, supplier1Endpoint1)
	key2s := supplierBuilder.BuildKey(serviceID, supplier1Endpoint2)
	key3s := supplierBuilder.BuildKey(serviceID, supplier2Endpoint1)
	require.Equal(t, key1s, key2s, "per-supplier: same supplier should produce same key")
	require.NotEqual(t, key1s, key3s, "per-supplier: different suppliers should be different")

	// Per-service: all same
	key1sv := serviceBuilder.BuildKey(serviceID, supplier1Endpoint1)
	key2sv := serviceBuilder.BuildKey(serviceID, supplier1Endpoint2)
	key3sv := serviceBuilder.BuildKey(serviceID, supplier2Endpoint1)
	require.Equal(t, key1sv, key2sv, "per-service: all endpoints should produce same key")
	require.Equal(t, key1sv, key3sv, "per-service: all endpoints should produce same key")
}

func TestKeyBuilder_DifferentServicesAlwaysDifferent(t *testing.T) {
	endpointAddr := protocol.EndpointAddr("pokt1abc-https://node.com")
	ethService := protocol.ServiceID("eth")
	polyService := protocol.ServiceID("poly")

	builders := []struct {
		name    string
		builder KeyBuilder
	}{
		{"per-endpoint", NewKeyBuilder(KeyGranularityEndpoint)},
		{"per-supplier", NewKeyBuilder(KeyGranularitySupplier)},
		{"per-service", NewKeyBuilder(KeyGranularityService)},
	}

	for _, bb := range builders {
		t.Run(bb.name, func(t *testing.T) {
			ethKey := bb.builder.BuildKey(ethService, endpointAddr)
			polyKey := bb.builder.BuildKey(polyService, endpointAddr)
			require.NotEqual(t, ethKey, polyKey, "different services should always produce different keys")
		})
	}
}
