package shannon

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sdk "github.com/pokt-network/shannon-sdk"

	"github.com/pokt-network/path/metrics"
	pathhttp "github.com/pokt-network/path/network/http"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
	"github.com/pokt-network/path/qos/heuristic"
	"github.com/pokt-network/path/reputation"
)

// classifyErrorAsSignal classifies a relay error and returns the appropriate reputation signal.
// This replaces the old two-step classification (error → sanction → signal) with direct mapping.
//
// See ERROR_CLASSIFICATION.md for detailed documentation of all error categories.
func classifyErrorAsSignal(logger polylog.Logger, err error, latency time.Duration) (protocolobservations.ShannonEndpointErrorType, reputation.Signal) {
	// No error: return unspecified.
	if err == nil {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNSPECIFIED,
			reputation.NewSuccessSignal(latency)
	}

	// Classify the errors and map directly to reputation signals.
	// Errors come from SDK, HTTP, internal sources, etc.
	switch {

	// HTTP relay errors - check first to handle HTTP-specific classifications
	case errors.Is(err, errSendHTTPRelay):
		return classifyHttpErrorAsSignal(logger, err, latency)

	// Endpoint payload failed to unmarshal/validate
	case errors.Is(err, errMalformedEndpointPayload):
		// Extract the payload content from the error message
		errorStr := err.Error()
		payloadContent := strings.TrimPrefix(errorStr, "raw_payload: ")
		if idx := strings.LastIndex(payloadContent, ": endpoint returned malformed payload"); idx != -1 {
			payloadContent = payloadContent[:idx]
		}
		return classifyMalformedPayloadAsSignal(logger, payloadContent, latency)

	// Endpoint payload failed to unmarshal into a RelayResponse struct
	// Category: Supplier Protocol Violations (CRITICAL -25)
	case errors.Is(err, sdk.ErrRelayResponseValidationUnmarshal):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_PAYLOAD_UNMARSHAL_ERR,
			reputation.NewCriticalErrorSignal("validation_error", latency)

	// Endpoint response failed basic validation
	// Category: Supplier Protocol Violations (CRITICAL -25)
	case errors.Is(err, sdk.ErrRelayResponseValidationBasicValidation):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_VALIDATION_ERR,
			reputation.NewCriticalErrorSignal("validation_error", latency)

	// Could not fetch the public key for supplier address used for the relay.
	// Category: Supplier Protocol Violations (CRITICAL -25)
	case errors.Is(err, sdk.ErrRelayResponseValidationGetPubKey):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_GET_PUBKEY_ERR,
			reputation.NewCriticalErrorSignal("validation_error", latency)

	// Received nil public key on supplier lookup using its address.
	// This means the supplier account is not properly initialized:
	//
	// In Cosmos SDK (and thus in pocketd) accounts:
	// - Are created when they receive tokens.
	// - Get their public key onchain once they sign their first transaction (e.g. send, delegate, stake, etc.)
	// Category: Supplier Protocol Violations (CRITICAL -25)
	case errors.Is(err, sdk.ErrRelayResponseValidationNilSupplierPubKey):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_NIL_SUPPLIER_PUBKEY,
			reputation.NewCriticalErrorSignal("validation_error", latency)

	// RelayResponse's signature failed validation.
	// Category: Supplier Protocol Violations (CRITICAL -25)
	case errors.Is(err, sdk.ErrRelayResponseValidationSignatureError):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_SIGNATURE_VALIDATION_ERR,
			reputation.NewCriticalErrorSignal("validation_error", latency)

	// Websocket connection failed.
	// Category: Supplier Infrastructure Issues - Connection (MAJOR -10)
	case errors.Is(err, errCreatingWebSocketConnection):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_CONNECTION_FAILED,
			reputation.NewMajorErrorSignal("connection_error", latency)

	// Error signing the relay request.
	// Category: Not Supplier's Fault - Transient (MINOR -3)
	// Could be PATH-side issue with signing
	case errors.Is(err, errRelayRequestWebsocketMessageSigningFailed):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_REQUEST_SIGNING_FAILED,
			reputation.NewMinorErrorSignal("websocket_validation")

	// Error validating the relay response in a websocket message.
	// Category: Not Supplier's Fault - Transient (MINOR -3)
	// Could be transient validation issue
	case errors.Is(err, errRelayResponseInWebsocketMessageValidationFailed):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_RELAY_RESPONSE_VALIDATION_FAILED,
			reputation.NewMinorErrorSignal("websocket_validation")
	}

	// Fallback to error matching using the error string.
	// Extract the specific error type using centralized error matching.
	extractedErr := extractErrFromRelayError(err)

	// Map known errors to endpoint error types and reputation signals.
	switch extractedErr {

	// Endpoint Configuration error
	// Category: Configuration Issues (MAJOR -10)
	case errRelayEndpointConfig:
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_CONFIG,
			reputation.NewMajorErrorSignal("config_error", latency)

	// Endpoint timeout error
	// Category: Supplier Infrastructure Issues - Timeout (MAJOR -10)
	case errRelayEndpointTimeout:
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT,
			reputation.NewMajorErrorSignal("timeout", latency)

	// Backend service returned non-2xx HTTP status (4xx, 5xx errors)
	// Category: Supplier Service Errors (CRITICAL -25)
	// HTTP errors allow recovery when service stabilizes
	case pathhttp.ErrRelayEndpointHTTPError:
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BAD_RESPONSE,
			reputation.NewCriticalErrorSignal("service_error", latency)

	// Request canceled by PATH
	// Category: Not Supplier's Fault - PATH Internal (NO PENALTY, +1)
	case errContextCanceled:
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_REQUEST_CANCELED_BY_PATH,
			reputation.NewSuccessSignal(0) // Neutral - not endpoint's fault

	default:
		// Unknown error: log and return generic internal error with minor penalty.
		// Category: Unknown/Unclassified Errors (MINOR -3)
		logger.Error().Err(err).
			Msg("Unrecognized relay error type encountered - code update needed to properly classify this error")

		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNKNOWN,
			reputation.NewMinorErrorSignal(err.Error())
	}
}

