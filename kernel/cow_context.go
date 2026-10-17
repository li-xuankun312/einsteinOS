package kernel

import (
	"sync"
	"sync/atomic"
)

type ContextPage struct {
	Content  string
	RefCount atomic.Int32
	Dirty    bool
}

type COWContext struct {
	mu       sync.RWMutex
	pages    []*ContextPage
	shared   []bool
	ownerPid int64
}

var (
	cowContexts  = make(map[int64]*COWContext)
	cowContextMu sync.Mutex
)

func NewCOWContext(pid int64) *COWContext {
	ctx := &COWContext{ownerPid: pid}
	cowContextMu.Lock()
	cowContexts[pid] = ctx
	cowContextMu.Unlock()
	return ctx
}

func GetCOWContext(pid int64) *COWContext {
	cowContextMu.Lock()
	defer cowContextMu.Unlock()
	return cowContexts[pid]
}

func (c *COWContext) Append(content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	page := &ContextPage{Content: content}
	page.RefCount.Store(1)
	c.pages = append(c.pages, page)
	c.shared = append(c.shared, false)
}

func (c *COWContext) ForkTo(childPid int64) *COWContext {
	c.mu.RLock()
	child := &COWContext{ownerPid: childPid}
	child.pages = make([]*ContextPage, len(c.pages))
	child.shared = make([]bool, len(c.pages))
	for i, p := range c.pages {
		p.RefCount.Add(1)
		child.pages[i] = p
		child.shared[i] = true
	}
	c.mu.RUnlock()

	c.mu.Lock()
	for i := range c.shared {
		c.shared[i] = true
	}
	c.mu.Unlock()

	cowContextMu.Lock()
	cowContexts[childPid] = child
	cowContextMu.Unlock()
	return child
}

func (c *COWContext) Write(idx int, content string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if idx < 0 || idx >= len(c.pages) {
		return
	}
	if c.shared[idx] && c.pages[idx].RefCount.Load() > 1 {
		old := c.pages[idx]
		old.RefCount.Add(-1)
		newPage := &ContextPage{Content: content, Dirty: true}
		newPage.RefCount.Store(1)
		c.pages[idx] = newPage
		c.shared[idx] = false
	} else {
		c.pages[idx].Content = content
		c.pages[idx].Dirty = true
	}
}

func (c *COWContext) Read(idx int) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if idx < 0 || idx >= len(c.pages) {
		return ""
	}
	return c.pages[idx].Content
}

func (c *COWContext) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.pages)
}

func (c *COWContext) TotalTokens() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var total int64
	for _, p := range c.pages {
		total += int64(len(p.Content)) / 4
	}
	return total
}

func (c *COWContext) Compact(keepLast int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pages) <= keepLast {
		return 0
	}
	removed := len(c.pages) - keepLast
	for i := 0; i < removed; i++ {
		c.pages[i].RefCount.Add(-1)
	}
	c.pages = c.pages[removed:]
	c.shared = c.shared[removed:]
	return removed
}

func (c *COWContext) Release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.pages {
		p.RefCount.Add(-1)
	}
	c.pages = nil
	c.shared = nil
	cowContextMu.Lock()
	delete(cowContexts, c.ownerPid)
	cowContextMu.Unlock()
}

func (c *COWContext) Snapshot() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, len(c.pages))
	for i, p := range c.pages {
		out[i] = p.Content
	}
	return out
}

func (c *COWContext) SharedPages() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	n := 0
	for _, s := range c.shared {
		if s {
			n++
		}
	}
	return n
}
