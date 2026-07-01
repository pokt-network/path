package gateway

import "testing"

// Worst case: a method that is NOT CometBFT, so the loop scans every prefix
// before returning false (and re-allocates the prefix slice each call).
func BenchmarkIsCometBFTMethod(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isCometBFTMethod("eth_getBlockByNumber")
	}
}

func BenchmarkIsCometBFTPath(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isCometBFTPath("/v1/cosmos/base/tendermint")
	}
}
