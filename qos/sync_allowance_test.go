package qos

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_MinAllowedBlockNumber(t *testing.T) {
	tests := []struct {
		name           string
		perceivedBlock uint64
		syncAllowance  uint64
		want           uint64
	}{
		{
			name:           "normal: perceived well above allowance",
			perceivedBlock: 1_000_000,
			syncAllowance:  100,
			want:           999_900,
		},
		{
			name:           "allowance greater than perceived clamps to 0 (no underflow)",
			perceivedBlock: 5,
			syncAllowance:  100,
			want:           0,
		},
		{
			name:           "allowance equal to perceived clamps to 0",
			perceivedBlock: 50,
			syncAllowance:  50,
			want:           0,
		},
		{
			name:           "zero perceived block clamps to 0",
			perceivedBlock: 0,
			syncAllowance:  10,
			want:           0,
		},
		{
			name:           "zero allowance returns perceived unchanged",
			perceivedBlock: 12345,
			syncAllowance:  0,
			want:           12345,
		},
		{
			name:           "large allowance near MaxUint64 does not wrap",
			perceivedBlock: 100,
			syncAllowance:  math.MaxUint64,
			want:           0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, MinAllowedBlockNumber(test.perceivedBlock, test.syncAllowance))
		})
	}
}
