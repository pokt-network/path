package data

import (
	"fmt"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"

	shannonmetrics "github.com/pokt-network/path/metrics/protocol/shannon"
	protocolobservation "github.com/pokt-network/path/observation/protocol"
)

// setLegacyFieldsFromShannonProtocolObservations populates legacy record with Shannon protocol data.
// It processes:
// - Service ID mapping to chain ID
// - Request errors
// - Endpoint observations and errors
// - Timestamps for queries and responses
// - Endpoint location information
//
// Parameters:
// - logger: logging interface
// - legacyRecord: the record to populate
// - observationList: list of Shannon protocol observations
// Returns: the populated legacy record
func setLegacyFieldsFromShannonProtocolObservations(
	logger polylog.Logger,
	legacyRecord *legacyRecord,
	observationList *protocolobservation.ShannonObservationsList,
) *legacyRecord {
	// TODO_MVP(@adshmh): Simplify this if ShannonObservationsList type is dropped in favor of using a single ShannonRequestObservation per service request.
	//
	requestObservations := observationList.GetObservations()
	if requestObservations == nil {
		return legacyRecord
	}
	// Pick the last entry: this can be dropped once the above TODO is completed.
	observations := requestObservations[len(requestObservations)-1]

	// Use the ServiceID as the legacy record's chain ID.
	legacyRecord.ChainID = observations.ServiceId

	// Request processing error: set the fields and skip further processing.
	if requestErr := observations.GetRequestError(); requestErr != nil {
		legacyRecord.ErrorType = requestErr.ErrorType.String()
		legacyRecord.ErrorMessage = requestErr.ErrorDetails

		// Request error: no more data to add.
		return legacyRecord
	}

	// Handle different observation types based on the oneof field
	switch obsData := observations.GetObservationData().(type) {

	// HTTP observations
	case *protocolobservation.ShannonRequestObservations_HttpObservations:
		return setLegacyFieldsFromHTTPObservations(logger, legacyRecord, obsData.HttpObservations)

	// Websocket connection observations
	case *protocolobservation.ShannonRequestObservations_WebsocketConnectionObservation:
		return setLegacyFieldsFromWebsocketConnectionObservation(logger, legacyRecord, obsData.WebsocketConnectionObservation)

	// Websocket message observations
	case *protocolobservation.ShannonRequestObservations_WebsocketMessageObservation:
		return setLegacyFieldsFromWebsocketMessageObservation(logger, legacyRecord, obsData.WebsocketMessageObservation)

	// Unknown observation type
	default:
		logger.Warn().Msg("Unknown observation type received for legacy record processing")
		return legacyRecord
	}
}

// setLegacyFieldsFromHTTPObservations populates legacy record with HTTP endpoint observation data.
// This handles the original HTTP relay processing logic.
func setLegacyFieldsFromHTTPObservations(
	logger polylog.Logger,
	legacyRecord *legacyRecord,
	httpObservations *protocolobservation.ShannonHTTPEndpointObservations,
) *legacyRecord {
	endpointObservations := httpObservations.GetEndpointObservations()
	// No endpoint observations: this should not happen as the request has not error set.
	// Log a warning entry.
	if len(endpointObservations) == 0 {
		logger.Warn().Err(fmt.Errorf("")).Msg("Received no Shannon endpoint observations for a valid request.")
		return legacyRecord
	}

	// TODO_FUTURE(@adshmh): Update the processing method if a retry mechanism is implemented:
	// Retries will result in multiple endpoint observations for a single request.
	//
	// Use the most recent entry in the endpoint observations.
	endpointObservation := endpointObservations[len(endpointObservations)-1]

	// Update error fields if an endpoint error has occurred.
	legacyRecord = setLegacyErrFieldsFromShannonEndpointError(legacyRecord, endpointObservation)

	// TODO_MVP(@adshmh): surface application public key if it is a must for the data pipeline.
	legacyRecord.ProtocolAppPublicKey = endpointObservation.GetEndpointAppAddress()

	// Set endpoint query/response timestamps
	legacyRecord.NodeQueryTimestamp = formatTimestampPbForBigQueryJSON(endpointObservation.EndpointQueryTimestamp)
	legacyRecord.NodeReceiveTimestamp = formatTimestampPbForBigQueryJSON(endpointObservation.EndpointResponseTimestamp)

	// track time spent waiting for the endpoint: required for calculating the `PortalTripTime` legacy field.
	legacyRecord.endpointTripTime = endpointObservation.EndpointResponseTimestamp.AsTime().Sub(endpointObservation.EndpointQueryTimestamp.AsTime()).Seconds()

	// Set endpoint address to the supplier address.
	// Will be "fallback" in the case of a request sent to a fallback endpoint.
	legacyRecord.NodeAddress = endpointObservation.GetSupplier()

	// Extract and set the endpoint's domain from its URL.
	// Empty value if parsing the URL above failed.
	endpointDomain, err := shannonmetrics.ExtractDomainOrHost(endpointObservation.GetEndpointUrl())
	if err != nil {
		logger.Error().Err(err).Msg("Could not extract domain from Shannon endpoint URL")
		return legacyRecord
	}
	legacyRecord.NodeDomain = endpointDomain

	return legacyRecord
}

