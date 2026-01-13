package http

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/network/concurrency"
)

// TODO_IMPROVE: Make these configurable.
const (
	// Maximum length of an HTTP response's body.
	maxResponseSize = 100 * 1024 * 1024 // 100MB limit

	// Maximum number of concurrent HTTP requests.
	concurrencyLimiterMax = 10_000
)

// HTTPClientWithDebugMetrics provides HTTP client functionality with embedded tracking of debug metrics.
// It includes things like:
// - Built-in request debugging
// - Metrics collection
// - Detailed logging
// - Timeout debugging
// - Connection issue visibility
type HTTPClientWithDebugMetrics struct {
	httpClient *http.Client
	limiter    *concurrency.ConcurrencyLimiter
	bufferPool *concurrency.BufferPool

	// Atomic counters for monitoring
	activeRequests   atomic.Uint64
	totalRequests    atomic.Uint64
	timeoutErrors    atomic.Uint64
	connectionErrors atomic.Uint64
}

// httpRequestMetrics holds detailed timing and status information for a single HTTP request
type httpRequestMetrics struct {
	startTime      time.Time
	url            string
	contextTimeout time.Duration
	goroutineCount int

	// DNS Resolution
	dnsLookupTime time.Duration

	// Connection Establishment
	connectTime      time.Duration
	connectionReused bool
	remoteAddr       string
	localAddr        string

	// TLS Handshake
	tlsTime time.Duration

	// Connection Acquisition (from pool or new)
	getConnTime time.Duration

	// Request Writing
	wroteHeadersTime time.Duration
	wroteRequestTime time.Duration

	// Response Waiting
	firstByteTime time.Duration

	// Overall
	totalTime  time.Duration
	statusCode int
	error      error
}

// NewDefaultHTTPClientWithDebugMetrics creates a new HTTP client with:
// - Transport settings configured for high-concurrency usage
// - Built in request debugging capabilities and metrics tracking
// TODO_TECHDEBT(@adshmh): Make HTTP client settings configurable
func NewDefaultHTTPClientWithDebugMetrics() *HTTPClientWithDebugMetrics {
	// Configure transport with optimized settings for high-concurrency usage
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,  // Quick connection establishment to fail fast on unreachable hosts
			KeepAlive: 30 * time.Second, // Keep-alive probe interval to maintain connection health
		}).DialContext,

		// Connection pool settings scaled with concurrency limit
		MaxIdleConns:        concurrencyLimiterMax / 5,  // Scale total pool: 20% of max concurrency
		MaxIdleConnsPerHost: concurrencyLimiterMax / 20, // Scale per-host pool: 5% of max concurrency
		MaxConnsPerHost:     concurrencyLimiterMax / 10, // Scale max connections: 10% of max concurrency
		IdleConnTimeout:     90 * time.Second,           // Reduced from 300s - shorter idle to free resources

		// Timeout settings optimized for quick failure detection
		TLSHandshakeTimeout:   5 * time.Second,  // Fast TLS timeout since handshakes typically complete in ~100ms
		ResponseHeaderTimeout: 70 * time.Second, // Conservative header timeout to allow for server processing time

		// Performance optimizations
		DisableKeepAlives:  false, // Enable connection reuse to reduce connection overhead
		DisableCompression: false, // Enable gzip compression to reduce bandwidth
		ForceAttemptHTTP2:  true,  // Prefer HTTP/2 for connection multiplexing benefits

		// Buffer sizes optimized for throughput
		WriteBufferSize: 32 * 1024, // 32KB write buffer to reduce syscalls for large requests
		ReadBufferSize:  32 * 1024, // 32KB read buffer to reduce syscalls for large responses
	}

	// Create HTTP client with large timeout as fallback
	// Individual requests will use context deadlines for actual timeout control
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   80 * time.Second, // Large fallback timeout (80 seconds)
	}

	return &HTTPClientWithDebugMetrics{
		httpClient: httpClient,
		limiter:    concurrency.NewConcurrencyLimiter(concurrencyLimiterMax),
		bufferPool: concurrency.NewBufferPool(maxResponseSize),
	}
}

