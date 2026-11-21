package vikingdb

import (
	"math"
	"math/rand"
)

type RandomProjection struct {
	matrix   [][]float32
	inputDim int
	outputDim int
}

func NewRandomProjection(inputDim, outputDim int, seed int64) *RandomProjection {
	rng := rand.New(rand.NewSource(seed))
	scale := float32(math.Sqrt(float64(inputDim) / float64(outputDim)))
	matrix := make([][]float32, outputDim)
	for i := range matrix {
		matrix[i] = make([]float32, inputDim)
		for j := range matrix[i] {
			matrix[i][j] = float32(rng.NormFloat64()) * scale
		}
	}
	return &RandomProjection{
		matrix:    matrix,
		inputDim:  inputDim,
		outputDim: outputDim,
	}
}

func (rp *RandomProjection) Project(vec []float32) []float32 {
	out := make([]float32, rp.outputDim)
	for i := 0; i < rp.outputDim; i++ {
		var sum float32
		row := rp.matrix[i]
		j := 0
		for ; j+3 < len(row) && j+3 < len(vec); j += 4 {
			sum += row[j]*vec[j] + row[j+1]*vec[j+1] + row[j+2]*vec[j+2] + row[j+3]*vec[j+3]
		}
		for ; j < len(row) && j < len(vec); j++ {
			sum += row[j] * vec[j]
		}
		out[i] = sum
	}
	return out
}

func (rp *RandomProjection) ProjectBatch(vecs [][]float32) [][]float32 {
	out := make([][]float32, len(vecs))
	for i, v := range vecs {
		out[i] = rp.Project(v)
	}
	return out
}

func (rp *RandomProjection) InputDim() int  { return rp.inputDim }
func (rp *RandomProjection) OutputDim() int { return rp.outputDim }

type SparseRandomProjection struct {
	indices  [][]int
	signs    [][]float32
	density  float64
	inputDim int
	outputDim int
}

func NewSparseRandomProjection(inputDim, outputDim int, density float64, seed int64) *SparseRandomProjection {
	rng := rand.New(rand.NewSource(seed))
	if density <= 0 || density > 1 {
		density = 1.0 / math.Sqrt(float64(inputDim))
	}
	indices := make([][]int, outputDim)
	signs := make([][]float32, outputDim)
	for i := 0; i < outputDim; i++ {
		for j := 0; j < inputDim; j++ {
			if rng.Float64() < density {
				indices[i] = append(indices[i], j)
				if rng.Float64() < 0.5 {
					signs[i] = append(signs[i], 1.0)
				} else {
					signs[i] = append(signs[i], -1.0)
				}
			}
		}
	}
	scale := float32(math.Sqrt(1.0 / density))
	for i := range signs {
		for j := range signs[i] {
			signs[i][j] *= scale
		}
	}
	return &SparseRandomProjection{
		indices:   indices,
		signs:     signs,
		density:   density,
		inputDim:  inputDim,
		outputDim: outputDim,
	}
}

func (srp *SparseRandomProjection) Project(vec []float32) []float32 {
	out := make([]float32, srp.outputDim)
	for i := 0; i < srp.outputDim; i++ {
		var sum float32
		for k, j := range srp.indices[i] {
			if j < len(vec) {
				sum += srp.signs[i][k] * vec[j]
			}
		}
		out[i] = sum
	}
	return out
}

func (srp *SparseRandomProjection) ProjectBatch(vecs [][]float32) [][]float32 {
	out := make([][]float32, len(vecs))
	for i, v := range vecs {
		out[i] = srp.Project(v)
	}
	return out
}

func (srp *SparseRandomProjection) Sparsity() float64 {
	totalNonZero := 0
	for _, idx := range srp.indices {
		totalNonZero += len(idx)
	}
	return 1.0 - float64(totalNonZero)/float64(srp.inputDim*srp.outputDim)
}

type SimplePCA struct {
	mean       []float32
	components [][]float32
	inputDim   int
	outputDim  int
	variance   []float32
}

func NewSimplePCA(inputDim, outputDim int) *SimplePCA {
	return &SimplePCA{
		inputDim:  inputDim,
		outputDim: outputDim,
	}
}

