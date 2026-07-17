package solana

import (
	"fmt"
	"sync"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos"
)

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
}

// TODO_FUTURE: add an endpoint ranking method which can be used to assign a rank/score to a valid endpoint to guide endpoint selection.
//
// ValidateEndpoint returns an error if the supplied endpoint is not valid based on the perceived state of Solana blockchain.
func (s *ServiceState) ValidateEndpoint(endpoint endpoint) error {
	s.serviceStateLock.RLock()
	defer s.serviceStateLock.RUnlock()

	if err := endpoint.validateBasic(); err != nil {
		return err
	}

	if endpoint.Epoch < s.perceivedEpoch {
		return fmt.Errorf("solana endpoint epoch is less than chain perceived epoch: %d < %d", endpoint.Epoch, s.perceivedEpoch)
	}

	if endpoint.BlockHeight < s.perceivedBlockHeight {
		return fmt.Errorf("solana endpoint block height is less than chain perceived block height: %d < %d", endpoint.BlockHeight, s.perceivedBlockHeight)
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