// setLegacyFieldsFromWebsocketConnectionObservation populates legacy record with Websocket connection observation data.
// This handles Websocket connection lifecycle (not individual messages).
func setLegacyFieldsFromWebsocketConnectionObservation(
	logger polylog.Logger,
	legacyRecord *legacyRecord,
	wsConnectionObs *protocolobservation.ShannonWebsocketConnectionObservation,
) *legacyRecord {
	logger = logger.With("method", "setLegacyFieldsFromWebsocketConnectionObservation")

	// Update error fields if a connection error has occurred.
	legacyRecord = setLegacyErrFieldsFromWebsocketConnectionError(legacyRecord, wsConnectionObs)

	// Set application address
	legacyRecord.ProtocolAppPublicKey = wsConnectionObs.GetEndpointAppAddress()

	// Websocket connections don't have separate query/response timestamps at the protocol level.
	// Connection timing is tracked at the gateway level instead.
	// Set both timestamps to empty strings to indicate they don't apply.
	legacyRecord.NodeQueryTimestamp = ""
	legacyRecord.NodeReceiveTimestamp = ""
	legacyRecord.endpointTripTime = 0

	// Set endpoint address to the supplier address.
	legacyRecord.NodeAddress = wsConnectionObs.GetSupplier()

	// Extract effective TLD+1 from endpoint URL.
	endpointUrl := wsConnectionObs.GetEndpointUrl()
	endpointDomain, err := shannonmetrics.ExtractDomainOrHost(endpointUrl)
	if err != nil {
		logger.Error().Err(err).Msgf("Could not extract domain from endpoint URL %s.", endpointUrl)
		endpointDomain = shannonmetrics.ErrDomain
	}
	legacyRecord.NodeDomain = endpointDomain

	return legacyRecord
}

// setLegacyFieldsFromWebsocketMessageObservation populates legacy record with Websocket message observation data.
// This handles individual Websocket messages sent over an established connection.
func setLegacyFieldsFromWebsocketMessageObservation(
	logger polylog.Logger,
	legacyRecord *legacyRecord,
	wsMessageObs *protocolobservation.ShannonWebsocketMessageObservation,
) *legacyRecord {
	// Update error fields if a message error has occurred.
	legacyRecord = setLegacyErrFieldsFromWebsocketMessageError(legacyRecord, wsMessageObs)

	// Set application address
	legacyRecord.ProtocolAppPublicKey = wsMessageObs.GetEndpointAppAddress()

	// Websocket messages lack separate request/response cycles - timestamps don't apply
	// Set both timestamps to empty strings as requested by @fredteumer
	legacyRecord.NodeQueryTimestamp = ""
	// TODO_REVISIT: Can individual websocket message have a receive timestamp?
	legacyRecord.NodeReceiveTimestamp = ""

	// Websocket messages have no request/response latency - set to 0 as it doesn't apply
	legacyRecord.endpointTripTime = 0

	// Set endpoint address to the supplier address.
	legacyRecord.NodeAddress = wsMessageObs.GetSupplier()

	// Extract and set the endpoint's domain from its URL.
	// Empty value if parsing the URL above failed.
	endpointUrl := wsMessageObs.GetEndpointUrl()
	endpointDomain, err := shannonmetrics.ExtractDomainOrHost(endpointUrl)
	if err != nil {
		logger.Error().Err(err).Msg("Could not extract domain from Websocket message endpoint URL")
		endpointDomain = shannonmetrics.ErrDomain
	}
	legacyRecord.NodeDomain = endpointDomain

	// Websocket messages lack HTTP-style methods and JSON-RPC extraction is QoS-level - using identifier for analytics
	// TODO_TECHDEBT(@adshmh,@commoddity): When QoS observations for Websocket messages are added,
	// use the method from the QoS observations and move this to a new method in the `legacy_qos.go` file.
	legacyRecord.ChainMethod = "websocket_message"

	// Using MessagePayloadSize as closest equivalent to HTTP request size for bandwidth analytics
	legacyRecord.RequestDataSize = float64(wsMessageObs.GetMessagePayloadSize())

	return legacyRecord
}

