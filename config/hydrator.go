package config

import (
	"time"

	"github.com/pokt-network/path/protocol"
)

/* --------------------------------- Hydrator Config Defaults -------------------------------- */

var (
	// endpointHydratorRunInterval specifies the run interval of an endpoint hydrator in milliseconds.
	defaultEndpointHydratorRunInterval = 10_000 * time.Millisecond

	// defaultMaxEndpointCheckWorkers specifies the maximum number of workers that will be used to concurrently check endpoints.
	defaultMaxEndpointCheckWorkers = 100
)

/* --------------------------------- Hydrator Config Struct -------------------------------- */

// EndpointHydratorConfig stores configuration settings for running an
// Endpoint Hydrator instance to collect observations about service endpoints.
// The hydrator will not start without specified service IDs.
type EndpointHydratorConfig struct {
	// List of service IDs to disable QoS checks for.
	// By default all configured service IDs will be checked unless specified here.
	// Startup will error if a service ID is specified here that is not in the protocol's configured service IDs.
	// Primarily just used for testing & development.
	QoSDisabledServiceIDs []protocol.ServiceID `yaml:"qos_disabled_service_ids"`

	// Interval between hydrator runs during which endpoint checks are performed
	RunInterval time.Duration `yaml:"run_interval_ms"`

	// Maximum number of concurrent endpoint check workers for performance tuning
	MaxEndpointCheckWorkers int `yaml:"max_endpoint_check_workers"`
}

/* --------------------------------- Hydrator Config Private Helpers -------------------------------- */

// hydrateHydratorDefaults assigns default values to HydratorConfig fields if they are not set.
func (c *EndpointHydratorConfig) hydrateHydratorDefaults() {
	if c.RunInterval == 0 {
		c.RunInterval = defaultEndpointHydratorRunInterval
	}
	if c.MaxEndpointCheckWorkers == 0 {
		c.MaxEndpointCheckWorkers = defaultMaxEndpointCheckWorkers
	}
}
