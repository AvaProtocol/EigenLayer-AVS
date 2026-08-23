package aggregator

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/signer"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// newAuthTestServer builds the minimal RpcServer the interceptors touch —
// they only ever reach config.Logger.
func newAuthTestServer() *RpcServer {
	return &RpcServer{
		config: &config.Config{Logger: testutil.GetLogger()},
	}
}

// operatorCredentials mints the same Authorization header the operator
// client sends (core/auth.ClientAuth), for the given key and epoch.
func operatorCredentials(t *testing.T, key *ecdsa.PrivateKey, epoch int64) context.Context {
	t.Helper()
	return operatorCredentialsFor(t, key, crypto.PubkeyToAddress(key.PublicKey).Hex(), epoch)
}

// operatorCredentialsFor signs as `key` while naming `operatorAddress` as
// the operator being acted for — the alias-key shape.
func operatorCredentialsFor(t *testing.T, key *ecdsa.PrivateKey, operatorAddress string, epoch int64) context.Context {
	t.Helper()
	token, err := signer.SignMessageAsHex(key, auth.GetOperatorSigninMessage(operatorAddress, epoch))
	if err != nil {
		t.Fatalf("signing operator message: %v", err)
	}
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", fmt.Sprintf("Bearer %d.%s", epoch, token),
	))
}

func newOperatorKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generating operator key: %v", err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey).Hex()
}

// callUnary runs a request through the unary interceptor and reports
// whether the handler was reached.
func callUnary(t *testing.T, server *RpcServer, ctx context.Context, method string, req any) (bool, error) {
	t.Helper()
	reached := false
	handler := func(context.Context, any) (any, error) {
		reached = true
		return nil, nil
	}
	_, err := server.operatorUnaryInterceptor()(ctx, req, &grpc.UnaryServerInfo{FullMethod: method}, handler)
	return reached, err
}

func requireUnauthenticated(t *testing.T, reached bool, err error) {
	t.Helper()
	if reached {
		t.Fatal("handler ran for a request that should have been refused")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v (%v)", status.Code(err), err)
	}
}

// The reported vulnerability: ReportEventOverload disabled any workflow
// for any caller, with no credentials at all.
func TestReportEventOverloadRejectsUnauthenticatedCaller(t *testing.T) {
	server := newAuthTestServer()
	alert := &avsproto.EventOverloadAlert{
		TaskId:          "01JZ0000000000000000000000",
		OperatorAddress: "0x0000000000000000000000000000000000000000",
	}

	reached, err := callUnary(t, server, context.Background(),
		avsproto.Node_ReportEventOverload_FullMethodName, alert)
	requireUnauthenticated(t, reached, err)
}

// Every state-changing Node RPC, not just the reported one.
func TestNodeRPCsRejectUnauthenticatedCallers(t *testing.T) {
	server := newAuthTestServer()
	_, address := newOperatorKey(t)

	cases := []struct {
		name   string
		method string
		req    any
	}{
		{"Ping", avsproto.Node_Ping_FullMethodName, &avsproto.Checkin{Address: address}},
		{"NotifyTriggers", avsproto.Node_NotifyTriggers_FullMethodName, &avsproto.NotifyTriggersReq{Address: address}},
		{"ReportEventOverload", avsproto.Node_ReportEventOverload_FullMethodName, &avsproto.EventOverloadAlert{OperatorAddress: address}},
		// Ack carries no operator address, so it cannot be authenticated
		// and must fail closed rather than fall through as anonymous.
		{"Ack", avsproto.Node_Ack_FullMethodName, &avsproto.AckMessageReq{Id: "some-id"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reached, err := callUnary(t, server, context.Background(), tc.method, tc.req)
			requireUnauthenticated(t, reached, err)
		})
	}
}

// A correctly signed operator gets through.
func TestNodeRPCAcceptsSignedOperator(t *testing.T) {
	server := newAuthTestServer()
	key, address := newOperatorKey(t)
	ctx := operatorCredentials(t, key, time.Now().Unix())

	reached, err := callUnary(t, server, ctx,
		avsproto.Node_NotifyTriggers_FullMethodName,
		&avsproto.NotifyTriggersReq{Address: address})
	if err != nil {
		t.Fatalf("signed operator was refused: %v", err)
	}
	if !reached {
		t.Fatal("handler did not run for a signed operator")
	}
}

