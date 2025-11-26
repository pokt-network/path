//go:build e2e

// Package e2e provides logging, formatting, and progress bar utilities for PATH E2E tests.
//
// This file contains all visual output helpers, ANSI color constants, and progress bar
// management for both HTTP and Websocket testing modes. It handles terminal formatting,
// colored output, and interactive progress visualization during load tests.
//
// VISUAL OUTPUT FEATURES:
// - ANSI color constants for terminal output (RED, GREEN, YELLOW, BLUE, etc.)
// - Color helper functions for success rates, latency values, and HTTP status codes
// - Progress bar management with automatic CI environment detection
// - Formatted latency display (milliseconds)
// - Visual feedback for long-running test operations
//
// PROGRESS BAR SYSTEM:
// - Automatically disabled in CI environments for clean log output
// - Supports multiple concurrent progress bars for parallel method testing
// - Custom formatting with method names, counters, and percentage completion
// - Graceful fallback to text-based progress when bars can't be created
//
// This separation ensures clean visual presentation while keeping display logic
// separate from test execution, calculation, and validation concerns.

package e2e

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/qos/jsonrpc"
)

// ===== ANSI Color Constants (for log output) =====
const (
	RED       = "\x1b[31m"
	GREEN     = "\x1b[32m"
	YELLOW    = "\x1b[33m"
	BLUE      = "\x1b[34m"
	CYAN      = "\x1b[36m"
	BOLD      = "\x1b[1m"
	BOLD_BLUE = "\x1b[1m\x1b[34m"
	BOLD_CYAN = "\x1b[1m\x1b[36m"
	RESET     = "\x1b[0m"
)

// ===== Service Summary Printing =====

// printServiceSummaries prints a summary table of all services after tests complete
func printServiceSummaries(summaries map[protocol.ServiceID]*serviceSummary) {
	fmt.Printf("\n\n%s===== SERVICE SUMMARY =====%s\n", BOLD_CYAN, RESET)

	// Sort service IDs for consistent output
	serviceIDs := make([]protocol.ServiceID, 0, len(summaries))
	for svcID := range summaries {
		serviceIDs = append(serviceIDs, svcID)
	}
	sort.Slice(serviceIDs, func(i, j int) bool {
		return string(serviceIDs[i]) < string(serviceIDs[j])
	})

	rows := make([]serviceRow, 0, len(serviceIDs))

	for _, svcID := range serviceIDs {
		summary := summaries[svcID]
		serviceConfig := summary.ServiceConfig

		successEmoji := getRateEmoji(summary.AvgSuccessRate, serviceConfig.SuccessRate)

		maxFailureRate := 1.0 - serviceConfig.SuccessRate
		maxAllowedFailures := int(float64(summary.TotalRequests) * maxFailureRate)
		failuresStr := fmt.Sprintf("%d", summary.TotalFailure)
		if summary.TotalFailure > 0 {
			failuresStr += fmt.Sprintf(" (max allowed: %d)", maxAllowedFailures)
		}

		rows = append(rows, serviceRow{
			service:      string(svcID),
			status:       successEmoji,
			totalReq:     summary.TotalRequests,
			totalSuccess: summary.TotalSuccess,
			failuresStr:  failuresStr,
			successRate:  fmt.Sprintf("%.2f%%", summary.AvgSuccessRate*100),
			p50Latency:   formatLatency(summary.AvgP50Latency),
			p90Latency:   formatLatency(summary.AvgP90Latency),
			avgLatency:   formatLatency(summary.AvgLatency),
		})
	}

	// Sort rows by success rate, then latency
	sortServiceRows(rows)

	// Print table
	printServiceTable(rows)

	fmt.Printf("\n%s===== END SERVICE SUMMARY =====%s\n", BOLD_CYAN, RESET)
}

