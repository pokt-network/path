package storage

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/pokt-network/path/protocol"
	"github.com/pokt-network/path/reputation"
)

// Compile-time check that RedisStorage implements reputation.Storage.
var _ reputation.Storage = (*RedisStorage)(nil)

// RedisStorage implements Storage using Redis as the backend.
// This implementation is suitable for multi-instance deployments
// where reputation data needs to be shared across PATH instances.
//
// Keys are stored as Redis Hashes with the format:
// {keyPrefix}{serviceID}:{endpointAddr}
//
// Each hash contains the following fields:
// - value: the score value (float64)
// - last_updated: Unix timestamp of last update (int64)
// - success_count: total successful requests (int64)
// - error_count: total failed requests (int64)
type RedisStorage struct {
	client    *redis.Client
	keyPrefix string
	ttl       time.Duration
}

// Redis hash field names
const (
	fieldValue        = "value"
	fieldLastUpdated  = "last_updated"
	fieldSuccessCount = "success_count"
	fieldErrorCount   = "error_count"
)

// NewRedisStorage creates a new Redis-backed storage.
// It validates the connection by sending a PING command.
func NewRedisStorage(ctx context.Context, config reputation.RedisConfig, ttl time.Duration) (*RedisStorage, error) {
	config.HydrateDefaults()

	client := redis.NewClient(&redis.Options{
		Addr:         config.Address,
		Password:     config.Password,
		DB:           config.DB,
		PoolSize:     config.PoolSize,
		DialTimeout:  config.DialTimeout,
		ReadTimeout:  config.ReadTimeout,
		WriteTimeout: config.WriteTimeout,
	})

	// Validate connection
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to connect to Redis at %s: %w", config.Address, err)
	}

	return &RedisStorage{
		client:    client,
		keyPrefix: config.KeyPrefix,
		ttl:       ttl,
	}, nil
}

// buildKey constructs the Redis key for an endpoint.
func (r *RedisStorage) buildKey(key reputation.EndpointKey) string {
	return r.keyPrefix + key.String()
}

// parseKey extracts the EndpointKey from a Redis key string.
func (r *RedisStorage) parseKey(redisKey string) (reputation.EndpointKey, bool) {
	// Remove prefix
	withoutPrefix := strings.TrimPrefix(redisKey, r.keyPrefix)
	if withoutPrefix == redisKey {
		return reputation.EndpointKey{}, false // Key didn't have the expected prefix
	}

	// Split into serviceID:endpointAddr
	parts := strings.SplitN(withoutPrefix, ":", 2)
	if len(parts) != 2 {
		return reputation.EndpointKey{}, false
	}

	return reputation.NewEndpointKey(
		protocol.ServiceID(parts[0]),
		protocol.EndpointAddr(parts[1]),
	), true
}

// Get retrieves the score for an endpoint.
func (r *RedisStorage) Get(ctx context.Context, key reputation.EndpointKey) (reputation.Score, error) {
	redisKey := r.buildKey(key)

	result, err := r.client.HGetAll(ctx, redisKey).Result()
	if err != nil {
		return reputation.Score{}, fmt.Errorf("failed to get score from Redis: %w", err)
	}

	if len(result) == 0 {
		return reputation.Score{}, reputation.ErrNotFound
	}

	return r.parseScore(result)
}

// GetMultiple retrieves scores for multiple endpoints using a pipeline.
func (r *RedisStorage) GetMultiple(ctx context.Context, keys []reputation.EndpointKey) (map[reputation.EndpointKey]reputation.Score, error) {
	if len(keys) == 0 {
		return make(map[reputation.EndpointKey]reputation.Score), nil
	}

	// Use pipeline for efficiency
	pipe := r.client.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(keys))

	for i, key := range keys {
		cmds[i] = pipe.HGetAll(ctx, r.buildKey(key))
	}

	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, fmt.Errorf("failed to execute pipeline: %w", err)
	}

	result := make(map[reputation.EndpointKey]reputation.Score, len(keys))
	for i, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil || len(data) == 0 {
			continue // Skip missing entries
		}

		score, err := r.parseScore(data)
		if err != nil {
			continue // Skip malformed entries
		}

		result[keys[i]] = score
	}

	return result, nil
}

