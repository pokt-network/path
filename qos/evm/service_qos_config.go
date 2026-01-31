package evm

import (
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

// QoSType is the QoS type for the EVM blockchain.
const QoSType = "evm"

// DefaultEVMArchivalThreshold is the default archival threshold for EVM-based chains.
// This is used by the archival heuristic to determine which requests require archival data.
// A block is considered "archival" if it's this many blocks behind the perceived block number.
const DefaultEVMArchivalThreshold = 128

// defaultEVMBlockNumberSyncAllowance is the default sync allowance for EVM-based chains.
// This number indicates how many blocks behind the perceived
// block number the endpoint may be and still be considered valid.
// 0 means disabled (no sync allowance check).
const defaultEVMBlockNumberSyncAllowance = 0

// ServiceQoSConfig defines the base interface for service QoS configurations.
// This avoids circular dependency with the config package.
type ServiceQoSConfig interface {
	GetServiceID() protocol.ServiceID
	GetServiceQoSType() string
}

// EVMServiceQoSConfig is the configuration for the EVM service QoS.
//
// Note: Archival capability is determined by external health checks, not by config.
// Health checks mark endpoints as archival-capable via UpdateFromExtractedData.
type EVMServiceQoSConfig interface {
	ServiceQoSConfig // Using locally defined interface to avoid circular dependency
	getEVMChainID() string
	getSyncAllowance() uint64
	getSupportedAPIs() map[sharedtypes.RPCType]struct{}
}

// NewEVMServiceQoSConfig creates a new EVM service configuration.
func NewEVMServiceQoSConfig(
	serviceID protocol.ServiceID,
	evmChainID string,
	supportedAPIs map[sharedtypes.RPCType]struct{},
) EVMServiceQoSConfig {
	return evmServiceQoSConfig{
		serviceID:     serviceID,
		evmChainID:    evmChainID,
		supportedAPIs: supportedAPIs,
	}
}

// NewEVMServiceQoSConfigWithSyncAllowance creates a new EVM service configuration with custom sync allowance.
func NewEVMServiceQoSConfigWithSyncAllowance(
	serviceID protocol.ServiceID,
	evmChainID string,
	supportedAPIs map[sharedtypes.RPCType]struct{},
	syncAllowance uint64,
) EVMServiceQoSConfig {
	return evmServiceQoSConfig{
		serviceID:     serviceID,
		evmChainID:    evmChainID,
		supportedAPIs: supportedAPIs,
		syncAllowance: syncAllowance,
	}
}

// Ensure implementation satisfies interface
var _ EVMServiceQoSConfig = (*evmServiceQoSConfig)(nil)

type evmServiceQoSConfig struct {
	serviceID     protocol.ServiceID
	evmChainID    string
	syncAllowance uint64
	supportedAPIs map[sharedtypes.RPCType]struct{}
}

// GetServiceID returns the ID of the service.
// Implements the ServiceQoSConfig interface.
func (c evmServiceQoSConfig) GetServiceID() protocol.ServiceID {
	return c.serviceID
}

// GetServiceQoSType returns the QoS type of the service.
// Implements the ServiceQoSConfig interface.
func (evmServiceQoSConfig) GetServiceQoSType() string {
	return QoSType
}

// getEVMChainID returns the chain ID.
// Implements the EVMServiceQoSConfig interface.
func (c evmServiceQoSConfig) getEVMChainID() string {
	return c.evmChainID
}

// getSyncAllowance returns the amount of blocks behind the perceived
// block number the endpoint may be and still be considered valid.
func (c evmServiceQoSConfig) getSyncAllowance() uint64 {
	if c.syncAllowance == 0 {
		c.syncAllowance = defaultEVMBlockNumberSyncAllowance
	}
	return c.syncAllowance
}

// getSupportedAPIs returns the RPC types supported by the service.
// Implements the EVMServiceQoSConfig interface.
func (c evmServiceQoSConfig) getSupportedAPIs() map[sharedtypes.RPCType]struct{} {
	return c.supportedAPIs
}
