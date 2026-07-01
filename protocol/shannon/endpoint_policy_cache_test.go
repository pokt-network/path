package shannon

import "testing"

// TestIsRawIP_CacheConsistency verifies the memoized isRawIP agrees with the
// uncached computation and is stable across repeated calls.
func TestIsRawIP_CacheConsistency(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://1.2.3.4:8545", true},
		{"http://10.0.0.1", true},
		{"https://[2001:db8::1]:8545", true},
		{"https://relay.example.com", false},
		{"https://us-east.somesupplier.network/v1", false},
		{"not a url", false},
		{"", false},
	}

	for _, c := range cases {
		if got := computeIsRawIP(c.url); got != c.want {
			t.Fatalf("computeIsRawIP(%q) = %v, want %v", c.url, got, c.want)
		}
		// First call populates the cache, second hits it; both must match.
		for i := 0; i < 2; i++ {
			if got := isRawIP(c.url); got != c.want {
				t.Fatalf("isRawIP(%q) call %d = %v, want %v", c.url, i, got, c.want)
			}
		}
	}
}

var benchPolicyURLs = []string{
	"https://1.2.3.4:8545",
	"https://relay.provider.io",
	"http://10.0.0.1:8080",
	"https://us-east.somesupplier.network/relay",
}

func BenchmarkIsRawIP_Uncached(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = computeIsRawIP(benchPolicyURLs[i%len(benchPolicyURLs)])
	}
}

func BenchmarkIsRawIP_Cached(b *testing.B) {
	for _, u := range benchPolicyURLs {
		_ = isRawIP(u) // warm cache
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = isRawIP(benchPolicyURLs[i%len(benchPolicyURLs)])
	}
}
