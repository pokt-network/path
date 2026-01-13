package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/redis/go-redis/v9"

	configpkg "github.com/pokt-network/path/config"
	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/health"
	"github.com/pokt-network/path/metrics/devtools"
	protocolPkg "github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/request"
	"github.com/pokt-network/path/router"
)

// Version information injected at build time via ldflags
var (
	Version   string
	Commit    string
	BuildDate string
)

// defaultConfigPath will be appended to the location of
// the executable to get the full path to the config file.
const defaultConfigPath = "config/.config.yaml"

func main() {
	log.Printf(`{"level":"info","message":"PATH 🌿 gateway starting..."}`)

	// Get the config path
	configPath, err := getConfigPath(defaultConfigPath)
	if err != nil {
		log.Fatalf(`{"level":"fatal","error":"%v","message":"failed to get config path"}`, err)
	}

	// Load the config
	config, err := configpkg.LoadGatewayConfigFromYAML(configPath)
	if err != nil {
		log.Printf(`{"level":"info", "error": "%v", "message": "failed to load config from filepath %v. trying GATEWAY_CONFIG environment variable..."}`, err, configPath)
		conf, err := configpkg.LoadGatewayConfigFromEnv()
		if err != nil {
			log.Fatalf(`{"level":"fatal","error":"%v","message":"failed to load config from environment variable and filepath"}`, err)
		}
		config = conf
	}

	// Initialize the logger
	log.Printf(`{"level":"info","message":"Initializing PATH logger with level: %s"}`, config.Logger.Level)
	loggerOpts := []polylog.LoggerOption{
		polyzero.WithLevel(polyzero.ParseLevel(config.Logger.Level)),
	}
	logger := polyzero.NewLogger(loggerOpts...)

	// Log the config path
	logger.Info().Msgf("Starting PATH using config file: %s", configPath)

	// Create a context for background services (pprof, health checks, reputation) that can be canceled during shutdown.
	// This context is used to signal graceful shutdown to all background goroutines.
	backgroundCtx, backgroundCancel := context.WithCancel(context.Background())

	// Create Shannon protocol instance (now the only supported protocol)
	protocol, err := getShannonProtocol(backgroundCtx, logger, config.GetGatewayConfig())
	if err != nil {
		log.Fatalf(`{"level":"fatal","error":"%v","message":"failed to create protocol"}`, err)
	}

	// Prepare the QoS instances using the unified services config
	unifiedServicesConfig := &config.GetGatewayConfig().GatewayConfig.UnifiedServices
	unifiedServicesConfig.HydrateDefaults()
	qosInstances, err := getServiceQoSInstances(logger, config, unifiedServicesConfig, protocol)
	if err != nil {
		log.Fatalf(`{"level":"fatal","error":"%v","message":"failed to setup QoS instances"}`, err)
	}

	// Setup metrics reporter, to be used by Gateway and Health Checks
	metricsReporter, err := setupMetricsServer(logger, config.Metrics.PrometheusAddr)
	if err != nil {
		log.Fatalf(`{"level":"fatal","error":"%v","message":"failed to start metrics server"}`, err)
	}

	// Setup the pprof server with the background context for graceful shutdown
	setupPprofServer(backgroundCtx, logger, config.Metrics.PprofAddr)

	// Setup the leaderboard publisher for endpoint distribution metrics.
	// This publishes endpoint leaderboard data every 10 seconds.
	leaderboardPublisher := setupLeaderboardPublisher(backgroundCtx, logger, protocol)

	// Setup the observation queue for async QoS data extraction.
	// This enables non-blocking response processing with sampled deep parsing.
	// NOTE: This is created before health check executor so it can be passed to both.
	var observationQueue *gateway.ObservationQueue
	observationPipelineConfig := config.GetGatewayConfig().GatewayConfig.ObservationPipelineConfig
	if observationPipelineConfig.Enabled {
		// Convert ObservationPipelineConfig to ObservationQueueConfig (same underlying structure)
		queueConfig := gateway.ObservationQueueConfig(observationPipelineConfig)

		// Create the observation queue
		observationQueue = gateway.NewObservationQueue(queueConfig, logger)

		// Build the extractor registry from unified services config
		extractorRegistry := buildExtractorRegistry(unifiedServicesConfig)
		observationQueue.SetRegistry(extractorRegistry)

		// Set the observation handler for processing extracted data.
		// The handler receives QoS instances so it can update perceived block numbers
		// from sampled requests without blocking the hot path.
		observationHandler := gateway.NewDefaultObservationHandler(logger)
		observationHandler.SetQoSInstances(qosInstances)
		observationQueue.SetHandler(observationHandler)

		// Configure per-service sample rates from unified services config
		if unifiedServicesConfig != nil {
			for _, svc := range unifiedServicesConfig.Services {
				merged := unifiedServicesConfig.GetMergedServiceConfig(svc.ID)
				if merged != nil && merged.ObservationPipeline != nil && merged.ObservationPipeline.SampleRate != nil {
					// Only set per-service rate if it differs from global
					if *merged.ObservationPipeline.SampleRate != queueConfig.SampleRate {
						observationQueue.SetPerServiceRate(svc.ID, *merged.ObservationPipeline.SampleRate)
						logger.Debug().
							Str("service_id", string(svc.ID)).
							Float64("sample_rate", *merged.ObservationPipeline.SampleRate).
							Msg("Configured per-service observation sample rate")
					}
				}
			}
		}

		logger.Info().
			Float64("sample_rate", queueConfig.SampleRate).
			Int("worker_count", queueConfig.WorkerCount).
			Int("queue_size", queueConfig.QueueSize).
			Int("extractor_count", extractorRegistry.Count()).
			Msg("Observation queue initialized")
	}

	// Setup the health check executor for YAML-configurable health checks.
	// This runs health checks against endpoints and records results to the reputation system.
	healthCheckConfig := &config.GetGatewayConfig().GatewayConfig.ActiveHealthChecksConfig

	// Create Redis client for leader election if Redis is configured
	// Redis is used for distributed leader election to ensure only one instance runs health checks
	var redisClient *redis.Client
	if config.RedisConfig != nil {
		redisClient = redis.NewClient(&redis.Options{
			Addr:         config.RedisConfig.Address,
			Password:     config.RedisConfig.Password,
			DB:           config.RedisConfig.DB,
			PoolSize:     config.RedisConfig.PoolSize,
			DialTimeout:  config.RedisConfig.DialTimeout,
			ReadTimeout:  config.RedisConfig.ReadTimeout,
			WriteTimeout: config.RedisConfig.WriteTimeout,
		})

		// Validate Redis connection
		if err := redisClient.Ping(backgroundCtx).Err(); err != nil {
			logger.Warn().Err(err).Msg("Failed to connect to Redis - leader election will be disabled")
			redisClient.Close()
			redisClient = nil
		} else {
			logger.Info().
				Str("address", config.RedisConfig.Address).
				Msg("Redis client initialized for leader election")
		}
	}

	healthCheckExecutor, leaderElector := setupHealthCheckExecutor(
		backgroundCtx,
		logger,
		protocol,
		healthCheckConfig,
		metricsReporter,
		observationQueue,
		unifiedServicesConfig,
		redisClient,
	)
	// Log health check executor status (used for debugging, future shutdown coordination)
	if healthCheckExecutor != nil {
		logger.Info().Msg("Health check executor initialized successfully")
	}

	// Setup the request parser which maps requests to the correct QoS instance.
	requestParser := &request.Parser{
		Logger:      logger,
		QoSServices: qosInstances,
	}

	// NOTE: the gateway uses the requestParser to get the correct QoS instance for any incoming request.
	gtw := &gateway.Gateway{
		Logger:                     logger,
		HTTPRequestParser:          requestParser,
		Protocol:                   protocol,
		MetricsReporter:            metricsReporter,
		WebsocketMessageBufferSize: config.GetRouterConfig().WebsocketMessageBufferSize,
		ObservationQueue:           observationQueue,
	}

	// Until all components are ready, the `/healthz` endpoint will return a 503 Service
	// Unavailable status; once all components are ready, it will return a 200 OK status.
	// health check components must implement the health.Check interface
	// to be able to signal they are ready to service requests.
	components := []health.Check{protocol}
	healthChecker := &health.Checker{
		Logger:            logger,
		Components:        components,
		ServiceIDReporter: protocol,
	}

	// Convert qosInstances to DataReporter map to satisfy the QoSDisqualifiedEndpointsReporter interface.
	qosLevelReporters := make(map[protocolPkg.ServiceID]devtools.QoSDisqualifiedEndpointsReporter)
	for serviceID, qosService := range qosInstances {
		qosLevelReporters[serviceID] = qosService
	}

	// Create the disqualified endpoints reporter to report data on disqualified endpoints
	// through the `/disqualified_endpoints` route for real time QoS data on service endpoints.
	disqualifiedEndpointsReporter := &devtools.DisqualifiedEndpointReporter{
		Logger:                logger,
		ProtocolLevelReporter: protocol,
		QoSLevelReporters:     qosLevelReporters,
	}

	// Initialize the API router to serve requests to the PATH API.
	apiRouter := router.NewRouter(
		logger,
		gtw,
		disqualifiedEndpointsReporter,
		healthChecker,
		config.GetRouterConfig(),
	)

	// -------------------- Start PATH API Router --------------------

	// This will block until the router is stopped.
	server, err := apiRouter.Start()
	if err != nil {
		logger.Error().Err(err).Msg("failed to start PATH API router")
	}

	// -------------------- Log PATH Startup Summary --------------------
	// Log comprehensive startup info for operators

	// Collect configured service IDs
	configuredServiceIDs := make([]string, 0, len(protocol.ConfiguredServiceIDs()))
	for serviceID := range protocol.ConfiguredServiceIDs() {
		configuredServiceIDs = append(configuredServiceIDs, string(serviceID))
	}

	// Log version info if available
	versionInfo := "dev"
	if Version != "" {
		versionInfo = Version
		if Commit != "" {
			versionInfo += " (" + Commit[:min(7, len(Commit))] + ")"
		}
	}

	// Log startup summary
	logger.Info().
		Str("version", versionInfo).
		Str("protocol", protocol.Name()).
		Int("service_count", len(configuredServiceIDs)).
		Str("services", strings.Join(configuredServiceIDs, ", ")).
		Msg("PATH gateway initialized")

	logger.Info().
		Int("port", config.GetRouterConfig().Port).
		Str("metrics_addr", config.Metrics.PrometheusAddr).
		Str("pprof_addr", config.Metrics.PprofAddr).
		Msg("Servers listening")

	// Log endpoints info
	logger.Info().
		Str("requests", fmt.Sprintf("http://localhost:%d/v1", config.GetRouterConfig().Port)).
		Str("health", fmt.Sprintf("http://localhost:%d/healthz", config.GetRouterConfig().Port)).
		Str("metrics", fmt.Sprintf("http://%s/metrics", config.Metrics.PrometheusAddr)).
		Str("pprof", fmt.Sprintf("http://%s/debug/pprof/", config.Metrics.PprofAddr)).
		Msg("Available endpoints")

	// Log health check status
	if healthCheckExecutor != nil {
		logger.Info().
			Bool("enabled", healthCheckConfig.Enabled).
			Bool("leader_election", redisClient != nil).
			Msg("Health checks configured")
	}

	// Log observation pipeline status
	if observationQueue != nil {
		logger.Info().
			Float64("sample_rate", observationPipelineConfig.SampleRate).
			Int("workers", observationPipelineConfig.WorkerCount).
			Msg("Observation pipeline active")
	}

	logger.Info().Msg("PATH gateway ready to accept requests")

	// -------------------- PATH Shutdown --------------------
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info().Msg("Shutting down PATH...")

	// Cancel background context to stop all background services (pprof, health checks)
	backgroundCancel()

	// Stop leader election first to release leadership gracefully
	if leaderElector != nil {
		if err := leaderElector.Stop(); err != nil {
			logger.Warn().Err(err).Msg("Failed to stop leader election gracefully")
		}
	}

	// Stop the health check executor
	if healthCheckExecutor != nil {
		healthCheckExecutor.Stop()
	}

	// Stop the leaderboard publisher
	if leaderboardPublisher != nil {
		leaderboardPublisher.Stop()
	}

	// Stop the observation queue to drain pending observations
	if observationQueue != nil {
		observationQueue.Stop()
	}

	// Close Redis client if it was created
	if redisClient != nil {
		if err := redisClient.Close(); err != nil {
			logger.Warn().Err(err).Msg("Failed to close Redis client")
		}
	}

	// TODO_IMPROVE: Make shutdown timeout configurable and add graceful shutdown of dependencies
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error().Err(err).Msg("PATH forced to shutdown")
	}

	logger.Info().Msg("PATH exited properly")
}

/* -------------------- Gateway Init Helpers -------------------- */

// getConfigPath returns the full path to the config file relative to the executable.
//
// Priority for determining config path:
// - If `-config` flag is set, use its value
// - Otherwise, use defaultConfigPath relative to executable directory
//
// Examples:
// - Executable in `/app` → config at `/app/config/.config.yaml`
// - Executable in `./bin` → config at `./bin/config/.config.yaml`
// - Executable in `./local/path` → config at `./local/path/.config.yaml`
func getConfigPath(defaultConfigPath string) (string, error) {
	var configPath string

	// Check for -config flag override
	flag.StringVar(&configPath, "config", "", "override the default config path")
	flag.Parse()
	if configPath != "" {
		return configPath, nil
	}

	// Get executable directory for default path
	exeDir, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}

	configPath = filepath.Join(filepath.Dir(exeDir), defaultConfigPath)

	return configPath, nil
}
