package solana

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos"
)

// defaultSolanaBlockNumberSyncAllowance is the sync allowance used when configuration has
// not supplied one (startup, or external health-check rules failed to load).
//
// Unlike EVM and CosmosSDK — both of which default to 0 — Solana cannot default to a strict
// comparison. Solana produces a block roughly every 400ms, and perceivedBlockHeight is a MAX
// over endpoint observations, so with zero tolerance only the most recently observed endpoint
// can ever be valid: every other endpoint's newest observation is, by construction, older
// than the one that just raised the bar.
//
// That is not hypothetical. It locked Solana onto a single operator in production
// (2026-08-18): endpoints carrying user traffic refreshed their block height continuously and
// stayed valid, while endpoints refreshed only by health checks (~0.35/s per endpoint against
// ~2.5 blocks/s) sat permanently behind and were filtered out — which kept them from
// receiving the traffic that would have refreshed them. No health-check rate fixes that; it
// is a race re-lost every block.
//
// 750 blocks ≈ 5 minutes of Solana, and matches the sync_allowance already configured for
// solana in the external health-check rules, so an unloaded config behaves like a loaded one.
const defaultSolanaBlockNumberSyncAllowance = 750

// ServiceState keeps the expected current state of the Solana blockchain
// based on the endpoints' responses to different requests.
type ServiceState struct {
	logger polylog.Logger

	serviceStateLock sync.RWMutex
	// perceivedEpoch is the perceived current epoch based on endpoints' responses to `getEpochInfo` requests.
	// See the following link for more details:
	// https://solana.com/docs/rpc/http/getepochinfo
	perceivedEpoch uint64
	// perceivedBlockHeight is the perceived blockheight based on endpoints' responses to `getEpochInfo` requests.
	perceivedBlockHeight uint64

	// chainID and serviceID to add to endpoint checks.
	// Used by observations of Synthetic requests.
	chainID   string
	serviceID protocol.ServiceID

	// syncAllowance is how many blocks an endpoint may trail perceivedBlockHeight and still
	// be considered valid. 0 (the zero value) means "not configured" and falls back to
	// defaultSolanaBlockNumberSyncAllowance — it does NOT disable the check, matching the
	// CosmosSDK QoS. Set dynamically from configuration via QoS.SetSyncAllowance.
	//
	// Atomic rather than guarded by serviceStateLock: it is written by the health-check
	// config refresh, not by the observation path, and ValidateEndpoint must not take a
	// write lock to read it.
	syncAllowance atomic.Uint64
}

// getSyncAllowance returns the configured block-height sync allowance, falling back to the
// Solana default when configuration has not supplied one.
func (s *ServiceState) getSyncAllowance() uint64 {
	if v := s.syncAllowance.Load(); v > 0 {
		return v
	}
	return defaultSolanaBlockNumberSyncAllowance
}

// SetSyncAllowance dynamically updates the block-height sync allowance for this QoS instance.
//
// Called by the health check executor when external rules are loaded or refreshed, via the
// `interface{ SetSyncAllowance(uint64) }` assertion. Solana did not implement that interface
// before, so the `sync_allowance` configured for the service reached the health check's own
// sync check and was silently dropped on the endpoint-selection path.
//
// Promoted to the Solana QoS via its embedded *ServiceState.
func (s *ServiceState) SetSyncAllowance(syncAllowance uint64) {
	s.syncAllowance.Store(syncAllowance)
}

