package vikingdb

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type IndexVersion struct {
	Version    int
	Timestamp  time.Time
	NodeCount  uint64
	MaxLevel   int
	EntryPoint uint64
	Config     HNSWConfig
	Checksum   uint64
}

type VersionManager struct {
	mu       sync.Mutex
	versions []IndexVersion
	maxKeep  int
}

func NewVersionManager(maxKeep int) *VersionManager {
	if maxKeep <= 0 {
		maxKeep = 10
	}
	return &VersionManager{maxKeep: maxKeep}
}

func (vm *VersionManager) Capture(h *HNSW) IndexVersion {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	v := IndexVersion{
		Version:    len(vm.versions) + 1,
		Timestamp:  time.Now(),
		NodeCount:  h.nodeCount,
		MaxLevel:   h.maxLevel,
		EntryPoint: h.entryPoint,
		Config:     h.cfg,
		Checksum:   h.computeChecksum(),
	}
	vm.versions = append(vm.versions, v)
	if len(vm.versions) > vm.maxKeep {
		vm.versions = vm.versions[len(vm.versions)-vm.maxKeep:]
	}
	return v
}

func (vm *VersionManager) Latest() *IndexVersion {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	if len(vm.versions) == 0 {
		return nil
	}
	v := vm.versions[len(vm.versions)-1]
	return &v
}

func (vm *VersionManager) Get(version int) *IndexVersion {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	for i := range vm.versions {
		if vm.versions[i].Version == version {
			v := vm.versions[i]
			return &v
		}
	}
	return nil
}

func (vm *VersionManager) List() []IndexVersion {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	out := make([]IndexVersion, len(vm.versions))
	copy(out, vm.versions)
	return out
}

func (vm *VersionManager) Diff(v1, v2 int) *VersionDiff {
	a := vm.Get(v1)
	b := vm.Get(v2)
	if a == nil || b == nil {
		return nil
	}
	return &VersionDiff{
		From:            *a,
		To:              *b,
		NodeCountDelta:  int64(b.NodeCount) - int64(a.NodeCount),
		MaxLevelChanged: a.MaxLevel != b.MaxLevel,
		EntryChanged:    a.EntryPoint != b.EntryPoint,
		ConfigChanged:   a.Config.M != b.Config.M || a.Config.EfSearch != b.Config.EfSearch,
		TimeDelta:       b.Timestamp.Sub(a.Timestamp),
	}
}

type VersionDiff struct {
	From            IndexVersion
	To              IndexVersion
	NodeCountDelta  int64
	MaxLevelChanged bool
	EntryChanged    bool
	ConfigChanged   bool
	TimeDelta       time.Duration
}

func (vd VersionDiff) Summary() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("v%d → v%d (Δ%v):\n", vd.From.Version, vd.To.Version, vd.TimeDelta.Round(time.Second)))
	sb.WriteString(fmt.Sprintf("  Nodes: %d → %d (%+d)\n", vd.From.NodeCount, vd.To.NodeCount, vd.NodeCountDelta))
	if vd.MaxLevelChanged {
		sb.WriteString(fmt.Sprintf("  MaxLevel: %d → %d\n", vd.From.MaxLevel, vd.To.MaxLevel))
	}
	if vd.EntryChanged {
		sb.WriteString(fmt.Sprintf("  EntryPoint: %d → %d\n", vd.From.EntryPoint, vd.To.EntryPoint))
	}
	if vd.ConfigChanged {
		sb.WriteString("  Config changed\n")
	}
	return sb.String()
}

func (h *HNSW) computeChecksum() uint64 {
	var cs uint64
	cs = cs*31 + h.nodeCount
	cs = cs*31 + h.entryPoint
	cs = cs*31 + uint64(h.maxLevel)
	cs = cs*31 + uint64(h.cfg.M)
	for id, node := range h.nodes {
		if h.tombstones.Has(id) {
			continue
		}
		cs = cs*31 + id
		cs = cs*31 + uint64(node.level)
		for _, conns := range node.connections {
			cs = cs*31 + uint64(len(conns))
		}
	}
	return cs
}

type ConfigMigration struct {
	OldConfig HNSWConfig
	NewConfig HNSWConfig
}

