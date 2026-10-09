package kernel

import (
	"sync"
	"time"
)

type CacheEntry struct {
	Key       string
	Content   string
	Dirty     bool
	RefCount  int
	LastUsed  time.Time
	TokenSize int64
}

type PromptCache struct {
	mu       sync.RWMutex
	entries  map[string]*CacheEntry
	maxSize  int64
	curSize  int64
}

var GlobalPromptCache = &PromptCache{
	entries: make(map[string]*CacheEntry),
	maxSize: 1000000,
}

func (pc *PromptCache) Get(key string) (string, bool) {
	pc.mu.RLock()
	e, ok := pc.entries[key]
	pc.mu.RUnlock()
	if !ok {
		return "", false
	}
	pc.mu.Lock()
	e.RefCount++
	e.LastUsed = time.Now()
	pc.mu.Unlock()
	return e.Content, true
}

func (pc *PromptCache) Put(key, content string, tokens int64) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if e, ok := pc.entries[key]; ok {
		pc.curSize -= e.TokenSize
		e.Content = content
		e.TokenSize = tokens
		e.Dirty = true
		e.RefCount++
		e.LastUsed = time.Now()
		pc.curSize += tokens
		return
	}

	for pc.curSize+tokens > pc.maxSize {
		pc.evictOne()
	}

	pc.entries[key] = &CacheEntry{
		Key:       key,
		Content:   content,
		Dirty:     true,
		RefCount:  1,
		LastUsed:  time.Now(),
		TokenSize: tokens,
	}
	pc.curSize += tokens
}

func (pc *PromptCache) evictOne() {
	var oldest string
	var oldestTime time.Time
	first := true
	for k, e := range pc.entries {
		if e.RefCount > 0 {
			continue
		}
		if first || e.LastUsed.Before(oldestTime) {
			oldest = k
			oldestTime = e.LastUsed
			first = false
		}
	}
	if oldest == "" {
		for k, e := range pc.entries {
			if first || e.LastUsed.Before(oldestTime) {
				oldest = k
				oldestTime = e.LastUsed
				first = false
			}
		}
	}
	if oldest != "" {
		pc.curSize -= pc.entries[oldest].TokenSize
		delete(pc.entries, oldest)
	}
}

func (pc *PromptCache) Release(key string) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if e, ok := pc.entries[key]; ok {
		e.RefCount--
	}
}

func (pc *PromptCache) Sync() []CacheEntry {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	var dirty []CacheEntry
	for _, e := range pc.entries {
		if e.Dirty {
			dirty = append(dirty, *e)
			e.Dirty = false
		}
	}
	return dirty
}

func (pc *PromptCache) Stats() (total, used, dirty int64) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	total = pc.maxSize
	used = pc.curSize
	for _, e := range pc.entries {
		if e.Dirty {
			dirty += e.TokenSize
		}
	}
	return
}
