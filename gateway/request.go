package gateway

import (
	"context"
	"net/http"

	pathhttp "github.com/pokt-network/path/network/http"
	"github.com/pokt-network/path/protocol"
)

// HTTPRequestParser is used, in handling an HTTP service request, to extract
// the service ID and corresponding QoS service from an HTTP request.
type HTTPRequestParser interface {
	// GetQoSService returns the qos for the service matching an HTTP request.
	GetQoSService(context.Context, *http.Request) (protocol.ServiceID, QoSService, error)

	// GetHTTPErrorResponse returns an HTTP response using the supplied error.
	// It will only be called if the GetQoSService method above returns an error.
	GetHTTPErrorResponse(context.Context, error) pathhttp.HTTPResponse
}

// TODO_INCOMPLETE: implement the HTTPRequestParser in a separate package, e.g. `request`.
// This is skipped for now to focus on the gateway package.
// Such an implementation should, at the minimum, perform the following tasks:
// 1. Extract the target ServiceID from the HTTP request, e.g. using the HTTP request's domain, headers, etc.
// 2. Use its configured mapping of service IDs to QoS implementations
// to return the matching QoS instance for the service.

// TODO_INCOMPLETE: the mapping of service IDs to service QoS implementations
// which will be embedded in the codebase, needs to be constructed by the
// config package, which should at the minimum:
// 1. Define names for service QoS implementations offered by the
// qos package, which are to be used in the config YAML to reference
// the embedded QoS implementations.
// 2. Parse a YAML snippet (using the above names) into a map of
// service IDs to the embedded service QoS implementations.
// e.g. the following YAML snippet:
//
//	eth: evm
//	poly: evm
//	solana:  solana
//
// could be translated into:
//
//	map[protocol.ServiceID]QoSService{
//	   "eth": qos.Evm{},
//	   "poly":  qos.Evm{},
//	   "solana":   qos.Solana{},
//	}
