package memory

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/rebuno/rebuno/internal/domain"
)

func TestJobQueueEnqueueAndAll(t *testing.T) {
	q := NewJobQueue()
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

func TestJobQueueDequeueForTool(t *testing.T) {
	q := NewJobQueue()
	ctx := context.Background()

	j1 := domain.Job{ID: uuid.New(), ToolID: "tool-a"}
	j2 := domain.Job{ID: uuid.New(), ToolID: "tool-b"}
	j3 := domain.Job{ID: uuid.New(), ToolID: "tool-a"}

	if err := q.Enqueue(ctx, j1); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, j2); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, j3); err != nil {
		t.Fatal(err)
	}

	got, err := q.DequeueForTool(ctx, "tool-a")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != j1.ID {
		t.Fatalf("expected first tool-a job, got %v", got)
	}

	// Second dequeue should return j3
	got, err = q.DequeueForTool(ctx, "tool-a")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != j3.ID {
		t.Fatalf("expected second tool-a job, got %v", got)
	}

	// No more tool-a jobs
	got, err = q.DequeueForTool(ctx, "tool-a")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %v", got)
	}

	// tool-b still there
	jobs, _ := q.All(ctx)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 remaining job, got %d", len(jobs))
	}
}

func TestJobQueueRemove(t *testing.T) {
	q := NewJobQueue()
	ctx := context.Background()

	j1 := domain.Job{ID: uuid.New(), ToolID: "tool-a"}
	j2 := domain.Job{ID: uuid.New(), ToolID: "tool-a"}

	if err := q.Enqueue(ctx, j1); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, j2); err != nil {
		t.Fatal(err)
	}

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

	// Removing nonexistent ID is a no-op
	if err := q.Remove(ctx, uuid.New()); err != nil {
		t.Fatal(err)
	}
}
