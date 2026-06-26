package reputation

import (
	"context"
	"strconv"
	"testing"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

// buildRankBenchService returns a service whose cache is pre-populated with n
// scored endpoints, plus the key slice to rank. Scores are spread so no two
// are equal (worst case for any comparison-based sort).
func buildRankBenchService(n int) (*service, []EndpointKey) {
	config := Config{Enabled: true, InitialScore: 80, MinThreshold: 30}
	config.HydrateDefaults()

	svc := NewService(config, newMockStorage()).(*service)

	keys := make([]EndpointKey, n)
	for i := 0; i < n; i++ {
		addr := protocol.EndpointAddr("endpoint-" + strconv.Itoa(i))
		key := NewEndpointKey("eth", addr, sharedtypes.RPCType_JSON_RPC)
		keys[i] = key
		// Descending input order forces maximum swaps for a naive sort.
		svc.cache[key] = Score{Value: float64(n - i)}
	}
	return svc, keys
}

func benchmarkRankEndpointsByScore(b *testing.B, n int) {
	svc, keys := buildRankBenchService(n)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.RankEndpointsByScore(ctx, keys); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRankEndpointsByScore10(b *testing.B)  { benchmarkRankEndpointsByScore(b, 10) }
func BenchmarkRankEndpointsByScore50(b *testing.B)  { benchmarkRankEndpointsByScore(b, 50) }
func BenchmarkRankEndpointsByScore200(b *testing.B) { benchmarkRankEndpointsByScore(b, 200) }
