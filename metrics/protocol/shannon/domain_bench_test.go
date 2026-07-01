package shannon

import "testing"

// benchURLs mixes the common shapes seen on the relay hot path:
// public TLD+1 (publicsuffix path), IP, and a multi-label host.
var benchURLs = []string{
	"https://node1.example.com:8545/v1/json",
	"https://us-east.somesupplier.network/relay",
	"http://192.168.1.10:8080",
	"https://relay.provider.io",
}

func BenchmarkExtractDomainOrHost(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ExtractDomainOrHost(benchURLs[i%len(benchURLs)]); err != nil {
			b.Fatal(err)
		}
	}
}
