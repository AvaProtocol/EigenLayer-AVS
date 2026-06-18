package config

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPaymasterCaller implements paymasterCaller for probe unit tests and
// records what it was asked to call.
type mockPaymasterCaller struct {
	result      []byte
	err         error
	gotTo       *common.Address
	gotData     []byte
	hadDeadline bool
}

func (m *mockPaymasterCaller) CallContract(ctx context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	_, m.hadDeadline = ctx.Deadline()
	m.gotTo = msg.To
	m.gotData = msg.Data
	return m.result, m.err
}

// abiEncodedAddress returns the 32-byte ABI encoding of a single address
// (right-aligned in a 32-byte word), i.e. what a view getter would return.
func abiEncodedAddress(addr common.Address) []byte {
	out := make([]byte, 32)
	copy(out[12:], addr.Bytes())
	return out
}

func TestFetchPaymasterVerifyingSigner_HappyPath(t *testing.T) {
	want := common.HexToAddress("0x1111111111111111111111111111111111111111")
	paymaster := common.HexToAddress("0x2222222222222222222222222222222222222222")
	mock := &mockPaymasterCaller{result: abiEncodedAddress(want)}

	got, err := fetchPaymasterVerifyingSigner(mock, paymaster)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// Probe must target the paymaster and run under a bounded context so an
	// unresponsive RPC fails the boot fast instead of hanging.
	require.NotNil(t, mock.gotTo)
	assert.Equal(t, paymaster, *mock.gotTo)
	assert.True(t, mock.hadDeadline, "startup probe must use a deadline-bounded context")
}

func TestFetchPaymasterOwner_HappyPath(t *testing.T) {
	want := common.HexToAddress("0x3333333333333333333333333333333333333333")
	mock := &mockPaymasterCaller{result: abiEncodedAddress(want)}

	got, err := fetchPaymasterOwner(mock, common.HexToAddress("0x1"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestFetchPaymasterVerifyingSigner_RPCError(t *testing.T) {
	mock := &mockPaymasterCaller{err: errors.New("dial tcp: connection refused")}

	_, err := fetchPaymasterVerifyingSigner(mock, common.HexToAddress("0x1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verifyingSigner", "error should name the failed getter")
	assert.Contains(t, err.Error(), "connection refused", "underlying RPC error should be wrapped")
}

func TestFetchPaymasterVerifyingSigner_EmptyResult(t *testing.T) {
	// No contract code at the address → empty return data → unpack fails.
	mock := &mockPaymasterCaller{result: []byte{}}

	_, err := fetchPaymasterVerifyingSigner(mock, common.HexToAddress("0x1"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unpack")
}

func TestPaymasterProbe_DistinctSelectors(t *testing.T) {
	// owner() and verifyingSigner() must pack different 4-byte selectors,
	// otherwise the two probes would be calling the same function.
	ownerMock := &mockPaymasterCaller{result: abiEncodedAddress(common.Address{})}
	signerMock := &mockPaymasterCaller{result: abiEncodedAddress(common.Address{})}

	_, err := fetchPaymasterOwner(ownerMock, common.HexToAddress("0x1"))
	require.NoError(t, err)
	_, err = fetchPaymasterVerifyingSigner(signerMock, common.HexToAddress("0x1"))
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(ownerMock.gotData), 4)
	require.GreaterOrEqual(t, len(signerMock.gotData), 4)
	assert.NotEqual(t, ownerMock.gotData[:4], signerMock.gotData[:4],
		"owner() and verifyingSigner() must use different function selectors")
}
