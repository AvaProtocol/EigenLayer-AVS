package metrics

import (
	"github.com/Layr-Labs/eigensdk-go/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type MetricsGenerator interface {
	metrics.Metrics

	IncTick()
	IncPing()

	AddUptime(float64)

	IncNumCheckRun()

	IncNumTasksReceived()
	IncNumTasksAcceptedByAggregator()
	// This metric would either need to be tracked by the aggregator itself,
	// or we would need to write a collector that queries onchain for this info
	// AddPercentageStakeSigned(percentage float64)
}

// AvsMetrics contains instrumented metrics that should be incremented by the avs node using the methods below
type AvsAndEigenMetrics struct {
	metrics.Metrics

	uptime prometheus.Counter

	numTick     prometheus.Counter
	numPingSent prometheus.Counter

	numTasksReceived prometheus.Counter
	// if numSignedTaskResponsesAcceptedByAggregator != numTasksReceived, then there is a bug
	numSignedTaskResponsesAcceptedByAggregator prometheus.Counter
}

const apNamespace = "ap"

func NewAvsAndEigenMetrics(avsName string, eigenMetrics *metrics.EigenMetrics, reg prometheus.Registerer) *AvsAndEigenMetrics {
	return &AvsAndEigenMetrics{
		Metrics: eigenMetrics,

		uptime: promauto.With(reg).NewCounter(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "uptime",
				Help:      "The elapse time in milliseconds since the node is booted",
			}),

		numTick: promauto.With(reg).NewCounter(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_tick",
				Help:      "The number of worker loop tick by the operator. If it isn't increasing, the operator is stuck",
			}),

		numPingSent: promauto.With(reg).NewCounter(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_ping",
				Help:      "The number of heartbeat send by operator. If it isn't increasing, the operator failed to communicate with aggregator",
			}),

		numTasksReceived: promauto.With(reg).NewCounter(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_tasks_received",
				Help:      "The number of tasks received by reading from the avs service manager contract",
			}),
		numSignedTaskResponsesAcceptedByAggregator: promauto.With(reg).NewCounter(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_signed_task_responses_accepted_by_aggregator",
				Help:      "The number of signed task responses accepted by the aggregator",
			}),
	}
}

func (m *AvsAndEigenMetrics) IncNumTasksReceived() {
	m.numTasksReceived.Inc()
}

func (m *AvsAndEigenMetrics) IncNumTasksAcceptedByAggregator() {
	m.numSignedTaskResponsesAcceptedByAggregator.Inc()
}

func (m *AvsAndEigenMetrics) IncTick() {
	m.numTick.Inc()
}

func (m *AvsAndEigenMetrics) IncPing() {
	m.numTick.Inc()
}

func (m *AvsAndEigenMetrics) IncNumCheckRun() {
	m.numTick.Inc()
}

func (m *AvsAndEigenMetrics) AddUptime(total float64) {
	m.uptime.Add(total)
}
