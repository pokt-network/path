package request

import (
	"fmt"

	"github.com/pokt-network/path/gateway"
)

var (
	// Wrap the Gateway error.
	// Allows the gateway package to recognize the type of error.
	errNoServiceIDProvided = fmt.Errorf("no service ID provided in '%s' header: %w", HTTPHeaderTargetServiceID, gateway.ErrGatewayNoServiceIDProvided)
)

// Use JSON-formatted HTTP payload for user errors.
// Not spec-required but expected by Gateway users.
const parserErrorTemplate = `{"code":%d,"message":"%s"}`

/* Parser Error Response */

type parserErrorResponse struct {
	err  string
	code int
}

func (r *parserErrorResponse) GetPayload() []byte {
	return []byte(fmt.Sprintf(parserErrorTemplate, r.code, r.err))
}

func (r *parserErrorResponse) GetHTTPStatusCode() int {
	return r.code
}

func (r *parserErrorResponse) GetHTTPHeaders() map[string]string {
	return map[string]string{}
}
