package vikingdb

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
)

type NormCache struct {
	mu      sync.RWMutex
	norms   map[uint64]float32
	hits    atomic.Int64
	misses  atomic.Int64
}

func NewNormCache() *NormCache {
	return &NormCache{norms: make(map[uint64]float32)}
}

func (nc *NormCache) Get(id uint64) (float32, bool) {
	nc.mu.RLock()
	n, ok := nc.norms[id]
	nc.mu.RUnlock()
	if ok {
		nc.hits.Add(1)
	} else {
		nc.misses.Add(1)
	}
	return n, ok
}

func (nc *NormCache) Set(id uint64, norm float32) {
	nc.mu.Lock()
	nc.norms[id] = norm
	nc.mu.Unlock()
}

func (nc *NormCache) SetFromVector(id uint64, vec []float32) float32 {
	n := computeL2Norm(vec)
	nc.Set(id, n)
	return n
}

func (nc *NormCache) Delete(id uint64) {
	nc.mu.Lock()
	delete(nc.norms, id)
	nc.mu.Unlock()
}

func (nc *NormCache) Len() int {
	nc.mu.RLock()
	defer nc.mu.RUnlock()
	return len(nc.norms)
}

func (nc *NormCache) HitRate() float64 {
	h := nc.hits.Load()
	m := nc.misses.Load()
	total := h + m
	if total == 0 {
		return 0
	}
	return float64(h) / float64(total)
}

func (nc *NormCache) Clear() {
	nc.mu.Lock()
	nc.norms = make(map[uint64]float32)
	nc.mu.Unlock()
	nc.hits.Store(0)
	nc.misses.Store(0)
}

func (nc *NormCache) Summary() string {
	return fmt.Sprintf("NormCache{size=%d hitRate=%.1f%%}", nc.Len(), nc.HitRate()*100)
}

func computeL2Norm(vec []float32) float32 {
	var sum float32
	i := 0
	for ; i+7 < len(vec); i += 8 {
		sum += vec[i]*vec[i] + vec[i+1]*vec[i+1] +
			vec[i+2]*vec[i+2] + vec[i+3]*vec[i+3] +
			vec[i+4]*vec[i+4] + vec[i+5]*vec[i+5] +
			vec[i+6]*vec[i+6] + vec[i+7]*vec[i+7]
	}
	for ; i < len(vec); i++ {
		sum += vec[i] * vec[i]
	}
	return float32(math.Sqrt(float64(sum)))
}

type NormAwareCosineProvider struct {
	cache *NormCache
}

func NewNormAwareCosineProvider(cache *NormCache) *NormAwareCosineProvider {
	return &NormAwareCosineProvider{cache: cache}
}

func (p *NormAwareCosineProvider) New(a []float32) Distancer {
	normA := computeL2Norm(a)
	return &normAwareCosineDistancer{a: a, normA: normA, cache: p.cache}
}

func (p *NormAwareCosineProvider) SingleDist(a, b []float32) float32 {
	return cosineDistNorm(a, b, computeL2Norm(a), computeL2Norm(b))
}

func (p *NormAwareCosineProvider) Step(a, b []float32) float32 { return p.SingleDist(a, b) }
func (p *NormAwareCosineProvider) Wrap(x float32) float32      { return x }
func (p *NormAwareCosineProvider) Type() string                 { return "norm-aware-cosine" }

type normAwareCosineDistancer struct {
	a     []float32
	normA float32
	cache *NormCache
}

func (d *normAwareCosineDistancer) Distance(b []float32) float32 {
	normB := computeL2Norm(b)
	return cosineDistNorm(d.a, b, d.normA, normB)
}

func cosineDistNorm(a, b []float32, normA, normB float32) float32 {
	if normA == 0 || normB == 0 {
		return 2.0
	}
	var dot float32
	i := 0
	for ; i+7 < len(a); i += 8 {
		dot += a[i]*b[i] + a[i+1]*b[i+1] + a[i+2]*b[i+2] + a[i+3]*b[i+3] +
			a[i+4]*b[i+4] + a[i+5]*b[i+5] + a[i+6]*b[i+6] + a[i+7]*b[i+7]
	}
	for ; i < len(a) && i < len(b); i++ {
		dot += a[i] * b[i]
	}
	return 1.0 - dot/(normA*normB)
}

type NormAwareDotProvider struct {
	cache *NormCache
}

func NewNormAwareDotProvider(cache *NormCache) *NormAwareDotProvider {
	return &NormAwareDotProvider{cache: cache}
}

func (p *NormAwareDotProvider) New(a []float32) Distancer {
	return &normAwareDotDistancer{a: a}
}

func (p *NormAwareDotProvider) SingleDist(a, b []float32) float32 {
	return negativeDot(a, b)
}

func (p *NormAwareDotProvider) Step(a, b []float32) float32 { return negativeDot(a, b) }
func (p *NormAwareDotProvider) Wrap(x float32) float32      { return x }
func (p *NormAwareDotProvider) Type() string                 { return "norm-aware-dot" }

type normAwareDotDistancer struct {
	a []float32
}

func (d *normAwareDotDistancer) Distance(b []float32) float32 {
	return negativeDot(d.a, b)
}

