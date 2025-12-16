package shannon

import (
	"fmt"

	apptypes "github.com/pokt-network/poktroll/x/application/types"
	sessiontypes "github.com/pokt-network/poktroll/x/session/types"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"
	sdk "github.com/pokt-network/shannon-sdk"

	"github.com/pokt-network/path/protocol"
)

// TODO_TECHDEBT(@adshmh): Refactor this:
// - Review the implementation of the endpoint interface.
// - Avoid the need for a shannon specific implementation of Endpoint
// - Example: Make endpoint an interface, implemented by:
//   - A Shannon endpoint
//   - A "fallback" URL with configurable fields: e.g. the Supplier set as "fallback"
// - PR Review Reference: https://github.com/pokt-network/path/pull/395#discussion_r2261426190

// endpoint defines the interface for Shannon endpoints, allowing for
// different implementations (e.g., protocol vs fallback endpoints).
type endpoint interface {
	protocol.Endpoint

	Session() *sessiontypes.Session
	Supplier() string

	// GetURL returns the appropriate URL for the given RPC type.
	// For regular endpoints, this returns the public URL regardless of RPC type.
	// For fallback endpoints, this returns the URL specific to the RPC type.
	GetURL(rpcType sharedtypes.RPCType) string
	IsFallback() bool
}

// -------------------- Fallback Endpoint --------------------

var _ endpoint = fallbackEndpoint{}
var _ protocol.Endpoint = fallbackEndpoint{}

// fallbackEndpoint is a fallback endpoint for a service.
//   - It is defined in the PATH config YAML file.
//   - It is identified by the `fallbackSupplierString` and its default URL.
type fallbackEndpoint struct {
	defaultURL  string
	rpcTypeURLs map[sharedtypes.RPCType]string
}

// `fallbackSupplierString` is a const value used as placeholder
// for the supplier address of fallback endpoints.
const fallbackSupplierString = "fallback"

// IsFallback returns true for fallback endpoints.
func (e fallbackEndpoint) IsFallback() bool {
	return true
}

// Addr returns the address of the fallback endpoint.
// Fallback endpoints do not exist on the Shannon protocol and so do not have a supplier address.
// Instead, they are identified by the `fallbackSupplierString` const value and the default URL.
func (e fallbackEndpoint) Addr() protocol.EndpointAddr {
	return protocol.EndpointAddr(fmt.Sprintf("%s-%s", fallbackSupplierString, e.defaultURL))
}

// PublicURL is a no-op for fallback endpoints.
// Fallback endpoints use `FallbackURL` to return the
// RPC type-specific URL for the endpoint.
func (e fallbackEndpoint) PublicURL() string {
	return ""
}

// GetURL returns the appropriate URL for the given RPC type
func (e fallbackEndpoint) GetURL(rpcType sharedtypes.RPCType) string {
	// If the RPC type is unknown, return the default URL.
	if rpcType == sharedtypes.RPCType_UNKNOWN_RPC {
		return e.defaultURL
	}

	url, ok := e.rpcTypeURLs[rpcType]
	// If the RPC type is not configured for the
	// fallback endpoint, return the default URL.
	if !ok {
		return e.defaultURL
	}

	// Return the URL for the configured RPC type.
	return url
}

func (e fallbackEndpoint) WebsocketURL() (string, error) {
	websocketURL, ok := e.rpcTypeURLs[sharedtypes.RPCType_WEBSOCKET]
	if !ok {
		return "", fmt.Errorf("websocket URL is not set")
	}
	return websocketURL, nil
}

// Session is a no-op for fallback endpoints.
func (e fallbackEndpoint) Session() *sessiontypes.Session {
	return &sessiontypes.Session{
		// TODO_TECHDEBT(@adshmh): Refactor to separate Shannon and Fallback endpoints.
		// This will allow removing the empty structs below, used to ensure non-nil values under Session field of any endpoint.
		//
		Header:      &sessiontypes.SessionHeader{},
		Application: &apptypes.Application{},
	}
}

// Supplier returns `fallbackSupplierString` as the supplier address.
func (e fallbackEndpoint) Supplier() string {
	return fallbackSupplierString
}

// -------------------- Shannon Protocol Endpoint --------------------

var _ endpoint = protocolEndpoint{}
var _ protocol.Endpoint = protocolEndpoint{}

// protocolEndpoint is a single endpoint present on the Shannon protocol.
//   - It is obtained from a Session returned by a Shannon Full Node.
//   - It is identified by its Supplier address and Relay MinerURL.
type protocolEndpoint struct {
	supplier string

	// Multi-RPC-type URL support: maps each RPC type to its specific URL
	// This replaces the previous url/websocketUrl fields to support all RPC types
	rpcTypeURLs map[sharedtypes.RPCType]string

	// defaultURL is used for logging/display purposes only
	// CRITICAL: Do NOT use defaultURL for actual routing - always use GetURL(rpcType)
	defaultURL string

	// TODO_IMPROVE: If the same endpoint is in the session of multiple apps at the same time,
	// the first app will be chosen. A randomization among the apps in this (unlikely) scenario
	// may be needed.
	// session is the active session corresponding to the app, of which the endpoint is a member.
	session sessiontypes.Session
}

