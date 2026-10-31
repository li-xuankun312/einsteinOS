package vikingdb

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"sync"
)

type Engine struct {
	mu          sync.RWMutex
	collections map[string]*Collection
	dim         int
	embedFn     func(text string) (Vector, error)
}

func NewEngine(dim int) *Engine {
	return &Engine{
		collections: make(map[string]*Collection),
		dim:         dim,
	}
}

func (e *Engine) SetEmbedFunc(fn func(string) (Vector, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.embedFn = fn
}

func (e *Engine) CreateCollection(name string) *Collection {
	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.collections[name]; ok {
		return c
	}
	c := NewCollection(name, e.dim)
	e.collections[name] = c
	return c
}

func (e *Engine) GetCollection(name string) *Collection {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.collections[name]
}

func (e *Engine) DropCollection(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.collections, name)
}

func (e *Engine) ListCollections() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	names := make([]string, 0, len(e.collections))
	for name := range e.collections {
		names = append(names, name)
	}
	return names
}

func (e *Engine) Embed(text string) (Vector, error) {
	e.mu.RLock()
	fn := e.embedFn
	e.mu.RUnlock()
	if fn != nil {
		return fn(text)
	}
	return HashEmbed(text, e.dim), nil
}

func (e *Engine) Stats() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	stats := map[string]interface{}{
		"collections": len(e.collections),
		"dim":         e.dim,
	}
	totalEvents := 0
	totalEntities := 0
	for _, c := range e.collections {
		totalEvents += c.EventCount()
		totalEntities += c.EntityCount()
	}
	stats["total_events"] = totalEvents
	stats["total_entities"] = totalEntities
	return stats
}

func HashEmbed(text string, dim int) Vector {
	vec := make(Vector, dim)
	text = strings.ToLower(strings.TrimSpace(text))
	words := strings.Fields(text)

	for wi, word := range words {
		h := sha256.Sum256([]byte(word))
		for i := 0; i < dim && i*4+3 < len(h); i++ {
			bits := binary.LittleEndian.Uint32(h[i*4 : i*4+4])
			val := float32(bits) / float32(math.MaxUint32)
			val = val*2 - 1
			posWeight := 1.0 / float32(1+wi)
			vec[i] += val * posWeight
		}

		if len(word) >= 3 {
			for j := 0; j <= len(word)-3; j++ {
				tri := word[j : j+3]
				th := sha256.Sum256([]byte(tri))
				idx := int(binary.LittleEndian.Uint32(th[:4])) % dim
				sign := float32(1)
				if th[4]&1 == 1 {
					sign = -1
				}
				vec[idx] += sign * 0.3
			}
		}
	}

	return Normalize(vec)
}

func (e *Engine) Summary() string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[VikingDB] dim=%d collections=%d\n", e.dim, len(e.collections)))
	for name, c := range e.collections {
		sb.WriteString(fmt.Sprintf("  %s: %d events, %d entities, %d files\n",
			name, c.EventCount(), c.EntityCount(), c.FileCount()))
	}
	return sb.String()
}
