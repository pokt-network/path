package noop

import (
	"errors"
	"math/rand"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/selector"
)

// RandomEndpointSelector provides the functionality defined by the protocol.EndpointSelector interface.
var _ protocol.EndpointSelector = RandomEndpointSelector{}

// RandomEndpointSelector returns a randomly selected endpoint from the set of available ones, when its
// Select method is called.
// It has no fields, since the endpoint selection is random, i.e. no data is kept on the endpoints.
type RandomEndpointSelector struct{}

// Select returns a randomly selected endpoint from the set of supplied endpoints.
// This method fulfills the protocol.EndpointSelector interface.
func (RandomEndpointSelector) Select(endpoints protocol.EndpointAddrList) (protocol.EndpointAddr, error) {
	if len(endpoints) == 0 {
		return protocol.EndpointAddr(""), errors.New("RandomEndpointSelector: an empty endpoint list was supplied to the selector")
	}

	selectedEndpointAddr := endpoints[rand.Intn(len(endpoints))]
	return selectedEndpointAddr, nil
}

// SelectMultiple returns multiple randomly selected endpoints from the set of supplied endpoints.
// This method fulfills the protocol.EndpointSelector interface.
func (RandomEndpointSelector) SelectMultiple(endpoints protocol.EndpointAddrList, numEndpoints uint) (protocol.EndpointAddrList, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("RandomEndpointSelector: an empty endpoint list was supplied to the selector")
	}

	return selector.RandomSelectMultiple(endpoints, numEndpoints), nil
}
