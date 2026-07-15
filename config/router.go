package config

import (
	"fmt"
	"time"
)

/* --------------------------------- Router Config Defaults -------------------------------- */

// TODO_IMPROVE: Make all of these configurable for PATH users
const (
	// default PATH port
	defaultPort = 3069

	// defaultMaxRequestHeaderBytes is the default maximum size of the HTTP request header.
	defaultMaxRequestHeaderBytes = 2 * 1e6 // 2 MB

	// Reserve time for system overhead, i.e. time spent on non-business logic operations.
	// Examples:
	// - Read HTTP Request body
	// - Write HTTP Response
	defaultSystemOverheadAllowanceDuration = 10 * time.Second

	// https://pkg.go.dev/net/http#Server
	// HTTP server's default timeout values.
	defaultHTTPServerReadTimeout  = 60 * time.Second
	defaultHTTPServerWriteTimeout = 120 * time.Second
	defaultHTTPServerIdleTimeout  = 180 * time.Second

	// defaultWebsocketMessageBufferSize is the buffer size for websocket message observations.
	// Reduced from 1000 to prevent OOM. At 100: 100 × ~3KB × 100 connections = ~30MB.
	// Can be tuned based on expected concurrent websocket connections and message frequency.
	defaultWebsocketMessageBufferSize = 100

	// defaultMaxRequestBodyBytes caps the size of an HTTP request body PATH will
	// read into memory. Without a limit, a single request with a multi-GB body
	// OOMs the process (the body is buffered whole, and observation/detection
	// paths read it before any downstream limit applies). 10MB is well above any
	// legitimate JSON-RPC payload — including large batches and raw transactions —
	// while bounding a single request's allocation. Reads past the limit fail
	// (request rejected) rather than growing memory without bound.
	defaultMaxRequestBodyBytes = 10 * 1024 * 1024

	// defaultMaxConcurrentWebsocketConnections caps concurrent live websocket
	// connections per gateway pod (defense-in-depth against goroutine/FD
	// exhaustion). Each connection costs ~5-7 goroutines + 2 sockets + buffers
	// (~50-100KB), so 10000 ≈ 0.5-1GB worst case — a generous ceiling well above
	// normal load that still bounds catastrophic runaway. Tune to observed
	// concurrent websocket load (Polygon is the primary websocket service).
	// Set to a negative value in config to disable the limit entirely.
	defaultMaxConcurrentWebsocketConnections = 10000
)

/* --------------------------------- Router Config Struct -------------------------------- */

// RouterConfig contains server configuration settings.
// See default values above.
type RouterConfig struct {
	Port                            int           `yaml:"port"`
	MaxRequestHeaderBytes           int           `yaml:"max_request_header_bytes"`
	ReadTimeout                     time.Duration `yaml:"read_timeout"`
	WriteTimeout                    time.Duration `yaml:"write_timeout"`
	IdleTimeout                     time.Duration `yaml:"idle_timeout"`
	SystemOverheadAllowanceDuration time.Duration `yaml:"system_overhead_allowance_duration"`
	// WebsocketMessageBufferSize is the buffer size for websocket message observation channels.
	// Larger values use more memory but can handle higher message throughput.
	// Default: 50 (prevents OOM while maintaining reasonable throughput)
	WebsocketMessageBufferSize int `yaml:"websocket_message_buffer_size"`
	// MaxConcurrentWebsocketConnections caps the number of concurrent live
	// websocket connections per gateway pod. Default: 10000. A negative value
	// disables the limit.
	MaxConcurrentWebsocketConnections int `yaml:"max_concurrent_websocket_connections"`
	// MaxRequestBodyBytes caps the size (in bytes) of an HTTP request body PATH
	// will read into memory. Default: 10MB. A negative value disables the limit.
	MaxRequestBodyBytes int64 `yaml:"max_request_body_bytes"`
}

/* --------------------------------- Router Config Private Helpers -------------------------------- */

// hydrateRouterDefaults assigns default values to RouterConfig fields if they are not set.
// Returns an error if the configuration is invalid.
func (c *RouterConfig) hydrateRouterDefaults() error {
	if c.Port == 0 {
		c.Port = defaultPort
	}
	if c.MaxRequestHeaderBytes == 0 {
		c.MaxRequestHeaderBytes = defaultMaxRequestHeaderBytes
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = defaultHTTPServerReadTimeout
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = defaultHTTPServerWriteTimeout
	}
	if c.IdleTimeout == 0 {
		c.IdleTimeout = defaultHTTPServerIdleTimeout
	}
	if c.SystemOverheadAllowanceDuration == 0 {
		c.SystemOverheadAllowanceDuration = defaultSystemOverheadAllowanceDuration
	}
	if c.WebsocketMessageBufferSize == 0 {
		c.WebsocketMessageBufferSize = defaultWebsocketMessageBufferSize
	}
	// Only an unset (zero) value takes the default; a negative value is preserved
	// so operators can explicitly disable the limit.
	if c.MaxConcurrentWebsocketConnections == 0 {
		c.MaxConcurrentWebsocketConnections = defaultMaxConcurrentWebsocketConnections
	}
	if c.MaxRequestBodyBytes == 0 {
		c.MaxRequestBodyBytes = defaultMaxRequestBodyBytes
	}
	if c.SystemOverheadAllowanceDuration >= c.ReadTimeout || c.SystemOverheadAllowanceDuration >= c.WriteTimeout {
		return fmt.Errorf("system overhead allowance duration %v must be less than read timeout %v and write timeout %v", c.SystemOverheadAllowanceDuration, c.ReadTimeout, c.WriteTimeout)
	}
	return nil
}
