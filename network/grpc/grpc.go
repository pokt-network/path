package grpc

import (
	"crypto/tls"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Default gRPC configuration values optimized for production workloads.
// These can be overridden via YAML configuration.
const (
	// Backoff configuration for connection retries
	defaultBackoffBaseDelay  = 1 * time.Second
	defaultBackoffMaxDelay   = 60 * time.Second
	defaultMinConnectTimeout = 10 * time.Second

	// Keepalive configuration to maintain healthy connections
	// and detect dead connections quickly under high load.
	defaultKeepAliveTime    = 30 * time.Second // Send pings every 30s if no activity
	defaultKeepAliveTimeout = 30 * time.Second // Wait 30s for ping ack before considering dead

	// Connection pool settings for high-throughput scenarios
	defaultInitialWindowSize     = 1 << 20 // 1MB - initial flow control window
	defaultInitialConnWindowSize = 1 << 20 // 1MB - connection-level flow control window
	defaultMaxRecvMsgSize        = 1 << 22 // 4MB - max message size
	defaultMaxSendMsgSize        = 1 << 22 // 4MB - max message size
)

type GRPCConfig struct {
	HostPort          string        `yaml:"host_port"`
	Insecure          bool          `yaml:"insecure"`
	BackoffBaseDelay  time.Duration `yaml:"backoff_base_delay"`
	BackoffMaxDelay   time.Duration `yaml:"backoff_max_delay"`
	MinConnectTimeout time.Duration `yaml:"min_connect_timeout"`
	KeepAliveTime     time.Duration `yaml:"keep_alive_time"`
	KeepAliveTimeout  time.Duration `yaml:"keep_alive_timeout"`
}

// ConnectGRPC creates a production-ready gRPC client connection.
//
// Configuration:
// - TLS is enabled by default; set `grpc_config.insecure` to disable.
// - Backoff parameters can be customized under `grpc_config` in YAML.
// - Keepalive is configured to maintain healthy connections under high load.
//
// Production optimizations:
// - Keepalive pings detect dead connections quickly
// - Exponential backoff for connection retries
// - Increased flow control windows for high throughput
// - Large message sizes for batch operations
func ConnectGRPC(config GRPCConfig) (*grpc.ClientConn, error) {
	// Build common dial options for production use
	dialOptions := buildDialOptions(config)

	if config.Insecure {
		dialOptions = append(dialOptions, grpc.WithTransportCredentials(insecure.NewCredentials()))
		return grpc.NewClient(
			config.HostPort,
			dialOptions...,
		)
	}

	// For TLS connections, we still need to use grpc.Dial due to E2E test compatibility.
	// TODO_TECHDEBT: migrate to grpc.NewClient when E2E tests are updated.
	dialOptions = append(dialOptions, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	return grpc.Dial( //nolint:all
		config.HostPort,
		dialOptions...,
	)
}

// buildDialOptions constructs the gRPC dial options for production use.
// These options are applied to both secure and insecure connections.
func buildDialOptions(config GRPCConfig) []grpc.DialOption {
	// Configure exponential backoff for connection retries
	backoffConfig := backoff.Config{
		BaseDelay:  config.BackoffBaseDelay,
		Multiplier: backoff.DefaultConfig.Multiplier, // 1.6
		Jitter:     backoff.DefaultConfig.Jitter,     // 0.2
		MaxDelay:   config.BackoffMaxDelay,
	}

	// Configure keepalive to maintain healthy connections and detect dead ones.
	// This is critical for high-throughput scenarios where connections may be
	// silently dropped by intermediate proxies/load balancers.
	keepaliveParams := keepalive.ClientParameters{
		Time:                config.KeepAliveTime,    // Send pings after this duration of inactivity
		Timeout:             config.KeepAliveTimeout, // Wait this long for ping ack
		PermitWithoutStream: true,                    // Send pings even without active streams
	}

	return []grpc.DialOption{
		// Connection management
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoffConfig,
			MinConnectTimeout: config.MinConnectTimeout,
		}),

		// Keepalive for connection health
		grpc.WithKeepaliveParams(keepaliveParams),

		// Flow control for high throughput
		grpc.WithInitialWindowSize(defaultInitialWindowSize),
		grpc.WithInitialConnWindowSize(defaultInitialConnWindowSize),

		// Message size limits for batch operations
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(defaultMaxRecvMsgSize),
			grpc.MaxCallSendMsgSize(defaultMaxSendMsgSize),
		),
	}
}

func (c *GRPCConfig) HydrateDefaults() GRPCConfig {
	if c.BackoffBaseDelay == 0 {
		c.BackoffBaseDelay = defaultBackoffBaseDelay
	}
	if c.BackoffMaxDelay == 0 {
		c.BackoffMaxDelay = defaultBackoffMaxDelay
	}
	if c.MinConnectTimeout == 0 {
		c.MinConnectTimeout = defaultMinConnectTimeout
	}
	if c.KeepAliveTime == 0 {
		c.KeepAliveTime = defaultKeepAliveTime
	}
	if c.KeepAliveTimeout == 0 {
		c.KeepAliveTimeout = defaultKeepAliveTimeout
	}
	return *c
}