// SendHTTPRelay sends an HTTP POST request with the relay data to the specified URL.
// Uses the provided context for timeout and cancellation control.
// Logs detailed metrics and debugging information on failure for debugging.
//
// Returns: response body, HTTP status code, error
func (h *HTTPClientWithDebugMetrics) SendHTTPRelay(
	ctx context.Context,
	logger polylog.Logger,
	endpointURL string,
	method string,
	relayRequestBz []byte,
	headers map[string]string,
) ([]byte, int, error) {
	// Acquire concurrency slot before proceeding
	// TODO: use a predefined error, so we can build a proper observation indicating request failure due to reaching max concurrency.
	if !h.limiter.Acquire(ctx) {
		return nil, 0, fmt.Errorf("failed to acquire concurrency slot: context canceled")
	}
	defer h.limiter.Release()

	// Set up debugging context and logging function
	debugCtx, requestRecorder := h.setupRequestDebugging(ctx, logger, endpointURL)

	var requestErr error
	defer func() {
		requestRecorder(requestErr)
	}()

	// Validate URL format
	_, err := url.Parse(endpointURL)
	if err != nil {
		requestErr = fmt.Errorf("SHOULD NEVER HAPPEN: invalid URL: %w", err)
		return nil, 0, requestErr
	}

	req, err := http.NewRequestWithContext(
		debugCtx,
		method,
		endpointURL,
		bytes.NewReader(relayRequestBz),
	)
	if err != nil {
		requestErr = fmt.Errorf("failed to create HTTP request: %w", err)
		return nil, 0, requestErr
	}

	// TODO_TECHDEBT(@adshmh): Content-Type HTTP header should be set by the QoS.
	//
	// Set HTTP headers.
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// Execute HTTP request
	resp, err := h.httpClient.Do(req)
	if err != nil {
		requestErr = h.categorizeError(debugCtx, err)
		return nil, 0, requestErr
	}
	defer resp.Body.Close()

	// Read and validate response
	responseBody, err := h.readAndValidateResponse(resp)
	return responseBody, resp.StatusCode, err
}

// setupRequestDebugging initializes request metrics, HTTP debugging context, and atomic counters.
// Returns the debug context and a cleanup function that accepts an error parameter.
func (h *HTTPClientWithDebugMetrics) setupRequestDebugging(
	ctx context.Context,
	logger polylog.Logger,
	endpointURL string,
) (context.Context, func(error)) {
	// Update atomic counters
	h.activeRequests.Add(1)
	h.totalRequests.Add(1)

	startTime := time.Now()

	// Initialize metrics collection
	metrics := &httpRequestMetrics{
		startTime:      startTime,
		goroutineCount: runtime.NumGoroutine(),
		url:            endpointURL,
	}

	// Capture context timeout for logging
	if deadline, ok := ctx.Deadline(); ok {
		metrics.contextTimeout = time.Until(deadline)
	}

	// Create HTTP trace and add to context
	trace := createDetailedHTTPTrace(metrics)
	debugCtx := httptrace.WithClientTrace(ctx, trace)

	// Return recorder function that logs request details.
	requestRecorder := func(err error) {
		h.activeRequests.Add(^uint64(0)) // Atomic decrement
		metrics.totalTime = time.Since(metrics.startTime)
		metrics.error = err
		// Log detailed metrics on error for debugging
		if err != nil {
			h.logRequestMetrics(logger, *metrics)
		}
	}

	return debugCtx, requestRecorder
}

// categorizeError categorizes HTTP client errors and updates counters for monitoring
func (h *HTTPClientWithDebugMetrics) categorizeError(ctx context.Context, err error) error {
	if ctx.Err() == context.DeadlineExceeded {
		h.timeoutErrors.Add(1)
		return fmt.Errorf("request timeout: %w", err)
	} else {
		h.connectionErrors.Add(1)
		return fmt.Errorf("connection error: %w", err)
	}
}

