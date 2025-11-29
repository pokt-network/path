package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestRouterConfig_UnmarshalYAML(t *testing.T) {
	tests := []struct {
		name     string
		yamlData string
		want     RouterConfig
		wantErr  bool
	}{
		{
			name: "should unmarshal without error",
			yamlData: `
port: 8080
`,
			want: RouterConfig{
				Port: 8080,
			},
			wantErr: false,
		},
		{
			name: "should return error for invalid YAML",
			yamlData: `
port: invalid_port
`,
			want:    RouterConfig{},
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := require.New(t)
			var got RouterConfig
			err := yaml.Unmarshal([]byte(test.yamlData), &got)
			if test.wantErr {
				c.Error(err)
			} else {
				c.NoError(err)
				c.Equal(test.want, got)
			}
		})
	}
}

func TestRouterConfig_hydrateRouterDefaults(t *testing.T) {
	tests := []struct {
		name    string
		cfg     RouterConfig
		want    RouterConfig
		wantErr bool
	}{
		{
			name: "should set all defaults",
			cfg:  RouterConfig{},
			want: RouterConfig{
				Port:                            defaultPort,
				MaxRequestHeaderBytes:           defaultMaxRequestHeaderBytes,
				ReadTimeout:                     defaultHTTPServerReadTimeout,
				WriteTimeout:                    defaultHTTPServerWriteTimeout,
				IdleTimeout:                     defaultHTTPServerIdleTimeout,
				SystemOverheadAllowanceDuration: defaultSystemOverheadAllowanceDuration,
				WebsocketMessageBufferSize:      defaultWebsocketMessageBufferSize,
			},
			wantErr: false,
		},
		{
			name: "should not override set values",
			cfg: RouterConfig{
				Port: 8080,
			},
			want: RouterConfig{
				Port:                            8080,
				MaxRequestHeaderBytes:           defaultMaxRequestHeaderBytes,
				ReadTimeout:                     defaultHTTPServerReadTimeout,
				WriteTimeout:                    defaultHTTPServerWriteTimeout,
				IdleTimeout:                     defaultHTTPServerIdleTimeout,
				SystemOverheadAllowanceDuration: defaultSystemOverheadAllowanceDuration,
				WebsocketMessageBufferSize:      defaultWebsocketMessageBufferSize,
			},
			wantErr: false,
		},
		{
			name: "should return error when system overhead exceeds read timeout",
			cfg: RouterConfig{
				ReadTimeout:                     5 * time.Second,
				WriteTimeout:                    120 * time.Second,
				SystemOverheadAllowanceDuration: 10 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "should return error when system overhead exceeds write timeout",
			cfg: RouterConfig{
				ReadTimeout:                     60 * time.Second,
				WriteTimeout:                    5 * time.Second,
				SystemOverheadAllowanceDuration: 10 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "should not override custom websocket buffer size",
			cfg: RouterConfig{
				WebsocketMessageBufferSize: 500,
			},
			want: RouterConfig{
				Port:                            defaultPort,
				MaxRequestHeaderBytes:           defaultMaxRequestHeaderBytes,
				ReadTimeout:                     defaultHTTPServerReadTimeout,
				WriteTimeout:                    defaultHTTPServerWriteTimeout,
				IdleTimeout:                     defaultHTTPServerIdleTimeout,
				SystemOverheadAllowanceDuration: defaultSystemOverheadAllowanceDuration,
				WebsocketMessageBufferSize:      500, // Custom value should be preserved
			},
			wantErr: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := require.New(t)
			err := test.cfg.hydrateRouterDefaults()
			if test.wantErr {
				c.Error(err)
			} else {
				c.NoError(err)
				c.Equal(test.want, test.cfg)
			}
		})
	}
}
