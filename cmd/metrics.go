package main

import (
	"context"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/metrics"
)

// TODO_TECHDEBT(@adshmh): Support configurable pprof server address/port.
const (
	// pprofAddr is the address at which pprof server will be listening.
	// NOTE: This address was selected based on the example here:
	// https://pkg.go.dev/net/http/pprof
	pprofAddr = ":6060"

	// prometheusMetricsServerAddr is the address at which the prometheus metrics server will be listening.
	prometheusMetricsServerAddr = ":9090"
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
