package vikingdb

import (
	"fmt"
	"math"
	"sync"
)

func L2SquaredSIMD4(a, b []float32) float32 {
	n := len(a)
	if n != len(b) {
		return math.MaxFloat32
	}
	var sum0, sum1, sum2, sum3 float32
	i := 0
	for ; i+15 < n; i += 16 {
		d0 := a[i] - b[i]
		d1 := a[i+1] - b[i+1]
		d2 := a[i+2] - b[i+2]
		d3 := a[i+3] - b[i+3]
		sum0 += d0*d0 + d1*d1 + d2*d2 + d3*d3
		d4 := a[i+4] - b[i+4]
		d5 := a[i+5] - b[i+5]
		d6 := a[i+6] - b[i+6]
		d7 := a[i+7] - b[i+7]
		sum1 += d4*d4 + d5*d5 + d6*d6 + d7*d7
		d8 := a[i+8] - b[i+8]
		d9 := a[i+9] - b[i+9]
		d10 := a[i+10] - b[i+10]
		d11 := a[i+11] - b[i+11]
		sum2 += d8*d8 + d9*d9 + d10*d10 + d11*d11
		d12 := a[i+12] - b[i+12]
		d13 := a[i+13] - b[i+13]
		d14 := a[i+14] - b[i+14]
		d15 := a[i+15] - b[i+15]
		sum3 += d12*d12 + d13*d13 + d14*d14 + d15*d15
	}
	sum := sum0 + sum1 + sum2 + sum3
	for ; i < n; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

func DotProductSIMD4(a, b []float32) float32 {
	n := len(a)
	if n != len(b) {
		return 0
	}
	var sum0, sum1, sum2, sum3 float32
	i := 0
	for ; i+15 < n; i += 16 {
		sum0 += a[i]*b[i] + a[i+1]*b[i+1] + a[i+2]*b[i+2] + a[i+3]*b[i+3]
		sum1 += a[i+4]*b[i+4] + a[i+5]*b[i+5] + a[i+6]*b[i+6] + a[i+7]*b[i+7]
		sum2 += a[i+8]*b[i+8] + a[i+9]*b[i+9] + a[i+10]*b[i+10] + a[i+11]*b[i+11]
		sum3 += a[i+12]*b[i+12] + a[i+13]*b[i+13] + a[i+14]*b[i+14] + a[i+15]*b[i+15]
	}
	sum := sum0 + sum1 + sum2 + sum3
	for ; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}

type AngularProvider struct{}

func NewAngularProvider() AngularProvider { return AngularProvider{} }

func (p AngularProvider) New(a []float32) Distancer {
	return &angularDistancer{a: a, norm: computeNorm(a)}
}
func (p AngularProvider) SingleDist(a, b []float32) float32 { return angularDist(a, b) }
func (p AngularProvider) Step(a, b []float32) float32       { return angularDist(a, b) }
func (p AngularProvider) Wrap(x float32) float32            { return x }
func (p AngularProvider) Type() string                      { return "angular" }

type angularDistancer struct {
	a    []float32
	norm float32
}

func (d *angularDistancer) Distance(b []float32) float32 {
	return angularDist(d.a, b)
}

func angularDist(a, b []float32) float32 {
	cosine := cosineDist(a, b)
	angle := float32(math.Acos(float64(1.0 - cosine)))
	return angle / math.Pi
}

type WeightedL2Provider struct {
	weights []float32
}

func NewWeightedL2Provider(weights []float32) *WeightedL2Provider {
	return &WeightedL2Provider{weights: weights}
}

func (p *WeightedL2Provider) New(a []float32) Distancer {
	return &weightedL2Distancer{a: a, weights: p.weights}
}
func (p *WeightedL2Provider) SingleDist(a, b []float32) float32 { return p.dist(a, b) }
func (p *WeightedL2Provider) Step(a, b []float32) float32       { return p.dist(a, b) }
func (p *WeightedL2Provider) Wrap(x float32) float32            { return x }
func (p *WeightedL2Provider) Type() string                      { return "weighted-l2" }

func (p *WeightedL2Provider) dist(a, b []float32) float32 {
	if len(a) != len(b) {
		return math.MaxFloat32
	}
	var sum float32
	for i := range a {
		d := a[i] - b[i]
		w := float32(1.0)
		if i < len(p.weights) {
			w = p.weights[i]
		}
		sum += w * d * d
	}
	return sum
}

type weightedL2Distancer struct {
	a       []float32
	weights []float32
}

func (d *weightedL2Distancer) Distance(b []float32) float32 {
	if len(d.a) != len(b) {
		return math.MaxFloat32
	}
	var sum float32
	for i := range d.a {
		diff := d.a[i] - b[i]
		w := float32(1.0)
		if i < len(d.weights) {
			w = d.weights[i]
		}
		sum += w * diff * diff
	}
	return sum
}

type MinkowskiProvider struct {
	p float64
}

func NewMinkowskiProvider(p float64) MinkowskiProvider {
	return MinkowskiProvider{p: p}
}

func (mp MinkowskiProvider) New(a []float32) Distancer {
	return &minkowskiDistancer{a: a, p: mp.p}
}
func (mp MinkowskiProvider) SingleDist(a, b []float32) float32 { return minkowskiDist(a, b, mp.p) }
func (mp MinkowskiProvider) Step(a, b []float32) float32       { return minkowskiDist(a, b, mp.p) }
func (mp MinkowskiProvider) Wrap(x float32) float32            { return x }
func (mp MinkowskiProvider) Type() string                      { return "minkowski" }

type minkowskiDistancer struct {
	a []float32
	p float64
}

func (d *minkowskiDistancer) Distance(b []float32) float32 {
	return minkowskiDist(d.a, b, d.p)
}

func minkowskiDist(a, b []float32, p float64) float32 {
	if len(a) != len(b) {
		return math.MaxFloat32
	}
	var sum float64
	for i := range a {
		d := math.Abs(float64(a[i]) - float64(b[i]))
		sum += math.Pow(d, p)
	}
	return float32(math.Pow(sum, 1.0/p))
}

type DistanceBenchmark struct {
	provider DistanceProvider
	dim      int
	results  []benchResult
}

type benchResult struct {
	ops      int
	duration float64
	opsPerSec float64
}

func NewDistanceBenchmark(provider DistanceProvider, dim int) *DistanceBenchmark {
	return &DistanceBenchmark{provider: provider, dim: dim}
}

func (db *DistanceBenchmark) Run(vectors [][]float32, iterations int) float64 {
	if len(vectors) < 2 {
		return 0
	}
	query := vectors[0]
	ops := 0
	for iter := 0; iter < iterations; iter++ {
		for i := 1; i < len(vectors); i++ {
			db.provider.SingleDist(query, vectors[i])
			ops++
		}
	}
	return float64(ops)
}

type CachedDistanceProvider struct {
	inner DistanceProvider
	cache sync.Map
}

func NewCachedDistanceProvider(inner DistanceProvider) *CachedDistanceProvider {
	return &CachedDistanceProvider{inner: inner}
}

func (p *CachedDistanceProvider) New(a []float32) Distancer {
	return p.inner.New(a)
}

type distKey struct {
	a, b uintptr
}

func (p *CachedDistanceProvider) SingleDist(a, b []float32) float32 {
	return p.inner.SingleDist(a, b)
}

func (p *CachedDistanceProvider) Step(a, b []float32) float32 {
	return p.inner.Step(a, b)
}

func (p *CachedDistanceProvider) Wrap(x float32) float32 { return p.inner.Wrap(x) }
func (p *CachedDistanceProvider) Type() string            { return "cached-" + p.inner.Type() }

type MultiDistanceProvider struct {
	providers []DistanceProvider
	weights   []float32
}

func NewMultiDistanceProvider(providers []DistanceProvider, weights []float32) *MultiDistanceProvider {
	return &MultiDistanceProvider{providers: providers, weights: weights}
}

func (p *MultiDistanceProvider) New(a []float32) Distancer {
	return &multiDistancer{providers: p.providers, weights: p.weights, a: a}
}

func (p *MultiDistanceProvider) SingleDist(a, b []float32) float32 {
	var sum float32
	for i, prov := range p.providers {
		d := prov.SingleDist(a, b)
		w := float32(1.0)
		if i < len(p.weights) {
			w = p.weights[i]
		}
		sum += w * d
	}
	return sum
}

func (p *MultiDistanceProvider) Step(a, b []float32) float32 { return p.SingleDist(a, b) }
func (p *MultiDistanceProvider) Wrap(x float32) float32      { return x }
func (p *MultiDistanceProvider) Type() string                 { return "multi" }

type multiDistancer struct {
	providers []DistanceProvider
	weights   []float32
	a         []float32
}

func (d *multiDistancer) Distance(b []float32) float32 {
	var sum float32
	for i, prov := range d.providers {
		dist := prov.SingleDist(d.a, b)
		w := float32(1.0)
		if i < len(d.weights) {
			w = d.weights[i]
		}
		sum += w * dist
	}
	return sum
}

func GetExtendedDistanceProvider(name string) (DistanceProvider, error) {
	switch name {
	case "l2-squared", "l2", "euclidean":
		return NewL2SquaredProvider(), nil
	case "cosine":
		return NewCosineProvider(), nil
	case "dot", "dot-product":
		return NewDotProductProvider(), nil
	case "hamming":
		return NewHammingProvider(), nil
	case "manhattan", "l1":
		return NewManhattanProvider(), nil
	case "chebyshev", "linf":
		return NewChebyshevProvider(), nil
	case "jaccard":
		return NewJaccardProvider(), nil
	case "angular":
		return NewAngularProvider(), nil
	default:
		return nil, fmt.Errorf("unknown distance type: %s", name)
	}
}
