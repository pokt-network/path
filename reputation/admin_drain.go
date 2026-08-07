package reputation

import (
	"context"
	"strings"
	"time"

	"github.com/pokt-network/path/protocol"
)

// DrainRequest asks that a set of endpoints be temporarily benched for a service.
//
// Endpoints are named by Identifiers, which the CALLER resolves from something human —
// an operator domain, a URL — into the concrete strings a reputation key can carry. That
// resolution cannot happen here: key granularity is per-service configuration (endpoint
// address / URL / domain / supplier address), so the same operator is a hostname on one
// service and a pokt1… supplier address on another, and only the protocol layer knows the
// supplier→URL mapping needed to bridge them.
type DrainRequest struct {
	ServiceID protocol.ServiceID

	// Identifiers is the set of reputation-key identifiers to bench. A key matches if its
	// EndpointAddr equals any entry. Required — an empty set benches nothing, deliberately,
	// so a failed resolution can never widen into "drain the whole service".
	Identifiers []string

	// Label is what the caller asked for (e.g. "spacebelt.xyz"), carried through to the
	// response and logs only. Never used for matching.
	Label string

	// Duration is how long to bench for. Zero RELEASES any drain this pod applied.
	Duration time.Duration

	// RPCType optionally narrows to one protocol ("websocket", "json_rpc", …).
	RPCType string

	// DryRun reports what would be benched without changing anything.
	DryRun bool
}

// DrainResult reports what a DrainRequest matched.
type DrainResult struct {
	ServiceID string `json:"service_id"`
	Label     string `json:"label,omitempty"`
	RPCType   string `json:"rpc_type,omitempty"`

	// Identifiers is how many key identifiers the caller resolved. Zero means resolution
	// failed upstream, which is a different problem from "resolved fine, matched nothing".
	Identifiers int `json:"identifiers_resolved"`

	// MatchedEndpoints is how many live endpoints the caller's target resolved to, set by
	// the caller. Distinguishes "the target names nothing" from "it names endpoints that
	// carry no reputation score yet".
	MatchedEndpoints int `json:"matched_endpoints"`

	// Matched is how many scored endpoints those identifiers hit.
	Matched int `json:"matched"`
	// Drained is how many were benched (equals Matched unless DryRun).
	Drained int `json:"drained"`
	// Released is how many drains this call lifted (Duration == 0 only).
	Released int `json:"released"`

	// DrainedUntil is when the bench expires. Absent on a release or dry run.
	DrainedUntil string `json:"drained_until,omitempty"`

	// Warning is set when the result needs reading with care.
	Warning string `json:"warning,omitempty"`

	// PropagationError is set when the drain could not be written to shared storage, which
	// means it applied to THIS POD ONLY.
	PropagationError string `json:"propagation_error,omitempty"`

	DryRun bool `json:"dry_run"`
}

