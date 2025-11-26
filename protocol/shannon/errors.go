package shannon

import (
	"context"
	"errors"
	"strings"

	pathhttp "github.com/pokt-network/path/network/http"
	protocolobservations "github.com/pokt-network/path/observation/protocol"
)

var (
	// Unsupported gateway mode
	errProtocolContextSetupUnsupportedGatewayMode = errors.New("unsupported gateway mode")

	// ** Network errors **
	// endpoint configuration error:
	// - TLS certificate verification error.
	// - DNS error on lookup of endpoint URL.
	errRelayEndpointConfig = errors.New("endpoint configuration error")

	// endpoint timeout
	errRelayEndpointTimeout = errors.New("timeout waiting for endpoint response")
	// PATH manually canceled the context for the request.
	// E.g. Parallel requests were made and one succeeded so the other was canceled.
	errContextCanceled = errors.New("context canceled manually")

	// HTTP relay request failed - wraps net/http package errors
	errSendHTTPRelay = errors.New("HTTP relay request failed")

	// ** Centralized gateway mode errors **

	// Centralized gateway mode: Error getting onchain data for app
	errProtocolContextSetupCentralizedAppFetchErr = errors.New("error getting onchain data for app owned by the gateway")
	// Centralized gateway mode app does not delegate to the gateway.
	errProtocolContextSetupCentralizedAppDelegation = errors.New("centralized gateway mode app does not delegate to the gateway")
	// Centralized gateway mode: no active sessions could be retrieved for the service.
	errProtocolContextSetupCentralizedNoSessions = errors.New("no active sessions could be retrieved for the service")
	// Centralized gateway mode: no owned apps found for the service.
	errProtocolContextSetupCentralizedNoAppsForService = errors.New("ZERO owned apps found for service")

	// Delegated gateway mode: could not extract app from HTTP request.
	errProtocolContextSetupGetAppFromHTTPReq = errors.New("error getting the selected app from the HTTP request")
	// Delegated gateway mode: could not fetch session for app from the full node
	errProtocolContextSetupFetchSession = errors.New("error getting a session from the full node for app")
	// Delegated gateway mode: gateway does not have delegation for the app.
	errProtocolContextSetupAppDoesNotDelegate = errors.New("gateway does not have delegation for app")
	// Delegated gateway mode: app is not staked for the service.
	errProtocolContextSetupAppNotStaked = errors.New("app is not staked for the service")

	// ** Request context setup errors **

	// No endpoints available for the service.
	// Can be due to one or more of the following:
	// - Any of the gateway mode errors above.
	// - Error fetching a session for an app.
	errProtocolContextSetupNoEndpoints = errors.New("no endpoints found for service: relay request will fail")
	// Selected endpoint is no longer available.
	// Can happen due to:
	// - Bug in endpoint selection logic.
	// - Endpoint sanctioned due to an observation while selection logic was running.
	errRequestContextSetupInvalidEndpointSelected = errors.New("selected endpoint is not available: relay request will fail")
	// Error initializing a signer for the current gateway mode.
	errRequestContextSetupErrSignerSetup = errors.New("error getting the permitted signer: relay request will fail")

	// The endpoint returned a malformed payload.
	// Helps track more fine-grained metrics on endpoint errors.
	errMalformedEndpointPayload = errors.New("endpoint returned malformed payload")

	// The endpoint returned a non-2XX response.
	errEndpointNon2XXHTTPStatusCode = errors.New("endpoint returned non-2xx HTTP status code")

	// ** Websocket errors **

	// Error creating a Websocket connection.
	errCreatingWebSocketConnection = errors.New("error creating Websocket connection")

	// Error signing the relay request in a websocket message.
	errRelayRequestWebsocketMessageSigningFailed = errors.New("error signing relay request in websocket message")

	// Error validating the relay response in a websocket message.
	errRelayResponseInWebsocketMessageValidationFailed = errors.New("error validating relay response in websocket message")
)

// extractErrFromRelayError:
// • Analyzes errors returned during relay operations
// • Matches errors to predefined types through:
//   - Primary: Error comparison (with unwrapping)
//   - Fallback: String analysis for unrecognized types
//
// • Centralizes error recognition logic to avoid duplicate string matching
// • Provides fine-grained HTTP error classification
func extractErrFromRelayError(err error) error {
	// HTTP relay request failed.
	// Return as-is for further classification
	if errors.Is(err, errSendHTTPRelay) {
		return err
	}

	if isEndpointNetworkConfigError(err) {
		return errRelayEndpointConfig
	}

	// http endpoint timeout
	if strings.Contains(err.Error(), context.DeadlineExceeded.Error()) { // "context deadline exceeded"
		return errRelayEndpointTimeout
	}

	// Endpoint's backend service returned a non 2xx HTTP status code.
	if strings.Contains(err.Error(), "non 2xx HTTP status code") {
		return pathhttp.ErrRelayEndpointHTTPError
	}
	// context canceled manually
	if strings.Contains(err.Error(), "context canceled") {
		return errContextCanceled
	}

	// No known patterns matched.
	// return the error as-is.
	return err
}

// isEndpointNetworkConfigError returns true if the error indicating an endpoint configuration error.
//
// Examples:
// - Error verifying endpoint's TLS certificate
// - Error on DNS lookup of endpoint's URL.
func isEndpointNetworkConfigError(err error) bool {
	errStr := err.Error()
	switch {
	case strings.Contains(errStr, "dial tcp: lookup"):
		return true
	case strings.Contains(errStr, "tls: failed to verify certificate"):
		return true
	default:
		return false
	}
}

// isMalformedEndpointPayloadError returns true if the error indicates a malformed endpoint payload.
// Used for metrics categorization of endpoint errors.
func isMalformedEndpointPayloadError(errorType protocolobservations.ShannonEndpointErrorType) bool {
	switch errorType {
	case protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_CONNECTION_REFUSED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVICE_NOT_CONFIGURED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_UNEXPECTED_EOF,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_WIRE_TYPE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_PROTOCOL_RELAY_REQUEST,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SUPPLIERS_NOT_REACHABLE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_BACKEND_SERVICE,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TCP_CONNECTION,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_RESPONSE_SIZE_EXCEEDED,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_SERVER_CLOSED_CONNECTION,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_HTTP_TRANSPORT,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_DNS_RESOLUTION,
		protocolobservations.ShannonEndpointErrorType_SHANNON_ENDPOINT_ERROR_RAW_PAYLOAD_TLS_HANDSHAKE:
		return true
	default:
		return false
	}
}
