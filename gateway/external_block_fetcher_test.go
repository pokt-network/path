package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog/polyzero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalBlockFetcher_EVMHex(t *testing.T) {
	// Mock server returning an EVM hex block height
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1940c6f5"}`))
	}))
	defer server.Close()

	logger := polyzero.NewLogger()
	configs := []ExternalBlockSourceConfig{{
		URL:      server.URL,
		Method:   "eth_blockNumber",
		Interval: 100 * time.Millisecond,
		Timeout:  2 * time.Second,
	}}

	fetcher := NewExternalBlockHeightFetcher(logger, configs)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	heights := fetcher.Start(ctx)

	// Should receive the parsed height
	select {
	case h := <-heights:
		// 0x1940c6f5 = 423,135,989
		assert.Equal(t, int64(0x1940c6f5), h)
	case <-ctx.Done():
		t.Fatal("timed out waiting for block height")
	}
}

func TestExternalBlockFetcher_SolanaNumeric(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":123456789}`))
	}))
	defer server.Close()

	logger := polyzero.NewLogger()
	configs := []ExternalBlockSourceConfig{{
		URL:      server.URL,
		Method:   "getBlockHeight",
		Interval: 100 * time.Millisecond,
		Timeout:  2 * time.Second,
	}}

	fetcher := NewExternalBlockHeightFetcher(logger, configs)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	heights := fetcher.Start(ctx)

	select {
	case h := <-heights:
		assert.Equal(t, int64(123456789), h)
	case <-ctx.Done():
		t.Fatal("timed out waiting for block height")
	}
}

func TestExternalBlockFetcher_CosmosDecimal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"sync_info":{"latest_block_height":"9876543"}}}`))
	}))
	defer server.Close()

	logger := polyzero.NewLogger()
	configs := []ExternalBlockSourceConfig{{
		URL:      server.URL,
		Method:   "status",
		Interval: 100 * time.Millisecond,
		Timeout:  2 * time.Second,
	}}

	fetcher := NewExternalBlockHeightFetcher(logger, configs)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	heights := fetcher.Start(ctx)

	select {
	case h := <-heights:
		assert.Equal(t, int64(9876543), h)
	case <-ctx.Done():
		t.Fatal("timed out waiting for block height")
	}
}

func TestExternalBlockFetcher_MultipleSources_TakesMax(t *testing.T) {
	// Source 1: lower height
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x64"}`)) // 100
	}))
	defer server1.Close()

	// Source 2: higher height
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xC8"}`)) // 200
	}))
	defer server2.Close()

	logger := polyzero.NewLogger()
	configs := []ExternalBlockSourceConfig{
		{URL: server1.URL, Method: "eth_blockNumber", Interval: 100 * time.Millisecond, Timeout: 2 * time.Second},
		{URL: server2.URL, Method: "eth_blockNumber", Interval: 100 * time.Millisecond, Timeout: 2 * time.Second},
	}

	fetcher := NewExternalBlockHeightFetcher(logger, configs)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	heights := fetcher.Start(ctx)

	select {
	case h := <-heights:
		assert.Equal(t, int64(200), h)
	case <-ctx.Done():
		t.Fatal("timed out waiting for block height")
	}
}

func TestExternalBlockFetcher_SourceDown_FallsBackToOther(t *testing.T) {
	// Source 1: unreachable (closed immediately)
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server1.Close() // Close immediately to make it unreachable

	// Source 2: working
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xC8"}`)) // 200
	}))
	defer server2.Close()

	logger := polyzero.NewLogger()
	configs := []ExternalBlockSourceConfig{
		{URL: server1.URL, Method: "eth_blockNumber", Interval: 100 * time.Millisecond, Timeout: 1 * time.Second},
		{URL: server2.URL, Method: "eth_blockNumber", Interval: 100 * time.Millisecond, Timeout: 1 * time.Second},
	}

	fetcher := NewExternalBlockHeightFetcher(logger, configs)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	heights := fetcher.Start(ctx)

	select {
	case h := <-heights:
		assert.Equal(t, int64(200), h)
	case <-ctx.Done():
		t.Fatal("timed out waiting for block height")
	}
}

func TestExternalBlockFetcher_AllSourcesDown_NoOutput(t *testing.T) {
	// Both sources are unreachable
	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server1.Close()
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server2.Close()

	logger := polyzero.NewLogger()
	configs := []ExternalBlockSourceConfig{
		{URL: server1.URL, Method: "eth_blockNumber", Interval: 100 * time.Millisecond, Timeout: 500 * time.Millisecond},
		{URL: server2.URL, Method: "eth_blockNumber", Interval: 100 * time.Millisecond, Timeout: 500 * time.Millisecond},
	}

	fetcher := NewExternalBlockHeightFetcher(logger, configs)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	heights := fetcher.Start(ctx)

	// Should not receive any height (all sources down)
	select {
	case h := <-heights:
		t.Fatalf("expected no height, got %d", h)
	case <-time.After(2 * time.Second):
		// Expected: no output
	}

	cancel()
	// Channel should eventually close
	_, ok := <-heights
	require.False(t, ok, "channel should be closed after context cancel")
}

func TestExternalBlockFetcher_DefaultConfig(t *testing.T) {
	logger := polyzero.NewLogger()
	configs := []ExternalBlockSourceConfig{{
		URL: "http://localhost:12345",
		// Method, Path, Interval, Timeout all empty → should use defaults
	}}

	fetcher := NewExternalBlockHeightFetcher(logger, configs)

	assert.Equal(t, defaultExternalBlockInterval, fetcher.interval)
	assert.Equal(t, defaultExternalBlockMethod, fetcher.configs[0].Method)
	assert.Equal(t, defaultExternalBlockPath, fetcher.configs[0].Path)
}
