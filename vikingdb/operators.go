package vikingdb

import (
	"time"
)

func Dedup(c *Collection, simThreshold float32) int {
	removed := 0
	seen := make(map[string]bool)
	var toDelete []string

	for id, event := range c.events {
		if seen[id] {
			continue
		}
		if len(event.Embedding) == 0 {
			continue
		}
		results := c.EventIndex.Search(event.Embedding, 5, func(m map[string]interface{}) bool {
			mid, _ := m["conv_id"].(string)
			return mid == event.ConvID
		})
		for _, r := range results {
			if r.ID == id {
				continue
			}
			if r.Score >= simThreshold && !seen[r.ID] {
				toDelete = append(toDelete, r.ID)
				seen[r.ID] = true
				removed++
			}
		}
		seen[id] = true
	}

	for _, id := range toDelete {
		c.EventIndex.Delete(id)
		delete(c.events, id)
	}
	return removed
}

func TimeCompress(c *Collection, olderThan time.Duration, batchSize int) int {
	cutoff := time.Now().Add(-olderThan)
	var old []*Event
	for _, e := range c.events {
		if e.Timestamp.Before(cutoff) {
			old = append(old, e)
		}
	}
	if len(old) < batchSize {
		return 0
	}

	sortEventsByTime(old)
	compressed := 0
	for i := 0; i+batchSize <= len(old); i += batchSize {
		batch := old[i : i+batchSize]
		merged := mergeEvents(batch)
		c.InsertEvent(merged)
		for _, e := range batch {
			c.EventIndex.Delete(e.ID)
			delete(c.events, e.ID)
		}
		compressed += batchSize - 1
	}
	return compressed
}

func Forget(c *Collection, olderThan time.Duration) int {
	cutoff := time.Now().Add(-olderThan)
	var toDelete []string
	for id, e := range c.events {
		if e.Timestamp.Before(cutoff) {
			toDelete = append(toDelete, id)
		}
	}
	for _, id := range toDelete {
		c.EventIndex.Delete(id)
		delete(c.events, id)
	}
	return len(toDelete)
}

func MergeEntities(c *Collection, idA, idB string) *Entity {
	a := c.entities[idA]
	b := c.entities[idB]
	if a == nil || b == nil {
		return nil
	}

	merged := &Entity{
		ID:           idA,
		Name:         a.Name,
		Type:         a.Type,
		Properties:   make(map[string]interface{}),
		EventHistory: append(a.EventHistory, b.EventHistory...),
		FirstSeen:    a.FirstSeen,
		LastSeen:     b.LastSeen,
		Frequency:    a.Frequency + b.Frequency,
		Embedding:    a.Embedding,
	}

	if b.FirstSeen.Before(a.FirstSeen) {
		merged.FirstSeen = b.FirstSeen
	}
	if a.LastSeen.After(b.LastSeen) {
		merged.LastSeen = a.LastSeen
	}

	for k, v := range a.Properties {
		merged.Properties[k] = v
	}
	for k, v := range b.Properties {
		merged.Properties[k] = v
	}

	delete(c.entities, idB)
	c.EntityIndex.Delete(idB)
	c.entities[idA] = merged
	if len(merged.Embedding) > 0 {
		c.EntityIndex.Upsert(idA, merged.Embedding, map[string]interface{}{
			"name":      merged.Name,
			"type":      merged.Type,
			"frequency": merged.Frequency,
		})
	}

	return merged
}

func EventToEntity(c *Collection, event *Event, entityID, entityName, entityType string) *Entity {
	entity := &Entity{
		ID:           entityID,
		Name:         entityName,
		Type:         entityType,
		Properties:   make(map[string]interface{}),
		EventHistory: []string{event.ID},
		FirstSeen:    event.Timestamp,
		LastSeen:     event.Timestamp,
		Frequency:    1,
		Embedding:    event.Embedding,
	}
	c.InsertEntity(entity)
	return c.entities[entityID]
}

func mergeEvents(batch []*Event) *Event {
	if len(batch) == 0 {
		return nil
	}

	merged := &Event{
		ID:        batch[0].ID + "_merged",
		Timestamp: batch[0].Timestamp,
		ConvID:    batch[0].ConvID,
		Goal:      batch[0].Goal,
		Tool:      batch[0].Tool,
		ExitCode:  0,
	}

	var summaries []string
	files := make(map[string]bool)
	tags := make(map[string]bool)
	var sumVec Vector

	for _, e := range batch {
		if e.Summary != "" {
			summaries = append(summaries, e.Summary)
		}
		for _, f := range e.Files {
			files[f] = true
		}
		for _, t := range e.Tags {
			tags[t] = true
		}
		if e.ExitCode != 0 {
			merged.ExitCode = e.ExitCode
		}
		if len(e.Embedding) > 0 {
			if sumVec == nil {
				sumVec = make(Vector, len(e.Embedding))
			}
			for i, v := range e.Embedding {
				sumVec[i] += v
			}
		}
		if e.Timestamp.After(merged.Timestamp) {
			merged.Timestamp = e.Timestamp
		}
	}

	merged.Summary = joinStrings(summaries, "; ")
	for f := range files {
		merged.Files = append(merged.Files, f)
	}
	for t := range tags {
		merged.Tags = append(merged.Tags, t)
	}
	if sumVec != nil {
		merged.Embedding = Normalize(sumVec)
	}

	return merged
}

func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for _, s := range ss[1:] {
		result += sep + s
	}
	return result
}
