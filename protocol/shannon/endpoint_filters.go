package shannon

import (
	"strings"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
)

// This file holds the per-session endpoint filtering stages used by
// getSessionsUniqueEndpoints. Each stage mutates the working endpoint map in
// place (via delete) instead of allocating a fresh map and copying survivors,
// which removes several map allocations per session on the relay hot path.
//
// Deleting the current key from a map during a range loop is well-defined in Go.
// The caller is responsible for passing a map it owns (never the cached
// session-endpoints map), so these filters cannot corrupt shared state.

// removeBlockedSuppliers deletes endpoints whose supplier address appears in
// blockedSuppliers (the service's config-driven blocklist). Returns the number
// of endpoints removed.
func removeBlockedSuppliers(
	endpoints map[protocol.EndpointAddr]endpoint,
	blockedSuppliers []string,
	logger polylog.Logger,
) int {
	if len(blockedSuppliers) == 0 {
		return 0
	}

	blockedSet := make(map[string]struct{}, len(blockedSuppliers))
	for _, addr := range blockedSuppliers {
		blockedSet[addr] = struct{}{}
	}

	removed := 0
	for addr, ep := range endpoints {
		supplierAddr := ep.Supplier()
		if _, isBlocked := blockedSet[supplierAddr]; isBlocked {
			delete(endpoints, addr)
			removed++
			logger.Debug().
				Str("supplier", supplierAddr).
				Str("endpoint", string(addr)).
				Msg("Skipping config-blocked supplier")
		}
	}
	return removed
}

// retainAllowedSuppliers deletes endpoints whose supplier is not in
// allowedSuppliers, always keeping requestedEndpointAddr. Returns the number of
// endpoints removed. The supplier address is taken from the "supplier-url"
// endpoint address prefix to preserve the original matching semantics.
func retainAllowedSuppliers(
	endpoints map[protocol.EndpointAddr]endpoint,
	allowedSuppliers []string,
	requestedEndpointAddr protocol.EndpointAddr,
	logger polylog.Logger,
) int {
	allowedSet := make(map[string]struct{}, len(allowedSuppliers))
	for _, s := range allowedSuppliers {
		allowedSet[s] = struct{}{}
	}

	removed := 0
	for addr := range endpoints {
		// Extract supplier address from endpoint address (format: "supplierAddr-url").
		supplierAddr := string(addr)
		if dashIndex := strings.Index(supplierAddr, "-"); dashIndex > 0 {
			supplierAddr = supplierAddr[:dashIndex]
		}

		if _, allowed := allowedSet[supplierAddr]; allowed || addr == requestedEndpointAddr {
			continue
		}

		delete(endpoints, addr)
		removed++
		logger.Debug().
			Str("supplier", supplierAddr).
			Str("endpoint", string(addr)).
			Msg("Skipping endpoint - supplier not in allowed list")
	}
	return removed
}

// removeBlacklistedSuppliers deletes endpoints whose supplier is blacklisted
// (per isBlacklisted), always keeping requestedEndpointAddr. Returns the number
// of endpoints removed.
func removeBlacklistedSuppliers(
	endpoints map[protocol.EndpointAddr]endpoint,
	requestedEndpointAddr protocol.EndpointAddr,
	isBlacklisted func(supplierAddr string) bool,
	logger polylog.Logger,
) int {
	removed := 0
	for addr, ep := range endpoints {
		supplierAddr := ep.Supplier()
		if isBlacklisted(supplierAddr) && addr != requestedEndpointAddr {
			delete(endpoints, addr)
			removed++
			logger.Debug().
				Str("supplier", supplierAddr).
				Str("endpoint", string(addr)).
				Msg("Skipping blacklisted supplier")
		}
	}
	return removed
}

// filterExhaustedSuppliers removes endpoints whose supplier has exhausted the
// application's per-session stake budget (per isExhausted), always keeping
// requestedEndpointAddr.
//
// Safety net: if every endpoint would be removed, none are — exhausted suppliers
// are kept as a last resort so the request still has somewhere to go (a supplier
// may still serve relays as goodwill). In that case applied is false.
//
// Returns skipped (how many qualify as exhausted) and applied (whether the
// removals were actually performed).
func filterExhaustedSuppliers(
	endpoints map[protocol.EndpointAddr]endpoint,
	requestedEndpointAddr protocol.EndpointAddr,
	isExhausted func(ep endpoint) bool,
	logger polylog.Logger,
) (skipped int, applied bool) {
	// Collect removals first so the safety net can decide whether applying them
	// would empty the pool.
	var toRemove []protocol.EndpointAddr
	for addr, ep := range endpoints {
		if addr == requestedEndpointAddr {
			continue
		}
		if isExhausted(ep) {
			toRemove = append(toRemove, addr)
		}
	}

	skipped = len(toRemove)
	if skipped == 0 {
		return 0, false
	}
	// All endpoints are exhausted: keep them rather than corner ourselves into
	// an empty pool.
	if skipped == len(endpoints) {
		return skipped, false
	}

	for _, addr := range toRemove {
		logger.Debug().
			Str("supplier", endpoints[addr].Supplier()).
			Str("endpoint", string(addr)).
			Msg("Skipping exhausted (over-serviced) supplier for this session")
		delete(endpoints, addr)
	}
	return skipped, true
}
