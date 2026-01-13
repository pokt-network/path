#!/usr/bin/env bash

# TODO_UPNEXT(@olshansky): Further improvements
# - [ ] Add support for REST based services

# Source helpers for TLD reporting
source "$(dirname "$0")/shannon_preliminary_services_helpers.sh"

# For usage instructions, run:
# $ ./e2e/scripts/shannon_preliminary_services_test.sh --he

# References
# Pocket Services - Public Directory: https://docs.google.com/spreadsheets/d/1QWVGEuB2u5bkGfONoDNaltjny1jd9rJ_f7HZTjSGgQM/edit?gid=195862478#gid=195862478
# Grove Shannon Mainnet Applications: https://docs.google.com/spreadsheets/d/1EjF9buF6GNR4vGglUuMjLtJmrfQD9JunX_CJHslwt84/edit?gid=0#gid=0

# EVM-compatible services that use JSON-RPC eth_blockNumber for testing
EVM_SERVICES=(
    "arb-one"
    "arb-sepolia-testnet"
    "avax-dfk"
    "avax"
    "base-sepolia-testnet"
    "base"
    "bera"
    "bitcoin"
    "blast"
    "boba"
    "bsc"
    "celo"
    "eth"
    "eth-holesky-testnet"
    "eth-sepolia-testnet"
    "fantom"
    "fraxtal"
    "fuse"
    "gnosis"
    "harmony"
    "ink"
    "iotex"
    "kaia"
    "kava"
    "linea"
    "mantle"
    "metis"
    "moonbeam"
    "moonriver"
    "near"
    "oasys"
    "op"
    "op-sepolia-testnet"
    "opbnb"
    "osmosis"
    "poly"
    "poly-amoy-testnet"
    "poly-zkevm"
    "radix"
    "scroll"
    "sei"
    "solana"
    "sonic"
    "sui"
    "taiko"
    "taiko-hekla-testnet"
    "tron"
    "xrplevm"
    "xrplevm-testnet"
    "zklink-nova"
    "zksync_era"
)

# CometBFT services that use REST /status endpoint for testing
COMETBFT_SERVICES=(
    "pocket"
)

# Combined services list for backwards compatibility
SERVICES=("${EVM_SERVICES[@]}" "${COMETBFT_SERVICES[@]}")

# Configuration Variables
TARGET_SERVICE_HEADER="Target-Service-Id"
JSONRPC_TEST_PAYLOAD='{"jsonrpc":"2.0","method":"eth_blockNumber","id":1}'
MAX_RETRIES=5
RETRY_SLEEP_DURATION=1

# Helper function to determine service type
get_service_type() {
    local service="$1"
    for cometbft_service in "${COMETBFT_SERVICES[@]}"; do
        if [ "$service" = "$cometbft_service" ]; then
            echo "cometbft"
            return
        fi
    done
    for evm_service in "${EVM_SERVICES[@]}"; do
        if [ "$service" = "$evm_service" ]; then
            echo "evm"
            return
        fi
    done
    echo "ERROR: not evm or cometbft service: $service"
    return 1
}

# Disqualified endpoints configuration
DISQUALIFIED_API_URL="http://localhost:3069/disqualified_endpoints"

print_show_help() {
    echo ""
    echo "RUN: ./e2e/scripts/shannon_preliminary_services_test.sh --help"
    echo ""
    exit 1
}

