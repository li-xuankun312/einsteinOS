package vikingdb

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type IndexInfo struct {
	NodeCount      uint64
	ActiveCount    int
	TombstoneCount int
	MaxLevel       int
	EntryPoint     uint64
	EntryPointExt  string
	Dimensions     int
	TotalEdges     int
	AvgDegree      float64
	MemoryBytes    int64
	Config         HNSWConfig
	CreatedAt      time.Time
	LastModified   time.Time
}

func (h *HNSW) Info() IndexInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	info := IndexInfo{
		NodeCount:      h.nodeCount,
		TombstoneCount: h.tombstones.Len(),
		MaxLevel:       h.maxLevel,
		EntryPoint:     h.entryPoint,
		EntryPointExt:  h.reverseMap[h.entryPoint],
		Config:         h.cfg,
	}
	info.ActiveCount = int(h.nodeCount) - info.TombstoneCount
	totalEdges := 0
	dim := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		if dim == 0 {
			dim = len(node.vector)
		}
		for level := 0; level <= node.level; level++ {
			totalEdges += len(node.neighborsAtLevel(level))
		}
	}
	info.Dimensions = dim
	info.TotalEdges = totalEdges
	if info.ActiveCount > 0 {
		info.AvgDegree = float64(totalEdges) / float64(info.ActiveCount)
	}
	info.MemoryBytes = h.estimateMemory()
	return info
}

func (ii IndexInfo) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Index Info:\n"))
	sb.WriteString(fmt.Sprintf("  Nodes: %d active, %d tombstoned, %d total\n",
		ii.ActiveCount, ii.TombstoneCount, ii.NodeCount))
	sb.WriteString(fmt.Sprintf("  Dimensions: %d\n", ii.Dimensions))
	sb.WriteString(fmt.Sprintf("  Max Level: %d, Entry: %s (%d)\n", ii.MaxLevel, ii.EntryPointExt, ii.EntryPoint))
	sb.WriteString(fmt.Sprintf("  Edges: %d, Avg Degree: %.1f\n", ii.TotalEdges, ii.AvgDegree))
	sb.WriteString(fmt.Sprintf("  Memory: %s\n", formatMemory(ii.MemoryBytes)))
	sb.WriteString(fmt.Sprintf("  Config: %s\n", ii.Config.String()))
	return sb.String()
}

func (h *HNSW) estimateMemory() int64 {
	var mem int64
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		mem += int64(len(node.vector)) * 4
		mem += 64
		for _, conns := range node.connections {
			mem += int64(len(conns)) * 8
			mem += 24
		}
	}
	mem += int64(len(h.idMap)) * 48
	mem += int64(len(h.reverseMap)) * 48
	mem += int64(len(h.meta)) * 200
	return mem
}

func formatMemory(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

type ExportedIndex struct {
	Config     HNSWConfig
	EntryPoint uint64
	MaxLevel   int
	Nodes      []ExportedNode
	Tombstones []uint64
}

type ExportedNode struct {
	ID          uint64
	ExtID       string
	Level       int
	Vector      Vector
	Connections [][]uint64
	Metadata    map[string]interface{}
}

func (h *HNSW) Export() *ExportedIndex {
	h.mu.RLock()
	defer h.mu.RUnlock()
	exported := &ExportedIndex{
		Config:     h.cfg,
		EntryPoint: h.entryPoint,
		MaxLevel:   h.maxLevel,
		Tombstones: h.tombstones.List(),
	}
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		en := ExportedNode{
			ID:          id,
			ExtID:       h.reverseMap[id],
			Level:       node.level,
			Vector:      make(Vector, len(node.vector)),
			Connections: make([][]uint64, node.level+1),
			Metadata:    h.meta[id],
		}
		copy(en.Vector, node.vector)
		for level := 0; level <= node.level; level++ {
			conns := node.neighborsAtLevel(level)
			en.Connections[level] = make([]uint64, len(conns))
			copy(en.Connections[level], conns)
		}
		exported.Nodes = append(exported.Nodes, en)
	}
	sort.Slice(exported.Nodes, func(i, j int) bool {
		return exported.Nodes[i].ID < exported.Nodes[j].ID
	})
	return exported
}

