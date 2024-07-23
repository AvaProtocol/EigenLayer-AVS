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

	IncNumTasksReceived(string)
	IncNumTasksAcceptedByAggregator()
	// This metric would either need to be tracked by the aggregator itself,
	// or we would need to write a collector that queries onchain for this info
	// AddPercentageStakeSigned(percentage float64)
}

// AvsMetrics contains instrumented metrics that should be incremented by the avs node using the methods below
type AvsAndEigenMetrics struct {
	metrics.Metrics

	uptime *prometheus.CounterVec

	numWorkerLoop     *prometheus.CounterVec
	numPingSent       *prometheus.CounterVec
	numCheckProcessed *prometheus.CounterVec
	numTasksReceived  *prometheus.CounterVec
	// if numSignedTaskResponsesAcceptedByAggregator != numTasksReceived, then there is a bug
	numSignedTaskResponsesAcceptedByAggregator *prometheus.CounterVec

	operatorAddress string
	version string
}

const apNamespace = "ap"

func NewAvsAndEigenMetrics(avsName, operatorAddress, version string, eigenMetrics *metrics.EigenMetrics, reg prometheus.Registerer) *AvsAndEigenMetrics {
	return &AvsAndEigenMetrics{
		Metrics: eigenMetrics,

		uptime: promauto.With(reg).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "uptime_milliseconds_total",
				Help:      "The elapse time in milliseconds since the node is booted",
			}, []string{"operator", "version"}),

		numWorkerLoop: promauto.With(reg).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_worker_loop_total",
				Help:      "The number of worker loop by the operator. If it isn't increasing, the operator is stuck",
			}, []string{"operator", "version"}),

		numPingSent: promauto.With(reg).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_ping_total",
				Help:      "The number of heartbeat send by operator. If it isn't increasing, the operator failed to communicate with aggregator",
			}, []string{"operator", "version", "status"}),

		numCheckProcessed: promauto.With(reg).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_check_processed_total",
				Help:      "The number of check has been performed by operator.",
			}, []string{"operator", "version", "type", "status"}),

		numTasksReceived: promauto.With(reg).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_tasks_received",
				Help:      "The number of tasks received by reading from the avs service manager contract",
			}, []string{"operator", "version", "type"}),

		numSignedTaskResponsesAcceptedByAggregator: promauto.With(reg).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "num_signed_task_responses_accepted_by_aggregator",
				Help:      "The number of signed task responses accepted by the aggregator",
			}, []string{"operator", "version",}),

		operatorAddress: operatorAddress,
		version: version,
	}
}

func (m *AvsAndEigenMetrics) IncNumTasksReceived(checkType string) {
	m.numTasksReceived.WithLabelValues(m.operatorAddress, m.version, checkType).  Inc()
}

func (m *AvsAndEigenMetrics) IncNumTasksAcceptedByAggregator() {
	m.numSignedTaskResponsesAcceptedByAggregator.WithLabelValues(m.operatorAddress, m.version).Inc()
}

func (m *AvsAndEigenMetrics) IncWorkerLoop() {
	m.numWorkerLoop.WithLabelValues(m.operatorAddress, m.version).Inc()
}

func (m *AvsAndEigenMetrics) IncPing(status string) {
	m.numPingSent.WithLabelValues(m.operatorAddress, m.version, status).Inc()
}

func (m *AvsAndEigenMetrics) IncNumCheckRun(checkType, status string) {
	m.numCheckProcessed.WithLabelValues(m.operatorAddress, m.version, checkType, status).Inc()
}

func (m *AvsAndEigenMetrics) AddUptime(total float64) {
	m.uptime.WithLabelValues(m.operatorAddress, m.version).Add(total)
}
