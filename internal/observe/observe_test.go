package observe_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rebuno/rebuno/internal/observe"
)

func TestRecordReplayCounter(t *testing.T) {
	obs := observe.New()
	obs.RecordReplay(true)
	obs.RecordReplay(true)
	obs.RecordReplay(false)

	body := renderRegistry(t, obs.Registry())
	if !strings.Contains(body, replayTotalMetric) {
		t.Fatalf("expected metrics to contain %q", replayTotalMetric)
	}
	if !strings.Contains(body, `hit="true"`) {
		t.Fatalf("expected hit=true label")
	}
	if !strings.Contains(body, `hit="false"`) {
		t.Fatalf("expected hit=false label")
	}
}

func TestRecordDispatchOutcome(t *testing.T) {
	obs := observe.New()
	obs.RecordDispatchOutcome("success")
	obs.RecordDispatchOutcome("rejected")
	obs.RecordDispatchOutcome("exhausted")

	body := renderRegistry(t, obs.Registry())
	if !strings.Contains(body, dispatchOutcomeMetric) {
		t.Fatalf("expected metrics to contain %q", dispatchOutcomeMetric)
	}
}

func TestRecordPolicyLatency(t *testing.T) {
	obs := observe.New()
	obs.RecordPolicyLatency(25 * time.Millisecond)

	body := renderRegistry(t, obs.Registry())
	if !strings.Contains(body, policyLatencyMetric) {
		t.Fatalf("expected metrics to contain %q", policyLatencyMetric)
	}
}

func TestRecordExecutionTerminal(t *testing.T) {
	obs := observe.New()
	obs.RecordExecutionTerminal("completed")
	obs.RecordExecutionTerminal("failed")
	obs.RecordExecutionTerminal("cancelled")

	body := renderRegistry(t, obs.Registry())
	if !strings.Contains(body, executionsCompletedMetric) {
		t.Fatalf("expected metrics to contain %q", executionsCompletedMetric)
	}
	for _, status := range []string{"completed", "failed", "cancelled"} {
		if !strings.Contains(body, `status="`+status+`"`) {
			t.Fatalf("expected status=%q label", status)
		}
	}
}

func TestRecordDispatchLatency(t *testing.T) {
	obs := observe.New()
	obs.RecordDispatchLatency(50 * time.Millisecond)

	body := renderRegistry(t, obs.Registry())
	if !strings.Contains(body, dispatchLatencyMetric) {
		t.Fatalf("expected metrics to contain %q", dispatchLatencyMetric)
	}
}

func TestRecordReclaimedStalled(t *testing.T) {
	obs := observe.New()
	obs.RecordReclaimedStalled(3)
	obs.RecordReclaimedStalled(0) // no-op, must not panic

	body := renderRegistry(t, obs.Registry())
	if !strings.Contains(body, dispatchesReclaimedMetric+" 3") {
		t.Fatalf("expected %q to equal 3, body:\n%s", dispatchesReclaimedMetric, body)
	}
}

