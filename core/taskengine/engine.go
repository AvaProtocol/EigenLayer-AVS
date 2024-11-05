// The core package that manage and distribute and execute task
package taskengine

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"sync"
	"time"

	"github.com/AvaProtocol/ap-avs/core/apqueue"
	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/model"
	"github.com/AvaProtocol/ap-avs/storage"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/ethclient"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

var (
	rpcConn *ethclient.Client
	// websocket client used for subscription
	wsEthClient *ethclient.Client
	wsRpcURL    string
	logger      sdklogging.Logger
)

func SetLogger(mylogger sdklogging.Logger) {
	logger = mylogger
}

type operatorState struct {
	// list of task id that we had synced to this operator
	TaskID         map[string]bool
	MonotonicClock int64
}

type Engine struct {
	db    storage.Storage
	queue *apqueue.Queue

	// maintain a list of active job that we have to synced to operators
	// only task triggers are sent to operator
	tasks            map[string]*model.Task
	lock             *sync.Mutex
	trackSyncedTasks map[string]*operatorState

	// when shutdown is true, our engine will perform the shutdown
	// pending execution will be pushed out before the shutdown completely
	// to force shutdown, one can type ctrl+c twice
	shutdown bool

	// seq is a monotonic number to keep track our task id
	seq storage.Sequence

	logger sdklogging.Logger
}

func SetRpc(rpcURL string) {
	if conn, err := ethclient.Dial(rpcURL); err == nil {
		rpcConn = conn
	} else {
		panic(err)
	}
}

func SetWsRpc(rpcURL string) {
	wsRpcURL = rpcURL
	if err := retryWsRpc(); err != nil {
		panic(err)
	}
}

func retryWsRpc() error {
	conn, err := ethclient.Dial(wsRpcURL)
	if err == nil {
		wsEthClient = conn
		return nil
	}

	return err
}

func New(db storage.Storage, config *config.Config, queue *apqueue.Queue, logger sdklogging.Logger) *Engine {
	e := Engine{
		db:    db,
		queue: queue,

		lock:             &sync.Mutex{},
		tasks:            make(map[string]*model.Task),
		trackSyncedTasks: make(map[string]*operatorState),
		shutdown:         false,

		logger: logger,
	}

	SetRpc(config.SmartWallet.EthRpcUrl)
	//SetWsRpc(config.SmartWallet.EthWsUrl)

	return &e
}

func (n *Engine) Stop() {
	n.seq.Release()
	n.shutdown = true
}

func (n *Engine) Start() {
	var err error
	n.seq, err = n.db.GetSequence([]byte("t:seq"), 1000)
	if err != nil {
		panic(err)
	}

	// TODO: FIX

	taskPrefix1 := fmt.Sprintf("t:%s:", TaskStatusToStorageKey(avsproto.TaskStatus_Active))
	taskPrefix2 := fmt.Sprintf("t:%s:", TaskStatusToStorageKey(avsproto.TaskStatus_Executing))

	for _, taskPrefix := range []string{taskPrefix1, taskPrefix2} {
		n.logger.Info("engine discovery", "task_prefix", taskPrefix)
		kvs, e := n.db.GetByPrefix([]byte(taskPrefix))
		if e != nil {
			panic(e)
		}
		for _, item := range kvs {
			var task model.Task
			if err := json.Unmarshal(item.Value, &task); err == nil {
				n.tasks[string(item.Key)] = &task
			}
		}
	}
	n.logger.Info("task engine booted with  tasks", "task", n.tasks)

}

