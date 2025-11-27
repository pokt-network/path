//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/pokt-network/path/protocol"
)

// -----------------------------------------------------------------------------
// Environment Variables
// -----------------------------------------------------------------------------

// Environment variable names
const (
	// [REQUIRED] The test mode to run (e2e or load)
	envTestMode = "TEST_MODE" // The test mode to run (e2e or load)

	// [OPTIONAL] Run the test only against the specified service IDs.
	// If not set, all service IDs for the protocol will be used.
	envTestServiceIDs = "TEST_SERVICE_IDS"

	// [OPTIONAL] Run only Websocket tests (skips HTTP tests entirely).
	// If not set, only HTTP tests will run.
	envTestWebSockets = "TEST_WEBSOCKETS"
)

// getEnvConfig fetches and validates environment config from environment variables
func getEnvConfig() (envConfig, error) {
	testMode := testMode(os.Getenv(envTestMode))
	if err := testMode.isValid(); err != nil {
		return envConfig{}, err
	}

	var testServiceIDs []protocol.ServiceID
	if testServiceIDsEnv := os.Getenv(envTestServiceIDs); testServiceIDsEnv != "" {
		for _, serviceID := range strings.Split(testServiceIDsEnv, ",") {
			testServiceIDs = append(testServiceIDs, protocol.ServiceID(serviceID))
		}
	}

	// Parse Websocket-only mode
	websocketsOnly, _ := strconv.ParseBool(os.Getenv(envTestWebSockets))

	return envConfig{
		testMode:       testMode,
		testServiceIDs: testServiceIDs,
		websocketsOnly: websocketsOnly,
	}, nil
}

// -----------------------------------------------------------------------------
// Enums
// -----------------------------------------------------------------------------

// TODO_TECHDEBT(@commoddity): Separate E2E and Load test modes into separate files and remove the need for this enum.
type testMode string

const (
	testModeE2E  testMode = "e2e"  // Run E2E tests
	testModeLoad testMode = "load" // Run load tests
)

// isValid checks if testMode is valid and set
func (t testMode) isValid() error {
	if t == "" {
		return fmt.Errorf("[REQUIRED] %s environment variable is not set", envTestMode)
	}
	if t != testModeE2E && t != testModeLoad {
		return fmt.Errorf("invalid test mode %s", t)
	}
	return nil
}

// -----------------------------------------------------------------------------
// Config Loading
// -----------------------------------------------------------------------------

// Config file paths relative to the e2e directory
const (
	// Expected name and location of custom config file
	customConfigFile = "config/.e2e_load_test.config.yaml"

	// Default config file (used if custom config file is not found)
	defaultConfigFile = "config/e2e_load_test.config.default.yaml"

	// Services file path
	servicesFile = "config/services_shannon.yaml"

	// Shannon config file path
	shannonConfigFile = "config/.shannon.config.yaml"
)

// loadE2ELoadTestConfig loads the E2E configuration in the following order:
//  1. Custom config in e2e/config/.e2e_load_test.config.yaml
//  2. Default config in e2e/config/e2e_load_test.config.default.yaml
func loadE2ELoadTestConfig() (*Config, error) {
	envConfig, err := getEnvConfig()
	if err != nil {
		return nil, err
	}

	var cfgPath string
	// Prefer custom config if present, otherwise fall back to default
	if _, err := os.Stat(customConfigFile); err == nil {
		fmt.Printf("💽 Using CUSTOM config file: %se2e/%s%s\n\n", CYAN, customConfigFile, RESET)
		cfgPath = customConfigFile
	} else {
		fmt.Printf("💾 Using DEFAULT config file: %se2e/%s%s\n", CYAN, defaultConfigFile, RESET)
		cfgPath = defaultConfigFile
	}

	// Load the config
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return nil, err
	}

	// Set the config path
	cfg.cfgPath = cfgPath

	// Set the environment configuration
	cfg.envConfig = envConfig

	// Load test services
	services, err := loadTestServices()
	if err != nil {
		return nil, fmt.Errorf("failed to load test services: %w", err)
	}
	cfg.services = services

	// Log configuration files being used
	fmt.Printf("📋 Using services config: %se2e/%s%s\n", CYAN, servicesFile, RESET)
	if _, err := os.Stat(shannonConfigFile); err == nil {
		fmt.Printf("🔧 Using Shannon config: %se2e/%s%s\n", CYAN, shannonConfigFile, RESET)
		fmt.Printf("⚠️  %sIMPORTANT:%s E2E tests will fail for services without owned app private keys in %s%s%s\n",
			YELLOW, RESET, CYAN, shannonConfigFile, RESET)
		fmt.Printf("   Check that %s'owned_apps_private_keys_hex'%s includes apps for all services you're testing. ⚠️\n", CYAN, RESET)
	} else {
		fmt.Printf("❌ %sWARNING:%s Shannon config file not found at %se2e/%s%s\n",
			RED, RESET, CYAN, shannonConfigFile, RESET)
		fmt.Printf("   E2E tests will fail! Run: %smake config_prepare_shannon_e2e%s\n", CYAN, RESET)
	}
	fmt.Printf("\n")

	// Validate the configuration
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return cfg, nil
}

