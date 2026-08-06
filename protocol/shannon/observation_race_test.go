package shannon

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	protocolobservations "github.com/pokt-network/path/observation/protocol"
)

// Regression tests for the SIGSEGV that killed a production gateway pod inside
// handleEndpointSuccess (protocol/shannon/context.go, `append` to endpointObservations).
//
// The crash was NOT a nil field. It was a data race on the slice header, and the reason the
// writer and the reader overlap is structural rather than incidental:
//
//	gateway: currentProtocolCtx := rc.protocolContexts[0]
//	  hedge:   go executeRequest(primaryCtx = that same context)
//	           └─ loses the race, but keeps running for defaultLoserGraceWindow (2s) on a
//	              detached context, then records its observation
//	gateway: race() returns the winner, handler returns
//	           └─ rc.protocolContexts[0].GetObservations() reads endpointObservations
//
// So one goroutine appends while another reads the same slice, with no ordering between them.
// The previous "collector goroutine + channel" scheme could not cover this: the channel existed
// only for the duration of a parallel batch and was reset to nil when it drained, so a late
// writer read nil and fell through to a bare append — and that nil check was itself racing with
// the reset.
//
// Both tests below are meaningful only under `-race`.

// Test_RecordEndpointObservation_ConcurrentWithGetObservations reproduces the crash shape
// directly: writers appending while readers snapshot, on one shared requestContext.
func Test_RecordEndpointObservation_ConcurrentWithGetObservations(t *testing.T) {
	c := require.New(t)

	rc := &requestContext{serviceID: "poly"}

	const writers, readers, perWriter = 8, 4, 200

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				rc.recordEndpointObservation(&protocolobservations.ShannonEndpointObservation{})
			}
		}()
	}
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				// Walk the returned slice: a torn header or a reallocated backing array
				// surfaces here rather than being silently tolerated.
				got := rc.GetObservations()
				for _, o := range got.GetShannon().GetObservations() {
					_ = len(o.GetHttpObservations().GetEndpointObservations())
				}
			}
		}()
	}
	wg.Wait()

	c.Equal(writers*perWriter, len(rc.endpointObservations), "every observation must be retained")
}

// Test_GetObservations_ReturnsSnapshotNotLiveSlice guards the subtler half of the fix. Locking
// the append alone is not enough: handing the caller the live slice moves the same race one step
// out, where a late hedge loser mutates the backing array the caller is still walking.
func Test_GetObservations_ReturnsSnapshotNotLiveSlice(t *testing.T) {
	c := require.New(t)

	rc := &requestContext{serviceID: "poly"}
	rc.recordEndpointObservation(&protocolobservations.ShannonEndpointObservation{})

	obs := rc.GetObservations()
	snapshot := obs.GetShannon().GetObservations()[0].
		GetHttpObservations().GetEndpointObservations()
	c.Len(snapshot, 1)

	// The losing hedge branch lands after the reader already has its copy.
	for i := 0; i < 64; i++ {
		rc.recordEndpointObservation(&protocolobservations.ShannonEndpointObservation{})
	}

	c.Len(snapshot, 1, "caller's slice must not observe writes made after GetObservations returned")
	c.Len(rc.endpointObservations, 65, "the context itself must still accumulate them")
}

// Test_RecordEndpointObservation_BoundedByMaxBatchPayloads verifies the cap that used to live in
// the collector goroutine survived the move: an unbounded slice is a memory-exhaustion vector on
// a large batch.
func Test_RecordEndpointObservation_BoundedByMaxBatchPayloads(t *testing.T) {
	c := require.New(t)

	rc := &requestContext{serviceID: "poly"}
	rc.concurrencyConfig.MaxBatchPayloads = 10

	for i := 0; i < 50; i++ {
		rc.recordEndpointObservation(&protocolobservations.ShannonEndpointObservation{})
	}
	c.Len(rc.endpointObservations, 10, "observations must be capped at MaxBatchPayloads")

	// A zero cap means "unset", not "drop everything" — single-relay contexts leave it unset.
	uncapped := &requestContext{serviceID: "poly"}
	for i := 0; i < 50; i++ {
		uncapped.recordEndpointObservation(&protocolobservations.ShannonEndpointObservation{})
	}
	c.Len(uncapped.endpointObservations, 50, "an unset cap must not drop observations")
}
