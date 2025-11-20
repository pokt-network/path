package main

import (
	"strings"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/buildwithgrove/path/config"
	"github.com/buildwithgrove/path/gateway"
	"github.com/buildwithgrove/path/protocol"
)

// getServiceQoSInstances returns all QoS instances to be used by the Gateway and the EndpointHydrator.
func getServiceQoSInstances(
	logger polylog.Logger,
	gatewayConfig config.GatewayConfig,
	protocolInstance gateway.Protocol,
) (map[protocol.ServiceID]gateway.QoSService, error) {
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

	// TODO_TECHDEBT(@adshmh): Refactor to move the Validate method call to GatewayConfig struct.
	//
	// Validate the QoS services config.
	if err := gatewayConfig.ServicesQoSConfigs.Validate(hydratedLogger, gatewayServiceIDs); err != nil {
		return nil, err
	}

	// Log services QoS configs.
	gatewayConfig.ServicesQoSConfigs.LogServicesConfigs(hydratedLogger)

	// Use the services QoS configs to build QoS instances.
	return gatewayConfig.ServicesQoSConfigs.BuildQoSInstances(qosLogger)
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
