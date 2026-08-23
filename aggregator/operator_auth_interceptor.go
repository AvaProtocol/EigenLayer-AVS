package aggregator

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Authentication for the Node gRPC service — the operator plane.
//
// Every RPC on this service either mutates task state or streams task
// data, so all of them need a verified operator identity. Enforcement
// lives in an interceptor rather than in each handler on purpose: a
// per-handler check is one forgotten call away from an unauthenticated
// state change, which is exactly how ReportEventOverload shipped able to
// disable any workflow for any caller.
//
// The model is default-deny. A method is authenticated unless it is
// listed in publicNodeMethods, and an authenticated method must carry an
// operator address for the signature to bind to. A new RPC that has
// neither is refused at runtime until someone makes a deliberate choice
// about which side of the line it belongs on.

// publicNodeMethods are the Node RPCs that may be served without an
// operator identity.
var publicNodeMethods = map[string]struct{}{
	// HealthCheck is a connection probe. It reads no state and writes
	// none, and operators call it before they have working credentials
	// in order to tell "aggregator is down" from "my key is wrong".
	avsproto.Node_HealthCheck_FullMethodName: {},
}

// operatorAddressFromRequest returns the operator address a request
// claims to originate from. The boolean distinguishes "this message type
// has no address field" from "the address field is empty", so the caller
// can fail closed on the former instead of treating it as anonymous.
func operatorAddressFromRequest(req any) (string, bool) {
	switch msg := req.(type) {
	case *avsproto.Checkin:
		return msg.GetAddress(), true
	case *avsproto.SyncMessagesReq:
		return msg.GetAddress(), true
	case *avsproto.NotifyTriggersReq:
		return msg.GetAddress(), true
	case *avsproto.EventOverloadAlert:
		return msg.GetOperatorAddress(), true
	default:
		return "", false
	}
}

// authorizeOperator verifies that the caller holds the key for the
// operator address its request claims. Until this ran, that address was
// an unauthenticated assertion: the aggregator's approved-operator
// allowlist only means something once the claim behind it is proven.
//
// Every failure returns the same opaque error. The reason is logged, not
// returned — an unauthenticated caller should not learn whether an
// address is known, whether a signature parsed, or which of the two it
// got wrong.
func (r *RpcServer) authorizeOperator(ctx context.Context, fullMethod string, req any) error {
	const refusal = "operator authentication required"

	claimedAddress, hasAddress := operatorAddressFromRequest(req)
	if !hasAddress {
		// Fail closed: a non-public RPC whose request carries no
		// operator address cannot be authenticated at all.
		r.config.Logger.Error("refusing Node RPC that carries no operator address to authenticate",
			"method", fullMethod)
		return status.Error(codes.Unauthenticated, refusal)
	}

	if !common.IsHexAddress(claimedAddress) {
		r.config.Logger.Warn("refusing Node RPC with malformed operator address",
			"method", fullMethod, "claimed_address", claimedAddress)
		return status.Error(codes.Unauthenticated, refusal)
	}

	verified, err := r.verifyOperator(ctx, claimedAddress)
	if err != nil || !verified {
		r.config.Logger.Warn("refusing Node RPC with unverified operator signature",
			"method", fullMethod, "claimed_address", claimedAddress, "error", err)
		return status.Error(codes.Unauthenticated, refusal)
	}

	return nil
}

// operatorUnaryInterceptor authenticates every unary Node RPC.
func (r *RpcServer) operatorUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if _, public := publicNodeMethods[info.FullMethod]; public {
			return handler(ctx, req)
		}
		if err := r.authorizeOperator(ctx, info.FullMethod, req); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// operatorStreamInterceptor authenticates every streaming Node RPC.
//
// A streaming RPC carries its operator address in the first client
// message, which has not been received yet when the interceptor runs.
// Wrapping the stream moves the check to the moment that message
// arrives, still before the handler is given it.
func (r *RpcServer) operatorStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if _, public := publicNodeMethods[info.FullMethod]; public {
			return handler(srv, stream)
		}
		return handler(srv, &authenticatedServerStream{
			ServerStream: stream,
			authorize: func(msg any) error {
				return r.authorizeOperator(stream.Context(), info.FullMethod, msg)
			},
		})
	}
}

// authenticatedServerStream authorizes the first message a client sends
// and passes every later one straight through — the operator identity is
// fixed for the life of the stream, so re-verifying an ecrecover on each
// message would only burn CPU.
type authenticatedServerStream struct {
	grpc.ServerStream

	authorize func(any) error
	verified  bool
}

func (s *authenticatedServerStream) RecvMsg(msg any) error {
	if err := s.ServerStream.RecvMsg(msg); err != nil {
		return err
	}
	if s.verified {
		return nil
	}
	if err := s.authorize(msg); err != nil {
		return err
	}
	s.verified = true
	return nil
}
