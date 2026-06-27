package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// TestNodeRoundTrip walks each Node variant through
// OpenAPI → proto → OpenAPI and asserts the config survives. Covers the
// simple field-mapped variants; Loop is exercised separately so the
// nested-runner recursion gets its own assertions.
func TestNodeRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T) generated.Node
		check func(t *testing.T, out generated.Node)
	}{
		{
			name: "customCode",
			build: func(t *testing.T) generated.Node {
				typ := generated.CustomCode
				inner := generated.CustomCodeNode{Type: &typ, Config: &generated.CustomCodeNodeConfig{
					Lang:   generated.Lang("javascript"),
					Source: "return {ok: true};",
				}}
				n := generated.Node{Id: "cc1", Type: generated.NodeTypeCustomCode}
				require.NoError(t, n.FromCustomCodeNode(inner))
				return n
			},
			check: func(t *testing.T, out generated.Node) {
				v, err := out.AsCustomCodeNode()
				require.NoError(t, err)
				assert.Equal(t, "return {ok: true};", v.Config.Source)
				assert.Equal(t, generated.Lang("javascript"), v.Config.Lang)
			},
		},
		{
			name: "ethTransfer",
			build: func(t *testing.T) generated.Node {
				typ := generated.EthTransfer
				inner := generated.ETHTransferNode{Type: &typ, Config: &generated.ETHTransferNodeConfig{
					Destination: generated.EthereumAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"),
					Amount:      "1000000000000000",
				}}
				n := generated.Node{Id: "tx1", Type: generated.NodeTypeEthTransfer}
				require.NoError(t, n.FromETHTransferNode(inner))
				return n
			},
			check: func(t *testing.T, out generated.Node) {
				v, err := out.AsETHTransferNode()
				require.NoError(t, err)
				assert.Equal(t, "1000000000000000", v.Config.Amount)
				assert.Equal(t, "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd", string(v.Config.Destination))
			},
		},
		{
			name: "restApi",
			build: func(t *testing.T) generated.Node {
				typ := generated.RestApi
				inner := generated.RestAPINode{Type: &typ, Config: &generated.RestAPINodeConfig{
					Method: generated.RestAPINodeConfigMethod("POST"),
					Url:    "https://example.com/hook",
				}}
				n := generated.Node{Id: "http1", Type: generated.NodeTypeRestApi}
				require.NoError(t, n.FromRestAPINode(inner))
				return n
			},
			check: func(t *testing.T, out generated.Node) {
				v, err := out.AsRestAPINode()
				require.NoError(t, err)
				assert.Equal(t, "https://example.com/hook", v.Config.Url)
				assert.Equal(t, "POST", string(v.Config.Method))
			},
		},
		{
			name: "filter",
			build: func(t *testing.T) generated.Node {
				typ := generated.Filter
				inner := generated.FilterNode{Type: &typ, Config: &generated.FilterNodeConfig{
					Expression:    "value.amount > 0",
					InputVariable: "{{custom_code1.data}}",
				}}
				n := generated.Node{Id: "f1", Type: generated.NodeTypeFilter}
				require.NoError(t, n.FromFilterNode(inner))
				return n
			},
			check: func(t *testing.T, out generated.Node) {
				v, err := out.AsFilterNode()
				require.NoError(t, err)
				assert.Equal(t, "value.amount > 0", v.Config.Expression)
				assert.Equal(t, "{{custom_code1.data}}", v.Config.InputVariable)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			start := tc.build(t)
			pn, err := OpenAPIToProtoNode(start)
			require.NoError(t, err)
			rt, err := ProtoToOpenAPINode(pn)
			require.NoError(t, err)
			assert.Equal(t, start.Id, rt.Id)
			tc.check(t, rt)
		})
	}
}

