// The core package that manage and distribute and execute task
package taskengine

import (
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"sync"
	"time"

	"github.com/AvaProtocol/ap-avs/core/apqueue"
	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/AvaProtocol/ap-avs/model"
	"github.com/AvaProtocol/ap-avs/storage"
	"github.com/ethereum/go-ethereum/ethclient"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

var (
	rpcConn *ethclient.Client
)

type Engine struct {
	db    storage.Storage
	queue *apqueue.Queue

	// maintain a list of active job to sync to operator
	tasks     map[string]*model.Task
	lock      sync.Mutex
	sentTasks map[string]bool
}

func SetRpc(rpcURL string) {
	if conn, err := ethclient.Dial(rpcURL); err == nil {
		rpcConn = conn
	}
}

func New(db storage.Storage, config *config.Config, queue *apqueue.Queue) *Engine {
	e := Engine{
		db:    db,
		queue: queue,

		lock:      sync.Mutex{},
		tasks:     make(map[string]*model.Task),
		sentTasks: make(map[string]bool),
	}

	SetRpc(config.EthHttpRpcUrl)

	return &e
}

func (n *Engine) Start() {
	kvs, e := n.db.GetByPrefix([]byte("t:a:"))
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

func (n *Engine) CreateTask(user *model.User, taskPayload *avsproto.CreateTaskReq) (*model.Task, error) {
	var err error
	salt := big.NewInt(0)

	user.SmartAccountAddress, err = aa.GetSenderAddress(rpcConn, user.Address, salt)

	if err != nil {
		return nil, err
	}

	task, err := model.NewTaskFromProtobuf(user, taskPayload)

	if err != nil {
		return nil, err
	}

	updates := map[string][]byte{}

	updates[fmt.Sprintf("t:a:%s", task.ID)], err = task.ToJSON()
	updates[fmt.Sprintf("u:%s", string(task.Key()))] = []byte(model.TaskStatusActive)

	if err = n.db.BatchWrite(updates); err != nil {
		return nil, err
	}

	n.lock.Lock()
	defer n.lock.Unlock()
	n.tasks[task.ID] = task

	return task, nil
}

func (n *Engine) StreamCheckToOperator(payload *avsproto.SyncTasksReq, srv avsproto.Aggregator_SyncTasksServer) error {
	for {
		for _, task := range n.tasks {
			key := fmt.Sprintf("%s:%s", payload.Address, task.ID)

			if _, ok := n.sentTasks[key]; ok {
				continue
			}
			log.Printf("sync task %s to operator %s", task.ID, payload.Address)
			resp := avsproto.SyncTasksResp{
				// TODO: Hook up to the new task channel to syncdicate in realtime
				// Currently this is setup just to generate the metrics so we can
				// prepare the dashboard
				// Our actually task will be more completed
				Id:        task.ID,
				CheckType: "CheckTrigger",
				Trigger:   task.Trigger.ToProtoBuf(),
			}

			if err := srv.Send(&resp); err != nil {
				log.Printf("error when sending task to operator %s: %v", payload.Address, err)
				return err
			}

			n.lock.Lock()
			n.sentTasks[key] = true
			n.lock.Unlock()
		}

		time.Sleep(time.Duration(10) * time.Second)
	}
}

// TODO: Merge and verify from multiple operators
func (n *Engine) AggregateChecksResult(address string, ids []string) error {
	for _, id := range ids {
		n.lock.Lock()
		delete(n.tasks, id)
		delete(n.sentTasks, fmt.Sprintf("%s:%s", address, id))
		n.lock.Unlock()
	}

	// Now we will queue the job
	for _, id := range ids {
		n.queue.Enqueue("contract_run", id, []byte(id))
	}

	return nil
}

func GetTask(db storage.Storage, id string) (*model.Task, error) {
	var task model.Task
	item, err := db.GetKey([]byte("t:a:" + id))

	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(item, &task)
	if err != nil {
		return nil, err
	}

	return &task, nil
}
