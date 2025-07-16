package aggregator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	timestamppb "google.golang.org/protobuf/types/known/timestamppb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

type OperatorNode struct {
	Address       string `json:"address"`
	Name          string `json:"name"`
	RemoteIP      string `json:"remote_ip"`
	LastPingEpoch int64  `json:"last_ping"`
	Version       string `json:"version"`
	MetricsPort   int32  `json:"metrics_port"`
	BlockNumer    int64  `json:"block_number"`
	EventCount    int64  `json:"event_count"`
}

// KnownOperator represents an operator from the JSON file
type KnownOperator struct {
	Address   string  `json:"address"`
	Name      string  `json:"name"`
	EthStaked float64 `json:"ethStaked"`
	Slashable float64 `json:"slashable"`
	Stakers   int     `json:"stakers"`
	AVSs      int     `json:"AVSs"`
}

var (
	operatorNames   = make(map[string]string)
	operatorNamesMu sync.RWMutex
)

// LoadOperatorNames loads operator names from the JSON file
func LoadOperatorNames(jsonData []byte) error {
	var knownOperators []KnownOperator
	if err := json.Unmarshal(jsonData, &knownOperators); err != nil {
		return fmt.Errorf("failed to unmarshal operator names: %w", err)
	}

	operatorNamesMu.Lock()
	defer operatorNamesMu.Unlock()

	for _, op := range knownOperators {
		operatorNames[op.Address] = op.Name
	}

	return nil
}

// GetOperatorName returns the operator name for a given address
func GetOperatorName(address string) string {
	operatorNamesMu.RLock()
	defer operatorNamesMu.RUnlock()

	if name, exists := operatorNames[address]; exists {
		return name
	}
	return ""
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
		Name:          GetOperatorName(payload.Address),
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

	// Use address as key to prevent duplicates from different IDs
	return o.db.Set(append(operatorPrefix, []byte(payload.Address)...), data)
}

func (o *OperatorPool) GetAll() []*OperatorNode {
	var nodes []*OperatorNode
	seenAddresses := make(map[string]bool)

	kvs, err := o.db.GetByPrefix(operatorPrefix)
	if err != nil {
		return nodes
	}

	for _, rawValue := range kvs {
		node := &OperatorNode{}
		if err := json.Unmarshal(rawValue.Value, node); err != nil {
			continue
		}

		// Skip duplicates - only keep the first occurrence of each address
		if seenAddresses[node.Address] {
			continue
		}
		seenAddresses[node.Address] = true

		// Ensure name is populated (for backward compatibility with nodes without names)
		if node.Name == "" {
			node.Name = GetOperatorName(node.Address)
		}

		nodes = append(nodes, node)
	}

	return nodes
}

// CleanupDuplicateOperators removes old duplicate entries from the database
// This helps clean up entries from the old ID-based storage system
func (o *OperatorPool) CleanupDuplicateOperators() error {
	kvs, err := o.db.GetByPrefix(operatorPrefix)
	if err != nil {
		return err
	}

	addressToKeys := make(map[string][][]byte)

	// Group all keys by operator address
	for _, kv := range kvs {
		node := &OperatorNode{}
		if err := json.Unmarshal(kv.Value, node); err != nil {
			continue
		}

		addressToKeys[node.Address] = append(addressToKeys[node.Address], kv.Key)
	}

	// For each address with multiple keys, keep only the address-based key
	for address, keys := range addressToKeys {
		if len(keys) <= 1 {
			continue // No duplicates
		}

		expectedKey := append(operatorPrefix, []byte(address)...)

		// Delete all keys except the expected address-based key
		for _, key := range keys {
			if string(key) != string(expectedKey) {
				o.db.Delete(key)
			}
		}
	}

	return nil
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