// The core of the escalation: holding one key must not let you speak as
// another operator. The approved-operator allowlist is only meaningful
// if this holds.
func TestOperatorCannotImpersonateAnotherAddress(t *testing.T) {
	server := newAuthTestServer()
	attackerKey, _ := newOperatorKey(t)
	_, victimAddress := newOperatorKey(t)

	ctx := operatorCredentials(t, attackerKey, time.Now().Unix())

	reached, err := callUnary(t, server, ctx,
		avsproto.Node_NotifyTriggers_FullMethodName,
		&avsproto.NotifyTriggersReq{Address: victimAddress})
	requireUnauthenticated(t, reached, err)
}

// An expired signature is refused, so a header captured off the
// plaintext wire has a bounded life.
func TestExpiredOperatorSignatureRejected(t *testing.T) {
	server := newAuthTestServer()
	key, address := newOperatorKey(t)
	ctx := operatorCredentials(t, key, time.Now().Add(-time.Hour).Unix())

	reached, err := callUnary(t, server, ctx,
		avsproto.Node_Ping_FullMethodName, &avsproto.Checkin{Address: address})
	requireUnauthenticated(t, reached, err)
}

// A far-future epoch would otherwise mint a credential that never expires.
func TestFarFutureOperatorSignatureRejected(t *testing.T) {
	server := newAuthTestServer()
	key, address := newOperatorKey(t)
	ctx := operatorCredentials(t, key, time.Now().Add(24*time.Hour).Unix())

	reached, err := callUnary(t, server, ctx,
		avsproto.Node_Ping_FullMethodName, &avsproto.Checkin{Address: address})
	requireUnauthenticated(t, reached, err)
}

// HealthCheck stays reachable: operators use it to tell "aggregator is
// down" from "my credentials are wrong".
func TestHealthCheckRemainsPublic(t *testing.T) {
	server := newAuthTestServer()

	reached, err := callUnary(t, server, context.Background(),
		avsproto.Node_HealthCheck_FullMethodName, &avsproto.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check was refused: %v", err)
	}
	if !reached {
		t.Fatal("health check handler did not run")
	}
}

// Default-deny: a future RPC that is neither listed public nor carries an
// operator address must be refused rather than served anonymously.
func TestUnknownRequestTypeFailsClosed(t *testing.T) {
	server := newAuthTestServer()
	key, _ := newOperatorKey(t)
	ctx := operatorCredentials(t, key, time.Now().Unix())

	reached, err := callUnary(t, server, ctx,
		"/aggregator.Node/SomeFutureMethod", &avsproto.HealthCheckRequest{})
	requireUnauthenticated(t, reached, err)
}

