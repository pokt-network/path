package metrics

import (
	"strconv"

	"github.com/pokt-network/poktroll/pkg/polylog"

	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/observation"
	protocolobs "github.com/pokt-network/path/observation/protocol"
	qosobs "github.com/pokt-network/path/observation/qos"
)

// PrometheusMetricsReporter provides the functionality required for exporting PATH metrics to Grafana.
type PrometheusMetricsReporter struct {
	Logger polylog.Logger
}

// Publish exports service request and response metrics to Prometheus/Grafana
// Implements the gateway.RequestResponseReporter interface.
// Records metrics as defined in how_metrics_should_work.md
func (pmr *PrometheusMetricsReporter) Publish(observations *observation.RequestResponseObservations) {
	if observations == nil {
		return
	}

	serviceID := observations.GetServiceId()
	if serviceID == "" {
		return
	}

	qosObs := observations.GetQos()

	// Process protocol observations (Shannon)
	pmr.publishProtocolMetrics(serviceID, observations.GetProtocol(), qosObs)

	// Process QoS observations
	pmr.publishQoSMetrics(serviceID, qosObs)
}

// publishProtocolMetrics processes Shannon protocol observations
func (pmr *PrometheusMetricsReporter) publishProtocolMetrics(serviceID string, protocolObs *protocolobs.Observations, qosObs *qosobs.Observations) {
	if protocolObs == nil {
		return
	}

	shannonObs := protocolObs.GetShannon()
	if shannonObs == nil {
		return
	}

	// Process each request observation
	for _, reqObs := range shannonObs.GetObservations() {
		pmr.processRequestObservation(serviceID, reqObs, qosObs)
	}
}

// processRequestObservation records metrics for a single request observation
func (pmr *PrometheusMetricsReporter) processRequestObservation(serviceID string, reqObs *protocolobs.ShannonRequestObservations, qosObs *qosobs.Observations) {
	if reqObs == nil {
		return
	}

	// Process HTTP observations
	httpObs := reqObs.GetHttpObservations()
	if httpObs != nil {
		for i, endpointObs := range httpObs.GetEndpointObservations() {
			pmr.processEndpointObservation(serviceID, endpointObs, i, qosObs)
		}
	}
}

// processEndpointObservation records metrics for a single endpoint observation
func (pmr *PrometheusMetricsReporter) processEndpointObservation(serviceID string, endpointObs *protocolobs.ShannonEndpointObservation, attemptIndex int, qosObs *qosobs.Observations) {
	if endpointObs == nil {
		return
	}

	endpointURL := endpointObs.GetEndpointUrl()
	domain, err := shannonmetrics.ExtractDomainOrHost(endpointURL)
	if err != nil {
		domain = shannonmetrics.ErrDomain
	}

	// Calculate latency from timestamps
	var latencyMs float64
	queryTime := endpointObs.GetEndpointQueryTimestamp()
	responseTime := endpointObs.GetEndpointResponseTimestamp()
	if queryTime != nil && responseTime != nil {
		latencyMs = float64(responseTime.AsTime().Sub(queryTime.AsTime()).Milliseconds())
	}

	// Get status code (returns 0 if not set, treat as 200 for success)
	statusCode := int(endpointObs.GetEndpointBackendServiceHttpResponseStatusCode())
	if statusCode == 0 {
		statusCode = 200 // Default success
	}

	// Determine RPC type from QoS observations
	rpcType := pmr.getRPCTypeFromQoS(qosObs)

	// Metric 5: Request count and latency
	statusCodeStr := GetStatusCodeCategory(statusCode)
	latencySeconds := latencyMs / 1000.0
	RecordRequest(domain, rpcType, serviceID, statusCodeStr, latencySeconds)

	// Metric 4: Latency reputation
	latencySignal := GetLatencySignal(latencyMs)
	RecordLatencyReputation(domain, rpcType, serviceID, latencySignal)

	// Check if error was explicitly set (nil means no error, not UNSPECIFIED)
	// This is important because UNSPECIFIED when explicitly set means "unknown error",
	// while nil means "success with no error"
	hasError := endpointObs.ErrorType != nil
	errorType := endpointObs.GetErrorType()

	// Metric 3: Observation pipeline - determine signal from error type
	reputationSignal := pmr.getReputationSignalFromEndpoint(hasError, errorType, latencyMs)
	networkType := pmr.getNetworkType(serviceID, qosObs)
	method := pmr.getMethodFromQoS(qosObs)
	RecordObservation(domain, rpcType, serviceID, networkType, method, reputationSignal)

	// Metric 9: Request/Response sizes
	responseSize := endpointObs.GetEndpointBackendServiceHttpResponsePayloadSize()
	if responseSize > 0 {
		RecordRequestSize(rpcType, serviceID, 0, responseSize)
	}

	// Check if this was a retry (attemptIndex > 0 means retry)
	isRetry := attemptIndex > 0

	// Metric 6: Retries distribution (if this was a retry)
	if isRetry && hasError {
		retryReason := pmr.getRetryReason(errorType)
		RecordRetryDistribution(domain, rpcType, serviceID, retryReason)
	}

	// Metric 7: Retry results (if this was a retry attempt)
	if isRetry {
		result := RetryResultSuccess
		if hasError {
			result = RetryResultFailure
		}
		RecordRetryResult(rpcType, serviceID, strconv.Itoa(attemptIndex), result, latencySeconds)
	}
}

