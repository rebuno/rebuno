package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestClientSendsBearerFromEnv(t *testing.T) {
	t.Setenv("REBUNO_API_KEY", "sekrit")
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := newClient(srv.URL).do(context.Background(), http.MethodPost, "/v0/agents", map[string]string{"id": "a"}, nil); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer sekrit" {
		t.Errorf("expected bearer header, got %q", got)
	}
}

func TestClientSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(domain.APIError{Code: "not_found", Message: "agent not found"})
	}))
	defer srv.Close()

	var out domain.Agent
	err := newClient(srv.URL).do(context.Background(), http.MethodGet, "/v0/agents/nope", nil, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "agent not found") {
		t.Errorf("expected the kernel's message, got %q", err)
	}
}

// A non-2xx with a body that is not an APIError must still fail, rather than
// decoding the garbage into the caller's output.
func TestClientFailsOnUnparsableErrorBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>gateway</html>"))
	}))
	defer srv.Close()

	var out domain.Agent
	err := newClient(srv.URL).do(context.Background(), http.MethodGet, "/v0/agents", nil, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "502") {
		t.Errorf("expected the status line, got %q", err)
	}
}

func TestResolveExecIDByShortPrefix(t *testing.T) {
	a := uuid.MustParse("a1b2c3d4-0000-4000-8000-000000000001")
	b := uuid.MustParse("a1b2c3d4-0000-4000-8000-000000000002")
	c := uuid.MustParse("ffffffff-0000-4000-8000-000000000003")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(domain.ExecutionPage{Executions: []domain.Execution{{ID: a}, {ID: b}, {ID: c}}})
	}))
	defer srv.Close()
	cl := newClient(srv.URL)

	for _, tc := range []struct {
		name    string
		arg     string
		want    uuid.UUID
		wantErr string
	}{
		{"full uuid", c.String(), c, ""},
		{"unique prefix", "ffff", c, ""},
		{"ambiguous prefix", "a1b2", uuid.Nil, "ambiguous"},
		{"no match", "beef", uuid.Nil, "no execution matching"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveExecID(context.Background(), cl, tc.arg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("expected %s, got %s", tc.want, got)
			}
		})
	}
}
