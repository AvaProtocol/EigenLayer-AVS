package aggregator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	timestamppb "google.golang.org/protobuf/types/known/timestamppb"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
)

type OperatorNode struct {
	Address       string `json:"address"`
	LastPingEpoch int64  `json:"last_ping"`
}

func (o *OperatorNode) LastSeen() string {
	now := time.Now()
	last := time.Unix(o.LastPingEpoch, 0)

	duration := now.Sub(last)

	// Extract hours, minutes, and seconds from the duration
	hours := int(duration.Hours())
	minutes := int(duration.Minutes()) % 60
	seconds := int(duration.Seconds()) % 60

	if hours > 0 {
		return fmt.Sprintf("%dh%dm ago", hours, minutes)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm%ds ago", minutes, seconds)
	} else {
		return fmt.Sprintf("%ds ago", seconds)
	}
}

var (
	operatorPrefix = []byte("operator:")
)

type OperatorPool struct {
	db storage.Storage
}

func (o *OperatorPool) Checkin(payload *avsproto.Checkin) error {
	now := time.Now()

	status := &OperatorNode{
		Address:       payload.Address,
		LastPingEpoch: now.Unix(),
	}

	data, err := json.Marshal(status)

	if err != nil {
		return fmt.Errorf("cannot update operator status due to json encoding")
	}

	return o.db.Set(append(operatorPrefix, []byte(payload.Id)...), data)
}

func (o *OperatorPool) GetAll() []*OperatorNode {
	var nodes []*OperatorNode

	kvs, err := o.db.GetByPrefix(operatorPrefix)
	if err != nil {
		return nodes
	}

	for _, rawValue := range kvs {
		node := &OperatorNode{}
		json.Unmarshal(rawValue.Value, node)

		nodes = append(nodes, node)
	}

	return nodes
}

func (r *RpcServer) Ping(ctx context.Context, payload *avsproto.Checkin) (*avsproto.CheckinResp, error) {

	if err := r.operatorPool.Checkin(payload); err != nil {
		return nil, fmt.Errorf("cannot update operator status error: %w", err)
	}

	return &avsproto.CheckinResp{
		UpdatedAt: timestamppb.Now(),
	}, nil
}