// fakeServerStream feeds one client message to the interceptor's wrapper,
// standing in for the SyncMessages request an operator opens with.
type fakeServerStream struct {
	grpc.ServerStream
	ctx      context.Context
	incoming *avsproto.SyncMessagesReq
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

func (f *fakeServerStream) RecvMsg(m any) error {
	req, ok := m.(*avsproto.SyncMessagesReq)
	if !ok {
		return fmt.Errorf("unexpected message type %T", m)
	}
	// Field-by-field rather than a struct copy: protobuf messages carry
	// an internal mutex that must not be copied.
	req.Address = f.incoming.Address
	return nil
}

// callSyncMessages drives the stream interceptor the way the generated
// handler does: wrap the stream, then receive the first message.
func callSyncMessages(t *testing.T, server *RpcServer, ctx context.Context, address string) (bool, error) {
	t.Helper()
	stream := &fakeServerStream{ctx: ctx, incoming: &avsproto.SyncMessagesReq{Address: address}}

	reached := false
	handler := func(_ any, wrapped grpc.ServerStream) error {
		// The generated _Node_SyncMessages_Handler does exactly this
		// before invoking the service method.
		if err := wrapped.RecvMsg(new(avsproto.SyncMessagesReq)); err != nil {
			return err
		}
		reached = true
		return nil
	}

	err := server.operatorStreamInterceptor()(nil, stream,
		&grpc.StreamServerInfo{FullMethod: avsproto.Node_SyncMessages_FullMethodName}, handler)
	return reached, err
}

// SyncMessages streams every active task's metadata. Unauthenticated, it
// let anyone claiming an approved operator address harvest the whole
// platform's task list.
func TestSyncMessagesRejectsUnauthenticatedStream(t *testing.T) {
	server := newAuthTestServer()
	_, address := newOperatorKey(t)

	reached, err := callSyncMessages(t, server, context.Background(), address)
	requireUnauthenticated(t, reached, err)
}

// Claiming someone else's operator address on a stream is refused too.
func TestSyncMessagesRejectsSpoofedAddress(t *testing.T) {
	server := newAuthTestServer()
	attackerKey, _ := newOperatorKey(t)
	_, victimAddress := newOperatorKey(t)

	ctx := operatorCredentials(t, attackerKey, time.Now().Unix())
	reached, err := callSyncMessages(t, server, ctx, victimAddress)
	requireUnauthenticated(t, reached, err)
}

func TestSyncMessagesAcceptsSignedOperator(t *testing.T) {
	server := newAuthTestServer()
	key, address := newOperatorKey(t)

	ctx := operatorCredentials(t, key, time.Now().Unix())
	reached, err := callSyncMessages(t, server, ctx, address)
	if err != nil {
		t.Fatalf("signed operator stream was refused: %v", err)
	}
	if !reached {
		t.Fatal("stream handler did not run for a signed operator")
	}
}

// withCachedAlias builds a resolver whose mapping is already cached, so
// the test never needs a chain connection. A nil contract is only
// consulted on a cache miss.
func withCachedAlias(operator, alias string) *operatorAliasResolver {
	resolver := newOperatorAliasResolver(nil, testutil.GetLogger())
	resolver.cache[common.HexToAddress(operator)] = aliasEntry{
		alias:     common.HexToAddress(alias),
		fetchedAt: time.Now(),
	}
	return resolver
}

// Two of the three operators approved on mainnet run with an alias key:
// the registered address stays cold and a separate hot key signs. That
// signature must be accepted for the operator it was declared against.
func TestAliasKeyOperatorIsAccepted(t *testing.T) {
	server := newAuthTestServer()
	aliasKey, aliasAddress := newOperatorKey(t)
	_, operatorAddress := newOperatorKey(t)
	server.aliasResolver = withCachedAlias(operatorAddress, aliasAddress)

	// ClientAuth signs the message naming the operator, using the alias key.
	ctx := operatorCredentialsFor(t, aliasKey, operatorAddress, time.Now().Unix())

	reached, err := callUnary(t, server, ctx,
		avsproto.Node_NotifyTriggers_FullMethodName,
		&avsproto.NotifyTriggersReq{Address: operatorAddress})
	if err != nil {
		t.Fatalf("alias-key operator was refused: %v", err)
	}
	if !reached {
		t.Fatal("handler did not run for an alias-key operator")
	}
}

// A key that is neither the operator's nor its declared alias is refused.
func TestUndeclaredKeyRejectedForAliasOperator(t *testing.T) {
	server := newAuthTestServer()
	_, declaredAlias := newOperatorKey(t)
	_, operatorAddress := newOperatorKey(t)
	server.aliasResolver = withCachedAlias(operatorAddress, declaredAlias)

	attackerKey, _ := newOperatorKey(t)
	ctx := operatorCredentialsFor(t, attackerKey, operatorAddress, time.Now().Unix())

	reached, err := callUnary(t, server, ctx,
		avsproto.Node_NotifyTriggers_FullMethodName,
		&avsproto.NotifyTriggersReq{Address: operatorAddress})
	requireUnauthenticated(t, reached, err)
}

// An operator that declared no alias must sign with its own key.
func TestOperatorWithoutAliasMustSignWithOwnKey(t *testing.T) {
	server := newAuthTestServer()
	_, operatorAddress := newOperatorKey(t)
	server.aliasResolver = withCachedAlias(operatorAddress, "0x0000000000000000000000000000000000000000")

	otherKey, _ := newOperatorKey(t)
	ctx := operatorCredentialsFor(t, otherKey, operatorAddress, time.Now().Unix())

	reached, err := callUnary(t, server, ctx,
		avsproto.Node_Ping_FullMethodName, &avsproto.Checkin{Address: operatorAddress})
	requireUnauthenticated(t, reached, err)
}