# Function to display usage/help, including tl;dr and script docstring
print_help() {
    cat <<'EOF'
### Test onchain service availability on Shannon ###

Quickstart with Examples:
  ./e2e/scripts/shannon_preliminary_services_test.sh --network main --environment production --portal_app_id "your_app_id" --api_key "your_api_key"
  ./e2e/scripts/shannon_preliminary_services_test.sh --network main --environment production --services bsc,eth,pocket,poly --portal_app_id "your_app_id" --api_key "your_api_key"
  ./e2e/scripts/shannon_preliminary_services_test.sh --network beta --environment local --disqualified_endpoints
  ./e2e/scripts/shannon_preliminary_services_test.sh --network alpha --environment local
  ./e2e/scripts/shannon_preliminary_services_test.sh --network main --environment production --onchain-services


tl;dr
- Find all services with >=1 supplier on Shannon using 'pocketd'
- Confirm at least 1 'eth_blockNumber' request returns a 200 using PATH
- Output logs AND JSON report named supplier_report_<YYYY-MM-DD>_.json
- Optionally include disqualified endpoint analysis
- Optionally override the SERVICES list by querying all on-chain services from Pocketd


############################################
# Shannon Services Supplier Report Generator
############################################

 DESCRIPTION:
   What this script does:
   • 🔍 Queries supplier counts for a predefined list of Shannon service IDs
   • 🧪 Optionally tests each service with suppliers using JSON-RPC curl requests
   • 📊 Generates both human-readable console output and a detailed CSV report
   • 🚫 With --disqualified_endpoints: Also queries and stores disqualified endpoint data as JSON
   • 🔄 With --onchain-services: Dynamically fetches the list of services from on-chain via Pocketd, overriding the default SERVICES list


 USAGE:
   ./e2e/scripts/shannon_preliminary_services_test.sh --network <alpha|beta|main> --environment <local|production> [--disqualified_endpoints] [--portal_app_id <id>] [--api_key <key>]

 ARGUMENTS:
  -n, --network                 Network to use: 'alpha', 'beta' or 'main' (required)
  -e, --environment             Environment to use: 'local' or 'production' (required)

  -p, --portal_app_id           Portal Application ID for production (required when environment=production)
  -k, --api_key                 API Key for production (required when environment=production)

  -d, --disqualified_endpoints  Also query disqualified endpoints and add to JSON report (optional)
  -o, --onchain-services        Override the SERVICES list by querying all on-chain services from Pocketd (optional)
  -s, --services                Comma-separated list of services to test (e.g. -s bsc,eth,pocket,poly) (optional)

  -h, --help                    Show this help message and exit


 ENVIRONMENT BEHAVIOR:
   local:      Uses http://localhost:3069 with Target-Service-Id header
   production: Uses https://<service>.rpc.grove.city URLs with Portal-Application-Id and Authorization headers
               Note: Uses service alias (when available) instead of service_id for production subdomain URLs

 OUTPUT:
   - Console: Summary table showing service IDs, supplier counts, and test results
   - JSON File: Detailed report with test results (supplier_report_YYYY-MM-DD_HH:MM:SS.json)
   - JSON File: (with -d flag) Disqualified endpoints data (sanctioned-endpoint-results.json)

 CSV COLUMNS:
   service_id, suppliers, test_result, error_message, endpoint_response, unmarshaling_error

 JSON STRUCTURE (with -d flag):
   {"suppliers_passed": {services with working JSON-RPC and suppliers},
    "suppliers_failed": {services with failed JSON-RPC but suppliers, includes errors array},
    "no_suppliers": {services with no suppliers}}

EOF
    exit 0
}

# Parse command line arguments
NETWORK=""
ENVIRONMENT=""
QUERY_DISQUALIFIED=false
PORTAL_APP_ID=""
API_KEY=""

# Check for --help or -h anywhere in the arguments
for arg in "$@"; do
    if [[ "$arg" == "--help" || "$arg" == "-h" ]]; then
        print_help
    fi
done

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
    -n | --network)
        NETWORK="$2"
        shift 2
        ;;
    -e | --environment)
        ENVIRONMENT="$2"
        shift 2
        ;;
    -d | --disqualified_endpoints)
        QUERY_DISQUALIFIED=true
        shift
        ;;
    -p | --portal_app_id)
        PORTAL_APP_ID="$2"
        shift 2
        ;;
    -k | --api_key)
        API_KEY="$2"
        shift 2
        ;;
    -o | --onchain-services)
        USE_ONCHAIN_SERVICES=true
        shift
        ;;
    -s | --services)
        SERVICES_OVERRIDE="$2"
        shift 2
        ;;
    -h | --help)
        print_help
        ;;
    *)
        echo "Unknown option: $1"
        print_show_help
        exit 1
        ;;
    esac
done

##  Validate arguments

# Check if network was provided and valid
if [ -z "$NETWORK" ]; then
    echo "ERROR: --network flag is required"
    print_show_help
    exit 1
fi
# Check if network is valid
if [[ "$NETWORK" != "alpha" && "$NETWORK" != "beta" && "$NETWORK" != "main" ]]; then
    echo "ERROR: --network must be one of 'alpha', 'beta', or 'main'"
    print_show_help
    exit 1
fi

# Check if environment was provided and valid
if [ -z "$ENVIRONMENT" ]; then
    echo "ERROR: --environment flag is required"
    print_show_help
    exit 1
fi
# Check if environment is valid
if [[ "$ENVIRONMENT" != "local" && "$ENVIRONMENT" != "production" ]]; then
    echo "ERROR: --environment must be either 'local' or 'production'"
    print_show_help
    exit 1
fi

# If SERVICES_OVERRIDE is set, override the SERVICES array
if [ -n "$SERVICES_OVERRIDE" ]; then
    IFS=',' read -r -a SERVICES <<<"$SERVICES_OVERRIDE"
fi

# What this script does:
echo -e "\n✨ What this script does: ✨"
echo -e "  • 🔍 Queries all known Shannon services for supplier counts"
echo -e "  • 🧪 Tests JSON-RPC requests for services with suppliers"
echo -e "  • 📊 Generates a summary table and JSON report"
echo -e "  • 🚫 Optionally includes disqualified endpoint analysis\n"

