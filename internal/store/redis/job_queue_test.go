//go:build redis

package redis

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

func testClient(t *testing.T) *JobQueue {
	t.Helper()
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379"
	}
	ctx := context.Background()
	client, err := NewClient(ctx, url)
	if err != nil {
		t.Skipf("skipping redis test: %v", err)
	}
	t.Cleanup(func() {
		client.FlushDB(ctx)
		client.Close()
	})
	return NewJobQueue(client)
}

func TestRedisJobQueueEnqueueAndAll(t *testing.T) {
	q := testClient(t)
	ctx := context.Background()

	j1 := domain.Job{ID: uuid.New(), ToolID: "tool-a"}
	j2 := domain.Job{ID: uuid.New(), ToolID: "tool-b"}

	if err := q.Enqueue(ctx, j1); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, j2); err != nil {
		t.Fatal(err)
	}

	jobs, err := q.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
}

func TestRedisJobQueueDequeueForTool(t *testing.T) {
	q := testClient(t)
	ctx := context.Background()

	j1 := domain.Job{ID: uuid.New(), ToolID: "tool-a"}
	j2 := domain.Job{ID: uuid.New(), ToolID: "tool-b"}
	j3 := domain.Job{ID: uuid.New(), ToolID: "tool-a"}

	q.Enqueue(ctx, j1)
	q.Enqueue(ctx, j2)
	q.Enqueue(ctx, j3)

	got, err := q.DequeueForTool(ctx, "tool-a")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != j1.ID {
		t.Fatalf("expected first tool-a job, got %v", got)
	}

	got, err = q.DequeueForTool(ctx, "tool-a")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != j3.ID {
		t.Fatalf("expected second tool-a job, got %v", got)
	}

	got, err = q.DequeueForTool(ctx, "tool-a")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestRedisJobQueueRemove(t *testing.T) {
	q := testClient(t)
	ctx := context.Background()

	j1 := domain.Job{ID: uuid.New(), ToolID: "tool-a"}
	j2 := domain.Job{ID: uuid.New(), ToolID: "tool-a"}

	q.Enqueue(ctx, j1)
	q.Enqueue(ctx, j2)

	if err := q.Remove(ctx, j1.ID); err != nil {
		t.Fatal(err)
	}

	jobs, _ := q.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job after remove, got %d", len(jobs))
	}
	if jobs[0].ID != j2.ID {
		t.Fatalf("expected j2 to remain, got %v", jobs[0].ID)
	}
}
