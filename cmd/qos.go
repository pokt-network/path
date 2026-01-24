package main

import (
	"fmt"
	"strings"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

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
		var rpcTypesStr []string
		if unifiedConfig != nil {
			serviceType = unifiedConfig.GetServiceType(serviceID)
			syncAllowance = unifiedConfig.GetSyncAllowanceForService(serviceID)
			rpcTypesStr = unifiedConfig.GetServiceRPCTypes(serviceID)
		}

		svcLogger := hydratedLogger.With("service_id", serviceID).With("service_type", string(serviceType))

		switch serviceType {
		case gateway.ServiceTypeEVM:
			evmQoS := evm.NewSimpleQoSInstanceWithSyncAllowance(qosLogger, serviceID, syncAllowance)
			qosServices[serviceID] = evmQoS
			if syncAllowance > 0 {
				svcLogger.Info().Uint64("sync_allowance", syncAllowance).Msg("✅ EVM QoS: sync allowance ENABLED")
			} else {
				svcLogger.Info().Msg("⚠️ EVM QoS: sync allowance DISABLED (set sync_allowance > 0 to enable)")
			}

		case gateway.ServiceTypeCosmos:
			// Convert string RPC types to sharedtypes.RPCType
			supportedAPIs := convertRPCTypesToMap(rpcTypesStr)

			cosmosQoS := cosmos.NewSimpleQoSInstanceWithAPIs(qosLogger, serviceID, syncAllowance, supportedAPIs)
			qosServices[serviceID] = cosmosQoS
			if syncAllowance > 0 {
				svcLogger.Info().Uint64("sync_allowance", syncAllowance).Msgf("✅ Cosmos QoS: sync allowance ENABLED, RPC types: %v", rpcTypesStr)
			} else {
				svcLogger.Info().Msgf("⚠️ Cosmos QoS: sync allowance DISABLED, RPC types: %v", rpcTypesStr)
			}

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

// convertRPCTypesToMap converts string RPC types to a map of sharedtypes.RPCType.
// This is used to configure supported APIs for QoS instances based on unified config.
func convertRPCTypesToMap(rpcTypesStr []string) map[sharedtypes.RPCType]struct{} {
	supportedAPIs := make(map[sharedtypes.RPCType]struct{})

	for _, rpcTypeStr := range rpcTypesStr {
		var rpcType sharedtypes.RPCType
		switch strings.ToLower(rpcTypeStr) {
		case "json_rpc", "jsonrpc":
			rpcType = sharedtypes.RPCType_JSON_RPC
		case "rest":
			rpcType = sharedtypes.RPCType_REST
		case "comet_bft", "cometbft":
			rpcType = sharedtypes.RPCType_COMET_BFT
		case "websocket", "ws":
			rpcType = sharedtypes.RPCType_WEBSOCKET
		case "grpc":
			rpcType = sharedtypes.RPCType_GRPC
		default:
			// Skip unknown RPC types
			continue
		}
		supportedAPIs[rpcType] = struct{}{}
	}

	// If no valid RPC types were found, default to REST and COMET_BFT for Cosmos chains
	if len(supportedAPIs) == 0 {
		supportedAPIs[sharedtypes.RPCType_REST] = struct{}{}
		supportedAPIs[sharedtypes.RPCType_COMET_BFT] = struct{}{}
	}

	return supportedAPIs
}
