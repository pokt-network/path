package main

import (
	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/qos/cosmos"
	"github.com/pokt-network/path/qos/evm"
	"github.com/pokt-network/path/qos/solana"
	qostypes "github.com/pokt-network/path/qos/types"
)

// buildExtractorRegistry creates an ExtractorRegistry populated with the appropriate
// DataExtractor for each configured service based on its QoS type.
//
// This enables the observation queue to route each service's responses to the
// correct extractor for parsing (block height, chain ID, sync status, etc.).
//
// Parameters:
//   - unifiedConfig: The unified services configuration from the gateway config
//
// Returns:
//   - A populated ExtractorRegistry with extractors for all configured services
func buildExtractorRegistry(unifiedConfig *gateway.UnifiedServicesConfig) *qostypes.ExtractorRegistry {
	registry := qostypes.NewExtractorRegistry()

	// Create singleton extractors for each QoS type
	// These are stateless and can be safely shared across services
	evmExtractor := evm.NewEVMDataExtractor()
	cosmosExtractor := cosmos.NewCosmosDataExtractor()
	solanaExtractor := solana.NewSolanaDataExtractor()

	// Register the appropriate extractor for each configured service
	for _, svcConfig := range unifiedConfig.Services {
		serviceID := svcConfig.ID

		switch unifiedConfig.GetServiceType(serviceID) {
		case gateway.ServiceTypeEVM:
			registry.Register(serviceID, evmExtractor)
		case gateway.ServiceTypeCosmos:
			registry.Register(serviceID, cosmosExtractor)
		case gateway.ServiceTypeSolana:
			registry.Register(serviceID, solanaExtractor)
		// Default: falls back to NoOpDataExtractor via registry.Get()
		}
	}

	return registry
}
