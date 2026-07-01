package jsonrpc

import "testing"

func BenchmarkIDString(b *testing.B) {
	id := IDFromInt(123456)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = id.String()
	}
}
