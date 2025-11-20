package solana

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/buildwithgrove/path/protocol"
)

// NewQoSInstance builds and returns an instance of the Solana QoS service.
func NewQoSInstance(logger polylog.Logger, serviceID protocol.ServiceID, serviceConfig *Config) *QoS {
	chainID := serviceConfig.ChainID

	logger = logger.With(
		"qos_instance", "solana",
		"chain_id", chainID,
		"service_id", serviceID,
	)

	serviceState := &ServiceState{
		logger:    logger,
		serviceID: serviceID,
		chainID:   chainID,
	}

	solanaEndpointStore := &EndpointStore{
		logger:       logger,
		serviceState: serviceState,
	}

	requestValidator := &requestValidator{
		logger:        logger,
		serviceID:     serviceID,
		chainID:       chainID,
		endpointStore: solanaEndpointStore,
	}

	return &QoS{
		logger:           logger,
		ServiceState:     serviceState,
		EndpointStore:    solanaEndpointStore,
		requestValidator: requestValidator,
	}
}
