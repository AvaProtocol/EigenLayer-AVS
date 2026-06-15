package metrics

import (
	"strconv"

	"github.com/Layr-Labs/eigensdk-go/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type MetricsGenerator interface {
	metrics.Metrics

	IncWorkerLoop()
	IncPing(string)

	SetPingDuration(float64)

	AddUptime(float64)

	IncNumCheckRun(string, string)

	IncNumTasksReceived(string)
	IncNumTasksAcceptedByAggregator()
	// This metric would either need to be tracked by the aggregator itself,
	// or we would need to write a collector that queries onchain for this info
	// AddPercentageStakeSigned(percentage float64)

	// Per-chain capability gauges — updated from the Ping loop so a
	// stalled chain shows up in dashboards immediately. SetChainAdvertised
	// is 1 when the chain is in the advertised set, 0 when it's been
	// dropped (subscription stalled). SetChainHeadLagSeconds is the time
	// since the last block head was observed for the chain — used to
	// alert on subscriptions that fall behind even before they fail the
	// staleness threshold.
	SetChainAdvertised(chainID int64, advertised bool)
	SetChainHeadLagSeconds(chainID int64, lagSeconds float64)
	SetEventSubscriptions(chainID int64, active, desired int)
	IncEventSubscriptionRebuildFailures(chainID int64)
}

// AvsMetrics contains instrumented metrics that should be incremented by the avs node using the methods below
type AvsAndEigenMetrics struct {
	metrics.Metrics

	uptime *prometheus.CounterVec

	numWorkerLoop *prometheus.CounterVec

	numPingSent  *prometheus.CounterVec
	durationPing *prometheus.GaugeVec

	numCheckProcessed *prometheus.CounterVec
	numTasksReceived  *prometheus.CounterVec
	// if numSignedTaskResponsesAcceptedByAggregator != numTasksReceived, then there is a bug
	numSignedTaskResponsesAcceptedByAggregator *prometheus.CounterVec

	chainAdvertised    *prometheus.GaugeVec
	chainHeadLagSecond *prometheus.GaugeVec
	eventSubsActive    *prometheus.GaugeVec
	eventSubsDesired   *prometheus.GaugeVec
	eventRebuildErrors *prometheus.CounterVec

	operatorAddress string
	version         string
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

		durationPing: promauto.With(reg).NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: apNamespace,
				Name:      "ping_duration_seconds",
				Help:      "The duration of ping check send to operator. If it spikes, it could indicator aggreator issues or operator network issue",
			}, []string{"operator", "version"}),

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
			}, []string{"operator", "version"}),

		chainAdvertised: promauto.With(reg).NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: apNamespace,
				Name:      "operator_advertised_chains",
				Help:      "1 when the operator is currently advertising this chain to the aggregator, 0 when the chain has been dropped (stalled subscription). Per (operator, chain_id).",
			}, []string{"operator", "version", "chain_id"}),

		chainHeadLagSecond: promauto.With(reg).NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: apNamespace,
				Name:      "operator_chain_head_lag_seconds",
				Help:      "Seconds elapsed since the operator last observed a new block head on this chain. Rising values indicate a degrading or hung subscription before it crosses the staleness threshold and is dropped from advertising.",
			}, []string{"operator", "version", "chain_id"}),

		eventSubsActive: promauto.With(reg).NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: apNamespace,
				Name:      "event_subscriptions_active",
				Help:      "The number of active unique event subscriptions.",
			}, []string{"operator", "version", "chain_id"}),

		eventSubsDesired: promauto.With(reg).NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: apNamespace,
				Name:      "event_subscriptions_desired",
				Help:      "The number of desired unique event subscriptions.",
			}, []string{"operator", "version", "chain_id"}),

		eventRebuildErrors: promauto.With(reg).NewCounterVec(
			prometheus.CounterOpts{
				Namespace: apNamespace,
				Name:      "event_subscription_rebuild_failures_total",
				Help:      "The number of failed event subscription rebuild attempts, covering both full post-reconnect rebuilds and incremental subscription updates.",
			}, []string{"operator", "version", "chain_id"}),

		operatorAddress: operatorAddress,
		version:         version,
	}
}

func (m *AvsAndEigenMetrics) IncNumTasksReceived(checkType string) {
	m.numTasksReceived.WithLabelValues(m.operatorAddress, m.version, checkType).Inc()
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

func (m *AvsAndEigenMetrics) SetPingDuration(duration float64) {
	m.durationPing.WithLabelValues(m.operatorAddress, m.version).Set(duration)
}

func (m *AvsAndEigenMetrics) SetChainAdvertised(chainID int64, advertised bool) {
	v := 0.0
	if advertised {
		v = 1.0
	}
	m.chainAdvertised.WithLabelValues(m.operatorAddress, m.version, strconv.FormatInt(chainID, 10)).Set(v)
}

func (m *AvsAndEigenMetrics) SetChainHeadLagSeconds(chainID int64, lagSeconds float64) {
	m.chainHeadLagSecond.WithLabelValues(m.operatorAddress, m.version, strconv.FormatInt(chainID, 10)).Set(lagSeconds)
}

func (m *AvsAndEigenMetrics) SetEventSubscriptions(chainID int64, active, desired int) {
	labels := []string{m.operatorAddress, m.version, strconv.FormatInt(chainID, 10)}
	m.eventSubsActive.WithLabelValues(labels...).Set(float64(active))
	m.eventSubsDesired.WithLabelValues(labels...).Set(float64(desired))
}

func (m *AvsAndEigenMetrics) IncEventSubscriptionRebuildFailures(chainID int64) {
	m.eventRebuildErrors.WithLabelValues(m.operatorAddress, m.version, strconv.FormatInt(chainID, 10)).Inc()
}