func negativeDot(a, b []float32) float32 {
	var sum float32
	i := 0
	for ; i+7 < len(a); i += 8 {
		sum += a[i]*b[i] + a[i+1]*b[i+1] + a[i+2]*b[i+2] + a[i+3]*b[i+3] +
			a[i+4]*b[i+4] + a[i+5]*b[i+5] + a[i+6]*b[i+6] + a[i+7]*b[i+7]
	}
	for ; i < len(a) && i < len(b); i++ {
		sum += a[i] * b[i]
	}
	return -sum
}

type InnerProductToL2 struct {
	normA float32
	normB float32
}

func IPToL2(dotProduct, normA, normB float32) float32 {
	return normA*normA + normB*normB - 2*dotProduct
}

func L2ToIP(l2Dist, normA, normB float32) float32 {
	return (normA*normA + normB*normB - l2Dist) / 2
}

func L2ToCosine(l2Dist, normA, normB float32) float32 {
	if normA == 0 || normB == 0 {
		return 2.0
	}
	ip := L2ToIP(l2Dist, normA, normB)
	return 1.0 - ip/(normA*normB)
}

type DistanceConverter struct {
	fromType string
	toType   string
	normA    float32
	normB    float32
}

func NewDistanceConverter(from, to string) *DistanceConverter {
	return &DistanceConverter{fromType: from, toType: to}
}

func (dc *DistanceConverter) WithNorms(normA, normB float32) *DistanceConverter {
	dc.normA = normA
	dc.normB = normB
	return dc
}

func (dc *DistanceConverter) Convert(dist float32) float32 {
	switch dc.fromType + "->" + dc.toType {
	case "l2->cosine":
		return L2ToCosine(dist, dc.normA, dc.normB)
	case "l2->dot":
		return L2ToIP(dist, dc.normA, dc.normB)
	case "dot->l2":
		return IPToL2(dist, dc.normA, dc.normB)
	case "cosine->l2":
		ip := (1.0 - dist) * dc.normA * dc.normB
		return IPToL2(ip, dc.normA, dc.normB)
	default:
		return dist
	}
}

type DistanceHistogram struct {
	buckets  []int
	bounds   []float32
	count    int
	sum      float64
	sumSq    float64
	minDist  float32
	maxDist  float32
	mu       sync.Mutex
}

func NewDistanceHistogram(numBuckets int, maxDist float32) *DistanceHistogram {
	bounds := make([]float32, numBuckets)
	step := maxDist / float32(numBuckets)
	for i := range bounds {
		bounds[i] = float32(i+1) * step
	}
	return &DistanceHistogram{
		buckets: make([]int, numBuckets+1),
		bounds:  bounds,
		minDist: math.MaxFloat32,
	}
}

func (dh *DistanceHistogram) Observe(dist float32) {
	dh.mu.Lock()
	defer dh.mu.Unlock()
	dh.count++
	dh.sum += float64(dist)
	dh.sumSq += float64(dist) * float64(dist)
	if dist < dh.minDist {
		dh.minDist = dist
	}
	if dist > dh.maxDist {
		dh.maxDist = dist
	}
	bucket := len(dh.bounds)
	for i, b := range dh.bounds {
		if dist <= b {
			bucket = i
			break
		}
	}
	dh.buckets[bucket]++
}

func (dh *DistanceHistogram) Mean() float64 {
	dh.mu.Lock()
	defer dh.mu.Unlock()
	if dh.count == 0 {
		return 0
	}
	return dh.sum / float64(dh.count)
}

func (dh *DistanceHistogram) Std() float64 {
	dh.mu.Lock()
	defer dh.mu.Unlock()
	if dh.count < 2 {
		return 0
	}
	mean := dh.sum / float64(dh.count)
	variance := dh.sumSq/float64(dh.count) - mean*mean
	if variance < 0 {
		variance = 0
	}
	return math.Sqrt(variance)
}

func (dh *DistanceHistogram) Percentile(p float64) float32 {
	dh.mu.Lock()
	defer dh.mu.Unlock()
	if dh.count == 0 {
		return 0
	}
	target := int(float64(dh.count) * p)
	cumulative := 0
	for i, c := range dh.buckets {
		cumulative += c
		if cumulative >= target {
			if i < len(dh.bounds) {
				return dh.bounds[i]
			}
			return dh.maxDist
		}
	}
	return dh.maxDist
}

func (dh *DistanceHistogram) Summary() string {
	dh.mu.Lock()
	defer dh.mu.Unlock()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("DistHistogram{n=%d mean=%.4f std=%.4f min=%.4f max=%.4f}\n",
		dh.count, dh.sum/float64(dh.count+1), dh.Std(), dh.minDist, dh.maxDist))
	maxBucket := 0
	for _, c := range dh.buckets {
		if c > maxBucket {
			maxBucket = c
		}
	}
	for i, c := range dh.buckets {
		if c == 0 {
			continue
		}
		label := ""
		if i < len(dh.bounds) {
			label = fmt.Sprintf("≤%.3f", dh.bounds[i])
		} else {
			label = fmt.Sprintf(">%.3f", dh.bounds[len(dh.bounds)-1])
		}
		barLen := 0
		if maxBucket > 0 {
			barLen = c * 40 / maxBucket
		}
		bar := strings.Repeat("█", barLen)
		sb.WriteString(fmt.Sprintf("  %8s [%5d] %s\n", label, c, bar))
	}
	return sb.String()
}
