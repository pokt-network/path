package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/config"
	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/cosmos"
	"github.com/pokt-network/path/qos/evm"
	"github.com/pokt-network/path/qos/noop"
	"github.com/pokt-network/path/qos/selector"
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

	configureEndpointSelection(hydratedLogger, unifiedConfig)

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

		// Per-operator concentration cap: bounds any single operator's (eTLD+1) share of
		// endpoint selection (config-driven, shipped ON by default). Applied uniformly to
		// every QoS type that supports it — EVM/Cosmos/Solana/NoOp — covering both HTTP and
		// WebSocket initial selection via requestContext.Select. Disabled (>= 1 or <= 0)
		// leaves selection as a flat random pick.
		if unifiedConfig != nil {
			maxOperatorShare := resolveMaxOperatorShare(unifiedConfig, serviceID)

			// Publish the service's resolved selection settings to the selector package. The
			// pick that serves a request happens inside a shared helper called from four QoS
			// implementations, none of which carry this config; publishing it here keeps the
			// cap and the weighting basis service-accurate without threading configuration
			// through every one of them.
			selector.SetServiceSelectionSettings(
				serviceID,
				unifiedConfig.GetBackendRegistrationWeightCapForService(serviceID),
				maxOperatorShare,
			)

			if setter, ok := qosServices[serviceID].(interface{ SetMaxOperatorShare(float64) }); ok {
				setter.SetMaxOperatorShare(maxOperatorShare)
				if maxOperatorShare > 0 && maxOperatorShare < 1 {
					svcLogger.Info().Float64("max_operator_share", maxOperatorShare).Msg("✅ QoS: per-operator concentration cap ENABLED")
				}
			} else {
				// Every current QoS type implements SetMaxOperatorShare; a new type that
				// forgets it would silently run uncapped despite the shipped-on default.
				svcLogger.Warn().Msg("⚠️ QoS type does not support the per-operator concentration cap; selection runs uncapped")
			}
		}
	}

	hydratedLogger.Info().Msgf("Initialized %d QoS service instances", len(qosServices))
	return qosServices, nil
}

// configureEndpointSelection publishes the endpoint-selection weighting knobs before any QoS
// instance is built.
//
// EVERY knob here ships ON and is turned OFF by an env var, never on. A behavior that ships
// behind a default-off flag is never exercised by a canary — canary and control run the same
// distribution and the deploy proves nothing — so the flags exist as kill switches, settable
// on a running deployment without waiting for an image build. Each log line below names the
// env var that reverts it, so the switch is discoverable from the pod's own startup logs.
func configureEndpointSelection(logger polylog.Logger, unifiedConfig *gateway.UnifiedServicesConfig) {
	// Measure an operator's share in distinct BACKEND URLs rather than in supplier
	// registrations. Several suppliers can register against the same backend, so
	// registration-counted shares credit one operator's 6 machines as 25 endpoints — past what
	// its infrastructure represents. This is the BROADEST revert: disabling it also neutralizes
	// the per-backend weight cap and the operator cap on the serving pick.
	if os.Getenv("PATH_OPERATOR_SHARE_BY_BACKEND_URL") == "false" {
		selector.SetOperatorShareBackendURLDedup(false)
		logger.Warn().Msg("⚠️ operator share counted by SUPPLIER REGISTRATION; per-backend weight cap and serving-pick operator cap are both inert (PATH_OPERATOR_SHARE_BY_BACKEND_URL=false)")
	} else {
		logger.Info().Msg("✅ operator share counted by distinct BACKEND URL")
	}

	// Per-backend registration weight cap (K): weight(backend) = min(registrations, K).
	// K=1 restores the previous backend-uniform basis exactly.
	if raw := os.Getenv("PATH_BACKEND_REGISTRATION_WEIGHT_CAP"); raw != "" {
		k, err := strconv.Atoi(raw)
		if err != nil || k < 1 {
			logger.Warn().Str("value", raw).Msg("⚠️ ignoring PATH_BACKEND_REGISTRATION_WEIGHT_CAP: want an integer >= 1")
		} else {
			selector.SetBackendRegistrationWeightCap(k)
			logger.Warn().Int("backend_registration_weight_cap", k).Msg("⚠️ per-backend registration weight cap overridden (PATH_BACKEND_REGISTRATION_WEIGHT_CAP); 1 = previous backend-uniform basis")
		}
	}
	logger.Info().Int("backend_registration_weight_cap", selector.BackendRegistrationWeightCap()).
		Msg("✅ endpoint selection weighted by min(registrations-per-backend, K); set PATH_BACKEND_REGISTRATION_WEIGHT_CAP=1 to revert to backend-uniform")

	// Whether the pick that serves the request applies the per-operator cap. Off leaves the
	// new weighting basis in place uncapped, which is how the two halves are A/B-ed apart.
	if os.Getenv("PATH_PRIMARY_PICK_OPERATOR_CAP") == "false" {
		selector.SetBackendPickOperatorCap(false)
		logger.Warn().Msg("⚠️ per-operator concentration cap DISABLED on the serving endpoint pick (PATH_PRIMARY_PICK_OPERATOR_CAP=false)")
	} else {
		logger.Info().Msg("✅ per-operator concentration cap applied on the serving endpoint pick")
	}

	// A cap no assignment can satisfy (cap * operators <= 1) falls back to the previous 0.65
	// cap rather than to a forced uniform-over-operators split. See
	// selector.infeasibleCapFallbackShare.
	if os.Getenv("PATH_SELECTION_CAP_INFEASIBLE_UNIFORM") == "true" {
		selector.SetCapInfeasibleForcesUniform(true)
		logger.Warn().Msg("⚠️ an unsatisfiable operator cap now forces UNIFORM-OVER-OPERATORS (PATH_SELECTION_CAP_INFEASIBLE_UNIFORM=true); two-operator services move to 50/50")
	}

	if unifiedConfig == nil {
		return
	}

	defaultShare := unifiedConfig.GetDefaultMaxOperatorShare()
	// Fleet-wide override of the cap VALUE, so it can be moved (or disabled with >= 1) on a
	// running deployment without editing per-service config.
	if raw := os.Getenv("PATH_MAX_OPERATOR_SHARE"); raw != "" {
		share, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			logger.Warn().Str("value", raw).Msg("⚠️ ignoring PATH_MAX_OPERATOR_SHARE: not a number")
		} else {
			maxOperatorShareOverride = &share
			logger.Warn().Float64("max_operator_share", share).Msg("⚠️ per-operator concentration cap overridden for ALL services (PATH_MAX_OPERATOR_SHARE)")
		}
	}
	if maxOperatorShareOverride != nil {
		defaultShare = *maxOperatorShareOverride
	}
	selector.SetDefaultMaxOperatorShare(defaultShare)
}

// maxOperatorShareOverride is the PATH_MAX_OPERATOR_SHARE fleet-wide cap override, applied to
// every service in place of its resolved configuration. nil when unset.
var maxOperatorShareOverride *float64

// resolveMaxOperatorShare returns the cap to apply to a service, honoring the fleet-wide
// override.
func resolveMaxOperatorShare(unifiedConfig *gateway.UnifiedServicesConfig, serviceID protocol.ServiceID) float64 {
	if maxOperatorShareOverride != nil {
		return *maxOperatorShareOverride
	}
	return unifiedConfig.GetMaxOperatorShareForService(serviceID)
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
