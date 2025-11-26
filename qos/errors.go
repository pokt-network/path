package qos

import (
	"github.com/pokt-network/poktroll/pkg/polylog"

	"github.com/pokt-network/path/qos/jsonrpc"
)

// RequestContextFromInternalError returns a request context for an internal error.
// E.g. the selected endpoint did not return a response.
func RequestContextFromInternalError(logger polylog.Logger, jsonrpcRequestID jsonrpc.ID, err error) *RequestErrorContext {
	return &RequestErrorContext{
		Logger:   logger,
		Response: jsonrpc.NewErrResponseInternalErr(jsonrpcRequestID, err),
	}
}
