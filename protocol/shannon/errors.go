package shannon

import (
	"context"
	"crypto/x509"
	"errors"
	"net"

 	pathhttp "github.com/pokt-network/path/network/http"
 	protocolobservations "github.com/pokt-network/path/observation/protocol"
)

var (
	// Unsupported gateway mode
	errProtocolContextSetupUnsupportedGatewayMode = errors.New("unsupported gateway mode")

	// === Network & relay errors ===
	errRelayEndpointConfig       = errors.New("endpoint configuration error")                  // TLS/DNS/config issues
	errRelayEndpointTimeout      = errors.New("timeout waiting for endpoint response")
	errContextCanceled           = errors.New("context canceled manually")                      // PATH-initiated cancel
	errSendHTTPRelay             = errors.New("HTTP relay request failed")
	errMalformedEndpointPayload  = errors.New("endpoint returned malformed payload")

	// === Gateway mode errors ===
	errProtocolContextSetupCentralizedAppFetchErr      = errors.New("error getting onchain data for app owned by the gateway")
	errProtocolContextSetupCentralizedAppDelegation    = errors.New("centralized gateway mode app does not delegate to the gateway")
	errProtocolContextSetupCentralizedNoSessions       = errors.New("no active sessions could be retrieved for the service")
	errProtocolContextSetupCentralizedNoAppsForService = errors.New("ZERO owned apps found for service")
	errProtocolContextSetupGetAppFromHTTPReq           = errors.New("error getting the selected app from the HTTP request")
	errProtocolContextSetupFetchSession                = errors.New("error getting a session from the full node for app")
	errProtocolContextSetupAppDoesNotDelegate          = errors.New("gateway does not have delegation for app")
	errProtocolContextSetupAppNotStaked                = errors.New("app is not staked for the service")

	// === Request context setup errors ===
	errProtocolContextSetupNoEndpoints                = errors.New("no endpoints found for service: relay request will fail")
	errRequestContextSetupInvalidEndpointSelected     = errors.New("selected endpoint is not available: relay request will fail")
	errRequestContextSetupErrSignerSetup              = errors.New("error getting the permitted signer: relay request will fail")

	// === Websocket errors ===
	errCreatingWebSocketConnection                    = errors.New("error creating Websocket connection")
	errRelayRequestWebsocketMessageSigningFailed      = errors.New("error signing relay request in websocket message")
	errRelayResponseInWebsocketMessageValidationFailed = errors.New("error validating relay response in websocket message")
)

// extractErrFromRelayError:
// • Analyzes errors returned during relay operations
// • Matches errors to predefined types through:
//   - Primary: errors.Is / errors.As (robust, works with wrapped errors)
//   - Secondary: Type-specific matching for network/config errors
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

	// Context-based errors (robust, works recursively through wrapped errors)
	if errors.Is(err, context.DeadlineExceeded) {
		return errRelayEndpointTimeout
	}

	if errors.Is(err, pathhttp.ErrRelayEndpointHTTPError) {
		return pathhttp.ErrRelayEndpointHTTPError
	}

	if errors.Is(err, context.Canceled) {
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
	// Use errors.As for robust type matching (works with wrapped errors)
	// Covers common DNS, dial, and TLS certificate issues

	// DNS resolution errors
	if errors.As(err, &net.DNSError{}) {
		return true
	}

	// General dial errors (includes connection refused, etc.)
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}

	// Common x509 certificate validation errors
	if errors.As(err, &x509.HostnameError{}) {
		return true
	}
	if errors.As(err, &x509.UnknownAuthorityError{}) {
		return true
	}
	if errors.As(err, &x509.CertificateInvalidError{}) {
		return true
	}

	return false
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
