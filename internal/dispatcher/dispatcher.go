package dispatcher

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type Config struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Timeout     time.Duration
}

func DefaultConfig() Config {
	return Config{
		MaxAttempts: 5,
		BaseDelay:   1 * time.Second,
		MaxDelay:    30 * time.Second,
		Timeout:     30 * time.Second,
	}
}

type WebhookPayload struct {
	ExecutionID string `json:"execution_id"`
	DispatchID  string `json:"dispatch_id"`
}

type Outcome int

const (
	OutcomeSuccess Outcome = iota
	OutcomeRejected
	OutcomeExhausted
)

type Result struct {
	Outcome      Outcome
	AttemptCount int
	StatusCode   int
	Err          error
}

type Dispatcher struct {
	client *http.Client
	cfg    Config
	logger *slog.Logger
}

func New(client *http.Client, cfg Config, logger *slog.Logger) *Dispatcher {
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{client: client, cfg: cfg, logger: logger}
}

// Deliver makes a single delivery attempt and returns the result immediately.
func (d *Dispatcher) Deliver(ctx context.Context, url, secret string, execID, dispatchID uuid.UUID) Result {
	payload := WebhookPayload{
		ExecutionID: execID.String(),
		DispatchID:  dispatchID.String(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{Outcome: OutcomeExhausted, Err: fmt.Errorf("marshal payload: %w", err)}
	}
	signature := SignPayload(secret, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: OutcomeExhausted, Err: fmt.Errorf("build request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Rebuno-Signature", "sha256="+signature)
	req.Header.Set("Rebuno-Execution-Id", execID.String())

	resp, err := d.client.Do(req)
	if err != nil {
		d.logger.Debug("dispatch attempt failed", slog.String("execution_id", execID.String()), slog.String("error", err.Error()))
		return Result{Outcome: OutcomeExhausted, AttemptCount: 1, Err: err}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return Result{Outcome: OutcomeSuccess, AttemptCount: 1, StatusCode: resp.StatusCode}
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return Result{Outcome: OutcomeRejected, AttemptCount: 1, StatusCode: resp.StatusCode, Err: fmt.Errorf("agent rejected dispatch with %d", resp.StatusCode)}
	}
	d.logger.Debug("dispatch attempt server error", slog.String("execution_id", execID.String()), slog.Int("status", resp.StatusCode))
	return Result{Outcome: OutcomeExhausted, AttemptCount: 1, StatusCode: resp.StatusCode, Err: fmt.Errorf("agent returned %d", resp.StatusCode)}
}

func SignPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func BackoffDelay(base, max time.Duration, attempt int) time.Duration {
	exponent := attempt - 1
	if exponent < 0 {
		exponent = 0
	}
	d := time.Duration(math.Pow(2, float64(exponent))) * base
	if d > max {
		d = max
	}
	jitter := time.Duration(rand.Float64() * 0.25 * float64(d))
	return d + jitter
}
