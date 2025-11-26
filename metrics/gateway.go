package metrics

import (
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/pokt-network/path/observation"
)

// See the metrics initialization below for details.
const (
	// The POSIX process that emits metrics
	pathProcess = "path"

	// The list of metrics being tracked for gateway-level observations
	requestsTotalMetricName         = "requests_total" // TODO_TECHDEBT: Align the relays/requests terminology
	parallelRequestsTotalMetricName = "parallel_requests_total"
	responseSizeBytesMetricName     = "response_size_bytes"
	relayDurationSecondsMetricName  = "relay_duration_seconds"
	versionInfoMetricName           = "version_info"
)

func init() {
	prometheus.MustRegister(relaysTotal)
	prometheus.MustRegister(parallelRequestsTotal)
	prometheus.MustRegister(relaysDurationSeconds)
	prometheus.MustRegister(relayResponseSizeBytes)
	prometheus.MustRegister(versionInfo)
}

var (
	// relaysTotal is a counter tracking processed requests per PATH instance.
	// Increment on each service request with labels:
	//   - service_id: Identifies the service
	//   - request_type: "organic" or "synthetic"
	//   - request_error_kind: request error kind, if any.
	//
	// Usage:
	// - Monitor total request load.
	// - Compare requests across services or PATH instances.
	relaysTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      requestsTotalMetricName,
			Help:      "Total number of requests processed, labeled by service ID.",
		},
		[]string{"service_id", "request_type", "request_error_kind"},
	)

	// relaysDurationSeconds measures request processing duration with the service_id label.
	// Histogram buckets from 0.1s to 15s capture performance from fast responses to timeouts.
	//
	// Usage:
	// - Analyze typical response times and long-tail latency issues.
	// - Compare performance across services.
	// - Compare performance under different loads.
	relaysDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: pathProcess,
			Name:      relayDurationSecondsMetricName,
			Help:      "Histogram of request processing time (duration) in seconds",
			// Buckets are selected as: [0, 0.1), [0.1, 0.5), [0.5, 1), [1, 2), [2, 5), [5, 15)
			// This is because the request processing time is expected to be normally distributed.
			// This means we need a higher resolution (smaller buckets and more granularity) for the lower values,
			// and less resolution (big buckets and low granularity) for the higher values because it'll have less
			// data points.
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 15},
		},
		[]string{"service_id"},
	)

	// relayResponseSizeBytes tracks response payload sizes in bytes.
	// Histogram buckets from 100B to 50KB capture size distribution.
	//
	// Usage:
	// 	- Performance tuning to understand skew of data distribution
	//	- Visibility into small & large response size distribution
	relayResponseSizeBytes = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: pathProcess,
			Name:      responseSizeBytesMetricName,
			Help:      "Histogram of response sizes in bytes for performance analysis.",
			// TODO_IMPROVE: Consider configuring bucket sizes externally for flexible adjustments
			// in response to different data patterns or deployment scenarios.
			Buckets: []float64{100, 500, 1_000, 5_000, 10_000, 50_000},
		},
		[]string{"service_id"},
	)

	// TODO_MVP(@adshmh): Add a serviceRequestSize metric once the `request` package is refactored to
	// fully encapsulate the task of dealing with the HTTP request, including:
	//	1. Reading of all HTTP headers: Target-Service-Id, etc.
	//	2. Reading of the HTTP request's body
	//	3. Building an HTTP observation using the extracted data.
	// This will also involve a small refactor on protocol and qos packages to accept a custom struct
	// rather than an HTTP request.

	// versionInfo provides version information about the running PATH instance.
	// This is a gauge metric that is set to 1 with labels containing version details.
	// Labels:
	//   - version: Version string from git describe (e.g., "v1.0.0" or "v1.0.0-dev1")
	//   - commit: Git commit SHA
	//   - build_date: ISO8601 timestamp when the binary was built
	//
	// Use to analyze:
	//   - Which version of PATH is running
	//   - Track deployment rollouts
	//   - Correlate issues with specific builds
	versionInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: pathProcess,
			Name:      versionInfoMetricName,
			Help:      "Version information about the running PATH instance",
		},
		[]string{"version", "commit", "build_date"},
	)

	// parallelRequestsTotal tracks individual parallel requests within a batch.
	// Increment for each parallel request made with labels:
	//   - service_id: Identifies the service
	//   - num_requests: Total number of parallel requests in the batch (1, 2, 3, etc.)
	//   - num_successful: Number of successful parallel requests
	//   - num_failed: Number of failed parallel requests
	//   - num_canceled: Number of canceled parallel requests
	//
	// Usage:
	// - Track how many parallel requests are made per incoming request
	// - Monitor success/failure/cancellation rates within parallel batches
	// - This is ONLY intended for very low cardinality (i.e. multiplicity <= 5)
	parallelRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      parallelRequestsTotalMetricName,
			Help:      "Total parallel requests made, labeled by batch size and outcome.",
		},
		[]string{"service_id", "num_requests", "num_successful", "num_failed", "num_canceled"},
	)
)

