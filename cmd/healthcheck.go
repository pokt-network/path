package main

import (
	"context"
	"fmt"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/redis/go-redis/v9"

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
//   - observationQueue: Queue for async observation processing (optional)
//   - unifiedServicesConfig: Unified services config for per-service health check overrides
//   - redisClient: Redis client for leader election (optional, nil if Redis not configured)
//
// Returns the health check executor instance and leader elector (may be nil), or nil/nil if not enabled.
func setupHealthCheckExecutor(
	ctx context.Context,
	logger polylog.Logger,
	protocolInstance gateway.Protocol,
	config *gateway.ActiveHealthChecksConfig,
	metricsReporter gateway.RequestResponseReporter,
	dataReporter gateway.RequestResponseReporter,
	observationQueue *gateway.ObservationQueue,
	unifiedServicesConfig *gateway.UnifiedServicesConfig,
	redisClient *redis.Client,
) (*gateway.HealthCheckExecutor, *gateway.LeaderElector) {
	if config == nil || !config.Enabled {
		logger.Info().Msg("Health check executor is disabled")
		return nil, nil
	}

	// Get the reputation service from the protocol
	reputationSvc := protocolInstance.GetReputationService()
	if reputationSvc == nil {
		logger.Warn().Msg("Reputation service not available - health check executor will not record signals")
	}

	// Create the health check executor
	executor := gateway.NewHealthCheckExecutor(gateway.HealthCheckExecutorConfig{
		Config:                config,
		ReputationSvc:         reputationSvc,
		Logger:                logger.With("component", "health_check_executor"),
		Protocol:              protocolInstance,
		MetricsReporter:       metricsReporter,
		DataReporter:          dataReporter,
		ObservationQueue:      observationQueue,
		MaxWorkers:            10,
		UnifiedServicesConfig: unifiedServicesConfig,
	})

	// Initialize external config fetching if configured
	executor.InitExternalConfig(ctx)

	// Initialize leader election if configured
	var leaderElector *gateway.LeaderElector
	if config.Coordination.Type == "leader_election" {
		if redisClient == nil {
			logger.Warn().Msg("Leader election configured but Redis client not available - falling back to single-instance mode")
		} else {
			leaderElector = gateway.NewLeaderElector(gateway.LeaderElectorConfig{
				Client: redisClient,
				Config: config.Coordination,
				Logger: logger.With("component", "leader_elector"),
			})
			if leaderElector != nil {
				if err := leaderElector.Start(ctx); err != nil {
					logger.Warn().Err(err).Msg("Failed to start leader election - falling back to single-instance mode")
					leaderElector = nil
				} else {
					logger.Info().
						Dur("lease_duration", config.Coordination.LeaseDuration).
						Dur("renew_interval", config.Coordination.RenewInterval).
						Str("key", config.Coordination.Key).
						Msg("Leader election started for health checks")
				}
			}
		}
	}

	// Start the health check loop
	go runHealthCheckLoop(ctx, logger, executor, protocolInstance, leaderElector)

	logger.Info().
		Int("local_service_count", len(config.Local)).
		Bool("external_config_enabled", config.External != nil && config.External.URL != "").
		Bool("leader_election_enabled", leaderElector != nil).
		Msg("Health check executor started")

	return executor, leaderElector
}

// runHealthCheckLoop runs health checks periodically based on configuration.
func runHealthCheckLoop(
	ctx context.Context,
	logger polylog.Logger,
	executor *gateway.HealthCheckExecutor,
	protocolInstance gateway.Protocol,
	leaderElector *gateway.LeaderElector,
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
	runHealthChecks(ctx, logger, executor, protocolInstance, leaderElector)

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("Health check loop stopped")
			executor.Stop()
			return
		case <-ticker.C:
			runHealthChecks(ctx, logger, executor, protocolInstance, leaderElector)
		}
	}
}

// runHealthChecks executes all configured health checks through the protocol layer.
// Health checks are sent as synthetic relay requests, testing the full path including relay miners.
// If leader election is enabled, only the leader instance runs health checks.
func runHealthChecks(
	ctx context.Context,
	logger polylog.Logger,
	executor *gateway.HealthCheckExecutor,
	protocolInstance gateway.Protocol,
	leaderElector *gateway.LeaderElector,
) {
	if !executor.ShouldRunChecks() {
		return
	}

	// Check leader election status if configured
	if leaderElector != nil {
		if !leaderElector.IsLeader() {
			logger.Debug().Msg("Not the leader - skipping health checks")
			return
		}
		logger.Debug().Msg("Leader status confirmed - running health checks")
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

	// Run all checks through the protocol layer (synthetic relay requests)
	// This tests the full path including relay miners, just like regular user requests.
	err := executor.RunAllChecksViaProtocol(ctx, getEndpointAddrs)
	if err != nil {
		logger.Warn().Err(err).Msg("Some health checks failed")
	}
}
