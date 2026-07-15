package qos

// Block-height plausibility guards.
//
// Several QoS types derive a "perceived" chain height by taking the maximum
// height any endpoint reports. That makes a single endpoint able to set the
// perceived height to an arbitrary value: report a height far above the real
// tip and every honest endpoint falls outside the sync-allowance window and is
// filtered out, handing 100% of the service's traffic to the liar.
//
// IsPlausibleBlockHeight is a cheap defense-in-depth guard applied where the
// perceived height is updated. It rejects absurd absolute values and
// implausibly large single-update jumps, which blunts the total-hijack case
// (e.g. a report of MaxUint64). It does NOT defend against small over-reports
// (a value just past the sync allowance) — that requires outlier-resistant,
// median-anchored consensus (as EVM has and the other QoS types still need).
const (
	// MaxPlausibleBlockHeight is an absolute sanity ceiling on any reported
	// block height. No real chain is within many orders of magnitude of 10^12
	// blocks (at 200ms/block that is ~6000 years), so a reported height above
	// this is malicious or garbage and must never become the perceived height.
	MaxPlausibleBlockHeight uint64 = 1_000_000_000_000 // 1e12

	// MaxBlockHeightJump bounds how far above the current perceived height a
	// single update may move it. Real chains advance a bounded number of blocks
	// between observations; a jump of 10M blocks (days of the fastest chains)
	// is implausible in steady state. Only applied once a perceived height is
	// established (current > 0); the absolute ceiling covers cold start.
	MaxBlockHeightJump uint64 = 10_000_000 // 1e7
)

// IsPlausibleBlockHeight reports whether a newly-reported block height is a
// plausible update to the current perceived height. Callers should only adopt
// `candidate` as the new perceived height when this returns true.
func IsPlausibleBlockHeight(candidate, current uint64) bool {
	// Absolute sanity ceiling (also the only guard at cold start, current == 0).
	if candidate > MaxPlausibleBlockHeight {
		return false
	}
	// Reject implausibly large jumps above an established perceived height.
	// current <= MaxPlausibleBlockHeight here, so current + MaxBlockHeightJump
	// cannot overflow.
	if current > 0 && candidate > current+MaxBlockHeightJump {
		return false
	}
	return true
}
