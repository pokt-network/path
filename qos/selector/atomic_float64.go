package selector

import (
	"math"
	"sync/atomic"
)

// AtomicFloat64 is a lock-free float64 backed by an atomic uint64 (via math.Float64bits).
// The zero value is 0.0 and ready to use. Must not be copied after first use (it embeds
// atomic.Uint64) — hold it by pointer or as a field of a pointer-held struct.
//
// It exists to remove the repeated math.Float64bits/Float64frombits plumbing that each QoS
// type would otherwise carry for its per-operator concentration cap.
type AtomicFloat64 struct {
	bits atomic.Uint64
}

// Load returns the current value.
func (a *AtomicFloat64) Load() float64 { return math.Float64frombits(a.bits.Load()) }

// Store sets the value.
func (a *AtomicFloat64) Store(v float64) { a.bits.Store(math.Float64bits(v)) }
