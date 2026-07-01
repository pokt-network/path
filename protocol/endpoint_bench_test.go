package protocol

import "testing"

// Realistic endpoint address: supplier address + a URL that itself contains
// dashes (worst case for the old strings.Split, which split on every dash).
const benchEndpointAddr EndpointAddr = "pokt1eetcwfv2agdl2nvpf4cprhe89rdq3cxdf037wq-https://relayminer.shannon-mainnet.eu.nodefleet.net"

func BenchmarkEndpointAddr_GetAddress(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := benchEndpointAddr.GetAddress(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEndpointAddr_GetURL(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := benchEndpointAddr.GetURL(); err != nil {
			b.Fatal(err)
		}
	}
}