// publishGatewayMetrics publishes all metrics related to gateway-level observations.
// Returns:
// - true if the request was valid.
// - false otherwise.
func publishGatewayMetrics(
	logger polylog.Logger,
	gatewayObservations *observation.GatewayObservations,
) bool {
	// Extract the service ID from the gateway observations
	serviceID := gatewayObservations.GetServiceId()

	// Extract the request type from the gateway observations
	requestType := observation.RequestType_name[int32(gatewayObservations.GetRequestType())]

	// Extract the request error kind from the gateway observations
	var requestErrorKind string
	requestErr := gatewayObservations.GetRequestError()
	if requestErr != nil {
		requestErrorKind = requestErr.GetErrorKind().String()
		logger.With(
			"service_id", serviceID,
			"request_type", requestType,
			"request_error_kind", requestErrorKind,
			"request_error_details", requestErr.GetDetails(),
		).Error().Msg("Invalid request: No Protocol or QoS observations were made.")
	}

	// Increment on each service request with labels:
	//   - service_id: Identifies the service
	//   - request_type: "organic" or "synthetic"
	//   - request_error_kind: any gateway-level request errors: e.g. no service ID specified in request's HTTP headers.
	relaysTotal.
		With(prometheus.Labels{
			"service_id":         serviceID,
			"request_type":       requestType,
			"request_error_kind": requestErrorKind,
		}).
		Inc()

	// Publish request duration in seconds
	duration := gatewayObservations.GetCompletedTime().AsTime().Sub(gatewayObservations.GetReceivedTime().AsTime()).Seconds()
	relaysDurationSeconds.
		With(prometheus.Labels{"service_id": serviceID}).
		Observe(duration)

	// Publish response_size in bytes
	relayResponseSizeBytes.
		With(prometheus.Labels{"service_id": serviceID}).
		Observe(float64(gatewayObservations.GetResponseSize()))

	// Record the outcome of parallel requests within a batch.
	// Only record if parallel request observations are available
	if parallelRequestsObs := gatewayObservations.GetGatewayParallelRequestObservations(); parallelRequestsObs != nil {
		parallelRequestsTotal.With(prometheus.Labels{
			"service_id":     serviceID,
			"num_requests":   fmt.Sprintf("%d", parallelRequestsObs.GetNumRequests()),
			"num_successful": fmt.Sprintf("%d", parallelRequestsObs.GetNumSuccessful()),
			"num_failed":     fmt.Sprintf("%d", parallelRequestsObs.GetNumFailed()),
			"num_canceled":   fmt.Sprintf("%d", parallelRequestsObs.GetNumCanceled()),
		}).Inc()
	}

	// Return the validity status of the request.
	return requestErr == nil
}

// SetVersionInfo sets the version information metric with the provided build details.
// This should be called once during application startup.
func SetVersionInfo(version, commit, buildDate string) {
	// Set default values if any are empty
	if version == "" {
		version = "unknown"
	}
	if commit == "" {
		commit = "unknown"
	}
	if buildDate == "" {
		buildDate = "unknown"
	}

	versionInfo.With(prometheus.Labels{
		"version":    version,
		"commit":     commit,
		"build_date": buildDate,
	}).Set(1)
}
