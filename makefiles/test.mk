# DEV_NOTE: DO NOT CHANGE the (cd e2e && go test ...) to the (go test ... e2e)
# in the helpers below. This is needed to ensure the logs are beautified as expected.

#########################
### Test Make Targets ###
#########################

.PHONY: test_all ## Run all unit tests and E2E test a subset of key services.
test_all: test_unit
	@$(MAKE) e2e_test eth,poly,xrplevm-testnet,oasys

.PHONY: test_unit
test_unit: ## Run all unit tests
	go test ./... -short -count=1

.PHONY: go_lint
go_lint: ## Run all go linters
	golangci-lint run --timeout 5m --build-tags test

#################
### E2E Tests ###
#################

.PHONY: e2e_test_all
e2e_test_all: shannon_e2e_config_warning check_docker ## Run HTTP-only E2E tests for all service IDs
	(cd e2e && TEST_MODE=e2e TEST_PROTOCOL=shannon go test -v -tags=e2e -count=1 -run Test_PATH_E2E)

.PHONY: e2e_test
e2e_test: shannon_e2e_config_warning check_docker ## Run HTTP-only E2E tests with specified service IDs (e.g. make e2e_test eth,xrplevm)
	@if [ "$(filter-out $@,$(MAKECMDGOALS))" = "" ]; then \
		echo "$(RED)$(BOLD)❌ Error: Service IDs are required (comma-separated list)$(RESET)"; \
		echo "  👀 Example: $(CYAN)make e2e_test eth,xrplevm$(RESET)"; \
		echo "  💡 To run with default service IDs, use: $(CYAN)make e2e_test_all$(RESET)"; \
		exit 1; \
	fi
	(cd e2e && TEST_MODE=e2e TEST_PROTOCOL=shannon TEST_SERVICE_IDS=$(filter-out $@,$(MAKECMDGOALS)) go test -v -tags=e2e -count=1 -run Test_PATH_E2E)

# Websocket E2E Tests
.PHONY: e2e_test_websocket_all
e2e_test_websocket_all: shannon_e2e_config_warning check_docker ## Run Websocket-only E2E tests for all Websocket-compatible services
	(cd e2e && TEST_MODE=e2e TEST_PROTOCOL=shannon TEST_WEBSOCKETS=true go test -v -tags=e2e -count=1 -run Test_PATH_E2E)

.PHONY: e2e_test_websocket
e2e_test_websocket: shannon_e2e_config_warning check_docker ## Run Websocket-only E2E tests with specified service IDs (e.g. make e2e_test_websocket xrplevm)
	@if [ "$(filter-out $@,$(MAKECMDGOALS))" = "" ]; then \
		echo "$(RED)$(BOLD)❌ Error: Service IDs are required (comma-separated list)$(RESET)"; \
		echo "  👀 Example: $(CYAN)make e2e_test_websocket xrplevm,xrplevm-testnet$(RESET)"; \
		echo "  💡 To run all Websocket-compatible services, use: $(CYAN)make e2e_test_websocket_all$(RESET)"; \
		exit 1; \
	fi
	(cd e2e && TEST_MODE=e2e TEST_PROTOCOL=shannon TEST_SERVICE_IDS=$(filter-out $@,$(MAKECMDGOALS)) TEST_WEBSOCKETS=true go test -v -tags=e2e -count=1 -run Test_PATH_E2E)

.PHONY: e2e_test_eth_fallback
e2e_test_eth_fallback: shannon_e2e_config_warning check_docker ## Run an E2E Shannon relay test with ETH fallback enabled (requires FALLBACK_URL)
	@if [ "$(filter-out $@,$(MAKECMDGOALS))" = "" ]; then \
		echo "$(RED)$(BOLD)❌ Error: ETH fallback URL is required$(RESET)"; \
		echo "  👀 Example: $(CYAN)make e2e_test_eth_fallback https://eth.rpc.backup.io$(RESET)"; \
		echo "  💡 Usage: $(CYAN)make e2e_test_eth_fallback <FALLBACK_URL>$(RESET)"; \
		echo "  📝 The fallback URL should be a valid HTTP/HTTPS endpoint for ETH service"; \
		exit 1; \
	fi
	./e2e/scripts/run_eth_fallback_test.sh $(filter-out $@,$(MAKECMDGOALS))

#######################
### Race E2E Tests ###
#######################

