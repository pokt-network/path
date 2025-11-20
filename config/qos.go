package config

import (
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/buildwithgrove/path/gateway"
	"github.com/buildwithgrove/path/protocol"
	"github.com/buildwithgrove/path/qos/cosmos"
	"github.com/buildwithgrove/path/qos/evm"
	"github.com/buildwithgrove/path/qos/solana"
)

// ServicesQoSConfig represents the top-level configuration for all services
type ServicesQoSConfig struct {
	Services map[string]ServiceQoSConfig `yaml:"services"`
}

// Validate ensures the ServicesQoSConfig is valid
func (sc *ServicesQoSConfig) Validate(logger polylog.Logger, gatewayServices map[protocol.ServiceID]struct{}) error {
	if sc.Services == nil {
		err := fmt.Errorf("services map cannot be nil")
		logger.Error().Err(err).Msg("Validation failed")
		return err
	}

	hasErrors := false

	for serviceIDStr, serviceConfig := range sc.Services {
		serviceID := protocol.ServiceID(serviceIDStr)

		// Check if service is configured in the gateway
		if _, found := gatewayServices[serviceID]; !found {
			logger.Warn().Msgf("⚠️  🔍 Service ID '%s' has QoS configuration defined BUT no owned apps configured! 🚫 All requests for this service will fail. Configure owned app private keys for this service to enable QoS.", serviceID)
			continue
		}

		// Validate the service configuration
		if err := serviceConfig.Validate(logger, serviceID); err != nil {
			logger.Error().Err(err).Msg("Validation failed for service")
			hasErrors = true
		}
	}

	if hasErrors {
		return fmt.Errorf("validation failed for one or more services")
	}

	return nil
}

// BuildQoSInstances creates QoS instances for all configured services
func (sc *ServicesQoSConfig) BuildQoSInstances(logger polylog.Logger) (map[protocol.ServiceID]gateway.QoSService, error) {
	qosServices := make(map[protocol.ServiceID]gateway.QoSService)

	qosLogger := logger.With("module", "qos")

	for serviceIDStr, serviceConfig := range sc.Services {
		serviceID := protocol.ServiceID(serviceIDStr)

		qosInstance, err := serviceConfig.BuildQoSInstance(qosLogger, serviceID)
		if err != nil {
			return nil, fmt.Errorf("failed to build QoS instance for service %q: %w", serviceID, err)
		}

		qosServices[serviceID] = qosInstance
		qosLogger.With("service_id", serviceID).Debug().Msg("Added QoS instance for the service ID.")
	}

	return qosServices, nil
}

// LogServicesConfigs logs the configuration for every service ID
func (sc *ServicesQoSConfig) LogServicesConfigs(logger polylog.Logger) {
	if sc.Services == nil || len(sc.Services) == 0 {
		logger.Warn().Msg("No services configured in QoS config")
		return
	}

	logger.Info().Msgf("Logging QoS configuration for %d service(s)", len(sc.Services))

	for serviceIDStr, serviceConfig := range sc.Services {
		serviceID := protocol.ServiceID(serviceIDStr)
		serviceLogger := logger.With("service_id", serviceID)
		serviceConfig.LogConfig(serviceLogger)
	}
}

// ServiceQoSConfig represents a single service configuration entry
type ServiceQoSConfig struct {
	// EVM-specific configuration (non-nil indicates this is an EVM service)
	EVM *evm.Config `yaml:"evm,omitempty"`

	// Cosmos SDK-specific configuration (non-nil indicates this is a Cosmos SDK service)
	Cosmos *cosmos.Config `yaml:"cosmos,omitempty"`

	// Solana-specific configuration (non-nil indicates this is a Solana service)
	Solana *solana.Config `yaml:"solana,omitempty"`
}

// Validate validates the service configuration
func (sc *ServiceQoSConfig) Validate(logger polylog.Logger, serviceID protocol.ServiceID) error {
	if sc.EVM != nil {
		return sc.EVM.Validate(logger, serviceID)
	}

	if sc.Cosmos != nil {
		return sc.Cosmos.Validate(logger, serviceID)
	}

	if sc.Solana != nil {
		return sc.Solana.Validate(logger, serviceID)
	}

	return fmt.Errorf("service %q has no configuration: all config fields are nil", serviceID)
}

// BuildQoSInstance creates a QoS instance for this service configuration
func (sc *ServiceQoSConfig) BuildQoSInstance(logger polylog.Logger, serviceID protocol.ServiceID) (gateway.QoSService, error) {
	if sc.EVM != nil {
		return evm.NewQoSInstance(logger, serviceID, sc.EVM), nil
	}

	if sc.Cosmos != nil {
		return cosmos.NewQoSInstance(logger, serviceID, sc.Cosmos), nil
	}

	if sc.Solana != nil {
		return solana.NewQoSInstance(logger, serviceID, sc.Solana), nil
	}

	return nil, fmt.Errorf("service %q has no valid configuration", serviceID)
}

// LogConfig logs the configuration for this service
func (sc *ServiceQoSConfig) LogConfig(logger polylog.Logger) {
	if sc.EVM != nil {
		sc.EVM.LogConfig(logger)
	}

	if sc.Cosmos != nil {
		sc.Cosmos.LogConfig(logger)
	}

	if sc.Solana != nil {
		sc.Solana.LogConfig(logger)
	}
}
