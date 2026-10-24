package vikingdb

import (
	"math"
	"sync"
)

type BatchDistancer struct {
	provider DistanceProvider
	query    []float32
}

func NewBatchDistancer(provider DistanceProvider, query []float32) *BatchDistancer {
	return &BatchDistancer{provider: provider, query: query}
}

func (bd *BatchDistancer) DistanceTo(vec []float32) float32 {
	return bd.provider.SingleDist(bd.query, vec)
}

func (bd *BatchDistancer) DistancesToMany(vecs [][]float32) []float32 {
	dists := make([]float32, len(vecs))
	for i, v := range vecs {
		dists[i] = bd.provider.SingleDist(bd.query, v)
	}
	return dists
}

func (bd *BatchDistancer) DistancesToManyConcurrent(vecs [][]float32, workers int) []float32 {
	if workers <= 1 || len(vecs) < 100 {
		return bd.DistancesToMany(vecs)
	}

	dists := make([]float32, len(vecs))
	chunkSize := (len(vecs) + workers - 1) / workers
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if start >= len(vecs) {
			break
		}
		if end > len(vecs) {
			end = len(vecs)
		}
		wg.Add(1)
		go func(s, e int) {
			defer wg.Done()
			for i := s; i < e; i++ {
				dists[i] = bd.provider.SingleDist(bd.query, vecs[i])
			}
		}(start, end)
	}
	wg.Wait()
	return dists
}

type QuantizedVector struct {
	Data  []uint8
	Min   float32
	Scale float32
}

func QuantizeVector(vec []float32) QuantizedVector {
	minVal := float32(math.MaxFloat32)
	maxVal := float32(-math.MaxFloat32)
	for _, v := range vec {
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}

	scale := float32(0)
	if maxVal > minVal {
		scale = 255.0 / (maxVal - minVal)
	}

	data := make([]uint8, len(vec))
	for i, v := range vec {
		data[i] = uint8((v - minVal) * scale)
	}

	return QuantizedVector{Data: data, Min: minVal, Scale: scale}
}

func (qv QuantizedVector) Dequantize() []float32 {
	vec := make([]float32, len(qv.Data))
	invScale := float32(0)
	if qv.Scale > 0 {
		invScale = 1.0 / qv.Scale
	}
	for i, d := range qv.Data {
		vec[i] = float32(d)*invScale + qv.Min
	}
	return vec
}

func QuantizedL2Distance(a, b QuantizedVector) float32 {
	if len(a.Data) != len(b.Data) {
		return math.MaxFloat32
	}
	var sum uint32
	for i := range a.Data {
		d := int32(a.Data[i]) - int32(b.Data[i])
		sum += uint32(d * d)
	}
	aInvScale := float32(1.0)
	if a.Scale > 0 {
		aInvScale = 1.0 / a.Scale
	}
	return float32(sum) * aInvScale * aInvScale
}

type ManhattanProvider struct{}

func NewManhattanProvider() ManhattanProvider { return ManhattanProvider{} }

func (p ManhattanProvider) New(a []float32) Distancer {
	return &manhattanDistancer{a: a}
}

func (p ManhattanProvider) SingleDist(a, b []float32) float32 {
	return manhattanDist(a, b)
}

func (p ManhattanProvider) Step(a, b []float32) float32 {
	return manhattanDist(a, b)
}

func (p ManhattanProvider) Wrap(x float32) float32 { return x }
func (p ManhattanProvider) Type() string            { return "manhattan" }

type manhattanDistancer struct {
	a []float32
}

func (d *manhattanDistancer) Distance(b []float32) float32 {
	return manhattanDist(d.a, b)
}

func manhattanDist(a, b []float32) float32 {
	if len(a) != len(b) {
		return math.MaxFloat32
	}
	var sum float32
	for i := range a {
		d := a[i] - b[i]
		if d < 0 {
			sum -= d
		} else {
			sum += d
		}
	}
	return sum
}

