package solana

import "github.com/pokt-network/path/protocol"

// QoSType is the QoS type for the Solana blockchain.
const QoSType = "solana"

// ServiceQoSConfig defines the base interface for service QoS configurations.
// This avoids circular dependency with the config package.
type ServiceQoSConfig interface {
	GetServiceID() protocol.ServiceID
	GetServiceQoSType() string
}

// SolanaServiceQoSConfig is the configuration for the Solana service QoS.
type SolanaServiceQoSConfig interface {
	ServiceQoSConfig    // Using locally defined interface to avoid circular dependency
	getChainID() string // The Chain ID set by the preprocessor.
}

// NewSolanaServiceQoSConfig creates a new Solana service configuration.
func NewSolanaServiceQoSConfig(
	serviceID protocol.ServiceID,
	chainID string,
) SolanaServiceQoSConfig {
	return solanaServiceQoSConfig{
		serviceID: serviceID,
		chainID:   chainID,
	}
}

// Ensure implementation satisfies interface
var _ SolanaServiceQoSConfig = (*solanaServiceQoSConfig)(nil)

type solanaServiceQoSConfig struct {
	serviceID protocol.ServiceID
	chainID   string
}

// GetServiceID returns the ID of the service.
// Implements the ServiceQoSConfig interface.
func (c solanaServiceQoSConfig) GetServiceID() protocol.ServiceID {
	return c.serviceID
}

// getChainID returns the chain ID configured for the Solana QoS instance.
// Implements the ServiceQoSConfig interface.
func (c solanaServiceQoSConfig) getChainID() string {
	return c.chainID
}

// GetServiceQoSType returns the QoS type of the service.
// Implements the ServiceQoSConfig interface.
func (solanaServiceQoSConfig) GetServiceQoSType() string {
	return QoSType
}
