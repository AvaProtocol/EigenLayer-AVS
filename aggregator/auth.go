package aggregator

import (
	"context"
	"fmt"

	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"google.golang.org/grpc/metadata"
)

// The public Aggregator gRPC service has been removed (REST migration).
// The handlers that lived here — GetKey, GetSignatureFormat — moved to
// the REST surface under POST /api/v1/auth:exchange; the verifyAuth
// helper they shared with the other Aggregator handlers is no longer
// needed because REST middleware verifies JWTs in the handler stack.
//
// What stays in this file: verifyOperator, the operator-stream gRPC
// auth check used by the Node service. Operators continue to speak
// gRPC.

// verifyOperator checks validity of the signature submit by operator related request
func (r *RpcServer) verifyOperator(ctx context.Context, operatorAddr string) (bool, error) {
	// TODO: Temporary not enforce auth
	if !enforceAuth {
		return true, nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false, fmt.Errorf("cannot read metadata from request")
	}

	authRawHeaders := md.Get("authorization")
	if len(authRawHeaders) < 1 {
		return false, fmt.Errorf("missing auth header")
	}

	return auth.VerifyOperator(authRawHeaders[0], operatorAddr)
}

// enforceAuth toggles operator-side auth enforcement. Same default as
// the legacy file: false until all operators upgrade past 1.3.
const enforceAuth = false
