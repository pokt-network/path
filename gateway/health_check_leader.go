// Package gateway provides leader election for health checks in distributed deployments.
//
// The LeaderElector ensures only one PATH instance runs health checks at a time,
// preventing duplicate traffic to endpoints and multiple reputation updates.
//
// # Redis Operations
//
// Acquisition uses SET NX EX (atomic set-if-not-exists with expiry).
//
// Renewal and release use Lua scripts for atomicity. Why Lua scripts?
// These operations require "check-then-act" logic (e.g., "if I own the key, extend it").
// Without atomicity, a race condition can occur:
//
//	Instance A: GET key → sees itself as leader
//	            (key expires here, Instance B acquires leadership)
//	Instance A: EXPIRE key → accidentally extends B's lock!
//
// Lua scripts execute atomically on the Redis server - the entire script runs
// as a single uninterruptible operation. No other Redis commands can execute
// between the GET and EXPIRE, eliminating the race condition.
//
// The scripts are sent to Redis on first use via EVAL, then cached by Redis
// and referenced by SHA1 hash (EVALSHA) for subsequent calls. No pre-registration
// or Redis configuration is required.
package gateway

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/pokt-network/poktroll/pkg/polylog"
	"github.com/redis/go-redis/v9"
)

// LeaderElector provides distributed leader election using Redis.
// Only the leader instance runs health checks to avoid duplicate traffic.
type LeaderElector struct {
	client        *redis.Client
	config        LeaderElectionConfig
	instanceID    string
	isLeader      atomic.Bool
	logger        polylog.Logger
	stopCh        chan struct{}
	renewalCancel context.CancelFunc
}

// LeaderElectorConfig contains configuration for creating a LeaderElector.
type LeaderElectorConfig struct {
	Client *redis.Client
	Config LeaderElectionConfig
	Logger polylog.Logger
}

// NewLeaderElector creates a new LeaderElector.
// Returns nil if the coordination type is "none" or Redis client is nil.
func NewLeaderElector(cfg LeaderElectorConfig) *LeaderElector {
	if cfg.Config.Type == "none" || cfg.Client == nil {
		return nil
	}

	// Generate a unique instance ID (hostname + pid + timestamp)
	hostname, _ := os.Hostname()
	instanceID := hostname + "-" + strconv.Itoa(os.Getpid()) + "-" + time.Now().Format("150405")

	return &LeaderElector{
		client:     cfg.Client,
		config:     cfg.Config,
		instanceID: instanceID,
		logger:     cfg.Logger,
		stopCh:     make(chan struct{}),
	}
}

// Start begins the leader election process.
// It will attempt to acquire leadership immediately, then continue
// renewing/acquiring at the configured interval.
func (l *LeaderElector) Start(ctx context.Context) error {
	// Create a context for the renewal goroutine
	renewCtx, cancel := context.WithCancel(ctx)
	l.renewalCancel = cancel

	// Try to acquire leadership immediately
	l.tryAcquireLeadership(renewCtx)

	// Start the renewal/acquisition goroutine
	go l.runLeadershipLoop(renewCtx)

	return nil
}

// Stop gracefully shuts down the leader elector.
// If this instance is the leader, it will release leadership.
func (l *LeaderElector) Stop() error {
	if l.renewalCancel != nil {
		l.renewalCancel()
	}

	close(l.stopCh)

	// Release leadership if we're the leader
	if l.isLeader.Load() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		l.releaseLeadership(ctx)
	}

	return nil
}

// IsLeader returns true if this instance is currently the leader.
func (l *LeaderElector) IsLeader() bool {
	return l.isLeader.Load()
}

// tryAcquireLeadership attempts to acquire leadership using SET NX EX.
// Returns true if leadership was acquired or renewed.
func (l *LeaderElector) tryAcquireLeadership(ctx context.Context) bool {
	// If we're already the leader, try to renew
	if l.isLeader.Load() {
		return l.renewLeadership(ctx)
	}

	// Try to acquire leadership with SET NX EX
	// This sets the key only if it doesn't exist, with an expiry
	leaseSeconds := int(l.config.LeaseDuration.Seconds())
	ok, err := l.client.SetNX(ctx, l.config.Key, l.instanceID, time.Duration(leaseSeconds)*time.Second).Result()
	if err != nil {
		l.logger.Warn().Err(err).Msg("Failed to acquire leadership")
		l.isLeader.Store(false)
		return false
	}

	if ok {
		l.logger.Info().
			Str("instance_id", l.instanceID).
			Dur("lease_duration", l.config.LeaseDuration).
			Msg("Acquired health check leadership")
		l.isLeader.Store(true)
		return true
	}

	// Someone else is the leader
	l.isLeader.Store(false)
	return false
}

// renewLeadership extends the lease if we're still the leader.
//
// This uses a Lua script to atomically check ownership and extend the lease.
// The script is sent to Redis via EVAL on first call, then cached by Redis
// and executed via EVALSHA (by SHA1 hash) on subsequent calls.
// See package documentation for why atomicity is required here.
func (l *LeaderElector) renewLeadership(ctx context.Context) bool {
	// Lua script: atomically check ownership and extend expiry.
	// KEYS[1] = leader key, ARGV[1] = our instance ID, ARGV[2] = lease seconds.
	// Returns 1 if extended (we own the key), 0 otherwise.
	script := redis.NewScript(`
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("expire", KEYS[1], ARGV[2])
		end
		return 0
	`)

	leaseSeconds := int(l.config.LeaseDuration.Seconds())
	result, err := script.Run(ctx, l.client, []string{l.config.Key}, l.instanceID, leaseSeconds).Int()
	if err != nil {
		l.logger.Warn().Err(err).Msg("Failed to renew leadership")
		l.isLeader.Store(false)
		return false
	}

	if result == 1 {
		l.logger.Debug().
			Str("instance_id", l.instanceID).
			Msg("Renewed health check leadership")
		l.isLeader.Store(true)
		return true
	}

	// Someone else took leadership
	l.logger.Info().Msg("Lost health check leadership")
	l.isLeader.Store(false)
	return false
}

// releaseLeadership releases leadership by deleting the key.
//
// This uses a Lua script to atomically check ownership before deleting.
// Without this, we could accidentally delete another instance's lock if
// leadership changed between checking and deleting.
// See package documentation for why atomicity is required here.
func (l *LeaderElector) releaseLeadership(ctx context.Context) {
	// Lua script: atomically check ownership and delete.
	// KEYS[1] = leader key, ARGV[1] = our instance ID.
	// Returns 1 if deleted (we owned the key), 0 otherwise.
	script := redis.NewScript(`
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("del", KEYS[1])
		end
		return 0
	`)

	_, err := script.Run(ctx, l.client, []string{l.config.Key}, l.instanceID).Int()
	if err != nil {
		l.logger.Warn().Err(err).Msg("Failed to release leadership")
		return
	}

	l.logger.Info().Msg("Released health check leadership")
	l.isLeader.Store(false)
}

// runLeadershipLoop periodically renews or attempts to acquire leadership.
func (l *LeaderElector) runLeadershipLoop(ctx context.Context) {
	ticker := time.NewTicker(l.config.RenewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-l.stopCh:
			return
		case <-ticker.C:
			l.tryAcquireLeadership(ctx)
		}
	}
}

// GetLeaderInstanceID returns the current leader's instance ID.
// Returns empty string if no leader or on error.
func (l *LeaderElector) GetLeaderInstanceID(ctx context.Context) string {
	result, err := l.client.Get(ctx, l.config.Key).Result()
	if err != nil {
		return ""
	}
	return result
}
