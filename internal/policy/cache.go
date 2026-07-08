package policy

import (
	"container/list"
	"sync"
)

const defaultBundleCacheSize = 1024

// bundleCache memoizes compiled *RuleEngine instances keyed by agent ID. An
// entry is reused only while the agent's bundle content is unchanged, so a
// bundle update is picked up on the next evaluation without any explicit
// invalidation. It is safe for concurrent use and bounded by an LRU capacity.
type bundleCache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List               // front = most recently used
	m   map[string]*list.Element // agentID -> *list.Element holding a *cacheEntry
}

type cacheEntry struct {
	agentID string
	bundle  string
	engine  *RuleEngine
}

func newBundleCache(capacity int) *bundleCache {
	if capacity <= 0 {
		capacity = defaultBundleCacheSize
	}
	return &bundleCache{
		cap: capacity,
		ll:  list.New(),
		m:   make(map[string]*list.Element, capacity),
	}
}

// getOrCompile returns the cached engine for agentID when its stored bundle
// matches bundle; otherwise it calls compile and caches the result. The lock is
// held across compile so concurrent callers for the same agent don't duplicate
// work; compilation is sub-millisecond, so serializing it is cheaper than the
// coordination a finer-grained scheme would need. Compile errors are not cached.
func (c *bundleCache) getOrCompile(agentID, bundle string, compile func(string) (*RuleEngine, error)) (*RuleEngine, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.m[agentID]; ok {
		ent := el.Value.(*cacheEntry)
		if ent.bundle == bundle {
			c.ll.MoveToFront(el)
			return ent.engine, nil
		}
		// Bundle changed: recompile and replace in place.
		engine, err := compile(bundle)
		if err != nil {
			return nil, err
		}
		ent.bundle = bundle
		ent.engine = engine
		c.ll.MoveToFront(el)
		return engine, nil
	}

	engine, err := compile(bundle)
	if err != nil {
		return nil, err
	}
	el := c.ll.PushFront(&cacheEntry{agentID: agentID, bundle: bundle, engine: engine})
	c.m[agentID] = el
	if c.ll.Len() > c.cap {
		c.evictOldest()
	}
	return engine, nil
}

func (c *bundleCache) evictOldest() {
	el := c.ll.Back()
	if el == nil {
		return
	}
	c.ll.Remove(el)
	delete(c.m, el.Value.(*cacheEntry).agentID)
}
