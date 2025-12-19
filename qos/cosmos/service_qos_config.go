package cosmos

import (
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

// QoSType is the QoS type for the CosmosSDK blockchain.
const QoSType = "cosmossdk"

// defaultCosmosSDKBlockNumberSyncAllowance is the default sync allowance for CosmosSDK-based chains.
// This number indicates how many blocks behind the perceived
// block number the endpoint may be and still be considered valid.
// 0 means disabled (no sync allowance check).
const defaultCosmosSDKBlockNumberSyncAllowance = 0

// ServiceQoSConfig defines the base interface for service QoS configurations.
// This avoids circular dependency with the config package.
type ServiceQoSConfig interface {
	GetServiceID() protocol.ServiceID
	GetServiceQoSType() string
}

// CosmosSDKServiceQoSConfig is the configuration for the CosmosSDK service QoS.
type CosmosSDKServiceQoSConfig interface {
	ServiceQoSConfig // Using locally defined interface to avoid circular dependency
	getCosmosSDKChainID() string
	getEVMChainID() string
	getSyncAllowance() uint64
	getSupportedAPIs() map[sharedtypes.RPCType]struct{}
}

// NewCosmosSDKServiceQoSConfig creates a new CosmosSDK service configuration.
func NewCosmosSDKServiceQoSConfig(
	serviceID protocol.ServiceID,
	cosmosSDKChainID string,
	evmChainID string,
	supportedAPIs map[sharedtypes.RPCType]struct{},
) CosmosSDKServiceQoSConfig {
	return cosmosSDKServiceQoSConfig{
		serviceID:        serviceID,
		cosmosSDKChainID: cosmosSDKChainID,
		evmChainID:       evmChainID,
		supportedAPIs:    supportedAPIs,
	}
}

// NewCosmosSDKServiceQoSConfigWithSyncAllowance creates a new CosmosSDK service configuration with custom sync allowance.
func NewCosmosSDKServiceQoSConfigWithSyncAllowance(
	serviceID protocol.ServiceID,
	cosmosSDKChainID string,
	evmChainID string,
	supportedAPIs map[sharedtypes.RPCType]struct{},
	syncAllowance uint64,
) CosmosSDKServiceQoSConfig {
	return cosmosSDKServiceQoSConfig{
		serviceID:        serviceID,
		cosmosSDKChainID: cosmosSDKChainID,
		evmChainID:       evmChainID,
		supportedAPIs:    supportedAPIs,
		syncAllowance:    syncAllowance,
	}
}

// Ensure implementation satisfies interface
var _ CosmosSDKServiceQoSConfig = (*cosmosSDKServiceQoSConfig)(nil)

type cosmosSDKServiceQoSConfig struct {
	serviceID        protocol.ServiceID
	cosmosSDKChainID string
	evmChainID       string
	syncAllowance    uint64
	supportedAPIs    map[sharedtypes.RPCType]struct{}
}

// GetServiceID returns the ID of the service.
// Implements the ServiceQoSConfig interface.
func (c cosmosSDKServiceQoSConfig) GetServiceID() protocol.ServiceID {
	return c.serviceID
}

// GetServiceQoSType returns the QoS type of the service.
// Implements the ServiceQoSConfig interface.
func (cosmosSDKServiceQoSConfig) GetServiceQoSType() string {
	return QoSType
}

// getCosmosSDKChainID returns the chain ID.
// Implements the CosmosSDKServiceQoSConfig interface.
func (c cosmosSDKServiceQoSConfig) getCosmosSDKChainID() string {
	return c.cosmosSDKChainID
}

// getEVMChainID returns the EVM chain ID.
// This is necessary for Cosmos chains that have native EVM support; XRPLEVM, etc...
// Implements the CosmosSDKServiceQoSConfig interface.
func (c cosmosSDKServiceQoSConfig) getEVMChainID() string {
	return c.evmChainID
}

// getSyncAllowance returns the amount of blocks behind the perceived
// block number the endpoint may be and still be considered valid.
func (c cosmosSDKServiceQoSConfig) getSyncAllowance() uint64 {
	if c.syncAllowance == 0 {
		c.syncAllowance = defaultCosmosSDKBlockNumberSyncAllowance
	}
	return c.syncAllowance
}

// getSupportedAPIs returns the RPC types supported by the service.
// For example, XRPLEVM supports the following RPC types:
//   - JSON_RPC
//   - REST
//   - COMET_BFT
//   - WEBSOCKET (does not currently have a QoS quality check system in PATH)
//
// This is used:
// 1. to validate the request and whether this service supports the request's RPC type
// 2. to determine the appropriate synthetic QoS endpoint checks to run
func (c cosmosSDKServiceQoSConfig) getSupportedAPIs() map[sharedtypes.RPCType]struct{} {
	return c.supportedAPIs
}
