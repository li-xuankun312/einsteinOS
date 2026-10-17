package vikingdb

import (
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"
)

type SIMDLevel int

const (
	SIMDNone   SIMDLevel = 0
	SIMDScalar SIMDLevel = 1
	SIMDSSE    SIMDLevel = 2
	SIMDAVX    SIMDLevel = 3
	SIMDAVX512 SIMDLevel = 4
	SIMDNEON   SIMDLevel = 5
	SIMDSVE    SIMDLevel = 6
)

func (s SIMDLevel) String() string {
	switch s {
	case SIMDNone:
		return "none"
	case SIMDScalar:
		return "scalar"
	case SIMDSSE:
		return "sse"
	case SIMDAVX:
		return "avx"
	case SIMDAVX512:
		return "avx512"
	case SIMDNEON:
		return "neon"
	case SIMDSVE:
		return "sve"
	default:
		return "unknown"
	}
}

type ArchInfo struct {
	OS       string
	Arch     string
	SIMD     SIMDLevel
	NumCPU   int
	CacheL1  int
	Optimal  int
}

func DetectArch() ArchInfo {
	info := ArchInfo{
		OS:     runtime.GOOS,
		Arch:   runtime.GOARCH,
		NumCPU: runtime.NumCPU(),
		SIMD:   SIMDScalar,
	}
	switch runtime.GOARCH {
	case "amd64":
		info.SIMD = SIMDAVX
		info.CacheL1 = 32 * 1024
		info.Optimal = 8
	case "arm64":
		info.SIMD = SIMDNEON
		info.CacheL1 = 64 * 1024
		info.Optimal = 4
	default:
		info.CacheL1 = 16 * 1024
		info.Optimal = 4
	}
	return info
}

func (ai ArchInfo) Summary() string {
	return fmt.Sprintf("Arch{%s/%s simd=%s cpus=%d cache=%dKB unroll=%d}",
		ai.OS, ai.Arch, ai.SIMD, ai.NumCPU, ai.CacheL1/1024, ai.Optimal)
}

type ArchDispatcher struct {
	arch      ArchInfo
	l2Func    func(a, b []float32) float32
	dotFunc   func(a, b []float32) float32
	cosFunc   func(a, b []float32) float32
}

func NewArchDispatcher() *ArchDispatcher {
	arch := DetectArch()
	d := &ArchDispatcher{arch: arch}
	switch {
	case arch.Optimal >= 8:
		d.l2Func = l2Squared8Wide
		d.dotFunc = dot8Wide
		d.cosFunc = cosine8Wide
	default:
		d.l2Func = l2Squared4Wide
		d.dotFunc = dot4Wide
		d.cosFunc = cosine4Wide
	}
	return d
}

func (d *ArchDispatcher) L2Squared(a, b []float32) float32 {
	return d.l2Func(a, b)
}

func (d *ArchDispatcher) DotProduct(a, b []float32) float32 {
	return d.dotFunc(a, b)
}

func (d *ArchDispatcher) Cosine(a, b []float32) float32 {
	return d.cosFunc(a, b)
}

func (d *ArchDispatcher) Arch() ArchInfo {
	return d.arch
}

