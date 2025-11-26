package noop

import (
	"github.com/pokt-network/path/protocol"
)

// endpointResponse keeps the response received from an endpoint, along with the correspoding
// endpoint's address.
// It is used by the requestContext struct to maintain the response(s) received from endpoint(s).
type endpointResponse struct {
	// EndpointAddr is the address of the endpoint which returned the response stored in
	// this instance of endpointResponse.
	EndpointAddr protocol.EndpointAddr
	// ResponseBytes is the raw response received from an endpoint.
	ResponseBytes []byte
}
