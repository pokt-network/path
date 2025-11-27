package data

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/observation"
)

// setLegacyFieldsFromGatewayAuthData populates legacy record fields from auth data.
// Parameters:
// - legacyRecord: the record to populate
// - authObservations: source of authorization data
// Returns: the populated legacy record
func setLegacyFieldsFromGatewayAuthData(
	legacyRecord *legacyRecord,
	authObservations *observation.RequestAuth,
) *legacyRecord {
	legacyRecord.TraceID = authObservations.TraceId
	legacyRecord.Region = authObservations.Region

	portalCredentials := authObservations.GetPortalCredentials()
	// No Portal Credentials fields set, skip the rest of the processing.
	if portalCredentials == nil {
		return legacyRecord
	}
	legacyRecord.PortalAccountID = portalCredentials.PortalAccountId
	legacyRecord.PortalAppID = portalCredentials.PortalApplicationId

	return legacyRecord
}

// setLegacyFieldsFromGatewayObservations populates a legacy record with gateway observation data.
// It captures:
// - Request authentication data
// - Request type information
// - Timing information (start/completion timestamps)
// - Date formatting for BigQuery
// - Request round-trip processing time
//
// Parameters:
// - logger: logging interface
// - legacyRecord: the record to populate
// - observations: source gateway observations
// Returns: the populated legacy record
func setLegacyFieldsFromGatewayObservations(
	logger polylog.Logger,
	legacyRecord *legacyRecord,
	observations *observation.GatewayObservations,
) *legacyRecord {
	legacyRecord = setLegacyFieldsFromGatewayAuthData(legacyRecord, observations.GetRequestAuth())

	// Track organic (i.e. from the user) and synthetic (i.e. from the endpoint hydrator) requests.
	legacyRecord.RelayType = observations.RequestType.String()

	// Update request reception and completion timestamps.
	legacyRecord.RequestStartTimestamp = formatTimestampPbForBigQueryJSON(observations.ReceivedTime)
	legacyRecord.RequestReturnTimestamp = formatTimestampPbForBigQueryJSON(observations.CompletedTime)

	// BigQuery DATE type, format: "2025-04-11"
	legacyRecord.RequestDate = observations.ReceivedTime.AsTime().Format("2006-01-02")

	// BigQuery TIMESTAMP type, format: "2025-04-11T14:30:00.000Z"
	legacyRecord.RequestTimestamp = formatTimestampPbForBigQueryJSON(observations.ReceivedTime)

	// Request processing time, in seconds.
	//   - For HTTP requests, this is the round-trip time.
	//   - For Websocket requests, this is the total elapsed time the Websocket connection was open.
	legacyRecord.RequestRoundTripTime = float64(observations.CompletedTime.AsTime().Sub(observations.ReceivedTime.AsTime()).Milliseconds()) / 1000

	return legacyRecord
}