// setLegacyErrFieldsFromWebsocketConnectionError populates error fields in legacy record from Websocket connection error data.
func setLegacyErrFieldsFromWebsocketConnectionError(
	legacyRecord *legacyRecord,
	wsConnectionObs *protocolobservation.ShannonWebsocketConnectionObservation,
) *legacyRecord {
	endpointErr := wsConnectionObs.ErrorType
	// No endpoint error has occurred: no error processing required.
	if endpointErr == nil {
		return legacyRecord
	}

	// Update ErrorType using the observed endpoint error.
	legacyRecord.ErrorType = endpointErr.String()

	// Build the endpoint error details, including any sanctions.
	var errMsg string
	if errDetails := wsConnectionObs.GetErrorDetails(); errDetails != "" {
		errMsg = fmt.Sprintf("error details: %s", errDetails)
	}

	// Add the sanction details to the error message.
	if endpointSanction := wsConnectionObs.RecommendedSanction; endpointSanction != nil {
		errMsg = fmt.Sprintf("%s, sanction: %s", errMsg, endpointSanction.String())
	}

	// Set the error message field.
	legacyRecord.ErrorMessage = errMsg

	return legacyRecord
}

// setLegacyErrFieldsFromWebsocketMessageError populates error fields in legacy record from Websocket message error data.
func setLegacyErrFieldsFromWebsocketMessageError(
	legacyRecord *legacyRecord,
	wsMessageObs *protocolobservation.ShannonWebsocketMessageObservation,
) *legacyRecord {
	endpointErr := wsMessageObs.ErrorType
	// No endpoint error has occurred: no error processing required.
	if endpointErr == nil {
		return legacyRecord
	}

	// Update ErrorType using the observed endpoint error.
	legacyRecord.ErrorType = endpointErr.String()

	// Build the endpoint error details, including any sanctions.
	var errMsg string
	if errDetails := wsMessageObs.GetErrorDetails(); errDetails != "" {
		errMsg = fmt.Sprintf("error details: %s", errDetails)
	}

	// Add the sanction details to the error message.
	if endpointSanction := wsMessageObs.RecommendedSanction; endpointSanction != nil {
		errMsg = fmt.Sprintf("%s, sanction: %s", errMsg, endpointSanction.String())
	}

	// Set the error message field.
	legacyRecord.ErrorMessage = errMsg

	return legacyRecord
}

// setLegacyErrFieldsFromShannonEndpointError populates error fields in legacy record from endpoint error data.
// It handles:
// - Error type mapping
// - Error message construction
// - Sanction details when present
//
// Parameters:
// - legacyRecord: the record to update
// - endpointObservation: endpoint observation containing error data
// Returns: the updated legacy record
func setLegacyErrFieldsFromShannonEndpointError(
	legacyRecord *legacyRecord,
	endpointObservation *protocolobservation.ShannonEndpointObservation,
) *legacyRecord {

	endpointErr := endpointObservation.ErrorType
	// No endpoint error has occurred: no error processing required.
	if endpointErr == nil {
		return legacyRecord
	}

	// Update ErrorType using the observed endpoint error.
	legacyRecord.ErrorType = endpointErr.String()

	// Build the endpoint error details, including any sanctions.
	var errMsg string
	if errDetails := endpointObservation.GetErrorDetails(); errDetails != "" {
		errMsg = fmt.Sprintf("error details: %s", errDetails)
	}

	// Add the sanction details to the error message.
	if endpointSanction := endpointObservation.RecommendedSanction; endpointSanction != nil {
		errMsg = fmt.Sprintf("%s, sanction: %s", errMsg, endpointSanction.String())
	}

	legacyRecord.ErrorMessage = errMsg

	return legacyRecord
}

// formatTimestampPbForBigQueryJSON formats a protobuf Timestamp for BigQuery JSON inserts.
// BigQuery expects timestamps in RFC 3339 format: YYYY-MM-DDTHH:MM:SS[.SSSSSS]Z
func formatTimestampPbForBigQueryJSON(pbTimestamp *timestamppb.Timestamp) string {
	// Convert the protobuf timestamp to Go time.Time
	goTime := pbTimestamp.AsTime()

	// Format in RFC 3339 format which BigQuery expects
	return goTime.Format(time.RFC3339Nano)
}
