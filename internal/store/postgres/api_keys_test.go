package postgres

import (
	"bytes"
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rebuno/rebuno/internal/domain"
	"github.com/rebuno/rebuno/internal/store"
	"github.com/rebuno/rebuno/internal/store/memstore"
)

func TestAPIKeyBackends(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testAPIKeyStore(t, memstore.NewStore()) })
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
		testAPIKeyStore(t, NewStore(pool))
	})
}

func testAPIKeyStore(t *testing.T, s store.APIKeyStore) {
	t.Helper()
	ctx := context.Background()
	original := bytes.Repeat([]byte{1}, 32)
	key := domain.APIKey{ID: uuid.NewString(), Name: "concurrent", Scopes: []domain.Scope{domain.ScopeExecutionsRead}, SecretHash: original, CreatedAt: time.Now().UTC()}
	if err := s.CreateAPIKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAPIKey(ctx, key); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	// Readers cannot mutate stored scopes or secrets through shared slices.
	got, err := s.GetAPIKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.SecretHash[0] = 9
	got.Scopes[0] = domain.ScopeAPIKeysManage
	got, err = s.GetAPIKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.SecretHash, original) || got.Scopes[0] != domain.ScopeExecutionsRead {
		t.Fatal("stored credential changed through reader")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, b := range []byte{2, 3} {
		wg.Go(func() {
			results <- s.RotateAPIKey(ctx, key.ID, original, bytes.Repeat([]byte{b}, 32))
		})
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, domain.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("rotation winners %d conflicts %d", wins, conflicts)
	}
	got, err = s.GetAPIKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(got.SecretHash, original) {
		t.Fatal("rotation not persisted")
	}
	// Whichever operation wins the race, revocation remains permanent.
	wg.Go(func() {
		if err := s.RevokeAPIKey(ctx, key.ID, time.Now().UTC()); err != nil {
			t.Error(err)
		}
	})
	wg.Go(func() {
		if err := s.RotateAPIKey(ctx, key.ID, got.SecretHash, bytes.Repeat([]byte{4}, 32)); err != nil && !errors.Is(err, domain.ErrConflict) {
			t.Error(err)
		}
	})
	wg.Wait()
	got, err = s.GetAPIKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RevokedAt == nil {
		t.Fatal("rotation revived revoked key")
	}
	if err := s.RotateAPIKey(ctx, key.ID, got.SecretHash, original); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("revoked rotation: %v", err)
	}
	revoked := *got.RevokedAt
	if err := s.RevokeAPIKey(ctx, key.ID, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAPIKey(ctx, key.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RevokedAt.Equal(revoked) {
		t.Fatal("repeated revocation changed timestamp")
	}
	list, err := s.ListAPIKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, k := range list {
		if k.ID == key.ID {
			found = true
			if k.RevokedAt == nil {
				t.Fatal("listing hides revocation")
			}
		}
	}
	if !found {
		t.Fatal("missing listed key")
	}
	if err := s.RevokeAPIKey(ctx, "missing", time.Now().UTC()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing revoke: %v", err)
	}
	if err := s.RotateAPIKey(ctx, "missing", original, original); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing rotate: %v", err)
	}
}
