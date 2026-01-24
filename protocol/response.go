package protocol

// RelayMetadata contains metadata about a relay for debugging and transparency.
// This information is exposed via HTTP response headers to help clients
// understand which infrastructure served their request.
type RelayMetadata struct {
	// AppAddress is the application address used to sign/authorize the relay.
	AppAddress string
	// SupplierAddress is the supplier/operator address of the endpoint that served the relay.
	SupplierAddress string
	// SessionID is the session identifier from which the supplier was selected.
	SessionID string
}

// Response is a general purpose struct for capturing the response to a relay, received from an endpoint.
// TODO_FUTURE(@adshmh): It only supports HTTP responses for now; add support for others.
type Response struct {
	// Bytes is the response to a relay received from an endpoint.
	// An endpoint is the backend server servicing an onchain service.
	// This can be the serialized response to any type of RPC (gRPC, HTTP, etc.)
	Bytes []byte
	// HTTPStatusCode is the HTTP status returned by an endpoint in response to a relay request.
	HTTPStatusCode int

	// EndpointAddr is the address of the endpoint which returned the response.
	EndpointAddr

	// Metadata contains relay-specific information for debugging/transparency.
	// Used to populate response headers like X-Supplier-Address, X-App-Address, X-Session-ID.
	Metadata RelayMetadata
}
