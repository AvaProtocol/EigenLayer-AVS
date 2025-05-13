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

type MetricsOnlyCollector struct {
	originalCollector prometheus.Collector
	logger            logging.Logger
	wrappedLogger     *MetricsOnlyLogger
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

	return &MetricsOnlyCollector{
		originalCollector: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "eigenlayer",
			Subsystem: "economic",
			Name:      "operator_stake",
			Help:      "Operator stake in the EigenLayer protocol",
		}),
		logger:        logger,
		wrappedLogger: wrappedLogger,
	}
}

func (c *MetricsOnlyCollector) Describe(ch chan<- *prometheus.Desc) {
	c.originalCollector.Describe(ch)
}

func (c *MetricsOnlyCollector) Collect(ch chan<- prometheus.Metric) {
	c.originalCollector.Collect(ch)
}
