package evm

import (
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

// QoSType is the QoS type for the EVM blockchain.
const QoSType = "evm"

// 128 is the default archival threshold for EVM-based chains.
// This is an opinionated value that aligns with industry standard
// practices for defining what constitutes an archival block.
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
type EVMServiceQoSConfig interface {
	ServiceQoSConfig // Using locally defined interface to avoid circular dependency
	getEVMChainID() string
	getSyncAllowance() uint64
	getEVMArchivalCheckConfig() evmArchivalCheckConfig
	archivalCheckEnabled() bool
	getSupportedAPIs() map[sharedtypes.RPCType]struct{}
}

// evmArchivalCheckConfig is the configuration for the archival check.
//
// The basic methodology is:
//  1. Select a `ContractAddress` for the chain with a frequent transaction volume and large balance.
//  2. Determine its starting block height (`ContractStartBlock`).
//  3. Set a `Threshold` for how many blocks below the current block number are considered "archival" data.
//
// With all of this data, the QoS implementation can select a random block number to check using `eth_getBalance`.
type evmArchivalCheckConfig struct {
	threshold          uint64 // The number of blocks below the current block number to be considered "archival" data
	contractAddress    string // The address of the contract to check for the archival balance.
	contractStartBlock uint64 // The start block of the contract address (ie. when it first had a balance)
}

func (c evmArchivalCheckConfig) IsEmpty() bool {
	return c.contractAddress == "" || c.contractStartBlock == 0 || c.threshold == 0
}

// NewEVMServiceQoSConfig creates a new EVM service configuration with the specified archival check settings.
func NewEVMServiceQoSConfig(
	serviceID protocol.ServiceID,
	evmChainID string,
	archivalCheckConfig *evmArchivalCheckConfig,
	supportedAPIs map[sharedtypes.RPCType]struct{},
) EVMServiceQoSConfig {
	return evmServiceQoSConfig{
		serviceID:           serviceID,
		evmChainID:          evmChainID,
		archivalCheckConfig: archivalCheckConfig,
		supportedAPIs:       supportedAPIs,
	}
}

// NewEVMServiceQoSConfigWithSyncAllowance creates a new EVM service configuration with custom sync allowance.
func NewEVMServiceQoSConfigWithSyncAllowance(
	serviceID protocol.ServiceID,
	evmChainID string,
	archivalCheckConfig *evmArchivalCheckConfig,
	supportedAPIs map[sharedtypes.RPCType]struct{},
	syncAllowance uint64,
) EVMServiceQoSConfig {
	return evmServiceQoSConfig{
		serviceID:           serviceID,
		evmChainID:          evmChainID,
		archivalCheckConfig: archivalCheckConfig,
		supportedAPIs:       supportedAPIs,
		syncAllowance:       syncAllowance,
	}
}

func NewEVMArchivalCheckConfig(
	contractAddress string,
	contractStartBlock uint64,
) *evmArchivalCheckConfig {
	return &evmArchivalCheckConfig{
		threshold:          DefaultEVMArchivalThreshold,
		contractAddress:    contractAddress,
		contractStartBlock: contractStartBlock,
	}
}

// Ensure implementation satisfies interface
var _ EVMServiceQoSConfig = (*evmServiceQoSConfig)(nil)

type evmServiceQoSConfig struct {
	serviceID           protocol.ServiceID
	evmChainID          string
	syncAllowance       uint64
	archivalCheckConfig *evmArchivalCheckConfig
	supportedAPIs       map[sharedtypes.RPCType]struct{}
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

// archivalCheckEnabled returns true if the archival check is enabled.
// If the archival check is not enabled for the service, this will always return false.
func (c evmServiceQoSConfig) archivalCheckEnabled() bool {
	return c.archivalCheckConfig != nil
}

// getEVMArchivalCheckConfig returns the archival check configuration.
// Implements the EVMServiceQoSConfig interface.
func (c evmServiceQoSConfig) getEVMArchivalCheckConfig() evmArchivalCheckConfig {
	return *c.archivalCheckConfig
}

// getSupportedAPIs returns the RPC types supported by the service.
// Implements the EVMServiceQoSConfig interface.
func (c evmServiceQoSConfig) getSupportedAPIs() map[sharedtypes.RPCType]struct{} {
	return c.supportedAPIs
}