// loadConfig loads the E2E configuration from the specified YAML file
func loadConfig(filePath string) (*Config, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// loadTestServices loads test services based on the protocol
func loadTestServices() (TestServices, error) {
	data, err := os.ReadFile(servicesFile)
	if err != nil {
		return TestServices{}, fmt.Errorf("failed to read services file %s: %w", servicesFile, err)
	}

	var services TestServices
	if err := yaml.Unmarshal(data, &services); err != nil {
		return TestServices{}, fmt.Errorf("failed to unmarshal services from %s: %w", servicesFile, err)
	}

	return services, nil
}

// -----------------------------------------------------------------------------
// Config Struct - Configures the test case
//
// Public fields are unmarshalled from the YAML files:
//   - `config/e2e_load_test.config.default.yaml`
//   - `config/.e2e_load_test.config.yaml`
// -----------------------------------------------------------------------------

// DEV_NOTE: All structs and `yaml:` tagged fields must be public to allow for unmarshalling using `gopkg.in/yaml`
type (
	// Config is the top-level E2E test configuration
	Config struct {
		// cfgPath is private because it is not loaded from YAML,
		// so the requirement for it to be public does not apply.
		// Can be either:
		// 		- `config/e2e_load_test.config.default.yaml`
		// 		- `config/.e2e_load_test.config.yaml`
		cfgPath string

		// envConfig is private because it is loaded from environment variables,
		// not YAML so the requirement for it to be public does not apply.
		envConfig envConfig

		// services are set after being unmarshalled from `config/services_shannon.yaml`
		services TestServices

		// Below fields are all unmarshalled from the YAML files
		E2ELoadTestConfig      E2ELoadTestConfig                    `yaml:"e2e_load_test_config"`
		DefaultServiceConfig   ServiceConfig                        `yaml:"default_service_config"`
		ServiceConfigOverrides map[protocol.ServiceID]ServiceConfig `yaml:"service_config_overrides"`
	}

	// envConfig for environment configuration (loaded from environment variables, not YAML)
	envConfig struct {
		testMode       testMode
		testServiceIDs []protocol.ServiceID
		websocketsOnly bool
	}

	// E2ELoadTestConfig for test mode configuration
	E2ELoadTestConfig struct {
		E2EConfig      E2EConfig       `yaml:"e2e_config"`       // E2E test mode configuration
		LoadTestConfig *LoadTestConfig `yaml:"load_test_config"` // Load test mode configuration (pointer, may be nil)
	}

	// E2EConfig for E2E test mode configuration
	E2EConfig struct {
		WaitForHydrator int          `yaml:"wait_for_hydrator"` // Seconds to wait for hydrator checks
		DockerConfig    DockerConfig `yaml:"docker_config"`     // Docker configuration
	}

	// DockerConfig for Docker configuration
	DockerConfig struct {
		DockerLog         bool `yaml:"docker_log"`          // Log Docker container output
		ForceRebuildImage bool `yaml:"force_rebuild_image"` // Force Docker image rebuild (useful after code changes)
	}

	// LoadTestConfig for load test mode configuration
	LoadTestConfig struct {
		GatewayURLOverride  string `yaml:"gateway_url_override"`  // [REQUIRED] Custom PATH gateway URL
		PortalApplicationID string `yaml:"portal_application_id"` // [OPTIONAL] Grove Portal Application ID for the test. Required if using the Grove Portal.
		PortalAPIKey        string `yaml:"portal_api_key"`        // [OPTIONAL] Grove Portal API key for the test. Required if Grove Portal Application requires API key.
	}

	// ServiceConfig for common service configuration options
	ServiceConfig struct {
		GlobalRPS         int           `yaml:"global_rps"`          // Requests per second (shared by all methods)
		RequestsPerMethod int           `yaml:"requests_per_method"` // Total number of requests to send for each method
		SuccessRate       float64       `yaml:"success_rate"`        // Minimum success rate required (0-1)
		MaxP50LatencyMS   time.Duration `yaml:"max_p50_latency_ms"`  // Maximum P50 latency in milliseconds
		MaxP95LatencyMS   time.Duration `yaml:"max_p95_latency_ms"`  // Maximum P95 latency in milliseconds
		MaxP99LatencyMS   time.Duration `yaml:"max_p99_latency_ms"`  // Maximum P99 latency in milliseconds
		Archival          *bool         `yaml:"archival"`            // Whether the service is archival, used to override the default service config
	}
)

// getTestServices returns test services filtered by protocol specified in environment
func (c *Config) getTestServices() ([]*TestService, error) {
	// If no service IDs are specified, include all test cases
	testServiceIds := c.getTestServiceIDs()

	// Track which service IDs were provided but had no test cases
	serviceIdsWithNoTestCases := make(map[string]struct{})
	for _, id := range testServiceIds {
		serviceIdsWithNoTestCases[string(id)] = struct{}{}
	}

	shouldIncludeAllServices := len(testServiceIds) == 0
	var filteredTestCases []*TestService
	for _, tc := range c.services.Services {
		isServiceIdInTestServiceIds := slices.Contains(testServiceIds, tc.ServiceID)
		if shouldIncludeAllServices || isServiceIdInTestServiceIds {
			filteredTestCases = append(filteredTestCases, &tc)
			// Remove from map if found
			delete(serviceIdsWithNoTestCases, string(tc.ServiceID))
		}
	}

	if len(filteredTestCases) == 0 || len(serviceIdsWithNoTestCases) > 0 {
		var missingServiceIds []string
		for id := range serviceIdsWithNoTestCases {
			missingServiceIds = append(missingServiceIds, id)
		}
		fmt.Printf("⚠️ The following service IDs have no E2E / Load test cases and will there be skipped: [%s] ⚠️\n", strings.Join(missingServiceIds, ", "))
		fmt.Printf("⚠️ Please refer to the `e2e/%s` file to see which services are configured ⚠️\n", servicesFile)
	}

	return filteredTestCases, nil
}

// TODO_TECHDEBT(@commoddity): Refactor EVM Tests to avoid `if cfg.getTestMode() == ` checks.
// Separate out load tests and E2E tests into different files.
func (c *Config) getTestMode() testMode {
	return c.envConfig.testMode
}

func (c *Config) getTestServiceIDs() []protocol.ServiceID {
	return c.envConfig.testServiceIDs
}

func (c *Config) isWebSocketsOnly() bool {
	return c.envConfig.websocketsOnly
}

func (c *Config) useServiceSubdomain() bool {
	return !strings.Contains(c.E2ELoadTestConfig.LoadTestConfig.GatewayURLOverride, "localhost")
}

func (c *Config) getGatewayURLForLoadTest() string {
	return c.E2ELoadTestConfig.LoadTestConfig.GatewayURLOverride
}

// validate performs configuration validation based on schema and runtime requirements
func (c *Config) validate() error {
	mode := c.getTestMode()

	// Validate load test mode
	if mode == testModeLoad {
		if c.E2ELoadTestConfig.LoadTestConfig == nil {
			return fmt.Errorf("❌ load test mode requires loadTestConfig to be set")
		}
		if c.E2ELoadTestConfig.LoadTestConfig.GatewayURLOverride == "" {
			return fmt.Errorf("❌ load test mode requires GatewayURLOverride to be set")
		}
		if c.E2ELoadTestConfig.LoadTestConfig.PortalApplicationID == "" {
			return fmt.Errorf("❌ load test mode requires PortalApplicationID to be set")
		}
	}

	// Validate e2e test mode
	if mode == testModeE2E {
		if _, err := os.Stat(shannonConfigFile); os.IsNotExist(err) {
			return fmt.Errorf("e2e test mode requires %s to exist", shannonConfigFile)
		}
	}

	// Validate test services
	if err := c.services.validate(); err != nil {
		return fmt.Errorf("test services validation failed: %w", err)
	}

	// Validate default service config
	if c.DefaultServiceConfig.RequestsPerMethod <= 0 {
		return fmt.Errorf("DefaultServiceConfig.RequestsPerMethod must be greater than 0")
	}
	if c.DefaultServiceConfig.GlobalRPS <= 0 {
		return fmt.Errorf("DefaultServiceConfig.GlobalRPS must be greater than 0")
	}
	if c.DefaultServiceConfig.SuccessRate < 0 || c.DefaultServiceConfig.SuccessRate > 1 {
		return fmt.Errorf("DefaultServiceConfig.SuccessRate must be between 0 and 1")
	}

	return nil
}

// applyOverrides merges non-zero values from the override config into this config.
// This ensures that only fields that are explicitly set in the override config are merged,
// while preserving the default values for the other fields.
func (sc *ServiceConfig) applyOverride(ts *TestService, override *ServiceConfig) {
	if override == nil {
		return
	}

	if override.GlobalRPS != 0 {
		sc.GlobalRPS = override.GlobalRPS
	}
	if override.RequestsPerMethod != 0 {
		sc.RequestsPerMethod = override.RequestsPerMethod
	}
	if override.SuccessRate != 0 {
		sc.SuccessRate = override.SuccessRate
	}
	if override.MaxP50LatencyMS != 0 {
		sc.MaxP50LatencyMS = override.MaxP50LatencyMS
	}
	if override.MaxP95LatencyMS != 0 {
		sc.MaxP95LatencyMS = override.MaxP95LatencyMS
	}
	if override.MaxP99LatencyMS != 0 {
		sc.MaxP99LatencyMS = override.MaxP99LatencyMS
	}
	// If archival override is set, set it on the test service
	if override.Archival != nil {
		ts.Archival = *override.Archival
	}
}
