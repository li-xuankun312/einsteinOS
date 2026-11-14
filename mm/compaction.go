package mm

import (
	"fmt"
	"sync"
)

type CompactionStrategy int

const (
	COMPACT_TAIL     CompactionStrategy = 0
	COMPACT_SUMMARY  CompactionStrategy = 1
	COMPACT_PRIORITY CompactionStrategy = 2
)

type CompactionResult struct {
	PagesFreed   int
	TokensBefore int64
	TokensAfter  int64
	Strategy     CompactionStrategy
}

type ContextCompactor struct {
	mu        sync.Mutex
	threshold int
	strategy  CompactionStrategy
	history   []CompactionResult
}

var GlobalCompactor = &ContextCompactor{
	threshold: 80,
	strategy:  COMPACT_TAIL,
}

func (c *ContextCompactor) SetThreshold(pct int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pct > 0 && pct < 100 {
		c.threshold = pct
	}
}

func (c *ContextCompactor) SetStrategy(s CompactionStrategy) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.strategy = s
}

func (c *ContextCompactor) NeedsCompaction(used, limit int64) bool {
	if limit <= 0 {
		return false
	}
	c.mu.Lock()
	thresh := c.threshold
	c.mu.Unlock()
	return (used * 100 / limit) >= int64(thresh)
}

type CompactableContext interface {
	TotalTokens() int64
	Len() int
	Compact(keepLast int) int
	Read(idx int) string
	Write(idx int, content string)
}

func (c *ContextCompactor) DoCompaction(ctx CompactableContext, limit int64) CompactionResult {
	c.mu.Lock()
	strategy := c.strategy
	c.mu.Unlock()

	before := ctx.TotalTokens()
	pages := ctx.Len()

	var freed int
	switch strategy {
	case COMPACT_TAIL:
		keep := pages / 2
		if keep < 2 {
			keep = 2
		}
		freed = ctx.Compact(keep)

	case COMPACT_SUMMARY:
		if pages > 4 {
			var summary string
			for i := 0; i < pages-2; i++ {
				content := ctx.Read(i)
				if len(content) > 200 {
					summary += content[:200] + "...\n"
				} else {
					summary += content + "\n"
				}
			}
			freed = ctx.Compact(3)
			ctx.Write(0, fmt.Sprintf("[compacted %d pages]\n%s", pages-3, summary))
		}

	case COMPACT_PRIORITY:
		if pages > 6 {
			keep := pages * 40 / 100
			if keep < 3 {
				keep = 3
			}
			freed = ctx.Compact(keep)
		}
	}

	after := ctx.TotalTokens()
	result := CompactionResult{
		PagesFreed:   freed,
		TokensBefore: before,
		TokensAfter:  after,
		Strategy:     strategy,
	}

	c.mu.Lock()
	c.history = append(c.history, result)
	c.mu.Unlock()

	return result
}

func (c *ContextCompactor) History() []CompactionResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CompactionResult, len(c.history))
	copy(out, c.history)
	return out
}

func (c *ContextCompactor) Stats() (total int, totalFreed int, totalSaved int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.history {
		total++
		totalFreed += r.PagesFreed
		totalSaved += r.TokensBefore - r.TokensAfter
	}
	return
}

func StrategyName(s CompactionStrategy) string {
	switch s {
	case COMPACT_TAIL:
		return "tail"
	case COMPACT_SUMMARY:
		return "summary"
	case COMPACT_PRIORITY:
		return "priority"
	default:
		return "unknown"
	}
}
