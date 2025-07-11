package metrics

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"

	sdkavsregistry "github.com/Layr-Labs/eigensdk-go/chainio/clients/avsregistry"
	sdkelcontracts "github.com/Layr-Labs/eigensdk-go/chainio/clients/elcontracts"
	"github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/Layr-Labs/eigensdk-go/types"
)

type MetricsOnlyLogger struct {
	logging.Logger
}

func (l *MetricsOnlyLogger) Error(msg string, keysAndValues ...interface{}) {
	l.Logger.Error(fmt.Sprintf("[METRICS ONLY] %s", msg), keysAndValues...)
}

func (l *MetricsOnlyLogger) Errorf(format string, args ...interface{}) {
	l.Logger.Errorf("[METRICS ONLY] "+format, args...)
}

type EconomicMetricsCollector struct {
	elReader     *sdkelcontracts.ChainReader
	avsReader    *sdkavsregistry.ChainReader
	avsName      string
	logger       logging.Logger
	operatorAddr common.Address
	quorumNames  map[types.QuorumNum]string

	operatorStake *prometheus.GaugeVec
}

func NewMetricsOnlyEconomicCollector(
	elReader interface{},
	avsReader interface{},
	avsName string,
	logger logging.Logger,
	operatorAddr common.Address,
	quorumNames map[types.QuorumNum]string,
) prometheus.Collector {
	wrappedLogger := &MetricsOnlyLogger{
		Logger: logger,
	}

	operatorStake := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "eigenlayer",
			Subsystem: "economic",
			Name:      "operator_stake",
			Help:      "Operator stake in the EigenLayer protocol",
		},
		[]string{"quorum"},
	)

	// Type assert the interfaces to their proper types
	var elChainReader *sdkelcontracts.ChainReader
	var avsChainReader *sdkavsregistry.ChainReader

	if elReader != nil {
		if el, ok := elReader.(*sdkelcontracts.ChainReader); ok {
			elChainReader = el
		}
	}

	if avsReader != nil {
		if avs, ok := avsReader.(*sdkavsregistry.ChainReader); ok {
			avsChainReader = avs
		}
	}

	return &EconomicMetricsCollector{
		elReader:      elChainReader,
		avsReader:     avsChainReader,
		avsName:       avsName,
		logger:        wrappedLogger,
		operatorAddr:  operatorAddr,
		quorumNames:   quorumNames,
		operatorStake: operatorStake,
	}
}

func (c *EconomicMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	c.operatorStake.Describe(ch)
}

func (c *EconomicMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx := context.Background()

	for quorumNum, quorumName := range c.quorumNames {
		stake := c.getOperatorStake(ctx, quorumNum)
		c.operatorStake.WithLabelValues(quorumName).Set(stake)
	}

	c.operatorStake.Collect(ch)
}

// getOperatorStake attempts to query the actual operator stake from EigenLayer contracts
func (c *EconomicMetricsCollector) getOperatorStake(ctx context.Context, quorumNum types.QuorumNum) float64 {
	// If we don't have proper readers, return 0 and log debug message
	if c.elReader == nil || c.avsReader == nil {
		c.logger.Debug("EigenLayer or AVS reader not available for stake collection",
			"quorum", quorumNum,
			"operatorAddr", c.operatorAddr.Hex(),
			"hasElReader", c.elReader != nil,
			"hasAvsReader", c.avsReader != nil,
		)
		return 0
	}

	// Try to get operator ID first
	operatorId, err := c.avsReader.GetOperatorId(&bind.CallOpts{Context: ctx}, c.operatorAddr)
	if err != nil {
		c.logger.Debug("Failed to get operator ID for stake collection",
			"err", err,
			"quorum", quorumNum,
			"operatorAddr", c.operatorAddr.Hex(),
		)
		return 0
	}

	// Check if operator is registered (non-zero operator ID)
	if operatorId == [32]byte{} {
		c.logger.Debug("Operator not registered, stake is 0",
			"quorum", quorumNum,
			"operatorAddr", c.operatorAddr.Hex(),
		)
		return 0
	}

	// Try to get operator stake from the registry
	// Note: This is a simplified approach - in production you might want to:
	// 1. Get the current block number
	// 2. Query operator state at that block
	// 3. Sum up stakes across all strategies for the quorum

	// For now, we'll use a placeholder implementation that doesn't cause errors
	c.logger.Debug("Operator stake collection partially implemented",
		"quorum", quorumNum,
		"operatorAddr", c.operatorAddr.Hex(),
		"operatorId", fmt.Sprintf("%x", operatorId),
		"note", "Full stake calculation not yet implemented",
	)

	// Return 0 for now - this can be enhanced later with proper stake calculation
	return 0
}