func l2Squared4Wide(a, b []float32) float32 {
	n := len(a)
	if n != len(b) {
		return math.MaxFloat32
	}
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+3 < n; i += 4 {
		d0 := a[i] - b[i]
		d1 := a[i+1] - b[i+1]
		d2 := a[i+2] - b[i+2]
		d3 := a[i+3] - b[i+3]
		s0 += d0 * d0
		s1 += d1 * d1
		s2 += d2 * d2
		s3 += d3 * d3
	}
	sum := s0 + s1 + s2 + s3
	for ; i < n; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

func l2Squared8Wide(a, b []float32) float32 {
	n := len(a)
	if n != len(b) {
		return math.MaxFloat32
	}
	var s0, s1 float32
	i := 0
	for ; i+7 < n; i += 8 {
		d0 := a[i] - b[i]
		d1 := a[i+1] - b[i+1]
		d2 := a[i+2] - b[i+2]
		d3 := a[i+3] - b[i+3]
		s0 += d0*d0 + d1*d1 + d2*d2 + d3*d3
		d4 := a[i+4] - b[i+4]
		d5 := a[i+5] - b[i+5]
		d6 := a[i+6] - b[i+6]
		d7 := a[i+7] - b[i+7]
		s1 += d4*d4 + d5*d5 + d6*d6 + d7*d7
	}
	sum := s0 + s1
	for ; i < n; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

func dot4Wide(a, b []float32) float32 {
	n := len(a)
	if n != len(b) {
		return 0
	}
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+3 < n; i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	sum := s0 + s1 + s2 + s3
	for ; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}

func dot8Wide(a, b []float32) float32 {
	n := len(a)
	if n != len(b) {
		return 0
	}
	var s0, s1 float32
	i := 0
	for ; i+7 < n; i += 8 {
		s0 += a[i]*b[i] + a[i+1]*b[i+1] + a[i+2]*b[i+2] + a[i+3]*b[i+3]
		s1 += a[i+4]*b[i+4] + a[i+5]*b[i+5] + a[i+6]*b[i+6] + a[i+7]*b[i+7]
	}
	sum := s0 + s1
	for ; i < n; i++ {
		sum += a[i] * b[i]
	}
	return sum
}

func cosine4Wide(a, b []float32) float32 {
	n := len(a)
	if n != len(b) || n == 0 {
		return 2.0
	}
	var dotSum, normA, normB float32
	i := 0
	for ; i+3 < n; i += 4 {
		dotSum += a[i]*b[i] + a[i+1]*b[i+1] + a[i+2]*b[i+2] + a[i+3]*b[i+3]
		normA += a[i]*a[i] + a[i+1]*a[i+1] + a[i+2]*a[i+2] + a[i+3]*a[i+3]
		normB += b[i]*b[i] + b[i+1]*b[i+1] + b[i+2]*b[i+2] + b[i+3]*b[i+3]
	}
	for ; i < n; i++ {
		dotSum += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := float32(math.Sqrt(float64(normA)) * math.Sqrt(float64(normB)))
	if denom == 0 {
		return 2.0
	}
	return 1.0 - dotSum/denom
}

func cosine8Wide(a, b []float32) float32 {
	n := len(a)
	if n != len(b) || n == 0 {
		return 2.0
	}
	var dot0, dot1, na0, na1, nb0, nb1 float32
	i := 0
	for ; i+7 < n; i += 8 {
		dot0 += a[i]*b[i] + a[i+1]*b[i+1] + a[i+2]*b[i+2] + a[i+3]*b[i+3]
		dot1 += a[i+4]*b[i+4] + a[i+5]*b[i+5] + a[i+6]*b[i+6] + a[i+7]*b[i+7]
		na0 += a[i]*a[i] + a[i+1]*a[i+1] + a[i+2]*a[i+2] + a[i+3]*a[i+3]
		na1 += a[i+4]*a[i+4] + a[i+5]*a[i+5] + a[i+6]*a[i+6] + a[i+7]*a[i+7]
		nb0 += b[i]*b[i] + b[i+1]*b[i+1] + b[i+2]*b[i+2] + b[i+3]*b[i+3]
		nb1 += b[i+4]*b[i+4] + b[i+5]*b[i+5] + b[i+6]*b[i+6] + b[i+7]*b[i+7]
	}
	dotSum := dot0 + dot1
	normA := na0 + na1
	normB := nb0 + nb1
	for ; i < n; i++ {
		dotSum += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := float32(math.Sqrt(float64(normA)) * math.Sqrt(float64(normB)))
	if denom == 0 {
		return 2.0
	}
	return 1.0 - dotSum/denom
}

type DistanceBench struct {
	mu      sync.Mutex
	results map[string]BenchEntry
}

type BenchEntry struct {
	Name     string
	Dim      int
	Ops      int
	Duration time.Duration
	OpsPerSec float64
}

func NewDistanceBench() *DistanceBench {
	return &DistanceBench{results: make(map[string]BenchEntry)}
}

func (db *DistanceBench) Benchmark(name string, dim, ops int, fn func([]float32, []float32) float32) BenchEntry {
	a := make([]float32, dim)
	b := make([]float32, dim)
	for i := range a {
		a[i] = float32(i) * 0.01
		b[i] = float32(i) * 0.02
	}
	for i := 0; i < 100; i++ {
		fn(a, b)
	}
	start := time.Now()
	for i := 0; i < ops; i++ {
		fn(a, b)
	}
	elapsed := time.Since(start)
	entry := BenchEntry{
		Name:      name,
		Dim:       dim,
		Ops:       ops,
		Duration:  elapsed,
		OpsPerSec: float64(ops) / elapsed.Seconds(),
	}
	db.mu.Lock()
	db.results[name] = entry
	db.mu.Unlock()
	return entry
}

func (db *DistanceBench) BenchmarkAll(dim, ops int) map[string]BenchEntry {
	db.Benchmark("l2_scalar", dim, ops, l2Squared4Wide)
	db.Benchmark("l2_8wide", dim, ops, l2Squared8Wide)
	db.Benchmark("l2_16wide", dim, ops, L2SquaredSIMD4)
	db.Benchmark("dot_scalar", dim, ops, dot4Wide)
	db.Benchmark("dot_8wide", dim, ops, dot8Wide)
	db.Benchmark("dot_16wide", dim, ops, DotProductSIMD4)
	db.Benchmark("cosine_4wide", dim, ops, cosine4Wide)
	db.Benchmark("cosine_8wide", dim, ops, cosine8Wide)
	db.mu.Lock()
	defer db.mu.Unlock()
	out := make(map[string]BenchEntry)
	for k, v := range db.results {
		out[k] = v
	}
	return out
}

func (db *DistanceBench) Summary() string {
	db.mu.Lock()
	defer db.mu.Unlock()
	var sb strings.Builder
	sb.WriteString("Distance Benchmarks:\n")
	names := make([]string, 0, len(db.results))
	for name := range db.results {
		names = append(names, name)
	}
	for _, name := range names {
		e := db.results[name]
		sb.WriteString(fmt.Sprintf("  %-20s dim=%d  %.0f ops/s  (%v total)\n",
			e.Name, e.Dim, e.OpsPerSec, e.Duration.Round(time.Microsecond)))
	}
	return sb.String()
}

type AdaptiveDistancer struct {
	dispatcher *ArchDispatcher
	dim        int
	distType   string
}

func NewAdaptiveDistancer(distType string) *AdaptiveDistancer {
	return &AdaptiveDistancer{
		dispatcher: NewArchDispatcher(),
		distType:   distType,
	}
}

func (ad *AdaptiveDistancer) Distance(a, b []float32) float32 {
	switch ad.distType {
	case "l2", "l2-squared", "euclidean":
		return ad.dispatcher.L2Squared(a, b)
	case "dot", "dot-product":
		return -ad.dispatcher.DotProduct(a, b)
	case "cosine":
		return ad.dispatcher.Cosine(a, b)
	default:
		return ad.dispatcher.L2Squared(a, b)
	}
}

func (ad *AdaptiveDistancer) AsDistFunc() DistFunc {
	return func(a, b Vector) float32 {
		return ad.Distance(a, b)
	}
}

func (ad *AdaptiveDistancer) Info() string {
	return fmt.Sprintf("AdaptiveDistancer{type=%s arch=%s}", ad.distType, ad.dispatcher.Arch().Summary())
}