// classifyHttpErrorAsSignal classifies HTTP-related errors and returns error type + reputation signal.
// See ERROR_CLASSIFICATION.md sections 3-6 for HTTP error categories.
func classifyHttpErrorAsSignal(logger polylog.Logger, err error, latency time.Duration) (protocolobservations.ShannonEndpointErrorType, reputation.Signal) {
	logger = logger.With("error_message", err.Error())

	// Backend service returned non-2xx HTTP status code
	// Category: Supplier Service Errors (CRITICAL -25)
	if errors.Is(err, pathhttp.ErrRelayEndpointHTTPError) {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NON_2XX_STATUS,
			reputation.NewCriticalErrorSignal("service_error", latency)
	}

	// RelayMiner returned non-2xx HTTP status code.
	if errors.Is(err, errEndpointNon2XXHTTPStatusCode) {
		errorType, signal := classifyNon2XXStatusCode(err, latency)
		return errorType, signal
	}

	errStr := err.Error()

	// Connection establishment failures
	// Category: Supplier Infrastructure Issues - Connection (MAJOR -10)
	switch {
	case strings.Contains(errStr, "connection refused"):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_REFUSED,
			reputation.NewMajorErrorSignal("connection_error", latency)
	case strings.Contains(errStr, "connection reset"):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_RESET,
			reputation.NewMajorErrorSignal("connection_error", latency)
	case strings.Contains(errStr, "no route to host"):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NO_ROUTE_TO_HOST,
			reputation.NewMajorErrorSignal("connection_error", latency)
	case strings.Contains(errStr, "network is unreachable"):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NETWORK_UNREACHABLE,
			reputation.NewMajorErrorSignal("connection_error", latency)
	}

	// Transport layer errors
	// Category: Supplier Infrastructure Issues - Connection/Transport (MAJOR -10)
	switch {
	case strings.Contains(errStr, "broken pipe"):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BROKEN_PIPE,
			reputation.NewMajorErrorSignal("connection_error", latency)
	case strings.Contains(errStr, "i/o timeout"):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_IO_TIMEOUT,
			reputation.NewMajorErrorSignal("timeout", latency)
	case strings.Contains(errStr, "context deadline exceeded"):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONTEXT_DEADLINE_EXCEEDED,
			reputation.NewMajorErrorSignal("timeout", latency)
	}

	// Connection timeout (separate from i/o timeout)
	// Category: Supplier Infrastructure Issues - Timeout (MAJOR -10)
	if strings.Contains(errStr, "dial tcp") && strings.Contains(errStr, "timeout") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_TIMEOUT,
			reputation.NewMajorErrorSignal("timeout", latency)
	}

	// HTTP protocol errors
	// Category: Supplier Infrastructure Issues - Transport (MAJOR -10)
	switch {
	case strings.Contains(errStr, "malformed HTTP"):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BAD_RESPONSE,
			reputation.NewMajorErrorSignal("transport_error", latency)
	case strings.Contains(errStr, "invalid status"):
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_INVALID_STATUS,
			reputation.NewMajorErrorSignal("transport_error", latency)
	}

	// Generic transport errors (catch-all for other transport issues)
	// Category: Supplier Infrastructure Issues - Transport (MAJOR -10)
	if strings.Contains(errStr, "transport") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_TRANSPORT_ERROR,
			reputation.NewMajorErrorSignal("transport_error", latency)
	}

	// If we can't classify the HTTP error, it's an unknown error
	// Category: Unknown/Unclassified Errors (MINOR -3)
	logger.With(
		"err_preview", errStr[:min(100, len(errStr))],
	).Warn().Msg("Unable to classify HTTP error - defaulting to unknown error with minor penalty")

	return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_UNKNOWN,
		reputation.NewMinorErrorSignal("unknown_http_error")
}

