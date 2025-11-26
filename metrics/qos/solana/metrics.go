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
	requestsTotalMetric = "solana_requests_total"
)

func init() {
	prometheus.MustRegister(requestsTotal)
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
		[]string{"chain_id", "service_id", "request_origin", "request_method", "success", "error_type", "http_status_code", "endpoint_domain"},
	)

	// TODO_TECHDEBT(@adshmh): Add a new metric to export JSONRPC responses error codes:
	// Consistent with Cosmos and EVM metrics.
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

	// Increment request counters with all corresponding labels
	requestsTotal.With(
		prometheus.Labels{
			"chain_id":         interpreter.GetChainID(),
			"service_id":       interpreter.GetServiceID(),
			"request_origin":   observations.GetRequestOrigin().String(),
			"request_method":   interpreter.GetRequestMethod(),
			"success":          fmt.Sprintf("%t", interpreter.IsRequestSuccessful()),
			"error_type":       interpreter.GetRequestErrorType(),
			"http_status_code": fmt.Sprintf("%d", interpreter.GetRequestHTTPStatus()),
			"endpoint_domain":  interpreter.GetEndpointDomain(),
		}).Inc()
}
