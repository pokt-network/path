package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/cosmos"
	"github.com/pokt-network/path/qos/evm"
	"github.com/pokt-network/path/qos/solana"
	qostypes "github.com/pokt-network/path/qos/types"
)

// TestBuildExtractorRegistryEmpty tests that an empty config produces an empty registry.
func TestBuildExtractorRegistryEmpty(t *testing.T) {
	config := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{},
	}

	registry := buildExtractorRegistry(config)

	require.NotNil(t, registry)
	require.Equal(t, 0, registry.Count())
}

// TestBuildExtractorRegistryEVMService tests that EVM services get the EVM extractor.
func TestBuildExtractorRegistryEVMService(t *testing.T) {
	config := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{
				ID:   "eth",
				Type: gateway.ServiceTypeEVM,
			},
			{
				ID:   "base",
				Type: gateway.ServiceTypeEVM,
			},
		},
	}

	registry := buildExtractorRegistry(config)

	require.NotNil(t, registry)
	require.Equal(t, 2, registry.Count())

	// Verify both services have EVM extractors
	ethExtractor := registry.Get(protocol.ServiceID("eth"))
	require.NotNil(t, ethExtractor)
	_, isEVM := ethExtractor.(*evm.EVMDataExtractor)
	require.True(t, isEVM, "Expected EVMDataExtractor for eth service")

	baseExtractor := registry.Get(protocol.ServiceID("base"))
	require.NotNil(t, baseExtractor)
	_, isEVM = baseExtractor.(*evm.EVMDataExtractor)
	require.True(t, isEVM, "Expected EVMDataExtractor for base service")
}

// TestBuildExtractorRegistrySolanaService tests that Solana services get the Solana extractor.
func TestBuildExtractorRegistrySolanaService(t *testing.T) {
	config := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{
				ID:   "solana",
				Type: gateway.ServiceTypeSolana,
			},
		},
	}

	registry := buildExtractorRegistry(config)

	require.NotNil(t, registry)
	require.Equal(t, 1, registry.Count())

	solanaExtractor := registry.Get(protocol.ServiceID("solana"))
	require.NotNil(t, solanaExtractor)
	_, isSolana := solanaExtractor.(*solana.SolanaDataExtractor)
	require.True(t, isSolana, "Expected SolanaDataExtractor for solana service")
}

// TestBuildExtractorRegistryCosmosService tests that Cosmos services get the Cosmos extractor.
func TestBuildExtractorRegistryCosmosService(t *testing.T) {
	config := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{
				ID:   "cosmos",
				Type: gateway.ServiceTypeCosmos,
			},
		},
	}

	registry := buildExtractorRegistry(config)

	require.NotNil(t, registry)
	require.Equal(t, 1, registry.Count())

	cosmosExtractor := registry.Get(protocol.ServiceID("cosmos"))
	require.NotNil(t, cosmosExtractor)
	_, isCosmos := cosmosExtractor.(*cosmos.CosmosDataExtractor)
	require.True(t, isCosmos, "Expected CosmosDataExtractor for cosmos service")
}

// TestBuildExtractorRegistryPassthroughService tests that passthrough services get NoOp extractor.
func TestBuildExtractorRegistryPassthroughService(t *testing.T) {
	config := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{
				ID:   "generic-service",
				Type: gateway.ServiceTypePassthrough,
			},
		},
	}

	registry := buildExtractorRegistry(config)

	require.NotNil(t, registry)
	// Passthrough services don't get registered (fall back to NoOp via Get)
	require.Equal(t, 0, registry.Count())

	// Getting the extractor should return NoOp
	genericExtractor := registry.Get(protocol.ServiceID("generic-service"))
	require.NotNil(t, genericExtractor)
	_, isNoOp := genericExtractor.(*qostypes.NoOpDataExtractor)
	require.True(t, isNoOp, "Expected NoOpDataExtractor for passthrough service")
}

// TestBuildExtractorRegistryMixedServices tests a mix of different service types.
func TestBuildExtractorRegistryMixedServices(t *testing.T) {
	config := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{ID: "eth", Type: gateway.ServiceTypeEVM},
			{ID: "solana", Type: gateway.ServiceTypeSolana},
			{ID: "cosmos", Type: gateway.ServiceTypeCosmos},
			{ID: "generic", Type: gateway.ServiceTypePassthrough},
		},
	}

	registry := buildExtractorRegistry(config)

	require.NotNil(t, registry)
	// Only EVM, Solana, Cosmos get registered (3 total)
	require.Equal(t, 3, registry.Count())

	// Verify correct extractor types
	ethExtractor := registry.Get(protocol.ServiceID("eth"))
	_, isEVM := ethExtractor.(*evm.EVMDataExtractor)
	require.True(t, isEVM, "Expected EVMDataExtractor for eth")

	solanaExtractor := registry.Get(protocol.ServiceID("solana"))
	_, isSolana := solanaExtractor.(*solana.SolanaDataExtractor)
	require.True(t, isSolana, "Expected SolanaDataExtractor for solana")

	cosmosExtractor := registry.Get(protocol.ServiceID("cosmos"))
	_, isCosmos := cosmosExtractor.(*cosmos.CosmosDataExtractor)
	require.True(t, isCosmos, "Expected CosmosDataExtractor for cosmos")

	genericExtractor := registry.Get(protocol.ServiceID("generic"))
	_, isNoOp := genericExtractor.(*qostypes.NoOpDataExtractor)
	require.True(t, isNoOp, "Expected NoOpDataExtractor for generic")
}

// TestBuildExtractorRegistryWithDefaults tests that service type from defaults is used.
func TestBuildExtractorRegistryWithDefaults(t *testing.T) {
	config := &gateway.UnifiedServicesConfig{
		Defaults: gateway.ServiceDefaults{
			Type: gateway.ServiceTypeEVM, // Default type is EVM
		},
		Services: []gateway.ServiceConfig{
			{
				ID: "eth",
				// No type specified, should inherit from defaults
			},
		},
	}

	registry := buildExtractorRegistry(config)

	// This test verifies that GetServiceType falls back to defaults
	serviceType := config.GetServiceType("eth")
	require.Equal(t, gateway.ServiceTypeEVM, serviceType)

	// Verify the extractor is EVM
	ethExtractor := registry.Get(protocol.ServiceID("eth"))
	_, isEVM := ethExtractor.(*evm.EVMDataExtractor)
	require.True(t, isEVM, "Expected EVMDataExtractor for eth service with default type")
}

// TestBuildExtractorRegistryExtractorReuse tests that extractors are reused across services.
func TestBuildExtractorRegistryExtractorReuse(t *testing.T) {
	config := &gateway.UnifiedServicesConfig{
		Services: []gateway.ServiceConfig{
			{ID: "eth", Type: gateway.ServiceTypeEVM},
			{ID: "base", Type: gateway.ServiceTypeEVM},
			{ID: "polygon", Type: gateway.ServiceTypeEVM},
		},
	}

	registry := buildExtractorRegistry(config)

	// All EVM services should share the same extractor instance
	ethExtractor := registry.Get(protocol.ServiceID("eth"))
	baseExtractor := registry.Get(protocol.ServiceID("base"))
	polygonExtractor := registry.Get(protocol.ServiceID("polygon"))

	// Verify they're the same instance (pointer equality)
	require.Same(t, ethExtractor, baseExtractor, "EVM extractors should be the same instance")
	require.Same(t, baseExtractor, polygonExtractor, "EVM extractors should be the same instance")
}
