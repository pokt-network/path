package shannon

import "testing"

// TestExtractDomainOrHost_CacheConsistency verifies that the memoized result is
// identical to the uncached extraction, and that repeated calls are stable.
func TestExtractDomainOrHost_CacheConsistency(t *testing.T) {
	cases := []string{
		"https://node1.example.com:8545/v1/json",
		"https://us-east.somesupplier.network/relay",
		"http://192.168.1.10:8080",
		"https://relay.provider.io",
		"localhost:3000",
		"relayminer1",
	}

	for _, url := range cases {
		want, wantErr := extractDomainOrHost(url)

		// First call populates the cache, second hits it; both must agree with
		// the uncached path.
		for i := 0; i < 2; i++ {
			got, err := ExtractDomainOrHost(url)
			if (err != nil) != (wantErr != nil) {
				t.Fatalf("ExtractDomainOrHost(%q) err=%v, want err=%v", url, err, wantErr)
			}
			if got != want {
				t.Fatalf("ExtractDomainOrHost(%q) call %d = %q, want %q", url, i, got, want)
			}
		}
	}
}

// TestExtractDomainOrHost_ErrorsNotCached verifies invalid inputs keep returning
// errors (and are not poisoned into the success cache).
func TestExtractDomainOrHost_ErrorsNotCached(t *testing.T) {
	for _, bad := range []string{"", "https://"} {
		if _, err := ExtractDomainOrHost(bad); err == nil {
			t.Fatalf("ExtractDomainOrHost(%q) expected error, got nil", bad)
		}
		// Second call must still error.
		if _, err := ExtractDomainOrHost(bad); err == nil {
			t.Fatalf("ExtractDomainOrHost(%q) second call expected error, got nil", bad)
		}
	}
}
