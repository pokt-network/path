package qos

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_IsPlausibleBlockHeight(t *testing.T) {
	tests := []struct {
		name      string
		candidate uint64
		current   uint64
		want      bool
	}{
		{
			name:      "normal small advance is plausible",
			candidate: 1_000_100,
			current:   1_000_000,
			want:      true,
		},
		{
			name:      "cold start: reasonable first value is plausible",
			candidate: 20_000_000,
			current:   0,
			want:      true,
		},
		{
			name:      "cold start: MaxUint64 is rejected by the absolute ceiling",
			candidate: math.MaxUint64,
			current:   0,
			want:      false,
		},
		{
			name:      "steady state: MaxUint64 is rejected",
			candidate: math.MaxUint64,
			current:   1_000_000,
			want:      false,
		},
		{
			name:      "above the absolute ceiling is rejected",
			candidate: MaxPlausibleBlockHeight + 1,
			current:   1_000_000,
			want:      false,
		},
		{
			name:      "at the absolute ceiling with no established perceived is allowed",
			candidate: MaxPlausibleBlockHeight,
			current:   0,
			want:      true,
		},
		{
			name:      "implausibly large jump above established perceived is rejected",
			candidate: 1_000_000 + MaxBlockHeightJump + 1,
			current:   1_000_000,
			want:      false,
		},
		{
			name:      "jump right at the limit is allowed",
			candidate: 1_000_000 + MaxBlockHeightJump,
			current:   1_000_000,
			want:      true,
		},
		{
			name:      "equal values are plausible",
			candidate: 500,
			current:   500,
			want:      true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, IsPlausibleBlockHeight(test.candidate, test.current))
		})
	}
}
