// Package retry provides endpoint rotation metrics for retry operations.
package retry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// Endpoint rotation metrics
	endpointSwitchTotalMetric     = "shannon_retry_endpoint_switches_total"
	endpointExhaustionTotalMetric = "shannon_retry_endpoint_exhaustion_total"
)

var (
	// endpointSwitchTotal tracks endpoint switches during retry attempts.
	// Labels:
	//   - service_id: Target service identifier
	//   - attempt: Which retry attempt triggered the switch (2, 3, etc.)
	//
	// Use to analyze:
	//   - How often retry endpoint rotation is occurring
	//   - Distribution of switches across retry attempts
	//   - Service-specific retry patterns
	endpointSwitchTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      endpointSwitchTotalMetric,
			Help:      "Total number of endpoint switches during retry attempts",
		},
		[]string{"service_id", "attempt"},
	)

	// endpointExhaustionTotal tracks when all available endpoints have been tried.
	// Labels:
	//   - service_id: Target service identifier
	//   - num_endpoints_available: How many endpoints were available when exhausted
	//
	// Use to analyze:
	//   - How often we cycle through all endpoints
	//   - Whether endpoint pools are too small for reliability
	//   - Services with systematic endpoint failures
	endpointExhaustionTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      endpointExhaustionTotalMetric,
			Help:      "Total times all available endpoints were exhausted during retry",
		},
		[]string{"service_id", "num_endpoints_available"},
	)
)

// RecordEndpointSwitch records when we switch to a new endpoint during a retry attempt.
func RecordEndpointSwitch(serviceID string, attempt int) {
	endpointSwitchTotal.With(prometheus.Labels{
		"service_id": serviceID,
		"attempt":    formatAttempt(attempt),
	}).Inc()
}

// RecordEndpointExhaustion records when all available endpoints have been tried.
func RecordEndpointExhaustion(serviceID string, numEndpointsAvailable int) {
	endpointExhaustionTotal.With(prometheus.Labels{
		"service_id":              serviceID,
		"num_endpoints_available": formatEndpointCount(numEndpointsAvailable),
	}).Inc()
}

// formatEndpointCount formats the endpoint count for metric labels.
// Groups into ranges for better cardinality control.
func formatEndpointCount(count int) string {
	switch {
	case count == 1:
		return "1"
	case count == 2:
		return "2"
	case count >= 3 && count <= 5:
		return "3-5"
	case count >= 6 && count <= 10:
		return "6-10"
	case count >= 11 && count <= 20:
		return "11-20"
	default:
		return "20+"
	}
}
