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

	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/store/memstore"
	"github.com/rebuno/rebuno/internal/stream"
)

// setupStreamRouterFixtures builds a memstore-backed *api.KernelAPI adapter
// with the shared test agent registered and one execution created, mirroring
// setupRouter in router_test.go.
func setupStreamRouterFixtures(t *testing.T) (*api.KernelAPI, string) {
	t.Helper()
	ms := memstore.NewStore()
	k := kernel.New(kernel.DefaultConfig(), kernel.Deps{
		Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms, Policy: policy.NewBundleResolver(ms, policy.PermissiveEngine{}),
	})
	ctx := context.Background()
	if err := k.RegisterAgent(ctx, domain.Agent{ID: testAgentID, WebhookURL: "http://localhost", Secret: testAgentSecret}); err != nil {
		t.Fatal(err)
	}
	adapt := &api.KernelAPI{Inner: k}
	exec, err := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`), "")
	if err != nil {
		t.Fatal(err)
	}
	return adapt, exec.ID.String()
}

func TestStreamEndToEnd(t *testing.T) {
	adapt, execID := setupStreamRouterFixtures(t)

	hub := stream.NewHub(stream.NewMemoryBus())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = hub.Start(ctx) }()
	time.Sleep(10 * time.Millisecond)

	mux := api.NewRouter(adapt, adapt, adapt, "", hub, nil)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Open the SSE consumer stream. Bound it with a timeout so a broken
	// producer path fails the test instead of hanging it.
	reqCtx, reqCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reqCancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL+"/v0/executions/"+execID+"/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	// Push a delta via the producer endpoint (HMAC-signed like other agent calls).
	body, _ := json.Marshal(map[string]any{"seq": 7, "data": "hello world"})
	preq := httptest.NewRequest(http.MethodPost, "/v0/executions/"+execID+"/steps/step-abc/stream", bytes.NewReader(body))
	signAgentRequest(preq, body) // from router_test.go
	prr := httptest.NewRecorder()
	mux.ServeHTTP(prr, preq)
	if prr.Code != http.StatusNoContent {
		t.Fatalf("producer status = %d body=%s", prr.Code, prr.Body.String())
	}

	// Read the SSE frame.
	got := readSSEData(t, resp.Body)
	var d stream.Delta
	if err := json.Unmarshal([]byte(got), &d); err != nil {
		t.Fatalf("decode frame %q: %v", got, err)
	}
	if d.StepID != "step-abc" || d.Seq != 7 || d.Data != "hello world" {
		t.Fatalf("unexpected delta: %+v", d)
	}
}

// readSSEData reads until the first "data: " line and returns its payload.
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