func (n *Engine) CreateTask(user *model.User, taskPayload *avsproto.CreateTaskReq) (*model.Task, error) {
	var err error
	salt := big.NewInt(0)

	user.SmartAccountAddress, err = aa.GetSenderAddress(rpcConn, user.Address, salt)

	if err != nil {
		return nil, err
	}

	taskID, err := n.NewTaskID()
	if err != nil {
		return nil, fmt.Errorf("cannot create task right now. storage unavailable")
	}

	task, err := model.NewTaskFromProtobuf(taskID, user, taskPayload)

	if err != nil {
		return nil, err
	}

	updates := map[string][]byte{}

	updates[TaskStorageKey(task.ID, task.Status)], err = task.ToJSON()
	updates[TaskUserKey(task)] = []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Active))

	if err = n.db.BatchWrite(updates); err != nil {
		return nil, err
	}

	n.lock.Lock()
	defer n.lock.Unlock()
	n.tasks[task.ID] = task

	return task, nil
}

func (n *Engine) StreamCheckToOperator(payload *avsproto.SyncTasksReq, srv avsproto.Aggregator_SyncTasksServer) error {
	ticker := time.NewTicker(5 * time.Second)
	address := payload.Address

	n.logger.Info("open channel to stream check to operator", "operator", address)
	if _, ok := n.trackSyncedTasks[address]; !ok {
		n.lock.Lock()
		n.trackSyncedTasks[address] = &operatorState{
			MonotonicClock: payload.MonotonicClock,
			TaskID:         map[string]bool{},
		}
		n.lock.Unlock()
	} else {
		// The operator has restated, but we haven't clean it state yet, reset now
		if payload.MonotonicClock > n.trackSyncedTasks[address].MonotonicClock {
			n.trackSyncedTasks[address].TaskID = map[string]bool{}
			n.trackSyncedTasks[address].MonotonicClock = payload.MonotonicClock
		}
	}

	// Reset the state if the operator disconnect
	defer func() {
		n.logger.Info("operator disconnect, cleanup state", "operator", address)
		n.trackSyncedTasks[address].TaskID = map[string]bool{}
		n.trackSyncedTasks[address].MonotonicClock = 0
	}()

	for {
		select {
		case <-ticker.C:
			if n.shutdown {
				return nil
			}

			if n.tasks == nil {
				continue
			}

			for _, task := range n.tasks {
				if _, ok := n.trackSyncedTasks[address].TaskID[task.ID]; ok {
					continue
				}

				n.logger.Info("stream check to operator", "taskID", task.ID, "operator", payload.Address)
				resp := avsproto.SyncTasksResp{
					Id:        task.ID,
					CheckType: "CheckTrigger",
					Trigger:   task.Trigger,
				}

				if err := srv.Send(&resp); err != nil {
					return err
				}

				n.lock.Lock()
				n.trackSyncedTasks[address].TaskID[task.ID] = true
				n.lock.Unlock()
			}
		}
	}
}

// TODO: Merge and verify from multiple operators
func (n *Engine) AggregateChecksResult(address string, ids []string) error {
	if len(ids) < 1 {
		return nil
	}

	n.logger.Debug("process aggregator check hits", "operator", address, "task_ids", ids)
	for _, id := range ids {
		n.lock.Lock()
		delete(n.tasks, id)
		delete(n.trackSyncedTasks[address].TaskID, id)
		n.logger.Info("processed aggregator check hit", "operator", address, "id", id)
		n.lock.Unlock()
	}

	// Now we will queue the job
	for _, id := range ids {
		n.logger.Debug("mark task in executing status", "task_id", id)

		if err := n.db.Move(
			[]byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(avsproto.TaskStatus_Active), id)),
			[]byte(fmt.Sprintf("t:%s:%s", TaskStatusToStorageKey(avsproto.TaskStatus_Executing), id)),
		); err != nil {
			n.logger.Error("error moving the task storage from active to executing", "task", id, "error", err)
		}

		n.queue.Enqueue("contract_run", id, []byte(id))
		n.logger.Info("enqueue contract_run job", "taskid", id)
	}

	return nil
}