// classifyNon2XXStatusCode classifies non-2xx HTTP status codes into error types and signals.
// See ERROR_CLASSIFICATION.md sections 3 (5xx) and 8 (4xx).
func classifyNon2XXStatusCode(err error, latency time.Duration) (protocolobservations.ShannonEndpointErrorType, reputation.Signal) {
	statusCode, ok := extractHTTPStatusCode(err)
	if !ok {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_UNKNOWN,
			reputation.NewMinorErrorSignal("unknown_http_status")
	}

	switch {
	case statusCode >= 400 && statusCode < 500:
		// Category: Not Supplier's Fault - Client Errors (MINOR -3)
		// 4xx means PATH sent a bad request
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_4XX,
			reputation.NewMinorErrorSignal("client_error")

	case statusCode >= 500 && statusCode < 600:
		// Category: Supplier Service Errors (CRITICAL -25)
		// 5xx is server-side failure - supplier's responsibility
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX,
			reputation.NewCriticalErrorSignal("service_error", latency)

	default:
		// Category: Unknown/Unclassified Errors (MINOR -3)
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_UNKNOWN,
			reputation.NewMinorErrorSignal("unknown_http_status")
	}
}

// classifyMalformedPayloadAsSignal classifies errors found in malformed endpoint response payloads.
// See ERROR_CLASSIFICATION.md sections 1-2 and 4-6 for payload error categories.
func classifyMalformedPayloadAsSignal(logger polylog.Logger, payloadContent string, latency time.Duration) (protocolobservations.ShannonEndpointErrorType, reputation.Signal) {
	logger = logger.With("payload_content_preview", payloadContent[:min(len(payloadContent), 200)])

	// Use heuristic indicator analysis to detect HTTP error patterns (bad gateway, 502, 503, etc.)
	// This uses the same patterns as the gateway layer heuristic detection for consistency.
	indicatorResult := heuristic.IndicatorAnalysis([]byte(payloadContent), false)
	if indicatorResult.Found && indicatorResult.Category == heuristic.CategoryHTTPError {
		// HTTP error patterns (bad gateway, service unavailable, etc.) are server-side failures
		// Category: Supplier Service Errors (CRITICAL -25)
		logger.Debug().
			Str("pattern", indicatorResult.Pattern).
			Float64("confidence", indicatorResult.Confidence).
			Msg("Detected HTTP error pattern in malformed payload using heuristic analysis")
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE,
			reputation.NewCriticalErrorSignal("backend_http_error", latency)
	}

	// Connection refused errors - most common pattern (~52% of errors)
	// Category: Supplier Infrastructure Issues - Connection (MAJOR -10)
	if strings.Contains(payloadContent, "connection refused") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED,
			reputation.NewMajorErrorSignal("connection_error", latency)
	}

	// Service not configured - second most common pattern (~17% of errors)
	// Category: Supplier Service Misconfiguration (FATAL -50)
	if strings.Contains(payloadContent, "service endpoint not handled by relayer proxy") ||
		regexp.MustCompile(`service "[^"]+" not configured`).MatchString(payloadContent) {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVICE_NOT_CONFIGURED,
			reputation.NewFatalErrorSignal("service_misconfiguration")
	}

	// Protocol parsing errors
	// Category: Supplier Protocol Violations (CRITICAL -25)
	if regexp.MustCompile(`proto: illegal wireType \d+`).MatchString(payloadContent) {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_WIRE_TYPE,
			reputation.NewCriticalErrorSignal("validation_error", latency)
	}

	if strings.Contains(payloadContent, "proto: RelayRequest: wiretype end group") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_RELAY_REQUEST,
			reputation.NewCriticalErrorSignal("validation_error", latency)
	}

	// Unexpected EOF
	// Category: Supplier Infrastructure Issues - Transport (MAJOR -10)
	if strings.Contains(payloadContent, "unexpected EOF") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNEXPECTED_EOF,
			reputation.NewMajorErrorSignal("transport_error", latency)
	}

	// Backend service errors
	// Category: Supplier Service Errors (CRITICAL -25)
	if regexp.MustCompile(`backend service returned an error with status code \d+`).MatchString(payloadContent) {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE,
			reputation.NewCriticalErrorSignal("service_error", latency)
	}

	// Suppliers not reachable
	// Category: Not Supplier's Fault - Transient (MINOR -3)
	// Could be temporary network issue
	if strings.Contains(payloadContent, "supplier(s) not reachable") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SUPPLIERS_NOT_REACHABLE,
			reputation.NewMinorErrorSignal("suppliers_unreachable")
	}

	// Supplier does not belong to session
	// Category: Session Mismatch - Likely PATH Bug (NO PENALTY)
	// This error indicates PATH is sending a relay with a session header that doesn't match
	// what the RelayMiner expects. This is likely a session caching/timing issue on PATH side.
	// We should NOT penalize the supplier for this - it's not their fault.
	if strings.Contains(payloadContent, "supplier does not belong to session") {
		logger.Warn().
			Str("payload_preview", payloadContent[:min(len(payloadContent), 200)]).
			Msg("Session mismatch detected - supplier claims not in session. This is likely a PATH-side caching issue.")
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SESSION_MISMATCH,
			reputation.NewSuccessSignal(0) // No penalty - not supplier's fault
	}

	// Response size exceeded
	// Category: Not Supplier's Fault - Transient (MINOR -3)
	// Could be legitimate large response
	if strings.Contains(payloadContent, "body size exceeds maximum allowed") ||
		strings.Contains(payloadContent, "response limit exceed") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RESPONSE_SIZE_EXCEEDED,
			reputation.NewMinorErrorSignal("response_size_exceeded")
	}

	// Server closed connection
	// Category: Not Supplier's Fault - Transient (MINOR -3)
	// Normal HTTP behavior
	if strings.Contains(payloadContent, "server closed idle connection") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVER_CLOSED_CONNECTION,
			reputation.NewMinorErrorSignal("connection_closed")
	}

	// TCP connection errors
	// Category: Supplier Infrastructure Issues - Connection (MAJOR -10)
	if strings.Contains(payloadContent, "write tcp") && strings.Contains(payloadContent, "connection") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TCP_CONNECTION,
			reputation.NewMajorErrorSignal("connection_error", latency)
	}

	// DNS resolution errors
	// Category: Supplier Infrastructure Issues - Connection (MAJOR -10)
	if strings.Contains(payloadContent, "no such host") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_DNS_RESOLUTION,
			reputation.NewMajorErrorSignal("connection_error", latency)
	}

	// TLS handshake errors
	// Category: Supplier Infrastructure Issues - Connection (MAJOR -10)
	if strings.Contains(payloadContent, "tls") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TLS_HANDSHAKE,
			reputation.NewMajorErrorSignal("connection_error", latency)
	}

	// General HTTP transport errors
	// Category: Supplier Infrastructure Issues - Transport (MAJOR -10)
	if strings.Contains(payloadContent, "http:") || strings.Contains(payloadContent, "HTTP") {
		return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_HTTP_TRANSPORT,
			reputation.NewMajorErrorSignal("transport_error", latency)
	}

	// If we can't classify the malformed payload, it's an unknown error
	// Category: Unknown/Unclassified Errors (MINOR -3)
	logger.With(
		"endpoint_payload_preview", payloadContent[:min(100, len(payloadContent))],
	).Warn().Msg("Unable to classify malformed endpoint payload - defaulting to unknown error with minor penalty")

	return protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNKNOWN,
		reputation.NewMinorErrorSignal("unknown_payload_error")
}

