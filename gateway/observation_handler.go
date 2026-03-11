// Package gateway provides the default observation handler for async processing.
package gateway

import (
	"sync"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
	qostypes "github.com/pokt-network/path/qos/types"
)

// DefaultObservationHandler is the default implementation of ObservationHandler.
// It logs extracted data at debug level and updates QoS state with extracted data
// (e.g., perceived block number) without blocking user requests.
type DefaultObservationHandler struct {
	logger polylog.Logger

	// qosInstances maps service IDs to their QoS implementations.
	// Used to call UpdateFromExtractedData for block height updates.
	qosInstances   map[protocol.ServiceID]QoSService
	qosInstancesMu sync.RWMutex
}

// NewDefaultObservationHandler creates a new DefaultObservationHandler.
func NewDefaultObservationHandler(logger polylog.Logger) *DefaultObservationHandler {
	return &DefaultObservationHandler{
		logger:       logger.With("component", "observation_handler"),
		qosInstances: make(map[protocol.ServiceID]QoSService),
	}
}

// SetQoSInstances sets the QoS instances used for updating state from extracted data.
// This should be called after the QoS instances are created.
func (h *DefaultObservationHandler) SetQoSInstances(instances map[protocol.ServiceID]QoSService) {
	h.qosInstancesMu.Lock()
	defer h.qosInstancesMu.Unlock()
	h.qosInstances = instances
}

// HandleExtractedData processes the extracted data from an observation.
// This is called by worker pool goroutines after async parsing completes.
//
// This method:
//   - Updates QoS state (e.g., perceived block number) via UpdateFromExtractedData
//   - Logs extracted data at debug level for observability
func (h *DefaultObservationHandler) HandleExtractedData(obs *QueuedObservation, data *qostypes.ExtractedData) error {
	// Update QoS state with extracted data (e.g., block height)
	h.qosInstancesMu.RLock()
	qosInstance, exists := h.qosInstances[obs.ServiceID]
	h.qosInstancesMu.RUnlock()

	if exists && qosInstance != nil {
		if err := qosInstance.UpdateFromExtractedData(obs.EndpointAddr, data); err != nil {
			h.logger.Warn().
				Err(err).
				Str("service_id", string(obs.ServiceID)).
				Str("endpoint", string(obs.EndpointAddr)).
				Msg("Failed to update QoS state from extracted data")
		}
	}

	// Log extracted data at debug level
	logEvent := h.logger.Debug().
		Str("service_id", string(obs.ServiceID)).
		Str("endpoint", string(obs.EndpointAddr)).
		Str("source", string(obs.Source)).
		Int64("block_height", data.BlockHeight).
		Str("chain_id", data.ChainID).
		Bool("is_syncing", data.IsSyncing).
		Bool("archival_check_performed", data.ArchivalCheckPerformed).
		Bool("is_archival", data.IsArchival).
		Bool("is_valid", data.IsValidResponse).
		Int("http_status", data.HTTPStatusCode).
		Dur("response_time", data.ResponseTime)

	// Log extraction errors if any
	if data.HasErrors() {
		for field, errMsg := range data.ExtractionErrors {
			logEvent = logEvent.Str("extraction_error_"+field, errMsg)
		}
	}

	logEvent.Msg("Processed observation data")

	return nil
}
