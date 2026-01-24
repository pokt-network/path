package shannon

import (
	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/x/session/types"
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