# --- TLDs by Service Setup ---

# Call TLD helper and parse JSON into an associative array
echo -e "\n✨ Setting up Service TLDs by Service (this may take a little while)... ✨"
TLD_OUTPUT=$(shannon_query_service_tlds_by_id "$NETWORK" --structured)
echo "Retrieved TLDs by Service"
declare -A SERVICE_TLDS

while IFS="=" read -r key value; do
    SERVICE_TLDS["$key"]="$value"
done < <(
    echo "$TLD_OUTPUT" | jq -r '
    to_entries[] |
    "\(.key)=\(.value | join(","))"
  '
)

echo "✅ Finished setting SERVICE_TLDS"

# Helper to get TLDs for a service name
get_service_tlds() {
    local search_service="$1"
    echo "${SERVICE_TLDS[$search_service]}"
}

# --- End TLDs Setup ---

# --- Service Aliases Setup ---

# Function to get the service identifier for production URLs
# Returns the alias if available and environment is production, otherwise returns the service ID
get_service_identifier() {
    local service_id="$1"
    if [ "$ENVIRONMENT" = "production" ]; then
        case "$service_id" in
        arb-one) echo "arbitrum-one" ;;
        arb-sepolia-testnet) echo "arbitrum-sepolia-testnet" ;;
        base-sepolia-testnet) echo "base-sepolia-testnetnet" ;;
        eth-holesky-testnet) echo "eth-holesky-testnet" ;;
        eth-sepolia-testnet) echo "eth-sepolia-testnet" ;;
        op-sepolia-testnet) echo "optimism-sepolia-testnet" ;;
        poly) echo "poly" ;;
        taiko-hekla-testnet) echo "taiko-hekla-testnet" ;;
        xrplevm-testnet) echo "xrpl-evm-test" ;;
        zksync_era) echo "zksync-era" ;;
        *) echo "$service_id" ;;
        esac
    else
        echo "$service_id"
    fi
}

# --- End Service Aliases Setup ---

# Set node flag based on network
if [ "$NETWORK" = "alpha" ]; then
    NODE_FLAG="--node https://shannon-alpha-grove-rpc.alpha.poktroll.com"
elif [ "$NETWORK" = "beta" ]; then
    NODE_FLAG="--node https://shannon-testnet-grove-rpc.beta.poktroll.com"
elif [ "$NETWORK" = "main" ]; then
    NODE_FLAG="--node https://shannon-grove-rpc.mainnet.poktroll.com"
fi

# Local vs Production: Configure URLs and headers based on environment
if [ "$ENVIRONMENT" = "local" ]; then
    BASE_PATH_URL="http://localhost:3069/v1"
    BASE_DISQUALIFIED_URL="http://localhost:3069/disqualified_endpoints"
    USE_SUBDOMAIN=false
elif [ "$ENVIRONMENT" = "production" ]; then
    BASE_PATH_URL="https://rpc.grove.city/v1"
    BASE_DISQUALIFIED_URL="https://rpc.grove.city/disqualified_endpoints"
    USE_SUBDOMAIN=true
fi

# Production-specific parameters are required when environment is production
if [ "$ENVIRONMENT" = "production" ]; then
    if [ -z "$PORTAL_APP_ID" ]; then
        echo "ERROR: --portal_app_id is required when environment is production"
        print_show_help
    fi

    if [ -z "$API_KEY" ]; then
        echo "ERROR: --api_key is required when environment is production"
        print_show_help
    fi
fi

# Local: Check if PATH service is running via readiness endpoint
if [ "$ENVIRONMENT" = "local" ]; then
    echo "🏥 Checking if PATH service is ready..."
    # Using /ready instead of deprecated /healthz - /ready checks for sessions and endpoints
    ready_response=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:3069/ready 2>/dev/null)

    if [ "$ready_response" != "200" ]; then
        echo ""
        echo "❌ ERROR: PATH service is not running or not ready (HTTP $ready_response)"
        echo ""
        echo "⚠️ IMPORTANT: Ensure you are running PATH locally before proceeding."
        echo "    👀 See instructions here: https://www.notion.so/buildwithgrove/PATH-on-Shannon-Load-Tests-200a36edfff6805296c9ce10f2066de6?source=copy_link#205a36edfff68087b27dd086a28f21e9"
        echo ""
        echo "🚪 Exiting without testing services."
        exit 1
    fi

    echo -e "\n✅ PATH service is ready - proceeding with service tests\n"
else
    echo -e "\n🌍 Using production environment - skipping local health check\n"
fi

# Initialize temporary files for storing results
TEMP_DIR=$(mktemp -d)
RESULTS_FILE="$TEMP_DIR/results.txt"
JSON_REPORT_FILE="supplier_report_$(date +%Y-%m-%d_%H:%M:%S).json"
trap 'rm -rf "$TEMP_DIR"' EXIT

