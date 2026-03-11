#!/bin/bash
# =============================================================================
# Multi-Replica Archival Test Script
# =============================================================================
# Tests that archival endpoint selection works across replicas via Redis sync.
#
# This script is FULLY SELF-CONTAINED - it will:
# 1. Ensure Redis is running (via Docker)
# 2. Build PATH if needed
# 3. Start Replica 1 (port 3069) - acts as leader, runs health checks
# 4. Start Replica 2 (port 3070) - non-leader, must find archival via Redis
# 5. Run archival tests against both replicas
# 6. Clean up (stop replicas, keep Redis for future runs)
#
# The bug we fixed (Phase 02-03):
# Non-leader replicas couldn't find archival endpoints because health checks
# wrote to Redis with dynamic RPC type keys, but endpoint selection always
# looked up with RPCType_JSON_RPC.
#
# Usage:
#   ./scripts/test_multi_replica_archival.sh           # Run full test
#   ./scripts/test_multi_replica_archival.sh --quick   # Skip build, fewer requests
#   ./scripts/test_multi_replica_archival.sh --cleanup # Just cleanup any running replicas
# =============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
CONFIG_DIR="$PROJECT_DIR/bin/config"
REPLICA1_CONFIG="$CONFIG_DIR/.config.yaml"
REPLICA2_CONFIG="$CONFIG_DIR/.config-replica2.yaml"

REPLICA1_PORT=3069
REPLICA2_PORT=3070
REPLICA1_PID=""
REPLICA2_PID=""

REDIS_CONTAINER="path-test-redis"
REDIS_PORT=6379

# Test parameters
NUM_STRESS_REQUESTS=20
STARTUP_WAIT=30  # seconds to wait for health checks to run and establish perceived block

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# =============================================================================
# Cleanup
# =============================================================================
cleanup() {
    echo -e "\n${YELLOW}Cleaning up...${NC}"

    if [ -n "$REPLICA1_PID" ] && kill -0 "$REPLICA1_PID" 2>/dev/null; then
        echo "Stopping Replica 1 (PID: $REPLICA1_PID)"
        kill "$REPLICA1_PID" 2>/dev/null || true
        wait "$REPLICA1_PID" 2>/dev/null || true
    fi

    if [ -n "$REPLICA2_PID" ] && kill -0 "$REPLICA2_PID" 2>/dev/null; then
        echo "Stopping Replica 2 (PID: $REPLICA2_PID)"
        kill "$REPLICA2_PID" 2>/dev/null || true
        wait "$REPLICA2_PID" 2>/dev/null || true
    fi

    # Clean up any orphaned PATH processes on our ports
    for port in $REPLICA1_PORT $REPLICA2_PORT; do
        pid=$(lsof -ti:$port 2>/dev/null || true)
        if [ -n "$pid" ]; then
            echo "Killing orphaned process on port $port (PID: $pid)"
            kill $pid 2>/dev/null || true
        fi
    done

    if [ -f "$REPLICA2_CONFIG" ]; then
        rm -f "$REPLICA2_CONFIG"
    fi

    # Keep Redis running for future tests
    echo -e "${GREEN}Cleanup complete${NC}"
    echo "Note: Redis container '$REDIS_CONTAINER' left running for future tests"
}

trap cleanup EXIT

# =============================================================================
# Cleanup only mode
# =============================================================================
if [ "$1" == "--cleanup" ]; then
    echo "Running cleanup only..."
    cleanup
    exit 0
fi

