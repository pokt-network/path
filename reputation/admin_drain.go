package reputation

import (
	"context"
	"strings"
	"time"

	"github.com/pokt-network/path/protocol"
)

// DrainKey identifies a benched operator for one service and RPC type.
//
// The drain is stored as this PREDICATE rather than as a resolved set of EndpointKeys, and
// that is the whole design. An earlier version resolved the target to concrete keys once and
// benched those: EndpointAddr is `supplierAddr-url` and the supplier set in a session rotates
// every rollover (~20 min), so within one session the benched keys went stale, the live
// endpoints at that operator had never been benched, and selection picked them freely — while
// the metric kept reporting the stale count as if the bench held. A drain that expires
// silently at the next rollover is worse than no drain.
//
// Matching happens against the endpoint's live URL at selection time, so an endpoint rotated
// into the session at a drained operator is benched the moment it appears.
type DrainKey struct {
	ServiceID protocol.ServiceID
	// Domain is the registrable domain (eTLD+1), lowercased.
	Domain string
	// RPCType is the lowercase wire form ("websocket", "json_rpc", …). Empty means every
	// RPC type for the service.
	RPCType string
}

// DrainRequest benches one operator for a service.
type DrainRequest struct {
	ServiceID protocol.ServiceID

	// Domain is the operator's registrable domain (eTLD+1). Required — an empty domain
	// would bench the whole service, which is never what anyone meant to type.
	Domain string

	// Duration is how long to bench for. Zero RELEASES an existing drain.
	Duration time.Duration

	// RPCType optionally narrows to one protocol. Empty means all.
	RPCType string

	// DryRun reports what would happen without changing anything.
	DryRun bool
}

// DrainResult reports the outcome.
type DrainResult struct {
	ServiceID string `json:"service_id"`
	Domain    string `json:"domain"`
	RPCType   string `json:"rpc_type,omitempty"`

	Applied  bool `json:"applied"`
	Released bool `json:"released"`

	// MatchedEndpoints is how many live endpoints the target names, filled in by the
	// caller. Zero means the domain matches nothing currently in session — the drain still
	// applies (endpoints rotate in and out), but it is worth seeing.
	MatchedEndpoints int `json:"matched_endpoints"`

	// DrainedUntil is when the bench expires. Absent on a release or dry run.
	DrainedUntil string `json:"drained_until,omitempty"`

	// ActiveDrains lists every drain in force for this service after the call, so the
	// caller can see the whole picture rather than just the change they made.
	ActiveDrains []string `json:"active_drains"`

	// PropagationError is set when the drain could not be written to shared storage,
	// meaning it applied to THIS POD ONLY.
	PropagationError string `json:"propagation_error,omitempty"`

	DryRun bool `json:"dry_run"`
}

// DrainDomain benches (or releases) one operator for a service.
//
// NOT a penalty: no Score field is touched, so the quality signal stays readable while the
// drain is in effect — reading it is usually the point of draining. Selection consults the
// drain separately, via IsDomainDrained.
//
// Per-service, expires on its own, and propagated to every replica through shared storage.
func (s *service) DrainDomain(ctx context.Context, req DrainRequest) DrainResult {
	domain := strings.ToLower(strings.TrimSpace(req.Domain))
	rpcType := strings.ToLower(strings.TrimSpace(req.RPCType))

	result := DrainResult{
		ServiceID: string(req.ServiceID),
		Domain:    domain,
		RPCType:   rpcType,
		DryRun:    req.DryRun,
	}

	if !s.config.Enabled || domain == "" {
		result.ActiveDrains = s.activeDrainsFor(req.ServiceID)
		return result
	}

	key := DrainKey{ServiceID: req.ServiceID, Domain: domain, RPCType: rpcType}
	releasing := req.Duration <= 0
	until := time.Now().Add(req.Duration)

	if !req.DryRun {
		s.mu.Lock()
		if s.drainedDomains == nil {
			s.drainedDomains = make(map[DrainKey]time.Time)
		}
		now := time.Now()
		for k, exp := range s.drainedDomains {
			if !now.Before(exp) {
				delete(s.drainedDomains, k)
			}
		}
		if releasing {
			delete(s.drainedDomains, key)
		} else {
			s.drainedDomains[key] = until
		}
		s.mu.Unlock()

		// Propagate so ONE call benches the fleet. Applied after the local mutation so this
		// pod is correct even if storage is down, with the result saying so.
		var err error
		if releasing {
			err = s.storage.DeleteDrain(ctx, key)
		} else {
			err = s.storage.SetDrain(ctx, key, until)
		}
		if err != nil {
			result.PropagationError = err.Error()
		}
	}

	result.Applied = !releasing
	result.Released = releasing
	if !releasing && !req.DryRun {
		result.DrainedUntil = until.UTC().Format(time.RFC3339)
	}
	result.ActiveDrains = s.activeDrainsFor(req.ServiceID)

	if s.logger != nil {
		s.logger.Warn().
			Str("service_id", string(req.ServiceID)).
			Str("domain", domain).
			Str("rpc_type", rpcType).
			Dur("duration", req.Duration).
			Bool("dry_run", req.DryRun).
			Bool("released", releasing).
			Msg("⚠️ admin reputation drain changed")
	}

	return result
}

// IsDomainDrained reports whether an operator is benched for this service and RPC type.
//
// Called from selection with the domain derived from the endpoint's LIVE URL, which is what
// makes the bench survive session rotation: it never depends on which supplier addresses
// happen to be in the current session.
//
// A drain with an empty RPCType covers every RPC type for the service.
func (s *service) IsDomainDrained(serviceID protocol.ServiceID, domain, rpcType string) bool {
	if domain == "" {
		return false
	}
	domain = strings.ToLower(domain)
	rpcType = strings.ToLower(rpcType)

	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.drainedDomains) == 0 {
		return false
	}

	now := time.Now()
	for _, k := range [2]DrainKey{
		{ServiceID: serviceID, Domain: domain, RPCType: rpcType},
		{ServiceID: serviceID, Domain: domain, RPCType: ""},
	} {
		if until, ok := s.drainedDomains[k]; ok && now.Before(until) {
			return true
		}
	}
	return false
}

// activeDrainsFor lists the drains in force for a service, newest expiry last.
func (s *service) activeDrainsFor(serviceID protocol.ServiceID) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	out := make([]string, 0, len(s.drainedDomains))
	for k, until := range s.drainedDomains {
		if k.ServiceID != serviceID || !now.Before(until) {
			continue
		}
		rpc := k.RPCType
		if rpc == "" {
			rpc = "all"
		}
		out = append(out, k.Domain+" ("+rpc+") until "+until.UTC().Format(time.RFC3339))
	}
	return out
}

// refreshDrains replaces the local drain set with the one in shared storage.
//
// REPLACE, not merge: a release issued on another replica shows up as the drain being absent
// from storage, and merging would keep benching it here forever. A storage failure leaves the
// local set untouched rather than clearing it — losing Redis mid-incident must not silently
// un-bench everything.
func (s *service) refreshDrains(ctx context.Context) {
	drains, err := s.storage.ListDrains(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn().Err(err).Msg("failed to refresh admin drains; keeping the local set")
		}
		return
	}

	s.mu.Lock()
	s.drainedDomains = drains
	s.mu.Unlock()
}