// TestNodeRoundTrip_PerNodeChainId proves the persisted create() path decodes a
// per-node chain_id (int64) for every chain-aware node — OpenAPIToProtoNode is
// the same mapper CreateWorkflow runs, and it carries chainId via the protojson
// round-trip (jsonRetargetProto). This is the SDK's "create()-path int64" check,
// verifiable here without a deploy.
func TestNodeRoundTrip_PerNodeChainId(t *testing.T) {
	const chainID = int64(11155111)

	t.Run("contractWrite", func(t *testing.T) {
		typ := generated.ContractWrite
		inner := generated.ContractWriteNode{Type: &typ, Config: &generated.ContractWriteNodeConfig{
			ContractAddress: generated.EthereumAddress("0x1234567890123456789012345678901234567890"),
			ChainId:         generated.ChainId(chainID),
		}}
		n := generated.Node{Id: "cw1", Type: generated.NodeTypeContractWrite}
		require.NoError(t, n.FromContractWriteNode(inner))
		pn, err := OpenAPIToProtoNode(n)
		require.NoError(t, err)
		assert.Equal(t, chainID, pn.GetContractWrite().GetConfig().GetChainId())
	})

	t.Run("contractRead", func(t *testing.T) {
		typ := generated.ContractRead
		inner := generated.ContractReadNode{Type: &typ, Config: &generated.ContractReadNodeConfig{
			ContractAddress: generated.EthereumAddress("0x1234567890123456789012345678901234567890"),
			ChainId:         generated.ChainId(chainID),
		}}
		n := generated.Node{Id: "cr1", Type: generated.NodeTypeContractRead}
		require.NoError(t, n.FromContractReadNode(inner))
		pn, err := OpenAPIToProtoNode(n)
		require.NoError(t, err)
		assert.Equal(t, chainID, pn.GetContractRead().GetConfig().GetChainId())
	})

	t.Run("ethTransfer", func(t *testing.T) {
		typ := generated.EthTransfer
		inner := generated.ETHTransferNode{Type: &typ, Config: &generated.ETHTransferNodeConfig{
			Destination: generated.EthereumAddress("0x1234567890123456789012345678901234567890"),
			Amount:      "1",
			ChainId:     generated.ChainId(chainID),
		}}
		n := generated.Node{Id: "et1", Type: generated.NodeTypeEthTransfer}
		require.NoError(t, n.FromETHTransferNode(inner))
		pn, err := OpenAPIToProtoNode(n)
		require.NoError(t, err)
		assert.Equal(t, chainID, pn.GetEthTransfer().GetConfig().GetChainId())
	})
}

// TestNodeRoundTrip_Await proves the Await node survives the create→render REST
// round-trip (OpenAPI → proto → OpenAPI), so the SDK/studio can put it in a workflow.
func TestNodeRoundTrip_Await(t *testing.T) {
	typ := generated.AwaitNodeTypeAwait
	approvers := []string{"0xowner"}
	prompt := "approve the transfer?"
	timeout := int64(3600)
	inner := generated.AwaitNode{Type: &typ, Config: &generated.AwaitNodeConfig{
		Channel:        "telegram",
		Approvers:      &approvers,
		Prompt:         &prompt,
		TimeoutSeconds: &timeout,
	}}
	n := generated.Node{Id: "appr", Type: generated.NodeTypeAwait}
	require.NoError(t, n.FromAwaitNode(inner))

	// Inbound (CreateWorkflow path).
	pn, err := OpenAPIToProtoNode(n)
	require.NoError(t, err)
	cfg := pn.GetAwait().GetConfig()
	require.NotNil(t, cfg)
	assert.Equal(t, "telegram", cfg.GetChannel())
	assert.Equal(t, []string{"0xowner"}, cfg.GetApprovers())
	assert.Equal(t, "approve the transfer?", cfg.GetPrompt())
	assert.Equal(t, uint32(3600), cfg.GetTimeoutSeconds())

	// Outbound (GET/list render).
	rt, err := ProtoToOpenAPINode(pn)
	require.NoError(t, err)
	v, err := rt.AsAwaitNode()
	require.NoError(t, err)
	assert.Equal(t, "telegram", v.Config.Channel)
	require.NotNil(t, v.Config.TimeoutSeconds)
	assert.Equal(t, int64(3600), *v.Config.TimeoutSeconds)
}

func TestNodeRoundTrip_LoopWithCustomCodeRunner(t *testing.T) {
	innerTyp := generated.CustomCode
	runnerNode := generated.Node{Id: "inner", Type: generated.NodeTypeCustomCode}
	require.NoError(t, runnerNode.FromCustomCodeNode(generated.CustomCodeNode{
		Type:   &innerTyp,
		Config: &generated.CustomCodeNodeConfig{Lang: generated.Lang("javascript"), Source: "return value;"},
	}))

	loopTyp := generated.Loop
	loop := generated.LoopNode{
		Type: &loopTyp,
		Config: &generated.LoopNodeConfig{
			InputVariable: "{{settings.addressList}}",
			Runner:        runnerNode,
		},
	}
	n := generated.Node{Id: "loop1", Type: generated.NodeTypeLoop}
	require.NoError(t, n.FromLoopNode(loop))

	pn, err := OpenAPIToProtoNode(n)
	require.NoError(t, err)

	rt, err := ProtoToOpenAPINode(pn)
	require.NoError(t, err)
	roundLoop, err := rt.AsLoopNode()
	require.NoError(t, err)
	assert.Equal(t, "{{settings.addressList}}", roundLoop.Config.InputVariable)

	runnerRoundTrip, err := roundLoop.Config.Runner.AsCustomCodeNode()
	require.NoError(t, err)
	assert.Equal(t, "return value;", runnerRoundTrip.Config.Source)
}
