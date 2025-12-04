// Package gateway provides default health check configurations for common blockchain services.
//
// These default configurations replace hardcoded QoS checks in qos/evm, qos/cosmos, and qos/solana.
// Operators can override these defaults in their YAML configuration.
//
// Default checks are categorized by service type:
//   - EVM: eth_blockNumber, eth_chainId, eth_getBalance (archival)
//   - Cosmos (CometBFT): health, status
//   - Cosmos (REST): /cosmos/base/node/v1beta1/status
//   - Solana: getHealth, getEpochInfo
package gateway

import (
	"time"

	"github.com/pokt-network/path/protocol"
)

// Default check intervals for different blockchain types.
const (
	// EVMBlockNumberInterval is how often to check block height (frequent for sync detection).
	EVMBlockNumberInterval = 10 * time.Second

	// EVMChainIDInterval is how often to verify chain ID (less frequent as it rarely changes).
	EVMChainIDInterval = 20 * time.Minute

	// CosmosHealthInterval is how often to check CometBFT/Cosmos health.
	CosmosHealthInterval = 30 * time.Second

	// SolanaHealthInterval is how often to check Solana health.
	SolanaHealthInterval = 10 * time.Second
)

// ServiceType represents the type of blockchain service for default health check selection.
type ServiceType string

const (
	// ServiceTypeEVM covers all EVM-compatible chains (eth, base, polygon, etc.)
	ServiceTypeEVM ServiceType = "evm"

	// ServiceTypeCosmos covers all Cosmos SDK chains (cosmos, osmosis, etc.)
	ServiceTypeCosmos ServiceType = "cosmos"

	// ServiceTypeSolana covers Solana.
	ServiceTypeSolana ServiceType = "solana"
)

// GetDefaultEVMChecks returns the default health checks for EVM-compatible services.
// These replace the hardcoded checks in qos/evm/.
//
// Checks:
//   - eth_blockNumber: Verifies endpoint is synced (frequent check)
//   - eth_chainId: Verifies endpoint is on correct chain
func GetDefaultEVMChecks() []HealthCheckConfig {
	enabled := true
	return []HealthCheckConfig{
		{
			Name:               "eth_blockNumber",
			Type:               HealthCheckTypeJSONRPC,
			Enabled:            &enabled,
			Method:             "POST",
			Path:               "/",
			Body:               `{"jsonrpc":"2.0","id":1002,"method":"eth_blockNumber"}`,
			ExpectedStatusCode: 200,
			Timeout:            5 * time.Second,
			ReputationSignal:   "minor_error",
		},
		{
			Name:               "eth_chainId",
			Type:               HealthCheckTypeJSONRPC,
			Enabled:            &enabled,
			Method:             "POST",
			Path:               "/",
			Body:               `{"jsonrpc":"2.0","id":1001,"method":"eth_chainId"}`,
			ExpectedStatusCode: 200,
			Timeout:            5 * time.Second,
			ReputationSignal:   "major_error",
		},
	}
}

// GetDefaultEVMArchivalCheck returns the archival check for EVM services.
// This check is separate because it requires chain-specific parameters.
//
// Parameters:
//   - contractAddress: The contract address to check balance for
//   - blockNumberHex: The historical block number to query (hex format)
//   - expectedBalance: Expected balance value (optional validation)
//
// Example usage for mainnet:
//
//	GetDefaultEVMArchivalCheck(
//	  "0x28C6c06298d514Db089934071355E5743bf21d60", // Binance hot wallet
//	  "0xe71e1d",                                    // Historical block
//	)
func GetDefaultEVMArchivalCheck(contractAddress, blockNumberHex string) HealthCheckConfig {
	enabled := true
	return HealthCheckConfig{
		Name:               "eth_archival",
		Type:               HealthCheckTypeJSONRPC,
		Enabled:            &enabled,
		Method:             "POST",
		Path:               "/",
		Body:               `{"jsonrpc":"2.0","id":1003,"method":"eth_getBalance","params":["` + contractAddress + `","` + blockNumberHex + `"]}`,
		ExpectedStatusCode: 200,
		Timeout:            10 * time.Second,
		Archival:           true,
		ReputationSignal:   "critical_error",
	}
}

