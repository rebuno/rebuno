package observe

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	Namespace = "rebuno"

	replayTotalName           = "replay_total"
	dispatchOutcomesTotalName = "dispatch_outcomes_total"
	dispatchLatencyName       = "dispatch_latency_seconds"
	dispatchesReclaimedTotal  = "dispatches_reclaimed_total"
	queueDepthName            = "queue_depth"
	policyLatencyName         = "policy_latency_seconds"
	stepsSubmittedTotalName   = "steps_submitted_total"
	executionsCreatedTotal    = "executions_created_total"
	executionsCompletedTotal  = "executions_completed_total"
	workerErrorsTotalName     = "worker_errors_total"
	rateLimitDecisionsTotal   = "rate_limit_decisions_total"

	httpRequestsTotalName = "http_requests_total"
	httpRequestDuration   = "http_request_duration_seconds"
)

// Observer exposes Prometheus and OpenTelemetry helpers for the kernel.
// All methods are nil-safe; passing a nil Observer is equivalent to a no-op.
type Observer struct {
	registry *prometheus.Registry
	tracer   trace.Tracer

	replayTotal           *prometheus.CounterVec
	dispatchOutcomesTotal *prometheus.CounterVec
	dispatchLatency       prometheus.Histogram
	dispatchesReclaimed   prometheus.Counter
	queueDepth            prometheus.Gauge
	policyLatency         prometheus.Histogram
	stepsSubmittedTotal   *prometheus.CounterVec
	executionsCreated     prometheus.Counter
	executionsCompleted   *prometheus.CounterVec
	workerErrors          *prometheus.CounterVec
	rateLimitTotal        *prometheus.CounterVec

	httpRequestsTotal *prometheus.CounterVec
	httpDuration      *prometheus.HistogramVec
}

var (
	defaultOnce sync.Once
	defaultObs  *Observer
)

// Default returns a shared package-level Observer backed by a fresh
// Prometheus registry. Tests should prefer New() to avoid sharing metrics.
func Default() *Observer {
	defaultOnce.Do(func() { defaultObs = New() })
	return defaultObs
}

// New creates an Observer with its own fresh Prometheus registry.
func New() *Observer {
	reg := prometheus.NewRegistry()

	obs := &Observer{
		registry: reg,
		tracer:   otel.Tracer("github.com/rebuno/kernel"),

		replayTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      replayTotalName,
			Help:      "Total number of idempotent step replays, labelled by cache hit/miss.",
		}, []string{"hit"}),

		dispatchOutcomesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      dispatchOutcomesTotalName,
			Help:      "Total dispatch outcomes labelled by outcome (success, rejected, exhausted).",
		}, []string{"outcome"}),

		dispatchLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      dispatchLatencyName,
			Help:      "Webhook delivery attempt latency in seconds (the outbound agent call).",
			Buckets:   prometheus.DefBuckets,
		}),

		dispatchesReclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      dispatchesReclaimedTotal,
			Help:      "Total dispatches reclaimed from a stalled lease (a crashed replica left them in-flight).",
		}),

		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: Namespace,
			Name:      queueDepthName,
			Help:      "Recent number of dispatches claimed from the queue per drain tick.",
		}),

		policyLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      policyLatencyName,
			Help:      "Policy evaluation latency in seconds.",
			Buckets:   prometheus.DefBuckets,
		}),

		stepsSubmittedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      stepsSubmittedTotalName,
			Help:      "Total step submissions labelled by step kind.",
		}, []string{"kind"}),

		executionsCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      executionsCreatedTotal,
			Help:      "Total number of executions created.",
		}),

		executionsCompleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      executionsCompletedTotal,
			Help:      "Total executions that reached a terminal state, labelled by status (completed, failed, cancelled).",
		}, []string{"status"}),

		workerErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      workerErrorsTotalName,
			Help:      "Total background worker tick errors labelled by worker (dispatch, singletons).",
		}, []string{"worker"}),

		rateLimitTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      rateLimitDecisionsTotal,
			Help:      "Rate-limit decisions labelled by outcome (limited, error_allowed, error_denied).",
		}, []string{"outcome"}),

		httpRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: Namespace,
			Name:      httpRequestsTotalName,
			Help:      "Total HTTP requests to the kernel API labelled by route and status code.",
		}, []string{"route", "status"}),

		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: Namespace,
			Name:      httpRequestDuration,
			Help:      "HTTP request duration for the kernel API in seconds, labelled by route and status code.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"route", "status"}),
	}

	reg.MustRegister(
		obs.replayTotal,
		obs.dispatchOutcomesTotal,
		obs.dispatchLatency,
		obs.dispatchesReclaimed,
		obs.queueDepth,
		obs.policyLatency,
		obs.stepsSubmittedTotal,
		obs.executionsCreated,
		obs.executionsCompleted,
		obs.workerErrors,
		obs.rateLimitTotal,
		obs.httpRequestsTotal,
		obs.httpDuration,
	)

	// Go runtime and process collectors (goroutines, GC, heap, open FDs, CPU).
	// The bare registry above does not include these by default.
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	return obs
}

