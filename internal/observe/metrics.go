package observe

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Metrics struct {
	ExecutionsTotal    *prometheus.CounterVec
	IntentsTotal       *prometheus.CounterVec
	StepDuration       *prometheus.HistogramVec
	PolicyEvalDuration *prometheus.HistogramVec
	ActiveExecutions   prometheus.Gauge
}

var (
	metricsOnce     sync.Once
	metricsInstance *Metrics
)

func NewMetrics() *Metrics {
	metricsOnce.Do(func() {
		metricsInstance = newMetrics()
	})
	return metricsInstance
}

func newMetrics() *Metrics {
	return &Metrics{
		ExecutionsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "rebuno_executions_total",
			Help: "Total executions created, by terminal status",
		}, []string{"status"}),

		IntentsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "rebuno_intents_total",
			Help: "Total intents processed, by type and decision",
		}, []string{"type", "decision"}),

		StepDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rebuno_step_duration_seconds",
			Help:    "Tool execution latency",
			Buckets: prometheus.DefBuckets,
		}, []string{"tool_id"}),

		PolicyEvalDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rebuno_policy_eval_seconds",
			Help:    "Policy evaluation latency",
			Buckets: prometheus.DefBuckets,
		}, []string{"action"}),

		ActiveExecutions: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "rebuno_active_executions",
			Help: "Non-terminal executions",
		}),
	}
}
