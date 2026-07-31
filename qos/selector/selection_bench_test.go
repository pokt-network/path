package selector

import (
	"fmt"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"

	"github.com/pokt-network/path/protocol"
)

// Selection runs on every relay, so the instrumentation added to it must be cheap.
// Pool shaped like a real service: ~50 endpoints across 5 operators, one dominant.
func BenchmarkSelectWithConcentrationCap(b *testing.B) {
	var eps protocol.EndpointAddrList
	for i := 0; i < 43; i++ {
		eps = append(eps, protocol.EndpointAddr(fmt.Sprintf("s%d-https://n%d.dominant-op.com", i, i)))
	}
	for i := 0; i < 7; i++ {
		eps = append(eps, protocol.EndpointAddr(fmt.Sprintf("t%d-https://m%d.other-op%d.com", i, i, i%3)))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SelectWithConcentrationCap("bench-svc", eps, 0.65)
	}
}

// The diversity selector runs on every relay — the real hot path.
func BenchmarkSelectEndpointsWithDiversity(b *testing.B) {
	var eps protocol.EndpointAddrList
	for i := 0; i < 43; i++ {
		eps = append(eps, protocol.EndpointAddr(fmt.Sprintf("s%d-https://n%d.dominant-op.com", i, i)))
	}
	for i := 0; i < 7; i++ {
		eps = append(eps, protocol.EndpointAddr(fmt.Sprintf("t%d-https://m%d.other-op%d.com", i, i, i%3)))
	}
	logger := polyzero.NewLogger()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SelectEndpointsWithDiversity(logger, "bench-svc", eps, 1)
	}
}
