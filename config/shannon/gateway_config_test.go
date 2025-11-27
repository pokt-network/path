package shannon

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pokt-network/path/network/grpc"
	"github.com/pokt-network/path/protocol"
	shannonprotocol "github.com/pokt-network/path/protocol/shannon"
)

func Test_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  ShannonGatewayConfig
		wantErr bool
	}{
		// TODO_MVP(@adshmh): add unit tests for all GatewayConfig struct validation failure scenarios.
		{
			name: "should pass with valid config",
			config: ShannonGatewayConfig{
				FullNodeConfig: shannonprotocol.FullNodeConfig{
					RpcURL:                "https://rpc-url.io",
					SessionRolloverBlocks: 24,
					GRPCConfig: grpc.GRPCConfig{
						HostPort: "grpc-url.io:443",
					},
				},
				GatewayConfig: shannonprotocol.GatewayConfig{
					GatewayMode:          protocol.GatewayModeDelegated,
					GatewayAddress:       "pokt1710ed9a8d0986d808e607c5815cc5a13f15dba",
					GatewayPrivateKeyHex: "d5fcbfb894059a21e914a2d6bf1508319ce2b1b8878f15aa0c1cdf883feb018d",
				},
			},
			wantErr: false,
		},
		{
			name: "should fail with invalid URL",
			config: ShannonGatewayConfig{
				FullNodeConfig: shannonprotocol.FullNodeConfig{
					RpcURL: "invalid-url",
					GRPCConfig: grpc.GRPCConfig{
						HostPort: "grpc-url.io:443",
					},
				},
			},
			wantErr: true,
		},
		{
			name: "should fail with invalid gateway address",
			config: ShannonGatewayConfig{
				FullNodeConfig: shannonprotocol.FullNodeConfig{
					RpcURL: "https://rpc-url.io",
					GRPCConfig: grpc.GRPCConfig{
						HostPort: "grpc-url.io:443",
					},
				},
				GatewayConfig: shannonprotocol.GatewayConfig{
					GatewayMode:          protocol.GatewayModeDelegated,
					GatewayAddress:       "invalid_address",
					GatewayPrivateKeyHex: "d5fcbfb894059a21e914a2d6bf1508319ce2b1b8878f15aa0c1cdf883feb018d",
				},
			},
			wantErr: true,
		},
		{
			name: "should fail with invalid gateway private key",
			config: ShannonGatewayConfig{
				FullNodeConfig: shannonprotocol.FullNodeConfig{
					RpcURL: "https://rpc-url.io",
					GRPCConfig: grpc.GRPCConfig{
						HostPort: "grpc-url.io:443",
					},
				},
				GatewayConfig: shannonprotocol.GatewayConfig{
					GatewayMode:          protocol.GatewayModeDelegated,
					GatewayAddress:       "pokt1710ed9a8d0986d808e607c5815cc5a13f15dba",
					GatewayPrivateKeyHex: "invalid_private_key",
				},
			},
			wantErr: true,
		},
		{
			name: "should fail with invalid session rollover blocks",
			config: ShannonGatewayConfig{
				FullNodeConfig: shannonprotocol.FullNodeConfig{
					RpcURL:                "https://rpc-url.io",
					SessionRolloverBlocks: 0, // Invalid: must be positive
					GRPCConfig: grpc.GRPCConfig{
						HostPort: "grpc-url.io:443",
					},
				},
				GatewayConfig: shannonprotocol.GatewayConfig{
					GatewayMode:          protocol.GatewayModeDelegated,
					GatewayAddress:       "pokt1710ed9a8d0986d808e607c5815cc5a13f15dba",
					GatewayPrivateKeyHex: "d5fcbfb894059a21e914a2d6bf1508319ce2b1b8878f15aa0c1cdf883feb018d",
				},
			},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
