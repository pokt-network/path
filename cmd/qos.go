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
	"github.com/pokt-network/path/qos/noop"
	"github.com/pokt-network/path/qos/solana"
)

// getServiceQoSInstances returns all QoS instances to be used by the Gateway and the EndpointHydrator.
// Service types are determined from the unified YAML configuration (gateway_config.services[]).
// If a service is not configured, it defaults to passthrough/noop QoS.
func getServiceQoSInstances(
	logger polylog.Logger,
	gatewayConfig config.GatewayConfig,
	unifiedConfig *gateway.UnifiedServicesConfig,
	protocolInstance gateway.Protocol,
) (map[protocol.ServiceID]gateway.QoSService, error) {
	qosServices := make(map[protocol.ServiceID]gateway.QoSService)

	// Create loggers
	hydratedLogger := logger.With("module", "qos").With("method", "getServiceQoSInstances").With("protocol", protocolInstance.Name())
	qosLogger := logger.With("module", "qos").With("protocol", protocolInstance.Name())

	// Wait for the protocol to become healthy before configuring QoS instances.
	err := waitForProtocolHealth(hydratedLogger, protocolInstance, defaultProtocolHealthTimeout)
	if err != nil {
		return nil, err
	}

	// Get configured service IDs from the protocol instance.
	gatewayServiceIDs := protocolInstance.ConfiguredServiceIDs()
	logGatewayServiceIDs(hydratedLogger, gatewayServiceIDs)

	// Remove any service IDs that are manually disabled by the user.
	for _, disabledQoSServiceIDForGateway := range gatewayConfig.HydratorConfig.QoSDisabledServiceIDs {
		if _, found := gatewayServiceIDs[disabledQoSServiceIDForGateway]; !found {
			return nil, fmt.Errorf("[INVALID CONFIGURATION] QoS manually disabled for service ID: %s BUT NOT found in protocol's configured service IDs", disabledQoSServiceIDForGateway)
		}
		hydratedLogger.Info().Msgf("Gateway manually disabled QoS for service ID: %s", disabledQoSServiceIDForGateway)
		delete(gatewayServiceIDs, disabledQoSServiceIDForGateway)
	}

	// Initialize QoS services for all gateway service IDs using unified config.
	for serviceID := range gatewayServiceIDs {
		// Get service type from unified config (falls back to defaults if not explicitly configured)
		serviceType := gateway.ServiceTypePassthrough
		var syncAllowance uint64
		if unifiedConfig != nil {
			serviceType = unifiedConfig.GetServiceType(serviceID)
			syncAllowance = unifiedConfig.GetSyncAllowanceForService(serviceID)
		}

		svcLogger := hydratedLogger.With("service_id", serviceID).With("service_type", string(serviceType))

		switch serviceType {
		case gateway.ServiceTypeEVM:
			evmQoS := evm.NewSimpleQoSInstanceWithSyncAllowance(qosLogger, serviceID, syncAllowance)
			qosServices[serviceID] = evmQoS
			svcLogger.Debug().Uint64("sync_allowance", syncAllowance).Msg("Added EVM QoS instance")

		case gateway.ServiceTypeCosmos:
			cosmosQoS := cosmos.NewSimpleQoSInstanceWithSyncAllowance(qosLogger, serviceID, syncAllowance)
			qosServices[serviceID] = cosmosQoS
			svcLogger.Debug().Uint64("sync_allowance", syncAllowance).Msg("Added Cosmos QoS instance")

		case gateway.ServiceTypeSolana:
			solanaQoS := solana.NewSimpleQoSInstance(qosLogger, serviceID)
			qosServices[serviceID] = solanaQoS
			svcLogger.Debug().Msg("Added Solana QoS instance")

		case gateway.ServiceTypeGeneric:
			// Generic uses noop QoS (basic JSON-RPC handling without chain-specific validation)
			genericQoS := noop.NewNoOpQoSService(qosLogger, serviceID)
			qosServices[serviceID] = genericQoS
			svcLogger.Debug().Msg("Added Generic QoS instance (noop)")

		case gateway.ServiceTypePassthrough:
			// Passthrough uses noop QoS
			passthroughQoS := noop.NewNoOpQoSService(qosLogger, serviceID)
			qosServices[serviceID] = passthroughQoS
			svcLogger.Debug().Msg("Added Passthrough QoS instance (noop)")

		default:
			// Unknown type falls back to noop
			svcLogger.Warn().Msg("Unknown service type, using noop QoS")
			noopQoS := noop.NewNoOpQoSService(qosLogger, serviceID)
			qosServices[serviceID] = noopQoS
		}
	}

	hydratedLogger.Info().Msgf("Initialized %d QoS service instances", len(qosServices))
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

