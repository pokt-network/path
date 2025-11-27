package data

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/observation"
)

// TODO_MVP(@adshmh): Remove once the data pipeline has been updated.
//
// legacyRecord contains all the fields required by the legacy data pipeline.
type legacyRecord struct {
	TraceID                string  `json:"request_id"`            // Service Request's Trace ID.
	Region                 string  `json:"region"`                // Region where the gateway serving the request is located (Grove legacy metadata)
	PortalAccountID        string  `json:"portal_account_id"`     // Portal account ID (Grove legacy metadata)
	PortalAppID            string  `json:"portal_application_id"` // Portal application ID (Grove legacy metadata)
	ChainID                string  `json:"chain_id"`              // The ID of the service/blockchain
	ChainMethod            string  `json:"chain_method"`          // The method of the JSONRPC request: applicable only to JSONRPC-based services.
	ProtocolAppPublicKey   string  `json:"protocol_application_public_key"`
	RelayType              string  `json:"relay_type"` // Type of request: User / EndpointQualityCheck (i.e. from the EndpointHydrator)
	IsError                bool    `json:"is_error"`
	ErrorType              string  `json:"error_type"`           // Type of error encountered: user, protocol, internal.
	ErrorMessage           string  `json:"error_message"`        // Details of the error, if available.
	ErrorSource            string  `json:"error_source"`         // Hardcoded to "PATH" to inform the legacy data pipeline.
	NodeQueryTimestamp     string  `json:"node_send_ts"`         // TIMESTAMP of when the request was sent to the endpoint.
	NodeReceiveTimestamp   string  `json:"node_receive_ts"`      // TIMESTAMP of when the endpoint's response was received.
	RequestStartTimestamp  string  `json:"relay_start_ts"`       // TIMESTAMP of when the request was received.
	RequestReturnTimestamp string  `json:"relay_return_ts"`      // TIMESTAMP of when a response was returned to the client.
	RequestRoundTripTime   float64 `json:"relay_roundtrip_time"` // Request processing time, in seconds.
	PortalTripTime         float64 `json:"portal_trip_time"`     // In seconds: Total request processing time - time spent waiting for the endpoint.
	NodeTripTime           float64 `json:"node_trip_time"`       // In seconds: Total time spent waiting for the endpoint to respond.
	RequestDataSize        float64 `json:"request_data_size"`    // In bytes: the length of the request.
	RequestDate            string  `json:"date"`                 // BigQuery DATE type, format: "2025-04-11"
	RequestTimestamp       string  `json:"ts"`                   // BigQuery TIMESTAMP type, format: "2025-04-11T14:30:00.000Z"
	NodeAddress            string  `json:"pokt_node_address"`    // Address of the endpoint that served the request.
	NodeDomain             string  `json:"pokt_node_domain"`     // URL domain of the endpoint that served the request.

	// internal fields used for tracking protocol and QoS data.
	endpointTripTime float64 // endpoint response timestamp - endpoint query time, in seconds.
}

// converts data records to legacy format, for compatibility with the existing data pipeline.
// Returns multiple legacy records for scenarios like EVM batch requests where each method
// needs its own record.
func buildLegacyDataRecords(
	logger polylog.Logger,
	observations *observation.RequestResponseObservations,
) []*legacyRecord {
	// initialize the base legacy-compatible data record.
	baseLegacyRecord := &legacyRecord{}

	// Update the legacy data record from Gateway observations.
	if gatewayObservations := observations.GetGateway(); gatewayObservations != nil {
		baseLegacyRecord = setLegacyFieldsFromGatewayObservations(logger, baseLegacyRecord, gatewayObservations)
	}

	// TODO_MVP(@adshmh): Set legacy fields from Shannon observations.
	//
	// Extract protocol observations
	protocolObservations := observations.GetProtocol()

	// Update the data record from Shannon protocol data
	if shannonObservations := protocolObservations.GetShannon(); shannonObservations != nil {
		baseLegacyRecord = setLegacyFieldsFromShannonProtocolObservations(logger, baseLegacyRecord, shannonObservations)
	}

	// Update the legacy data records from QoS observations.
	// This may return multiple records for EVM batch requests.
	var legacyRecords []*legacyRecord
	if qosObservations := observations.GetQos(); qosObservations != nil {
		legacyRecords = setLegacyFieldsFromQoSObservations(logger, baseLegacyRecord, qosObservations)
	} else {
		legacyRecords = []*legacyRecord{baseLegacyRecord}
	}

	// Set constant/calculated/inferred fields' values for all records.
	for _, legacyRecord := range legacyRecords {
		if legacyRecord.ErrorType != "" {
			// Redundant value, set to comply with the legacy data pipeline.
			legacyRecord.IsError = true
		}

		// Hardcoded to "PATH" to inform the legacy data pipeline.
		legacyRecord.ErrorSource = "PATH"

		// Time spent waiting for the endpoint's response, in seconds.
		legacyRecord.NodeTripTime = legacyRecord.endpointTripTime

		// Total request processing time - time spent waiting for the endpoint, measured in seconds.
		legacyRecord.PortalTripTime = legacyRecord.RequestRoundTripTime - legacyRecord.endpointTripTime
	}

	return legacyRecords
}