// errorTypeToSignal maps an error type directly to a reputation signal.
// This is used when we only have the error type (from observations) rather than the full error.
// See ERROR_CLASSIFICATION.md for detailed documentation of all error categories.
func errorTypeToSignal(errorType protocolobservations.ShannonEndpointErrorType, latency time.Duration) reputation.Signal {
	switch errorType {
	// Category: Supplier Service Misconfiguration (FATAL -50)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVICE_NOT_CONFIGURED:
		return reputation.NewFatalErrorSignal("service_misconfiguration")

	// Category: Supplier Protocol Violations (CRITICAL -25)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_VALIDATION_ERR,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_SIGNATURE_VALIDATION_ERR,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RESPONSE_GET_PUBKEY_ERR,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_NIL_SUPPLIER_PUBKEY,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_PAYLOAD_UNMARSHAL_ERR,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_WIRE_TYPE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_RELAY_REQUEST:
		return reputation.NewCriticalErrorSignal("validation_error", latency)

	// Category: Supplier Service Errors (CRITICAL -25)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_NON_2XX_STATUS,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_BAD_RESPONSE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_5XX,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE:
		return reputation.NewCriticalErrorSignal("service_error", latency)

	// Category: Supplier Infrastructure Issues - Connection (MAJOR -10)
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

	// Category: Supplier Infrastructure Issues - Timeout (MAJOR -10)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_TIMEOUT,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_IO_TIMEOUT,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONTEXT_DEADLINE_EXCEEDED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_CONNECTION_TIMEOUT:
		return reputation.NewMajorErrorSignal("timeout", latency)

	// Category: Supplier Infrastructure Issues - Transport (MAJOR -10)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_TRANSPORT_ERROR,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_INVALID_STATUS,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNEXPECTED_EOF,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_HTTP_TRANSPORT:
		return reputation.NewMajorErrorSignal("transport_error", latency)

	// Category: Configuration Issues (MAJOR -10)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_CONFIG:
		return reputation.NewMajorErrorSignal("config_error", latency)

	// Category: Not Supplier's Fault - Client Errors (MINOR -3)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RELAY_MINER_HTTP_4XX:
		return reputation.NewMinorErrorSignal("client_error")

	// Category: Not Supplier's Fault - PATH Internal (NO PENALTY, +1)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_REQUEST_CANCELED_BY_PATH:
		return reputation.NewSuccessSignal(0)

	// Category: Not Supplier's Fault - Transient/Normal Behavior (MINOR -3)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_REQUEST_SIGNING_FAILED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_WEBSOCKET_RELAY_RESPONSE_VALIDATION_FAILED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RESPONSE_SIZE_EXCEEDED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVER_CLOSED_CONNECTION,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SUPPLIERS_NOT_REACHABLE:
		return reputation.NewMinorErrorSignal(errorType.String())

	// Category: Unknown/Unclassified Errors (MINOR -3)
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_HTTP_UNKNOWN,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNKNOWN,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_UNKNOWN:
		return reputation.NewMinorErrorSignal(errorType.String())

	default:
		// Unknown error type - treat as minor
		return reputation.NewMinorErrorSignal(errorType.String())
	}
}

