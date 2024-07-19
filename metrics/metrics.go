package metrics

import (
	"github.com/Layr-Labs/eigensdk-go/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type MetricsGenerator interface {
	metrics.Metrics

	IncWorkerLoop()
	IncPing(string)

	AddUptime(float64)

	IncNumCheckRun(string, string)

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

	numWorkerLoop     prometheus.Counter
	numPingSent       *prometheus.CounterVec
	numCheckProcessed *prometheus.CounterVec
	numTasksReceived  prometheus.Counter
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
				Name:      "uptime_milliseconds_total",
				Help:      "The elapse time in milliseconds since the node is booted",
			}),

		numWorkerLoop: promauto.With(reg).NewCounter(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_worker_loop_total",
				Help:      "The number of worker loop by the operator. If it isn't increasing, the operator is stuck",
			}),

		numPingSent: promauto.With(reg).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_ping_total",
				Help:      "The number of heartbeat send by operator. If it isn't increasing, the operator failed to communicate with aggregator",
			}, []string{"status"}),

		numCheckProcessed: promauto.With(reg).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_check_processed_total",
				Help:      "The number of check has been performed by operator.",
			}, []string{"type", "status"}),

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

func (m *AvsAndEigenMetrics) IncWorkerLoop() {
	m.numWorkerLoop.Inc()
}

func (m *AvsAndEigenMetrics) IncPing(status string) {
	m.numPingSent.WithLabelValues(status).Inc()
}

func (m *AvsAndEigenMetrics) IncNumCheckRun(checkType, status string) {
	m.numCheckProcessed.WithLabelValues(checkType, status).Inc()
}

func (m *AvsAndEigenMetrics) AddUptime(total float64) {
	m.uptime.Add(total)
}