type ChebyshevProvider struct{}

func NewChebyshevProvider() ChebyshevProvider { return ChebyshevProvider{} }

func (p ChebyshevProvider) New(a []float32) Distancer {
	return &chebyshevDistancer{a: a}
}

func (p ChebyshevProvider) SingleDist(a, b []float32) float32 {
	return chebyshevDist(a, b)
}

func (p ChebyshevProvider) Step(a, b []float32) float32 {
	return chebyshevDist(a, b)
}

func (p ChebyshevProvider) Wrap(x float32) float32 { return x }
func (p ChebyshevProvider) Type() string            { return "chebyshev" }

type chebyshevDistancer struct {
	a []float32
}

func (d *chebyshevDistancer) Distance(b []float32) float32 {
	return chebyshevDist(d.a, b)
}

func chebyshevDist(a, b []float32) float32 {
	if len(a) != len(b) {
		return math.MaxFloat32
	}
	var maxD float32
	for i := range a {
		d := a[i] - b[i]
		if d < 0 {
			d = -d
		}
		if d > maxD {
			maxD = d
		}
	}
	return maxD
}

type JaccardProvider struct{}

func NewJaccardProvider() JaccardProvider { return JaccardProvider{} }

func (p JaccardProvider) New(a []float32) Distancer {
	return &jaccardDistancer{a: a}
}

func (p JaccardProvider) SingleDist(a, b []float32) float32 {
	return jaccardDist(a, b)
}

func (p JaccardProvider) Step(a, b []float32) float32 {
	return jaccardDist(a, b)
}

func (p JaccardProvider) Wrap(x float32) float32 { return x }
func (p JaccardProvider) Type() string            { return "jaccard" }

type jaccardDistancer struct {
	a []float32
}

func (d *jaccardDistancer) Distance(b []float32) float32 {
	return jaccardDist(d.a, b)
}

func jaccardDist(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 1.0
	}
	var minSum, maxSum float32
	for i := range a {
		if a[i] < b[i] {
			minSum += a[i]
			maxSum += b[i]
		} else {
			minSum += b[i]
			maxSum += a[i]
		}
	}
	if maxSum == 0 {
		return 0
	}
	return 1.0 - minSum/maxSum
}

func VectorMagnitude(v []float32) float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return float32(math.Sqrt(sum))
}

func NormalizeVector(v []float32) []float32 {
	mag := VectorMagnitude(v)
	if mag == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / mag
	}
	return out
}

func VectorAdd(a, b []float32) []float32 {
	if len(a) != len(b) {
		return nil
	}
	out := make([]float32, len(a))
	for i := range a {
		out[i] = a[i] + b[i]
	}
	return out
}

func VectorSub(a, b []float32) []float32 {
	if len(a) != len(b) {
		return nil
	}
	out := make([]float32, len(a))
	for i := range a {
		out[i] = a[i] - b[i]
	}
	return out
}

func VectorScale(v []float32, s float32) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * s
	}
	return out
}

func VectorMean(vectors [][]float32) []float32 {
	if len(vectors) == 0 {
		return nil
	}
	dim := len(vectors[0])
	mean := make([]float32, dim)
	for _, v := range vectors {
		for i, x := range v {
			mean[i] += x
		}
	}
	scale := 1.0 / float32(len(vectors))
	for i := range mean {
		mean[i] *= scale
	}
	return mean
}

func VectorVariance(vectors [][]float32) []float32 {
	if len(vectors) == 0 {
		return nil
	}
	mean := VectorMean(vectors)
	dim := len(mean)
	variance := make([]float32, dim)
	for _, v := range vectors {
		for i, x := range v {
			d := x - mean[i]
			variance[i] += d * d
		}
	}
	scale := 1.0 / float32(len(vectors))
	for i := range variance {
		variance[i] *= scale
	}
	return variance
}