// extractHTTPStatusCode extracts the HTTP status code from the error message.
// Expects the status code to be at the end of the error string after ": ".
func extractHTTPStatusCode(err error) (int, bool) {
	errStr := err.Error()

	// Look for ": " followed by 3 digits at the end of the string
	re := regexp.MustCompile(`: (\d{3})$`)
	matches := re.FindStringSubmatch(errStr)

	if len(matches) < 2 {
		return 0, false
	}

	statusCode, parseErr := strconv.Atoi(matches[1])
	if parseErr != nil {
		return 0, false
	}

	// Basic validation that it's a valid HTTP status code
	if statusCode < 100 || statusCode > 599 {
		return 0, false
	}

	return statusCode, true
}

// isSupplierValidationError checks if an error is a supplier-specific validation error.
// These errors (signature, pubkey, unmarshal) are supplier issues that should NOT
// penalize the domain's reputation. Instead, the supplier should be blacklisted.
//
// Returns true for:
// - Signature validation errors
// - Public key errors (missing, nil)
// - Response unmarshal errors
// - Basic validation errors
func isSupplierValidationError(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, sdk.ErrRelayResponseValidationUnmarshal) ||
		errors.Is(err, sdk.ErrRelayResponseValidationBasicValidation) ||
		errors.Is(err, sdk.ErrRelayResponseValidationGetPubKey) ||
		errors.Is(err, sdk.ErrRelayResponseValidationNilSupplierPubKey) ||
		errors.Is(err, sdk.ErrRelayResponseValidationSignatureError)
}

