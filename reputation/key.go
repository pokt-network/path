package reputation

import (
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/protocol"
)

// KeyBuilder creates EndpointKeys with a specific granularity.
// Different implementations group endpoints differently for scoring.
// The RPC type is always included in keys to track separate reputation
// scores for different protocols (e.g., json_rpc vs websocket) at the same endpoint.
type KeyBuilder interface {
	// BuildKey creates an EndpointKey for the given service, endpoint, and RPC type.
	// The RPC type is required to track reputation separately for different protocols.
	BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, rpcType sharedtypes.RPCType) EndpointKey
}

// NewKeyBuilder creates a KeyBuilder for the specified granularity.
// If the granularity is invalid or empty, it defaults to per-endpoint.
func NewKeyBuilder(granularity string) KeyBuilder {
	switch granularity {
	case KeyGranularityDomain:
		return &DomainKeyBuilder{}
	case KeyGranularitySupplier:
		return &SupplierKeyBuilder{}
	default:
		// Default to per-endpoint (finest granularity)
		return &EndpointKeyBuilder{}
	}
}

// EndpointKeyBuilder creates keys with per-endpoint granularity.
// Each endpoint URL is scored separately, with separate scores per RPC type.
// Key format: serviceID:supplierAddr-endpointURL:rpcType
type EndpointKeyBuilder struct{}

// BuildKey creates a key using the full endpoint address and RPC type.
func (b *EndpointKeyBuilder) BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, rpcType sharedtypes.RPCType) EndpointKey {
	return NewEndpointKey(serviceID, endpointAddr, rpcType)
}

// DomainKeyBuilder creates keys with per-domain granularity.
// All endpoints from the same hosting domain share a score, tracked per RPC type.
// Key format: serviceID:domain:rpcType (e.g., eth:nodefleet.net:json_rpc)
type DomainKeyBuilder struct{}

// BuildKey creates a key using the domain extracted from the endpoint URL and RPC type.
// If the domain cannot be extracted, falls back to full endpoint address.
// The RPC type is always included to track separate scores for different protocols.
func (b *DomainKeyBuilder) BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, rpcType sharedtypes.RPCType) EndpointKey {
	// Get URL from endpoint address (format: supplierAddr-URL)
	endpointURL, err := endpointAddr.GetURL()
	if err != nil {
		// Fallback to full endpoint address if URL extraction fails
		return NewEndpointKey(serviceID, endpointAddr, rpcType)
	}

	// Extract domain from URL (e.g., nodefleet.net from https://rm-01.eu.nodefleet.net)
	domain, err := shannonmetrics.ExtractDomainOrHost(endpointURL)
	if err != nil {
		// Fallback to full endpoint address if domain extraction fails
		return NewEndpointKey(serviceID, endpointAddr, rpcType)
	}

	return NewEndpointKey(serviceID, protocol.EndpointAddr(domain), rpcType)
}

// SupplierKeyBuilder creates keys with per-supplier granularity.
// All endpoint URLs from the same supplier share a score, tracked per RPC type.
// Key format: serviceID:supplierAddr:rpcType
type SupplierKeyBuilder struct{}

// BuildKey creates a key using only the supplier address and RPC type.
// If the endpoint address cannot be parsed, falls back to full address.
// The RPC type is always included to track separate scores for different protocols.
func (b *SupplierKeyBuilder) BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, rpcType sharedtypes.RPCType) EndpointKey {
	supplierAddr, err := endpointAddr.GetAddress()
	if err != nil {
		// Fallback to full endpoint address if parsing fails
		return NewEndpointKey(serviceID, endpointAddr, rpcType)
	}
	return NewEndpointKey(serviceID, protocol.EndpointAddr(supplierAddr), rpcType)
}
