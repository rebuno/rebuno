package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

func TestAgentMetadataBackends(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testAgentMetadata(t, memstore.NewStore()) })
	t.Run("postgres", func(t *testing.T) {
		if testing.Short() {
			t.Skip("database integration test")
		}
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			t.Skip("DATABASE_URL not set")
		}
		ctx := context.Background()
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		if err := Migrate(ctx, pool); err != nil {
			t.Fatal(err)
		}
		testAgentMetadata(t, NewStore(pool))
	})
}

func testAgentMetadata(t *testing.T, s store.AgentStore) {
	t.Helper()
	ctx := context.Background()
	id := "agent-" + uuid.NewString()
	t.Cleanup(func() { _ = s.DeleteAgent(ctx, id) })

	if err := s.SetAgentMetadata(ctx, id, domain.AgentMetadata{Description: "x"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unregistered agent: %v", err)
	}
	if err := s.RegisterAgent(ctx, domain.Agent{ID: id, WebhookURL: "http://a", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	metadata := domain.AgentMetadata{
		Description:  "Researches a topic.",
		InputSchema:  json.RawMessage(`{"type": "object"}`),
		OutputSchema: json.RawMessage(`{"type": "string"}`),
	}
	if err := s.SetAgentMetadata(ctx, id, metadata); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterAgent(ctx, domain.Agent{ID: id, WebhookURL: "http://b", Secret: "s2"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAgent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.WebhookURL != "http://b" || got.Description != metadata.Description ||
		string(got.InputSchema) != string(metadata.InputSchema) || string(got.OutputSchema) != string(metadata.OutputSchema) {
		t.Fatalf("re-registration: %+v", got)
	}

	if err := s.SetAgentMetadata(ctx, id, domain.AgentMetadata{}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAgent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "" || got.InputSchema != nil || got.OutputSchema != nil {
		t.Fatalf("cleared metadata: %+v", got.AgentMetadata)
	}
}