// getReputationSignalFromEndpoint determines the reputation signal from error type and latency.
// hasError indicates whether the error_type field was explicitly set (nil check).
// This is important because UNSPECIFIED when set means "unknown error", not "success".
func (pmr *PrometheusMetricsReporter) getReputationSignalFromEndpoint(hasError bool, errorType protocolobs.ShannonEndpointErrorType, latencyMs float64) string {
	// No error field set = success, check latency for slow signals
	if !hasError {
		if latencyMs > 3000 {
			return SignalSlowASF
		} else if latencyMs > 1000 {
			return SignalSlow
		}
		return SignalOK
	}

	// Error field is set - check the specific error type
	switch errorType {
	case protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED:
		// Error field was set but type is unknown - treat as major error
		return SignalMajorError

	case protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_TIMEOUT,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_IO_TIMEOUT,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONTEXT_DEADLINE_EXCEEDED:
		return SignalMajorError

	case protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_REFUSED,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_RESET,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NO_ROUTE_TO_HOST,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NETWORK_UNREACHABLE:
		return SignalCriticalError

	case protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_CONFIG:
		// Config errors are fatal - they indicate misconfiguration that won't self-heal
		return SignalFatalError

	case protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX:
		return SignalCriticalError

	case protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_4XX:
		return SignalMinorError

	default:
		return SignalMajorError
	}
}

// getRetryReason determines the retry reason from error type
func (pmr *PrometheusMetricsReporter) getRetryReason(errorType protocolobs.ShannonEndpointErrorType) string {
	switch errorType {
	case protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_TIMEOUT,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_IO_TIMEOUT,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONTEXT_DEADLINE_EXCEEDED:
		return RetryReasonTimeout

	case protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_REFUSED,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_RESET,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NO_ROUTE_TO_HOST,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NETWORK_UNREACHABLE,
		protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_CONFIG:
		return RetryReasonConnection

	case protocolobs.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX:
		return RetryReason5xx

	default:
		return RetryReasonConnection
	}
}

// getNetworkType determines the network type from QoS observations and service ID
func (pmr *PrometheusMetricsReporter) getNetworkType(serviceID string, qosObs *qosobs.Observations) string {
	// If no QoS observations, this is a passthrough (NoOp QoS)
	if qosObs == nil {
		return NetworkTypePassthrough
	}

	// Determine network type from QoS observations
	if qosObs.GetEvm() != nil {
		return NetworkTypeEVM
	}
	if qosObs.GetCosmos() != nil {
		return NetworkTypeCosmos
	}
	if qosObs.GetSolana() != nil {
		return NetworkTypeSolana
	}

	// If QoS observations exist but no specific type, it's passthrough
	return NetworkTypePassthrough
}

