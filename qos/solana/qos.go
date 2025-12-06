package solana

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
)

// NewSimpleQoSInstance creates a minimal Solana QoS instance without chain-specific validation.
// Validation is now handled by active health checks.
// This constructor only requires the service ID and provides JSON-RPC request parsing.
func NewSimpleQoSInstance(logger polylog.Logger, serviceID protocol.ServiceID) *QoS {
	logger = logger.With(
		"qos_instance", "solana",
		"service_id", serviceID,
	)

	serviceState := &ServiceState{
		logger:    logger,
		serviceID: serviceID,
		chainID:   "", // No chain ID validation - handled by health checks
	}

	endpointStore := &EndpointStore{
		logger:       logger,
		serviceState: serviceState,
	}

	requestValidator := &requestValidator{
		logger:        logger,
		serviceID:     serviceID,
		chainID:       "",
		endpointStore: endpointStore,
	}

	return &QoS{
		logger:           logger,
		ServiceState:     serviceState,
		EndpointStore:    endpointStore,
		requestValidator: requestValidator,
	}
}