// Set stores or updates the score for an endpoint.
func (r *RedisStorage) Set(ctx context.Context, key reputation.EndpointKey, score reputation.Score) error {
	redisKey := r.buildKey(key)

	fields := map[string]interface{}{
		fieldValue:        strconv.FormatFloat(score.Value, 'f', -1, 64),
		fieldLastUpdated:  strconv.FormatInt(score.LastUpdated.Unix(), 10),
		fieldSuccessCount: strconv.FormatInt(score.SuccessCount, 10),
		fieldErrorCount:   strconv.FormatInt(score.ErrorCount, 10),
	}

	pipe := r.client.Pipeline()
	pipe.HSet(ctx, redisKey, fields)

	if r.ttl > 0 {
		pipe.Expire(ctx, redisKey, r.ttl)
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set score in Redis: %w", err)
	}

	return nil
}

// SetMultiple stores or updates scores for multiple endpoints using a pipeline.
func (r *RedisStorage) SetMultiple(ctx context.Context, scores map[reputation.EndpointKey]reputation.Score) error {
	if len(scores) == 0 {
		return nil
	}

	pipe := r.client.Pipeline()

	for key, score := range scores {
		redisKey := r.buildKey(key)

		fields := map[string]interface{}{
			fieldValue:        strconv.FormatFloat(score.Value, 'f', -1, 64),
			fieldLastUpdated:  strconv.FormatInt(score.LastUpdated.Unix(), 10),
			fieldSuccessCount: strconv.FormatInt(score.SuccessCount, 10),
			fieldErrorCount:   strconv.FormatInt(score.ErrorCount, 10),
		}

		pipe.HSet(ctx, redisKey, fields)

		if r.ttl > 0 {
			pipe.Expire(ctx, redisKey, r.ttl)
		}
	}

	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set multiple scores in Redis: %w", err)
	}

	return nil
}

// Delete removes the score for an endpoint.
func (r *RedisStorage) Delete(ctx context.Context, key reputation.EndpointKey) error {
	redisKey := r.buildKey(key)

	err := r.client.Del(ctx, redisKey).Err()
	if err != nil {
		return fmt.Errorf("failed to delete score from Redis: %w", err)
	}

	return nil
}

// List returns all stored endpoint keys, optionally filtered by service ID.
// Uses SCAN for production safety (doesn't block the server).
func (r *RedisStorage) List(ctx context.Context, serviceID string) ([]reputation.EndpointKey, error) {
	var pattern string
	if serviceID != "" {
		pattern = r.keyPrefix + serviceID + ":*"
	} else {
		pattern = r.keyPrefix + "*"
	}

	var keys []reputation.EndpointKey
	var cursor uint64

	for {
		var scanKeys []string
		var err error

		scanKeys, cursor, err = r.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to scan keys from Redis: %w", err)
		}

		for _, redisKey := range scanKeys {
			if endpointKey, ok := r.parseKey(redisKey); ok {
				keys = append(keys, endpointKey)
			}
		}

		if cursor == 0 {
			break
		}
	}

	return keys, nil
}

// Close releases resources held by the storage.
func (r *RedisStorage) Close() error {
	return r.client.Close()
}

// parseScore converts Redis hash data into a reputation.Score.
func (r *RedisStorage) parseScore(data map[string]string) (reputation.Score, error) {
	var score reputation.Score

	if v, ok := data[fieldValue]; ok {
		val, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return score, fmt.Errorf("invalid score value: %w", err)
		}
		score.Value = val
	}

	if v, ok := data[fieldLastUpdated]; ok {
		ts, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return score, fmt.Errorf("invalid last_updated: %w", err)
		}
		score.LastUpdated = time.Unix(ts, 0)
	}

	if v, ok := data[fieldSuccessCount]; ok {
		count, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return score, fmt.Errorf("invalid success_count: %w", err)
		}
		score.SuccessCount = count
	}

	if v, ok := data[fieldErrorCount]; ok {
		count, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return score, fmt.Errorf("invalid error_count: %w", err)
		}
		score.ErrorCount = count
	}

	return score, nil
}
