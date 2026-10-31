package vikingdb

import (
	"sync/atomic"
	"time"
)

type HNSWMetrics struct {
	insertCount     atomic.Int64
	deleteCount     atomic.Int64
	searchCount     atomic.Int64
	tombstoneCount  atomic.Int64
	cleanupCount    atomic.Int64

	totalInsertNs   atomic.Int64
	totalSearchNs   atomic.Int64
	totalDeleteNs   atomic.Int64

	distComputations atomic.Int64

	growCount       atomic.Int64
	lastGrowNs      atomic.Int64

	flatSearchCount atomic.Int64
	hnswSearchCount atomic.Int64
}

func NewHNSWMetrics() *HNSWMetrics {
	return &HNSWMetrics{}
}

func (m *HNSWMetrics) TrackInsert(start time.Time) {
	m.insertCount.Add(1)
	m.totalInsertNs.Add(time.Since(start).Nanoseconds())
}

func (m *HNSWMetrics) TrackSearch(start time.Time) {
	m.searchCount.Add(1)
	m.totalSearchNs.Add(time.Since(start).Nanoseconds())
}

func (m *HNSWMetrics) TrackDelete(start time.Time) {
	m.deleteCount.Add(1)
	m.totalDeleteNs.Add(time.Since(start).Nanoseconds())
}

func (m *HNSWMetrics) TrackDistComputation() {
	m.distComputations.Add(1)
}

func (m *HNSWMetrics) TrackDistComputations(n int64) {
	m.distComputations.Add(n)
}

func (m *HNSWMetrics) AddTombstone() {
	m.tombstoneCount.Add(1)
}

func (m *HNSWMetrics) RemoveTombstone() {
	m.tombstoneCount.Add(-1)
}

func (m *HNSWMetrics) SetTombstoneCount(n int64) {
	m.tombstoneCount.Store(n)
}

func (m *HNSWMetrics) TrackCleanup() {
	m.cleanupCount.Add(1)
}

func (m *HNSWMetrics) TrackGrow(start time.Time) {
	m.growCount.Add(1)
	m.lastGrowNs.Store(time.Since(start).Nanoseconds())
}

func (m *HNSWMetrics) TrackFlatSearch() {
	m.flatSearchCount.Add(1)
}

func (m *HNSWMetrics) TrackHNSWSearch() {
	m.hnswSearchCount.Add(1)
}

func (m *HNSWMetrics) Snapshot() map[string]interface{} {
	inserts := m.insertCount.Load()
	searches := m.searchCount.Load()
	deletes := m.deleteCount.Load()

	avgInsertUs := int64(0)
	if inserts > 0 {
		avgInsertUs = m.totalInsertNs.Load() / inserts / 1000
	}
	avgSearchUs := int64(0)
	if searches > 0 {
		avgSearchUs = m.totalSearchNs.Load() / searches / 1000
	}
	avgDeleteUs := int64(0)
	if deletes > 0 {
		avgDeleteUs = m.totalDeleteNs.Load() / deletes / 1000
	}

	return map[string]interface{}{
		"inserts":            inserts,
		"searches":           searches,
		"deletes":            deletes,
		"tombstones":         m.tombstoneCount.Load(),
		"cleanups":           m.cleanupCount.Load(),
		"dist_computations":  m.distComputations.Load(),
		"avg_insert_us":      avgInsertUs,
		"avg_search_us":      avgSearchUs,
		"avg_delete_us":      avgDeleteUs,
		"grows":              m.growCount.Load(),
		"flat_searches":      m.flatSearchCount.Load(),
		"hnsw_searches":      m.hnswSearchCount.Load(),
	}
}

func (m *HNSWMetrics) Reset() {
	m.insertCount.Store(0)
	m.deleteCount.Store(0)
	m.searchCount.Store(0)
	m.tombstoneCount.Store(0)
	m.cleanupCount.Store(0)
	m.totalInsertNs.Store(0)
	m.totalSearchNs.Store(0)
	m.totalDeleteNs.Store(0)
	m.distComputations.Store(0)
	m.growCount.Store(0)
	m.lastGrowNs.Store(0)
	m.flatSearchCount.Store(0)
	m.hnswSearchCount.Store(0)
}
