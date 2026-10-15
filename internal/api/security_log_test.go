package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/rebuno/rebuno/internal/api"
	"github.com/rebuno/rebuno/internal/auth"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/kernel"
	"github.com/rebuno/rebuno/internal/policy"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

func TestSecurityLogRecordsActionsAndDenials(t *testing.T) {
	var buf bytes.Buffer
	ms := memstore.NewStore()
	k := kernel.New(kernel.DefaultConfig(), kernel.Deps{
		APIKeys: ms, Events: ms, Steps: ms, Executions: ms, Agents: ms, Approvals: ms, Queue: ms, Locker: ms, UnitOfWork: ms,
		Policy:      policy.NewBundleResolver(ms, policy.PermissiveEngine{}, nil),
		SecurityLog: slog.New(slog.NewJSONHandler(&buf, nil)),
	})
	if err := k.RegisterAgent(auth.WithAdmin(context.Background()), domain.Agent{ID: testAgentID, WebhookURL: "http://localhost", Secret: testAgentSecret}); err != nil {
		t.Fatal(err)
	}
	adapt := &api.KernelAPI{Inner: k}
	mux := api.NewRouter(adapt, adapt, adapt, "bootstrap", nil, nil, nil)

	const bundle = "default_action: allow\nrules: []\n"
	body, _ := json.Marshal(map[string]string{"bundle": bundle})
	if rr := keyRequest(mux, "POST", "/v0/policies/"+testAgentID, "bootstrap", string(body)); rr.Code != 204 {
		t.Fatalf("load policy: %d", rr.Code)
	}
	rr := keyRequest(mux, "POST", "/v0/api-keys", "bootstrap", `{"name":"reader","scopes":["executions:read"]}`)
	var issued kernel.IssuedAPIKey
	if err := json.Unmarshal(rr.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if rr := keyRequest(mux, "POST", "/v0/api-keys", issued.Token, `{"name":"x","scopes":["executions:read"]}`); rr.Code != 403 {
		t.Fatalf("escalation: %d", rr.Code)
	}
	if rr := keyRequest(mux, "GET", "/v0/executions", "rbk_bad.secret", ""); rr.Code != 401 {
		t.Fatalf("bad token: %d", rr.Code)
	}

	var entries []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var e map[string]any
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("security log line is not JSON: %s", line)
		}
		entries = append(entries, e)
	}
	find := func(action, principal string) map[string]any {
		t.Helper()
		for _, e := range entries {
			if e["msg"] == action && e["principal"] == principal {
				return e
			}
		}
		t.Fatalf("no %s entry for %s in %v", action, principal, entries)
		return nil
	}

	sum := sha256.Sum256([]byte(bundle))
	if e := find("policy.load", "admin"); e["agent"] != testAgentID || e["bundle_sha256"] != hex.EncodeToString(sum[:]) {
		t.Errorf("policy.load: %v", e)
	}
	if e := find("api_key.create", "admin"); e["key_id"] != issued.ID {
		t.Errorf("api_key.create: %v", e)
	}
	if e := find("request.denied", "service:"+issued.ID); e["status"] != float64(403) || e["path"] != "/v0/api-keys" {
		t.Errorf("scope denial: %v", e)
	}
	if e := find("request.denied", "anonymous"); e["status"] != float64(401) {
		t.Errorf("bad token: %v", e)
	}
}
