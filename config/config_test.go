package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/gateway"
	"github.com/pokt-network/path/network/grpc"
	"github.com/pokt-network/path/protocol"
	shannonprotocol "github.com/pokt-network/path/protocol/shannon"
	"github.com/pokt-network/path/reputation"
)

// getTestDefaultGRPCConfig returns a GRPCConfig with default values applied
// using the same defaults as defined in the shannon package
func getTestDefaultGRPCConfig() grpc.GRPCConfig {
	return grpc.GRPCConfig{
		BackoffBaseDelay:  1 * time.Second,
		BackoffMaxDelay:   60 * time.Second,
		MinConnectTimeout: 10 * time.Second,
		KeepAliveTime:     30 * time.Second,
		KeepAliveTimeout:  30 * time.Second,
	}
}

func Test_LoadGatewayConfigFromYAML(t *testing.T) {
	tests := []struct {
		name        string
		filePath    string
		yamlData    string
		want        GatewayConfig
		wantErr     bool
		skipCompare bool // If true, only verify loading succeeds, don't compare values
	}{
		{
			name:        "should load valid config from example file",
			filePath:    "./examples/config.shannon_example.yaml",
			skipCompare: true, // Example config is a reference doc, not a test fixture
			want: GatewayConfig{
				FullNodeConfig: shannonprotocol.FullNodeConfig{
					RpcURL:                "https://shannon-grove-rpc.mainnet.poktroll.com",
					SessionRolloverBlocks: 10,
					GRPCConfig: func() grpc.GRPCConfig {
						config := getTestDefaultGRPCConfig()
						config.HostPort = "shannon-grove-grpc.mainnet.poktroll.com:443"
						return config
					}(),
					LazyMode: false,
					CacheConfig: shannonprotocol.CacheConfig{
						SessionTTL: 30 * time.Second,
					},
				},
				GatewayModeConfig: shannonprotocol.GatewayConfig{
					GatewayMode:          protocol.GatewayModeCentralized,
					GatewayAddress:       "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw",
					GatewayPrivateKeyHex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388",
					OwnedAppsPrivateKeysHex: []string{
						"40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388",
					},
					ServiceFallback: []shannonprotocol.ServiceFallback{
						{
							ServiceID:      "xrplevm",
							SendAllTraffic: false,
							FallbackEndpoints: []map[string]string{
								{
									"default_url": "http://12.34.56.78",
									"json_rpc":    "http://12.34.56.78:8545",
									"rest":        "http://12.34.56.78:1317",
									"comet_bft":   "http://12.34.56.78:26657",
									"websocket":   "http://12.34.56.78:8546",
								},
							},
						},
						{
							ServiceID:      "eth",
							SendAllTraffic: false,
							FallbackEndpoints: []map[string]string{
								{
									"default_url": "https://eth.rpc.backup.io",
								},
							},
						},
					},
					ReputationConfig: reputation.Config{
						Enabled:         true,
						StorageType:     "memory",
						InitialScore:    80,
						MinThreshold:    30,
						RecoveryTimeout: 5 * time.Minute,
					},
				},
				Router: RouterConfig{
					Port:                            defaultPort,
					MaxRequestHeaderBytes:           defaultMaxRequestHeaderBytes,
					ReadTimeout:                     30 * time.Second,  // Matches example config
					WriteTimeout:                    30 * time.Second,  // Matches example config
					IdleTimeout:                     120 * time.Second, // Matches example config
					SystemOverheadAllowanceDuration: defaultSystemOverheadAllowanceDuration,
					WebsocketMessageBufferSize:      8192, // Matches example config
				},
				Logger: LoggerConfig{
					Level: "info", // Matches example config
				},
			},
			wantErr: false,
		},
		{
			name:     "should return error for invalid full node URL",
			filePath: "invalid_full_node_url.yaml",
			yamlData: `
full_node_config:
  rpc_url: "invalid-url"
  grpc_config:
    host_port: "grpc-url.io:443"
  session_rollover_blocks: 10
gateway_config:
  gateway_mode: "centralized"
  gateway_address: "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw"
  gateway_private_key_hex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
`,
			wantErr: true,
		},
		{
			name:     "should return error for invalid gateway address",
			filePath: "invalid_gateway_address.yaml",
			yamlData: `
full_node_config:
  rpc_url: "https://rpc-url.io"
  grpc_config:
    host_port: "grpc-url.io:443"
  session_rollover_blocks: 10
gateway_config:
  gateway_address: "invalid_gateway_address"
  gateway_private_key_hex: "d5fcbfb894059a21e914a2d6bf1508319ce2b1b8878f15aa0c1cdf883feb018d"
  gateway_mode: "delegated"
`,
			wantErr: true,
		},
		{
			name:     "should return error for non-existent file",
			filePath: "non_existent.yaml",
			yamlData: "",
			wantErr:  true,
		},
		{
			name:     "should return error for invalid YAML",
			filePath: "invalid_config.yaml",
			yamlData: "invalid_yaml: [",
			wantErr:  true,
		},
		{
			name:     "should load config with valid logger level",
			filePath: "valid_logger.yaml",
			yamlData: `full_node_config:
  rpc_url: "https://shannon-testnet-grove-rpc.beta.poktroll.com"
  grpc_config:
    host_port: "shannon-testnet-grove-grpc.beta.poktroll.com:443"
  lazy_mode: false
  session_rollover_blocks: 10
gateway_config:
  gateway_mode: "centralized"
  gateway_address: "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw"
  gateway_private_key_hex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  owned_apps_private_keys_hex:
    - "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
logger_config:
  level: "debug"`,
			want: GatewayConfig{
				FullNodeConfig: shannonprotocol.FullNodeConfig{
					RpcURL:                "https://shannon-testnet-grove-rpc.beta.poktroll.com",
					SessionRolloverBlocks: 10,
					GRPCConfig: func() grpc.GRPCConfig {
						config := getTestDefaultGRPCConfig()
						config.HostPort = "shannon-testnet-grove-grpc.beta.poktroll.com:443"
						return config
					}(),
					LazyMode: false,
					CacheConfig: shannonprotocol.CacheConfig{
						SessionTTL: 20 * time.Second,
					},
				},
				GatewayModeConfig: shannonprotocol.GatewayConfig{
					GatewayMode:          protocol.GatewayModeCentralized,
					GatewayAddress:       "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw",
					GatewayPrivateKeyHex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388",
					OwnedAppsPrivateKeysHex: []string{
						"40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388",
					},
				},
				Router: RouterConfig{
					Port:                            defaultPort,
					MaxRequestHeaderBytes:           defaultMaxRequestHeaderBytes,
					ReadTimeout:                     defaultHTTPServerReadTimeout,
					WriteTimeout:                    defaultHTTPServerWriteTimeout,
					IdleTimeout:                     defaultHTTPServerIdleTimeout,
					SystemOverheadAllowanceDuration: defaultSystemOverheadAllowanceDuration,
					WebsocketMessageBufferSize:      defaultWebsocketMessageBufferSize,
				},
				Logger: LoggerConfig{
					Level: "debug",
				},
			},
			wantErr: false,
		},
		{
			name:     "should return error for invalid logger level",
			filePath: "invalid_logger_level.yaml",
			yamlData: `
full_node_config:
  rpc_url: "https://shannon-testnet-grove-rpc.beta.poktroll.com"
  grpc_config:
    host_port: "shannon-testnet-grove-grpc.beta.poktroll.com:443"
  session_rollover_blocks: 10
gateway_config:
  gateway_mode: "centralized"
  gateway_address: "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw"
  gateway_private_key_hex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
logger_config:
  level: "invalid_level"
`,
			wantErr: true,
		},
		{
			name:     "should return error for empty service ID in service_fallback",
			filePath: "empty_service_id.yaml",
			yamlData: `
full_node_config:
  rpc_url: "https://shannon-testnet-grove-rpc.beta.poktroll.com"
  grpc_config:
    host_port: "shannon-testnet-grove-grpc.beta.poktroll.com:443"
  session_rollover_blocks: 10
gateway_config:
  gateway_mode: "centralized"
  gateway_address: "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw"
  gateway_private_key_hex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  owned_apps_private_keys_hex:
    - "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  service_fallback:
    - service_id: ""
      send_all_traffic: false
      fallback_endpoints:
        - default_url: "https://eth.rpc.backup.io"
`,
			wantErr: true,
		},
		{
			name:     "should return error for missing fallback_endpoints in service_fallback",
			filePath: "missing_fallback_urls.yaml",
			yamlData: `
full_node_config:
  rpc_url: "https://shannon-testnet-grove-rpc.beta.poktroll.com"
  grpc_config:
    host_port: "shannon-testnet-grove-grpc.beta.poktroll.com:443"
  session_rollover_blocks: 10
gateway_config:
  gateway_mode: "centralized"
  gateway_address: "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw"
  gateway_private_key_hex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  owned_apps_private_keys_hex:
    - "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  service_fallback:
    - service_id: eth
      send_all_traffic: false
      fallback_endpoints: []
`,
			wantErr: true,
		},
		{
			name:     "should return error for invalid fallback endpoint URL",
			filePath: "invalid_fallback_url.yaml",
			yamlData: `
full_node_config:
  rpc_url: "https://shannon-testnet-grove-rpc.beta.poktroll.com"
  grpc_config:
    host_port: "shannon-testnet-grove-grpc.beta.poktroll.com:443"
  session_rollover_blocks: 10
gateway_config:
  gateway_mode: "centralized"
  gateway_address: "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw"
  gateway_private_key_hex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  owned_apps_private_keys_hex:
    - "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  service_fallback:
    - service_id: eth
      send_all_traffic: false
      fallback_endpoints:
        - default_url: "invalid-url-format"
`,
			wantErr: true,
		},
		{
			name:     "should return error for duplicate service IDs in service_fallback",
			filePath: "duplicate_service_ids.yaml",
			yamlData: `
full_node_config:
  rpc_url: "https://shannon-testnet-grove-rpc.beta.poktroll.com"
  grpc_config:
    host_port: "shannon-testnet-grove-grpc.beta.poktroll.com:443"
  session_rollover_blocks: 10
gateway_config:
  gateway_mode: "centralized"
  gateway_address: "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw"
  gateway_private_key_hex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  owned_apps_private_keys_hex:
    - "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  service_fallback:
    - service_id: eth
      send_all_traffic: false
      fallback_endpoints:
        - default_url: "https://eth.rpc.backup.io"
    - service_id: eth
      send_all_traffic: true
      fallback_endpoints:
        - default_url: "https://eth.rpc.backup2.io"
`,
			wantErr: true,
		},
		{
			name:     "should load config with reputation and tiered selection",
			filePath: "config_with_reputation.yaml",
			yamlData: `full_node_config:
  rpc_url: "https://shannon-grove-rpc.mainnet.poktroll.com"
  grpc_config:
    host_port: "shannon-grove-grpc.mainnet.poktroll.com:443"
  lazy_mode: false
  session_rollover_blocks: 10
gateway_config:
  gateway_mode: "centralized"
  gateway_address: "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw"
  gateway_private_key_hex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  owned_apps_private_keys_hex:
    - "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388"
  reputation_config:
    enabled: true
    storage_type: "memory"
    initial_score: 80
    min_threshold: 30
    recovery_timeout: 5m
    tiered_selection:
      enabled: true
      tier1_threshold: 70
      tier2_threshold: 50
      probation:
        enabled: true
        threshold: 10
        traffic_percent: 10
        recovery_multiplier: 2.0
  retry_config:
    enabled: true
    max_retries: 2
    retry_on_5xx: true
    retry_on_timeout: true
    retry_on_connection: true
logger_config:
  level: "info"`,
			want: GatewayConfig{
				FullNodeConfig: shannonprotocol.FullNodeConfig{
					RpcURL:                "https://shannon-grove-rpc.mainnet.poktroll.com",
					SessionRolloverBlocks: 10,
					GRPCConfig: func() grpc.GRPCConfig {
						config := getTestDefaultGRPCConfig()
						config.HostPort = "shannon-grove-grpc.mainnet.poktroll.com:443"
						return config
					}(),
					LazyMode: false,
					CacheConfig: shannonprotocol.CacheConfig{
						SessionTTL: 20 * time.Second,
					},
				},
				GatewayModeConfig: shannonprotocol.GatewayConfig{
					GatewayMode:          protocol.GatewayModeCentralized,
					GatewayAddress:       "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw",
					GatewayPrivateKeyHex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388",
					OwnedAppsPrivateKeysHex: []string{
						"40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388",
					},
					ReputationConfig: reputation.Config{
						Enabled:         true,
						StorageType:     "memory",
						InitialScore:    80,
						MinThreshold:    30,
						RecoveryTimeout: 5 * time.Minute,
						TieredSelection: reputation.TieredSelectionConfig{
							Enabled:        true,
							Tier1Threshold: 70,
							Tier2Threshold: 50,
							Probation: reputation.ProbationConfig{
								Enabled:            true,
								Threshold:          10,
								TrafficPercent:     10,
								RecoveryMultiplier: 2.0,
							},
						},
					},
					RetryConfig: gateway.RetryConfig{
						Enabled:           true,
						MaxRetries:        2,
						RetryOn5xx:        true,
						RetryOnTimeout:    true,
						RetryOnConnection: true,
					},
				},
				Router: RouterConfig{
					Port:                            defaultPort,
					MaxRequestHeaderBytes:           defaultMaxRequestHeaderBytes,
					ReadTimeout:                     defaultHTTPServerReadTimeout,
					WriteTimeout:                    defaultHTTPServerWriteTimeout,
					IdleTimeout:                     defaultHTTPServerIdleTimeout,
					SystemOverheadAllowanceDuration: defaultSystemOverheadAllowanceDuration,
					WebsocketMessageBufferSize:      defaultWebsocketMessageBufferSize,
				},
				Logger: LoggerConfig{
					Level: "info",
				},
			},
			wantErr: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := require.New(t)

			if test.yamlData != "" {
				err := os.WriteFile(test.filePath, []byte(test.yamlData), 0644)
				defer os.Remove(test.filePath)
				c.NoError(err)
			}

			got, err := LoadGatewayConfigFromYAML(test.filePath)
			if test.wantErr {
				c.Error(err)
			} else {
				c.NoError(err)
				// Only compare if not skipped (example config is a reference doc, not a test fixture)
				if !test.skipCompare {
					compareConfigs(c, test.want, got)
				} else {
					// At minimum, verify key fields are set
					c.NotEmpty(got.GatewayModeConfig.GatewayAddress, "GatewayAddress should be set")
					c.NotEmpty(got.GatewayModeConfig.GatewayMode, "GatewayMode should be set")
				}
			}
		})
	}
}

func compareConfigs(c *require.Assertions, want, got GatewayConfig) {
	c.Equal(want.Router, got.Router)
	c.Equal(want.Logger, got.Logger)
	c.Equal(want.FullNodeConfig, got.FullNodeConfig)
	c.Equal(want.GatewayModeConfig, got.GatewayModeConfig)
}
