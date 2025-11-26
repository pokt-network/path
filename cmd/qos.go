package main

import (
	"fmt"
	"strings"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/config"
	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/cosmos"
	"github.com/pokt-network/path/qos/evm"
	"github.com/pokt-network/path/qos/solana"
)

// getServiceQoSInstances returns all QoS instances to be used by the Gateway and the EndpointHydrator.
func getServiceQoSInstances(
	logger polylog.Logger,
	gatewayConfig config.GatewayConfig,
	protocolInstance gateway.Protocol,
) (map[protocol.ServiceID]gateway.QoSService, error) {
	// TODO_TECHDEBT(@adshmh): refactor this function to remove the
	// need to manually add entries for every new QoS implementation.
	qosServices := make(map[protocol.ServiceID]gateway.QoSService)

	// Create a logger for this function's own messages with method-specific context
	hydratedLogger := logger.With("module", "qos").With("method", "getServiceQoSInstances").With("protocol", protocolInstance.Name())

	// Create a separate logger for QoS instances without method-specific context
	qosLogger := logger.With("module", "qos").With("protocol", protocolInstance.Name())

	// Wait for the protocol to become healthy BEFORE configuring and starting the hydrator.
	// - Ensures the protocol instance's configured service IDs are available before hydrator startup.
	err := waitForProtocolHealth(hydratedLogger, protocolInstance, defaultProtocolHealthTimeout)
	if err != nil {
		return nil, err
	}

	// Get configured service IDs from the protocol instance.
	// - Used to run hydrator checks on all configured service IDs (except those manually disabled by the user).
	gatewayServiceIDs := protocolInstance.ConfiguredServiceIDs()
	logGatewayServiceIDs(hydratedLogger, gatewayServiceIDs)

	// Remove any service IDs that are manually disabled by the user.
	for _, disabledQoSServiceIDForGateway := range gatewayConfig.HydratorConfig.QoSDisabledServiceIDs {
		// Throw error if any manually disabled service IDs are not found in the protocol's configured service IDs.
		if _, found := gatewayServiceIDs[disabledQoSServiceIDForGateway]; !found {
			return nil, fmt.Errorf("[INVALID CONFIGURATION] QoS manually disabled for service ID: %s BUT NOT not found in protocol's configured service IDs", disabledQoSServiceIDForGateway)
		}
		hydratedLogger.Info().Msgf("Gateway manually disabled QoS for service ID: %s", disabledQoSServiceIDForGateway)
		delete(gatewayServiceIDs, disabledQoSServiceIDForGateway)
	}

	// Get the service configs for the current protocol
	qosServiceConfigs := config.QoSServiceConfigs.GetServiceConfigs(gatewayConfig)
	logQoSServiceConfigs(hydratedLogger, qosServiceConfigs)

	// Initialize QoS services for all service IDs with a corresponding QoS
	// implementation, as defined in the `config/service_qos.go` file.
	for _, qosServiceConfig := range qosServiceConfigs {
		serviceID := qosServiceConfig.GetServiceID()
		// Skip service IDs that are not configured for the PATH instance.
		if _, found := gatewayServiceIDs[serviceID]; !found {
			hydratedLogger.Warn().Msgf("⚠️  🔍 Service ID '%s' has QoS configuration defined BUT no owned apps configured! 🚫 This service will fallback to NoOp QoS and likely fail. Configure owned app private keys for this service to enable proper QoS.", serviceID)
			continue
		}

		switch qosServiceConfig.GetServiceQoSType() {
		case evm.QoSType:
			evmServiceQoSConfig, ok := qosServiceConfig.(evm.EVMServiceQoSConfig)
			if !ok {
				return nil, fmt.Errorf("SHOULD NEVER HAPPEN: error building QoS instances: service ID %q is not an EVM service", serviceID)
			}

			evmQoS := evm.NewQoSInstance(qosLogger, evmServiceQoSConfig)
			qosServices[serviceID] = evmQoS

			hydratedLogger.With("service_id", serviceID).Debug().Msg("Added EVM QoS instance for the service ID.")

		case cosmos.QoSType:
			cosmosSDKServiceQoSConfig, ok := qosServiceConfig.(cosmos.CosmosSDKServiceQoSConfig)
			if !ok {
				return nil, fmt.Errorf("SHOULD NEVER HAPPEN: error building QoS instances: service ID %q is not a CosmosSDK service", serviceID)
			}

			cosmosSDKQoS := cosmos.NewQoSInstance(qosLogger, cosmosSDKServiceQoSConfig)
			qosServices[serviceID] = cosmosSDKQoS

			hydratedLogger.With("service_id", serviceID).Debug().Msg("Added CosmosSDK QoS instance for the service ID.")

		case solana.QoSType:
			solanaServiceQoSConfig, ok := qosServiceConfig.(solana.SolanaServiceQoSConfig)
			if !ok {
				return nil, fmt.Errorf("SHOULD NEVER HAPPEN: error building QoS instances: service ID %q is not a Solana service", serviceID)
			}

			solanaQoS := solana.NewQoSInstance(qosLogger, solanaServiceQoSConfig)
			qosServices[serviceID] = solanaQoS

			hydratedLogger.With("service_id", serviceID).Debug().Msg("Added Solana QoS instance for the service ID.")
		default:
			return nil, fmt.Errorf("SHOULD NEVER HAPPEN: error building QoS instances: service ID %q not supported by PATH", serviceID)
		}
	}

	return qosServices, nil
}

// logGatewayServiceIDs outputs the available service IDs for the gateway.
func logGatewayServiceIDs(logger polylog.Logger, serviceConfigs map[protocol.ServiceID]struct{}) {
	// Output configured service IDs for gateway.
	serviceIDs := make([]string, 0, len(serviceConfigs))
	for serviceID := range serviceConfigs {
		serviceIDs = append(serviceIDs, string(serviceID))
	}
	logger.Info().Msgf("Service IDs configured by the gateway: %s.", strings.Join(serviceIDs, ", "))
}

// logQoSServiceConfigs outputs the configured service IDs for the gateway.
func logQoSServiceConfigs(logger polylog.Logger, serviceConfigs []config.ServiceQoSConfig) {
	// Output service IDs with QoS configurations
	serviceIDs := make([]string, 0, len(serviceConfigs))
	for _, serviceConfig := range serviceConfigs {
		serviceIDs = append(serviceIDs, string(serviceConfig.GetServiceID()))
	}
	logger.Info().Msgf("Service IDs with available QoS configurations: %s.", strings.Join(serviceIDs, ", "))
}
