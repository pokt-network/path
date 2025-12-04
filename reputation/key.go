package reputation

import (
	"github.com/pokt-network/path/protocol"

	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
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
// Each endpoint URL is scored separately.
// Key format: serviceID:supplierAddr-endpointURL
type EndpointKeyBuilder struct{}

// BuildKey creates a key using the full endpoint address.
func (b *EndpointKeyBuilder) BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr) EndpointKey {
	return NewEndpointKey(serviceID, endpointAddr)
}

// DomainKeyBuilder creates keys with per-domain granularity.
// All endpoints from the same hosting domain share a score.
// Key format: serviceID:domain (e.g., eth:nodefleet.net)
type DomainKeyBuilder struct{}

// BuildKey creates a key using the domain extracted from the endpoint URL.
// If the domain cannot be extracted, falls back to full endpoint address.
func (b *DomainKeyBuilder) BuildKey(serviceID protocol.ServiceID, endpointAddr protocol.EndpointAddr) EndpointKey {
	// Get URL from endpoint address (format: supplierAddr-URL)
	endpointURL, err := endpointAddr.GetURL()
	if err != nil {
		// Fallback to full endpoint address if URL extraction fails
		return NewEndpointKey(serviceID, endpointAddr)
	}

	// Extract domain from URL (e.g., nodefleet.net from https://rm-01.eu.nodefleet.net)
	domain, err := shannonmetrics.ExtractDomainOrHost(endpointURL)
	if err != nil {
		// Fallback to full endpoint address if domain extraction fails
		return NewEndpointKey(serviceID, endpointAddr)
	}

	return NewEndpointKey(serviceID, protocol.EndpointAddr(domain))
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
