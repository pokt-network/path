//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
)

/* -------------------- Dockertest Ephemeral PATH Container Setup -------------------- */

// --- USED ONLY FOR E2E TEST MODE ---

// This file is used to setup and tear down an ephemeral PATH container using Dockertest.

const (
	containerName        = "path"
	internalPathPort     = "3069"
	buildContextDir      = ".."
	defaultDockerfile    = "Dockerfile"
	configMountPoint     = ":/app/config/.config.yaml"
	containerEnvImageTag = "IMAGE_TAG=test"
	containerExtraHost   = "host.docker.internal:host-gateway" // allows the container to access the host machine's Docker daemon
	// containerExpirySeconds is the number of seconds after which the started PATH container should be removed by the dockertest library.
	containerExpirySeconds = 300
	// maxPathHealthCheckWaitTimeMillisec is the maximum amount of time a started PATH container has to report its status as healthy.
	// Once this time expires, the associated E2E test is marked as failed and the PATH container is removed.
	maxPathHealthCheckWaitTimeMillisec = 180_000

	// Redis container settings
	redisContainerName = "path-e2e-redis"
	redisImage         = "redis"
	redisImageTag      = "7-alpine"
	redisInternalPort  = "6379"
	// networkName is the Docker network used for container communication
	networkName = "path-e2e-network"
)

// getDockerfileName returns the Dockerfile to use, configurable via TEST_DOCKERFILE env var.
// Use TEST_DOCKERFILE=Dockerfile.race for race detection builds.
func getDockerfileName() string {
	if df := os.Getenv("TEST_DOCKERFILE"); df != "" {
		return df
	}
	return defaultDockerfile
}

// getImageName returns the Docker image name based on the Dockerfile being used.
func getImageName() string {
	if os.Getenv("TEST_DOCKERFILE") == "Dockerfile.race" {
		return "path-image-race"
	}
	return "path-image"
}

// eg. 3069/tcp
var containerPortAndProtocol = internalPathPort + "/tcp"

// redisResource holds the Redis container resource for cleanup
var redisResource *dockertest.Resource

// setupDockerNetwork creates a Docker network for container communication.
// Returns the network ID or empty string if network already exists.
func setupDockerNetwork(t *testing.T, pool *dockertest.Pool) string {
	t.Helper()

	// Check if network already exists
	networks, err := pool.Client.ListNetworks()
	if err != nil {
		t.Fatalf("Could not list networks: %s", err)
	}

	for _, n := range networks {
		if n.Name == networkName {
			fmt.Printf("  📡 Using existing Docker network: %s\n", networkName)
			return n.ID
		}
	}

	// Create the network
	network, err := pool.Client.CreateNetwork(docker.CreateNetworkOptions{
		Name:   networkName,
		Driver: "bridge",
	})
	if err != nil {
		t.Fatalf("Could not create network: %s", err)
	}

	fmt.Printf("  📡 Created Docker network: %s\n", networkName)
	return network.ID
}

// setupRedisContainer starts a Redis container for e2e tests.
// Returns the Redis container resource.
func setupRedisContainer(t *testing.T, pool *dockertest.Pool, networkID string) *dockertest.Resource {
	t.Helper()

	fmt.Println("🔴 Starting Redis container for e2e tests...")

	// Run Redis container
	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Name:       redisContainerName,
		Repository: redisImage,
		Tag:        redisImageTag,
		NetworkID:  networkID,
	}, func(config *docker.HostConfig) {
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	if err != nil {
		t.Fatalf("Could not start Redis container: %s", err)
	}

	if err := resource.Expire(containerExpirySeconds); err != nil {
		t.Fatalf("Could not set Redis container expiry: %s", err)
	}

	// Wait for Redis to be ready
	redisPort := resource.GetPort(redisInternalPort + "/tcp")
	fmt.Printf("  🔴 Redis container started on port %s\n", redisPort)

	// Verify Redis is responding
	if err := pool.Retry(func() error {
		// Simple TCP connection check - Redis responds to PING
		conn, err := pool.Client.InspectContainer(resource.Container.ID)
		if err != nil {
			return err
		}
		if !conn.State.Running {
			return fmt.Errorf("redis container not running")
		}
		return nil
	}); err != nil {
		t.Fatalf("Could not connect to Redis: %s", err)
	}

	fmt.Println("  ✅ Redis container is ready!")
	return resource
}

// cleanupRedisContainer removes the Redis container.
func cleanupRedisContainer(t *testing.T, pool *dockertest.Pool, resource *dockertest.Resource) {
	t.Helper()
	if resource != nil {
		if err := pool.Purge(resource); err != nil {
			t.Logf("Warning: could not purge Redis container: %s", err)
		}
	}
}

// cleanupDockerNetwork removes the Docker network.
func cleanupDockerNetwork(t *testing.T, pool *dockertest.Pool, networkID string) {
	t.Helper()
	if networkID != "" {
		if err := pool.Client.RemoveNetwork(networkID); err != nil {
			t.Logf("Warning: could not remove network: %s", err)
		}
	}
}

