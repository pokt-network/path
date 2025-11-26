package main

import (
	"fmt"
	"net/url"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/config"
	"github.com/pokt-network/path/data"
	"github.com/pokt-network/path/gateway"
)

// setupHTTPDataReporter initializes and starts the HTTP data reporter.
func setupHTTPDataReporter(
	logger polylog.Logger,
	config config.HTTPDataReporterConfig,
) (gateway.RequestResponseReporter, error) {
	if config.TargetURL == "" {
		logger.Warn().Msg("Target URL not specified for the HTTP data reporter: request data will not be reported.")
		return nil, nil
	}

	// Error parsing the specified target URL.
	if _, err := url.Parse(config.TargetURL); err != nil {
		return nil, fmt.Errorf("error processing the HTTP Data Reporter's target URL: %w", err)
	}

	return &data.DataReporterHTTP{
		Logger:           logger,
		DataProcessorURL: config.TargetURL,
		PostTimeoutMS:    config.PostTimeoutMS,
	}, nil
}
