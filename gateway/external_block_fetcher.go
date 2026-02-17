package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
)

const (
	defaultExternalBlockInterval = 30 * time.Second
	defaultExternalBlockTimeout  = 5 * time.Second
	defaultExternalBlockMethod   = "eth_blockNumber"
	defaultExternalBlockPath     = "/"
)

// ExternalBlockSourceConfig holds the resolved configuration for a single external
// block height source. Multiple sources can be configured per service for redundancy.
type ExternalBlockSourceConfig struct {
	URL      string        // RPC endpoint URL
	Method   string        // RPC method (e.g., "eth_blockNumber", "status", "getBlockHeight")
	Path     string        // Request path (e.g., "/", "/jsonrpc"). Default: "/"
	Interval time.Duration // Poll interval. Default: 30s
	Timeout  time.Duration // HTTP timeout. Default: 5s
}

// ExternalBlockHeightFetcher periodically queries one or more external RPC endpoints
// for the ground-truth block height. When multiple sources are configured, it takes
// the maximum across all sources for resilience — if one source is down or lagging,
// others compensate.
type ExternalBlockHeightFetcher struct {
	logger     polylog.Logger
	httpClient *http.Client
	configs    []ExternalBlockSourceConfig
	interval   time.Duration
}

// NewExternalBlockHeightFetcher creates a new fetcher for the given external block sources.
// The poll interval is taken from the first config (all sources are polled together).
func NewExternalBlockHeightFetcher(logger polylog.Logger, configs []ExternalBlockSourceConfig) *ExternalBlockHeightFetcher {
	// Apply defaults and determine poll interval
	interval := defaultExternalBlockInterval
	timeout := defaultExternalBlockTimeout

	for i := range configs {
		if configs[i].Method == "" {
			configs[i].Method = defaultExternalBlockMethod
		}
		if configs[i].Path == "" {
			configs[i].Path = defaultExternalBlockPath
		}
		if configs[i].Interval > 0 && configs[i].Interval < interval {
			interval = configs[i].Interval
		}
		if configs[i].Timeout > 0 && configs[i].Timeout > timeout {
			timeout = configs[i].Timeout
		}
	}

	return &ExternalBlockHeightFetcher{
		logger: logger,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		configs:  configs,
		interval: interval,
	}
}

// Start begins periodic fetching from all configured external sources.
// Returns a channel of block heights (the max across all sources per tick).
// The caller (QoS instance) consumes from the channel to update its consensus.
// The channel is closed when the context is canceled.
func (f *ExternalBlockHeightFetcher) Start(ctx context.Context) <-chan int64 {
	heights := make(chan int64, 1)

	go func() {
		defer close(heights)

		// Immediate fetch on startup
		if h := f.fetchMax(ctx); h > 0 {
			select {
			case heights <- h:
			case <-ctx.Done():
				return
			}
		}

		ticker := time.NewTicker(f.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if h := f.fetchMax(ctx); h > 0 {
					select {
					case heights <- h:
					default:
						// Channel full — consumer hasn't read yet, skip this tick
					}
				}
			}
		}
	}()

	return heights
}

// fetchMax queries all configured sources and returns the maximum block height.
// Returns 0 if all sources fail.
func (f *ExternalBlockHeightFetcher) fetchMax(ctx context.Context) int64 {
	var maxHeight int64

	for _, cfg := range f.configs {
		height, err := f.fetchOne(ctx, cfg)
		if err != nil {
			f.logger.Warn().
				Err(err).
				Str("url", cfg.URL).
				Str("method", cfg.Method).
				Msg("External block source fetch failed — skipping")
			continue
		}
		if height > maxHeight {
			maxHeight = height
		}
	}

	return maxHeight
}

// fetchOne queries a single external RPC endpoint for its block height.
func (f *ExternalBlockHeightFetcher) fetchOne(ctx context.Context, cfg ExternalBlockSourceConfig) (int64, error) {
	// Build JSON-RPC request body
	reqBody, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  cfg.Method,
		"params":  []interface{}{},
	})
	if err != nil {
		return 0, fmt.Errorf("marshaling request: %w", err)
	}

	// Build the full URL
	url := cfg.URL
	if cfg.Path != "/" && cfg.Path != "" {
		url = cfg.URL + cfg.Path
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16)) // 64KB limit
	if err != nil {
		return 0, fmt.Errorf("reading response: %w", err)
	}

	// Reuse the existing extractBlockHeight() from health_check_executor.go
	// which handles EVM hex, Cosmos decimal, and Solana numeric formats.
	height, err := extractBlockHeight(body)
	if err != nil {
		return 0, fmt.Errorf("parsing block height from %s: %w", url, err)
	}

	return height, nil
}
