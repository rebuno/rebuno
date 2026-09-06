package policy

import (
	"errors"
	"sync"
	"testing"
)

func TestBundleCacheCompilesOnceForSameBundle(t *testing.T) {
	c := newBundleCache(8)
	calls := 0
	compile := func(b string) (*RuleEngine, error) {
		calls++
		return &RuleEngine{}, nil
	}

	first, err := c.getOrCompile("agent-1", "bundle-v1", compile)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.getOrCompile("agent-1", "bundle-v1", compile)
	if err != nil {
		t.Fatal(err)
	}

	if calls != 1 {
		t.Fatalf("expected one compile for an unchanged bundle, got %d", calls)
	}
	if first != second {
		t.Fatal("expected the cached engine instance to be reused")
	}
}

func TestBundleCacheRecompilesWhenBundleChanges(t *testing.T) {
	c := newBundleCache(8)
	calls := 0
	compile := func(b string) (*RuleEngine, error) {
		calls++
		return &RuleEngine{}, nil
	}

	if _, err := c.getOrCompile("agent-1", "bundle-v1", compile); err != nil {
		t.Fatal(err)
	}
	if _, err := c.getOrCompile("agent-1", "bundle-v2", compile); err != nil {
		t.Fatal(err)
	}

	if calls != 2 {
		t.Fatalf("expected a recompile when the bundle content changes, got %d compiles", calls)
	}
}

func TestBundleCacheEvictsLeastRecentlyUsed(t *testing.T) {
	c := newBundleCache(2)
	compiles := map[string]int{}
	compile := func(agent string) func(string) (*RuleEngine, error) {
		return func(b string) (*RuleEngine, error) {
			compiles[agent]++
			return &RuleEngine{}, nil
		}
	}

	_, _ = c.getOrCompile("agent-1", "b", compile("agent-1"))
	_, _ = c.getOrCompile("agent-2", "b", compile("agent-2"))
	_, _ = c.getOrCompile("agent-1", "b", compile("agent-1")) // hit, promotes agent-1
	_, _ = c.getOrCompile("agent-3", "b", compile("agent-3")) // evicts agent-2

	_, _ = c.getOrCompile("agent-2", "b", compile("agent-2"))
	if compiles["agent-2"] != 2 {
		t.Fatalf("expected agent-2 to be evicted and recompiled, got %d compiles", compiles["agent-2"])
	}
	if compiles["agent-1"] != 1 {
		t.Fatalf("expected agent-1 to stay cached, got %d compiles", compiles["agent-1"])
	}
}

func TestBundleCacheDoesNotCacheCompileErrors(t *testing.T) {
	c := newBundleCache(8)
	calls := 0
	failing := func(b string) (*RuleEngine, error) {
		calls++
		return nil, errors.New("compile failed")
	}

	if _, err := c.getOrCompile("agent-1", "bundle-v1", failing); err == nil {
		t.Fatal("expected the compile error to propagate")
	}
	if _, err := c.getOrCompile("agent-1", "bundle-v1", failing); err == nil {
		t.Fatal("expected the compile error to propagate again")
	}
	if calls != 2 {
		t.Fatalf("expected the error path to recompile, got %d compiles", calls)
	}
}

func TestBundleCacheConcurrentAccess(t *testing.T) {
	c := newBundleCache(2)
	compile := func(b string) (*RuleEngine, error) { return &RuleEngine{}, nil }

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			agent := "agent-" + string(rune('a'+n%5))
			for j := 0; j < 100; j++ {
				if _, err := c.getOrCompile(agent, "bundle", compile); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
}
