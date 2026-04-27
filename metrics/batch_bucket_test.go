package metrics

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBatchCountBucket(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{-1, "1"},
		{0, "1"},
		{1, "1"},
		{2, "2-10"},
		{10, "2-10"},
		{11, "11-50"},
		{50, "11-50"},
		{51, "51-500"},
		{500, "51-500"},
		{501, "500+"},
		{5500, "500+"},
		{1_000_000, "500+"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, batchCountBucket(c.in), "batchCountBucket(%d)", c.in)
	}
}

func TestBatchCountBucket_BoundedCardinality(t *testing.T) {
	seen := map[string]struct{}{}
	for i := -10; i <= 10_000; i++ {
		seen[batchCountBucket(i)] = struct{}{}
	}
	require.LessOrEqual(t, len(seen), 5, "batch_count label cardinality must stay ≤5")
}
