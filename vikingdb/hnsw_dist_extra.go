package vikingdb

import (
	"math"
	"sort"
)

type BrayCurtisProvider struct{}

func NewBrayCurtisProvider() BrayCurtisProvider { return BrayCurtisProvider{} }
func (p BrayCurtisProvider) New(a []float32) Distancer {
	return &brayCurtisDistancer{a: a}
}
func (p BrayCurtisProvider) SingleDist(a, b []float32) float32 { return brayCurtisDist(a, b) }
func (p BrayCurtisProvider) Step(a, b []float32) float32       { return brayCurtisDist(a, b) }
func (p BrayCurtisProvider) Wrap(x float32) float32            { return x }
func (p BrayCurtisProvider) Type() string                      { return "bray-curtis" }

type brayCurtisDistancer struct{ a []float32 }

func (d *brayCurtisDistancer) Distance(b []float32) float32 { return brayCurtisDist(d.a, b) }

func brayCurtisDist(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 1.0
	}
	var num, den float32
	for i := range a {
		diff := a[i] - b[i]
		if diff < 0 {
			diff = -diff
		}
		num += diff
		sum := a[i] + b[i]
		if sum < 0 {
			sum = -sum
		}
		den += sum
	}
	if den == 0 {
		return 0
	}
	return num / den
}

type CanberraProvider struct{}

func NewCanberraProvider() CanberraProvider { return CanberraProvider{} }
func (p CanberraProvider) New(a []float32) Distancer {
	return &canberraDistancer{a: a}
}
func (p CanberraProvider) SingleDist(a, b []float32) float32 { return canberraDist(a, b) }
func (p CanberraProvider) Step(a, b []float32) float32       { return canberraDist(a, b) }
func (p CanberraProvider) Wrap(x float32) float32            { return x }
func (p CanberraProvider) Type() string                      { return "canberra" }

type canberraDistancer struct{ a []float32 }

func (d *canberraDistancer) Distance(b []float32) float32 { return canberraDist(d.a, b) }

func canberraDist(a, b []float32) float32 {
	if len(a) != len(b) {
		return math.MaxFloat32
	}
	var sum float32
	for i := range a {
		num := a[i] - b[i]
		if num < 0 {
			num = -num
		}
		absA := a[i]
		if absA < 0 {
			absA = -absA
		}
		absB := b[i]
		if absB < 0 {
			absB = -absB
		}
		den := absA + absB
		if den > 0 {
			sum += num / den
		}
	}
	return sum
}

type NormalizedL2Provider struct{}

func NewNormalizedL2Provider() NormalizedL2Provider { return NormalizedL2Provider{} }
func (p NormalizedL2Provider) New(a []float32) Distancer {
	return &normalizedL2Distancer{a: NormalizeVector(a)}
}
func (p NormalizedL2Provider) SingleDist(a, b []float32) float32 {
	return l2SquaredDist(NormalizeVector(a), NormalizeVector(b))
}
func (p NormalizedL2Provider) Step(a, b []float32) float32 { return p.SingleDist(a, b) }
func (p NormalizedL2Provider) Wrap(x float32) float32      { return x }
func (p NormalizedL2Provider) Type() string                 { return "normalized-l2" }

type normalizedL2Distancer struct{ a []float32 }

func (d *normalizedL2Distancer) Distance(b []float32) float32 {
	return l2SquaredDist(d.a, NormalizeVector(b))
}

type DistanceMatrix struct {
	ids     []uint64
	dists   [][]float32
	idIndex map[uint64]int
}

func NewDistanceMatrix(ids []uint64) *DistanceMatrix {
	n := len(ids)
	dists := make([][]float32, n)
	for i := range dists {
		dists[i] = make([]float32, n)
	}
	idIndex := make(map[uint64]int, n)
	for i, id := range ids {
		idIndex[id] = i
	}
	return &DistanceMatrix{ids: ids, dists: dists, idIndex: idIndex}
}

func (dm *DistanceMatrix) Set(a, b uint64, dist float32) {
	i, ok1 := dm.idIndex[a]
	j, ok2 := dm.idIndex[b]
	if ok1 && ok2 {
		dm.dists[i][j] = dist
		dm.dists[j][i] = dist
	}
}

func (dm *DistanceMatrix) Get(a, b uint64) float32 {
	i, ok1 := dm.idIndex[a]
	j, ok2 := dm.idIndex[b]
	if ok1 && ok2 {
		return dm.dists[i][j]
	}
	return math.MaxFloat32
}

