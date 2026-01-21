package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/protocol"
)

// BlockHeightValidator validates block heights from health check responses.
type BlockHeightValidator struct {
	logger        polylog.Logger
	externalCache *ExternalReferenceCache
	qosInstances  map[protocol.ServiceID]QoSService
}

// NewBlockHeightValidator creates a new block height validator.
func NewBlockHeightValidator(
	logger polylog.Logger,
	externalCache *ExternalReferenceCache,
) *BlockHeightValidator {
	return &BlockHeightValidator{
		logger:        logger.With("component", "block_height_validator"),
		externalCache: externalCache,
		qosInstances:  make(map[protocol.ServiceID]QoSService),
	}
}

// SetQoSInstances sets the QoS instances for accessing perceived block numbers.
func (v *BlockHeightValidator) SetQoSInstances(instances map[protocol.ServiceID]QoSService) {
	v.qosInstances = instances
}

// ValidateBlockHeight validates a health check response's block height against the configured reference.
// Returns an error if the validation fails, nil if it passes.
func (v *BlockHeightValidator) ValidateBlockHeight(
	ctx context.Context,
	serviceID protocol.ServiceID,
	responseBody []byte,
	validation *BlockHeightValidation,
) error {
	if validation == nil {
		return nil // No validation configured
	}

	// Extract block height from response
	endpointHeight, err := v.extractBlockHeight(responseBody)
	if err != nil {
		return fmt.Errorf("failed to extract block height: %w", err)
	}

	// Get reference height based on type
	referenceHeight, err := v.getReferenceHeight(ctx, serviceID, &validation.Reference)
	if err != nil {
		// Log warning but don't fail the health check
		// External endpoint failures shouldn't cause false negatives
		v.logger.Warn().
			Err(err).
			Str("service_id", string(serviceID)).
			Str("reference_type", string(validation.Reference.Type)).
			Msg("Failed to get reference block height, skipping validation")
		return nil
	}

	// Perform comparison
	passed := v.compareHeightsWithTolerance(endpointHeight, referenceHeight, validation.Reference.Tolerance, validation.Operator)

	// Log validation result
	logEvent := v.logger.Debug().
		Str("service_id", string(serviceID)).
		Int64("endpoint_height", endpointHeight).
		Int64("reference_height", referenceHeight).
		Int64("tolerance", validation.Reference.Tolerance).
		Str("operator", string(validation.Operator)).
		Str("reference_type", string(validation.Reference.Type)).
		Bool("passed", passed)

	if passed {
		logEvent.Msg("✅ Block height validation passed")
		return nil
	}

	logEvent.Msg("❌ Block height validation failed")
	return fmt.Errorf(
		"block height validation failed: endpoint height %d %s reference %d (tolerance: %d)",
		endpointHeight,
		validation.Operator,
		referenceHeight,
		validation.Reference.Tolerance,
	)
}

// extractBlockHeight extracts the block height from a JSON-RPC response.
// Supports various response formats:
// - {"result": "0x1940c6f5"} (EVM eth_blockNumber)
// - {"result": {"sync_info": {"latest_block_height": "12345"}}} (Cosmos status)
func (v *BlockHeightValidator) extractBlockHeight(responseBody []byte) (int64, error) {
	var response map[string]interface{}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return 0, fmt.Errorf("invalid JSON: %w", err)
	}

	// Check for JSON-RPC error
	if errField, exists := response["error"]; exists && errField != nil {
		return 0, fmt.Errorf("JSON-RPC error in response")
	}

	result, exists := response["result"]
	if !exists {
		return 0, fmt.Errorf("no result field in response")
	}

	// Try different formats
	switch v := result.(type) {
	case string:
		// EVM format: "0x1940c6f5"
		return parseHexBlockNumber(v)

	case float64:
		// Already a number
		return int64(v), nil

	case map[string]interface{}:
		// Cosmos format: {"sync_info": {"latest_block_height": "12345"}}
		if syncInfo, ok := v["sync_info"].(map[string]interface{}); ok {
			if heightStr, ok := syncInfo["latest_block_height"].(string); ok {
				height, err := strconv.ParseInt(heightStr, 10, 64)
				if err != nil {
					return 0, fmt.Errorf("invalid block height in sync_info: %w", err)
				}
				return height, nil
			}
		}

		return 0, fmt.Errorf("unsupported response format: object")

	default:
		return 0, fmt.Errorf("unsupported result type: %T", v)
	}
}

// getReferenceHeight gets the reference block height based on the reference type.
func (v *BlockHeightValidator) getReferenceHeight(
	ctx context.Context,
	serviceID protocol.ServiceID,
	ref *BlockHeightReference,
) (int64, error) {
	switch ref.Type {
	case BlockHeightReferenceTypeStatic:
		return ref.Value, nil

	case BlockHeightReferenceTypeExternal:
		return v.externalCache.GetBlockHeight(
			ctx,
			ref.Endpoint,
			ref.Method,
			ref.Headers,
			ref.Timeout,
			ref.CacheDuration,
		)

	case BlockHeightReferenceTypePerceived:
		qos, exists := v.qosInstances[serviceID]
		if !exists {
			return 0, fmt.Errorf("no QoS instance for service %s", serviceID)
		}

		// Get perceived block number from QoS
		// This requires adding a method to the QoS interface
		perceivedGetter, ok := qos.(interface{ GetPerceivedBlockNumber() uint64 })
		if !ok {
			return 0, fmt.Errorf("QoS instance does not support perceived block number")
		}

		perceived := perceivedGetter.GetPerceivedBlockNumber()
		if perceived == 0 {
			return 0, fmt.Errorf("perceived block number not yet available")
		}

		return int64(perceived), nil

	default:
		return 0, fmt.Errorf("unsupported reference type: %s", ref.Type)
	}
}

// compareHeightsWithTolerance compares heights with tolerance for equality operator.
func (v *BlockHeightValidator) compareHeightsWithTolerance(
	endpoint int64,
	reference int64,
	tolerance int64,
	operator BlockHeightOperator,
) bool {
	switch operator {
	case BlockHeightOperatorGreaterThanOrEqual:
		adjustedRef := reference - tolerance
		return endpoint >= adjustedRef

	case BlockHeightOperatorGreaterThan:
		adjustedRef := reference - tolerance
		return endpoint > adjustedRef

	case BlockHeightOperatorLessThanOrEqual:
		adjustedRef := reference + tolerance
		return endpoint <= adjustedRef

	case BlockHeightOperatorLessThan:
		adjustedRef := reference + tolerance
		return endpoint < adjustedRef

	case BlockHeightOperatorEqual:
		// For equality, check if within tolerance range
		diff := endpoint - reference
		if diff < 0 {
			diff = -diff
		}
		return diff <= tolerance

	default:
		return false
	}
}
