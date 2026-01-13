package main

import (
	"context"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics"
)

// setupMetricsServer initializes and starts the Prometheus metrics server at the supplied address.
func setupMetricsServer(logger polylog.Logger, addr string) (*metrics.PrometheusMetricsReporter, error) {
	pmr := &metrics.PrometheusMetricsReporter{
		Logger: logger,
	}

	if err := pmr.ServeMetrics(addr); err != nil {
		return nil, err
	}

	return pmr, nil
}

// setupPprofServer starts the metric package's pprof server, at the supplied address.
func setupPprofServer(ctx context.Context, logger polylog.Logger, addr string) {
	metrics.ServePprof(ctx, logger, addr)
}
