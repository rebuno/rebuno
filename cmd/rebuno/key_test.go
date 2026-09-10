package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

func TestKeyCommands(t *testing.T) {
	t.Setenv("REBUNO_API_KEY", "bootstrap")
	for _, tc := range []struct {
		args                   []string
		method, path, response string
	}{
		{[]string{"create", "reader", "--scope", "executions:read", "--scope", "agents:read"}, "POST", "/v0/api-keys", `{"id":"key","token":"issued-secret"}`},
		{[]string{"ls"}, "GET", "/v0/api-keys", `[]`},
		{[]string{"rotate", "key"}, "POST", "/v0/api-keys/key/rotate", `{"id":"key","token":"replacement-secret"}`},
		{[]string{"revoke", "key"}, "DELETE", "/v0/api-keys/key", ``},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer bootstrap" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if tc.args[0] == "create" {
					var req kernel.CreateAPIKeyRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					if req.Name != "reader" || !reflect.DeepEqual(req.Scopes, []domain.Scope{domain.ScopeExecutionsRead, domain.ScopeAgentsRead}) {
						t.Errorf("unexpected create request: %+v", req)
					}
				}
				if tc.response == "" {
					w.WriteHeader(204)
				} else {
					_, _ = w.Write([]byte(tc.response))
				}
			}))
			defer srv.Close()
			prior := kernelBaseURL
			kernelBaseURL = srv.URL
			defer func() { kernelBaseURL = prior }()
			cmd := keyCmd()
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if tc.response != "" && !json.Valid(output.Bytes()) {
				t.Fatalf("invalid JSON output: %s", output.String())
			}
		})
	}
}