// getMethodFromQoS extracts the request method from QoS observations.
// Returns the first method found (e.g., "eth_blockNumber", "status", "getHealth").
// Returns "unknown" if no method can be extracted.
func (pmr *PrometheusMetricsReporter) getMethodFromQoS(qosObs *qosobs.Observations) string {
	if qosObs == nil {
		return "unknown"
	}

	// Try EVM observations
	if evmObs := qosObs.GetEvm(); evmObs != nil {
		interpreter := &qosobs.EVMObservationInterpreter{Observations: evmObs}
		if methods, ok := interpreter.GetRequestMethods(); ok && len(methods) > 0 {
			return methods[0]
		}
	}

	// Try Cosmos observations
	if cosmosObs := qosObs.GetCosmos(); cosmosObs != nil {
		interpreter := &qosobs.CosmosSDKObservationInterpreter{Observations: cosmosObs}
		if methods, ok := interpreter.GetRequestMethods(); ok && len(methods) > 0 {
			return methods[0]
		}
	}

	// Try Solana observations
	if solanaObs := qosObs.GetSolana(); solanaObs != nil {
		interpreter := &qosobs.SolanaObservationInterpreter{Observations: solanaObs}
		if methods, ok := interpreter.GetRequestMethods(); ok && len(methods) > 0 {
			return methods[0]
		}
	}

	return "unknown"
}

// getRPCTypeFromQoS determines RPC type from QoS observations
func (pmr *PrometheusMetricsReporter) getRPCTypeFromQoS(qosObs *qosobs.Observations) string {
	if qosObs == nil {
		return "json_rpc" // Default
	}

	// EVM is always JSON-RPC
	if qosObs.GetEvm() != nil {
		return "json_rpc"
	}

	// Cosmos: Check BackendServiceType from request profiles
	if cosmosObs := qosObs.GetCosmos(); cosmosObs != nil {
		profiles := cosmosObs.GetRequestProfiles()
		if len(profiles) > 0 {
			backendDetails := profiles[0].GetBackendServiceDetails()
			if backendDetails != nil {
				switch backendDetails.GetBackendServiceType() {
				case qosobs.BackendServiceType_BACKEND_SERVICE_TYPE_JSONRPC:
					return "json_rpc"
				case qosobs.BackendServiceType_BACKEND_SERVICE_TYPE_REST:
					return "rest"
				case qosobs.BackendServiceType_BACKEND_SERVICE_TYPE_COMETBFT:
					return "comet_bft"
				}
			}
		}
		return "json_rpc" // Default for Cosmos
	}

	// Solana is always JSON-RPC
	if qosObs.GetSolana() != nil {
		return "json_rpc"
	}

	return "json_rpc" // Default
}

// publishQoSMetrics processes QoS observations (EVM, Cosmos, Solana)
func (pmr *PrometheusMetricsReporter) publishQoSMetrics(serviceID string, qosObs *qosobs.Observations) {
	if qosObs == nil {
		return
	}

	// Process EVM observations
	if evmObs := qosObs.GetEvm(); evmObs != nil {
		pmr.publishEVMMetrics(serviceID, evmObs)
	}

	// Process Cosmos observations
	if cosmosObs := qosObs.GetCosmos(); cosmosObs != nil {
		pmr.publishCosmosMetrics(serviceID, cosmosObs)
	}

	// Process Solana observations
	if solanaObs := qosObs.GetSolana(); solanaObs != nil {
		pmr.publishSolanaMetrics(serviceID, solanaObs)
	}
}

// publishEVMMetrics records EVM-specific metrics
func (pmr *PrometheusMetricsReporter) publishEVMMetrics(serviceID string, evmObs *qosobs.EVMRequestObservations) {
	// Metric 8: Batch size (if batch request)
	// Check if there are multiple request observations (batch request)
	requestCount := len(evmObs.GetRequestObservations())
	if requestCount > 1 {
		RecordBatchSize("jsonrpc", serviceID, strconv.Itoa(requestCount), 0)
	}
}

// publishCosmosMetrics records Cosmos-specific metrics
func (pmr *PrometheusMetricsReporter) publishCosmosMetrics(serviceID string, cosmosObs *qosobs.CosmosRequestObservations) {
	// Cosmos observations processing - add batch tracking if applicable
}

// publishSolanaMetrics records Solana-specific metrics
func (pmr *PrometheusMetricsReporter) publishSolanaMetrics(serviceID string, solanaObs *qosobs.SolanaRequestObservations) {
	// Solana observations processing - add batch tracking if applicable
}
