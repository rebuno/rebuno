package policy

import (
	"container/list"
	"sync"
)

const defaultBundleCacheSize = 1024

type bundleCache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List // front = most recently used
	m   map[string]*list.Element
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

func (c *bundleCache) getOrCompile(agentID, bundle string, compile func(string) (*RuleEngine, error)) (*RuleEngine, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.m[agentID]; ok {
		ent := el.Value.(*cacheEntry)
		if ent.bundle == bundle {
			c.ll.MoveToFront(el)
			return ent.engine, nil
		}
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
