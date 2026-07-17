package qos

// MinAllowedBlockNumber returns the lowest block number an endpoint may report
// while still being considered in sync with the chain: perceivedBlock minus the
// sync allowance, floored at 0.
//
// Both inputs are uint64, so a naive perceivedBlock - syncAllowance underflows
// to a value near MaxUint64 whenever the sync allowance exceeds the perceived
// block height (e.g. very early in a chain's life, or a misconfigured allowance).
// That underflow would make the "endpoint block >= minAllowed" check fail for
// every endpoint and reject the entire endpoint set. Saturating the subtraction
// at 0 instead means "no floor" — every endpoint passes — which is the correct
// behavior when the allowance is larger than the chain height so far.
func MinAllowedBlockNumber(perceivedBlock, syncAllowance uint64) uint64 {
	if perceivedBlock <= syncAllowance {
		return 0
	}
	return perceivedBlock - syncAllowance
}
