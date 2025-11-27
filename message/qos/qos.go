// package qos provides the functionality required for
// messaging (seriliaizing, sharing, etc...) QoS data between
// multiple PATH instances.
package qos

import (
	"encoding/json"
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/message"
	"github.com/pokt-network/path/protocol"
)

// The topic used by QoS publishers and subscribers
// for individual service request contexts.
const observationSetTopic = "qos.raw_data_set"

// TODO_MVP(@adshmh): Add the functionality required for fetching and applying QoS observations shared by other PATH instances.

type ServiceQoS interface {
	message.Unmarshaller
}

// ObservationSetMessage is the expected format of QoS messages shared
// between multiple PATH instances, using the provided MessagePlatform
type ObservationSetMessage struct {
	protocol.ServiceID `json:"service_id"`
	Payload            []byte `json:"payload"`
}

// TODO_MVP(@adshmh): implement the MessagePlatform interface in a separate package, using NATS or REDIS.
// MessagePlatform is used to:
// A) Publish QoS observation sets for sharing
// with other PATH instances, and
// B) Receive, through subscription to a topic, QoS observation
// sets shared by other PATH instances
type MessagePlatform interface {
	Publish(topic string, data []byte) error
	Subscribe(topic string) <-chan []byte
}

type Messenger struct {
	MessagePlatform
	Services map[protocol.ServiceID]ServiceQoS
	Logger   polylog.Logger
}

func (m *Messenger) Publish(observationSet message.ObservationSet) error {
	// TODO_IMPROVE: there may be some performance advantage to directly
	// sending a ServiceRequestContext to the service's QoS instance,
	// over publishing it to the shared medium to be picked up by
	// the same PATH instance.
	bz, err := observationSet.MarshalJSON()
	if err != nil {
		return fmt.Errorf("publish: error marshaling service request context: %w", err)
	}

	return m.MessagePlatform.Publish(observationSetTopic, bz)
}

func (m *Messenger) Start() error {
	// TODO_INCOMPLETE: validate the struct.

	observationSetMsgCh := m.Subscribe(observationSetTopic)

	go func() {
		m.run(observationSetMsgCh)
	}()

	return nil
}

func (m *Messenger) run(messageCh <-chan []byte) {
	// TODO_INCOMPLETE: use multiple goroutines here.
	for bz := range messageCh {
		var qosMsg ObservationSetMessage
		if err := json.Unmarshal(bz, &qosMsg); err != nil {
			// TODO_IMPROVE: log the error
			continue
		}

		serviceQoS, found := m.Services[qosMsg.ServiceID]
		if !found {
			// TODO_IMPROVE: log the error
			continue
		}

		// TODO_FUTURE: find out if there is a meaningful performance difference
		// if the code is refactored to use a single Unmarshal method call.
		observationSet, err := serviceQoS.UnmarshalJSONObservationSet(qosMsg.Payload)
		if err != nil {
			// TODO_IMPROVE: log the error
			continue
		}

		if err := observationSet.Broadcast(); err != nil {
			// TODO_IMPROVE: add more details to the log.
			m.Logger.Warn().Err(err).Msg("error broadcasting observationset")
		}
	}
}
