// Package config provides unified service configuration type aliases.
//
// The actual types are defined in the gateway package to avoid import cycles.
// This file provides type aliases for backward compatibility and convenience.
package config

import (
	"github.com/pokt-network/path/gateway"
)

// Type aliases for unified service configuration.
// The actual types are in the gateway package to avoid import cycles.
type (
	// ServiceType defines the QoS type for a service.
	ServiceType = gateway.ServiceType

	// LatencyProfileConfig defines latency thresholds for a category of services.
	LatencyProfileConfig = gateway.LatencyProfileConfig

	// ServiceReputationConfig holds per-service reputation configuration.
	ServiceReputationConfig = gateway.ServiceReputationConfig

	// ServiceLatencyConfig holds per-service latency configuration.
	ServiceLatencyConfig = gateway.ServiceLatencyConfig

	// ServiceTieredSelectionConfig holds per-service tiered selection configuration.
	ServiceTieredSelectionConfig = gateway.ServiceTieredSelectionConfig

	// ServiceProbationConfig holds per-service probation configuration.
	ServiceProbationConfig = gateway.ServiceProbationConfig

	// ServiceRetryConfig holds per-service retry configuration.
	ServiceRetryConfig = gateway.ServiceRetryConfig

	// ServiceObservationConfig holds per-service observation pipeline configuration.
	ServiceObservationConfig = gateway.ServiceObservationConfig

	// ServiceHealthCheckOverride holds per-service health check configuration overrides.
	ServiceHealthCheckOverride = gateway.ServiceHealthCheckOverride

	// ServiceFallbackConfig holds per-service fallback endpoint configuration.
	ServiceFallbackConfig = gateway.ServiceFallbackConfig

	// ServiceDefaults contains default settings inherited by all services.
	ServiceDefaults = gateway.ServiceDefaults

	// ServiceConfig defines configuration for a single service.
	ServiceConfig = gateway.ServiceConfig

	// UnifiedServicesConfig is the top-level configuration for the unified service system.
	UnifiedServicesConfig = gateway.UnifiedServicesConfig
)

// ServiceType constants re-exported from gateway package.
const (
	ServiceTypeEVM         = gateway.ServiceTypeEVM
	ServiceTypeSolana      = gateway.ServiceTypeSolana
	ServiceTypeCosmos      = gateway.ServiceTypeCosmos
	ServiceTypeGeneric     = gateway.ServiceTypeGeneric
	ServiceTypePassthrough = gateway.ServiceTypePassthrough
)