// GetDefaultCosmosChecks returns the default health checks for Cosmos SDK services.
// These replace the hardcoded checks in qos/cosmos/.
//
// Checks:
//   - cometbft_health: CometBFT JSON-RPC health check
//   - cometbft_status: CometBFT JSON-RPC status check (chain ID, sync status)
//   - cosmos_status: Cosmos SDK REST status endpoint
func GetDefaultCosmosChecks() []HealthCheckConfig {
	enabled := true
	return []HealthCheckConfig{
		{
			Name:               "cometbft_health",
			Type:               HealthCheckTypeJSONRPC,
			Enabled:            &enabled,
			Method:             "POST",
			Path:               "/",
			Body:               `{"jsonrpc":"2.0","id":2001,"method":"health"}`,
			ExpectedStatusCode: 200,
			Timeout:            5 * time.Second,
			ReputationSignal:   "minor_error",
		},
		{
			Name:               "cometbft_status",
			Type:               HealthCheckTypeJSONRPC,
			Enabled:            &enabled,
			Method:             "POST",
			Path:               "/",
			Body:               `{"jsonrpc":"2.0","id":2002,"method":"status"}`,
			ExpectedStatusCode: 200,
			Timeout:            5 * time.Second,
			ReputationSignal:   "major_error",
		},
		{
			Name:               "cosmos_status",
			Type:               HealthCheckTypeREST,
			Enabled:            &enabled,
			Method:             "GET",
			Path:               "/cosmos/base/node/v1beta1/status",
			ExpectedStatusCode: 200,
			Timeout:            5 * time.Second,
			ReputationSignal:   "minor_error",
		},
	}
}

// GetDefaultSolanaChecks returns the default health checks for Solana services.
// These replace the hardcoded checks in qos/solana/.
//
// Checks:
//   - getHealth: Solana JSON-RPC health check
//   - getEpochInfo: Solana JSON-RPC epoch info (sync validation)
func GetDefaultSolanaChecks() []HealthCheckConfig {
	enabled := true
	return []HealthCheckConfig{
		{
			Name:               "solana_getHealth",
			Type:               HealthCheckTypeJSONRPC,
			Enabled:            &enabled,
			Method:             "POST",
			Path:               "/",
			Body:               `{"jsonrpc":"2.0","id":1001,"method":"getHealth"}`,
			ExpectedStatusCode: 200,
			Timeout:            5 * time.Second,
			ReputationSignal:   "minor_error",
		},
		{
			Name:               "solana_getEpochInfo",
			Type:               HealthCheckTypeJSONRPC,
			Enabled:            &enabled,
			Method:             "POST",
			Path:               "/",
			Body:               `{"jsonrpc":"2.0","id":1002,"method":"getEpochInfo"}`,
			ExpectedStatusCode: 200,
			Timeout:            5 * time.Second,
			ReputationSignal:   "major_error",
		},
	}
}

// GetDefaultWebSocketCheck returns a basic WebSocket connectivity check.
// This can be used for any service that supports WebSocket connections.
//
// Parameters:
//   - name: Check name (e.g., "ws_connectivity")
//   - path: WebSocket path (e.g., "/ws" or "/")
func GetDefaultWebSocketCheck(name, path string) HealthCheckConfig {
	enabled := true
	return HealthCheckConfig{
		Name:             name,
		Type:             HealthCheckTypeWebSocket,
		Enabled:          &enabled,
		Path:             path,
		Timeout:          10 * time.Second,
		ReputationSignal: "minor_error",
	}
}