func (n *Engine) ListTasksByUser(user *model.User) ([]*avsproto.ListTasksResp_TaskItemResp, error) {
	taskIDs, err := n.db.GetByPrefix([]byte(fmt.Sprintf("u:%s", user.Address.String())))

	if err != nil {
		return nil, err
	}

	tasks := make([]*avsproto.ListTasksResp_TaskItemResp, len(taskIDs))
	for i, kv := range taskIDs {

		status, _ := strconv.Atoi(string(kv.Value))
		tasks[i] = &avsproto.ListTasksResp_TaskItemResp{
			Id:     string(model.TaskKeyToId(kv.Key[2:])),
			Status: avsproto.TaskStatus(status),
		}
	}

	return tasks, nil
}

func (n *Engine) GetTaskByUser(user *model.User, taskID string) (*model.Task, error) {
	task := &model.Task{
		ID:    taskID,
		Owner: user.Address.Hex(),
	}

	// Get Task Status
	rawStatus, err := n.db.GetKey([]byte(TaskUserKey(task)))
	status, _ := strconv.Atoi(string(rawStatus))

	taskRawByte, err := n.db.GetKey([]byte(
		TaskStorageKey(taskID, avsproto.TaskStatus(status)),
	))

	if err != nil {
		taskRawByte, err = n.db.GetKey([]byte(
			TaskStorageKey(taskID, avsproto.TaskStatus_Executing),
		))
		if err != nil {
			return nil, err
		}
	}

	err = task.FromStorageData(taskRawByte)
	return task, err
}

func (n *Engine) DeleteTaskByUser(user *model.User, taskID string) (bool, error) {
	task, err := n.GetTaskByUser(user, taskID)

	if err != nil {
		return false, err
	}

	if task.Status == avsproto.TaskStatus_Executing {
		return false, fmt.Errorf("Only non executing task can be deleted")
	}

	n.db.Delete([]byte(TaskStorageKey(task.ID, task.Status)))
	n.db.Delete([]byte(TaskUserKey(task)))

	return true, nil
}

func (n *Engine) CancelTaskByUser(user *model.User, taskID string) (bool, error) {
	task, err := n.GetTaskByUser(user, taskID)

	if err != nil {
		return false, err
	}

	if task.Status != avsproto.TaskStatus_Active {
		return false, fmt.Errorf("Only active task can be cancelled")
	}

	updates := map[string][]byte{}
	oldStatus := task.Status
	task.SetCanceled()
	updates[TaskStorageKey(task.ID, oldStatus)], err = task.ToJSON()
	updates[TaskUserKey(task)] = []byte(fmt.Sprintf("%d", task.Status))

	if err = n.db.BatchWrite(updates); err == nil {
		n.db.Move(
			[]byte(TaskStorageKey(task.ID, oldStatus)),
			[]byte(TaskStorageKey(task.ID, task.Status)),
		)

		delete(n.tasks, task.ID)
	} else {
		// TODO Gracefully handling of storage cleanup
	}

	return true, nil
}

func TaskStorageKey(id string, status avsproto.TaskStatus) string {
	return fmt.Sprintf(
		"t:%s:%s",
		TaskStatusToStorageKey(status),
		id,
	)
}

func TaskUserKey(t *model.Task) string {
	return fmt.Sprintf(
		"u:%s",
		t.Key(),
	)
}

func TaskStatusToStorageKey(v avsproto.TaskStatus) string {
	switch v {
	case 1:
		return "c"
	case 2:
		return "f"
	case 3:
		return "l"
	case 4:
		return "x"
	}

	return "a"
}

func (n *Engine) NewTaskID() (string, error) {
	num := uint64(0)
	var err error

	defer func() {
		r := recover()
		if r != nil {
			// recover from panic and send err instead
			err = r.(error)
		}
	}()

	num, err = n.seq.Next()
	if num == 0 {
		num, err = n.seq.Next()
	}

	if err != nil {
		return "", err
	}
	return strconv.FormatInt(int64(num), 10), nil
}