// TODO_FUTURE: add an endpoint ranking method which can be used to assign a rank/score to a valid endpoint to guide endpoint selection.
//
// ValidateEndpoint returns an error if the supplied endpoint is not valid based on the perceived state of Solana blockchain.
func (s *ServiceState) ValidateEndpoint(endpointAddr protocol.EndpointAddr, endpoint endpoint) error {
	s.serviceStateLock.RLock()
	perceivedEpoch := s.perceivedEpoch
	perceivedBlockHeight := s.perceivedBlockHeight
	s.serviceStateLock.RUnlock()

	// Rejections are recorded lazily: this runs for every endpoint on every selection pass
	// (~2000/s × the session's endpoint count on solana), and parsing the address out to a
	// domain on the passing path would be pure waste.
	//
	// Operator domain, not the supplier address: path_qos_filter_rejection_total is keyed on
	// domain because the supplier set rotates every session while rejections are sporadic per
	// supplier — with a supplier label it had the worst churn of any gateway metric.
	recordRejection := func(reason string) {
		metrics.RecordQoSFilterRejection(
			metrics.DomainFromEndpointAddr(string(endpointAddr)),
			string(s.serviceID),
			reason,
		)
	}

	if err := endpoint.validateBasic(); err != nil {
		// Split the reason so "we have never observed this endpoint" is distinguishable from
		// "this endpoint answered badly" — the two call for opposite responses, and lumping
		// them together is what made the pre-fix exclusions unreadable.
		reason := metrics.QoSFilterReasonInvalidResponse
		switch {
		case errors.Is(err, errNoGetHealthObs), errors.Is(err, errNoGetEpochInfoObs):
			reason = metrics.QoSFilterReasonBlockHeightUnknown
		case errors.Is(err, errRecentJSONRPCValidationError):
			reason = metrics.QoSFilterReasonInvalidResponse
		}
		recordRejection(reason)
		return err
	}

	if endpoint.Epoch < perceivedEpoch {
		recordRejection(metrics.QoSFilterReasonBlockHeightLag)
		return fmt.Errorf("solana endpoint epoch is less than chain perceived epoch: %d < %d", endpoint.Epoch, perceivedEpoch)
	}

	// An endpoint may trail the perceived height by up to the sync allowance.
	//
	// A strict comparison here is unusable on Solana: perceivedBlockHeight is a MAX over
	// observations of a chain producing ~2.5 blocks/s, so it is raised by whichever endpoint
	// reported last, above every other endpoint's most recent report. See
	// defaultSolanaBlockNumberSyncAllowance for what that cost in production.
	minAllowedBlockHeight := qos.MinAllowedBlockNumber(perceivedBlockHeight, s.getSyncAllowance())
	if endpoint.BlockHeight < minAllowedBlockHeight {
		recordRejection(metrics.QoSFilterReasonBlockHeightLag)
		return fmt.Errorf(
			"solana endpoint block height is outside the sync allowance: %d < %d (perceived %d, allowance %d)",
			endpoint.BlockHeight, minAllowedBlockHeight, perceivedBlockHeight, s.getSyncAllowance(),
		)
	}

	return nil
}

// UpdateFromObservations updates the service state using estimation(s) derived from the set of updated endpoints.
// NOTE: This only includes the set of endpoints for which an observation was received.
func (s *ServiceState) UpdateFromEndpoints(updatedEndpoints map[protocol.EndpointAddr]endpoint) error {
	s.serviceStateLock.Lock()
	defer s.serviceStateLock.Unlock()

	for endpointAddr, endpoint := range updatedEndpoints {
		if err := endpoint.validateBasic(); err != nil {
			continue
		}

		// The endpoint's Epoch should be at-least equal to the perceived epoch before being used to update the perceived state of Solana blockchain.
		if endpoint.Epoch < s.perceivedEpoch {
			continue
		}

		// The endpoint's BlockHeight should be greater than the perceived block height before being used to update the perceived state of Solana blockchain.
		if endpoint.BlockHeight <= s.perceivedBlockHeight {
			continue
		}

		// Defense-in-depth against block-height poisoning: ignore implausibly high
		// reports so one endpoint cannot set the perceived height arbitrarily high
		// and filter out every honest endpoint. (Small over-reports still require
		// median-anchored consensus — see qos.IsPlausibleBlockHeight.)
		if !qos.IsPlausibleBlockHeight(endpoint.BlockHeight, s.perceivedBlockHeight) {
			s.logger.Warn().
				Uint64("reported_block", endpoint.BlockHeight).
				Uint64("perceived_block", s.perceivedBlockHeight).
				Msg("⚠️ ignoring implausible block height (possible poisoning attempt)")
			continue
		}

		s.perceivedEpoch = endpoint.Epoch
		s.perceivedBlockHeight = endpoint.BlockHeight

		s.logger.With(
			"endpoint", endpointAddr,
			"block height", s.perceivedBlockHeight,
			"epoch", s.perceivedEpoch,
		).Debug().Msg("Updating latest block height")
	}

	return nil
}