// GetDefaultWebSocketCheckWithPayload returns a WebSocket check that sends a message
// and validates the response.
//
// Parameters:
//   - name: Check name
//   - path: WebSocket path
//   - payload: Message to send after connection
//   - expectedContains: String that must appear in the response (empty = any response)
func GetDefaultWebSocketCheckWithPayload(name, path, payload, expectedContains string) HealthCheckConfig {
	enabled := true
	return HealthCheckConfig{
		Name:                     name,
		Type:                     HealthCheckTypeWebSocket,
		Enabled:                  &enabled,
		Path:                     path,
		Body:                     payload,
		ExpectedResponseContains: expectedContains,
		Timeout:                  10 * time.Second,
		ReputationSignal:         "minor_error",
	}
}

// GetDefaultChecksForServiceType returns the appropriate default checks based on service type.
// This is a convenience function for operators who want to use defaults.
func GetDefaultChecksForServiceType(serviceType ServiceType) []HealthCheckConfig {
	switch serviceType {
	case ServiceTypeEVM:
		return GetDefaultEVMChecks()
	case ServiceTypeCosmos:
		return GetDefaultCosmosChecks()
	case ServiceTypeSolana:
		return GetDefaultSolanaChecks()
	default:
		return nil
	}
}

// BuildDefaultServiceConfig creates a complete ServiceHealthCheckConfig with defaults.
// This is useful for programmatically building configurations.
//
// Parameters:
//   - serviceID: The service identifier (e.g., "eth", "base", "cosmos")
//   - serviceType: The type of service for selecting default checks
//   - checkInterval: How often to run health checks (0 uses default)
func BuildDefaultServiceConfig(
	serviceID protocol.ServiceID,
	serviceType ServiceType,
	checkInterval time.Duration,
) ServiceHealthCheckConfig {
	enabled := true

	if checkInterval == 0 {
		switch serviceType {
		case ServiceTypeEVM:
			checkInterval = EVMBlockNumberInterval
		case ServiceTypeCosmos:
			checkInterval = CosmosHealthInterval
		case ServiceTypeSolana:
			checkInterval = SolanaHealthInterval
		default:
			checkInterval = DefaultHealthCheckInterval
		}
	}

	return ServiceHealthCheckConfig{
		ServiceID:     serviceID,
		CheckInterval: checkInterval,
		Enabled:       &enabled,
		Checks:        GetDefaultChecksForServiceType(serviceType),
	}
}

// KnownEVMServices maps common EVM service IDs to their names.
// This is provided for documentation and validation purposes.
var KnownEVMServices = map[protocol.ServiceID]string{
	"eth":    "Ethereum Mainnet",
	"base":   "Base",
	"poly":   "Polygon",
	"arb":    "Arbitrum",
	"opt":    "Optimism",
	"avax":   "Avalanche C-Chain",
	"bsc":    "BNB Smart Chain",
	"ftm":    "Fantom",
	"matic":  "Polygon (alternative)",
	"eth-hd": "Ethereum Holesky",
	"eth-sd": "Ethereum Sepolia",
}

// KnownCosmosServices maps common Cosmos service IDs to their names.
var KnownCosmosServices = map[protocol.ServiceID]string{
	"cosmos":  "Cosmos Hub",
	"osmosis": "Osmosis",
	"juno":    "Juno",
	"evmos":   "Evmos",
	"kava":    "Kava",
	"akash":   "Akash",
}

// KnownSolanaServices maps Solana service IDs to their names.
var KnownSolanaServices = map[protocol.ServiceID]string{
	"sol": "Solana Mainnet",
}

// InferServiceType attempts to infer the service type from the service ID.
// Returns empty string if the service type cannot be inferred.
//
// This uses the KnownXxxServices maps to determine the type.
// For unknown services, operators should explicitly specify the checks in YAML.
func InferServiceType(serviceID protocol.ServiceID) ServiceType {
	if _, ok := KnownEVMServices[serviceID]; ok {
		return ServiceTypeEVM
	}
	if _, ok := KnownCosmosServices[serviceID]; ok {
		return ServiceTypeCosmos
	}
	if _, ok := KnownSolanaServices[serviceID]; ok {
		return ServiceTypeSolana
	}
	return ""
}