// readAndValidateResponse reads the response body and validates the HTTP status code
func (h *HTTPClientWithDebugMetrics) readAndValidateResponse(resp *http.Response) ([]byte, error) {
	// Read response body with size protection using buffer pool
	responseBody, err := h.bufferPool.ReadWithBuffer(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Validate HTTP status code
	if err := EnsureHTTPSuccess(resp.StatusCode); err != nil {
		return responseBody, err
	}

	return responseBody, nil
}

// CheckHTTPStatusCode validates that the HTTP status code is in the range of 2xx.
func CheckHTTPStatusCode(statusCode int) error {
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%w: %d", ErrRelayEndpointHTTPError, statusCode)
	}
	return nil
}

// createDetailedHTTPTrace creates comprehensive HTTP tracing using the httptrace library:
// https://pkg.go.dev/net/http/httptrace
// Captures granular timing for every phase of the HTTP request lifecycle to identify bottlenecks.
func createDetailedHTTPTrace(metrics *httpRequestMetrics) *httptrace.ClientTrace {
	var (
		dnsStart, connectStart, tlsStart time.Time
		getConnStart, gotConnTime        time.Time
		wroteRequestStart                time.Time
		waitingForResponseStart          time.Time
	)

	return &httptrace.ClientTrace{
		// DNS Resolution Phase
		DNSStart: func(info httptrace.DNSStartInfo) {
			dnsStart = time.Now()
		},
		DNSDone: func(info httptrace.DNSDoneInfo) {
			if !dnsStart.IsZero() {
				metrics.dnsLookupTime = time.Since(dnsStart)
			}
		},

		// Connection Establishment Phase
		ConnectStart: func(network, addr string) {
			connectStart = time.Now()
			metrics.remoteAddr = addr
		},
		ConnectDone: func(network, addr string, err error) {
			if !connectStart.IsZero() {
				metrics.connectTime = time.Since(connectStart)
			}
		},

		// TLS Handshake Phase
		TLSHandshakeStart: func() {
			tlsStart = time.Now()
		},
		TLSHandshakeDone: func(state tls.ConnectionState, err error) {
			if !tlsStart.IsZero() {
				metrics.tlsTime = time.Since(tlsStart)
			}
		},

		// Connection Acquisition Phase
		// Tracks potential connection pool exhaustions.
		GetConn: func(hostPort string) {
			getConnStart = time.Now()
		},
		GotConn: func(info httptrace.GotConnInfo) {
			gotConnTime = time.Now() // Record when we actually got the connection
			if !getConnStart.IsZero() {
				metrics.getConnTime = time.Since(getConnStart) // This is pure connection acquisition time
			}
			metrics.connectionReused = info.Reused
			if info.Conn != nil {
				metrics.localAddr = info.Conn.LocalAddr().String()
			}
		},

		// Request Writing Phase
		// Tracks potential write delays.
		WroteHeaders: func() {
			// Time from getting connection to headers completion (not from connection start)
			if !gotConnTime.IsZero() {
				metrics.wroteHeadersTime = time.Since(gotConnTime)
			}
			// Start timing request body writing
			wroteRequestStart = time.Now()
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			// Entire request (headers + body) written successfully
			if !wroteRequestStart.IsZero() {
				metrics.wroteRequestTime = time.Since(wroteRequestStart)
			}
			// Now waiting for server response
			waitingForResponseStart = time.Now()
		},

		// Response Reading Phase
		GotFirstResponseByte: func() {
			if !waitingForResponseStart.IsZero() {
				metrics.firstByteTime = time.Since(waitingForResponseStart)
			}
		},
	}
}

// logRequestMetrics logs request metrics for debugging failed requests.
// Only called when a request fails. Uses DEBUG level to avoid log noise at high volume.
func (h *HTTPClientWithDebugMetrics) logRequestMetrics(logger polylog.Logger, metrics httpRequestMetrics) {
	// Log concise failure metrics at DEBUG level to avoid log spam on timeouts
	logger.Debug().
		Err(metrics.error).
		Str("url", metrics.url).
		Int64("total_ms", metrics.totalTime.Milliseconds()).
		Int64("timeout_ms", metrics.contextTimeout.Milliseconds()).
		Int("status_code", metrics.statusCode).
		Str("remote_addr", metrics.remoteAddr).
		Msg("HTTP request failed")
}