// setupPathInstance starts an instance of PATH in a Docker container.
//
// Returns:
// - "pathPort": the dynamically selected and exposed port by the ephemeral PATH container
// - "cleanup": a function that must be called to clean up the PATH container
//   - Test functions are responsible for calling this cleanup function
func setupPathInstance(
	t *testing.T,
	configFilePath string,
	dockerOpts DockerConfig,
) (containerPort string, cleanupFn func()) {
	t.Helper()

	// Initialize dockertest pool first (needed for Redis and network setup)
	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("Could not construct pool: %s", err)
	}
	pool.MaxWait = time.Duration(maxPathHealthCheckWaitTimeMillisec) * time.Millisecond

	// Setup Docker network for container communication
	networkID := setupDockerNetwork(t, pool)

	// Start Redis container first (PATH depends on it)
	redisRes := setupRedisContainer(t, pool, networkID)

	// Initialize the ephemeral PATH Docker container (connected to same network)
	pathResource, containerPort, logOutputFile := setupPathDocker(t, pool, networkID, configFilePath, dockerOpts)

	cleanupFn = func() {
		// Cleanup the ephemeral PATH Docker container
		cleanupPathDocker(t, pool, pathResource)

		// Cleanup Redis container
		cleanupRedisContainer(t, pool, redisRes)

		// Cleanup the network (after all containers are removed)
		cleanupDockerNetwork(t, pool, networkID)

		if logOutputFile != "" {
			fmt.Printf("\n%s===== 👀 LOGS 👀 =====%s\n", BOLD_CYAN, RESET)
			fmt.Printf("\n ✍️ PATH container output logged to %s ✍️ \n\n", logOutputFile)
			fmt.Printf("%s===== 👀 LOGS 👀 =====%s\n\n", BOLD_CYAN, RESET)
		}
	}

	return containerPort, cleanupFn
}

