#!/bin/bash
#
# PATH Load Test Script - All Chains
# ===================================
# Runs validated load tests against all configured chains
# Validates response payloads, not just HTTP status codes
#
# Usage:
#   ./scripts/load_test_all_chains.sh [OPTIONS]
#
# Options:
#   -c, --count       Number of requests per chain (default: 5000)
#   -p, --parallel    Concurrent requests (default: 50)
#   -g, --gateway     Gateway URL (default: http://localhost:3069)
#   -s, --services    Comma-separated list of services to test (default: all)
#   -o, --output      Output directory for results (default: /tmp/path_load_test)
#   -h, --help        Show this help message
#
# Examples:
#   ./scripts/load_test_all_chains.sh                    # Test all chains, 5k each
#   ./scripts/load_test_all_chains.sh -c 1000            # Test all chains, 1k each
#   ./scripts/load_test_all_chains.sh -s eth,poly,base   # Test specific chains
#   ./scripts/load_test_all_chains.sh -c 10000 -p 100    # 10k requests, 100 concurrent
#

set -e

# Default configuration
COUNT=5000
CONCURRENCY=50
GATEWAY_URL="http://localhost:3069"
SERVICES=""
OUTPUT_DIR="/home/overlordyorch/Development/path/load_test_results"
METRICS_URL="http://localhost:9090"

# Chain type configurations
# Maps service IDs to their RPC methods and validation patterns
declare -A CHAIN_METHODS
declare -A CHAIN_VALIDATORS

# EVM chains - use eth_blockNumber
EVM_CHAINS="arb-one arb-sepolia-testnet avax avax-dfk base base-sepolia-testnet bera blast boba bsc celo eth eth-holesky-testnet eth-sepolia-testnet fantom fraxtal fuse giwa-sepolia-testnet gnosis harmony hyperliquid ink iotex kaia linea mantle metis moonbeam moonriver oasys op op-sepolia-testnet opbnb poly poly-amoy-testnet poly-zkevm scroll sonic taiko unichain xrplevm xrplevm-testnet zklink-nova zksync-era sei kava"

# Solana - uses getHealth
SOLANA_CHAINS="solana"

# Cosmos chains - use status method (JSON-RPC)
# Note: Some chains have suppliers that proxy CometBFT methods (jackal, pocket),
# while others (fetch, osmosis, seda) have suppliers with misconfigured endpoints.
# The load test will accurately reflect which chains have working infrastructure.
COSMOS_CHAINS="akash atomone cheqd chihuahua fetch jackal juno osmosis persistence pocket seda shentu stargaze"

# Other chains with specific methods
NEAR_CHAINS="near"
SUI_CHAINS="sui"
RADIX_CHAINS="radix"
TRON_CHAINS="tron"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -c|--count)
            COUNT="$2"
            shift 2
            ;;
        -p|--parallel)
            CONCURRENCY="$2"
            shift 2
            ;;
        -g|--gateway)
            GATEWAY_URL="$2"
            shift 2
            ;;
        -s|--services)
            SERVICES="$2"
            shift 2
            ;;
        -o|--output)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        -h|--help)
            head -30 "$0" | tail -25
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Create output directory
mkdir -p "$OUTPUT_DIR"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
RESULTS_DIR="$OUTPUT_DIR/$TIMESTAMP"
mkdir -p "$RESULTS_DIR"

# Log file
LOG_FILE="$RESULTS_DIR/test.log"
SUMMARY_FILE="$RESULTS_DIR/summary.csv"

log() {
    echo -e "$1" | tee -a "$LOG_FILE"
}

# Get chain type and return appropriate test payload
# Returns: payload|method where method is POST or GET
get_chain_payload() {
    local service=$1

    # Check if EVM chain
    if echo "$EVM_CHAINS" | grep -qw "$service"; then
        echo '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}|POST'
        return
    fi

    # Solana
    if echo "$SOLANA_CHAINS" | grep -qw "$service"; then
        echo '{"jsonrpc":"2.0","method":"getHealth","params":[],"id":1}|POST'
        return
    fi

    # Cosmos chains - use status method (JSON-RPC)
    if echo "$COSMOS_CHAINS" | grep -qw "$service"; then
        echo '{"jsonrpc":"2.0","method":"status","params":[],"id":1}|POST'
        return
    fi

    # NEAR
    if echo "$NEAR_CHAINS" | grep -qw "$service"; then
        echo '{"jsonrpc":"2.0","method":"status","params":[],"id":1}|POST'
        return
    fi

    # SUI
    if echo "$SUI_CHAINS" | grep -qw "$service"; then
        echo '{"jsonrpc":"2.0","method":"sui_getLatestCheckpointSequenceNumber","params":[],"id":1}|POST'
        return
    fi

    # Radix
    if echo "$RADIX_CHAINS" | grep -qw "$service"; then
        echo '{"jsonrpc":"2.0","method":"status","params":[],"id":1}|POST'
        return
    fi

    # TRON
    if echo "$TRON_CHAINS" | grep -qw "$service"; then
        echo '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}|POST'
        return
    fi

    # Default: try eth_blockNumber
    echo '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}|POST'
}