func ImportIndex(exported *ExportedIndex) *HNSW {
	h := NewHNSW(exported.Config)
	h.entryPoint = exported.EntryPoint
	h.maxLevel = exported.MaxLevel
	for _, en := range exported.Nodes {
		node := &hnswNode{
			id:          en.ID,
			level:       en.Level,
			connections: make([][]uint64, en.Level+1),
			vector:      en.Vector,
		}
		for level := 0; level <= en.Level; level++ {
			if level < len(en.Connections) {
				node.connections[level] = en.Connections[level]
			} else {
				node.connections[level] = []uint64{}
			}
		}
		h.nodes[en.ID] = node
		h.idMap[en.ExtID] = en.ID
		h.reverseMap[en.ID] = en.ExtID
		if en.Metadata != nil {
			h.meta[en.ID] = en.Metadata
		}
		h.nodeCount++
		if en.ID >= h.nextID.Load() {
			h.nextID.Store(en.ID + 1)
		}
	}
	for _, id := range exported.Tombstones {
		h.tombstones.Add(id)
	}
	return h
}

type MaintenanceConfig struct {
	CleanupInterval    time.Duration
	MaxTombstoneRatio  float64
	RepairOnStartup    bool
	CompactAfterDelete int
	AutoShrink         bool
}

func DefaultMaintenanceConfig() MaintenanceConfig {
	return MaintenanceConfig{
		CleanupInterval:    5 * time.Minute,
		MaxTombstoneRatio:  0.2,
		RepairOnStartup:    true,
		CompactAfterDelete: 1000,
		AutoShrink:         true,
	}
}

type MaintenanceScheduler struct {
	h           *HNSW
	cfg         MaintenanceConfig
	deleteCount int
	lastCleanup time.Time
	stopCh      chan struct{}
}

func NewMaintenanceScheduler(h *HNSW, cfg MaintenanceConfig) *MaintenanceScheduler {
	return &MaintenanceScheduler{
		h:      h,
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

func (ms *MaintenanceScheduler) Start() {
	go ms.loop()
}

func (ms *MaintenanceScheduler) Stop() {
	close(ms.stopCh)
}

func (ms *MaintenanceScheduler) loop() {
	ticker := time.NewTicker(ms.cfg.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ms.stopCh:
			return
		case <-ticker.C:
			ms.check()
		}
	}
}

func (ms *MaintenanceScheduler) check() {
	tombRatio := float64(ms.h.tombstones.Len()) / float64(ms.h.nodeCount+1)
	if tombRatio > ms.cfg.MaxTombstoneRatio {
		ms.h.CleanupTombstones(500)
		ms.lastCleanup = time.Now()
	}
	if ms.cfg.AutoShrink && tombRatio > ms.cfg.MaxTombstoneRatio*2 {
		ms.h.PurgeDeleted()
	}
}

func (ms *MaintenanceScheduler) NotifyDelete() {
	ms.deleteCount++
	if ms.deleteCount >= ms.cfg.CompactAfterDelete {
		ms.h.CleanupTombstones(ms.deleteCount)
		ms.deleteCount = 0
	}
}

func (h *HNSW) Resize(newM, newEfConstruction int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if newM > 0 {
		h.cfg.M = newM
		h.cfg.MMax = newM
		h.cfg.MMax0 = newM * 2
		h.cfg.ML = 1.0 / math.Log(float64(newM))
	}
	if newEfConstruction > 0 {
		h.cfg.EfConstruction = newEfConstruction
	}
	repaired := 0
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		for level := 0; level <= node.level; level++ {
			maxConn := h.maxConnections(level)
			conns := node.neighborsAtLevel(level)
			if len(conns) > maxConn {
				node.Lock()
				shrunk := h.selectNeighborsHeuristic(node.vector, conns, maxConn)
				node.setNeighborsAtLevel(level, shrunk)
				node.Unlock()
				repaired++
			}
		}
	}
	return repaired
}

func (h *HNSW) Sample(n int) []ExportedNode {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var allIDs []uint64
	for id := range h.nodes {
		if !h.tombstones.Has(id) {
			allIDs = append(allIDs, id)
		}
	}
	if n >= len(allIDs) {
		n = len(allIDs)
	}
	sampled := make([]ExportedNode, 0, n)
	step := len(allIDs) / n
	if step < 1 {
		step = 1
	}
	for i := 0; i < len(allIDs) && len(sampled) < n; i += step {
		id := allIDs[i]
		node := h.nodes[id]
		if node == nil {
			continue
		}
		sampled = append(sampled, ExportedNode{
			ID:       id,
			ExtID:    h.reverseMap[id],
			Level:    node.level,
			Vector:   node.vector,
			Metadata: h.meta[id],
		})
	}
	return sampled
}

