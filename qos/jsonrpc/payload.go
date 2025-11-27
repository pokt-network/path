package jsonrpc

import (
	"encoding/json"
	"net/http"

	sharedtypes "github.com/pokt-network/poktroll/x/shared/types"

	"github.com/pokt-network/path/protocol"
)

func (r Request) BuildPayload() (protocol.Payload, error) {
	reqBz, err := r.MarshalJSON()
	if err != nil {
		return protocol.Payload{}, err
	}

	return protocol.Payload{
		Data:    string(reqBz),
		Method:  http.MethodPost, // Method is always POST for JSON-RPC.
		Headers: map[string]string{},
		RPCType: sharedtypes.RPCType_JSON_RPC,
	}, nil
}

func GetJsonRpcReqFromServicePayload(servicePayload protocol.Payload) (Request, error) {
	var jsonrpcReq Request
	err := json.Unmarshal([]byte(servicePayload.Data), &jsonrpcReq)
	if err != nil {
		return Request{}, err
	}
	return jsonrpcReq, nil
}
