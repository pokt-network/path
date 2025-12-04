// Package retry provides functionality for exporting retry metrics to Prometheus.
package retry

import (
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// The POSIX process that emits metrics
	pathProcess = "path"

	// Retry metrics
	retriesTotalMetric      = "shannon_retries_total"
	retrySuccessTotalMetric = "shannon_retry_success_total"
	retryLatencyMetric      = "shannon_retry_latency_seconds"
)

func init() {
	prometheus.MustRegister(retriesTotal)
	prometheus.MustRegister(retrySuccessTotal)
	prometheus.MustRegister(retryLatency)
}

var (
	// retriesTotal tracks the total number of retries attempted.
	// Labels:
	//   - service_id: Target service identifier
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//   - retry_reason: Reason for retry (timeout, 5xx, connection_error)
	//   - attempt: Retry attempt number (1, 2, 3, etc.)
	//
	// Use to analyze:
	//   - Retry frequency by service and reason
	//   - Which endpoints trigger the most retries
	//   - Retry patterns over time
	retriesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      retriesTotalMetric,
			Help:      "Total number of retry attempts",
		},
		[]string{"service_id", "endpoint_domain", "retry_reason", "attempt"},
	)

	// retrySuccessTotal tracks successful retries.
	// Labels:
	//   - service_id: Target service identifier
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//   - attempt: Retry attempt number that succeeded (1, 2, 3, etc.)
	//
	// Use to analyze:
	//   - Retry success rates
	//   - How many attempts typically needed to succeed
	//   - Endpoint reliability after initial failure
	retrySuccessTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      retrySuccessTotalMetric,
			Help:      "Total number of successful retries",
		},
		[]string{"service_id", "endpoint_domain", "attempt"},
	)

	// retryLatency tracks the total latency added by retries.
	// Labels:
	//   - service_id: Target service identifier
	//   - success: Whether the final result was successful after retries (true/false)
	//
	// Use to analyze:
	//   - Additional latency introduced by retries
	//   - Whether retries are adding significant delay
	retryLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: pathProcess,
			Name:      retryLatencyMetric,
			Help:      "Total latency added by retry attempts in seconds",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
		},
		[]string{"service_id", "success"},
	)
)

// Retry reason constants for metrics labeling.
const (
	RetryReasonTimeout         = "timeout"
	RetryReason5xx             = "5xx"
	RetryReasonConnectionError = "connection_error"
)

// RecordRetryAttempt records a retry attempt.
func RecordRetryAttempt(serviceID, endpointDomain, reason string, attempt int) {
	retriesTotal.With(prometheus.Labels{
		"service_id":      serviceID,
		"endpoint_domain": endpointDomain,
		"retry_reason":    reason,
		"attempt":         formatAttempt(attempt),
	}).Inc()
}

// RecordRetrySuccess records a successful retry.
func RecordRetrySuccess(serviceID, endpointDomain string, attempt int) {
	retrySuccessTotal.With(prometheus.Labels{
		"service_id":      serviceID,
		"endpoint_domain": endpointDomain,
		"attempt":         formatAttempt(attempt),
	}).Inc()
}

// RecordRetryLatency records the total latency added by retries.
func RecordRetryLatency(serviceID string, success bool, latencySeconds float64) {
	successStr := "false"
	if success {
		successStr = "true"
	}
	retryLatency.With(prometheus.Labels{
		"service_id": serviceID,
		"success":    successStr,
	}).Observe(latencySeconds)
}

// formatAttempt converts attempt number to string.
func formatAttempt(attempt int) string {
	switch attempt {
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	default:
		return "3+"
	}
}
