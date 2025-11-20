package main

// TODO_TECHDEBT(@olshansk): Revisit the name `hydrator` to something more appropriate.

import (
	"errors"
	"fmt"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/buildwithgrove/path/config"
	"github.com/buildwithgrove/path/gateway"
	"github.com/buildwithgrove/path/protocol"
)

// TODO_TECHDEBT: Make this configurable.
const defaultProtocolHealthTimeout = 2 * time.Minute

// setupEndpointHydrator
//
// - Initializes and starts an instance of EndpointHydrator matching the configuration settings.
// - Will NOT start the EndpointHydrator if no service QoS generators are specified.
func setupEndpointHydrator(
	cmdLogger polylog.Logger,
	protocolInstance gateway.Protocol,
	qosServices map[protocol.ServiceID]gateway.QoSService,
	metricsReporter gateway.RequestResponseReporter,
	dataReporter gateway.RequestResponseReporter,
	hydratorConfig config.EndpointHydratorConfig,
) (*gateway.EndpointHydrator, error) {
	if cmdLogger == nil {
		return nil, errors.New("no logger provided")
	}
	logger := cmdLogger.With(
		"component", "hydrator",
		"method", "setupEndpointHydrator",
	)

	if len(qosServices) == 0 {
		logger.Warn().Msg("endpoint hydrator is fully disabled: no (zero) active service QoS instances are specified")
		return nil, nil
	}

	if protocolInstance == nil {
		return nil, errors.New("endpoint hydrator enabled but no protocol provided. this should never happen")
	}

	// Get configured service IDs from the protocol instance.
	gatewayServiceIDs := protocolInstance.ConfiguredServiceIDs()

	// Filter out any service IDs that are manually disabled by the user.
	activeQoSServices := make(map[protocol.ServiceID]gateway.QoSService)
	for serviceID, qosService := range qosServices {
		activeQoSServices[serviceID] = qosService
	}

	for _, disabledQoSServiceIDForGateway := range hydratorConfig.QoSDisabledServiceIDs {
		// Throw error if any manually disabled service IDs are not found in the protocol's configured service IDs.
		if _, found := gatewayServiceIDs[disabledQoSServiceIDForGateway]; !found {
			return nil, fmt.Errorf("[INVALID CONFIGURATION] QoS manually disabled for service ID: %s BUT NOT not found in protocol's configured service IDs", disabledQoSServiceIDForGateway)
		}
		logger.Info().Msgf("Gateway manually disabled QoS for service ID: %s", disabledQoSServiceIDForGateway)
		delete(activeQoSServices, disabledQoSServiceIDForGateway)
	}

	// Check if all QoS services were disabled after filtering
	if len(activeQoSServices) == 0 {
		logger.Warn().Msg("endpoint hydrator is fully disabled: all QoS services were manually disabled")
		return nil, nil
	}

	endpointHydrator := gateway.EndpointHydrator{
		Logger:                  cmdLogger,
		Protocol:                protocolInstance,
		ActiveQoSServices:       activeQoSServices,
		RunInterval:             hydratorConfig.RunInterval,
		MaxEndpointCheckWorkers: hydratorConfig.MaxEndpointCheckWorkers,
		MetricsReporter:         metricsReporter,
		DataReporter:            dataReporter,
	}

	if err := endpointHydrator.Start(); err != nil {
		return nil, err
	}

	return &endpointHydrator, nil
}

// waitForProtocolHealth:
//
// - Blocks until the Protocol reports as healthy
// - Ensures hydrator only starts running once the underlying protocol layer is ready
func waitForProtocolHealth(logger polylog.Logger, protocolInstance gateway.Protocol, timeout time.Duration) error {
	logger.Info().Msg("waitForProtocolHealth: waiting for protocol to become healthy before configuring and starting hydrator")

	start := time.Now()
	for !protocolInstance.IsAlive() {
		if time.Since(start) > timeout {
			return errors.New("waitForProtocolHealth: protocol did not become healthy within timeout")
		}
		logger.Info().Msg("waitForProtocolHealth: protocol not yet healthy, waiting...")
		time.Sleep(1 * time.Second)
	}

	logger.Info().Msg("waitForProtocolHealth: protocol is now healthy, hydrator configuration and startup can proceed")
	return nil
}
