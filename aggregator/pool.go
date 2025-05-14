package aggregator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	timestamppb "google.golang.org/protobuf/types/known/timestamppb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

type OperatorNode struct {
	Address       string `json:"address"`
	RemoteIP      string `json:"remote_ip"`
	LastPingEpoch int64  `json:"last_ping"`
	Version       string `json:"version"`
	MetricsPort   int32  `json:"metrics_port"`
	BlockNumer    int64  `json:"block_number"`
	EventCount    int64  `json:"event_count"`
}

func (o *OperatorNode) LastSeen() string {
	now := time.Now()

	var last time.Time
	if o.LastPingEpoch > 1e12 { // Threshold for milliseconds (timestamps after 2001)
		last = time.Unix(o.LastPingEpoch/1000, 0)
	} else {
		last = time.Unix(o.LastPingEpoch, 0)
	}

	duration := now.Sub(last)

	days := int(duration.Hours()) / 24
	hours := int(duration.Hours()) % 24
	minutes := int(duration.Minutes()) % 60
	seconds := int(duration.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd%dh ago", days, hours)
	} else if hours > 0 {
		return fmt.Sprintf("%dh%dm ago", hours, minutes)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm%ds ago", minutes, seconds)
	} else {
		return fmt.Sprintf("%ds ago", seconds)
	}
}

func (o *OperatorNode) EtherscanURL() string {
	return fmt.Sprintf("%s/address/%s", config.EtherscanURL(), o.Address)
}

func (o *OperatorNode) EigenlayerURL() string {
	return fmt.Sprintf("%s/operator/%s", config.EigenlayerAppURL(), o.Address)
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
		MetricsPort:   payload.MetricsPort,
		RemoteIP:      payload.RemoteIP,
		Version:       payload.Version,
		BlockNumer:    payload.BlockNumber,
		EventCount:    payload.EventCount,
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
		if err := json.Unmarshal(rawValue.Value, node); err != nil {
			continue
		}

		nodes = append(nodes, node)
	}

	return nodes
}

func (r *RpcServer) Ping(ctx context.Context, payload *avsproto.Checkin) (*avsproto.CheckinResp, error) {
	if ok, err := r.verifyOperator(ctx, payload.Address); !ok {
		return nil, err
	}

	if err := r.operatorPool.Checkin(payload); err != nil {
		return nil, fmt.Errorf("cannot update operator status error: %w", err)
	}

	return &avsproto.CheckinResp{
		UpdatedAt: timestamppb.Now(),
	}, nil
}
