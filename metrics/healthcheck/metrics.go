// Package healthcheck provides functionality for exporting health check metrics to Prometheus.
package healthcheck

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// The POSIX process that emits metrics
	pathProcess = "path"

	// Health check metrics
	healthChecksExecutedTotalMetric   = "health_checks_executed_total"
	healthCheckDurationSecondsMetric  = "health_check_duration_seconds"
	healthCheckEndpointsCheckedMetric = "health_check_endpoints_checked"
)

func init() {
	prometheus.MustRegister(healthChecksExecutedTotal)
	prometheus.MustRegister(healthCheckDurationSeconds)
	prometheus.MustRegister(healthCheckEndpointsChecked)
}

var (
	// healthChecksExecutedTotal tracks the total health checks executed.
	// Labels:
	//   - service_id: Target service identifier
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//   - check_name: Name of the health check (e.g., "eth_blockNumber", "getHealth")
	//   - check_type: Type of health check (jsonrpc, rest, websocket, grpc)
	//   - success: Whether the health check passed (true/false)
	//   - error_type: Type of error if failed (empty if success)
	//
	// Use to analyze:
	//   - Health check success rates by service and endpoint
	//   - Which checks are failing most often
	//   - Domain-level health patterns
	healthChecksExecutedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      healthChecksExecutedTotalMetric,
			Help:      "Total number of health checks executed",
		},
		[]string{"service_id", "endpoint_domain", "check_name", "check_type", "success", "error_type"},
	)

	// healthCheckDurationSeconds tracks health check execution duration.
	// Labels:
	//   - service_id: Target service identifier
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//   - check_name: Name of the health check
	//   - check_type: Type of health check (jsonrpc, rest, websocket, grpc)
	//
	// Use to analyze:
	//   - Health check latency patterns
	//   - Slow endpoints by check type
	//   - Performance trends
	healthCheckDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: pathProcess,
			Name:      healthCheckDurationSecondsMetric,
			Help:      "Histogram of health check execution duration in seconds",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
		},
		[]string{"service_id", "endpoint_domain", "check_name", "check_type"},
	)

	// healthCheckEndpointsChecked tracks unique endpoints checked per service.
	// Labels:
	//   - service_id: Target service identifier
	//
	// Use to analyze:
	//   - Coverage of health checks across endpoints
	//   - Endpoint pool size trends
	healthCheckEndpointsChecked = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: pathProcess,
			Name:      healthCheckEndpointsCheckedMetric,
			Help:      "Number of endpoints checked in the last health check cycle",
		},
		[]string{"service_id"},
	)
)

// RecordHealthCheckResult records a health check execution result.
func RecordHealthCheckResult(serviceID, endpointDomain, checkName, checkType string, success bool, errorType string, durationSeconds float64) {
	successStr := "false"
	if success {
		successStr = "true"
	}

	healthChecksExecutedTotal.With(prometheus.Labels{
		"service_id":      serviceID,
		"endpoint_domain": endpointDomain,
		"check_name":      checkName,
		"check_type":      checkType,
		"success":         successStr,
		"error_type":      errorType,
	}).Inc()

	healthCheckDurationSeconds.With(prometheus.Labels{
		"service_id":      serviceID,
		"endpoint_domain": endpointDomain,
		"check_name":      checkName,
		"check_type":      checkType,
	}).Observe(durationSeconds)
}

// SetEndpointsChecked sets the number of endpoints checked for a service.
func SetEndpointsChecked(serviceID string, count int) {
	healthCheckEndpointsChecked.With(prometheus.Labels{
		"service_id": serviceID,
	}).Set(float64(count))
}
