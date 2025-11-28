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
	if c.SystemOverheadAllowanceDuration >= c.ReadTimeout || c.SystemOverheadAllowanceDuration >= c.WriteTimeout {
		return fmt.Errorf("system overhead allowance duration %v must be less than read timeout %v and write timeout %v", c.SystemOverheadAllowanceDuration, c.ReadTimeout, c.WriteTimeout)
	}
	return nil
}
