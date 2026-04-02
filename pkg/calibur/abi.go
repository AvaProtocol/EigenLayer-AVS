package calibur

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// executeSelector is the function selector for:
// execute((((address,uint256,bytes)[],bool),uint256,bytes32,address,uint256),bytes)
// This is Calibur's signed batched call execution function.
var executeSelector = crypto.Keccak256([]byte(
	"execute(((address,uint256,bytes)[],bool),uint256,bytes32,address,uint256),bytes)",
))[:4]

// EncodeExecuteCalldata ABI-encodes the calldata for Calibur's
// execute(SignedBatchedCall, bytes) function.
//
// Since the Calibur execute function has deeply nested dynamic types,
// we use manual ABI encoding for correctness.
func EncodeExecuteCalldata(call *SignedBatchedCall, wrappedSignature []byte) ([]byte, error) {
	// We need to manually ABI-encode a complex nested struct.
	// The function signature is:
	//   execute(SignedBatchedCall calldata signedBatchedCall, bytes calldata wrappedSignature)
	//
	// SignedBatchedCall is a struct with:
	//   BatchedCall batchedCall  (contains Call[] calls, bool revertOnFailure)
	//   uint256 nonce
	//   bytes32 keyHash
	//   address executor
	//   uint256 deadline
	//
	// For now, we use the go-ethereum ABI encoder by constructing the encoding manually.
	// This matches how the existing codebase handles complex ABI encoding in aa.go.

	encoded, err := encodeSignedBatchedCallAndSignature(call, wrappedSignature)
	if err != nil {
		return nil, err
	}

	// Prepend the 4-byte function selector
	result := make([]byte, 4+len(encoded))
	copy(result[:4], executeSelector)
	copy(result[4:], encoded)

	return result, nil
}

// encodeSignedBatchedCallAndSignature performs the full ABI encoding of the
// execute(SignedBatchedCall, bytes) parameters.
func encodeSignedBatchedCallAndSignature(call *SignedBatchedCall, sig []byte) ([]byte, error) {
	// Top-level has 2 parameters, both dynamic (struct with dynamic fields, and bytes).
	// ABI layout:
	//   offset_signedBatchedCall (32 bytes)
	//   offset_wrappedSignature (32 bytes)
	//   <signedBatchedCall encoding>
	//   <wrappedSignature encoding>

	// Encode the SignedBatchedCall struct
	structEncoded, err := encodeSignedBatchedCall(call)
	if err != nil {
		return nil, err
	}

	// Encode the wrapped signature as bytes
	sigEncoded := encodeDynamicBytes(sig)

	// Compute offsets (relative to start of params)
	// offset to signedBatchedCall = 64 (2 * 32 bytes for the two offset slots)
	// offset to wrappedSignature = 64 + len(structEncoded)
	offsetStruct := big.NewInt(64)
	offsetSig := big.NewInt(int64(64 + len(structEncoded)))

	var result []byte
	result = append(result, common.LeftPadBytes(offsetStruct.Bytes(), 32)...)
	result = append(result, common.LeftPadBytes(offsetSig.Bytes(), 32)...)
	result = append(result, structEncoded...)
	result = append(result, sigEncoded...)

	return result, nil
}

// encodeSignedBatchedCall ABI-encodes a SignedBatchedCall struct.
func encodeSignedBatchedCall(call *SignedBatchedCall) ([]byte, error) {
	// SignedBatchedCall has fields:
	//   BatchedCall batchedCall  (dynamic - contains Call[] which is dynamic)
	//   uint256 nonce            (static)
	//   bytes32 keyHash          (static)
	//   address executor         (static)
	//   uint256 deadline         (static)
	//
	// Layout:
	//   offset_batchedCall (32 bytes)
	//   nonce (32 bytes)
	//   keyHash (32 bytes)
	//   executor (32 bytes, left-padded address)
	//   deadline (32 bytes)
	//   <batchedCall encoding>

	batchEncoded, err := encodeBatchedCall(&call.BatchedCall)
	if err != nil {
		return nil, err
	}

	// Offset to batchedCall = 5 * 32 = 160 (5 head slots)
	offsetBatch := big.NewInt(160)

	var result []byte
	result = append(result, common.LeftPadBytes(offsetBatch.Bytes(), 32)...)
	result = append(result, common.LeftPadBytes(call.Nonce.Bytes(), 32)...)
	result = append(result, call.KeyHash[:]...)
	result = append(result, common.LeftPadBytes(call.Executor.Bytes(), 32)...)
	result = append(result, common.LeftPadBytes(call.Deadline.Bytes(), 32)...)
	result = append(result, batchEncoded...)

	return result, nil
}

