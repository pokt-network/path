package shannon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
	reputationstorage "github.com/pokt-network/path/reputation/storage"
)

// BenchmarkFilterByReputation measures the per-call cost of filterByReputation
// across the two key-builder granularities that allocate inside BuildKey, and
// compares the current implementation against the legacy one that called
// keyBuilder.BuildKey twice per endpoint.
//
// Run with:
//
//	go test -run='^$' -bench=BenchmarkFilterByReputation -benchmem ./protocol/shannon/
func BenchmarkFilterByReputation(b *testing.B) {
	for _, granularity := range []string{
		reputation.KeyGranularityEndpoint,
		reputation.KeyGranularityDomain,
	} {
		for _, n := range []int{8, 32} {
			b.Run(fmt.Sprintf("legacy/granularity=%s/endpoints=%d", granularity, n), func(b *testing.B) {
				benchFilterByReputation(b, granularity, n, true)
			})
			b.Run(fmt.Sprintf("fixed/granularity=%s/endpoints=%d", granularity, n), func(b *testing.B) {
				benchFilterByReputation(b, granularity, n, false)
			})
		}
	}
}

func benchFilterByReputation(b *testing.B, granularity string, n int, legacy bool) {
	ctx := context.Background()

	cfg := reputation.Config{
		Enabled:        true,
		InitialScore:   80,
		MinThreshold:   30,
		StorageType:    "memory",
		KeyGranularity: granularity,
	}
	cfg.HydrateDefaults()

	svc := reputation.NewService(cfg, reputationstorage.NewMemoryStorage(5*time.Minute))
	if err := svc.Start(ctx); err != nil {
		b.Fatalf("svc.Start: %v", err)
	}
	b.Cleanup(func() { _ = svc.Stop() })

	p := &Protocol{
		logger:            newSilentLogger(),
		reputationService: svc,
	}
	const serviceID = protocol.ServiceID("eth")

	endpoints := make(map[protocol.EndpointAddr]endpoint, n)
	for i := 0; i < n; i++ {
		addr := protocol.EndpointAddr(fmt.Sprintf("pokt1supplier%d-https://relay-%d.example.com:443/json-rpc", i, i))
		endpoints[addr] = &mockEndpoint{addr: addr}
	}

	logger := newSilentLogger()
	rpcType := sharedtypes.RPCType_JSON_RPC

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if legacy {
			_ = legacyFilterByReputation(p, ctx, serviceID, endpoints, rpcType, logger, "")
		} else {
			_ = p.filterByReputation(ctx, serviceID, endpoints, rpcType, logger, "")
		}
	}
}

// legacyFilterByReputation is the pre-fix implementation, kept locally so the
// benchmark above can compare old vs new. Do NOT call from production code.
// (It calls keyBuilder.BuildKey twice per endpoint and re-evaluates
// minThreshold inside the loop.)
func legacyFilterByReputation(
	p *Protocol,
	ctx context.Context,
	serviceID protocol.ServiceID,
	endpoints map[protocol.EndpointAddr]endpoint,
	rpcType sharedtypes.RPCType,
	logger polylog.Logger,
	requestedEndpointAddr protocol.EndpointAddr,
) map[protocol.EndpointAddr]endpoint {
	if p.reputationService == nil {
		return endpoints
	}

	keyBuilder := p.reputationService.KeyBuilderForService(serviceID)

	keys := make([]reputation.EndpointKey, 0, len(endpoints))
	for addr := range endpoints {
		keys = append(keys, keyBuilder.BuildKey(serviceID, addr, rpcType))
	}

	scores, err := p.reputationService.GetScores(ctx, keys)
	if err != nil {
		return endpoints
	}

	filtered := make(map[protocol.EndpointAddr]endpoint, len(endpoints))
	for addr, ep := range endpoints {
		key := keyBuilder.BuildKey(serviceID, addr, rpcType)
		score, exists := scores[key]
		if !exists {
			filtered[addr] = ep
			continue
		}
		if score.IsInCooldown() {
			if addr == requestedEndpointAddr {
				filtered[addr] = ep
			}
			continue
		}
		minThreshold := p.getMinThresholdForService(serviceID)
		if score.Value >= minThreshold || addr == requestedEndpointAddr {
			filtered[addr] = ep
		}
	}

	return filtered
}
