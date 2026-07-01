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

	// Build all fields then attach with a single .With call (one context clone).
	// Chaining a second .With for the header fields would clone the context
	// twice; this runs per session, per request.
	fields := []any{
		"session_id", session.SessionId,
		"session_number", session.SessionNumber,
		"num_blocks_per_session", session.NumBlocksPerSession,
		"supplier_count", len(session.Suppliers),
	}

	// Add session header details if available
	if session.Header != nil {
		fields = append(fields,
			"app_addr", session.Header.ApplicationAddress,
			"service_id", session.Header.ServiceId,
			"session_start_height", session.Header.SessionStartBlockHeight,
			"session_end_height", session.Header.SessionEndBlockHeight,
		)
	}

	return logger.With(fields...)
}