func (h *HNSW) VectorDimension() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, node := range h.nodes {
		if !h.tombstones.Has(id) {
			return len(node.vector)
		}
	}
	return 0
}

func (h *HNSW) MeanVector() Vector {
	h.mu.RLock()
	defer h.mu.RUnlock()
	dim := 0
	count := 0
	var sum []float64
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		if dim == 0 {
			dim = len(node.vector)
			sum = make([]float64, dim)
		}
		for i, v := range node.vector {
			sum[i] += float64(v)
		}
		count++
	}
	if count == 0 {
		return nil
	}
	mean := make(Vector, dim)
	for i := range sum {
		mean[i] = float32(sum[i] / float64(count))
	}
	return mean
}

func (h *HNSW) Centroid(extIDs []string) Vector {
	h.mu.RLock()
	defer h.mu.RUnlock()
	dim := 0
	var sum []float64
	count := 0
	for _, extID := range extIDs {
		id, ok := h.idMap[extID]
		if !ok {
			continue
		}
		node := h.nodes[id]
		if node == nil || h.tombstones.Has(id) {
			continue
		}
		if dim == 0 {
			dim = len(node.vector)
			sum = make([]float64, dim)
		}
		for i, v := range node.vector {
			sum[i] += float64(v)
		}
		count++
	}
	if count == 0 {
		return nil
	}
	centroid := make(Vector, dim)
	for i := range sum {
		centroid[i] = float32(sum[i] / float64(count))
	}
	return centroid
}

type HealthCheck struct {
	Healthy         bool
	NodeCount       int
	TombstoneRatio  float64
	OrphanCount     int
	DanglingEdges   int
	Components      int
	EntryPointValid bool
	Issues          []string
}

func (h *HNSW) HealthCheck() HealthCheck {
	h.mu.RLock()
	defer h.mu.RUnlock()
	hc := HealthCheck{Healthy: true}
	active := 0
	for id := range h.nodes {
		if !h.tombstones.Has(id) {
			active++
		}
	}
	hc.NodeCount = active
	if h.nodeCount > 0 {
		hc.TombstoneRatio = float64(h.tombstones.Len()) / float64(h.nodeCount)
	}
	if hc.TombstoneRatio > 0.3 {
		hc.Issues = append(hc.Issues, fmt.Sprintf("high tombstone ratio: %.1f%%", hc.TombstoneRatio*100))
		hc.Healthy = false
	}
	epNode := h.nodes[h.entryPoint]
	hc.EntryPointValid = epNode != nil && !h.tombstones.Has(h.entryPoint)
	if !hc.EntryPointValid && h.nodeCount > 0 {
		hc.Issues = append(hc.Issues, "invalid entry point")
		hc.Healthy = false
	}
	dangling := 0
	orphans := 0
	referenced := make(map[uint64]bool)
	referenced[h.entryPoint] = true
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		hasEdge := false
		for level := 0; level <= node.level; level++ {
			for _, c := range node.neighborsAtLevel(level) {
				referenced[c] = true
				if h.nodes[c] == nil || h.tombstones.Has(c) {
					dangling++
				}
				hasEdge = true
			}
		}
		if !hasEdge && id != h.entryPoint {
			orphans++
		}
	}
	hc.DanglingEdges = dangling
	hc.OrphanCount = orphans
	if dangling > 0 {
		hc.Issues = append(hc.Issues, fmt.Sprintf("%d dangling edges", dangling))
	}
	if orphans > active/10 && orphans > 10 {
		hc.Issues = append(hc.Issues, fmt.Sprintf("%d orphan nodes (%.1f%%)", orphans, float64(orphans)/float64(active)*100))
		hc.Healthy = false
	}
	if len(hc.Issues) > 0 {
		hc.Healthy = false
	}
	return hc
}

func (hc HealthCheck) Summary() string {
	status := "HEALTHY"
	if !hc.Healthy {
		status = "UNHEALTHY"
	}
	s := fmt.Sprintf("Health [%s]: %d nodes, tombstone=%.1f%%, orphans=%d, dangling=%d",
		status, hc.NodeCount, hc.TombstoneRatio*100, hc.OrphanCount, hc.DanglingEdges)
	if len(hc.Issues) > 0 {
		s += "\n  Issues: " + strings.Join(hc.Issues, "; ")
	}
	return s
}
