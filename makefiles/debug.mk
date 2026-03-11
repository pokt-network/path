#####################
### Debug targets ###
#####################

.PHONY: check_graphviz
# Internal helper: Check if graphviz is installed locally
check_graphviz:
	@if ! command -v graphviz >/dev/null 2>&1; then \
		echo "Graphviz is not installed. Visit graphviz.org before continuing"; \
		exit 1; \
	fi;

.PHONY: debug_goroutines
# Deploys pprof (using graphviz) using docker at http://localhost:8081/ui/
# Ref: https://www.graphviz.org/download
#
# TODO_TECHDEBT(@adshmh): Remove host networking mode flag (--network=host) and
# use a standard method of accessing localhost:6060 within the container.
# This is needed to avoid the host from needing to download graphviz.
debug_goroutines: check_docker
	@docker run --rm \
		--network=host \
		golang:1.25-alpine \
		apk add --no-cache graphviz && \
		go tool pprof -http="0.0.0.0:8081" http://localhost:6060/debug/pprof/goroutine
