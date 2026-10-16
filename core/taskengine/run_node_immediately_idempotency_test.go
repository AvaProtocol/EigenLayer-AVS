package taskengine

import (
	"context"
	"encoding/binary"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// customCodeReq builds a single-node RunNodeWithInputs request whose customCode
// node returns the given constant, so distinct sources produce distinct results.
func customCodeReq(source string) *avsproto.RunNodeWithInputsReq {
	return &avsproto.RunNodeWithInputsReq{
		Node: &avsproto.TaskNode{
			Id:   "cc1",
			Name: "cc1",
			Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Lang:   avsproto.Lang_LANG_JAVASCRIPT,
						Source: source,
					},
				},
			},
		},
	}
}

// G5: a retried or double-submitted nodes:run carrying the same Idempotency-Key
// must replay the first result instead of executing (and broadcasting) again.
func TestRunNodeImmediatelyIdempotency(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())
	user := &model.User{Address: common.HexToAddress("0x1111111111111111111111111111111111111111")}
	ctx := context.Background()

	// First call under key K executes node A ("first").
	resp1, err := engine.RunNodeImmediatelyRPCIdempotent(ctx, user, customCodeReq("return 'first'"), "confirm-key-1")
	require.NoError(t, err)
	require.True(t, resp1.GetSuccess(), "customCode node should execute successfully")

	// Same key, a DIFFERENT node ("second"): must return node A's cached result,
	// proving the second execution was short-circuited by idempotency.
	resp2, err := engine.RunNodeImmediatelyRPCIdempotent(ctx, user, customCodeReq("return 'second'"), "confirm-key-1")
	require.NoError(t, err)
	assert.True(t, proto.Equal(resp1, resp2), "same Idempotency-Key must replay the first result, not re-execute")

	// No key: never dedupes — executes node B, giving a result different from A.
	resp3, err := engine.RunNodeImmediatelyRPCIdempotent(ctx, user, customCodeReq("return 'second'"), "")
	require.NoError(t, err)
	assert.False(t, proto.Equal(resp1, resp3), "an empty key must not dedupe")

	// Different key: executes node B (not a replay of key K's result).
	resp4, err := engine.RunNodeImmediatelyRPCIdempotent(ctx, user, customCodeReq("return 'second'"), "confirm-key-2")
	require.NoError(t, err)
	assert.False(t, proto.Equal(resp1, resp4), "a different key must not replay another key's result")

	// Subject scoping: another user with the SAME key must not see the result.
	otherUser := &model.User{Address: common.HexToAddress("0x2222222222222222222222222222222222222222")}
	resp5, err := engine.RunNodeImmediatelyRPCIdempotent(ctx, otherUser, customCodeReq("return 'second'"), "confirm-key-1")
	require.NoError(t, err)
	assert.False(t, proto.Equal(resp1, resp5), "idempotency cache must be scoped per authenticated subject")

	// Zero-address subject (e.g. a partner-assertion simulate caller) must NOT
	// dedupe — otherwise two indistinguishable callers sharing a key would
	// cross-replay. Same key, different nodes → the second still executes.
	zeroUser := &model.User{} // Address is the zero value
	zresp1, err := engine.RunNodeImmediatelyRPCIdempotent(ctx, zeroUser, customCodeReq("return 'first'"), "shared-key")
	require.NoError(t, err)
	zresp2, err := engine.RunNodeImmediatelyRPCIdempotent(ctx, zeroUser, customCodeReq("return 'second'"), "shared-key")
	require.NoError(t, err)
	assert.False(t, proto.Equal(zresp1, zresp2), "a zero-address subject must not dedupe (avoids cross-caller replay)")
}

// The cache key is deterministic and scoped, and entries older than the TTL are
// ignored so a stale key is allowed to re-execute.
func TestIdempotencyCacheKeyAndTTL(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	kA := idempotencyCacheKey("0xA", "k")
	assert.Equal(t, string(kA), string(idempotencyCacheKey("0xA", "k")), "key derivation must be deterministic")
	assert.NotEqual(t, string(kA), string(idempotencyCacheKey("0xB", "k")), "different subject must yield a different key")
	assert.NotEqual(t, string(kA), string(idempotencyCacheKey("0xA", "k2")), "different client key must yield a different key")

	// Fresh entry round-trips.
	resp := &avsproto.RunNodeWithInputsResp{Success: true}
	engine.writeIdempotentResponse(kA, resp)
	got := engine.readIdempotentResponse(kA)
	require.NotNil(t, got)
	assert.True(t, got.GetSuccess())

	// An entry older than the TTL is ignored (a fresh request may re-execute).
	expiredKey := idempotencyCacheKey("0xA", "expired")
	payload, err := proto.Marshal(resp)
	require.NoError(t, err)
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint64(buf[:8], uint64(time.Now().Add(-idempotencyTTL-time.Minute).UnixNano()))
	copy(buf[8:], payload)
	require.NoError(t, db.Set(expiredKey, buf))
	assert.Nil(t, engine.readIdempotentResponse(expiredKey), "entry older than the TTL must be treated as absent")
}