// printServiceTable prints the formatted service summary table
func printServiceTable(rows []serviceRow) {
	fmt.Printf("| %-16s | %-6s | %-8s | %-9s | %-20s | %-12s | %-11s | %-11s | %-11s |\n",
		"Service", "Status", "Requests", "Successes", "Failures", "Success Rate", "P50 Latency", "P90 Latency", "Avg Latency")
	fmt.Printf("|------------------|--------|----------|-----------|----------------------|--------------|-------------|-------------|-------------|\n")
	for _, r := range rows {
		fmt.Printf("| %-16s | %-6s | %-8d | %-9d | %-20s | %-12s | %-11s | %-11s | %-11s |\n",
			r.service, r.status, r.totalReq, r.totalSuccess, r.failuresStr, r.successRate, r.p50Latency, r.p90Latency, r.avgLatency)
	}
}

// ===== Helper Functions for Colors and Formatting =====

// getSuccessRateColor returns color based on success rate
func getSuccessRateColor(rate float64) string {
	if rate >= 0.90 {
		return GREEN
	} else if rate >= 0.70 {
		return YELLOW
	}
	return RED
}

// getRateColor returns color for success rates compared to required rate
func getRateColor(rate, requiredRate float64) string {
	if rate >= requiredRate {
		return GREEN
	} else if rate >= requiredRate*0.50 {
		return YELLOW
	}
	return RED
}

// getRateEmoji returns emoji for success rates
func getRateEmoji(rate, requiredRate float64) string {
	if rate >= requiredRate {
		return "🟢"
	} else if rate >= requiredRate*0.50 {
		return "🟡"
	}
	return "🔴"
}

// getLatencyColor returns color for latency values
func getLatencyColor(actual, maxAllowed time.Duration) string {
	if float64(actual) <= float64(maxAllowed)*0.5 {
		return GREEN // Well under limit
	} else if float64(actual) <= float64(maxAllowed) {
		return YELLOW // Close to limit
	}
	return RED // Over limit
}

// getStatusCodeColor returns color based on HTTP status code
func getStatusCodeColor(code int) string {
	if code >= 200 && code < 300 {
		return GREEN // 2xx success
	} else if code >= 300 && code < 400 {
		return YELLOW // 3xx redirect
	}
	return RED // 4xx/5xx error
}

// formatLatency formats latency values to whole milliseconds
func formatLatency(d time.Duration) string {
	return fmt.Sprintf("%dms", d/time.Millisecond)
}

// ===== Method Metrics Printing =====

// printMethodMetrics prints formatted metrics for a method
func printMethodMetrics(
	serviceID protocol.ServiceID,
	m *methodMetrics,
	config ServiceConfig,
	serviceParams ServiceParams,
) {
	// Add a blank line before each test result for better readability
	fmt.Println()

	// Print metrics header with method name in blue
	fmt.Printf("%s~~~~~~~~~~ %s - %s ~~~~~~~~%s\n", BOLD_CYAN, serviceID, m.method, RESET)

	// Print the actual JSON-RPC request for this method
	printJSONRPCRequest(m.method, serviceParams)

	// Print HTTP success rate with color
	successColor := getSuccessRateColor(m.successRate)
	fmt.Printf("%sHTTP Success Rate%s: %s%.2f%%%s (%d/%d requests)\n",
		BOLD, RESET, successColor, m.successRate*100, RESET, m.success, m.requestCount)

	// Print latencies with color
	p50Color := getLatencyColor(m.p50, config.MaxP50LatencyMS)
	p95Color := getLatencyColor(m.p95, config.MaxP95LatencyMS)
	p99Color := getLatencyColor(m.p99, config.MaxP99LatencyMS)
	fmt.Printf("%sLatency P50%s: %s%s%s\n", BOLD, RESET, p50Color, formatLatency(m.p50), RESET)
	fmt.Printf("%sLatency P95%s: %s%s%s\n", BOLD, RESET, p95Color, formatLatency(m.p95), RESET)
	fmt.Printf("%sLatency P99%s: %s%s%s\n", BOLD, RESET, p99Color, formatLatency(m.p99), RESET)

	// Print JSON-RPC metrics if applicable
	printJSONRPCMetrics(m, config)

	// Print status codes
	printStatusCodes(m)

	// Print errors if any
	printErrors(m, config)
}

