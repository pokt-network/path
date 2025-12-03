package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/config/shannon"
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
		name     string
		filePath string
		yamlData string
		want     GatewayConfig
		wantErr  bool
	}{
		{
			name:     "should load valid shannon config without error",
			filePath: "./examples/config.shannon_example.yaml",
			want: GatewayConfig{
				ShannonConfig: &shannon.ShannonGatewayConfig{
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
					GatewayConfig: shannonprotocol.GatewayConfig{
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
						SanctionConfig: shannonprotocol.SanctionConfig{
							SessionSanctionDuration: 30 * time.Minute,
							CacheCleanupInterval:    5 * time.Minute,
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
						RetryConfig: shannonprotocol.RetryConfig{
							Enabled:           true,
							MaxRetries:        1,
							RetryOn5xx:        true,
							RetryOnTimeout:    true,
							RetryOnConnection: true,
						},
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
					Level: "error",
				},
			},
			wantErr: false,
		},
		{
			name:     "should return error for invalid full node URL",
			filePath: "invalid_full_node_url.yaml",
			yamlData: `
			shannon_config:
			  full_node_config:
			    rpc_url: "invalid-url"
			    grpc_url: "grpcs://grpc-url.io"
			    session_rollover_blocks: 10
			`,
			wantErr: true,
		},
		{
			name:     "should return error for invalid gateway address",
			filePath: "invalid_gateway_address.yaml",
			yamlData: `
			shannon_config:
			  full_node_config:
			    rpc_url: "https://rpc-url.io"
			    grpc_url: "grpcs://grpc-url.io"
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
			yamlData: `shannon_config:
  full_node_config:
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
				ShannonConfig: &shannon.ShannonGatewayConfig{
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
					GatewayConfig: shannonprotocol.GatewayConfig{
						GatewayMode:          protocol.GatewayModeCentralized,
						GatewayAddress:       "pokt1up7zlytnmvlsuxzpzvlrta95347w322adsxslw",
						GatewayPrivateKeyHex: "40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388",
						OwnedAppsPrivateKeysHex: []string{
							"40af4e7e1b311c76a573610fe115cd2adf1eeade709cd77ca31ad4472509d388",
						},
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
			shannon_config:
			  full_node_config:
			    rpc_url: "https://shannon-testnet-grove-rpc.beta.poktroll.com"
			    grpc_config:
			      host_port: "shannon-testnet-grove-grpc.beta.poktroll.com:443"
			    session_rollover_blocks: 10
			  gateway_config:
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
			shannon_config:
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
			          - json_rpc: "https://eth.rpc.backup.io""
			`,
			wantErr: true,
		},
		{
			name:     "should return error for missing fallback_endpoints in service_fallback",
			filePath: "missing_fallback_urls.yaml",
			yamlData: `
			shannon_config:
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
			shannon_config:
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
			          - json_rpc: "invalid-url-format"
			          - json_rpc: "ftp://invalid.protocol.com"
			`,
			wantErr: true,
		},
		{
			name:     "should return error for duplicate service IDs in service_fallback",
			filePath: "duplicate_service_ids.yaml",
			yamlData: `
			shannon_config:
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
			          - json_rpc: "https://eth.rpc.backup.io""
			      - service_id: eth
			        send_all_traffic: true
			        fallback_endpoints:
			          - json_rpc: "https://eth.rpc.backup.io""
			`,
			wantErr: true,
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
				compareConfigs(c, test.want, got)
			}
		})
	}
}

func compareConfigs(c *require.Assertions, want, got GatewayConfig) {
	c.Equal(want.Router, got.Router)
	c.Equal(want.Logger, got.Logger)
	if want.ShannonConfig != nil {
		c.Equal(want.ShannonConfig, got.ShannonConfig)
	}
}
