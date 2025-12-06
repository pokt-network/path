// Package retry provides functionality for exporting retry metrics to Prometheus.
package retry

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// The POSIX process that emits metrics
	pathProcess = "path"

	// Retry metrics
	retriesTotalMetric            = "shannon_retries_total"
	retrySuccessTotalMetric       = "shannon_retry_success_total"
	retryLatencyMetric            = "shannon_retry_latency_seconds"
	retryBudgetSkippedTotalMetric = "shannon_retry_budget_skipped_total"
	statusCodeZeroTotalMetric     = "shannon_status_code_zero_total"
)

func init() {
	prometheus.MustRegister(retriesTotal)
	prometheus.MustRegister(retrySuccessTotal)
	prometheus.MustRegister(retryLatency)
	prometheus.MustRegister(retryBudgetSkippedTotal)
	prometheus.MustRegister(statusCodeZeroTotal)
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
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//   - success: Whether the final result was successful after retries (true/false)
	//
	// Use to analyze:
	//   - Additional latency introduced by retries
	//   - Whether retries are adding significant delay
	//   - Which endpoints cause the most retry latency
	retryLatency = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: pathProcess,
			Name:      retryLatencyMetric,
			Help:      "Total latency added by retry attempts in seconds",
			Buckets:   []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
		},
		[]string{"service_id", "endpoint_domain", "success"},
	)

	// retryBudgetSkippedTotal tracks retries skipped due to MaxRetryLatency budget exceeded.
	// Labels:
	//   - service_id: Target service identifier
	//   - endpoint_domain: Effective TLD+1 domain extracted from endpoint URL
	//   - retry_reason: Reason why retry would have been attempted (timeout, 5xx, connection_error)
	//
	// Use to analyze:
	//   - How often slow requests prevent retries
	//   - Whether MaxRetryLatency budget is set appropriately
	//   - Endpoints that consistently exceed the retry time budget
	//   - Which error types are most affected by budget constraints
	retryBudgetSkippedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      retryBudgetSkippedTotalMetric,
			Help:      "Total retries skipped due to MaxRetryLatency budget exceeded",
		},
		[]string{"service_id", "endpoint_domain", "retry_reason"},
	)

	// statusCodeZeroTotal tracks requests with status code 0 that are treated as successful.
	// Labels:
	//   - service_id: Target service identifier
	//   - has_error: Whether the request had an error (for correlation analysis)
	//
	// Use to investigate:
	//   - Whether status code 0 is a protocol-level success indicator
	//   - Frequency of status code 0 responses
	//   - Correlation with error states
	statusCodeZeroTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      statusCodeZeroTotalMetric,
			Help:      "Total requests with status code 0 treated as successful",
		},
		[]string{"service_id", "has_error"},
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
func RecordRetryLatency(serviceID, endpointDomain string, success bool, latencySeconds float64) {
	successStr := "false"
	if success {
		successStr = "true"
	}
	retryLatency.With(prometheus.Labels{
		"service_id":      serviceID,
		"endpoint_domain": endpointDomain,
		"success":         successStr,
	}).Observe(latencySeconds)
}

// RecordRetryBudgetSkipped records when a retry is skipped due to MaxRetryLatency budget exceeded.
func RecordRetryBudgetSkipped(serviceID, endpointDomain, retryReason string) {
	retryBudgetSkippedTotal.With(prometheus.Labels{
		"service_id":      serviceID,
		"endpoint_domain": endpointDomain,
		"retry_reason":    retryReason,
	}).Inc()
}

// RecordStatusCodeZero records when a request with status code 0 is treated as successful.
// This helps investigate whether status code 0 is an expected protocol-level success indicator.
func RecordStatusCodeZero(serviceID string, hasError bool) {
	statusCodeZeroTotal.With(prometheus.Labels{
		"service_id": serviceID,
		"has_error":  fmt.Sprintf("%t", hasError),
	}).Inc()
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