// printJSONRPCRequest constructs and displays the request for a given method
func printJSONRPCRequest(method string, serviceParams ServiceParams) {
	// Check if this is a REST endpoint (starts with "/")
	if isRESTEndpoint(method) {
		// For REST endpoints, show HTTP method and path
		fmt.Printf("%sGET %s%s\n\n", CYAN, method, RESET)
		return
	}

	var request jsonrpc.Request

	// Construct the request based on method type
	switch {
	// Check if this is an EVM method
	case isEVMMethod(method):
		request = jsonrpc.Request{
			JSONRPC: jsonrpc.Version2,
			ID:      jsonrpc.IDFromInt(1),
			Method:  jsonrpc.Method(method),
			Params:  createEVMJsonRPCParams(jsonrpc.Method(method), serviceParams),
		}
	// Check if this is a Solana method
	case isSolanaMethod(method):
		request = jsonrpc.Request{
			JSONRPC: jsonrpc.Version2,
			ID:      jsonrpc.IDFromInt(1),
			Method:  jsonrpc.Method(method),
			Params:  createSolanaJsonRPCParams(jsonrpc.Method(method), serviceParams),
		}
	default:
		// For unknown methods, just show basic structure
		request = jsonrpc.Request{
			JSONRPC: jsonrpc.Version2,
			ID:      jsonrpc.IDFromInt(1),
			Method:  jsonrpc.Method(method),
			Params:  jsonrpc.Params{},
		}
	}

	// Marshal to JSON for display (compact single-line format for easy copy-paste)
	requestBytes, err := json.Marshal(request)
	if err != nil {
		fmt.Printf("%s{\"error\": \"failed to marshal request: %v\"}%s\n\n", YELLOW, err, RESET)
		return
	}

	fmt.Printf("%s%s%s\n\n", CYAN, string(requestBytes), RESET)
}

// printJSONRPCMetrics prints JSON-RPC specific metrics
func printJSONRPCMetrics(m *methodMetrics, config ServiceConfig) {
	if m.jsonrpcResponses+m.jsonrpcUnmarshalErrors == 0 {
		return
	}

	fmt.Printf("%sJSON-RPC Metrics:%s\n", BOLD, RESET)

	if m.jsonrpcResponses > 0 {
		// Unmarshal success rate
		color := getRateColor(m.jsonrpcSuccessRate, config.SuccessRate)
		fmt.Printf("  Unmarshal Success: %s%.2f%%%s (%d/%d responses)\n",
			color, m.jsonrpcSuccessRate*100, RESET, m.jsonrpcResponses, m.jsonrpcResponses+m.jsonrpcUnmarshalErrors)

		// Validation success rate
		color = getRateColor(m.jsonrpcValidateRate, config.SuccessRate)
		fmt.Printf("  Validation Success: %s%.2f%%%s (%d/%d responses)\n",
			color, m.jsonrpcValidateRate*100, RESET, m.jsonrpcResponses-m.jsonrpcValidateErrors, m.jsonrpcResponses)

		// Non-nil result rate
		color = getRateColor(m.jsonrpcResultRate, config.SuccessRate)
		fmt.Printf("  Has Result: %s%.2f%%%s (%d/%d responses)\n",
			color, m.jsonrpcResultRate*100, RESET, m.jsonrpcResponses-m.jsonrpcNilResult, m.jsonrpcResponses)

		// Error field absent rate
		color = getRateColor(m.jsonrpcErrorFieldRate, config.SuccessRate)
		fmt.Printf("  Does Not Have Error: %s%.2f%%%s (%d/%d responses)\n",
			color, m.jsonrpcErrorFieldRate*100, RESET, m.jsonrpcResponses-m.jsonrpcErrorField, m.jsonrpcResponses)
	}
}

// printStatusCodes prints HTTP status code distribution
func printStatusCodes(m *methodMetrics) {
	if len(m.statusCodes) == 0 {
		return
	}

	statusText := "Status Codes:"
	for code, count := range m.statusCodes {
		codeColor := getStatusCodeColor(code)
		statusText += fmt.Sprintf("\n  %s%d%s: %d", codeColor, code, RESET, count)
	}
	fmt.Println(statusText)
}

