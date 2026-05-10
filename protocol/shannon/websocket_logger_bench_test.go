package shannon

import (
	"io"
	"testing"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"

	"github.com/pokt-network/path/protocol"
)

// BenchmarkWebsocketLogger_PerMessage compares the legacy per-message
// `hydratedLogger` (which mutated wrc.logger and re-added 5–6 fields per call,
// growing the logger context over the lifetime of a connection) against the
// current `methodLogger` pattern (one .With("method", ...) call over a
// connection-level logger that's built once at setup).
//
// The logger is set to InfoLevel and discards output, so .Debug() short-circuits
// — this isolates the cost of the logger-construction pattern itself, which
// is what the production hot path (websocket message handling) is doing.
//
// Run with:
//
//	go test -run='^$' -bench=BenchmarkWebsocketLogger -benchmem ./protocol/shannon/
func BenchmarkWebsocketLogger_PerMessage(b *testing.B) {
	base := newSilentLogger()
	ep := &mockEndpoint{addr: "supplier1-https://relay.example.com:443/json-rpc"}
	const serviceID = protocol.ServiceID("eth")

	b.Run("legacy_hydratedLogger_mutating", func(b *testing.B) {
		wrc := &websocketRequestContext{
			logger: base.With(
				"method", "BuildWebsocketRequestContextForEndpoint",
				"service_id", serviceID,
				"endpoint_addr", ep.Addr(),
			),
			serviceID:        serviceID,
			selectedEndpoint: ep,
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			legacyHydratedLogger(wrc, "ProcessEndpointWebsocketMessage")
			wrc.logger.Debug().Msg("received message from endpoint")
		}
	})

	b.Run("fixed_methodLogger", func(b *testing.B) {
		wrc := &websocketRequestContext{
			logger:           buildWebsocketConnectionLogger(base, serviceID, ep),
			serviceID:        serviceID,
			selectedEndpoint: ep,
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			logger := wrc.methodLogger("ProcessEndpointWebsocketMessage")
			logger.Debug().Msg("received message from endpoint")
		}
	})
}

// BenchmarkWebsocketLogger_LongLivedConnection demonstrates the unbounded
// logger-context growth in the legacy pattern: by re-adding fields and
// reassigning wrc.logger every message, the underlying zerolog context buffer
// grows in size with each call. After many messages on the same connection,
// every subsequent .With() copies a much larger buffer.
//
// In the fixed pattern, the connection logger is hydrated once and never
// mutated, so per-message cost is constant regardless of connection age.
func BenchmarkWebsocketLogger_LongLivedConnection(b *testing.B) {
	base := newSilentLogger()
	ep := &mockEndpoint{addr: "supplier1-https://relay.example.com:443/json-rpc"}
	const serviceID = protocol.ServiceID("eth")

	// "Aged" = simulate 10k messages already processed on this connection
	// before measurement begins. The legacy logger should be much fatter at
	// this point; the fixed logger should be unchanged.
	const ageMessages = 10_000

	b.Run("legacy_after_10k_messages", func(b *testing.B) {
		wrc := &websocketRequestContext{
			logger: base.With(
				"method", "BuildWebsocketRequestContextForEndpoint",
				"service_id", serviceID,
				"endpoint_addr", ep.Addr(),
			),
			serviceID:        serviceID,
			selectedEndpoint: ep,
		}
		for i := 0; i < ageMessages; i++ {
			legacyHydratedLogger(wrc, "ProcessEndpointWebsocketMessage")
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			legacyHydratedLogger(wrc, "ProcessEndpointWebsocketMessage")
			wrc.logger.Debug().Msg("received message from endpoint")
		}
	})

	b.Run("fixed_after_10k_messages", func(b *testing.B) {
		wrc := &websocketRequestContext{
			logger:           buildWebsocketConnectionLogger(base, serviceID, ep),
			serviceID:        serviceID,
			selectedEndpoint: ep,
		}
		for i := 0; i < ageMessages; i++ {
			logger := wrc.methodLogger("ProcessEndpointWebsocketMessage")
			_ = logger
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			logger := wrc.methodLogger("ProcessEndpointWebsocketMessage")
			logger.Debug().Msg("received message from endpoint")
		}
	})
}

// newSilentLogger returns a logger that discards output and only emits at
// InfoLevel or above — so .Debug() in the benchmark loops short-circuits and
// we measure the logger-construction cost, not output formatting.
func newSilentLogger() polylog.Logger {
	return polyzero.NewLogger(
		polyzero.WithLevel(polyzero.InfoLevel),
		polyzero.WithOutput(io.Discard),
	)
}

// legacyHydratedLogger is a verbatim copy of the pre-fix `hydratedLogger`
// implementation — kept here only so the benchmark can compare the old
// behavior against the new one. Do NOT call from production code.
func legacyHydratedLogger(wrc *websocketRequestContext, methodName string) {
	logger := wrc.logger.With(
		"request_type", "websocket",
		"method", methodName,
		"service_id", wrc.serviceID,
	)

	defer func() {
		wrc.logger = logger
	}()

	selectedEndpoint := wrc.selectedEndpoint
	if selectedEndpoint == nil {
		return
	}

	logger = logger.With(
		"selected_endpoint_supplier", selectedEndpoint.Supplier(),
		"selected_endpoint_url", selectedEndpoint.PublicURL(),
	)

	sessionHeader := selectedEndpoint.Session().Header
	if sessionHeader == nil {
		return
	}

	logger = logger.With(
		"selected_endpoint_app", sessionHeader.ApplicationAddress,
	)
}