# Validate response based on chain type
validate_response() {
    local service=$1
    local response=$2

    # Check for error response first
    if echo "$response" | grep -q '"error"'; then
        echo "RPC_ERROR"
        return
    fi

    # Check for empty response
    if [ -z "$response" ] || [ "$response" == "null" ]; then
        echo "EMPTY"
        return
    fi

    # EVM chains - expect hex result
    if echo "$EVM_CHAINS $TRON_CHAINS" | grep -qw "$service"; then
        if echo "$response" | grep -qE '"result":"0x[0-9a-fA-F]+"'; then
            echo "VALID"
            return
        fi
    fi

    # Cosmos chains - expect status response with node_info or sync_info
    if echo "$COSMOS_CHAINS" | grep -qw "$service"; then
        if echo "$response" | grep -qE '"node_info"|"sync_info"|"latest_block_height"'; then
            echo "VALID"
            return
        fi
    fi

    # Solana - expect "ok" for getHealth
    if echo "$SOLANA_CHAINS" | grep -qw "$service"; then
        if echo "$response" | grep -qE '"result":"ok"'; then
            echo "VALID"
            return
        fi
    fi

    # NEAR - expect object with sync_info
    if echo "$NEAR_CHAINS" | grep -qw "$service"; then
        if echo "$response" | grep -q '"sync_info"'; then
            echo "VALID"
            return
        fi
    fi

    # SUI - expect numeric result
    if echo "$SUI_CHAINS" | grep -qw "$service"; then
        if echo "$response" | grep -qE '"result":[0-9]+'; then
            echo "VALID"
            return
        fi
    fi

    # Generic check - has a result field
    if echo "$response" | grep -q '"result"'; then
        echo "VALID"
        return
    fi

    echo "INVALID"
}

# Run load test for a single service
run_service_test() {
    local service=$1
    local results_file="$RESULTS_DIR/${service}_results.txt"
    local payload_info=$(get_chain_payload "$service")

    # Parse payload (format is payload|method, we only need the payload now)
    local payload=$(echo "$payload_info" | cut -d'|' -f1)

    log "${BLUE}Testing $service${NC} - $COUNT requests @ $CONCURRENCY concurrent"

    # Clear results file
    > "$results_file"

    # Export for subshells
    export GATEWAY_URL SERVICE="$service" PAYLOAD="$payload" RESULTS_FILE="$results_file"

    # Function to run single request
    run_single_request() {
        local resp=$(curl -s --max-time 15 -X POST "$GATEWAY_URL/v1" \
            -H "Content-Type: application/json" \
            -H "Target-Service-Id: $SERVICE" \
            -d "$PAYLOAD" 2>/dev/null)

        # Simple validation inline
        # Check for JSON-RPC result or CometBFT status response fields
        if echo "$resp" | grep -qE '"result"|"node_info"|"sync_info"'; then
            if echo "$resp" | grep -q '"error"'; then
                echo "RPC_ERROR" >> "$RESULTS_FILE"
            else
                echo "VALID" >> "$RESULTS_FILE"
            fi
        elif echo "$resp" | grep -q '"error"'; then
            local err=$(echo "$resp" | grep -o '"message":"[^"]*"' | head -1 | cut -d'"' -f4 | head -c 50)
            echo "RPC_ERROR:$err" >> "$RESULTS_FILE"
        elif [ -z "$resp" ]; then
            echo "TIMEOUT" >> "$RESULTS_FILE"
        else
            echo "INVALID" >> "$RESULTS_FILE"
        fi
    }
    export -f run_single_request

    # Run test
    local start_time=$(date +%s)
    seq 1 $COUNT | xargs -P $CONCURRENCY -I {} bash -c 'run_single_request' 2>/dev/null
    local end_time=$(date +%s)
    local duration=$((end_time - start_time))

    # Calculate results
    local total=$(wc -l < "$results_file" | tr -d ' \n')
    local valid=$(grep -c "^VALID$" "$results_file" 2>/dev/null | tr -d ' \n' || echo 0)
    local rpc_errors=$(grep -c "^RPC_ERROR" "$results_file" 2>/dev/null | tr -d ' \n' || echo 0)
    local timeouts=$(grep -c "^TIMEOUT$" "$results_file" 2>/dev/null | tr -d ' \n' || echo 0)
    local invalid=$(grep -c "^INVALID$" "$results_file" 2>/dev/null | tr -d ' \n' || echo 0)

    # Ensure numeric values (default to 0 if empty)
    [ -z "$total" ] && total=0
    [ -z "$valid" ] && valid=0
    [ -z "$rpc_errors" ] && rpc_errors=0
    [ -z "$timeouts" ] && timeouts=0
    [ -z "$invalid" ] && invalid=0

    # Calculate success rate
    local success_rate="0.0"
    if [ "$total" -gt 0 ]; then
        success_rate=$(awk "BEGIN {printf \"%.2f\", $valid/$total*100}")
    fi

    # Calculate throughput
    local throughput="0.0"
    if [ "$duration" -gt 0 ]; then
        throughput=$(awk "BEGIN {printf \"%.1f\", $total/$duration}")
    fi

    # Determine status color
    local status_color=$GREEN
    local status="PASS"
    if (( $(echo "$success_rate < 95" | bc -l) )); then
        status_color=$YELLOW
        status="WARN"
    fi
    if (( $(echo "$success_rate < 80" | bc -l) )); then
        status_color=$RED
        status="FAIL"
    fi

    # Log results
    log "  ${status_color}$status${NC} - Valid: $valid/$total (${success_rate}%) | Errors: $rpc_errors | Timeouts: $timeouts | ${throughput} req/s"

    # Append to summary CSV
    echo "$service,$total,$valid,$rpc_errors,$timeouts,$invalid,$success_rate,$throughput,$duration,$status" >> "$SUMMARY_FILE"

    # Log sample errors if any
    if [ "$rpc_errors" -gt 0 ]; then
        log "  Sample errors:"
        grep "^RPC_ERROR" "$results_file" | sort | uniq -c | sort -rn | head -3 | while read line; do
            log "    $line"
        done
    fi

    # Clean up results file to save disk space
    rm -f "$results_file"
}

