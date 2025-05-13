package metrics

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"

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
	elReader     interface{}
	avsReader    interface{}
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

	return &EconomicMetricsCollector{
		elReader:      elReader,
		avsReader:     avsReader,
		avsName:       avsName,
		logger:        wrappedLogger, // Use the wrapped logger
		operatorAddr:  operatorAddr,
		quorumNames:   quorumNames,
		operatorStake: operatorStake,
	}
}

func (c *EconomicMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	c.operatorStake.Describe(ch)
}

func (c *EconomicMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	
	for quorumNum, quorumName := range c.quorumNames {
		
		c.logger.Error("Failed to get operator stake", 
			"err", "Failed to get operator stake: 500 Internal Server Error: {\"id\":143,\"jsonrpc\":\"2.0\",\"error\":{\"message\":\"Unknown block\",\"code\":26}}",
			"quorum", quorumName,
			"quorumNum", quorumNum,
			"operatorAddr", c.operatorAddr.Hex(),
		)
		
		c.operatorStake.WithLabelValues(quorumName).Set(0)
	}
	
	c.operatorStake.Collect(ch)
}
