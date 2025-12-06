package concurrency

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	pathProcess = "path"

	// Metric names
	parallelEndpointsSelectedMetric = "shannon_parallel_endpoints_selected_total"
	parallelEndpointsDistMetric     = "shannon_parallel_endpoints_distribution"
)

var (
	// parallelEndpointsSelected tracks how many endpoints were selected for parallel execution
	parallelEndpointsSelected = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      parallelEndpointsSelectedMetric,
			Help:      "Total number of endpoints selected for parallel execution per request",
		},
		[]string{"service_id", "num_endpoints"},
	)

	// parallelEndpointsDistribution tracks the distribution of parallel endpoint counts
	parallelEndpointsDistribution = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: pathProcess,
			Name:      parallelEndpointsDistMetric,
			Help:      "Distribution of parallel endpoint counts per request",
			Buckets:   []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
		[]string{"service_id"},
	)
)

// RecordParallelEndpoints records metrics when multiple endpoints are selected for parallel execution.
// This helps operators understand:
// - How often parallel execution is being used
// - The distribution of endpoint counts
// - Token burn multiplication (num_endpoints × base cost)
func RecordParallelEndpoints(serviceID string, numEndpoints int) {
	parallelEndpointsSelected.With(prometheus.Labels{
		"service_id":    serviceID,
		"num_endpoints": formatEndpointCount(numEndpoints),
	}).Inc()

	parallelEndpointsDistribution.With(prometheus.Labels{
		"service_id": serviceID,
	}).Observe(float64(numEndpoints))
}

// formatEndpointCount formats the endpoint count for metric labels.
// Groups into ranges for better cardinality control.
func formatEndpointCount(count int) string {
	switch {
	case count == 1:
		return "1"
	case count == 2:
		return "2"
	case count == 3:
		return "3"
	case count >= 4 && count <= 5:
		return "4-5"
	case count >= 6 && count <= 10:
		return "6-10"
	default:
		return "10+"
	}
}
