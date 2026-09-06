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
	preq := httptest.NewRequest(http.MethodPost, "/v0/executions/"+execID+"/steps/step-abc/stream", bytes.NewReader(body))
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
	if d.StepID != "step-abc" || d.Seq != 7 || d.Data != "hello world" {
		t.Fatalf("unexpected delta: %+v", d)
	}
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
