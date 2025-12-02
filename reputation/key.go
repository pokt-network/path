package reputation

import (
	"github.com/pokt-network/path/protocol"
)

// KeyBuilder creates EndpointKeys with a specific granularity.
// Different implementations group endpoints differently for scoring.
type KeyBuilder interface {
	// BuildKey creates an EndpointKey for the given service and endpoint.
	BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr) EndpointKey
}

// NewKeyBuilder creates a KeyBuilder for the specified granularity.
// If the granularity is invalid or empty, it defaults to per-endpoint.
func NewKeyBuilder(granularity string) KeyBuilder {
	switch granularity {
	case KeyGranularitySupplier:
		return &SupplierKeyBuilder{}
	case KeyGranularityService:
		return &ServiceKeyBuilder{}
	default:
		// Default to per-endpoint (finest granularity)
		return &EndpointKeyBuilder{}
	}
}

// EndpointKeyBuilder creates keys with per-endpoint granularity.
// Each endpoint URL is scored separately.
// Key format: serviceID:supplierAddr-endpointURL
type EndpointKeyBuilder struct{}

// BuildKey creates a key using the full endpoint address.
func (b *EndpointKeyBuilder) BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr) EndpointKey {
	return NewEndpointKey(serviceID, endpointAddr)
}

// SupplierKeyBuilder creates keys with per-supplier granularity.
// All endpoint URLs from the same supplier share a score.
// Key format: serviceID:supplierAddr
type SupplierKeyBuilder struct{}

// BuildKey creates a key using only the supplier address.
// If the endpoint address cannot be parsed, falls back to full address.
func (b *SupplierKeyBuilder) BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr) EndpointKey {
	supplierAddr, err := endpointAddr.GetAddress()
	if err != nil {
		// Fallback to full endpoint address if parsing fails
		return NewEndpointKey(serviceID, endpointAddr)
	}
	return NewEndpointKey(serviceID, protocol.EndpointAddr(supplierAddr))
}

// ServiceKeyBuilder creates keys with per-service granularity.
// All endpoints for a service share a single score.
// Key format: serviceID:
type ServiceKeyBuilder struct{}

// BuildKey creates a key using only the service ID.
// The endpoint address is set to empty, so all endpoints share the same key.
func (b *ServiceKeyBuilder) BuildKey(serviceID protocol.ServiceID, _ protocol.EndpointAddr) EndpointKey {
	return NewEndpointKey(serviceID, "")
}
