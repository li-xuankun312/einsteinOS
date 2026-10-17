package vikingdb

import (
	"fmt"
	"math"
)

type Distancer interface {
	Distance(b []float32) float32
}

type DistanceProvider interface {
	New(a []float32) Distancer
	SingleDist(a, b []float32) float32
	Step(a, b []float32) float32
	Wrap(x float32) float32
	Type() string
}

type l2Distancer struct {
	a []float32
}

func (d *l2Distancer) Distance(b []float32) float32 {
	return l2SquaredDist(d.a, b)
}

type L2SquaredProvider struct{}

func NewL2SquaredProvider() L2SquaredProvider { return L2SquaredProvider{} }

func (p L2SquaredProvider) New(a []float32) Distancer {
	return &l2Distancer{a: a}
}

func (p L2SquaredProvider) SingleDist(a, b []float32) float32 {
	return l2SquaredDist(a, b)
}

func (p L2SquaredProvider) Step(a, b []float32) float32 {
	return l2SquaredDist(a, b)
}

func (p L2SquaredProvider) Wrap(x float32) float32 { return x }
func (p L2SquaredProvider) Type() string            { return "l2-squared" }

func l2SquaredDist(a, b []float32) float32 {
	if len(a) != len(b) {
		return math.MaxFloat32
	}
	var sum float32
	l := len(a)
	i := 0
	for ; i+3 < l; i += 4 {
		d0 := a[i] - b[i]
		d1 := a[i+1] - b[i+1]
		d2 := a[i+2] - b[i+2]
		d3 := a[i+3] - b[i+3]
		sum += d0*d0 + d1*d1 + d2*d2 + d3*d3
	}
	for ; i < l; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

type cosineDistancer struct {
	a    []float32
	norm float32
}

func (d *cosineDistancer) Distance(b []float32) float32 {
	return cosineDist(d.a, b)
}

type CosineProvider struct{}

func NewCosineProvider() CosineProvider { return CosineProvider{} }

func (p CosineProvider) New(a []float32) Distancer {
	return &cosineDistancer{a: a, norm: vecNorm(a)}
}

func (p CosineProvider) SingleDist(a, b []float32) float32 {
	return cosineDist(a, b)
}

func (p CosineProvider) Step(a, b []float32) float32 {
	return cosineDist(a, b)
}

func (p CosineProvider) Wrap(x float32) float32 { return x }
func (p CosineProvider) Type() string            { return "cosine" }

func cosineDist(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 2.0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := float32(math.Sqrt(float64(normA)) * math.Sqrt(float64(normB)))
	if denom == 0 {
		return 2.0
	}
	return 1.0 - dot/denom
}

func vecNorm(a []float32) float32 {
	var sum float32
	for _, v := range a {
		sum += v * v
	}
	return float32(math.Sqrt(float64(sum)))
}

type dotDistancer struct {
	a []float32
}

func (d *dotDistancer) Distance(b []float32) float32 {
	return dotDist(d.a, b)
}

type DotProductProvider struct{}

func NewDotProductProvider() DotProductProvider { return DotProductProvider{} }

func (p DotProductProvider) New(a []float32) Distancer {
	return &dotDistancer{a: a}
}

func (p DotProductProvider) SingleDist(a, b []float32) float32 {
	return dotDist(a, b)
}

func (p DotProductProvider) Step(a, b []float32) float32 {
	return dotDist(a, b)
}

func (p DotProductProvider) Wrap(x float32) float32 { return x }
func (p DotProductProvider) Type() string            { return "dot" }

func dotDist(a, b []float32) float32 {
	if len(a) != len(b) {
		return math.MaxFloat32
	}
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return -sum
}

type hammingDistancer struct {
	a []float32
}

func (d *hammingDistancer) Distance(b []float32) float32 {
	return hammingDist(d.a, b)
}

type HammingProvider struct{}

func NewHammingProvider() HammingProvider { return HammingProvider{} }

func (p HammingProvider) New(a []float32) Distancer {
	return &hammingDistancer{a: a}
}

func (p HammingProvider) SingleDist(a, b []float32) float32 {
	return hammingDist(a, b)
}

func (p HammingProvider) Step(a, b []float32) float32 {
	return hammingDist(a, b)
}

func (p HammingProvider) Wrap(x float32) float32 { return x }
func (p HammingProvider) Type() string            { return "hamming" }

func hammingDist(a, b []float32) float32 {
	if len(a) != len(b) {
		return math.MaxFloat32
	}
	var count float32
	for i := range a {
		if a[i] != b[i] {
			count++
		}
	}
	return count
}

func GetDistanceProvider(name string) (DistanceProvider, error) {
	switch name {
	case "l2-squared", "l2", "euclidean":
		return NewL2SquaredProvider(), nil
	case "cosine":
		return NewCosineProvider(), nil
	case "dot", "dot-product":
		return NewDotProductProvider(), nil
	case "hamming":
		return NewHammingProvider(), nil
	default:
		return nil, fmt.Errorf("unknown distance type: %s", name)
	}
}
