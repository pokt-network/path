package reputation

import (
	"context"
	"strings"
	"time"

	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	"github.com/pokt-network/path/protocol"
)

// DrainRequest asks that every scored endpoint belonging to one operator (eTLD+1) be
// benched for a service, by writing a cooldown expiry onto its existing score.
//
// Why a cooldown rather than a score change: selection already excludes endpoints in
// cooldown regardless of score, so this reuses a filter that is guaranteed to be
// consulted everywhere selection happens. Writing the score down instead would (a) be
// undone by the next successful health check and (b) corrupt the quality signal we are
// usually trying to READ while a drain is in effect.
type DrainRequest struct {
	ServiceID protocol.ServiceID

	// Domain is the eTLD+1 to bench (e.g. "example.com"). Required.
	Domain string

	// Duration is how long to bench for. Zero RELEASES a drain this process applied
	// (see DrainDomain for why that is narrower than "clear all cooldowns").
	Duration time.Duration

	// RPCType optionally narrows the drain to one protocol ("websocket", "json_rpc", …).
	// Empty means every RPC type for the service.
	RPCType string

	// DryRun reports what would be benched without writing anything.
	DryRun bool
}

// DrainResult reports what a DrainRequest matched.
type DrainResult struct {
	ServiceID string `json:"service_id"`
	Domain    string `json:"domain"`
	RPCType   string `json:"rpc_type,omitempty"`

	// Matched is how many scored endpoints belong to the requested domain.
	Matched int `json:"matched"`
	// Drained is how many were actually written (equals Matched unless DryRun).
	Drained int `json:"drained"`
	// Released is how many drains this call lifted (Duration == 0 only).
	Released int `json:"released"`

	// DrainedUntil is when the bench expires. Absent on a release or a dry run.
	DrainedUntil string `json:"drained_until,omitempty"`

	// DomainsSeen lists every domain that has a score for this service, so a typo in
	// Domain shows up as "matched 0, and here is what actually exists" rather than a
	// silent no-op that reads like a successful drain.
	DomainsSeen []string `json:"domains_seen"`

	// UnscoredWarning is set when the drain cannot be complete — see DrainDomain.
	UnscoredWarning string `json:"unscored_warning,omitempty"`

	DryRun bool `json:"dry_run"`
}

// DrainDomain benches every scored endpoint of one operator for a service.
//
// IMPORTANT LIMITATION — this can only bench endpoints that already carry a score,
// because the cooldown lives ON the score and the reputation service only knows about
// endpoints it has observed. An endpoint that has never been scored is treated by
// selection as "initial score, not in cooldown" and therefore stays selectable through a
// drain. In practice every endpoint on a service with health checks running is scored, so
// this is usually complete; when it is not, the result carries UnscoredWarning and the
// caller should not read a drain as airtight.
//
// The drain deliberately does NOT touch Value, CriticalStrikes or RecentCriticalRate. It
// is a routing instrument, not a penalty: reputation must still mean "how well did this
// endpoint serve", or a drain would poison the very measurement it exists to enable.
//
// Releasing (Duration == 0) only lifts cooldowns THIS process wrote and that nothing has
// overwritten since. A genuine cooldown earned while the drain was in effect is left
// alone — otherwise "undo my experiment" would silently un-bench a legitimately failing
// endpoint, which is the one outcome an operator would never intend.
func (s *service) DrainDomain(ctx context.Context, req DrainRequest) DrainResult {
	result := DrainResult{
		ServiceID:   string(req.ServiceID),
		Domain:      req.Domain,
		RPCType:     req.RPCType,
		DryRun:      req.DryRun,
		DomainsSeen: []string{},
	}

	if !s.config.Enabled || req.Domain == "" {
		return result
	}

	wantRPC := strings.ToLower(strings.TrimSpace(req.RPCType))
	now := time.Now()
	until := now.Add(req.Duration)
	releasing := req.Duration <= 0

	type pending struct {
		key   EndpointKey
		score Score
	}
	var writes []pending
	seen := map[string]struct{}{}

	s.mu.Lock()
	for key, score := range s.cache {
		if key.ServiceID != req.ServiceID {
			continue
		}

		domain := domainForKey(key)
		if domain == "" {
			continue
		}
		seen[domain] = struct{}{}

		if domain != req.Domain {
			continue
		}
		if wantRPC != "" && strings.ToLower(key.RPCType.String()) != wantRPC {
			continue
		}
		result.Matched++

		if releasing {
			// Only lift what this process benched and nothing has re-written since.
			applied, ok := s.drainedKeys[key]
			if !ok || !score.CooldownUntil.Equal(applied) {
				continue
			}
			result.Released++
			if req.DryRun {
				continue
			}
			score.CooldownUntil = time.Time{}
			delete(s.drainedKeys, key)
			s.setScoreLocked(key, score)
			writes = append(writes, pending{key, score})
			continue
		}

		if req.DryRun {
			result.Drained++
			continue
		}
		score.CooldownUntil = until
		if s.drainedKeys == nil {
			s.drainedKeys = map[EndpointKey]time.Time{}
		}
		s.drainedKeys[key] = until
		s.setScoreLocked(key, score)
		writes = append(writes, pending{key, score})
		result.Drained++
	}
	s.mu.Unlock()

	for d := range seen {
		result.DomainsSeen = append(result.DomainsSeen, d)
	}

	// Queue async writes so the bench survives this pod's next storage refresh. Best
	// effort by design: the local cache is already authoritative for selection, and a
	// dropped write only means the bench is pod-local, which is what the other admin
	// endpoints are anyway.
	for _, w := range writes {
		select {
		case s.writeCh <- writeRequest{key: w.key, score: w.score}:
		default:
		}
	}

	if !releasing && !req.DryRun && result.Drained > 0 {
		result.DrainedUntil = until.UTC().Format(time.RFC3339)
	}
	if result.Matched == 0 {
		result.UnscoredWarning = "no scored endpoints matched; unscored endpoints are NOT benched by a drain and stay selectable"
	}

	if s.logger == nil {
		return result
	}
	s.logger.Warn().
		Str("service_id", string(req.ServiceID)).
		Str("domain", req.Domain).
		Str("rpc_type", req.RPCType).
		Dur("duration", req.Duration).
		Bool("dry_run", req.DryRun).
		Int("matched", result.Matched).
		Int("drained", result.Drained).
		Int("released", result.Released).
		Msg("⚠️ admin reputation drain applied")

	return result
}

// domainForKey recovers the eTLD+1 for a reputation key. Keys carry different identifiers
// depending on the configured granularity (endpoint address, bare URL, domain, or supplier
// address), so this tries the richer forms first and falls back to treating the identifier
// as already being a host. A supplier-granularity key has no URL in it at all and yields
// "", which is why a drain is documented as domain-granularity-dependent.
func domainForKey(key EndpointKey) string {
	if url, err := key.EndpointAddr.GetURL(); err == nil {
		if domain, err := shannonmetrics.ExtractDomainOrHost(url); err == nil {
			return domain
		}
	}
	if domain, err := shannonmetrics.ExtractDomainOrHost(string(key.EndpointAddr)); err == nil {
		return domain
	}
	return ""
}
