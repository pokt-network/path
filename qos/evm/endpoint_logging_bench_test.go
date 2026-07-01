package evm

import (
	"strconv"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
)

// These benchmarks model the per-endpoint logging pattern in
// filterValidEndpointsWithDetails at the production log level (warn), where the
// debug diagnostics are dropped. The point: logger.With(...) clones the logger
// context regardless of level, while adding fields directly on a level-gated
// event allocates nothing when the event is disabled.

const benchEndpointCount = 40

func benchEndpointAddrs(n int) []string {
	addrs := make([]string, n)
	for i := 0; i < n; i++ {
		addrs[i] = "pokt1supplier" + strconv.Itoa(i) + "-https://relay" + strconv.Itoa(i) + ".example.com"
	}
	return addrs
}

// Old pattern: a per-endpoint child logger via .With, then a (dropped) debug log.
func BenchmarkPerEndpointWith(b *testing.B) {
	logger := polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn")))
	addrs := benchEndpointAddrs(benchEndpointCount)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, addr := range addrs {
			l := logger.With("endpoint_addr", addr)
			l.Debug().Msg("processing endpoint")
		}
	}
}

// New pattern: field added inline on the level-gated event (no per-endpoint clone).
func BenchmarkPerEndpointInline(b *testing.B) {
	logger := polyzero.NewLogger(polyzero.WithLevel(polyzero.ParseLevel("warn")))
	addrs := benchEndpointAddrs(benchEndpointCount)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, addr := range addrs {
			logger.Debug().Str("endpoint_addr", addr).Msg("processing endpoint")
		}
	}
}
