package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/dispatcher"
)

func TestAgentReadSignatureCannotBeReusedForAnotherExecution(t *testing.T) {
	mux, k, ctx := setupRouter(t)
	first, err := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := k.CreateExecution(ctx, testAgentID, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	original := httptest.NewRequest("GET", "/v0/executions/"+first.ID.String(), nil)
	signAgentRequest(original, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, original)
	if rr.Code != 200 {
		t.Fatalf("valid read: %d", rr.Code)
	}
	req := httptest.NewRequest("GET", "/v0/executions/"+second.ID.String(), nil)
	req.Header = original.Header.Clone()
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("retargeted read: %d", rr.Code)
	}
	for _, kind := range []string{"old timestamp", "legacy body signature"} {
		t.Run(kind, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v0/executions/"+first.ID.String(), nil)
			signAgentRequest(req, nil)
			switch kind {
			case "old timestamp":
				req.Header.Set(auth.HeaderTimestamp, strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10))
				req.Header.Set("Rebuno-Signature", auth.SignRequest(testAgentSecret, req, nil))
			case "legacy body signature":
				req.Header.Del(auth.HeaderTimestamp)
				req.Header.Set("Rebuno-Signature", "sha256="+dispatcher.SignPayload(testAgentSecret, nil))
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != 401 {
				t.Fatalf("unauthorized read accepted: %d", rr.Code)
			}
		})
	}
}