// setupPathDocker sets up and starts a Docker container for the PATH service using dockertest.
//
// Key steps:
// - Builds the container from a specified Dockerfile.
// - Mounts necessary configuration files.
// - Sets environment variables for the container.
// - Exposes required ports and sets extra hosts.
// - Connects to the provided Docker network (for Redis communication).
// - Sets up a signal handler to clean up the container on termination signals.
// - Performs a readiness check to ensure the container is ready for requests.
// - Returns the resource, container port, and log output file path.
func setupPathDocker(
	t *testing.T,
	pool *dockertest.Pool,
	networkID string,
	configFilePath string,
	dockerOpts DockerConfig,
) (*dockertest.Resource, string, string) {
	t.Helper()

	// Get docker options from the global test options
	logContainer := dockerOpts.DockerLog
	forceRebuild := dockerOpts.ForceRebuildImage

	// eg. {file_path}/path/e2e/.shannon.config.yaml
	configFilePath = filepath.Join(os.Getenv("PWD"), configFilePath)

	// Check if config file exists and exit if it does not
	if _, err := os.Stat(configFilePath); os.IsNotExist(err) {
		t.Fatalf("config file does not exist: %s", configFilePath)
	}

	// eg. {file_path}/path/e2e/config/.shannon.config.yaml:/app/config/.config.yaml
	containerConfigMount := configFilePath + configMountPoint

	// Get dockerfile and image name (configurable via TEST_DOCKERFILE env var)
	dockerfileName := getDockerfileName()
	imageName := getImageName()

	// Check if the image already exists and we're not forcing a rebuild
	imageExists := false
	if !forceRebuild {
		if _, err := pool.Client.InspectImage(imageName); err == nil {
			imageExists = true
			fmt.Println("\n🐳 Using existing Docker image, skipping build...")
			fmt.Printf("  💡 TIP: Set %se2e_load_test_config.e2e_config.docker_config.force_rebuild_image: true%s to rebuild the image if needed 💡\n", CYAN, RESET)
		}
	} else {
		fmt.Println("\n🔄 Force rebuild requested, will build Docker image...")
	}

	// Only build the image if it doesn't exist or force rebuild is set
	if !imageExists || forceRebuild {
		fmt.Printf("🏗️  Building Docker image using %s...\n", dockerfileName)

		// Build the image and log build output
		buildOptions := docker.BuildImageOptions{
			Name:           imageName,
			ContextDir:     buildContextDir,
			Dockerfile:     dockerfileName,
			OutputStream:   os.Stdout,
			SuppressOutput: false,
			NoCache:        forceRebuild, // If force rebuilding, also disable cache
		}
		if err := pool.Client.BuildImage(buildOptions); err != nil {
			t.Fatalf("could not build path image: %s", err)
		}
		fmt.Println("🐳 Docker image built successfully!")
	}

	fmt.Println("\n🌿 Starting PATH test container ...")

	// Run the built image - connect to network for Redis communication
	runOpts := &dockertest.RunOptions{
		Name:         containerName,
		Repository:   imageName,
		Mounts:       []string{containerConfigMount},
		Env:          []string{containerEnvImageTag},
		ExposedPorts: []string{containerPortAndProtocol},
		ExtraHosts:   []string{containerExtraHost},
		NetworkID:    networkID, // Connect to same network as Redis
	}
	resource, err := pool.RunWithOptions(runOpts, func(config *docker.HostConfig) {
		config.AutoRemove = true
		config.RestartPolicy = docker.RestartPolicy{Name: "no"}
	})
	if err != nil {
		t.Fatalf("Could not start resource: %s", err)
	}

	// Optionally log the PATH container output
	// Handle container log output based on environment
	var logOutputFile string
	if logContainer {
		var (
			output io.Writer
			dest   string
			f      *os.File
		)

		if isCIEnv() {
			// CI: log to stdout
			output = os.Stdout
			dest = "stdout (CI environment)"
		} else {
			// Local: log to file
			logOutputFile = os.Getenv("DOCKER_LOG_OUTPUT_FILE")
			if logOutputFile == "" {
				logOutputFile = fmt.Sprintf("/tmp/path_log_e2e_test_%d.txt", time.Now().Unix())
			}
			dest = logOutputFile

			var err error
			f, err = os.Create(logOutputFile)
			if err != nil {
				t.Fatalf("could not create log file %s: %v\n", logOutputFile, err)
			}
			output = f
		}

		// Log container output in a goroutine, ensuring file is closed after use
		go func(t *testing.T, f *os.File) {
			t.Helper()
			if f != nil {
				defer f.Close()
			}
			err := pool.Client.Logs(docker.LogsOptions{
				Container:    resource.Container.ID,
				OutputStream: output,
				ErrorStream:  output,
				Stdout:       true,
				Stderr:       true,
				Follow:       true,
			})
			if err != nil {
				log.Fatalf("could not fetch logs for PATH container: %s", err)
			}
		}(t, f)
		fmt.Printf("\n ✍️ PATH container output will be logged to %s ✍️ \n", dest)
	}

	// Create a context that we can cancel
	ctx, cancel := context.WithCancel(context.Background())

	// Set up signal handling
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	// Create a channel to wait for cleanup completion
	cleanupDone := make(chan struct{})

	// Handle signals
	go func() {
		select {
		case <-signalChan:
			fmt.Println("\n⚠️  Received Ctrl+C, cleaning up containers...")
			// Cancel the context
			cancel()

			// Perform cleanup
			if err := pool.Purge(resource); err != nil {
				log.Printf("Could not purge resource: %s", err)
			}

			// Signal that cleanup is done
			close(cleanupDone)

			// Exit the program after cleanup - prevents hanging
			fmt.Println("✅ Cleanup complete, exiting...")
			os.Exit(1)
		case <-ctx.Done():
			// Context was canceled elsewhere
			// Perform cleanup here too in case it wasn't already done
			if err := pool.Purge(resource); err != nil {
				log.Printf("Could not purge resource: %s", err)
			}
			close(cleanupDone)
		}
	}()

	if err := resource.Expire(containerExpirySeconds); err != nil {
		t.Fatalf("[ERROR] Failed to set expiration on docker container: %v", err)
	}

	fmt.Println("  ✅ PATH test container started successfully!")

	// performs a readiness check on the PATH container to ensure it is ready for requests
	// Using /ready instead of deprecated /healthz - /ready checks for sessions and endpoints
	readinessURL := fmt.Sprintf("http://%s/ready", resource.GetHostPort(containerPortAndProtocol))

	fmt.Printf("🏥  Performing readiness check on PATH test container at %s%s%s ...\n", CYAN, readinessURL, RESET)

	poolRetryChan := make(chan struct{}, 1)
	retryConnectFn := func() error {
		resp, err := http.Get(readinessURL)
		if err != nil {
			return fmt.Errorf("unable to connect to readiness endpoint: %w", err)
		}
		defer resp.Body.Close()

		// the readiness endpoint returns a 200 OK status if the service is ready
		// (has sessions and endpoints available)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("readiness endpoint returned non-200 status: %d", resp.StatusCode)
		}

		// notify the pool that the readiness check was successful
		poolRetryChan <- struct{}{}
		return nil
	}
	if err = pool.Retry(retryConnectFn); err != nil {
		t.Fatalf("could not connect to docker: %s", err)
	}

	fmt.Println("  ✅ PATH test container is ready and has active sessions!")

	<-poolRetryChan

	return resource, resource.GetPort(containerPortAndProtocol), logOutputFile
}

// cleanupPathDocker purges the Docker container and resource from the provided dockertest pool and resource.
func cleanupPathDocker(t *testing.T, pool *dockertest.Pool, resource *dockertest.Resource) {
	t.Helper()

	if err := pool.Purge(resource); err != nil {
		t.Fatalf("could not purge resource: %s", err)
	}
}
