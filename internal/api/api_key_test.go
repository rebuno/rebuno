package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
)

func keyRequest(mux http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestAPIKeyLifecycle(t *testing.T) {
	adapt, k := setupKernel(t)
	mux := api.NewRouter(adapt, adapt, adapt, "bootstrap", nil, nil)
	rr := keyRequest(mux, "POST", "/v0/api-keys", "bootstrap", `{"name":"reader","scopes":["executions:read"]}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var issued kernel.IssuedAPIKey
	if err := json.Unmarshal(rr.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing token or cache protection")
	}
	stored, err := k.Deps().APIKeys.GetAPIKey(context.Background(), issued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.SecretHash) != 32 || bytes.Contains(stored.SecretHash, []byte(issued.Token)) {
		t.Fatal("invalid stored hash")
	}
	if rr = keyRequest(mux, "GET", "/v0/executions", issued.Token, ""); rr.Code != 200 {
		t.Fatalf("read: %d", rr.Code)
	}
	if rr = keyRequest(mux, "POST", "/v0/api-keys", issued.Token, `{"name":"escalated","scopes":["api_keys:manage"]}`); rr.Code != 403 {
		t.Fatalf("escalation: %d", rr.Code)
	}
	rr = keyRequest(mux, "GET", "/v0/api-keys", "bootstrap", "")
	if rr.Code != 200 || strings.Contains(rr.Body.String(), issued.Token) || strings.Contains(rr.Body.String(), "secret_hash") || strings.Contains(rr.Body.String(), `"token"`) {
		t.Fatalf("unsafe listing: %d", rr.Code)
	}
	rr = keyRequest(mux, "POST", "/v0/api-keys/"+issued.ID+"/rotate", "bootstrap", "")
	if rr.Code != 200 {
		t.Fatalf("rotate: %d %s", rr.Code, rr.Body.String())
	}
	var rotated kernel.IssuedAPIKey
	if err := json.Unmarshal(rr.Body.Bytes(), &rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Token == issued.Token || rotated.ID != issued.ID {
		t.Fatal("invalid rotation")
	}
	if rr = keyRequest(mux, "GET", "/v0/executions", issued.Token, ""); rr.Code != 401 {
		t.Fatalf("old token: %d", rr.Code)
	}
	if rr = keyRequest(mux, "GET", "/v0/executions", rotated.Token, ""); rr.Code != 200 {
		t.Fatalf("rotated token: %d", rr.Code)
	}
	for range 2 {
		if rr = keyRequest(mux, "DELETE", "/v0/api-keys/"+issued.ID, "bootstrap", ""); rr.Code != 204 {
			t.Fatalf("revoke: %d", rr.Code)
		}
	}
	if rr = keyRequest(mux, "GET", "/v0/executions", rotated.Token, ""); rr.Code != 401 {
		t.Fatalf("revoked token: %d", rr.Code)
	}
	if rr = keyRequest(mux, "POST", "/v0/api-keys/"+issued.ID+"/rotate", "bootstrap", ""); rr.Code != 409 {
		t.Fatalf("revive revoked key: %d", rr.Code)
	}
	for _, bad := range []string{"BOOTSTRAP", "rbk_bad.secret", rotated.Token + "x", ""} {
		if rr = keyRequest(mux, "GET", "/v0/executions", bad, ""); rr.Code != 401 {
			t.Fatalf("invalid credential accepted: %d", rr.Code)
		}
	}
}

func TestAPIKeyScopes(t *testing.T) {
	adapt, k := setupKernel(t)
	mux := api.NewRouter(adapt, adapt, adapt, "bootstrap", nil, nil)
	scopes := []domain.Scope{domain.ScopeExecutionsRead, domain.ScopeExecutionsWrite, domain.ScopeAgentsRead, domain.ScopeAgentsWrite, domain.ScopeApprovalsRead, domain.ScopeApprovalsWrite, domain.ScopePoliciesWrite, domain.ScopeAPIKeysManage}
	routes := []struct {
		method, path string
		scope        domain.Scope
	}{
		{"GET", "/v0/executions", domain.ScopeExecutionsRead},
		{"GET", "/v0/executions/invalid", domain.ScopeExecutionsRead},
		{"GET", "/v0/executions/invalid/steps", domain.ScopeExecutionsRead},
		{"GET", "/v0/executions/invalid/steps/invalid", domain.ScopeExecutionsRead},
		{"GET", "/v0/executions/invalid/events", domain.ScopeExecutionsRead},
		{"GET", "/v0/executions/invalid/stream", domain.ScopeExecutionsRead},
		{"POST", "/v0/executions", domain.ScopeExecutionsWrite},
		{"POST", "/v0/executions/invalid/cancel", domain.ScopeExecutionsWrite},
		{"GET", "/v0/agents", domain.ScopeAgentsRead},
		{"GET", "/v0/agents/missing", domain.ScopeAgentsRead},
		{"POST", "/v0/agents", domain.ScopeAgentsWrite},
		{"DELETE", "/v0/agents/missing", domain.ScopeAgentsWrite},
		{"GET", "/v0/approvals", domain.ScopeApprovalsRead},
		{"GET", "/v0/approvals/invalid", domain.ScopeApprovalsRead},
		{"POST", "/v0/approvals/invalid/grant", domain.ScopeApprovalsWrite},
		{"POST", "/v0/approvals/invalid/deny", domain.ScopeApprovalsWrite},
		{"POST", "/v0/policies/missing", domain.ScopePoliciesWrite},
		{"GET", "/v0/api-keys", domain.ScopeAPIKeysManage},
		{"POST", "/v0/api-keys", domain.ScopeAPIKeysManage},
		{"DELETE", "/v0/api-keys/missing", domain.ScopeAPIKeysManage},
		{"POST", "/v0/api-keys/missing/rotate", domain.ScopeAPIKeysManage},
	}
	for _, scope := range scopes {
		t.Run(string(scope), func(t *testing.T) {
			issued, err := k.CreateAPIKey(context.Background(), kernel.CreateAPIKeyRequest{Name: "test", Scopes: []domain.Scope{scope}})
			if err != nil {
				t.Fatal(err)
			}
			for _, route := range routes {
				rr := keyRequest(mux, route.method, route.path, issued.Token, "")
				if scope != route.scope {
					if rr.Code != 403 {
						t.Errorf("%s %s: got %d want 403", route.method, route.path, rr.Code)
					}
				} else if rr.Code == 403 || rr.Code == 401 || rr.Code >= 500 {
					t.Errorf("%s %s: authorized request failed %d", route.method, route.path, rr.Code)
				}
			}
			// A client key never substitutes for an agent's HMAC signature.
			if rr := keyRequest(mux, "POST", "/v0/executions/invalid/heartbeat", issued.Token, ""); rr.Code != 401 {
				t.Errorf("agent route: %d", rr.Code)
			}
			if rr := keyRequest(mux, "POST", "/v0/policies/missing/test", issued.Token, ""); rr.Code != 403 {
				t.Errorf("policy replay must require both scopes: %d", rr.Code)
			}
		})
	}
	issued, err := k.CreateAPIKey(context.Background(), kernel.CreateAPIKeyRequest{Name: "policy-test", Scopes: []domain.Scope{domain.ScopePoliciesWrite, domain.ScopeExecutionsRead}})
	if err != nil {
		t.Fatal(err)
	}
	if rr := keyRequest(mux, "POST", "/v0/policies/missing/test", issued.Token, ""); rr.Code != 400 {
		t.Errorf("policy test combined scopes: %d", rr.Code)
	}
}

func TestAPIKeyValidationAndDevAuthentication(t *testing.T) {
	adapt, _ := setupKernel(t)
	mux := api.NewRouter(adapt, adapt, adapt, "", nil, nil)
	for _, body := range []string{`{}`, `{"name":"test","scopes":[]}`, `{"name":"test","scopes":["*"]}`, `{"name":"test","scopes":["executions:read","unknown"]}`} {
		if rr := keyRequest(mux, "POST", "/v0/api-keys", "", body); rr.Code != 400 {
			t.Fatalf("invalid key request: %d", rr.Code)
		}
	}
	if rr := keyRequest(mux, "GET", "/v0/executions", "invalid", ""); rr.Code != 401 {
		t.Fatalf("dev ignores supplied credentials: %d", rr.Code)
	}
	if rr := keyRequest(mux, "GET", "/v0/executions", "", ""); rr.Code != 200 {
		t.Fatalf("dev anonymous: %d", rr.Code)
	}
}