# Main execution
main() {
    log "=============================================="
    log "PATH Load Test - All Chains"
    log "=============================================="
    log "Timestamp: $TIMESTAMP"
    log "Gateway: $GATEWAY_URL"
    log "Requests per chain: $COUNT"
    log "Concurrency: $CONCURRENCY"
    log "Output: $RESULTS_DIR"
    log ""

    # Check gateway health
    log "Checking gateway health..."
    local health=$(curl -s "$GATEWAY_URL/healthz" 2>/dev/null)
    if [ -z "$health" ]; then
        log "${RED}ERROR: Gateway not responding at $GATEWAY_URL${NC}"
        exit 1
    fi

    local status=$(echo "$health" | jq -r '.status' 2>/dev/null)
    if [ "$status" != "ready" ]; then
        log "${RED}ERROR: Gateway not ready (status: $status)${NC}"
        exit 1
    fi
    log "${GREEN}Gateway is ready${NC}"
    log ""

    # Get list of services to test
    local services_to_test
    if [ -n "$SERVICES" ]; then
        # Use provided list
        services_to_test=$(echo "$SERVICES" | tr ',' '\n')
    else
        # Get all configured services
        services_to_test=$(echo "$health" | jq -r '.configuredServiceIDs[]' 2>/dev/null)
    fi

    local service_count=$(echo "$services_to_test" | wc -l)
    log "Services to test: $service_count"
    log ""

    # Initialize summary CSV
    echo "service,total,valid,rpc_errors,timeouts,invalid,success_rate,throughput,duration,status" > "$SUMMARY_FILE"

    # Run tests for each service
    local tested=0
    local passed=0
    local warned=0
    local failed=0

    for service in $services_to_test; do
        tested=$((tested + 1))
        log "[$tested/$service_count] "
        run_service_test "$service"

        # Track status
        local last_status=$(tail -1 "$SUMMARY_FILE" | cut -d',' -f10)
        case $last_status in
            PASS) passed=$((passed + 1)) ;;
            WARN) warned=$((warned + 1)) ;;
            FAIL) failed=$((failed + 1)) ;;
        esac

        log ""
    done

    # Print summary
    log "=============================================="
    log "SUMMARY"
    log "=============================================="
    log "Total services tested: $tested"
    log "${GREEN}Passed (>=95%): $passed${NC}"
    log "${YELLOW}Warning (80-95%): $warned${NC}"
    log "${RED}Failed (<80%): $failed${NC}"
    log ""
    log "Full results: $SUMMARY_FILE"
    log ""

    # Print table summary
    log "Service Results:"
    log "----------------"
    column -t -s',' "$SUMMARY_FILE" | head -1
    column -t -s',' "$SUMMARY_FILE" | tail -n +2 | sort -t',' -k7 -n

    # Return exit code based on failures
    if [ "$failed" -gt 0 ]; then
        exit 1
    fi
    exit 0
}

main "$@"