// getBlacklistReason returns the appropriate blacklist reason constant for an error.
// Used for metrics tracking to understand why suppliers are being blacklisted.
func getBlacklistReason(err error) string {
	if err == nil {
		return "unknown"
	}

	switch {
	case errors.Is(err, sdk.ErrRelayResponseValidationSignatureError):
		return metrics.BlacklistReasonSignatureError
	case errors.Is(err, sdk.ErrRelayResponseValidationUnmarshal):
		return metrics.BlacklistReasonUnmarshalError
	case errors.Is(err, sdk.ErrRelayResponseValidationGetPubKey):
		return metrics.BlacklistReasonPubKeyError
	case errors.Is(err, sdk.ErrRelayResponseValidationNilSupplierPubKey):
		return metrics.BlacklistReasonNilPubKey
	case errors.Is(err, sdk.ErrRelayResponseValidationBasicValidation):
		return metrics.BlacklistReasonValidationError
	default:
		return "unknown"
	}
}

// isPubkeyRelatedError checks if the error is related to public key issues.
// These errors may be caused by stale cached pubkey data and can potentially
// be resolved by invalidating the cache and retrying.
//
// Returns true for:
// - ErrRelayResponseValidationGetPubKey: Failed to fetch public key
// - ErrRelayResponseValidationNilSupplierPubKey: Supplier has nil public key
// - ErrRelayResponseValidationSignatureError: Signature doesn't match (may be wrong key)
func isPubkeyRelatedError(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, sdk.ErrRelayResponseValidationGetPubKey) ||
		errors.Is(err, sdk.ErrRelayResponseValidationNilSupplierPubKey) ||
		errors.Is(err, sdk.ErrRelayResponseValidationSignatureError)
}
