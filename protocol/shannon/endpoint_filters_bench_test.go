package shannon

import (
	"strconv"
	"testing"

	"github.com/pokt-network/path/protocol"
)

func benchEndpoints(n int) map[protocol.EndpointAddr]endpoint {
	eps := make(map[protocol.EndpointAddr]endpoint, n)
	for i := 0; i < n; i++ {
		s := "supplier" + strconv.Itoa(i)
		addr := protocol.EndpointAddr(s + "-https://" + s + ".example.com")
		eps[addr] = &cfgEndpoint{addr: addr, supplier: s, sessionID: "sess1"}
	}
	return eps
}

// cloneEndpoints mirrors a fresh per-call working map for the benchmark.
func cloneEndpoints(src map[protocol.EndpointAddr]endpoint) map[protocol.EndpointAddr]endpoint {
	dst := make(map[protocol.EndpointAddr]endpoint, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// filterNewMap reproduces the OLD per-stage pattern: allocate a fresh map and
// copy survivors. Kept here only as a benchmark baseline.
func filterNewMap(eps map[protocol.EndpointAddr]endpoint, drop func(string) bool) map[protocol.EndpointAddr]endpoint {
	out := make(map[protocol.EndpointAddr]endpoint, len(eps))
	for addr, ep := range eps {
		if !drop(ep.Supplier()) {
			out[addr] = ep
		}
	}
	return out
}

const benchEndpointCount = 30

// BenchmarkFilterStage_NewMap is the old build-a-new-map-per-stage approach.
func BenchmarkFilterStage_NewMap(b *testing.B) {
	base := benchEndpoints(benchEndpointCount)
	drop := func(s string) bool { return s == "supplier3" }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		work := cloneEndpoints(base)
		b.StartTimer()
		_ = filterNewMap(work, drop)
	}
}

// BenchmarkFilterStage_InPlace is the new delete-in-place approach.
func BenchmarkFilterStage_InPlace(b *testing.B) {
	base := benchEndpoints(benchEndpointCount)
	isBlacklisted := func(s string) bool { return s == "supplier3" }
	logger := testLogger()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		work := cloneEndpoints(base)
		b.StartTimer()
		_ = removeBlacklistedSuppliers(work, protocol.EndpointAddr(""), isBlacklisted, logger)
	}
}
