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
// What stays in this file: verifyOperator, the operator gRPC auth check
// used by the Node service. Operators continue to speak gRPC. It is
// applied to every Node RPC by the interceptors in
// operator_auth_interceptor.go, not by individual handlers.

// verifyOperator checks validity of the signature submit by operator related request.
//
// Callers get (false, err) on every failure; there is no configuration
// that turns this into a pass. It used to short-circuit on an
// `enforceAuth = false` constant left over from the 1.3 operator
// rollout, which made the whole Node service callable by anyone.
func (r *RpcServer) verifyOperator(ctx context.Context, operatorAddr string) (bool, error) {
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