func (dm *DistanceMatrix) NearestTo(id uint64, k int) []distIDPair {
	i, ok := dm.idIndex[id]
	if !ok {
		return nil
	}
	type scored struct {
		id   uint64
		dist float32
	}
	var results []scored
	for j, d := range dm.dists[i] {
		if j == i {
			continue
		}
		results = append(results, scored{dm.ids[j], d})
	}
	sort.Slice(results, func(a, b int) bool {
		return results[a].dist < results[b].dist
	})
	out := make([]distIDPair, 0, k)
	for j := 0; j < len(results) && j < k; j++ {
		out = append(out, distIDPair{id: results[j].id, dist: results[j].dist})
	}
	return out
}

func (dm *DistanceMatrix) Size() int { return len(dm.ids) }

func (dm *DistanceMatrix) MeanDistance() float32 {
	n := len(dm.ids)
	if n < 2 {
		return 0
	}
	var sum float64
	count := 0
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			sum += float64(dm.dists[i][j])
			count++
		}
	}
	return float32(sum / float64(count))
}

func BuildDistanceMatrix(h *HNSW, ids []uint64) *DistanceMatrix {
	dm := NewDistanceMatrix(ids)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for i := 0; i < len(ids); i++ {
		nodeA := h.nodes[ids[i]]
		if nodeA == nil {
			continue
		}
		for j := i + 1; j < len(ids); j++ {
			nodeB := h.nodes[ids[j]]
			if nodeB == nil {
				continue
			}
			d := h.cfg.Dist(nodeA.vector, nodeB.vector)
			dm.Set(ids[i], ids[j], d)
		}
	}
	return dm
}

type CentroidDistanceProvider struct {
	centroids [][]float32
	distFn    DistFunc
}

func NewCentroidDistanceProvider(centroids [][]float32, distFn DistFunc) *CentroidDistanceProvider {
	return &CentroidDistanceProvider{centroids: centroids, distFn: distFn}
}

func (p *CentroidDistanceProvider) NearestCentroid(vec []float32) (int, float32) {
	bestIdx := 0
	bestDist := float32(math.MaxFloat32)
	for i, c := range p.centroids {
		d := p.distFn(vec, c)
		if d < bestDist {
			bestDist = d
			bestIdx = i
		}
	}
	return bestIdx, bestDist
}

func (p *CentroidDistanceProvider) AssignAll(vecs [][]float32) []int {
	assignments := make([]int, len(vecs))
	for i, v := range vecs {
		assignments[i], _ = p.NearestCentroid(v)
	}
	return assignments
}

func (p *CentroidDistanceProvider) IntraClusterDist(vecs [][]float32, assignments []int) []float32 {
	k := len(p.centroids)
	dists := make([]float32, k)
	counts := make([]int, k)
	for i, v := range vecs {
		a := assignments[i]
		dists[a] += p.distFn(v, p.centroids[a])
		counts[a]++
	}
	for i := range dists {
		if counts[i] > 0 {
			dists[i] /= float32(counts[i])
		}
	}
	return dists
}

func (p *CentroidDistanceProvider) InterClusterDist() *DistanceMatrix {
	ids := make([]uint64, len(p.centroids))
	for i := range ids {
		ids[i] = uint64(i)
	}
	dm := NewDistanceMatrix(ids)
	for i := 0; i < len(p.centroids); i++ {
		for j := i + 1; j < len(p.centroids); j++ {
			d := p.distFn(p.centroids[i], p.centroids[j])
			dm.Set(uint64(i), uint64(j), d)
		}
	}
	return dm
}

func (p *CentroidDistanceProvider) Silhouette(vecs [][]float32, assignments []int) float64 {
	n := len(vecs)
	if n < 2 {
		return 0
	}
	k := len(p.centroids)
	clusterMembers := make([][]int, k)
	for i, a := range assignments {
		clusterMembers[a] = append(clusterMembers[a], i)
	}
	var totalSil float64
	for i := 0; i < n; i++ {
		a := assignments[i]
		intra := float64(0)
		if len(clusterMembers[a]) > 1 {
			for _, j := range clusterMembers[a] {
				if j != i {
					intra += float64(p.distFn(vecs[i], vecs[j]))
				}
			}
			intra /= float64(len(clusterMembers[a]) - 1)
		}
		minInter := math.MaxFloat64
		for c := 0; c < k; c++ {
			if c == a || len(clusterMembers[c]) == 0 {
				continue
			}
			inter := float64(0)
			for _, j := range clusterMembers[c] {
				inter += float64(p.distFn(vecs[i], vecs[j]))
			}
			inter /= float64(len(clusterMembers[c]))
			if inter < minInter {
				minInter = inter
			}
		}
		if minInter == math.MaxFloat64 {
			continue
		}
		maxAB := intra
		if minInter > maxAB {
			maxAB = minInter
		}
		if maxAB > 0 {
			totalSil += (minInter - intra) / maxAB
		}
	}
	return totalSil / float64(n)
}
