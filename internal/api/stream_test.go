package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/stream"
)

func TestStreamEndToEnd(t *testing.T) {
	adapt, k := setupKernel(t)
	exec, err := k.CreateExecution(context.Background(), testAgentID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	execID := exec.ID.String()

	hub := stream.NewHub(stream.NewMemoryBus())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = hub.Start(ctx) }()
	time.Sleep(10 * time.Millisecond)

	mux := api.NewRouter(adapt, adapt, adapt, "", hub, nil)
	submitStepHTTP(t, mux, k, exec.ID, "streaming", json.RawMessage(`{}`))
	stepID := computeStepID(t, exec.ID, domain.StepKindTool, "streaming", []byte(`{}`), 0)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Bounded so a broken producer path fails the test instead of hanging it.
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reqCancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL+"/v0/executions/"+execID+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	// Without this a reverse proxy buffers the body and the client sees nothing.
	if v := resp.Header.Get("X-Accel-Buffering"); v != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want \"no\"", v)
	}
	// A first body byte at connect, for proxies that ignore the header above.
	head := make([]byte, 6)
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		t.Fatalf("read connect frame: %v", err)
	}
	if string(head) != ": ok\n\n" {
		t.Fatalf("connect frame = %q", head)
	}

	body, _ := json.Marshal(map[string]any{"seq": 7, "data": "hello world"})
	preq := httptest.NewRequest(http.MethodPost, "/v0/executions/"+execID+"/steps/"+stepID+"/stream", bytes.NewReader(body))
	setLeaseHeaders(t, k, preq, exec.ID)
	signAgentRequest(preq, body)
	prr := httptest.NewRecorder()
	mux.ServeHTTP(prr, preq)
	if prr.Code != http.StatusNoContent {
		t.Fatalf("producer status = %d body=%s", prr.Code, prr.Body.String())
	}

	got := readSSEData(t, resp.Body)
	var d stream.Delta
	if err := json.Unmarshal([]byte(got), &d); err != nil {
		t.Fatalf("decode frame %q: %v", got, err)
	}
	if d.StepID != stepID || d.Seq != 7 || d.Data != "hello world" {
		t.Fatalf("unexpected delta: %+v", d)
	}
}

func TestStreamDeltaRejectedFromStaleProducer(t *testing.T) {
	adapt, k := setupKernel(t)
	ctx := context.Background()
	hub := stream.NewHub(stream.NewMemoryBus())
	mux := api.NewRouter(adapt, adapt, adapt, "", hub, nil)
	body := []byte(`{"seq":0,"data":"late"}`)
	publish := func(execID uuid.UUID, stepID string, lease func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v0/executions/"+execID.String()+"/steps/"+stepID+"/stream", bytes.NewReader(body))
		lease(req)
		signAgentRequest(req, body)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}

	t.Run("finished step", func(t *testing.T) {
		exec, err := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		submitStepHTTP(t, mux, k, exec.ID, "done", json.RawMessage(`{}`))
		stepID := computeStepID(t, exec.ID, domain.StepKindTool, "done", []byte(`{}`), 0)
		completeStepHTTP(t, mux, k, exec.ID, stepID)
		rr := publish(exec.ID, stepID, func(r *http.Request) { setLeaseHeaders(t, k, r, exec.ID) })
		if rr.Code != http.StatusConflict {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("superseded lease", func(t *testing.T) {
		exec, err := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		submitStepHTTP(t, mux, k, exec.ID, "slow", json.RawMessage(`{}`))
		stepID := computeStepID(t, exec.ID, domain.StepKindTool, "slow", []byte(`{}`), 0)
		stale := httptest.NewRequest(http.MethodPost, "/", nil)
		setLeaseHeaders(t, k, stale, exec.ID)

		q := k.Deps().Queue
		later := time.Now().UTC().Add(time.Hour)
		if _, err := q.ReclaimStalled(ctx, later, time.Minute, 10); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Claim(ctx, "replica-2", 10, later); err != nil {
			t.Fatal(err)
		}

		rr := publish(exec.ID, stepID, func(r *http.Request) {
			r.Header.Set("Rebuno-Dispatch-Id", stale.Header.Get("Rebuno-Dispatch-Id"))
			r.Header.Set("Rebuno-Dispatch-Attempt", stale.Header.Get("Rebuno-Dispatch-Attempt"))
		})
		if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "lease_superseded") {
			t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
		}
	})
}

func readSSEData(t *testing.T, r io.Reader) string {
	t.Helper()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatal("no SSE data frame received")
	return ""
}
