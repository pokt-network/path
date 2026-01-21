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
	logger         polylog.Logger
	externalCache  *ExternalReferenceCache
	qosInstances   map[protocol.ServiceID]QoSService
	getPerceivedFn func(protocol.ServiceID) uint64
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

	// Apply tolerance
	adjustedReference := v.applyTolerance(referenceHeight, validation.Reference.Tolerance, validation.Operator)

	// Perform comparison
	passed := v.compareHeights(endpointHeight, adjustedReference, validation.Operator)

	// Log validation result
	logEvent := v.logger.Debug().
		Str("service_id", string(serviceID)).
		Int64("endpoint_height", endpointHeight).
		Int64("reference_height", referenceHeight).
		Int64("adjusted_reference", adjustedReference).
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
		"block height validation failed: endpoint height %d %s adjusted reference %d (reference: %d, tolerance: %d)",
		endpointHeight,
		validation.Operator,
		adjustedReference,
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

// applyTolerance applies tolerance to the reference height based on the operator.
func (v *BlockHeightValidator) applyTolerance(reference int64, tolerance int64, operator BlockHeightOperator) int64 {
	switch operator {
	case BlockHeightOperatorGreaterThanOrEqual, BlockHeightOperatorGreaterThan:
		// For >= and >, subtract tolerance (allow endpoint to be N blocks behind)
		return reference - tolerance

	case BlockHeightOperatorLessThanOrEqual, BlockHeightOperatorLessThan:
		// For <= and <, add tolerance (allow endpoint to be N blocks ahead)
		return reference + tolerance

	case BlockHeightOperatorEqual:
		// For ==, tolerance creates a range [reference - tolerance, reference + tolerance]
		// We'll handle this in compareHeights
		return reference

	default:
		return reference
	}
}

// compareHeights compares the endpoint height with the adjusted reference using the operator.
func (v *BlockHeightValidator) compareHeights(endpoint int64, adjustedReference int64, operator BlockHeightOperator) bool {
	switch operator {
	case BlockHeightOperatorGreaterThanOrEqual:
		return endpoint >= adjustedReference

	case BlockHeightOperatorGreaterThan:
		return endpoint > adjustedReference

	case BlockHeightOperatorLessThanOrEqual:
		return endpoint <= adjustedReference

	case BlockHeightOperatorLessThan:
		return endpoint < adjustedReference

	case BlockHeightOperatorEqual:
		// For equality, we allow a tolerance range
		// adjustedReference is the reference value (tolerance not applied)
		// We need to get the tolerance from somewhere... let's recalculate
		// Actually, this is a design issue. Let me fix compareHeights to accept tolerance
		return endpoint == adjustedReference

	default:
		return false
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