// IsFallback returns false for protocol endpoints.
func (e protocolEndpoint) IsFallback() bool {
	return false
}

// TODO_MVP(@adshmh): replace EndpointAddr with a URL; a single URL should be treated the same regardless of the app to which it is attached.
// For protocol-level concerns: the (app/session, URL) should be taken into account; e.g. a healthy endpoint may have been maxed out for a particular app.
// For QoS-level concerns: only the URL of the endpoint matters; e.g. an unhealthy endpoint should be skipped regardless of the app/session to which it is attached.
func (e protocolEndpoint) Addr() protocol.EndpointAddr {
	return protocol.EndpointAddr(fmt.Sprintf("%s-%s", e.supplier, e.defaultURL))
}

// PublicURL returns the URL of the endpoint.
// Returns defaultURL for display/logging purposes.
func (e protocolEndpoint) PublicURL() string {
	return e.defaultURL
}

// GetURL returns the RPC-type-specific URL for the endpoint.
// If the requested RPC type is not available, returns empty string.
// CRITICAL: Callers must check for empty string and skip the endpoint if not supported.
func (e protocolEndpoint) GetURL(rpcType sharedtypes.RPCType) string {
	if rpcType == sharedtypes.RPCType_UNKNOWN_RPC {
		return e.defaultURL
	}
	if url, ok := e.rpcTypeURLs[rpcType]; ok {
		return url
	}
	// RPC type not supported by this endpoint - return empty string
	return ""
}

// WebsocketURL returns the websocket URL of the endpoint.
// Deprecated: Use GetURL(sharedtypes.RPCType_WEBSOCKET) instead.
func (e protocolEndpoint) WebsocketURL() (string, error) {
	url := e.GetURL(sharedtypes.RPCType_WEBSOCKET)
	if url == "" {
		return "", fmt.Errorf("websocket URL is not set")
	}
	return url, nil
}

// Session returns a pointer to the session associated with the endpoint.
func (e protocolEndpoint) Session() *sessiontypes.Session {
	return &e.session
}

// Supplier returns the supplier address of the endpoint.
func (e protocolEndpoint) Supplier() string {
	return e.supplier
}

// endpointsFromSession returns the list of all endpoints from a Shannon session.
// It returns a map for efficient lookup, as the main/only consumer of this function uses
// the return value for selecting an endpoint for sending a relay.
func endpointsFromSession(
	session sessiontypes.Session,
	// TODO_TECHDEBT(@adshmh): Refactor load testing logic to make it more visible.
	//
	// The only supplier allowed from the session.
	// Used in Load Testing against a single RelayMiner.
	allowedSupplierAddr string,
) (map[protocol.EndpointAddr]endpoint, error) {
	sf := sdk.SessionFilter{
		Session: &session,
	}

	// AllEndpoints will return a map of supplier address to a list of supplier endpoints.
	//
	// Each supplier address will have one or more endpoints, one per RPC-type.
	// For example, a supplier may have one endpoint for JSON-RPC and one for websocket.
	allEndpoints, err := sf.AllEndpoints()
	if err != nil {
		return nil, err
	}

	endpoints := make(map[protocol.EndpointAddr]endpoint)
	for _, supplierEndpoints := range allEndpoints {
		// All endpoints for a supplier will have the same supplier address & session,
		// so we can use the first item to set the supplier address & session.
		endpoint := protocolEndpoint{
			supplier: string(supplierEndpoints[0].Supplier()),
			// Set the session field on the endpoint for efficient lookup when sending relays.
			session:     session,
			rpcTypeURLs: make(map[sharedtypes.RPCType]string),
		}

		// Endpoint does not match the only allowed supplier.
		// Skip.
		// Used in Load Testing against a RelayMiner.
		// Makes sure the relays can be processed by the target RelayMiner by matching the supplier address.
		if allowedSupplierAddr != "" && endpoint.Supplier() != allowedSupplierAddr {
			continue
		}

		// Populate rpcTypeURLs map with all available RPC types for this supplier.
		// This replaces the previous hardcoded handling of only JSON_RPC and WEBSOCKET.
		// Now supports all RPC types: json_rpc, rest, comet_bft, websocket, grpc.
		for _, supplierRPCTypeEndpoint := range supplierEndpoints {
			rpcType := supplierRPCTypeEndpoint.RPCType()
			url := supplierRPCTypeEndpoint.Endpoint().Url

			// Skip UNKNOWN_RPC types
			if rpcType == sharedtypes.RPCType_UNKNOWN_RPC {
				continue
			}

			endpoint.rpcTypeURLs[rpcType] = url

			// Set defaultURL to first URL found (for logging/display only)
			// CRITICAL: This is NOT used for routing - GetURL(rpcType) is used instead
			if endpoint.defaultURL == "" {
				endpoint.defaultURL = url
			}
		}

		endpoints[endpoint.Addr()] = endpoint
	}

	return endpoints, nil
}
