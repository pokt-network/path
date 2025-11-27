package data

// TODO_MVP(@adshmh): handle QoS observations for:
// - CometBFT

import (
	"fmt"

	"github.com/pokt-network/poktroll/pkg/polylog"

	qosobservation "github.com/pokt-network/path/observation/qos"
)

// setLegacyFieldsFromQoSObservations populates legacy records with QoS observation data.
// Currently supports:
// - EVM observations (returns multiple records based on RequestObservations)
// - Solana observations (returns single record)
// - Cosmos SDK observations (returns multiple records based on RequestProfiles)
//
// Parameters:
// - logger: logging interface
// - baseLegacyRecord: the base record to populate
// - observations: QoS observations data
//
// Returns: slice of populated legacy records
func setLegacyFieldsFromQoSObservations(
	logger polylog.Logger,
	baseLegacyRecord *legacyRecord,
	observations *qosobservation.Observations,
) []*legacyRecord {
	// EVM observations may contains multiple records in the case of batch requests.
	if evmObservations := observations.GetEvm(); evmObservations != nil {
		return setLegacyFieldsFromQoSEVMObservations(logger, baseLegacyRecord, evmObservations)
	}

	// Use Solana observations to update the legacy record's fields.
	if solanaObservations := observations.GetSolana(); solanaObservations != nil {
		populatedRecord := setLegacyFieldsFromQoSSolanaObservations(logger, baseLegacyRecord, solanaObservations)
		// Solana does not support batch requests so expect a single record.
		return []*legacyRecord{populatedRecord}
	}

	// Use Cosmos SDK observations to update the legacy record's fields.
	if cosmosObservations := observations.GetCosmos(); cosmosObservations != nil {
		return setLegacyFieldsFromQoSCosmosObservations(logger, baseLegacyRecord, cosmosObservations)
	}

	// For all other services, expect a single record.
	return []*legacyRecord{baseLegacyRecord}
}

// qosEVMErrorTypeStr defines the prefix for EVM QoS error types in legacy records
const qosEVMErrorTypeStr = "QOS_EVM"

// setLegacyFieldsFromQoSEVMObservations populates legacy records with EVM-specific QoS data.
// It captures:
// - Request payload size
// - JSONRPC method information
// - Error details (when applicable)
// Creates one legacy record per RequestObservation
//
// Parameters:
// - logger: logging interface
// - baseLegacyRecord: the base record to copy for each method
// - observations: EVM-specific QoS observations
//
// Returns: slice of populated legacy records
// EVM batch requests are supported as of PR #388.
func setLegacyFieldsFromQoSEVMObservations(
	_ polylog.Logger,
	baseLegacyRecord *legacyRecord,
	observations *qosobservation.EVMRequestObservations,
) []*legacyRecord {
	// Set common fields from observations
	baseLegacyRecord.RequestDataSize = float64(observations.RequestPayloadLength)

	evmInterpreter := &qosobservation.EVMObservationInterpreter{
		Observations: observations,
	}

	// Extract all JSONRPC request methods
	jsonrpcRequestMethods, ok := evmInterpreter.GetRequestMethods()
	if !ok || len(jsonrpcRequestMethods) == 0 {
		// If no methods found, return single record with base data
		populateEVMErrorFields(baseLegacyRecord, evmInterpreter)
		return []*legacyRecord{baseLegacyRecord}
	}

	// Create a separate legacy record for each method
	// 	 - In the case of EVM batch requests, this will create multiple records.
	// 	 - Non-EVM batch requests will create a single record.
	var legacyRecords []*legacyRecord
	for _, method := range jsonrpcRequestMethods {
		// Create a copy of the base record
		recordCopy := *baseLegacyRecord
		legacyRecord := &recordCopy

		// Set the method for this record
		legacyRecord.ChainMethod = method

		// Populate error fields if needed
		populateEVMErrorFields(legacyRecord, evmInterpreter)

		legacyRecords = append(legacyRecords, legacyRecord)
	}

	return legacyRecords
}

// populateEVMErrorFields sets error-related fields in the legacy record based on QoS observations
func populateEVMErrorFields(legacyRecord *legacyRecord, evmInterpreter *qosobservation.EVMObservationInterpreter) {
	// ErrorType is already set at gateway or protocol level.
	// Skip updating the error fields to preserve the original error.
	if legacyRecord.ErrorType != "" {
		return
	}

	_, requestErr, err := evmInterpreter.GetRequestStatus()
	// Could not extract request error details, skip the rest of the updates.
	if err != nil || requestErr == nil {
		return
	}

	legacyRecord.ErrorMessage = requestErr.String()

	switch {
	case requestErr.IsRequestError():
		legacyRecord.ErrorType = fmt.Sprintf("%s_REQUEST_ERROR", qosEVMErrorTypeStr)
	case requestErr.IsResponseError():
		legacyRecord.ErrorType = fmt.Sprintf("%s_ENDPOINT_ERROR", qosEVMErrorTypeStr)
	default:
		legacyRecord.ErrorType = fmt.Sprintf("%s_UNKNOWN_ERROR", qosEVMErrorTypeStr)
	}
}