# If --onchain-services is set, override SERVICES array with on-chain queried list
if [ "$USE_ONCHAIN_SERVICES" = true ]; then
    echo -e "\n🔗 Fetching on-chain services via pocketd..."
    ONCHAIN_SERVICES_RAW=$(pocketd query service all-services --network=main --home=~/.pocket_prod --grpc-insecure=false -o json | jq -r '.service[].id')
    # Convert multi-line string to bash array (compatible with Linux and macOS)
    SERVICES=()
    while IFS= read -r line; do
        SERVICES+=("$line")
    done <<<"$ONCHAIN_SERVICES_RAW"
    echo -e "\n📝 SERVICES to be tested (from on-chain):"
    printf '  - %s\n' "${SERVICES[@]}"
fi

# Initialize array to store services with suppliers
declare -a services_with_suppliers

echo -e "\n=== SUPPLIER COUNT REPORT ==="
echo -e "Generated: $(date)"
echo -e "Network: $NETWORK"
echo -e "Environment: $ENVIRONMENT"
if [ "$QUERY_DISQUALIFIED" = true ]; then
    echo -e "Mode: Including disqualified endpoints analysis 📊"
else
    echo -e "Mode: Basic supplier count and testing only 📈"
fi
echo -e "=============================="
echo ""

# Iterate through each service in the embedded array
# Collect all service:count (or ERROR/FAILED) for table display later
declare -a all_services_results
for service in "${SERVICES[@]}"; do
    # Skip empty services (shouldn't happen with embedded array, but safety check)
    if [[ -z "$service" ]]; then
        continue
    fi

    # Run the command and capture output
    output=$(pocketd q supplier list-suppliers --service-id "$service" $NODE_FLAG 2>/dev/null)

    if [ $? -eq 0 ]; then
        # Extract the total count from pagination section
        total=$(echo "$output" | grep -E "^\s*total:" | sed 's/.*total: *"\([0-9]*\)".*/\1/')

        if [[ "$total" =~ ^[0-9]+$ ]]; then
            echo "$service:$total" >>"$RESULTS_FILE"
            all_services_results+=("$service:$total")
            if [ "$total" -gt 0 ]; then
                echo "🔍 $service: ✅ $total suppliers"
                services_with_suppliers+=("$service:$total")
            else
                echo "🔍 $service: ❌ suppliers"
            fi
        else
            echo "$service:ERROR" >>"$RESULTS_FILE"
            all_services_results+=("$service:ERROR")
            echo "🔍 $service: ❌ suppliers"
        fi
    else
        echo "$service:FAILED" >>"$RESULTS_FILE"
        all_services_results+=("$service:FAILED")
        echo "🔍 $service: ❌ (command failed)"
    fi
done

