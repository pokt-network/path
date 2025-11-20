package solana

import (
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/buildwithgrove/path/protocol"
)

// Config represents Solana-specific service configuration
type Config struct {
	// Chain ID (e.g., "solana", "mainnet-beta")
	ChainID string `yaml:"chain_id"`
}

// Validate validates the Config struct
func (c *Config) Validate(logger polylog.Logger, serviceID protocol.ServiceID) error {
	if c.ChainID == "" {
		return fmt.Errorf("service %s: chain_id is required", serviceID)
	}

	return nil
}

// LogConfig logs the Solana service configuration
func (c *Config) LogConfig(logger polylog.Logger) {
	logger.Info().
		Str("type", "Solana").
		Str("chain_id", c.ChainID).
		Msg("Solana service configuration")
}
