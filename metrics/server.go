package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const endpointMetrics = "/metrics"

// Starts a metrics server on the given address.
func (pmr *PrometheusMetricsReporter) ServeMetrics(addr string) error {
	// Use a dedicated ServeMux rather than http.DefaultServeMux. Importing
	// net/http/pprof (in pprof.go) registers the /debug/pprof/* handlers on
	// DefaultServeMux via its init(); serving DefaultServeMux here would expose
	// those profiling endpoints (heap dumps, goroutine stacks, CPU profiles) on
	// the public metrics port. A private mux keeps this server to /metrics only.
	mux := http.NewServeMux()
	mux.Handle(endpointMetrics, promhttp.Handler())

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// Start the server in a new goroutine
	go func() {
		pmr.Logger.Info().Str("endpoint_addr", addr).Msg("starting Prometheus reporter to serve metrics asynchronously.")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			pmr.Logger.Error().Err(err).Msg("prometheus metrics reporter failed starting server")
			return
		}
	}()

	return nil
}
