package reputation

import (
	"context"
	"strconv"
	"testing"
	"time"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

// oldGetArchivalScan reproduces the previous full-global-cache scan, kept here
// only as a benchmark baseline.
func oldGetArchivalScan(s *service, serviceID protocol.ServiceID) []EndpointKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []EndpointKey
	for key, score := range s.cache {
		if key.ServiceID != serviceID {
			continue
		}
		if !score.IsArchivalCapable() {
			continue
		}
		result = append(result, key)
	}
	return result
}

// buildArchivalBenchService simulates a busy gateway: many services, each with
// many endpoints, a fraction archival-capable. Mirrors the prod shape where the
// global cache is large but one service's archival set is small.
func buildArchivalBenchService(numServices, endpointsPerService int) (*service, protocol.ServiceID) {
	svc := newArchivalTestService()
	future := time.Now().Add(time.Hour)
	for s := 0; s < numServices; s++ {
		serviceID := protocol.ServiceID("svc" + strconv.Itoa(s))
		for e := 0; e < endpointsPerService; e++ {
			key := NewEndpointKey(serviceID, protocol.EndpointAddr("ep"+strconv.Itoa(e)), sharedtypes.RPCType_JSON_RPC)
			score := Score{Value: 80}
			// ~10% of endpoints are archival-capable.
			if e%10 == 0 {
				score.IsArchival = true
				score.ArchivalExpiresAt = future
			}
			svc.setScoreLocked(key, score)
		}
	}
	return svc, protocol.ServiceID("svc0")
}

func BenchmarkGetArchivalEndpoints_OldFullScan(b *testing.B) {
	svc, serviceID := buildArchivalBenchService(50, 2000) // 100k cache entries
	ctx := context.Background()
	_ = ctx
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = oldGetArchivalScan(svc, serviceID)
	}
}

func BenchmarkGetArchivalEndpoints_Indexed(b *testing.B) {
	svc, serviceID := buildArchivalBenchService(50, 2000) // 100k cache entries
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = svc.GetArchivalEndpoints(ctx, serviceID)
	}
}