# =============================================================================
# Redis Setup
# =============================================================================
ensure_redis() {
    echo -e "${BLUE}[1/6] Checking Redis...${NC}"

    # Check if Redis container exists and is running
    if docker ps --format '{{.Names}}' | grep -q "^${REDIS_CONTAINER}$"; then
        echo -e "${GREEN}Redis is already running${NC}"
        return 0
    fi

    # Check if container exists but stopped
    if docker ps -a --format '{{.Names}}' | grep -q "^${REDIS_CONTAINER}$"; then
        echo "Starting existing Redis container..."
        docker start "$REDIS_CONTAINER"
    else
        echo "Creating new Redis container..."
        docker run -d \
            --name "$REDIS_CONTAINER" \
            -p ${REDIS_PORT}:6379 \
            redis:7-alpine
    fi

    # Wait for Redis to be ready
    echo "Waiting for Redis..."
    for i in {1..10}; do
        if docker exec "$REDIS_CONTAINER" redis-cli ping 2>/dev/null | grep -q "PONG"; then
            echo -e "${GREEN}Redis is ready${NC}"
            return 0
        fi
        sleep 1
    done

    echo -e "${RED}Redis failed to start${NC}"
    return 1
}

# =============================================================================
# Build PATH
# =============================================================================
build_path() {
    echo -e "${BLUE}[2/6] Building PATH...${NC}"

    if [ "$QUICK_MODE" == "true" ] && [ -f "$PROJECT_DIR/bin/path" ]; then
        echo "Quick mode: Using existing binary"
        return 0
    fi

    cd "$PROJECT_DIR"

    if [ -f Makefile ] && grep -q "path_build" Makefile; then
        make path_build
    else
        echo "Building with go build..."
        go build -o bin/path ./cmd/main.go
    fi

    if [ ! -f "$PROJECT_DIR/bin/path" ]; then
        echo -e "${RED}Build failed - binary not found${NC}"
        return 1
    fi

    echo -e "${GREEN}Build complete${NC}"
}

# =============================================================================
# Create Replica 2 Config
# =============================================================================
create_replica2_config() {
    echo -e "${BLUE}[3/6] Creating Replica 2 config...${NC}"

    if [ ! -f "$REPLICA1_CONFIG" ]; then
        echo -e "${RED}Replica 1 config not found: $REPLICA1_CONFIG${NC}"
        return 1
    fi

    # Copy and modify ports for replica 2
    sed -e 's/port: 3069/port: 3070/' \
        -e 's/prometheus_addr: ":9090"/prometheus_addr: ":9091"/' \
        -e 's/pprof_addr: ":6060"/pprof_addr: ":6061"/' \
        "$REPLICA1_CONFIG" > "$REPLICA2_CONFIG"

    echo "Created: $REPLICA2_CONFIG"
}

# =============================================================================
# Start Replicas
# =============================================================================
start_replica() {
    local replica_num=$1
    local port=$2
    local config=$3
    local log_file="/tmp/path-replica${replica_num}.log"

    echo "Starting Replica $replica_num on port $port..."

    # Check if port is already in use
    if lsof -i:$port >/dev/null 2>&1; then
        echo -e "${YELLOW}Port $port already in use, killing existing process...${NC}"
        kill $(lsof -ti:$port) 2>/dev/null || true
        sleep 1
    fi

    cd "$PROJECT_DIR/bin"
    ./path -config "$config" > "$log_file" 2>&1 &
    local pid=$!

    echo "Replica $replica_num started (PID: $pid, Log: $log_file)"

    # Wait for replica to be ready
    for i in {1..30}; do
        if curl -s "http://localhost:$port/health" > /dev/null 2>&1; then
            echo -e "${GREEN}Replica $replica_num is ready${NC}"
            echo $pid
            return 0
        fi
        sleep 1
        echo -n "."
    done

    echo -e "\n${RED}Replica $replica_num failed to start${NC}"
    echo "Last 30 lines of log:"
    tail -30 "$log_file"
    return 1
}