# Display all services (including 0 suppliers) as a table
if [ ${#all_services_results[@]} -gt 0 ]; then
    echo ""
    echo -e "=============================="
    echo -e "👥 SERVICE SUPPLIER COUNTS:"
    echo -e "=============================="
    printf "%-20s | %-9s | %s\n" "SERVICE ID" "SUPPLIERS" "TLDS"
    printf "%-20s-+-%9s-+-%s\n" "--------------------" "---------" "----------------"

    for item in "${all_services_results[@]}"; do
        IFS=':' read -r service count <<<"$item"
        tlds="${SERVICE_TLDS[$service]}"
        if [[ "$count" =~ ^[0-9]+$ ]]; then
            printf "%-20s | %-9s | %s\n" "$service" "$count" "$tlds"
        else
            printf "%-20s | %-9s | %s\n" "$service" "ERROR" "$tlds"
        fi
    done

    echo "=============================="

    echo ""

    echo "=============================="
    echo "🧪 TESTING SERVICES..."
    echo "=============================="

    # Initialize JSON report structure with three categories
    echo '{"suppliers_failed": {}, "suppliers_passed": {}, "no_suppliers": [], "summary": {}}' >"$TEMP_DIR/json_report.json"

    # Array to store all results for sorting
    declare -a all_results

    for item in "${services_with_suppliers[@]}"; do
        IFS=':' read -r service count <<<"$item"
        service_type=$(get_service_type "$service")
        service_identifier=$(get_service_identifier "$service")
        service_identifier="${service_identifier//_/-}"
        echo -e "\n 🚀 Testing $service ($service_type)..."

        # Execute 5 curl requests and require ALL to succeed
        request_count=0
        successful_requests=0
        failed_requests=0
        max_requests=5
        declare -a error_responses=()
        declare -a detailed_errors=()

        # Construct URL and headers based on environment and service type
        if [ "$service_type" = "cometbft" ]; then
            # CometBFT services use /status endpoint
            if [ "$USE_SUBDOMAIN" = true ]; then
                # Production: use subdomain format with required headers
                service_url="https://${service_identifier}.rpc.grove.city/v1/status"
            else
                # Local: use header format
                service_url="$BASE_PATH_URL/status"
            fi
        else
            # EVM services use JSON-RPC
            if [ "$USE_SUBDOMAIN" = true ]; then
                # Production: use subdomain format with required headers
                service_url="https://${service_identifier}.rpc.grove.city/v1"
            else
                # Local: use header format
                service_url="$BASE_PATH_URL"
            fi
        fi

        echo "  🌐 Target URL: $service_url"

        while [ $request_count -lt $max_requests ]; do
            request_count=$((request_count + 1))
            request_prefix="    📡 Request $request_count/$max_requests..."

            # Execute the curl request using the pre-constructed URL
            if [ "$service_type" = "cometbft" ]; then
                # CometBFT services use /status endpoint
                if [ "$USE_SUBDOMAIN" = true ]; then
                    # Production: use subdomain format with required headers
                    curl_result=$(curl -s -w "%{http_code}" "$service_url" \
                        -H "Portal-Application-Id: $PORTAL_APP_ID" \
                        -H "Authorization: $API_KEY" 2>/dev/null)
                else
                    # Local: use header format
                    curl_result=$(curl -s -w "%{http_code}" "$service_url" \
                        -H "$TARGET_SERVICE_HEADER: $service" 2>/dev/null)
                fi

                # Extract HTTP status code from curl response
                if [ ${#curl_result} -ge 3 ]; then
                    http_code="${curl_result: -3}"
                    response_body="${curl_result%???}"
                else
                    http_code="000"
                    response_body=""
                fi

                # Check both HTTP status and JSON error field
                if [ "$http_code" = "200" ]; then
                    # Check if response body is valid JSON and has no error field
                    if [ -n "$response_body" ] && echo "$response_body" | jq -e . >/dev/null 2>&1; then
                        if echo "$response_body" | jq -e '.error' >/dev/null 2>&1; then
                            echo "$request_prefix ❌ Failed (HTTP 200 but JSON error present)"
                            failed_requests=$((failed_requests + 1))
                            error_response=$(echo "$response_body" | jq -c '.error')
                            error_responses+=("$error_response")
                            error_message=$(echo "$response_body" | jq -r '.error.message // .error // "Unknown error"' 2>/dev/null || echo "Parse error")
                            endpoint_response=$(echo "$response_body" | jq -r '.error.data.endpoint_response // ""' 2>/dev/null || echo "")
                            unmarshaling_error=$(echo "$response_body" | jq -r '.error.data.unmarshaling_error // ""' 2>/dev/null || echo "")
                            # Safely create JSON with potentially problematic extracted strings
                            error_detail=$(jq -n --arg msg "$error_message" --arg ep "$endpoint_response" --arg unmarshal "$unmarshaling_error" \
                                '{error_message: $msg, endpoint_response: $ep, unmarshaling_error: $unmarshal}')
                            detailed_errors+=("$error_detail")
                        else
                            echo "$request_prefix ✅ Success (HTTP 200, no JSON error)"
                            successful_requests=$((successful_requests + 1))
                        fi
                    else
                        echo "$request_prefix ❌ Failed (HTTP 200 but invalid JSON)"
                        failed_requests=$((failed_requests + 1))
                        error_response='{"code":-32700,"message":"Invalid JSON response"}'
                        error_responses+=("$error_response")
                        # Safely create JSON with potentially problematic response body
                        error_detail=$(jq -n --arg msg "Invalid JSON response" --arg body "$response_body" --arg unmarshal "" \
                            '{error_message: $msg, endpoint_response: $body, unmarshaling_error: $unmarshal}')
                        detailed_errors+=("$error_detail")
                    fi
                else
                    echo "$request_prefix ❌ Failed (HTTP $http_code)"
                    failed_requests=$((failed_requests + 1))
                    error_response="{\"code\":-32000,\"message\":\"HTTP $http_code response\"}"
                    error_responses+=("$error_response")
                    # Safely create JSON with potentially problematic response body
                    error_detail=$(jq -n --arg msg "HTTP $http_code response" --arg body "$response_body" --arg unmarshal "" \
                        '{error_message: $msg, endpoint_response: $body, unmarshaling_error: $unmarshal}')
                    detailed_errors+=("$error_detail")
                fi
            else
                # EVM services use JSON-RPC
                if [ "$USE_SUBDOMAIN" = true ]; then
                    # Production: use subdomain format with required headers
                    curl_result=$(curl -s -w "%{http_code}" "$service_url" \
                        -H "Portal-Application-Id: $PORTAL_APP_ID" \
                        -H "Authorization: $API_KEY" \
                        -d "$JSONRPC_TEST_PAYLOAD" 2>/dev/null)
                else
                    # Local: use header format
                    curl_result=$(curl -s -w "%{http_code}" "$service_url" \
                        -H "$TARGET_SERVICE_HEADER: $service" \
                        -d "$JSONRPC_TEST_PAYLOAD" 2>/dev/null)
                fi

                # Extract HTTP status code from curl response
                if [ ${#curl_result} -ge 3 ]; then
                    http_code="${curl_result: -3}"
                    response_body="${curl_result%???}"
                else
                    http_code="000"
                    response_body=""
                fi

                # Check both HTTP status and JSON error field
                if [ "$http_code" = "200" ]; then
                    # Check if response body is valid JSON
                    if [ -n "$response_body" ] && echo "$response_body" | jq -e . >/dev/null 2>&1; then
                        # Check if response contains an error field
                        if echo "$response_body" | jq -e '.error' >/dev/null 2>&1; then
                            echo "$request_prefix ❌ Failed (HTTP 200 but JSON-RPC error)"
                            failed_requests=$((failed_requests + 1))
                            # Collect error response
                            error_response=$(echo "$response_body" | jq -c '.error')
                            error_responses+=("$error_response")
                            # Collect detailed error for JSON report - with safe parsing
                            error_message=$(echo "$response_body" | jq -r '.error.message // .error // "Unknown error"' 2>/dev/null || echo "Parse error")
                            endpoint_response=$(echo "$response_body" | jq -r '.error.data.endpoint_response // ""' 2>/dev/null || echo "")
                            unmarshaling_error=$(echo "$response_body" | jq -r '.error.data.unmarshaling_error // ""' 2>/dev/null || echo "")
                            # Safely create JSON with potentially problematic extracted strings
                            error_detail=$(jq -n --arg msg "$error_message" --arg ep "$endpoint_response" --arg unmarshal "$unmarshaling_error" \
                                '{error_message: $msg, endpoint_response: $ep, unmarshaling_error: $unmarshal}')
                            detailed_errors+=("$error_detail")
                        else
                            echo "$request_prefix ✅ Success (HTTP 200, no JSON error)"
                            successful_requests=$((successful_requests + 1))
                        fi
                    else
                        echo "$request_prefix ❌ Failed (HTTP 200 but invalid JSON)"
                        failed_requests=$((failed_requests + 1))
                        # Create error object for JSON parsing failures
                        error_response='{"code":-32700,"message":"Invalid JSON response"}'
                        error_responses+=("$error_response")
                        # Safely create JSON with potentially problematic response body
                        error_detail=$(jq -n --arg msg "Invalid JSON response" --arg body "$response_body" --arg unmarshal "" \
                            '{error_message: $msg, endpoint_response: $body, unmarshaling_error: $unmarshal}')
                        detailed_errors+=("$error_detail")
                    fi
                else
                    echo "$request_prefix ❌ Failed (HTTP $http_code)"
                    failed_requests=$((failed_requests + 1))
                    # Create error object for HTTP failures
                    error_response="{\"code\":-32000,\"message\":\"HTTP $http_code response\"}"
                    error_responses+=("$error_response")
                    # Safely create JSON with potentially problematic response body
                    error_detail=$(jq -n --arg msg "HTTP $http_code response" --arg body "$response_body" --arg unmarshal "" \
                        '{error_message: $msg, endpoint_response: $body, unmarshaling_error: $unmarshal}')
                    detailed_errors+=("$error_detail")
                fi
            fi

            # Brief pause between requests
            if [ $request_count -lt $max_requests ]; then
                sleep $RETRY_SLEEP_DURATION
            fi
        done

        # Determine overall result - ANY request success counts as pass
        if [ $successful_requests -eq $max_requests ]; then
            echo "  🟢 ALL PASSED ($successful_requests/$max_requests requests succeeded)"
            test_result="🟢"
            overall_status="success"
        elif [ $successful_requests -gt 0 ]; then
            echo "  🟡 PARTIAL SUCCESS ($successful_requests/$max_requests requests succeeded)"
            test_result="🟡"
            overall_status="success"
        else
            echo "  💔 ALL FAILED ($successful_requests/$max_requests requests succeeded, $failed_requests failed)"
            test_result="💔"
            overall_status="failed"
        fi

        # If disqualified endpoints flag is set, query disqualified endpoints
        disqualified_response=""
        if [ "$QUERY_DISQUALIFIED" = true ]; then
            echo "    📊 Querying disqualified endpoints for $service..."

            # Construct URL and headers based on environment
            if [ "$USE_SUBDOMAIN" = true ]; then
                # Production: use subdomain format with required headers
                # Use alias if available in production, otherwise use service ID
                service_identifier=$(get_service_identifier "$service")
                service_identifier="${service_identifier//_/-}"
                disqualified_url="https://${service_identifier}.rpc.grove.city/disqualified_endpoints"
                disqualified_response=$(curl -s "$disqualified_url" \
                    -H "Portal-Application-Id: $PORTAL_APP_ID" \
                    -H "Authorization: $API_KEY" 2>/dev/null)
            else
                # Local: use header format
                disqualified_response=$(curl -s "$BASE_DISQUALIFIED_URL" -H "Target-Service-Id: $service" 2>/dev/null)
            fi
            curl_exit_code=$?

            if [ $curl_exit_code -eq 0 ] && [ -n "$disqualified_response" ]; then
                # Check if response is valid JSON
                if echo "$disqualified_response" | jq empty 2>/dev/null; then
                    echo "    ✅ Successfully retrieved disqualified endpoints data"
                else
                    echo "    ❌ Invalid JSON from disqualified endpoints"
                    disqualified_response=""
                fi
            else
                echo "    💥 Disqualified endpoints call failed"
                disqualified_response=""
            fi
        fi

        # Create JSON entry for this service
        errors_json="[]"
        for error_detail in "${detailed_errors[@]}"; do
            errors_json=$(echo "$errors_json" | jq --argjson err "$error_detail" '. += [$err]')
        done

        # Build service JSON with optional disqualified endpoints response
        tlds_json=$(jq -c --arg s "$service" '.[$s]' <<<"$TLD_OUTPUT")
        if [ -z "$tlds_json" ]; then
            tlds_json='[]'
        fi
        tlds_json="[]"
        if [ "$QUERY_DISQUALIFIED" = true ] && [ -n "$disqualified_response" ]; then
            service_json=$(jq -n \
                --arg service_id "$service" \
                --arg suppliers "$count" \
                --arg test_result "$test_result" \
                --arg status "$overall_status" \
                --arg successful_requests "$successful_requests" \
                --arg failed_requests "$failed_requests" \
                --arg total_requests "$max_requests" \
                --argjson errors "$errors_json" \
                --argjson disqualified_response "$disqualified_response" \
                --argjson tlds "$tlds_json" \
                '{
                    service_id: $service_id,
                    suppliers: ($suppliers | tonumber),
                    test_result: $test_result,
                    status: $status,
                    successful_requests: ($successful_requests | tonumber),
                    failed_requests: ($failed_requests | tonumber),
                    total_requests: ($total_requests | tonumber),
                    errors: $errors,
                    disqualifed_endpoints_response: $disqualified_response,
                    tlds: $tlds
                }')
        else
            service_json=$(jq -n \
                --arg service_id "$service" \
                --arg suppliers "$count" \
                --arg test_result "$test_result" \
                --arg status "$overall_status" \
                --arg successful_requests "$successful_requests" \
                --arg failed_requests "$failed_requests" \
                --arg total_requests "$max_requests" \
                --argjson errors "$errors_json" \
                --argjson tlds "$tlds_json" \
                '{
                    service_id: $service_id,
                    suppliers: ($suppliers | tonumber),
                    test_result: $test_result,
                    status: $status,
                    successful_requests: ($successful_requests | tonumber),
                    failed_requests: ($failed_requests | tonumber),
                    total_requests: ($total_requests | tonumber),
                    errors: $errors,
                    tlds: $tlds
                }')
        fi

        # Add to appropriate category in JSON report based on test results
        if [ "$overall_status" = "success" ]; then
            jq --arg service "$service" --argjson data "$service_json" '.suppliers_passed[$service] = $data' "$TEMP_DIR/json_report.json" >"$TEMP_DIR/temp_report.json" &&
                mv "$TEMP_DIR/temp_report.json" "$TEMP_DIR/json_report.json"
        else
            jq --arg service "$service" --argjson data "$service_json" '.suppliers_failed[$service] = $data' "$TEMP_DIR/json_report.json" >"$TEMP_DIR/temp_report.json" &&
                mv "$TEMP_DIR/temp_report.json" "$TEMP_DIR/json_report.json"
        fi

        # Keep track for console output
        all_results+=("$service,$count,$test_result")

        # Clear the error arrays for next service
        unset error_responses
        unset detailed_errors
    done

    # Add services with no suppliers to the no_suppliers array
    for service in "${SERVICES[@]}"; do
        # Check if this service is in our services_with_suppliers array
        found=false
        for item in "${services_with_suppliers[@]}"; do
            IFS=':' read -r service_with_suppliers count <<<"$item"
            if [ "$service" = "$service_with_suppliers" ]; then
                found=true
                break
            fi
        done

        # If not found in services_with_suppliers, add to no_suppliers
        if [ "$found" = false ]; then
            jq --arg service "$service" '.no_suppliers += [$service]' "$TEMP_DIR/json_report.json" >"$TEMP_DIR/temp_report.json" &&
                mv "$TEMP_DIR/temp_report.json" "$TEMP_DIR/json_report.json"
        fi
    done

    # Add summary to JSON report
    total_services=${#services_with_suppliers[@]}
    successful_services=$(jq '.suppliers_passed | length' "$TEMP_DIR/json_report.json")
    failed_services=$(jq '.suppliers_failed | length' "$TEMP_DIR/json_report.json")
    no_suppliers_count=$(jq '.no_suppliers | length' "$TEMP_DIR/json_report.json")

    summary_json=$(jq -n \
        --arg total "$total_services" \
        --arg successful "$successful_services" \
        --arg failed "$failed_services" \
        --arg no_suppliers "$no_suppliers_count" \
        --arg timestamp "$(date)" \
        --arg disqualified_flag "$QUERY_DISQUALIFIED" \
        '{
            total_services_with_suppliers: ($total | tonumber),
            successful_services: ($successful | tonumber),
            failed_services: ($failed | tonumber),
            no_suppliers_count: ($no_suppliers | tonumber),
            timestamp: $timestamp,
            includes_disqualified_endpoints: ($disqualified_flag == "true")
        }')

    jq --argjson summary "$summary_json" '.summary = $summary' "$TEMP_DIR/json_report.json" >"$TEMP_DIR/temp_report.json" &&
        mv "$TEMP_DIR/temp_report.json" "$TEMP_DIR/json_report.json"

    # Sort suppliers_passed and suppliers_failed by service_id, and sort no_suppliers array
    jq '.suppliers_passed = (.suppliers_passed | to_entries | sort_by(.key) | from_entries) |
        .suppliers_failed = (.suppliers_failed | to_entries | sort_by(.key) | from_entries) |
        .no_suppliers = (.no_suppliers | sort)' "$TEMP_DIR/json_report.json" >"$JSON_REPORT_FILE"

    echo ""
    echo "=============================="
    echo "📋 FINAL REPORT"
    echo "=============================="
    echo "💾 Report saved to: $JSON_REPORT_FILE"

    if [ "$QUERY_DISQUALIFIED" = true ]; then
        echo "📊 Report includes disqualified endpoints data for each service"
    fi

    echo ""
    echo "Legend:"
    echo "  SERVICE ID : Unique identifier for the service tested."
    echo "  SUPPLIERS  : Number of suppliers found for the service."
    echo "  RESULT     : Test status for supplier endpoints:"
    echo "    🟢  All requests passed (all tests succeeded)"
    echo "    🟡  Partial success (some, but not all, requests succeeded)"
    echo "    💔  All requests failed (no successful tests)"
    echo ""
    # Display as a nice table
    printf "%-20s | %-9s | %-7s | %s\n" "SERVICE ID" "SUPPLIERS" "RESULT" "TLDS"
    printf "%-20s-+-%9s-+-%7s-+-%s\n" "--------------------" "---------" "-------" "----------------"

    # Sort and display the results from the all_results array
    IFS=$'\n' sorted_results=($(printf '%s\n' "${all_results[@]}" | sort -t, -k3,3r -k1,1))
    for row in "${sorted_results[@]}"; do
        IFS=',' read -r service count result <<<"$row"
        tlds="${SERVICE_TLDS[$service]}"
        printf "%-20s | %-9s | %-7s | %s\n" "$service" "$count" "$result" "$tlds"
    done

    echo "=============================="
else
    echo ""
    echo "🚫 No services with suppliers found. Skipping tests."

    # Add all services to no_suppliers since none have suppliers
    no_suppliers_json="[]"
    for service in "${SERVICES[@]}"; do
        no_suppliers_json=$(echo "$no_suppliers_json" | jq --arg service "$service" '. += [$service]')
    done

    # Create JSON report with all services in no_suppliers
    summary_json=$(jq -n \
        --arg no_suppliers_count "${#SERVICES[@]}" \
        --arg timestamp "$(date)" \
        --arg disqualified_flag "$QUERY_DISQUALIFIED" \
        '{
            total_services_with_suppliers: 0,
            successful_services: 0,
            failed_services: 0,
            no_suppliers_count: ($no_suppliers_count | tonumber),
            timestamp: $timestamp,
            includes_disqualified_endpoints: ($disqualified_flag == "true")
        }')

    jq -n \
        --argjson no_suppliers "$no_suppliers_json" \
        --argjson summary "$summary_json" \
        '{
            suppliers_failed: {},
            suppliers_passed: {},
            no_suppliers: ($no_suppliers | sort),
            summary: $summary
        }' >"$JSON_REPORT_FILE"

    echo "💾 Empty report saved to: $JSON_REPORT_FILE"
fi
