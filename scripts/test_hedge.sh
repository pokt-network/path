#!/bin/bash
# test_hedge.sh - Test hedge functionality in PATH
#
# Usage: ./test_hedge.sh [service_id] [request_count]
#   service_id:    Service to test (default: base)
#   request_count: Number of requests (default: 5)
#
# Prerequisites:
#   - PATH running on localhost:3069
#   - Redis running
#   - hedge_delay configured (recommend 100ms for testing)

set -euo pipefail

SERVICE_ID="${1:-base}"
REQUEST_COUNT="${2:-5}"
PATH_URL="${PATH_URL:-http://localhost:3069}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}═══════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  PATH Hedge Test - Service: ${SERVICE_ID}${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════${NC}"
echo ""

# Check if PATH is running
if ! curl -s "${PATH_URL}/health" > /dev/null 2>&1; then
    echo -e "${RED}❌ PATH not responding at ${PATH_URL}/health${NC}"
    echo "   Start PATH first: cd ~/Development/path && setsid ./bin/path -config ./bin/config/.config.yaml > /tmp/path.log 2>&1 &"
    exit 1
fi

echo -e "${GREEN}✓ PATH is running${NC}"
echo ""

# Counters
primary_only=0
primary_won=0
hedge_won=0
no_hedge=0
errors=0

# Request that should trigger hedge (eth_getLogs with large block range)
PAYLOAD='{"jsonrpc":"2.0","method":"eth_getLogs","params":[{"fromBlock":"0x2780000","toBlock":"0x2780100"}],"id":1}'

echo -e "${YELLOW}Sending ${REQUEST_COUNT} requests...${NC}"
echo ""

for i in $(seq 1 "$REQUEST_COUNT"); do
    response=$(curl -s -w "\n%{http_code}" -X POST "${PATH_URL}/v1" \
        -H "Content-Type: application/json" \
        -H "Target-Service-Id: ${SERVICE_ID}" \
        -d "$PAYLOAD" 2>&1)
    
    http_code=$(echo "$response" | tail -n1)
    headers=$(curl -s -I -X POST "${PATH_URL}/v1" \
        -H "Content-Type: application/json" \
        -H "Target-Service-Id: ${SERVICE_ID}" \
        -d "$PAYLOAD" 2>&1)
    
    hedge_result=$(echo "$headers" | grep -i "X-Hedge-Result" | cut -d: -f2 | tr -d ' \r' || echo "unknown")
    supplier=$(echo "$headers" | grep -i "X-Supplier-Address" | cut -d: -f2 | tr -d ' \r' || echo "unknown")
    tried=$(echo "$headers" | grep -i "X-Suppliers-Tried" | cut -d: -f2 | tr -d ' \r' || echo "unknown")
    retry_count=$(echo "$headers" | grep -i "X-Retry-Count" | cut -d: -f2 | tr -d ' \r' || echo "0")
    
    # Color based on result
    case "$hedge_result" in
        "primary_only")
            color=$BLUE
            ((primary_only++))
            ;;
        "primary_won")
            color=$GREEN
            ((primary_won++))
            ;;
        "hedge_won")
            color=$YELLOW
            ((hedge_won++))
            ;;
        "no_hedge")
            color=$RED
            ((no_hedge++))
            ;;
        *)
            color=$RED
            ((errors++))
            ;;
    esac
    
    # Count how many suppliers were tried
    tried_count=$(echo "$tried" | tr ',' '\n' | grep -c . || echo "1")
    
    printf "[%2d] ${color}%-14s${NC} | Winner: %-15s | Tried: %d | Retries: %s | HTTP: %s\n" \
        "$i" "$hedge_result" "${supplier:0:15}" "$tried_count" "$retry_count" "$http_code"
done

echo ""
echo -e "${BLUE}═══════════════════════════════════════════════════${NC}"
echo -e "${BLUE}  Summary${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════${NC}"
echo -e "  ${BLUE}primary_only${NC}: $primary_only (primary responded before hedge_delay)"
echo -e "  ${GREEN}primary_won${NC}:  $primary_won (hedge started, primary faster)"
echo -e "  ${YELLOW}hedge_won${NC}:    $hedge_won (hedge started, hedge faster)"
echo -e "  ${RED}no_hedge${NC}:     $no_hedge (hedge couldn't start)"
echo -e "  ${RED}errors${NC}:       $errors"
echo ""

# Verdict
hedge_active=$((primary_won + hedge_won))
if [ "$hedge_active" -gt 0 ]; then
    echo -e "${GREEN}✓ Hedge is working! ($hedge_active requests had active hedging)${NC}"
else
    echo -e "${YELLOW}⚠ No hedge activity detected. Check hedge_delay config.${NC}"
fi