start_replicas() {
    echo -e "${BLUE}[4/6] Starting replicas...${NC}"

    REPLICA1_PID=$(start_replica 1 $REPLICA1_PORT "config/.config.yaml")
    if [ -z "$REPLICA1_PID" ]; then
        return 1
    fi

    REPLICA2_PID=$(start_replica 2 $REPLICA2_PORT "config/.config-replica2.yaml")
    if [ -z "$REPLICA2_PID" ]; then
        return 1
    fi

    echo -e "\n${YELLOW}Waiting for health checks to establish perceived block (up to ${STARTUP_WAIT}s)...${NC}"

    # Wait for perceived block to be established by checking if archival is detected
    for i in $(seq 1 $STARTUP_WAIT); do
        # Make a test request and check if archival is being detected
        local test_response
        test_response=$(curl -s -D /tmp/headers-check.txt \
            -H "Content-Type: application/json" \
            -H "Target-Service-Id: bsc" \
            -d "$BSC_ARCHIVAL_REQUEST" \
            "http://localhost:$REPLICA1_PORT/v1" 2>/dev/null)

        local archival_val=$(grep -i "X-Archival-Request" /tmp/headers-check.txt 2>/dev/null | grep -o 'true\|false' || echo "unknown")

        if [ "$archival_val" == "true" ]; then
            echo -e "\n${GREEN}Perceived block established - archival detection working${NC}"
            # Give a few more seconds for Redis sync
            sleep 3
            return 0
        fi

        echo -n "."
        sleep 1
    done

    echo -e "\n${YELLOW}Warning: Archival detection may not be working yet (X-Archival-Request: $archival_val)${NC}"
    echo "Continuing anyway - health checks may still be running..."
}

# =============================================================================
# Test Functions
# =============================================================================

# BSC archival request - queries balance at historical block 33,049,200
BSC_ARCHIVAL_REQUEST='{"jsonrpc":"2.0","id":1,"method":"eth_getBalance","params":["0xfb50526f49894b78541b776f5aaefe43e3bd8590","0x1f86b10"]}'

test_single_request() {
    local port=$1
    local replica_name=$2

    echo -e "\n${YELLOW}Testing $replica_name (port $port)...${NC}"

    # Make request and capture response + headers
    local response
    response=$(curl -s -w "\n%{http_code}" \
        -H "Content-Type: application/json" \
        -H "Target-Service-Id: bsc" \
        -D /tmp/headers-$port.txt \
        -d "$BSC_ARCHIVAL_REQUEST" \
        "http://localhost:$port/v1" 2>/dev/null)

    local http_code=$(echo "$response" | tail -1)
    local body=$(echo "$response" | sed '$d')

    echo "HTTP Status: $http_code"

    # Show relevant headers
    local archival_header=$(grep -i "X-Archival-Request" /tmp/headers-$port.txt 2>/dev/null | tr -d '\r' || echo "X-Archival-Request: [not found]")
    local suppliers_header=$(grep -i "X-Suppliers-Tried" /tmp/headers-$port.txt 2>/dev/null | tr -d '\r' || echo "X-Suppliers-Tried: [not found]")

    echo "Headers:"
    echo "  $archival_header"
    echo "  $suppliers_header"

    # Check response
    if echo "$body" | grep -q '"error"'; then
        local error_msg=$(echo "$body" | grep -o '"message":"[^"]*"' | head -1)
        echo -e "${RED}ERROR: $error_msg${NC}"
        return 1
    fi

    if echo "$body" | grep -q '"result"'; then
        local result_value=$(echo "$body" | grep -o '"result":"[^"]*"' | head -1)
        echo -e "${GREEN}SUCCESS: $result_value${NC}"
        return 0
    fi

    echo -e "${RED}UNEXPECTED: $body${NC}"
    return 1
}

test_stress() {
    local port=$1
    local replica_name=$2
    local num_requests=${3:-$NUM_STRESS_REQUESTS}
    local successes=0
    local failures=0

    echo -e "\n${YELLOW}Stress test: $num_requests requests to $replica_name...${NC}"

    for i in $(seq 1 $num_requests); do
        local response
        response=$(curl -s \
            -H "Content-Type: application/json" \
            -H "Target-Service-Id: bsc" \
            -d "$BSC_ARCHIVAL_REQUEST" \
            "http://localhost:$port/v1" 2>/dev/null)

        if echo "$response" | grep -q '"result"'; then
            ((successes++))
            echo -n "."
        else
            ((failures++))
            echo -n "X"
        fi
    done

    echo ""
    echo "Results: $successes/$num_requests successful"

    if [ $failures -gt 0 ]; then
        echo -e "${RED}$failures failures detected${NC}"
        return 1
    fi

    echo -e "${GREEN}All requests successful${NC}"
    return 0
}

