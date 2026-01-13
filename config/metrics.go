package config

/* --------------------------------- Metrics Config Defaults -------------------------------- */

const (
	// defaultPrometheusPort is the default port for the Prometheus metrics server
	defaultPrometheusPort = ":9090"

	// defaultPprofPort is the default port for the pprof server
	// NOTE: This address was selected based on the example here:
	// https://pkg.go.dev/net/http/pprof
	defaultPprofPort = ":6060"
)

/* --------------------------------- Metrics Config Struct -------------------------------- */

// MetricsConfig contains configuration for metrics and profiling servers.
type MetricsConfig struct {
	// PrometheusAddr is the address at which the Prometheus metrics server will listen
	// Default: ":9090"
	PrometheusAddr string `yaml:"prometheus_addr"`

	// PprofAddr is the address at which the pprof server will listen
	// Default: ":6060"
	PprofAddr string `yaml:"pprof_addr"`
}

/* --------------------------------- Metrics Config Private Helpers -------------------------------- */

// hydrateMetricsDefaults assigns default values to MetricsConfig fields if they are not set.
func (c *MetricsConfig) hydrateMetricsDefaults() {
	if c.PrometheusAddr == "" {
		c.PrometheusAddr = defaultPrometheusPort
	}
	if c.PprofAddr == "" {
		c.PprofAddr = defaultPprofPort
	}
}
