package shannon

import (
	"gopkg.in/yaml.v3"

	shannonprotocol "github.com/pokt-network/path/protocol/shannon"
	"github.com/pokt-network/path/reputation"
)

// Fields that are unmarshaled from the config YAML must be capitalized.
type ShannonGatewayConfig struct {
	FullNodeConfig shannonprotocol.FullNodeConfig `yaml:"full_node_config"`
	GatewayConfig  shannonprotocol.GatewayConfig  `yaml:"gateway_config"`

	// RedisConfig is the global Redis configuration passed from the top-level config.
	// Used by reputation storage (when storage_type is "redis") and leader election.
	RedisConfig *reputation.RedisConfig `yaml:"-"` // Not from YAML, passed programmatically
}

// UnmarshalYAML is a custom unmarshaller for GatewayConfig.
// It performs validation after unmarshaling the config.
func (c *ShannonGatewayConfig) UnmarshalYAML(value *yaml.Node) error {
	// Temp alias to avoid recursion; this is the recommend pattern for Go YAML custom unmarshalers
	type temp ShannonGatewayConfig
	var val struct {
		temp `yaml:",inline"`
	}
	if err := value.Decode(&val); err != nil {
		return err
	}
	*c = ShannonGatewayConfig(val.temp)
	return nil
}

// validate checks if the configuration is valid after loading it from the YAML file.
func (c ShannonGatewayConfig) Validate() error {
	if err := c.FullNodeConfig.Validate(); err != nil {
		return err
	}
	if err := c.GatewayConfig.Validate(); err != nil {
		return err
	}
	return nil
}