func (h *HNSW) MigrateConfig(newCfg HNSWConfig) (*MigrationResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := &MigrationResult{
		OldConfig: h.cfg,
		NewConfig: newCfg,
	}
	start := time.Now()
	if newCfg.M != h.cfg.M || newCfg.MMax != h.cfg.MMax || newCfg.MMax0 != h.cfg.MMax0 {
		h.cfg.M = newCfg.M
		h.cfg.MMax = newCfg.MMax
		h.cfg.MMax0 = newCfg.MMax0
		h.cfg.ML = newCfg.ML
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
					result.EdgesAdjusted++
				}
			}
			result.NodesProcessed++
		}
	}
	if newCfg.EfConstruction != h.cfg.EfConstruction {
		h.cfg.EfConstruction = newCfg.EfConstruction
	}
	if newCfg.EfSearch != h.cfg.EfSearch {
		h.cfg.EfSearch = newCfg.EfSearch
	}
	if newCfg.Dist != nil {
		h.cfg.Dist = newCfg.Dist
	}
	result.Duration = time.Since(start)
	return result, nil
}

type MigrationResult struct {
	OldConfig      HNSWConfig
	NewConfig      HNSWConfig
	NodesProcessed int
	EdgesAdjusted  int
	Duration       time.Duration
}

func (mr MigrationResult) Summary() string {
	return fmt.Sprintf("Migration: processed %d nodes, adjusted %d edges in %v\n  %s → %s",
		mr.NodesProcessed, mr.EdgesAdjusted, mr.Duration, mr.OldConfig, mr.NewConfig)
}

type IndexDiff struct {
	AddedIDs    []string
	RemovedIDs  []string
	ModifiedIDs []string
	MetaChanges map[string][]string
}

func DiffIndices(a, b *HNSW) *IndexDiff {
	a.mu.RLock()
	b.mu.RLock()
	defer a.mu.RUnlock()
	defer b.mu.RUnlock()
	diff := &IndexDiff{MetaChanges: make(map[string][]string)}
	aIDs := make(map[string]bool)
	for extID, id := range a.idMap {
		if !a.tombstones.Has(id) {
			aIDs[extID] = true
		}
	}
	bIDs := make(map[string]bool)
	for extID, id := range b.idMap {
		if !b.tombstones.Has(id) {
			bIDs[extID] = true
		}
	}
	for extID := range bIDs {
		if !aIDs[extID] {
			diff.AddedIDs = append(diff.AddedIDs, extID)
		}
	}
	for extID := range aIDs {
		if !bIDs[extID] {
			diff.RemovedIDs = append(diff.RemovedIDs, extID)
		}
	}
	for extID := range aIDs {
		if !bIDs[extID] {
			continue
		}
		aID := a.idMap[extID]
		bID := b.idMap[extID]
		aNode := a.nodes[aID]
		bNode := b.nodes[bID]
		if aNode == nil || bNode == nil {
			continue
		}
		if vectorsDiffer(aNode.vector, bNode.vector) {
			diff.ModifiedIDs = append(diff.ModifiedIDs, extID)
		}
	}
	sort.Strings(diff.AddedIDs)
	sort.Strings(diff.RemovedIDs)
	sort.Strings(diff.ModifiedIDs)
	return diff
}

func vectorsDiffer(a, b Vector) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return true
		}
	}
	return false
}

func (d *IndexDiff) HasChanges() bool {
	return len(d.AddedIDs) > 0 || len(d.RemovedIDs) > 0 || len(d.ModifiedIDs) > 0
}

func (d *IndexDiff) TotalChanges() int {
	return len(d.AddedIDs) + len(d.RemovedIDs) + len(d.ModifiedIDs)
}

func (d *IndexDiff) Summary() string {
	return fmt.Sprintf("IndexDiff: +%d -%d ~%d", len(d.AddedIDs), len(d.RemovedIDs), len(d.ModifiedIDs))
}

type IndexClone struct {
	Original *HNSW
}

func (h *HNSW) Clone() *HNSW {
	exported := h.Export()
	return ImportIndex(exported)
}

func (h *HNSW) DeepEqual(other *HNSW) bool {
	diff := DiffIndices(h, other)
	if diff.HasChanges() {
		return false
	}
	h.mu.RLock()
	other.mu.RLock()
	defer h.mu.RUnlock()
	defer other.mu.RUnlock()
	if h.maxLevel != other.maxLevel {
		return false
	}
	if h.cfg.M != other.cfg.M || h.cfg.EfSearch != other.cfg.EfSearch {
		return false
	}
	return true
}