func (pca *SimplePCA) Fit(vectors [][]float32) {
	if len(vectors) == 0 {
		return
	}
	dim := len(vectors[0])
	pca.mean = make([]float32, dim)
	for _, v := range vectors {
		for i, x := range v {
			pca.mean[i] += x
		}
	}
	n := float32(len(vectors))
	for i := range pca.mean {
		pca.mean[i] /= n
	}
	centered := make([][]float32, len(vectors))
	for i, v := range vectors {
		centered[i] = make([]float32, dim)
		for j, x := range v {
			centered[i][j] = x - pca.mean[j]
		}
	}
	pca.components = make([][]float32, pca.outputDim)
	pca.variance = make([]float32, pca.outputDim)
	for comp := 0; comp < pca.outputDim; comp++ {
		direction := make([]float32, dim)
		rng := rand.New(rand.NewSource(int64(comp * 42)))
		for i := range direction {
			direction[i] = float32(rng.NormFloat64())
		}
		direction = NormalizeVector(direction)
		for iter := 0; iter < 50; iter++ {
			newDir := make([]float32, dim)
			for _, v := range centered {
				dot := dotProd(v, direction)
				for d := 0; d < dim; d++ {
					newDir[d] += dot * v[d]
				}
			}
			direction = NormalizeVector(newDir)
		}
		var totalVar float32
		for _, v := range centered {
			proj := dotProd(v, direction)
			totalVar += proj * proj
		}
		pca.components[comp] = direction
		pca.variance[comp] = totalVar / n
		for _, v := range centered {
			proj := dotProd(v, direction)
			for d := 0; d < dim; d++ {
				v[d] -= proj * direction[d]
			}
		}
	}
}

func dotProd(a, b []float32) float32 {
	var sum float32
	for i := range a {
		if i >= len(b) {
			break
		}
		sum += a[i] * b[i]
	}
	return sum
}

func (pca *SimplePCA) Transform(vec []float32) []float32 {
	if pca.components == nil {
		return vec
	}
	centered := make([]float32, len(vec))
	for i, x := range vec {
		if i < len(pca.mean) {
			centered[i] = x - pca.mean[i]
		}
	}
	out := make([]float32, pca.outputDim)
	for i, comp := range pca.components {
		out[i] = dotProd(centered, comp)
	}
	return out
}

func (pca *SimplePCA) TransformBatch(vecs [][]float32) [][]float32 {
	out := make([][]float32, len(vecs))
	for i, v := range vecs {
		out[i] = pca.Transform(v)
	}
	return out
}

func (pca *SimplePCA) ExplainedVariance() []float32 {
	return pca.variance
}

func (pca *SimplePCA) TotalExplainedRatio() float64 {
	if len(pca.variance) == 0 {
		return 0
	}
	var total, explained float64
	for _, v := range pca.variance {
		explained += float64(v)
	}
	total = explained * float64(pca.inputDim) / float64(pca.outputDim)
	if total == 0 {
		return 0
	}
	return explained / total
}

type ProjectedDistanceProvider struct {
	projection interface{ Project([]float32) []float32 }
	inner      DistanceProvider
}

func NewProjectedDistanceProvider(proj interface{ Project([]float32) []float32 }, inner DistanceProvider) *ProjectedDistanceProvider {
	return &ProjectedDistanceProvider{projection: proj, inner: inner}
}

func (p *ProjectedDistanceProvider) New(a []float32) Distancer {
	projected := p.projection.Project(a)
	return p.inner.New(projected)
}

func (p *ProjectedDistanceProvider) SingleDist(a, b []float32) float32 {
	pa := p.projection.Project(a)
	pb := p.projection.Project(b)
	return p.inner.SingleDist(pa, pb)
}

func (p *ProjectedDistanceProvider) Step(a, b []float32) float32 {
	return p.SingleDist(a, b)
}

func (p *ProjectedDistanceProvider) Wrap(x float32) float32 { return p.inner.Wrap(x) }
func (p *ProjectedDistanceProvider) Type() string            { return "projected-" + p.inner.Type() }

type DimensionReducer struct {
	rp      *RandomProjection
	pca     *SimplePCA
	usePCA  bool
	trained bool
}

func NewDimensionReducer(inputDim, outputDim int, usePCA bool) *DimensionReducer {
	dr := &DimensionReducer{usePCA: usePCA}
	if usePCA {
		dr.pca = NewSimplePCA(inputDim, outputDim)
	} else {
		dr.rp = NewRandomProjection(inputDim, outputDim, 42)
		dr.trained = true
	}
	return dr
}

func (dr *DimensionReducer) Train(vectors [][]float32) {
	if dr.usePCA {
		dr.pca.Fit(vectors)
	}
	dr.trained = true
}

func (dr *DimensionReducer) Reduce(vec []float32) []float32 {
	if !dr.trained {
		return vec
	}
	if dr.usePCA {
		return dr.pca.Transform(vec)
	}
	return dr.rp.Project(vec)
}

func (dr *DimensionReducer) ReduceBatch(vecs [][]float32) [][]float32 {
	out := make([][]float32, len(vecs))
	for i, v := range vecs {
		out[i] = dr.Reduce(v)
	}
	return out
}
