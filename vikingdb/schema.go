package vikingdb

import (
	"time"
)

type Event struct {
	ID        string
	Timestamp time.Time
	ConvID    string
	Goal      string
	Summary   string
	Tool      string
	Command   string
	ExitCode  int
	Output    string
	Files     []string
	Tags      []string
	Embedding Vector
}

type Entity struct {
	ID           string
	Name         string
	Type         string
	Properties   map[string]interface{}
	EventHistory []string
	FirstSeen    time.Time
	LastSeen     time.Time
	Frequency    int
	Embedding    Vector
}

type Collection struct {
	Name       string
	EventIndex *VectorIndex
	EntityIndex *VectorIndex

	events   map[string]*Event
	entities map[string]*Entity

	eventsByConv map[string][]string
	eventsByTool map[string][]string
	eventsByFile map[string][]string
	entitiesByType map[string][]string
}

func NewCollection(name string, dim int) *Collection {
	return &Collection{
		Name:           name,
		EventIndex:     NewVectorIndex(dim),
		EntityIndex:    NewVectorIndex(dim),
		events:         make(map[string]*Event),
		entities:       make(map[string]*Entity),
		eventsByConv:   make(map[string][]string),
		eventsByTool:   make(map[string][]string),
		eventsByFile:   make(map[string][]string),
		entitiesByType: make(map[string][]string),
	}
}

func (c *Collection) InsertEvent(e *Event) {
	c.events[e.ID] = e
	if e.ConvID != "" {
		c.eventsByConv[e.ConvID] = append(c.eventsByConv[e.ConvID], e.ID)
	}
	if e.Tool != "" {
		c.eventsByTool[e.Tool] = append(c.eventsByTool[e.Tool], e.ID)
	}
	for _, f := range e.Files {
		c.eventsByFile[f] = append(c.eventsByFile[f], e.ID)
	}
	if len(e.Embedding) > 0 {
		meta := map[string]interface{}{
			"goal":      e.Goal,
			"tool":      e.Tool,
			"conv_id":   e.ConvID,
			"timestamp": e.Timestamp.Unix(),
			"exit_code": e.ExitCode,
		}
		c.EventIndex.Upsert(e.ID, e.Embedding, meta)
	}
}

func (c *Collection) InsertEntity(e *Entity) {
	existing, ok := c.entities[e.ID]
	if ok {
		existing.LastSeen = e.LastSeen
		existing.Frequency++
		for k, v := range e.Properties {
			existing.Properties[k] = v
		}
		existing.EventHistory = append(existing.EventHistory, e.EventHistory...)
		if len(e.Embedding) > 0 {
			existing.Embedding = e.Embedding
		}
	} else {
		if e.Properties == nil {
			e.Properties = make(map[string]interface{})
		}
		if e.Frequency == 0 {
			e.Frequency = 1
		}
		c.entities[e.ID] = e
		if e.Type != "" {
			c.entitiesByType[e.Type] = append(c.entitiesByType[e.Type], e.ID)
		}
	}
	ent := c.entities[e.ID]
	if len(ent.Embedding) > 0 {
		meta := map[string]interface{}{
			"name":      ent.Name,
			"type":      ent.Type,
			"frequency": ent.Frequency,
		}
		c.EntityIndex.Upsert(ent.ID, ent.Embedding, meta)
	}
}

func (c *Collection) GetEvent(id string) *Event {
	return c.events[id]
}

func (c *Collection) GetEntity(id string) *Entity {
	return c.entities[id]
}

func (c *Collection) EventsByConv(convID string) []*Event {
	ids := c.eventsByConv[convID]
	out := make([]*Event, 0, len(ids))
	for _, id := range ids {
		if e := c.events[id]; e != nil {
			out = append(out, e)
		}
	}
	return out
}

func (c *Collection) EventsByTool(tool string) []*Event {
	ids := c.eventsByTool[tool]
	out := make([]*Event, 0, len(ids))
	for _, id := range ids {
		if e := c.events[id]; e != nil {
			out = append(out, e)
		}
	}
	return out
}

func (c *Collection) EventsByFile(path string) []*Event {
	ids := c.eventsByFile[path]
	out := make([]*Event, 0, len(ids))
	for _, id := range ids {
		if e := c.events[id]; e != nil {
			out = append(out, e)
		}
	}
	return out
}

func (c *Collection) EntitiesByType(typ string) []*Entity {
	ids := c.entitiesByType[typ]
	out := make([]*Entity, 0, len(ids))
	for _, id := range ids {
		if e := c.entities[id]; e != nil {
			out = append(out, e)
		}
	}
	return out
}

func (c *Collection) SearchEvents(query Vector, topK int) []SearchResult {
	return c.EventIndex.Search(query, topK, nil)
}

func (c *Collection) SearchEntities(query Vector, topK int) []SearchResult {
	return c.EntityIndex.Search(query, topK, nil)
}

func (c *Collection) RecentEvents(n int) []*Event {
	all := make([]*Event, 0, len(c.events))
	for _, e := range c.events {
		all = append(all, e)
	}
	sortEventsByTime(all)
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

func (c *Collection) EventCount() int  { return len(c.events) }
func (c *Collection) EntityCount() int { return len(c.entities) }
func (c *Collection) FileCount() int   { return len(c.eventsByFile) }

func sortEventsByTime(events []*Event) {
	for i := 1; i < len(events); i++ {
		key := events[i]
		j := i - 1
		for j >= 0 && events[j].Timestamp.After(key.Timestamp) {
			events[j+1] = events[j]
			j--
		}
		events[j+1] = key
	}
}
