package shannon

import (
	"sync"
	"time"

	"github.com/pokt-network/path/protocol"
)

// supplierBlacklist tracks suppliers that should be temporarily excluded from selection
// due to validation/signature errors. These errors are supplier-specific and should not
// penalize the domain's reputation.
//
// The blacklist is session-scoped: entries expire after a configurable duration or
// when the session changes.
type supplierBlacklist struct {
	mu sync.RWMutex

	// entries maps (serviceID, supplierAddr) -> blacklist entry
	entries map[blacklistKey]*blacklistEntry

	// defaultTTL is how long a supplier stays blacklisted (default: session duration)
	defaultTTL time.Duration
}

type blacklistKey struct {
	serviceID    protocol.ServiceID
	supplierAddr string
}

type blacklistEntry struct {
	reason    string
	timestamp time.Time
	expiresAt time.Time
}

// defaultBlacklistTTL is the default time a supplier stays blacklisted.
// Set to 15 minutes to balance protection against bad suppliers while
// allowing faster recovery for transient validation errors.
const defaultBlacklistTTL = 15 * time.Minute

// newSupplierBlacklist creates a new supplier blacklist.
func newSupplierBlacklist() *supplierBlacklist {
	return &supplierBlacklist{
		entries:    make(map[blacklistKey]*blacklistEntry),
		defaultTTL: defaultBlacklistTTL,
	}
}

// Blacklist adds a supplier to the blacklist for a specific service.
// This is called when a supplier returns a validation/signature error.
func (sb *supplierBlacklist) Blacklist(serviceID protocol.ServiceID, supplierAddr, reason string) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	key := blacklistKey{serviceID: serviceID, supplierAddr: supplierAddr}
	now := time.Now()

	sb.entries[key] = &blacklistEntry{
		reason:    reason,
		timestamp: now,
		expiresAt: now.Add(sb.defaultTTL),
	}
}

// IsBlacklisted checks if a supplier is currently blacklisted for a service.
func (sb *supplierBlacklist) IsBlacklisted(serviceID protocol.ServiceID, supplierAddr string) bool {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	key := blacklistKey{serviceID: serviceID, supplierAddr: supplierAddr}
	entry, exists := sb.entries[key]
	if !exists {
		return false
	}

	// Check if entry has expired
	if time.Now().After(entry.expiresAt) {
		return false
	}

	return true
}

// GetBlacklistReason returns the reason a supplier was blacklisted, if blacklisted.
func (sb *supplierBlacklist) GetBlacklistReason(serviceID protocol.ServiceID, supplierAddr string) (string, bool) {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	key := blacklistKey{serviceID: serviceID, supplierAddr: supplierAddr}
	entry, exists := sb.entries[key]
	if !exists || time.Now().After(entry.expiresAt) {
		return "", false
	}

	return entry.reason, true
}

// Cleanup removes expired entries from the blacklist.
// Called periodically to prevent memory growth.
func (sb *supplierBlacklist) Cleanup() {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	now := time.Now()
	for key, entry := range sb.entries {
		if now.After(entry.expiresAt) {
			delete(sb.entries, key)
		}
	}
}

// Count returns the number of currently blacklisted suppliers (for metrics/debugging).
func (sb *supplierBlacklist) Count() int {
	sb.mu.RLock()
	defer sb.mu.RUnlock()

	count := 0
	now := time.Now()
	for _, entry := range sb.entries {
		if !now.After(entry.expiresAt) {
			count++
		}
	}
	return count
}

// ClearForService removes all blacklist entries for a specific service.
// Called when a new session starts for the service.
func (sb *supplierBlacklist) ClearForService(serviceID protocol.ServiceID) {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	for key := range sb.entries {
		if key.serviceID == serviceID {
			delete(sb.entries, key)
		}
	}
}

// Unblacklist removes a supplier from the blacklist.
// Called when a health check succeeds for a previously blacklisted supplier.
func (sb *supplierBlacklist) Unblacklist(serviceID protocol.ServiceID, supplierAddr string) bool {
	sb.mu.Lock()
	defer sb.mu.Unlock()

	key := blacklistKey{serviceID: serviceID, supplierAddr: supplierAddr}
	if _, exists := sb.entries[key]; exists {
		delete(sb.entries, key)
		return true
	}
	return false
}