// printErrors prints top errors with appropriate coloring
func printErrors(m *methodMetrics, config ServiceConfig) {
	if len(m.errors) == 0 {
		return
	}

	// Determine if the test passed
	testPassed := m.successRate >= config.SuccessRate &&
		m.p50 <= config.MaxP50LatencyMS &&
		m.p95 <= config.MaxP95LatencyMS &&
		m.p99 <= config.MaxP99LatencyMS

	// Choose error color based on test status
	errorColor := YELLOW // Yellow for warnings
	if !testPassed {
		errorColor = RED // Red for critical errors
	}

	fmt.Println("") // Add a new line before logging errors
	fmt.Printf("%sTop errors:%s\n", errorColor, RESET)

	// Sort errors by count (descending)
	type errorEntry struct {
		message string
		count   int
	}
	var sortedErrors []errorEntry
	for errMsg, count := range m.errors {
		sortedErrors = append(sortedErrors, errorEntry{message: errMsg, count: count})
	}
	sort.Slice(sortedErrors, func(i, j int) bool {
		return sortedErrors[i].count > sortedErrors[j].count
	})

	// Display top 5 errors
	for i, err := range sortedErrors {
		if i >= 5 {
			break
		}
		fmt.Printf("  %d. %s%s%s: %d\n", i+1, errorColor, err.message, RESET, err.count)
	}

	if len(sortedErrors) > 5 {
		fmt.Printf("  ... and %s%d%s more error types\n", errorColor, len(sortedErrors)-5, RESET)
	}
}

// Helper functions to determine method types
func isEVMMethod(method string) bool {
	evmMethods := getEVMTestMethods()
	for _, evmMethod := range evmMethods {
		if evmMethod == method {
			return true
		}
	}
	return false
}

func isSolanaMethod(method string) bool {
	solanaMethods := getSolanaTestMethods()
	for _, solanaMethod := range solanaMethods {
		if solanaMethod == method {
			return true
		}
	}
	return false
}

func isRESTEndpoint(method string) bool {
	// REST endpoints start with "/" (e.g., "/cosmos/distribution/v1beta1/params")
	return strings.HasPrefix(method, "/")
}

// ===== Service Summary Row Management =====

// serviceRow represents a row in the service summary table
type serviceRow struct {
	service      string
	status       string
	totalReq     int
	totalSuccess int
	failuresStr  string
	successRate  string
	p50Latency   string
	p90Latency   string
	avgLatency   string
}

// sortServiceRows sorts service summary rows by success rate and latency
func sortServiceRows(rows []serviceRow) {
	sort.Slice(rows, func(i, j int) bool {
		// Parse success rates
		srI, _ := strconv.ParseFloat(strings.TrimSuffix(rows[i].successRate, "%"), 64)
		srJ, _ := strconv.ParseFloat(strings.TrimSuffix(rows[j].successRate, "%"), 64)
		if srI != srJ {
			return srI > srJ // Descending success rate
		}
		// Parse P50 latency
		p50I, _ := strconv.Atoi(strings.TrimSuffix(rows[i].p50Latency, "ms"))
		p50J, _ := strconv.Atoi(strings.TrimSuffix(rows[j].p50Latency, "ms"))
		if p50I != p50J {
			return p50I < p50J // Ascending P50 latency
		}
		// Parse P90 latency
		p90I, _ := strconv.Atoi(strings.TrimSuffix(rows[i].p90Latency, "ms"))
		p90J, _ := strconv.Atoi(strings.TrimSuffix(rows[j].p90Latency, "ms"))
		if p90I != p90J {
			return p90I < p90J // Ascending P90 latency
		}
		// Parse avg latency
		avgI, _ := strconv.Atoi(strings.TrimSuffix(rows[i].avgLatency, "ms"))
		avgJ, _ := strconv.Atoi(strings.TrimSuffix(rows[j].avgLatency, "ms"))
		return avgI < avgJ // Ascending avg latency
	})
}

// ===== Progress Bar Management =====
//
// Progress bars provide visual feedback during HTTP load tests.
// They are automatically disabled in CI environments for clean log output.