# Race detection E2E tests - builds PATH with -race flag to detect data races
# Note: Race builds are slower (~10x) and use more memory. Use for validation, not CI.

.PHONY: e2e_test_race
e2e_test_race: shannon_e2e_config_warning check_docker ## Run E2E tests with race detection (e.g. make e2e_test_race eth)
	@if [ "$(filter-out $@,$(MAKECMDGOALS))" = "" ]; then \
		echo "$(RED)$(BOLD)❌ Error: Service IDs are required (comma-separated list)$(RESET)"; \
		echo "  👀 Example: $(CYAN)make e2e_test_race eth,xrplevm$(RESET)"; \
		exit 1; \
	fi
	@echo "$(YELLOW)⚠️  Building PATH with race detector enabled (this may take longer)$(RESET)"
	(cd e2e && TEST_MODE=e2e TEST_PROTOCOL=shannon TEST_DOCKERFILE=Dockerfile.race TEST_SERVICE_IDS=$(filter-out $@,$(MAKECMDGOALS)) go test -v -tags=e2e -count=1 -run Test_PATH_E2E)

.PHONY: e2e_test_race_all
e2e_test_race_all: shannon_e2e_config_warning check_docker ## Run all E2E tests with race detection
	@echo "$(YELLOW)⚠️  Building PATH with race detector enabled (this may take longer)$(RESET)"
	(cd e2e && TEST_MODE=e2e TEST_PROTOCOL=shannon TEST_DOCKERFILE=Dockerfile.race go test -v -tags=e2e -count=1 -run Test_PATH_E2E)


##################
### Load Tests ###
##################

# HTTP Load Tests
.PHONY: load_test_all
load_test_all: ## Run HTTP-only load tests for all service IDs
	(cd e2e && TEST_MODE=load TEST_PROTOCOL=shannon go test -v -tags=e2e -count=1 -run Test_PATH_E2E)

.PHONY: load_test
load_test: ## Run HTTP-only load tests with specified service IDs (e.g. make load_test eth,xrplevm)
	@if [ "$(filter-out $@,$(MAKECMDGOALS))" = "" ]; then \
		echo "$(RED)$(BOLD)❌ Error: Service IDs are required (comma-separated list)$(RESET)"; \
		echo "  👀 Example: $(CYAN)make load_test eth,xrplevm$(RESET)"; \
		echo "  💡 To run with default service IDs, use: $(CYAN)make load_test_all$(RESET)"; \
		exit 1; \
	fi
	@(cd e2e && TEST_MODE=load TEST_PROTOCOL=shannon TEST_SERVICE_IDS=$(filter-out $@,$(MAKECMDGOALS)) go test -v -tags=e2e -count=1 -run Test_PATH_E2E)

# Websocket Load Tests
.PHONY: load_test_websocket_all
load_test_websocket_all: ## Run Websocket-only load tests for all Websocket-compatible services
	(cd e2e && TEST_MODE=load TEST_PROTOCOL=shannon TEST_WEBSOCKETS=true go test -v -tags=e2e -count=1 -run Test_PATH_E2E)

.PHONY: load_test_websocket
load_test_websocket: ## Run Websocket-only load tests with specified service IDs (e.g. make load_test_websocket xrplevm)
	@if [ "$(filter-out $@,$(MAKECMDGOALS))" = "" ]; then \
		echo "$(RED)$(BOLD)❌ Error: Service IDs are required (comma-separated list)$(RESET)"; \
		echo "  👀 Example: $(CYAN)make load_test_websocket xrplevm,xrplevm-testnet$(RESET)"; \
		echo "  💡 To run all Websocket-compatible services, use: $(CYAN)make load_test_websocket_all$(RESET)"; \
		exit 1; \
	fi
	@(cd e2e && TEST_MODE=load TEST_PROTOCOL=shannon TEST_SERVICE_IDS=$(filter-out $@,$(MAKECMDGOALS)) TEST_WEBSOCKETS=true go test -v -tags=e2e -count=1 -run Test_PATH_E2E)

.PHONY: config_enable_grove_fallback
config_enable_grove_fallback: ## Enable fallback endpoints for all services in PATH config
	@echo "🔧 Enabling fallback endpoints for all services..."
	@echo "📝 Updating local/path/.config.yaml to send all traffic to fallback endpoints"
	@yq eval '.shannon_config.gateway_config.service_fallback[].send_all_traffic = true' -i local/path/.config.yaml
	@echo "✅ Fallback endpoints enabled for all services"