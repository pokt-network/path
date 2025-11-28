package solana

import (
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/pokt-network/path/observation/qos"
)

const (
	// The POSIX process that emits metrics
	pathProcess = "path"

	// The list of metrics being tracked for Solana QoS
	requestsTotalMetric    = "solana_requests_total"
	batchRequestSizeMetric = "solana_batch_request_size"
)

func init() {
	prometheus.MustRegister(requestsTotal)
	prometheus.MustRegister(batchRequestSize)
}

var (
	// TODO_MVP(@adshmh):
	// - Add 'errorSubType' label for more granular error categorization
	// - Use 'errorType' for broad error categories (e.g., request validation, protocol error)
	// - Use 'errorSubType' for specifics (e.g., endpoint maxed out, timed out)
	// - Remove 'success' label (success = absence of errorType)
	// - Update EVM observations proto files and add interpreter support
	//
	// TODO_MVP(@adshmh):
	// - Track endpoint responses separately from requests if/when retries are implemented
	//   (A single request may generate multiple responses due to retries)
	//
	// requestsTotal tracks total Solana requests processed
	//
	// - Labels:
	//   - chain_id: Target Solana chain identifier
	//   - service_id: Service ID of the Solana QoS instance
	//   - request_origin: origin of the request: User or Hydrator.
	//   - request_method: JSON-RPC method name
	//   - success: Whether a valid response was received
	//   - error_type: Type of error if request failed (empty for success)
	//   - http_status_code: HTTP status code returned to user
	//   - endpoint_domain: Effective TLD+1 domain of the endpoint that served the request
	//
	// - Use cases:
	//   - Analyze request volume by chain and method
	//   - Track success rates across PATH deployment regions
	//   - Identify method usage patterns per chain
	//   - Measure end-to-end request success rates
	//   - Review error types by method and chain
	//   - Examine HTTP status code distribution
	//   - Performance and reliability by endpoint domain
	requestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: pathProcess,
			Name:      requestsTotalMetric,
			Help:      "Total number of requests processed by Solana QoS instance(s)",
		},
		[]string{"chain_id", "service_id", "request_origin", "request_method", "is_batch_request", "success", "error_type", "http_status_code", "endpoint_domain"},
	)

	// TODO_TECHDEBT(@adshmh): Add a new metric to export JSONRPC responses error codes:
	// Consistent with Cosmos and EVM metrics.

	// batchRequestSize tracks the distribution of batch request sizes.
	// Only recorded for batch requests (requests with more than one JSON-RPC method).
	//
	// Labels:
	//   - chain_id: Target Solana chain identifier
	//   - service_id: Service ID of the Solana QoS instance
	//
	// Use to analyze:
	//   - Batch request size patterns
	//   - Average batch sizes per chain/service
	//   - Capacity planning based on batch request patterns
	batchRequestSize = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: pathProcess,
			Name:      batchRequestSizeMetric,
			Help:      "Distribution of batch request sizes (number of JSON-RPC methods per batch)",
			Buckets:   []float64{1, 2, 5, 10, 25, 50, 100},
		},
		[]string{"chain_id", "service_id"},
	)
)

// PublishMetrics:
// - Exports all Solana-related Prometheus metrics using observations from Solana QoS service
// - Logs errors for unexpected (should-never-happen) conditions
func PublishMetrics(logger polylog.Logger, observations *qos.SolanaRequestObservations) {
	logger = logger.With("method", "PublishMetricsSolana")

	// Skip if observations is nil.
	// This should never happen as PublishQoSMetrics uses nil checks to identify which QoS service produced the observations.
	if observations == nil {
		logger.ProbabilisticDebugInfo(polylog.ProbabilisticDebugInfoProb).Msg("SHOULD RARELY HAPPEN: Unable to publish Solana metrics: received nil observations.")
		return
	}

	// Create an interpreter for the observations
	interpreter := &qos.SolanaObservationInterpreter{
		Logger:       logger,
		Observations: observations,
	}

	// Extract request methods
	methods := extractRequestMethods(logger, interpreter)

	// Record if this is a batch request (more than one method).
	isBatchRequest := len(methods) > 1

	// Record batch size for batch requests
	if isBatchRequest {
		batchRequestSize.With(
			prometheus.Labels{
				"chain_id":   interpreter.GetChainID(),
				"service_id": interpreter.GetServiceID(),
			}).Observe(float64(len(methods)))
	}

	// Count each method as a separate request.
	// This is required for batch requests.
	for _, method := range methods {
		// Increment request counters with all corresponding labels
		requestsTotal.With(
			prometheus.Labels{
				"chain_id":         interpreter.GetChainID(),
				"service_id":       interpreter.GetServiceID(),
				"request_origin":   observations.GetRequestOrigin().String(),
				"request_method":   method,
				"is_batch_request": fmt.Sprintf("%t", isBatchRequest),
				"success":          fmt.Sprintf("%t", interpreter.IsRequestSuccessful()),
				"error_type":       interpreter.GetRequestErrorType(),
				"http_status_code": fmt.Sprintf("%d", interpreter.GetRequestHTTPStatus()),
				"endpoint_domain":  interpreter.GetEndpointDomain(),
			}).Inc()
	}
}

// extractRequestMethods extracts the request methods from the interpreter.
// Returns empty slice if methods cannot be determined.
func extractRequestMethods(logger polylog.Logger, interpreter *qos.SolanaObservationInterpreter) []string {
	methods, methodsFound := interpreter.GetRequestMethods()
	if !methodsFound {
		// For clarity in metrics, use empty slice as the default value when method can't be determined
		methods = []string{}
		// This can happen for invalid requests, but we should still log it
		logger.Debug().Msgf("Should happen very rarely: Unable to determine request method for Solana metrics: %+v", interpreter)
	}
	return methods
}