// progressBars
// • Holds and manages progress bars for all methods in a test
// • Used to visualize test progress interactively
type progressBars struct {
	bars    map[string]*pb.ProgressBar
	pool    *pb.Pool
	enabled bool
}

// newProgressBars
// • Creates a set of progress bars for all methods in a test
// • Disables progress bars in CI/non-interactive environments
func newProgressBars(testMethodsMap map[string]testMethodConfig) (*progressBars, error) {
	// Check if we're running in CI or non-interactive environment
	if isCIEnv() {
		fmt.Println("Running in CI environment - progress bars disabled")
		return &progressBars{
			bars:    make(map[string]*pb.ProgressBar),
			enabled: false,
		}, nil
	}

	// Sort methods for consistent display order
	var sortedMethods []string
	for method := range testMethodsMap {
		sortedMethods = append(sortedMethods, method)
	}
	sort.Slice(sortedMethods, func(i, j int) bool {
		return string(sortedMethods[i]) < string(sortedMethods[j])
	})

	// Calculate the longest method name for padding
	longestLen := 0
	for _, method := range sortedMethods {
		if len(string(method)) > longestLen {
			longestLen = len(string(method))
		}
	}

	// Create a progress bar for each method
	bars := make(map[string]*pb.ProgressBar)
	barList := make([]*pb.ProgressBar, 0, len(testMethodsMap))

	for _, method := range sortedMethods {
		def := testMethodsMap[method]

		// Store the method name with padding for display
		padding := longestLen - len(string(method))
		methodWithPadding := string(method) + strings.Repeat(" ", padding)

		// Create a custom format for counters with padding for consistent spacing
		// Format: current/total with padding to make 3 digits minimum
		// This formats as "  1/300" or "010/300" for consistent width
		customCounterFormat := `{{ printf "%3d/%3d" .Current .Total }}`

		// Create a colored template with padded counters
		tmpl := fmt.Sprintf(`{{ blue "%s" }} %s {{ bar . "[" "=" ">" " " "]" | blue }} {{ green (percent .) }}`,
			methodWithPadding, customCounterFormat)

		// Create the bar with the template and start it
		bar := pb.ProgressBarTemplate(tmpl).New(def.serviceConfig.RequestsPerMethod)

		// Ensure we're not using byte formatting
		bar.Set(pb.Bytes, false)

		// Set max width for the bar
		bar.SetMaxWidth(100)

		bars[method] = bar
		barList = append(barList, bar)
	}

	// Try to create a pool with all the bars
	pool, err := pb.StartPool(barList...)
	if err != nil {
		// If we fail to create progress bars, fall back to simple output
		fmt.Printf("Warning: Could not create progress bars: %v\n", err)
		return &progressBars{
			bars:    make(map[string]*pb.ProgressBar),
			enabled: false,
		}, nil
	}

	return &progressBars{
		bars:    bars,
		pool:    pool,
		enabled: true,
	}, nil
}

// finish completes all progress bars
func (p *progressBars) finish() error {
	if !p.enabled || p.pool == nil {
		return nil
	}
	return p.pool.Stop()
}

// get returns the progress bar for a specific method
func (p *progressBars) get(method string) *pb.ProgressBar {
	if !p.enabled {
		return nil
	}
	return p.bars[method]
}

// showWaitBar shows a progress bar for the optional for hydrator checks to complete
func showWaitBar(secondsToWait int) {
	// Create a progress bar for the optional wait time
	waitBar := pb.ProgressBarTemplate(`{{ blue "Waiting" }} {{ printf "%2d/%2d" .Current .Total }} {{ bar . "[" "=" ">" " " "]" | blue }} {{ green (percent .) }}`).New(secondsToWait)
	waitBar.Set(pb.Bytes, false)
	waitBar.SetMaxWidth(100)
	waitBar.Start()

	// Wait for specified seconds, updating the progress bar every second
	for range secondsToWait {
		waitBar.Increment()
		<-time.After(1 * time.Second)
	}

	waitBar.Finish()
}
