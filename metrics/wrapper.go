package metrics

import (
	"context"
	"fmt"

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
		} else {
			wrappedLogger.Error("Failed type assertion for elReader: expected *sdkelcontracts.ChainReader")
		}
	}

	if avsReader != nil {
		if avs, ok := avsReader.(*sdkavsregistry.ChainReader); ok {
			avsChainReader = avs
		} else {
			wrappedLogger.Error("Failed type assertion for avsReader: expected *sdkavsregistry.ChainReader")
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

	// For now, skip the actual RPC call that's causing "Unknown block" errors
	// The stake calculation is not yet implemented anyway, so we can safely return 0
	// This prevents the metrics collection from failing due to RPC node sync issues
	c.logger.Debug("Stake collection temporarily disabled to avoid RPC sync issues",
		"quorum", quorumNum,
		"operatorAddr", c.operatorAddr.Hex(),
		"note", "This will be re-enabled when full stake calculation is implemented",
	)
	return 0

	// TODO: Re-enable this when implementing full stake calculation
	// Use a confirmed block number (a few blocks behind) to avoid sync issues
	// The original implementation was:
	// 1. Get operator ID from AVS registry
	// 2. Check if operator is registered (non-zero operator ID)
	// 3. Get operator stake from the registry
	// 4. Sum up stakes across all strategies for the quorum
	//
	// This was causing "Unknown block" errors because it was querying the "latest" block
	// which might not be available due to RPC node sync issues or reorganizations
}