// DrainDomain benches the endpoints named by req.Identifiers for a service.
//
// The bench is held in s.drainedKeys and applied as an OVERLAY at read time — it never
// touches the stored Score. That is deliberate and load-bearing: an earlier version wrote
// CooldownUntil onto the score, and refreshFromStorage, which unconditionally overwrites
// the local cache from Redis, erased every drain within a refresh cycle. The endpoint
// still reported drained=N, so it looked like it was working while benching nothing. Any
// state that must outlive a storage refresh cannot live on the score.
//
// Because the drain is an overlay, releasing it can never disturb a cooldown the endpoint
// earned on its own: those live on the score, which the drain has not modified.
//
// NOT a penalty. Value, CriticalStrikes and RecentCriticalRate are untouched, so the
// quality signal stays readable while the drain is in effect — reading it is usually the
// entire point of draining.
//
// LIMITATION: only endpoints that already carry a score can be benched, because the
// overlay is keyed by EndpointKey and unscored endpoints have none. Selection treats an
// unscored endpoint as "initial score, not in cooldown", so it stays selectable. On a
// service with health checks running everything is scored; when it is not, Warning says so.
//
// Per-pod, in-memory, expires on its own, does not survive a restart.
func (s *service) DrainDomain(ctx context.Context, req DrainRequest) DrainResult {
	result := DrainResult{
		ServiceID:   string(req.ServiceID),
		Label:       req.Label,
		RPCType:     req.RPCType,
		Identifiers: len(req.Identifiers),
		DryRun:      req.DryRun,
	}

	if !s.config.Enabled {
		result.Warning = "reputation is disabled; nothing to drain"
		return result
	}
	if len(req.Identifiers) == 0 {
		result.Warning = "no identifiers resolved; nothing was benched"
		return result
	}

	want := make(map[string]struct{}, len(req.Identifiers))
	for _, id := range req.Identifiers {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = struct{}{}
		}
	}
	wantRPC := strings.ToLower(strings.TrimSpace(req.RPCType))

	now := time.Now()
	until := now.Add(req.Duration)
	releasing := req.Duration <= 0

	// Keys mutated locally, replayed to shared storage after the lock is released — a
	// storage round trip per key must not be held under the reputation mutex, which every
	// request path contends on.
	var touched []EndpointKey

	s.mu.Lock()
	// Drop expired entries so the map cannot grow without bound across many drains.
	for k, exp := range s.drainedKeys {
		if !now.Before(exp) {
			delete(s.drainedKeys, k)
		}
	}

	for key := range s.cache {
		if key.ServiceID != req.ServiceID {
			continue
		}
		if _, ok := want[string(key.EndpointAddr)]; !ok {
			continue
		}
		if wantRPC != "" && strings.ToLower(key.RPCType.String()) != wantRPC {
			continue
		}
		result.Matched++

		if releasing {
			if _, ok := s.drainedKeys[key]; !ok {
				continue
			}
			result.Released++
			if !req.DryRun {
				delete(s.drainedKeys, key)
				touched = append(touched, key)
			}
			continue
		}

		if req.DryRun {
			result.Drained++
			continue
		}
		if s.drainedKeys == nil {
			s.drainedKeys = make(map[EndpointKey]time.Time)
		}
		s.drainedKeys[key] = until
		touched = append(touched, key)
		result.Drained++
	}
	s.mu.Unlock()

	// Propagate to shared storage so ONE admin call applies fleet-wide. Without this a
	// drain is pod-local, and an operator has to hit every replica to bench anything —
	// which in practice means a partial drain nobody realises is partial.
	//
	// Applied after the local mutation so this pod is correct even if storage is down; the
	// result then says so rather than implying a fleet-wide bench that did not happen.
	if !req.DryRun && len(touched) > 0 {
		for _, key := range touched {
			var err error
			if releasing {
				err = s.storage.DeleteDrain(ctx, key)
			} else {
				err = s.storage.SetDrain(ctx, key, until)
			}
			if err != nil {
				result.PropagationError = err.Error()
				result.Warning = "drain applied to THIS POD ONLY: shared storage write failed, other replicas are unaffected"
				break
			}
		}
	}

	if !releasing && !req.DryRun && result.Drained > 0 {
		result.DrainedUntil = until.UTC().Format(time.RFC3339)
	}
	if result.Matched == 0 {
		result.Warning = "identifiers resolved but matched no scored endpoints; unscored endpoints are NOT benched and stay selectable"
	}

	if s.logger != nil {
		s.logger.Warn().
			Str("service_id", string(req.ServiceID)).
			Str("label", req.Label).
			Str("rpc_type", req.RPCType).
			Dur("duration", req.Duration).
			Bool("dry_run", req.DryRun).
			Int("identifiers", len(req.Identifiers)).
			Int("matched", result.Matched).
			Int("drained", result.Drained).
			Int("released", result.Released).
			Msg("⚠️ admin reputation drain applied")
	}

	return result
}

// drainOverlayLocked re-applies an active admin drain's cooldown on top of a score read
// from the cache. The caller MUST hold s.mu (read or write).
//
// Applied at read time rather than written into the score so a storage refresh cannot
// erase it — see DrainDomain. Takes the later of the two expiries, so a drain can extend
// an earned cooldown but never shortens one.
func (s *service) drainOverlayLocked(key EndpointKey, score Score) Score {
	until, ok := s.drainedKeys[key]
	if !ok || !time.Now().Before(until) {
		return score
	}
	if until.After(score.CooldownUntil) {
		score.CooldownUntil = until
	}
	return score
}
