package row

import (
	"container/list"
	"sync"
)

// lruShard is one stripe of the compiled-statement cache: a map for lookup and
// a list for recency, guarded by its own mutex.
type lruShard struct {
	mu    sync.Mutex
	max   int
	items map[string]*list.Element
	order *list.List // front is most recently used
}

type lruEntry struct {
	key string
	val *plan
}

// planCache memoises compiled statements. Compilation is cheap but not free,
// and applications tend to issue the same few dozen statements forever, so the
// cache turns per-query parsing into a hash lookup.
//
// It is striped because it sits on the hot path of every query; a single mutex
// would serialise otherwise independent goroutines.
type planCache struct {
	shards [16]lruShard
}

func newPlanCache(max int) *planCache {
	c := &planCache{}
	per := max / len(c.shards)
	if per < 1 {
		per = 1
	}
	for i := range c.shards {
		c.shards[i] = lruShard{
			max:   per,
			items: make(map[string]*list.Element, per),
			order: list.New(),
		}
	}
	return c
}

// shardFor picks a stripe with FNV-1a, which is short, allocation-free and
// spreads SQL text well enough for this purpose.
func (c *planCache) shardFor(key string) *lruShard {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return &c.shards[h%uint32(len(c.shards))]
}

func (c *planCache) get(key string) (*plan, bool) {
	s := c.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.items[key]
	if !ok {
		return nil, false
	}
	s.order.MoveToFront(el)
	return el.Value.(*lruEntry).val, true
}

func (c *planCache) put(key string, p *plan) {
	s := c.shardFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[key]; ok {
		s.order.MoveToFront(el)
		el.Value.(*lruEntry).val = p
		return
	}
	el := s.order.PushFront(&lruEntry{key: key, val: p})
	s.items[key] = el
	for s.order.Len() > s.max {
		last := s.order.Back()
		if last == nil {
			break
		}
		s.order.Remove(last)
		delete(s.items, last.Value.(*lruEntry).key)
	}
}

// len reports the total number of cached plans. Used by tests.
func (c *planCache) len() int {
	n := 0
	for i := range c.shards {
		c.shards[i].mu.Lock()
		n += c.shards[i].order.Len()
		c.shards[i].mu.Unlock()
	}
	return n
}
