package health

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
)

const (
	// The image tag is set to the value of the IMAGE_TAG environment variable,
	// which is passed to the Docker image as a build argument at build time.
	// It represent the semver version of PATH (eg. `v0.0.1`).
	imageTagEnvVar = "IMAGE_TAG"
	// If the image tag is not set by the Docker build process, the default value is "development".
	defaultImageTag = "development"
)

// The status of the health check component.
type healthCheckStatus string

const (
	// StatusReady indicates that all PATH components are ready
	statusReady healthCheckStatus = "ready"
	// StatusNotReady indicates that one or more PATH components
	// are still initializing (e.g. warming up caches, etc)
	statusNotReady healthCheckStatus = "not_ready"
)

type (
	// health.Checker struct is used to store all PATH components whose
	// health needs to be checked to consider PATH ready to serve traffic.
	Checker struct {
		Logger            polylog.Logger
		Components        []Check
		ServiceIDReporter ServiceIDReporter
	}

	// health.Check is an interface that must be implemented
	// by components that need to report their health status
	Check interface {
		Name() string // Name returns the name of the component being checked.
		// TODO_FUTURE: consider adding a message/reason for an unhealthy status.
		IsAlive() bool // IsAlive returns true if the component is healthy, otherwise false.
	}

	// ServiceIDReporter is satisfied by the protocol instance and returns
	// the list of service IDs that the protocol instance is configured for.
	ServiceIDReporter interface {
		ConfiguredServiceIDs() map[protocol.ServiceID]struct{}
	}
)

// healthCheckJSON is the JSON structure of the response body
// returned by the `/healthz` endpoint along with the status code.
//
// Deprecated: Use /health for liveness, /ready for readiness, and /config for configuration.
type healthCheckJSON struct {
	// Deprecated indicates this endpoint is deprecated
	Deprecated string `json:"deprecated"`
	// Status is either "ready" or "not_ready". "not_ready" indicates
	// that the service is still warming up its caches, etc.
	Status healthCheckStatus `json:"status"`
	// ImageTag is the semver tag of the PATH Docker image, eg. `v0.0.1`
	// Will default to `development` if not set in the image.
	ImageTag string `json:"imageTag"`
	// ReadyStates is a map of component names to their ready status
	ReadyStates map[string]bool `json:"readyStates,omitempty"`
	// ConfiguredServiceIDs lists the service IDs that the PATH instance is configured for.
	ConfiguredServiceIDs []protocol.ServiceID `json:"configuredServiceIDs,omitempty"`
}

// healthCheckHandler returns the health status of PATH as a JSON response.
//
// It will return a 200 OK status code if all components are ready or
// a 503 Service Unavailable status code if any component is not ready.
//
// Deprecated: Use /health for liveness, /ready for readiness, and /config for configuration.
func (c *Checker) HealthzHandler(w http.ResponseWriter, req *http.Request) {
	readyStates := c.getComponentReadyStates()
	status := getStatus(readyStates)

	responseBytes := c.getHealthCheckResponse(status, readyStates)
	if responseBytes == nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Add deprecation headers per RFC 8594
	w.Header().Set("Deprecation", "true")
	w.Header().Set("Sunset", "2025-06-01")
	w.Header().Set("Link", `</health>; rel="successor-version", </ready>; rel="successor-version", </config>; rel="successor-version"`)
	w.Header().Set("Content-Type", "application/json")

	if status == statusReady {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
	}

	if _, err := w.Write(responseBytes); err != nil {
		c.Logger.Error().Msgf("error writing health check response: %s", err.Error())
	}
}

// getHealthCheckResponse returns the health check JSON response body as bytes
//
// The value of the IMAGE_TAG environment variable is set in the Docker image by a build arg at build time.
// If the IMAGE_TAG environment variable is not set, the default value is "development".
func (c *Checker) getHealthCheckResponse(status healthCheckStatus, readyStates map[string]bool) []byte {
	imageTag := os.Getenv(imageTagEnvVar) // eg. `v0.0.1`
	if imageTag == "" {
		imageTag = defaultImageTag
	}

	healthCheckJSON := healthCheckJSON{
		Deprecated:           "This endpoint is deprecated. Use /health for liveness, /ready for readiness, and /config for configuration.",
		Status:               status,
		ReadyStates:          readyStates,
		ImageTag:             imageTag,
		ConfiguredServiceIDs: c.getConfiguredServiceIDs(),
	}

	responseBytes, err := json.Marshal(healthCheckJSON)
	if err != nil {
		c.Logger.Error().Msgf("error marshaling health check response: %s", err.Error())
		return nil
	}

	return responseBytes
}

// getComponentReadyStates returns a map of component names to their ready status
func (c *Checker) getComponentReadyStates() map[string]bool {
	readyStates := make(map[string]bool)
	for _, component := range c.Components {
		readyStates[component.Name()] = component.IsAlive()
	}

	return readyStates
}

// getConfiguredServiceIDs returns a slice of configured service IDs
func (c *Checker) getConfiguredServiceIDs() []protocol.ServiceID {
	if c.ServiceIDReporter == nil {
		return nil
	}
	configuredServiceIDs := make([]protocol.ServiceID, 0, len(c.ServiceIDReporter.ConfiguredServiceIDs()))
	for serviceID := range c.ServiceIDReporter.ConfiguredServiceIDs() {
		configuredServiceIDs = append(configuredServiceIDs, serviceID)
	}
	slices.Sort(configuredServiceIDs)
	return configuredServiceIDs
}

// getStatus returns false if any component is not ready, otherwise true
func getStatus(readyStates map[string]bool) healthCheckStatus {
	for _, ready := range readyStates {
		if !ready {
			return statusNotReady
		}
	}
	return statusReady
}
