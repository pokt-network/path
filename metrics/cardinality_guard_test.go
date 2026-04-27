package metrics

import (
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCardinalityGuard_AdmitsRepeatedTuples(t *testing.T) {
	g := newCardinalityGuard("test", 5)
	for range 100 {
		require.True(t, g.allow("a", "b"))
	}
	require.Equal(t, int64(1), g.count.Load(), "repeated tuples must not inflate count")
}

func TestCardinalityGuard_DropsPastLimit(t *testing.T) {
	g := newCardinalityGuard("test", 3)
	require.True(t, g.allow("svc", "1"))
	require.True(t, g.allow("svc", "2"))
	require.True(t, g.allow("svc", "3"))
	require.False(t, g.allow("svc", "4"), "tuple 4 must be dropped")
	require.False(t, g.allow("svc", "5"), "tuple 5 must be dropped")

	// Previously-seen tuples still admitted.
	require.True(t, g.allow("svc", "1"))
	require.True(t, g.allow("svc", "2"))
	require.Equal(t, int64(3), g.count.Load())
}

func TestCardinalityGuard_NilReceiver(t *testing.T) {
	var g *cardinalityGuard
	require.True(t, g.allow("anything"))
}

func TestCardinalityGuard_Concurrent(t *testing.T) {
	g := newCardinalityGuard("test", 50)

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			g.allow("supplier-"+strconv.Itoa(n), "svc")
		}(i)
	}
	wg.Wait()

	require.LessOrEqual(t, g.count.Load(), int64(50), "count must never exceed limit")
}