func (o *Observer) Registry() *prometheus.Registry {
	if o == nil {
		return nil
	}
	return o.registry
}

func (o *Observer) Tracer() trace.Tracer {
	if o == nil {
		return otel.Tracer("")
	}
	return o.tracer
}

func (o *Observer) RecordReplay(hit bool) {
	if o == nil {
		return
	}
	o.replayTotal.WithLabelValues(strconv.FormatBool(hit)).Inc()
}

func (o *Observer) ReplayHit() { o.RecordReplay(true) }

func (o *Observer) ReplayMiss() { o.RecordReplay(false) }

func (o *Observer) RecordRateLimit(outcome string) {
	if o == nil {
		return
	}
	o.rateLimitTotal.WithLabelValues(outcome).Inc()
}

func (o *Observer) RecordDispatchOutcome(outcome string) {
	if o == nil {
		return
	}
	o.dispatchOutcomesTotal.WithLabelValues(outcome).Inc()
}

func (o *Observer) RecordDispatchLatency(d time.Duration) {
	if o == nil {
		return
	}
	o.dispatchLatency.Observe(d.Seconds())
}

func (o *Observer) RecordReclaimedStalled(n int) {
	if o == nil || n <= 0 {
		return
	}
	o.dispatchesReclaimed.Add(float64(n))
}

func (o *Observer) RecordQueueDepth(depth int) {
	if o == nil {
		return
	}
	o.queueDepth.Set(float64(depth))
}

func (o *Observer) RecordPolicyLatency(d time.Duration) {
	if o == nil {
		return
	}
	o.policyLatency.Observe(d.Seconds())
}

func (o *Observer) RecordStepSubmitted(kind string) {
	if o == nil {
		return
	}
	o.stepsSubmittedTotal.WithLabelValues(kind).Inc()
}

func (o *Observer) RecordExecutionCreated() {
	if o == nil {
		return
	}
	o.executionsCreated.Inc()
}

func (o *Observer) RecordExecutionTerminal(status string) {
	if o == nil {
		return
	}
	o.executionsCompleted.WithLabelValues(status).Inc()
}

func (o *Observer) RecordWorkerError(worker string) {
	if o == nil {
		return
	}
	o.workerErrors.WithLabelValues(worker).Inc()
}

func (o *Observer) RecordHTTP(route string, statusCode int, duration time.Duration) {
	if o == nil {
		return
	}
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	labels := prometheus.Labels{"route": route, "status": strconv.Itoa(statusCode)}
	o.httpRequestsTotal.With(labels).Inc()
	o.httpDuration.With(labels).Observe(duration.Seconds())
}

// NewLogger builds a slog.Logger writing to stderr in the given format
// ("json" or "text", default text) at the given level ("debug", "info",
// "warn", "error", default info).
func NewLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(h)
}

// InitTracer configures the global OpenTelemetry tracer provider.
//
// When endpoint is empty it installs a provider with no exporter (spans are
// created but dropped) and returns a no-op shutdown — this is the default
// self-hosted posture. When endpoint is set it attaches an OTLP/gRPC exporter
// with a ParentBased(TraceIDRatio(sampleRate)) sampler and returns the
// provider's shutdown func for flush-on-exit.
func InitTracer(ctx context.Context, endpoint string, sampleRate float64, insecure bool, logger *slog.Logger) (func(context.Context) error, error) {
	res := resource.NewWithAttributes(
		"",
		attribute.String("service.name", "rebuno-kernel"),
	)

	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRate))

	if endpoint == "" {
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSampler(sampler),
		)
		otel.SetTracerProvider(tp)
		if logger != nil {
			logger.Info("tracing disabled (no OTEL endpoint configured)")
		}
		return tp.Shutdown, nil
	}

	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(endpoint)}
	if insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("create otlp exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exporter),
	)
	otel.SetTracerProvider(tp)
	if logger != nil {
		logger.Info("tracing enabled", "endpoint", endpoint, "sample_rate", sampleRate)
	}
	return tp.Shutdown, nil
}
