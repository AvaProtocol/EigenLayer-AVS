package metrics

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/Layr-Labs/eigensdk-go/chainio/clients/elcontracts"
	"github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/Layr-Labs/eigensdk-go/metrics/collectors/economic"
	"github.com/Layr-Labs/eigensdk-go/types"
)

func NewMetricsOnlyEconomicCollector(
	elReader elcontracts.ELReader,
	avsReader economic.AvsRegistryReader,
	avsName string,
	logger logging.Logger,
	operatorAddr common.Address,
	quorumNames map[types.QuorumNum]string,
) prometheus.Collector {
	return &MetricsOnlyCollector{
		collector: economic.NewCollector(
			elReader,
			avsReader,
			avsName,
			logger,
			operatorAddr,
			quorumNames,
		),
		logger: logger,
	}
}

type MetricsOnlyCollector struct {
	collector prometheus.Collector
	logger    logging.Logger
}

func (c *MetricsOnlyCollector) Describe(ch chan<- *prometheus.Desc) {
	c.collector.Describe(ch)
}

func (c *MetricsOnlyCollector) Collect(ch chan<- prometheus.Metric) {
	wrappedLogger := &metricsOnlyLogger{
		Logger: c.logger,
	}

	originalLogger := c.logger
	c.logger = wrappedLogger

	c.collector.Collect(ch)

	c.logger = originalLogger
}

type metricsOnlyLogger struct {
	logging.Logger
}

func (l *metricsOnlyLogger) Error(msg string, keysAndValues ...interface{}) {
	l.Logger.Error("[METRICS ONLY] "+msg, keysAndValues...)
}

func (l *metricsOnlyLogger) Errorf(format string, args ...interface{}) {
	l.Logger.Errorf("[METRICS ONLY] "+format, args...)
}
