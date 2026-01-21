package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
)

// ExternalReferenceCache caches block heights from external endpoints.
// This prevents hammering external endpoints (like Alchemy, Infura) on every health check.
type ExternalReferenceCache struct {
	mu     sync.RWMutex
	cache  map[string]*cachedBlockHeight
	logger polylog.Logger
	client *http.Client
}

// cachedBlockHeight represents a cached block height value.
type cachedBlockHeight struct {
	height    int64
	fetchedAt time.Time
	ttl       time.Duration
}

// NewExternalReferenceCache creates a new external reference cache.
func NewExternalReferenceCache(logger polylog.Logger) *ExternalReferenceCache {
	return &ExternalReferenceCache{
		cache:  make(map[string]*cachedBlockHeight),
		logger: logger.With("component", "external_reference_cache"),
		client: &http.Client{
			Timeout: 10 * time.Second, // Max timeout for external requests
		},
	}
}

// GetBlockHeight fetches the block height from an external endpoint, using cache if available.
// Returns the block height and an error if the fetch fails.
func (c *ExternalReferenceCache) GetBlockHeight(
	ctx context.Context,
	endpoint string,
	method string,
	headers map[string]string,
	timeout time.Duration,
	cacheDuration time.Duration,
) (int64, error) {
	// Create cache key from endpoint + method
	cacheKey := fmt.Sprintf("%s:%s", endpoint, method)

	// Check cache first
	c.mu.RLock()
	cached, exists := c.cache[cacheKey]
	c.mu.RUnlock()

	if exists && time.Since(cached.fetchedAt) < cached.ttl {
		c.logger.Debug().
			Str("endpoint", endpoint).
			Str("method", method).
			Int64("height", cached.height).
			Dur("age", time.Since(cached.fetchedAt)).
			Msg("Using cached external reference block height")
		return cached.height, nil
	}

	// Cache miss or expired - fetch from external endpoint
	height, err := c.fetchBlockHeight(ctx, endpoint, method, headers, timeout)
	if err != nil {
		return 0, err
	}

	// Store in cache
	c.mu.Lock()
	c.cache[cacheKey] = &cachedBlockHeight{
		height:    height,
		fetchedAt: time.Now(),
		ttl:       cacheDuration,
	}
	c.mu.Unlock()

	c.logger.Debug().
		Str("endpoint", endpoint).
		Str("method", method).
		Int64("height", height).
		Dur("cache_duration", cacheDuration).
		Msg("Fetched and cached external reference block height")

	return height, nil
}

// fetchBlockHeight queries an external endpoint for the current block height.
func (c *ExternalReferenceCache) fetchBlockHeight(
	ctx context.Context,
	endpoint string,
	method string,
	headers map[string]string,
	timeout time.Duration,
) (int64, error) {
	// Create timeout context
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build JSON-RPC request
	requestBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  []interface{}{},
		"id":      1,
	}

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(timeoutCtx, "POST", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Execute request
	startTime := time.Now()
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	latency := time.Since(startTime)

	// Check status code
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse JSON-RPC response
	var jsonResp struct {
		Result string `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(respBody, &jsonResp); err != nil {
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}

	// Check for JSON-RPC error
	if jsonResp.Error != nil {
		return 0, fmt.Errorf("JSON-RPC error: %s (code %d)", jsonResp.Error.Message, jsonResp.Error.Code)
	}

	// Parse hex block number
	height, err := parseHexBlockNumber(jsonResp.Result)
	if err != nil {
		return 0, fmt.Errorf("failed to parse block number: %w", err)
	}

	c.logger.Debug().
		Str("endpoint", endpoint).
		Str("method", method).
		Int64("height", height).
		Dur("latency", latency).
		Msg("Successfully fetched block height from external endpoint")

	return height, nil
}

// parseHexBlockNumber parses a hex string (e.g., "0x1940c6f5") to int64.
func parseHexBlockNumber(hexStr string) (int64, error) {
	// Remove "0x" prefix if present
	hexStr = strings.TrimPrefix(hexStr, "0x")
	hexStr = strings.TrimPrefix(hexStr, "0X")

	// Parse as base 16
	value, err := strconv.ParseInt(hexStr, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid hex number %q: %w", hexStr, err)
	}

	return value, nil
}

// ClearCache clears all cached block heights (for testing).
func (c *ExternalReferenceCache) ClearCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]*cachedBlockHeight)
}
