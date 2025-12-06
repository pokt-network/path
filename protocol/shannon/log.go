package shannon

import (
	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/x/session/types"

	"github.com/pokt-network/path/log"
	"github.com/pokt-network/path/protocol"
)

// hydrateLoggerWithSession enhances a logger with full session details.
// Creates contextually rich logs with comprehensive session information.
//
// Parameters:
//   - logger: The base logger to enhance
//   - session: The session object containing full session data
//
// Returns:
//   - An enhanced logger with all relevant session fields attached
func hydrateLoggerWithSession(
	logger polylog.Logger,
	session *types.Session,
) polylog.Logger {
	// Handle nil session
	if session == nil {
		return logger
	}

	// Start with basic session fields
	hydratedLogger := logger.With(
		"session_id", session.SessionId,
		"session_number", session.SessionNumber,
		"num_blocks_per_session", session.NumBlocksPerSession,
		"supplier_count", len(session.Suppliers),
	)

	// Add session header details if available
	if session.Header != nil {
		hydratedLogger = hydratedLogger.With(
			"app_addr", session.Header.ApplicationAddress,
			"service_id", session.Header.ServiceId,
			"session_start_height", session.Header.SessionStartBlockHeight,
			"session_end_height", session.Header.SessionEndBlockHeight,
		)
	}

	return hydratedLogger
}

// hydrateLoggerWithPayload enhances a logger with payload details.
// Creates contextually rich logs with payload information.
//
// Parameters:
//   - logger: The base logger to enhance
//   - payload: The payload object containing request data
//
// Returns:
//   - An enhanced logger with all relevant payload fields attached
func hydrateLoggerWithPayload(
	logger polylog.Logger,
	payload *protocol.Payload,
) polylog.Logger {
	// Handle nil payload
	if payload == nil {
		return logger
	}

	// Add payload fields, using data length instead of full data content
	return logger.With(
		"payload_data_length", len(payload.Data),
		"payload_method", payload.Method,
		"payload_path", payload.Path,
		"payload_data_preview", log.Preview(payload.Data),
	)
}
