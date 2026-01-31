// Request package is responsible for parsing and forwarding user requests.
// It is not responsible for QoS, authorization, etc...
// For example, Processing should fail here only if no authoritative service ID is provided - Bad Request
//
// The responsibility of the `request` package is to extract the authoritative service ID and return the target service's corresponding QoS instance.
// See: https://github.com/pokt-network/path/blob/e0067eb0f9ab0956127c952980b09909a795b300/gateway/gateway.go#L52C2-L52C45
package request

import (
	"context"
	"errors"
	"net/http"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/gateway"
	pathhttp "github.com/pokt-network/path/network/http"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/noop"
)

// HTTP Request Headers
// Please see the following link on the deprecation of X- prefix in HTTP header
// parameter names and why it wasn't used: https://www.rfc-editor.org/rfc/rfc6648#section-3
const (
	// HTTPHeaderTargetServiceID is the key used to lookup the HTTP header specifying the target
	// service's ID.
	HTTPHeaderTargetServiceID = "Target-Service-Id"

	// TODO_DOCUMENT(@adshmh): Update the docs at https://path.grove.city/ to reflect this usage pattern.
	// HTTPHeaderAppAddress is the key of the entry in HTTP headers that holds the target app's address
	// in delegated mode. The target app will be used for sending the relay request.
	HTTPHeaderAppAddress = "App-Address"

	// HTTPHeaderTargetSuppliers is the key of the entry in HTTP headers that holds a comma-separated list
	// of supplier addresses. When present, only suppliers from this list will be used for relays,
	// bypassing reputation and other filtering logic.
	// Example: "Target-Suppliers: pokt1abc...,pokt1def...,pokt1ghi..."
	HTTPHeaderTargetSuppliers = "Target-Suppliers"

	// WebSocket handshake headers for RelayMiner validation
	// These headers carry RelayRequest.Meta equivalent data for connection-time validation.

	// HTTPHeaderSessionID is the session identifier.
	HTTPHeaderSessionID = "Pocket-Session-Id"

	// HTTPHeaderSessionStartHeight is the session start block height.
	HTTPHeaderSessionStartHeight = "Pocket-Session-Start-Height"

	// HTTPHeaderSessionEndHeight is the session end block height.
	HTTPHeaderSessionEndHeight = "Pocket-Session-End-Height"

	// HTTPHeaderSupplierAddress is the target supplier operator address.
	HTTPHeaderSupplierAddress = "Pocket-Supplier-Address"

	// HTTPHeaderSignature is the ring signature for handshake validation (base64 encoded).
	HTTPHeaderSignature = "Pocket-Signature"
)

// The Parser struct is responsible for parsing the authoritative service ID from the request's
// 'Target-Service-Id' header and returning the corresponding QoS service implementation.
type Parser struct {
	Logger polylog.Logger

	// QoSServices is the set of QoS services to which the request parser should map requests based on the extracted service ID.
	QoSServices map[protocol.ServiceID]gateway.QoSService
}

/* --------------------------------- HTTP Request Parsing -------------------------------- */

// GetQoSService returns the QoS service implementation for the given request, as well as the authoritative service ID.
// If the service ID does not have a corresponding QoS implementation, the NoOp QoS service is returned.
func (p *Parser) GetQoSService(ctx context.Context, req *http.Request) (protocol.ServiceID, gateway.QoSService, error) {
	// Get the authoritative service ID from the request's header.
	serviceID, err := p.getServiceID(req)
	if err != nil {
		return "", nil, err
	}

	// Return the QoS service implementation for the request's service ID if it exists.
	if qosService, ok := p.QoSServices[serviceID]; ok {
		return serviceID, qosService, nil
	}

	// No matching QoS implementation.
	// Log a warning.
	// Return a NoOp QoS implementation.
	p.Logger.With(
		"method", "GetQoSService",
		"service_id", serviceID,
	).Warn().Msg("No matching QoS implementations found. Using NoOp QoS.")

	return serviceID, noop.NoOpQoS{}, nil
}

// getServiceID extracts the authoritative service ID from the HTTP request's `Target-Service-Id` header.
func (p *Parser) getServiceID(req *http.Request) (protocol.ServiceID, error) {
	if serviceID := req.Header.Get(HTTPHeaderTargetServiceID); serviceID != "" {
		return protocol.ServiceID(serviceID), nil
	}
	return "", errNoServiceIDProvided
}

/* --------------------------------- HTTP Error Response -------------------------------- */

// GetHTTPErrorResponse returns an HTTP response with the appropriate status code and
// error message, which ensures the error response is returned in a valid JSON format.
func (p *Parser) GetHTTPErrorResponse(ctx context.Context, err error) pathhttp.HTTPResponse {
	if errors.Is(err, errNoServiceIDProvided) {
		return &parserErrorResponse{err: err.Error(), code: http.StatusBadRequest}
	}
	return &parserErrorResponse{err: err.Error(), code: http.StatusNotFound}
}

// GetQoSServiceForServiceID returns the QoS service for a given service ID.
// Returns nil if no QoS service is registered for the service ID.
// Implements the gateway.QoSServiceRegistry interface.
func (p *Parser) GetQoSServiceForServiceID(serviceID protocol.ServiceID) gateway.QoSService {
	if qosService, ok := p.QoSServices[serviceID]; ok {
		return qosService
	}
	return nil
}
