package aggregator

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	timestamppb "google.golang.org/protobuf/types/known/timestamppb"

	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
)

type OperatorStatus struct {
	Address       string `json:"address"`
	LastPingEpoch int64  `json:"last_ping"`
}

var (
	operatorPrefix = []byte("operator:")
)

func (r *RpcServer) Ping(ctx context.Context, payload *avsproto.Checkin) (*avsproto.CheckinResp, error) {
	now := time.Now()

	log.Println("Receive operator %s ping", payload.Id)
	status := &OperatorStatus{
		Address:       payload.Address,
		LastPingEpoch: now.Unix(),
	}

	data, err := json.Marshal(status)

	if err != nil {
		return nil, fmt.Errorf("cannot update operator status due to json encoding")
	}

	if err = r.db.Set(append(operatorPrefix, []byte(payload.Id)...), data); err != nil {
		return nil, fmt.Errorf("cannot update operator status due to json encoding: %w", err)
	}

	return &avsproto.CheckinResp{
		UpdatedAt: timestamppb.Now(),
	}, nil
}