// setLegacyFieldsFromQoSSolanaObservations populates legacy record with Solana-specific QoS data.
// It captures:
// - Request payload size
// - JSONRPC method information
// - Error details (when applicable)
//
// Parameters:
// - logger: logging interface
// - legacyRecord: the record to populate
// - observations: Solana-specific QoS observations
// Returns: the populated legacy record
func setLegacyFieldsFromQoSSolanaObservations(
	logger polylog.Logger,
	legacyRecord *legacyRecord,
	observations *qosobservation.SolanaRequestObservations,
) *legacyRecord {
	logger = logger.With("method", "setLegacyFieldsFromQoSSolanaObservations")

	// In bytes: the length of the request: float64 type is for compatibility with the legacy data pipeline.
	legacyRecord.RequestDataSize = float64(observations.RequestPayloadLength)

	// Initialize the Solana observations interpreter.
	// Used to extract required fields from the observations.
	solanaInterpreter := &qosobservation.SolanaObservationInterpreter{
		Logger:       logger,
		Observations: observations,
	}

	// Extract the JSONRPC request's method.
	legacyRecord.ChainMethod = solanaInterpreter.GetRequestMethod()

	// ErrorType is already set at gateway or protocol level.
	// Skip updating the error fields to preserve the original error.
	if legacyRecord.ErrorType != "" {
		return legacyRecord
	}

	errType := solanaInterpreter.GetRequestErrorType()
	legacyRecord.ErrorType = errType
	legacyRecord.ErrorMessage = errType

	return legacyRecord
}

// qosCosmosErrorTypeStr defines the prefix for Cosmos QoS error types in legacy records
const qosCosmosErrorTypeStr = "QOS_COSMOS"

// setLegacyFieldsFromQoSCosmosObservations populates legacy records with Cosmos SDK-specific QoS data.
// It captures:
// - Request payload size (aggregated across all request profiles)
// - Request methods (REST API paths and JSON-RPC methods)
// - Error details (when applicable)
// Creates one legacy record per request method, similar to EVM batch handling
//
// Parameters:
// - logger: logging interface
// - baseLegacyRecord: the base record to copy for each method
// - observations: Cosmos SDK-specific QoS observations
//
// Returns: slice of populated legacy records
func setLegacyFieldsFromQoSCosmosObservations(
	logger polylog.Logger,
	baseLegacyRecord *legacyRecord,
	observations *qosobservation.CosmosRequestObservations,
) []*legacyRecord {
	logger = logger.With("method", "setLegacyFieldsFromQoSCosmosObservations")

	// Initialize the Cosmos observations interpreter
	cosmosInterpreter := &qosobservation.CosmosSDKObservationInterpreter{
		Logger:       logger,
		Observations: observations,
	}

	// Set common fields from observations - aggregate payload length across all request profiles
	baseLegacyRecord.RequestDataSize = float64(cosmosInterpreter.GetTotalRequestPayloadLength())

	// Extract all request methods (REST API paths and JSON-RPC methods)
	requestMethods, ok := cosmosInterpreter.GetRequestMethods()
	if !ok || len(requestMethods) == 0 {
		// If no methods found, return single record with base data and populate error fields
		populateCosmosErrorFields(logger, baseLegacyRecord, cosmosInterpreter)
		return []*legacyRecord{baseLegacyRecord}
	}

	// Create a separate legacy record for each method
	// This enables the data pipeline to track metrics per individual method
	// Similar to EVM batch request handling
	var legacyRecords []*legacyRecord
	for _, method := range requestMethods {
		// Create a copy of the base record
		recordCopy := *baseLegacyRecord
		legacyRecord := &recordCopy

		// Set the method for this record
		legacyRecord.ChainMethod = method

		// Populate error fields if needed
		populateCosmosErrorFields(logger, legacyRecord, cosmosInterpreter)

		legacyRecords = append(legacyRecords, legacyRecord)
	}

	return legacyRecords
}

// populateCosmosErrorFields sets error-related fields in the legacy record based on Cosmos QoS observations
func populateCosmosErrorFields(logger polylog.Logger, legacyRecord *legacyRecord, cosmosInterpreter *qosobservation.CosmosSDKObservationInterpreter) {
	// ErrorType is already set at gateway or protocol level.
	// Skip updating the error fields to preserve the original error.
	if legacyRecord.ErrorType != "" {
		return
	}

	httpStatusCode, requestErr, err := cosmosInterpreter.GetRequestStatus()
	// Could not extract request error details, skip the rest of the updates.
	if err != nil {
		logger.Debug().Err(err).Msg("Failed to extract request status from Cosmos observations")
		return
	}

	// No error occurred, request was successful
	if requestErr == nil {
		return
	}

	// Set error message and type based on the request error
	legacyRecord.ErrorMessage = requestErr.ErrorKind.String()

	// Categorize errors based on HTTP status code and error details
	// TODO_TECHDEBT: Add more specific Cosmos error categorization as needed
	switch {
	case httpStatusCode >= 400 && httpStatusCode < 500:
		legacyRecord.ErrorType = fmt.Sprintf("%s_REQUEST_ERROR", qosCosmosErrorTypeStr)
	case httpStatusCode >= 500:
		legacyRecord.ErrorType = fmt.Sprintf("%s_ENDPOINT_ERROR", qosCosmosErrorTypeStr)
	default:
		legacyRecord.ErrorType = fmt.Sprintf("%s_UNKNOWN_ERROR", qosCosmosErrorTypeStr)
	}
}
