package main

import (
	"context"
	"fmt"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/protocol"
)

// defaultProtocolHealthTimeout is the default timeout for waiting for the protocol to become healthy.
const defaultProtocolHealthTimeout = 60 * time.Second

// waitForProtocolHealth blocks until the protocol reports healthy status or the timeout is reached.
// This is used to ensure the protocol has initialized its sessions before proceeding.
func waitForProtocolHealth(logger polylog.Logger, protocol gateway.Protocol, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for protocol to become healthy after %v", timeout)
		case <-ticker.C:
			if protocol.IsAlive() {
				logger.Info().Msg("Protocol is now healthy")
				return nil
			}
			logger.Debug().Msg("Waiting for protocol to become healthy...")
		}
	}
}

// setupHealthCheckExecutor creates and starts the health check executor.
// The health check executor runs configurable health checks against endpoints
// through the protocol layer and records results to the reputation system.
//
// Parameters:
//   - ctx: Background context for the health check executor lifecycle
//   - logger: Logger for health check executor messages
//   - protocol: The protocol instance for sending health check requests
//   - config: Health check configuration from YAML
//   - metricsReporter: Reporter for health check metrics
//   - dataReporter: Reporter for health check data
//   - qosInstances: QoS service instances for each service
//
// Returns the health check executor instance, or nil if not enabled.
func setupHealthCheckExecutor(
	ctx context.Context,
	logger polylog.Logger,
	protocolInstance gateway.Protocol,
	config *gateway.ActiveHealthChecksConfig,
	metricsReporter gateway.RequestResponseReporter,
	dataReporter gateway.RequestResponseReporter,
	qosInstances map[protocol.ServiceID]gateway.QoSService,
) *gateway.HealthCheckExecutor {
	if config == nil || !config.Enabled {
		logger.Info().Msg("Health check executor is disabled")
		return nil
	}

	// Get the reputation service from the protocol
	reputationSvc := protocolInstance.GetReputationService()
	if reputationSvc == nil {
		logger.Warn().Msg("Reputation service not available - health check executor will not record signals")
	}

	// Create the health check executor
	executor := gateway.NewHealthCheckExecutor(gateway.HealthCheckExecutorConfig{
		Config:          config,
		ReputationSvc:   reputationSvc,
		Logger:          logger.With("component", "health_check_executor"),
		Protocol:        protocolInstance,
		MetricsReporter: metricsReporter,
		DataReporter:    dataReporter,
		MaxWorkers:      10,
	})

	// Initialize external config fetching if configured
	executor.InitExternalConfig(ctx)

	// Start the health check loop
	go runHealthCheckLoop(ctx, logger, executor, protocolInstance, qosInstances)

	logger.Info().
		Int("local_service_count", len(config.Local)).
		Bool("external_config_enabled", config.External != nil && config.External.URL != "").
		Msg("Health check executor started")

	return executor
}

// runHealthCheckLoop runs health checks periodically based on configuration.
func runHealthCheckLoop(
	ctx context.Context,
	logger polylog.Logger,
	executor *gateway.HealthCheckExecutor,
	protocolInstance gateway.Protocol,
	qosInstances map[protocol.ServiceID]gateway.QoSService,
) {
	// Default check interval
	checkInterval := 30 * time.Second

	logger.Info().
		Dur("check_interval", checkInterval).
		Msg("Starting health check loop")

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	// Run initial check after a short delay to let services stabilize
	time.Sleep(5 * time.Second)
	runHealthChecks(ctx, logger, executor, protocolInstance, qosInstances)

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("Health check loop stopped")
			executor.Stop()
			return
		case <-ticker.C:
			runHealthChecks(ctx, logger, executor, protocolInstance, qosInstances)
		}
	}
}

// runHealthChecks executes all configured health checks through the protocol layer.
// Health checks are sent as synthetic relay requests, testing the full path including relay miners.
func runHealthChecks(
	ctx context.Context,
	logger polylog.Logger,
	executor *gateway.HealthCheckExecutor,
	protocolInstance gateway.Protocol,
	qosInstances map[protocol.ServiceID]gateway.QoSService,
) {
	if !executor.ShouldRunChecks() {
		return
	}

	logger.Debug().Msg("Running health checks via protocol")

	// Get endpoint addresses from the protocol's endpoint getter
	getEndpointAddrs := func(serviceID protocol.ServiceID) ([]protocol.EndpointAddr, error) {
		endpointInfoFn := protocolInstance.GetEndpointsForHealthCheck()
		infos, err := endpointInfoFn(serviceID)
		if err != nil {
			return nil, err
		}
		addrs := make([]protocol.EndpointAddr, len(infos))
		for i, info := range infos {
			addrs[i] = info.Addr
		}
		return addrs, nil
	}

	// Get QoS service for a service ID
	getServiceQoS := func(serviceID protocol.ServiceID) gateway.QoSService {
		return qosInstances[serviceID]
	}

	// Run all checks through the protocol layer (synthetic relay requests)
	// This tests the full path including relay miners, just like regular user requests.
	err := executor.RunAllChecksViaProtocol(ctx, getEndpointAddrs, getServiceQoS)
	if err != nil {
		logger.Warn().Err(err).Msg("Some health checks failed")
	}
}