run_tests() {
    echo -e "${BLUE}[5/6] Running tests...${NC}"

    local r1_single=0
    local r2_single=0
    local r2_stress=0

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo " Single Request Tests"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    test_single_request $REPLICA1_PORT "Replica 1 (Leader)" || r1_single=1
    test_single_request $REPLICA2_PORT "Replica 2 (Non-Leader)" || r2_single=1

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo " Stress Test (Replica 2 - Critical Path)"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    if [ "$QUICK_MODE" == "true" ]; then
        test_stress $REPLICA2_PORT "Replica 2" 5 || r2_stress=1
    else
        test_stress $REPLICA2_PORT "Replica 2" $NUM_STRESS_REQUESTS || r2_stress=1
    fi

    # Return combined result
    if [ $r1_single -eq 0 ] && [ $r2_single -eq 0 ] && [ $r2_stress -eq 0 ]; then
        return 0
    fi

    # Store results for summary
    echo "$r1_single $r2_single $r2_stress" > /tmp/test_results.txt
    return 1
}

# =============================================================================
# Summary
# =============================================================================
print_summary() {
    local test_passed=$1

    echo -e "${BLUE}[6/6] Summary${NC}"
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo " Test Results"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    if [ "$test_passed" == "true" ]; then
        echo -e "${GREEN}"
        echo "  ╔═══════════════════════════════════════════╗"
        echo "  ║         ALL TESTS PASSED                  ║"
        echo "  ╚═══════════════════════════════════════════╝"
        echo -e "${NC}"
        echo ""
        echo "Cross-replica archival sync is working correctly:"
        echo "  - Replica 1 (leader): Health checks mark archival endpoints in Redis"
        echo "  - Replica 2 (non-leader): Finds archival endpoints via Redis lookup"
        echo ""
        echo "The Phase 02-03 fix (RPC type consistency) is verified."
    else
        echo -e "${RED}"
        echo "  ╔═══════════════════════════════════════════╗"
        echo "  ║         SOME TESTS FAILED                 ║"
        echo "  ╚═══════════════════════════════════════════╝"
        echo -e "${NC}"
        echo ""
        if [ -f /tmp/test_results.txt ]; then
            read r1 r2 r2s < /tmp/test_results.txt
            echo "  Replica 1 single:  $([ "$r1" == "0" ] && echo -e "${GREEN}PASS${NC}" || echo -e "${RED}FAIL${NC}")"
            echo "  Replica 2 single:  $([ "$r2" == "0" ] && echo -e "${GREEN}PASS${NC}" || echo -e "${RED}FAIL${NC}")"
            echo "  Replica 2 stress:  $([ "$r2s" == "0" ] && echo -e "${GREEN}PASS${NC}" || echo -e "${RED}FAIL${NC}")"
        fi
        echo ""
        echo "Check logs for details:"
        echo "  - Replica 1: /tmp/path-replica1.log"
        echo "  - Replica 2: /tmp/path-replica2.log"
    fi
    echo ""
}

# =============================================================================
# Main
# =============================================================================
main() {
    QUICK_MODE="false"
    if [ "$1" == "--quick" ]; then
        QUICK_MODE="true"
        NUM_STRESS_REQUESTS=5
        STARTUP_WAIT=15  # Still need time for health checks
        echo -e "${YELLOW}Quick mode enabled${NC}"
    fi

    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo " PATH Multi-Replica Archival Test"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo ""
    echo "This test verifies Phase 02 correctness fixes:"
    echo "  - 02-01: Bi-directional archival state updates"
    echo "  - 02-03: RPC type consistency for cross-replica sync"
    echo ""

    # Run all steps
    ensure_redis || exit 1
    build_path || exit 1
    create_replica2_config || exit 1
    start_replicas || exit 1

    if run_tests; then
        print_summary "true"
        exit 0
    else
        print_summary "false"
        exit 1
    fi
}

main "$@"
