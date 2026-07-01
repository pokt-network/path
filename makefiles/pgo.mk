###############################################
###   Profile-Guided Optimization (PGO)     ###
###############################################

# Go automatically uses a file named `default.pgo` in the main package
# directory (`cmd/`) when building. All build invocations target `./cmd`, so a
# committed `cmd/default.pgo` is picked up everywhere (local, release, Docker)
# with no flag changes. See cmd/PGO.md for the workflow and refresh cadence.

PGO_PROFILE_PATH ?= cmd/default.pgo

.PHONY: pgo_status
pgo_status: ## Show whether a PGO profile is installed (cmd/default.pgo)
	@if [ -f $(PGO_PROFILE_PATH) ]; then \
		echo "PGO profile present: $(PGO_PROFILE_PATH) ($$(du -h $(PGO_PROFILE_PATH) | cut -f1))"; \
	else \
		echo "No PGO profile at $(PGO_PROFILE_PATH)."; \
		echo "Install one with: make pgo_install PROFILE=/path/to/cpu.prof"; \
	fi

.PHONY: pgo_install
pgo_install: ## Install a prod CPU profile as the PGO profile. Usage: make pgo_install PROFILE=/path/to/cpu.prof
	@if [ -z "$(PROFILE)" ]; then echo "ERROR: set PROFILE=/path/to/cpu.prof"; exit 1; fi
	@if [ ! -f "$(PROFILE)" ]; then echo "ERROR: $(PROFILE) not found"; exit 1; fi
	@echo "Validating $(PROFILE) is a parseable pprof profile..."
	@go tool pprof -raw "$(PROFILE)" >/dev/null 2>&1 || { echo "ERROR: $(PROFILE) is not a valid pprof profile"; exit 1; }
	@cp "$(PROFILE)" $(PGO_PROFILE_PATH)
	@echo "Installed -> $(PGO_PROFILE_PATH)"
	@echo "'go build ./cmd' now uses it automatically. Commit $(PGO_PROFILE_PATH)."

.PHONY: pgo_verify
pgo_verify: ## Verify that building ./cmd applies the PGO profile
	@if [ ! -f $(PGO_PROFILE_PATH) ]; then echo "No $(PGO_PROFILE_PATH) to verify"; exit 1; fi
	@tmpbin=$$(mktemp); \
	go build -o $$tmpbin ./cmd && \
	if go version -m $$tmpbin | grep -q -- "-pgo="; then \
		echo "OK: built binary records PGO ($(PGO_PROFILE_PATH))"; rm -f $$tmpbin; \
	else \
		echo "WARNING: built binary does not record PGO — profile not applied"; rm -f $$tmpbin; exit 1; \
	fi