func TestRecordWorkerError(t *testing.T) {
	obs := observe.New()
	obs.RecordWorkerError("dispatch")

	body := renderRegistry(t, obs.Registry())
	if !strings.Contains(body, workerErrorsMetric) {
		t.Fatalf("expected metrics to contain %q", workerErrorsMetric)
	}
	if !strings.Contains(body, `worker="dispatch"`) {
		t.Fatalf("expected worker=dispatch label")
	}
}
func TestClosedLabelDomainsStartAtZero(t *testing.T) {
	body := renderRegistry(t, observe.New().Registry())

	for _, want := range []string{
		`rebuno_replay_total{hit="true"} 0`,
		`rebuno_replay_total{hit="false"} 0`,
		`rebuno_dispatch_outcomes_total{outcome="success"} 0`,
		`rebuno_dispatch_outcomes_total{outcome="rejected"} 0`,
		`rebuno_dispatch_outcomes_total{outcome="exhausted"} 0`,
		`rebuno_policy_decisions_total{decision="allow"} 0`,
		`rebuno_policy_decisions_total{decision="deny"} 0`,
		`rebuno_policy_decisions_total{decision="require_approval"} 0`,
		`rebuno_approval_outcomes_total{outcome="granted"} 0`,
		`rebuno_approval_outcomes_total{outcome="denied"} 0`,
		`rebuno_approval_outcomes_total{outcome="expired"} 0`,
		`rebuno_steps_submitted_total{kind="tool_call"} 0`,
		`rebuno_steps_submitted_total{kind="llm_call"} 0`,
		`rebuno_executions_completed_total{status="completed"} 0`,
		`rebuno_executions_completed_total{status="failed"} 0`,
		`rebuno_executions_completed_total{status="cancelled"} 0`,
		`rebuno_rate_limit_decisions_total{outcome="limited"} 0`,
		`rebuno_rate_limit_decisions_total{outcome="error_allowed"} 0`,
		`rebuno_rate_limit_decisions_total{outcome="error_denied"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected a fresh registry to export %q", want)
		}
	}
}

func TestRecordedValuesArePreDeclared(t *testing.T) {
	obs := observe.New()
	before := strings.Count(renderRegistry(t, obs.Registry()), "\n")

	// Every value the kernel actually passes today.
	obs.RecordReplay(true)
	obs.RecordReplay(false)
	for _, v := range []string{"success", "rejected", "exhausted"} {
		obs.RecordDispatchOutcome(v)
	}
	for _, v := range []string{"allow", "deny", "require_approval"} {
		obs.RecordPolicyDecision(v)
	}
	for _, v := range []string{"granted", "denied", "expired"} {
		obs.RecordApprovalOutcome(v)
	}
	for _, v := range []string{"tool_call", "llm_call"} {
		obs.RecordStepSubmitted(v)
	}
	for _, v := range []string{"completed", "failed", "cancelled"} {
		obs.RecordExecutionTerminal(v)
	}
	for _, v := range []string{"limited", "error_allowed", "error_denied"} {
		obs.RecordRateLimit(v)
	}

	if after := strings.Count(renderRegistry(t, obs.Registry()), "\n"); after != before {
		t.Errorf("recording created %d new series; every value above must be pre-declared in initLabels", after-before)
	}
}

func TestRuntimeCollectorsRegistered(t *testing.T) {
	obs := observe.New()
	body := renderRegistry(t, obs.Registry())
	for _, m := range []string{"go_goroutines", "process_cpu_seconds_total"} {
		if !strings.Contains(body, m) {
			t.Fatalf("expected runtime metric %q to be exported", m)
		}
	}
}

func TestNilObserverIsNoop(t *testing.T) {
	var obs *observe.Observer
	// All new recorders must be nil-safe.
	obs.RecordExecutionTerminal("completed")
	obs.RecordDispatchLatency(time.Second)
	obs.RecordReclaimedStalled(5)
	obs.RecordWorkerError("dispatch")
}

func TestMetricsEndpoint(t *testing.T) {
	obs := observe.New()
	obs.RecordReplay(true)

	handler := promhttp.HandlerFor(obs.Registry(), promhttp.HandlerOpts{})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, replayTotalMetric) {
		t.Fatalf("expected /metrics body to contain %q", replayTotalMetric)
	}
}

const (
	replayTotalMetric         = "rebuno_replay_total"
	dispatchOutcomeMetric     = "rebuno_dispatch_outcomes_total"
	policyLatencyMetric       = "rebuno_policy_latency_seconds"
	executionsCompletedMetric = "rebuno_executions_completed_total"
	dispatchLatencyMetric     = "rebuno_dispatch_latency_seconds"
	dispatchesReclaimedMetric = "rebuno_dispatches_reclaimed_total"
	workerErrorsMetric        = "rebuno_worker_errors_total"
)

func renderRegistry(t *testing.T, reg prometheus.Gatherer) string {
	t.Helper()
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr.Body.String()
}
