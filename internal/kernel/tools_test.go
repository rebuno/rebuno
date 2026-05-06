package kernel

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/rebuno/rebuno/internal/domain"
)

func sampleSchemas(ids ...string) []domain.ToolSchema {
	out := make([]domain.ToolSchema, len(ids))
	for i, id := range ids {
		out[i] = domain.ToolSchema{
			ID:          id,
			Description: id + " description",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}
	}
	return out
}

func toolIDs(schemas []domain.ToolSchema) []string {
	out := make([]string, len(schemas))
	for i, s := range schemas {
		out[i] = s.ID
	}
	sort.Strings(out)
	return out
}

func TestToolDirectory_PublishAndList(t *testing.T) {
	d := newToolDirectory(nil)
	d.Publish("runner-1", sampleSchemas("github_create_pr", "github_issue_read"))

	got := toolIDs(d.List(""))
	want := []string{"github_create_pr", "github_issue_read"}
	if !equalSlices(got, want) {
		t.Errorf("List() = %v, want %v", got, want)
	}
}

func TestToolDirectory_PublishReplacesPreviousFromSameRunner(t *testing.T) {
	d := newToolDirectory(nil)
	d.Publish("runner-1", sampleSchemas("github_create_pr", "github_issue_read"))
	d.Publish("runner-1", sampleSchemas("github_create_pr")) // dropped issue_read

	got := toolIDs(d.List(""))
	if !equalSlices(got, []string{"github_create_pr"}) {
		t.Errorf("after re-publish List() = %v, want only [github_create_pr]", got)
	}
}

func TestToolDirectory_DropRunnerRemovesOnlyItsEntries(t *testing.T) {
	d := newToolDirectory(nil)
	d.Publish("runner-1", sampleSchemas("a_one", "a_two"))
	d.Publish("runner-2", sampleSchemas("b_one"))

	d.DropRunner("runner-1")
	got := toolIDs(d.List(""))
	if !equalSlices(got, []string{"b_one"}) {
		t.Errorf("after DropRunner runner-1, got %v, want [b_one]", got)
	}
}

func TestToolDirectory_TwoRunnersSameToolBothVisibleAfterOneDrops(t *testing.T) {
	d := newToolDirectory(nil)
	d.Publish("runner-1", sampleSchemas("github_create_pr"))
	time.Sleep(time.Millisecond) // ensure RegisteredAt differs
	d.Publish("runner-2", sampleSchemas("github_create_pr"))

	d.DropRunner("runner-2")

	got := d.List("")
	if len(got) != 1 || got[0].ID != "github_create_pr" {
		t.Fatalf("after dropping runner-2, expected [github_create_pr] still visible; got %v", got)
	}
	if got[0].RunnerID != "runner-1" {
		t.Errorf("survivor should be runner-1, got %s", got[0].RunnerID)
	}
}

func TestToolDirectory_PrefixFiltering(t *testing.T) {
	d := newToolDirectory(nil)
	d.Publish("runner-1", sampleSchemas(
		"github_create_pr",
		"github_issue_read",
		"compute_heavy_job",
		"githubactions_x", // should NOT match prefix=github
	))

	tests := []struct {
		prefix string
		want   []string
	}{
		{"", []string{"compute_heavy_job", "github_create_pr", "github_issue_read", "githubactions_x"}},
		{"github", []string{"github_create_pr", "github_issue_read"}},
		{"compute", []string{"compute_heavy_job"}},
		{"compute_heavy_job", []string{"compute_heavy_job"}}, // exact match
		{"nonexistent", []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			got := toolIDs(d.List(tt.prefix))
			if !equalSlices(got, tt.want) {
				t.Errorf("List(%q) = %v, want %v", tt.prefix, got, tt.want)
			}
		})
	}
}

func TestToolDirectory_NewestWinsOnConflict(t *testing.T) {
	d := newToolDirectory(nil)

	older := domain.ToolSchema{
		ID:          "x_tool",
		Description: "old version",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"}}}`),
	}
	newer := domain.ToolSchema{
		ID:          "x_tool",
		Description: "new version",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"}}}`),
	}

	d.Publish("runner-1", []domain.ToolSchema{older})
	time.Sleep(time.Millisecond)
	d.Publish("runner-2", []domain.ToolSchema{newer})

	got := d.List("")
	if len(got) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(got))
	}
	if got[0].Description != "new version" {
		t.Errorf("expected newer version to win, got %q", got[0].Description)
	}
	if got[0].RunnerID != "runner-2" {
		t.Errorf("expected runner-2 to win, got %s", got[0].RunnerID)
	}
}

func TestToolDirectory_IdenticalSchemasMergeCleanly(t *testing.T) {
	// HA case: two runners with the same code, identical schemas. Should
	// surface as one entry with no warning. This test doesn't assert on the
	// log output, but verifies the merge path works.
	d := newToolDirectory(nil)

	schema := domain.ToolSchema{
		ID:          "x_tool",
		Description: "same",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	d.Publish("runner-1", []domain.ToolSchema{schema})
	d.Publish("runner-2", []domain.ToolSchema{schema})

	got := d.List("")
	if len(got) != 1 {
		t.Fatalf("expected 1 merged entry, got %d", len(got))
	}
	if got[0].Description != "same" {
		t.Errorf("description corrupted: %q", got[0].Description)
	}
}

func TestToolDirectory_EmptyAfterDropAll(t *testing.T) {
	d := newToolDirectory(nil)
	d.Publish("runner-1", sampleSchemas("a_one"))
	d.Publish("runner-2", sampleSchemas("a_one"))

	d.DropRunner("runner-1")
	d.DropRunner("runner-2")

	got := d.List("")
	if len(got) != 0 {
		t.Errorf("expected empty list, got %v", toolIDs(got))
	}
}

func TestSchemasIdentical(t *testing.T) {
	a := domain.ToolSchema{Description: "x", InputSchema: json.RawMessage(`{"a":1}`)}
	b := domain.ToolSchema{Description: "x", InputSchema: json.RawMessage(`{"a":1}`)}
	c := domain.ToolSchema{Description: "y", InputSchema: json.RawMessage(`{"a":1}`)}

	if !schemasIdentical([]domain.ToolSchema{a, b}) {
		t.Error("identical schemas reported as different")
	}
	if schemasIdentical([]domain.ToolSchema{a, c}) {
		t.Error("different descriptions reported as identical")
	}
}

func TestMatchPrefix(t *testing.T) {
	tests := []struct {
		toolID, prefix string
		want           bool
	}{
		{"github_create_pr", "", true},
		{"github_create_pr", "github", true},
		{"github_create_pr", "github_create_pr", true},
		{"github", "github", true},
		{"githubactions_x", "github", false}, // not a prefix match (no '_' delim)
		{"github.x", "git", false},           // partial-segment, not a prefix
	}
	for _, tt := range tests {
		got := matchPrefix(tt.toolID, tt.prefix)
		if got != tt.want {
			t.Errorf("matchPrefix(%q, %q) = %v, want %v", tt.toolID, tt.prefix, got, tt.want)
		}
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
