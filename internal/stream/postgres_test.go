package stream

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() {
		t.Skip("database integration test")
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPostgresBusPublishDeliver(t *testing.T) {
	pool := testPool(t)
	bus := NewPostgresBus(pool, nil)

	got := make(chan Delta, 1)
	exec := uuid.New()
	deliver := func(id uuid.UUID, d Delta) {
		if id == exec {
			select {
			case got <- d:
			default:
			}
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = bus.Start(ctx, deliver) }()
	time.Sleep(200 * time.Millisecond) // let LISTEN establish

	want := Delta{StepID: "s1", Seq: 3, Data: "chunk"}
	if err := bus.Publish(ctx, exec, want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case d := <-got:
		if d != want {
			t.Fatalf("got %+v want %+v", d, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no delivery via LISTEN/NOTIFY")
	}
}
