package cosmos

import (
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/buildwithgrove/path/protocol"
)

// Config represents Cosmos SDK-specific service configuration
type Config struct {
	// Cosmos SDK chain ID (e.g., "cosmoshub-4")
	CosmosChainID string `yaml:"chain_id"`

	// EVM chain ID in hex format for Cosmos chains with native EVM support (e.g., XRPLEVM)
	EVMChainID string `yaml:"evm_chain_id"`

	// Sync allowance override
	SyncAllowance uint64 `yaml:"sync_allowance"`

	// Supported RPC types for this service
	SupportedAPIs []string `yaml:"supported_apis"`
}

// Validate validates the Cosmos service configuration
func (c *Config) Validate(logger polylog.Logger, serviceID protocol.ServiceID) error {
	if c.CosmosChainID == "" {
		err := fmt.Errorf("service %q: chain_id cannot be empty", serviceID)
		logger.Error().Err(err).Msg("Validation failed")
		return err
	}

	if c.EVMChainID == "" {
		err := fmt.Errorf("service %q: evm_chain_id cannot be empty", serviceID)
		logger.Error().Err(err).Msg("Validation failed")
		return err
	}

	if c.SyncAllowance == 0 {
		err := fmt.Errorf("service %q: sync_allowance must be greater than 0", serviceID)
		logger.Error().Err(err).Msg("Validation failed")
		return err
	}

	if c.SupportedAPIs == nil || len(c.SupportedAPIs) == 0 {
		err := fmt.Errorf("service %q: supported_apis cannot be empty", serviceID)
		logger.Error().Err(err).Msg("Validation failed")
		return err
	}

	// Check for duplicate API types
	seen := make(map[string]bool)
	for _, apiType := range c.SupportedAPIs {
		if seen[apiType] {
			err := fmt.Errorf("service %q: duplicate RPC type %q in supported_apis", serviceID, apiType)
			logger.Error().Err(err).Msg("Validation failed")
			return err
		}
		seen[apiType] = true

		// Validate each API type against the RPCType enum
		rpcTypeValue, exists := sharedtypes.RPCType_value[apiType]
		if !exists || rpcTypeValue <= 0 {
			err := fmt.Errorf("service %q: invalid RPC type %q in supported_apis", serviceID, apiType)
			logger.Error().Err(err).Msg("Validation failed")
			return err
		}
	}

	return nil
}

// GetSupportedAPIs returns the supported RPC types as a map of RPCType enum values
func (c *Config) GetSupportedAPIs() map[sharedtypes.RPCType]struct{} {
	result := make(map[sharedtypes.RPCType]struct{}, len(c.SupportedAPIs))

	for _, apiType := range c.SupportedAPIs {
		if rpcTypeValue, exists := sharedtypes.RPCType_value[apiType]; exists {
			result[sharedtypes.RPCType(rpcTypeValue)] = struct{}{}
		}
	}

	return result
}

// LogConfig logs the Cosmos service configuration
func (c *Config) LogConfig(logger polylog.Logger) {
	logger.Info().
		Str("type", "Cosmos").
		Str("cosmos_chain_id", c.CosmosChainID).
		Str("evm_chain_id", c.EVMChainID).
		Uint64("sync_allowance", c.SyncAllowance).
		Int("supported_apis_count", len(c.SupportedAPIs)).
		Msg("Cosmos service configuration")
}
