package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/pokt-network/path/config/shannon"
	shannonprotocol "github.com/pokt-network/path/protocol/shannon"
	"github.com/pokt-network/path/reputation"
)

/* ---------------------------------  Gateway Config Struct -------------------------------- */

// GatewayConfig contains all configuration details needed to operate a gateway,
// parsed from a YAML config file.
// The config structure is flattened - full_node_config and gateway_config are at root level.
type GatewayConfig struct {
	// Shannon protocol configuration (flattened from previous shannon_config wrapper)
	FullNodeConfig    shannonprotocol.FullNodeConfig `yaml:"full_node_config"`
	GatewayModeConfig shannonprotocol.GatewayConfig  `yaml:"gateway_config"`

	// Other gateway configurations
	Router             RouterConfig           `yaml:"router_config"`
	Logger             LoggerConfig           `yaml:"logger_config"`
	Metrics            MetricsConfig          `yaml:"metrics_config"`
	HydratorConfig     EndpointHydratorConfig `yaml:"hydrator_config"`
	MessagingConfig    MessagingConfig        `yaml:"messaging_config"`
	DataReporterConfig HTTPDataReporterConfig `yaml:"data_reporter_config"`

	// Global Redis configuration - used by reputation storage (when storage_type is "redis")
	// and leader election for health checks.
	RedisConfig *reputation.RedisConfig `yaml:"redis_config,omitempty"`
}

type EnvConfigError struct {
	Description string
}

func (c EnvConfigError) Error() string {
	return c.Description
}

// LoadGatewayConfigFromYAML reads a YAML configuration file from the specified path
// and unmarshals its content into a GatewayConfig instance.
func LoadGatewayConfigFromYAML(path string) (GatewayConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return GatewayConfig{}, err
	}

	var config GatewayConfig
	if err = yaml.Unmarshal(data, &config); err != nil {
		return GatewayConfig{}, err
	}

	if err = config.hydrateDefaults(); err != nil {
		return GatewayConfig{}, err
	}

	return config, config.validate()
}

func LoadGatewayConfigFromEnv() (GatewayConfig, error) {
	conf := os.Getenv("GATEWAY_CONFIG")

	if conf == "" {
		return GatewayConfig{}, EnvConfigError{Description: "Failed to load config from GATEWAY_CONFIG environment variable"}
	}

	var config GatewayConfig
	err := yaml.Unmarshal([]byte(conf), &config)
	if err != nil {
		return GatewayConfig{}, err
	}

	if err = config.hydrateDefaults(); err != nil {
		return GatewayConfig{}, err
	}

	return config, config.validate()
}

/* --------------------------------- Gateway Config Methods -------------------------------- */

// GetShannonConfig returns a ShannonGatewayConfig constructed from the flattened config fields.
// This maintains compatibility with code that expects the old nested structure.
func (c *GatewayConfig) GetShannonConfig() *shannon.ShannonGatewayConfig {
	return &shannon.ShannonGatewayConfig{
		FullNodeConfig: c.FullNodeConfig,
		GatewayConfig:  c.GatewayModeConfig,
		RedisConfig:    c.RedisConfig,
	}
}

// GetGatewayConfig is an alias for GetShannonConfig for backward compatibility.
// Deprecated: Use GetShannonConfig instead.
func (c *GatewayConfig) GetGatewayConfig() *shannon.ShannonGatewayConfig {
	return c.GetShannonConfig()
}

func (c *GatewayConfig) GetRouterConfig() RouterConfig {
	return c.Router
}

/* --------------------------------- Gateway Config Hydration Helpers -------------------------------- */

func (c *GatewayConfig) hydrateDefaults() error {
	if err := c.Router.hydrateRouterDefaults(); err != nil {
		return fmt.Errorf("invalid router config: %w", err)
	}
	c.Logger.hydrateLoggerDefaults()
	c.Metrics.hydrateMetricsDefaults()
	c.HydratorConfig.hydrateHydratorDefaults()
	c.FullNodeConfig.HydrateDefaults()
	return nil
}

/* --------------------------------- Gateway Config Validation Helpers -------------------------------- */

func (c *GatewayConfig) validate() error {
	if err := c.validateProtocolConfig(); err != nil {
		return err
	}
	if err := c.Logger.Validate(); err != nil {
		return err
	}
	return nil
}

// validateProtocolConfig checks if the protocol configuration is valid.
func (c *GatewayConfig) validateProtocolConfig() error {
	if err := c.FullNodeConfig.Validate(); err != nil {
		return err
	}
	if err := c.GatewayModeConfig.Validate(); err != nil {
		return err
	}
	return nil
}
