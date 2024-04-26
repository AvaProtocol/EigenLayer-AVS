package aggregator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/rpc"

	cstaskmanager "github.com/OAK-Foundation/oak-avs/contracts/bindings/AutomationTaskManager"
	"github.com/OAK-Foundation/oak-avs/core"

	"github.com/Layr-Labs/eigensdk-go/crypto/bls"
	sdktypes "github.com/Layr-Labs/eigensdk-go/types"
)

var (
	TaskNotFoundError400                     = errors.New("400. Task not found")
	OperatorNotPartOfTaskQuorum400           = errors.New("400. Operator not part of quorum")
	TaskResponseDigestNotFoundError500       = errors.New("500. Failed to get task response digest")
	UnknownErrorWhileVerifyingSignature400   = errors.New("400. Failed to verify signature")
	SignatureVerificationFailed400           = errors.New("400. Signature verification failed")
	CallToGetCheckSignaturesIndicesFailed500 = errors.New("500. Failed to get check signatures indices")
)

// Request represents a JSON-RPC request
type Request struct {
	Method string      `json:"method"`
	Params interface{} `json:"params"`
	ID     int         `json:"id"`
}

// Response represents a JSON-RPC response
type Response struct {
	ID     int         `json:"id"`
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// CommandHandler is a function type for handling commands
type CommandHandler func(params interface{}) (interface{}, error)

// CommandMap maps command names to their respective handlers
var CommandMap = map[string]CommandHandler{
	"TaskSubmission": TaskSubmissionHandler,
	"TaskTriggering": TaskTriggeringHandler,
	"TaskExecuted":   TaskExecutedHandler,
}

// TaskSubmissionHandler handles TaskSubmission command
func TaskSubmissionHandler(params interface{}) (interface{}, error) {
	// Process TaskSubmission command here
	return "TaskSubmission processed", nil
}

// TaskTriggeringHandler handles TaskTriggering command
func TaskTriggeringHandler(params interface{}) (interface{}, error) {
	// Process TaskTriggering command here
	return "TaskTriggering processed", nil
}

// TaskExecutedHandler handles TaskExecuted command
func TaskExecutedHandler(params interface{}) (interface{}, error) {
	// Process TaskExecuted command here
	return "TaskExecuted processed", nil
}

func jsonError(code int, message string) error {
	return &Response{
		Error:  message,
		ID:     0,
		Result: nil,
	}
}

// RPCServer handles JSON-RPC requests
func RPCServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req Request
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	handler, ok := CommandMap[req.Method]
	if !ok {
		err := jsonError(-32601, "Method not found: "+req.Method)
		json.NewEncoder(w).Encode(err)
		return
	}

	result, err := handler(req.Params)
	if err != nil {
		json.NewEncoder(w).Encode(jsonError(-32603, err.Error()))
		return
	}

	resp := Response{
		ID:     req.ID,
		Result: result,
		Error:  "",
	}

	json.NewEncoder(w).Encode(resp)
}

func (agg *Aggregator) startServer(ctx context.Context) error {

	err := rpc.Register(agg)
	if err != nil {
		agg.logger.Fatal("Format of service TaskManager isn't correct. ", "err", err)
	}
	rpc.HandleHTTP()
	err = http.ListenAndServe(agg.serverIpPortAddr, nil)
	if err != nil {
		agg.logger.Fatal("ListenAndServe", "err", err)
	}

	return nil
}

type SignedTaskResponse struct {
	TaskResponse cstaskmanager.IIncredibleSquaringTaskManagerTaskResponse
	BlsSignature bls.Signature
	OperatorId   bls.OperatorId
}

// rpc endpoint which is called by operator
// reply doesn't need to be checked. If there are no errors, the task response is accepted
// rpc framework forces a reply type to exist, so we put bool as a placeholder
func (agg *Aggregator) ProcessSignedTaskResponse(signedTaskResponse *SignedTaskResponse, reply *bool) error {
	agg.logger.Infof("Received signed task response: %#v", signedTaskResponse)
	taskIndex := signedTaskResponse.TaskResponse.ReferenceTaskIndex
	taskResponseDigest, err := core.GetTaskResponseDigest(&signedTaskResponse.TaskResponse)
	if err != nil {
		agg.logger.Error("Failed to get task response digest", "err", err)
		return TaskResponseDigestNotFoundError500
	}
	agg.taskResponsesMu.Lock()
	if _, ok := agg.taskResponses[taskIndex]; !ok {
		agg.taskResponses[taskIndex] = make(map[sdktypes.TaskResponseDigest]cstaskmanager.IIncredibleSquaringTaskManagerTaskResponse)
	}
	if _, ok := agg.taskResponses[taskIndex][taskResponseDigest]; !ok {
		agg.taskResponses[taskIndex][taskResponseDigest] = signedTaskResponse.TaskResponse
	}
	agg.taskResponsesMu.Unlock()

	err = agg.blsAggregationService.ProcessNewSignature(
		context.Background(), taskIndex, taskResponseDigest,
		&signedTaskResponse.BlsSignature, signedTaskResponse.OperatorId,
	)
	return err
}
