package gateway

import (
	"context"

	"github.com/pokt-network/path/protocol"
)

// perceivedBlockResetter is implemented by QoS instances that track a perceived block
// height and can reset it (in-memory + Redis). EVM, Cosmos, Solana, and NoOp QoS
// implement it; QoS types without a perceived height (e.g. generic JSON-RPC) do not.
type perceivedBlockResetter interface {
	ResetPerceivedBlockHeight(ctx context.Context) error
}

// ChainStateAdmin resets per-service chain state (perceived block height) via admin
// endpoints. It backs POST /admin/chain-state/clear/{serviceId}.
//
// Perceived block height is a monotonic max floor held in per-pod memory and mirrored to
// Redis; neither the max-based consensus nor the external floor can LOWER it, so a
// too-high value (e.g. a slot-poisoned Solana height, or a value raised by a mislabeled
// external source) cannot self-correct. This admin path deletes it so it rebuilds from
// fresh endpoint observations. Like the circuit-breaker clear, it must be called on each
// pod because the in-memory floor is per-pod.
type ChainStateAdmin struct {
	qosInstances map[protocol.ServiceID]QoSService
}

// NewChainStateAdmin builds a ChainStateAdmin over the given QoS instances.
func NewChainStateAdmin(qosInstances map[protocol.ServiceID]QoSService) *ChainStateAdmin {
	return &ChainStateAdmin{qosInstances: qosInstances}
}

// ResetChainState clears the perceived block height (in-memory + Redis) for a service.
// Returns found=false if there is no such service or its QoS does not track a perceived
// block height; err is non-nil only if the reset itself failed.
func (a *ChainStateAdmin) ResetChainState(ctx context.Context, serviceID string) (found bool, err error) {
	qos, ok := a.qosInstances[protocol.ServiceID(serviceID)]
	if !ok {
		return false, nil
	}
	resetter, ok := qos.(perceivedBlockResetter)
	if !ok {
		return false, nil
	}
	if err := resetter.ResetPerceivedBlockHeight(ctx); err != nil {
		return true, err
	}
	return true, nil
}
