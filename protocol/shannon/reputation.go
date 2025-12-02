package shannon

import (
	"context"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	reputationmetrics "github.com/pokt-network/path/metrics/reputation"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// mapErrorToSignal maps a Shannon endpoint error type and sanction type to a reputation signal.
// This bridges the existing error classification system with the new reputation system.
func mapErrorToSignal(
	errorType protocolobservations.ShannonEndpointErrorType,
	sanctionType protocolobservations.ShannonSanctionType,
	latency time.Duration,
) reputation.Signal {
	// Map based on sanction type first (severity-based grouping)
	switch sanctionType {
	case protocolobservations.ShannonSanctionType_SHANNON_SANCTION_PERMANENT:
		// Permanent sanctions map to fatal errors (service misconfiguration, etc.)
		return reputation.NewFatalErrorSignal(errorType.String())

	case protocolobservations.ShannonSanctionType_SHANNON_SANCTION_SESSION:
		// Session sanctions - further classify by error type
		return mapSessionSanctionError(errorType, latency)

	case protocolobservations.ShannonSanctionType_SHANNON_SANCTION_DO_NOT_SANCTION:
		// These errors are not sanctioned but still should affect reputation
		return mapNonSanctionedError(errorType, latency)

	default:
		// Unknown sanction type - treat as minor error
		return reputation.NewMinorErrorSignal(errorType.String())
	}
}

// mapSessionSanctionError maps session-level sanction errors to reputation signals.
// Session sanctions are typically for recoverable issues like timeouts or connection problems.
func mapSessionSanctionError(
	errorType protocolobservations.ShannonEndpointErrorType,
	latency time.Duration,
) reputation.Signal {
	switch errorType {
	// Timeout errors - Major (connection issues)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_IO_TIMEOUT,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONTEXT_DEADLINE_EXCEEDED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_TIMEOUT:
		return reputation.NewMajorErrorSignal("timeout", latency)

	// Connection errors - Major
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_REFUSED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_RESET,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NO_ROUTE_TO_HOST,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NETWORK_UNREACHABLE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BROKEN_PIPE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_CONNECTION_FAILED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TCP_CONNECTION,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_DNS_RESOLUTION,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TLS_HANDSHAKE:
		return reputation.NewMajorErrorSignal("connection_error", latency)

	// HTTP 5xx and service errors - Critical
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NON_2XX_STATUS,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BAD_RESPONSE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SUPPLIERS_NOT_REACHABLE:
		return reputation.NewCriticalErrorSignal("service_error", latency)

	// Validation/Signature errors - Critical (potential malicious behavior)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_VALIDATION_ERR,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_SIGNATURE_VALIDATION_ERR,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_GET_PUBKEY_ERR,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_NIL_SUPPLIER_PUBKEY,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_PAYLOAD_UNMARSHAL_ERR:
		return reputation.NewCriticalErrorSignal("validation_error", latency)

	// Protocol/Transport errors - Major
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_TRANSPORT_ERROR,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_INVALID_STATUS,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_WIRE_TYPE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_RELAY_REQUEST,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNEXPECTED_EOF,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_HTTP_TRANSPORT:
		return reputation.NewMajorErrorSignal("transport_error", latency)

	// Configuration errors - Critical
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_CONFIG:
		return reputation.NewCriticalErrorSignal("config_error", latency)

	default:
		// Unknown session sanction error - treat as major
		return reputation.NewMajorErrorSignal(errorType.String(), latency)
	}
}

// mapNonSanctionedError maps errors that don't warrant sanctions to reputation signals.
// These are typically client-side or non-actionable errors.
func mapNonSanctionedError(
	errorType protocolobservations.ShannonEndpointErrorType,
	latency time.Duration,
) reputation.Signal {
	switch errorType {
	// Request canceled by PATH (not endpoint's fault)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_REQUEST_CANCELED_BY_PATH:
		// Don't penalize endpoint for PATH-side cancellation
		// Return a neutral signal (success with zero latency won't affect much)
		return reputation.NewSuccessSignal(0)

	// RelayMiner HTTP 4xx (client error, not endpoint's fault)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_4XX:
		return reputation.NewMinorErrorSignal("client_error")

	// Websocket validation failures (could be transient)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_REQUEST_SIGNING_FAILED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_RELAY_RESPONSE_VALIDATION_FAILED:
		return reputation.NewMinorErrorSignal("websocket_validation")

	// Response size exceeded (could be legitimate large response)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RESPONSE_SIZE_EXCEEDED:
		return reputation.NewMinorErrorSignal("response_size_exceeded")

	// Server closed idle connection (normal behavior)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVER_CLOSED_CONNECTION:
		return reputation.NewMinorErrorSignal("connection_closed")

	// Unknown HTTP error
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_UNKNOWN:
		return reputation.NewMinorErrorSignal("unknown_http_error")

	// Unknown payload error
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNKNOWN:
		return reputation.NewMinorErrorSignal("unknown_payload_error")

	default:
		// Default for non-sanctioned errors - treat as minor
		return reputation.NewMinorErrorSignal(errorType.String())
	}
}

// filterByReputation filters endpoints based on their reputation score.
// Returns only endpoints with scores above the configured minimum threshold.
// Endpoints without a score (new endpoints) are assumed to have the initial score.
func (p *Protocol) filterByReputation(
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpoints map[protocol.EndpointAddr]endpoint,
	logger polylog.Logger,
) map[protocol.EndpointAddr]endpoint {
	if p.reputationService == nil {
		return endpoints
	}

	// Build endpoint keys for batch lookup
	keys := make([]reputation.EndpointKey, 0, len(endpoints))
	for addr := range endpoints {
		keys = append(keys, reputation.NewEndpointKey(serviceID, addr))
	}

	// Get scores for all endpoints in a single call
	scores, err := p.reputationService.GetScores(ctx, keys)
	if err != nil {
		logger.Warn().Err(err).Msg("Failed to get reputation scores, allowing all endpoints")
		reputationmetrics.RecordError("get_scores", "storage_error")
		return endpoints
	}

	// Filter endpoints below threshold
	filtered := make(map[protocol.EndpointAddr]endpoint, len(endpoints))
	for addr, ep := range endpoints {
		key := reputation.NewEndpointKey(serviceID, addr)
		score, exists := scores[key]

		// Extract domain for metrics
		endpointDomain := extractEndpointDomain(ep.PublicURL(), logger)

		// If score doesn't exist, the endpoint is new and gets initial score (which is above threshold)
		if !exists {
			filtered[addr] = ep
			reputationmetrics.RecordEndpointAllowed(string(serviceID), endpointDomain)
			continue
		}

		// Record score observation for histogram
		reputationmetrics.RecordScoreObservation(string(serviceID), score.Value)

		// Check if score is above threshold (using FilterByScore's logic inline for efficiency)
		// The threshold is stored in the config, but we use MinThreshold from reputation package defaults
		// since the service handles the comparison internally
		if score.Value >= reputation.DefaultMinThreshold {
			filtered[addr] = ep
			reputationmetrics.RecordEndpointAllowed(string(serviceID), endpointDomain)
		} else {
			logger.Debug().
				Str("endpoint", string(addr)).
				Float64("score", score.Value).
				Float64("threshold", reputation.DefaultMinThreshold).
				Msg("Filtering out low-reputation endpoint")
			reputationmetrics.RecordEndpointFiltered(string(serviceID), endpointDomain)
		}
	}

	return filtered
}

// extractEndpointDomain extracts the domain from an endpoint URL for metrics labeling.
func extractEndpointDomain(url string, logger polylog.Logger) string {
	domain, err := shannonmetrics.ExtractDomainOrHost(url)
	if err != nil {
		logger.Debug().Err(err).Str("url", url).Msg("Could not extract domain from endpoint URL")
		return shannonmetrics.ErrDomain
	}
	return domain
}
