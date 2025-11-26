package main

// TODO_TECHDEBT(@olshansk): Revisit the name `hydrator` to something more appropriate.

import (
	"errors"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/config"
	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
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

	endpointHydrator := gateway.EndpointHydrator{
		Logger:                  cmdLogger,
		Protocol:                protocolInstance,
		ActiveQoSServices:       qosServices,
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
