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
	case KeyGranularityURL:
		return &URLKeyBuilder{}
	case KeyGranularityDomain:
		return &DomainKeyBuilder{}
	case KeyGranularitySupplier:
		return &SupplierKeyBuilder{}
	case KeyGranularityEndpoint:
		return &EndpointKeyBuilder{}
	default:
		// Empty or unrecognized granularity falls back to the shipped default (per-URL):
		// shared-backend failures are attributed to the URL, not duplicated per supplier.
		return &URLKeyBuilder{}
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

// URLKeyBuilder creates keys with per-URL granularity.
// All suppliers that front the exact same backend URL share a score, tracked per RPC type.
// Key format: serviceID:endpointURL (e.g., eth:https://rm-01.eu.example.com)
// This drops the supplier address from the key so an exact URL match — i.e. the same
// physical backend behind multiple staked supplier addresses — is scored (and cooled) once
// rather than once per supplier. It keeps distinct URLs separate (no dilution across an
// operator's other backends, unlike per-domain).
type URLKeyBuilder struct{}

// BuildKey creates a key using the URL extracted from the endpoint address and RPC type.
// If the URL cannot be extracted, it falls back to the full endpoint address (which
// degrades safely to per-endpoint granularity for that one malformed address).
func (b *URLKeyBuilder) BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr, rpcType sharedtypes.RPCType) EndpointKey {
	endpointURL, err := endpointAddr.GetURL()
	if err != nil {
		// Fallback to full endpoint address if URL extraction fails
		return NewEndpointKey(serviceID, endpointAddr, rpcType)
	}
	return NewEndpointKey(serviceID, protocol.EndpointAddr(endpointURL), rpcType)
}

// DomainKeyBuilder creates keys with per-domain granularity.
// All endpoints from the same hosting domain share a score, tracked per RPC type.
// Key format: serviceID:domain:rpcType (e.g., eth:example.com:json_rpc)
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

	// Extract domain from URL (e.g., example.com from https://rm-01.eu.example.com)
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