// encodeBatchedCall ABI-encodes a BatchedCall struct.
func encodeBatchedCall(batch *BatchedCall) ([]byte, error) {
	// BatchedCall has:
	//   Call[] calls         (dynamic)
	//   bool revertOnFailure (static)
	//
	// Layout:
	//   offset_calls (32 bytes)
	//   revertOnFailure (32 bytes, bool as uint256)
	//   <calls array encoding>

	callsEncoded, err := encodeCallsArray(batch.Calls)
	if err != nil {
		return nil, err
	}

	// Offset to calls = 2 * 32 = 64 (2 head slots)
	offsetCalls := big.NewInt(64)

	revertFlag := make([]byte, 32)
	if batch.RevertOnFailure {
		revertFlag[31] = 1
	}

	var result []byte
	result = append(result, common.LeftPadBytes(offsetCalls.Bytes(), 32)...)
	result = append(result, revertFlag...)
	result = append(result, callsEncoded...)

	return result, nil
}

// encodeCallsArray ABI-encodes a Call[] array.
func encodeCallsArray(calls []Call) ([]byte, error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("calls array must not be empty")
	}

	// Dynamic array encoding:
	//   length (32 bytes)
	//   offset_call_0 (32 bytes)
	//   offset_call_1 (32 bytes)
	//   ...
	//   <call_0 encoding>
	//   <call_1 encoding>
	//   ...

	// First, encode all calls to know their sizes
	var callEncodings [][]byte
	for i := range calls {
		enc, err := encodeCall(&calls[i])
		if err != nil {
			return nil, fmt.Errorf("failed to encode call %d: %w", i, err)
		}
		callEncodings = append(callEncodings, enc)
	}

	// Compute offsets: each call's offset is relative to the start of the offsets area
	// Base offset = len(calls) * 32 (one offset slot per call)
	baseOffset := len(calls) * 32
	offsets := make([]int, len(calls))
	currentOffset := baseOffset
	for i, enc := range callEncodings {
		offsets[i] = currentOffset
		currentOffset += len(enc)
	}

	// Build the result
	lengthBytes := common.LeftPadBytes(big.NewInt(int64(len(calls))).Bytes(), 32)

	var result []byte
	result = append(result, lengthBytes...)
	for _, off := range offsets {
		result = append(result, common.LeftPadBytes(big.NewInt(int64(off)).Bytes(), 32)...)
	}
	for _, enc := range callEncodings {
		result = append(result, enc...)
	}

	return result, nil
}

// encodeCall ABI-encodes a single Call struct.
func encodeCall(call *Call) ([]byte, error) {
	// Call has:
	//   address to    (static)
	//   uint256 value (static)
	//   bytes data    (dynamic)
	//
	// Layout:
	//   to (32 bytes, left-padded)
	//   value (32 bytes)
	//   offset_data (32 bytes)
	//   <data encoding>

	dataEncoded := encodeDynamicBytes(call.Data)

	// Offset to data = 3 * 32 = 96 (3 head slots)
	offsetData := big.NewInt(96)

	var result []byte
	result = append(result, common.LeftPadBytes(call.To.Bytes(), 32)...)
	result = append(result, common.LeftPadBytes(call.Value.Bytes(), 32)...)
	result = append(result, common.LeftPadBytes(offsetData.Bytes(), 32)...)
	result = append(result, dataEncoded...)

	return result, nil
}

// encodeDynamicBytes ABI-encodes a bytes value (length-prefixed, 32-byte padded).
func encodeDynamicBytes(data []byte) []byte {
	length := big.NewInt(int64(len(data)))
	lengthBytes := common.LeftPadBytes(length.Bytes(), 32)

	// Pad data to 32-byte boundary
	paddedLen := ((len(data) + 31) / 32) * 32
	paddedData := make([]byte, paddedLen)
	copy(paddedData, data)

	var result []byte
	result = append(result, lengthBytes...)
	result = append(result, paddedData...)

	return result
}
