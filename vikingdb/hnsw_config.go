package vikingdb

import (
	"fmt"
	"math"
)

type HNSWConfigBuilder struct {
	cfg HNSWConfig
}

func NewHNSWConfigBuilder() *HNSWConfigBuilder {
	return &HNSWConfigBuilder{
		cfg: DefaultHNSWConfig(),
	}
}

func (b *HNSWConfigBuilder) WithM(m int) *HNSWConfigBuilder {
	b.cfg.M = m
	b.cfg.MMax = m
	b.cfg.MMax0 = m * 2
	b.cfg.ML = 1.0 / math.Log(float64(m))
	return b
}

func (b *HNSWConfigBuilder) WithMMax(mMax int) *HNSWConfigBuilder {
	b.cfg.MMax = mMax
	return b
}

func (b *HNSWConfigBuilder) WithMMax0(mMax0 int) *HNSWConfigBuilder {
	b.cfg.MMax0 = mMax0
	return b
}

func (b *HNSWConfigBuilder) WithEfConstruction(ef int) *HNSWConfigBuilder {
	b.cfg.EfConstruction = ef
	return b
}

func (b *HNSWConfigBuilder) WithEfSearch(ef int) *HNSWConfigBuilder {
	b.cfg.EfSearch = ef
	return b
}

func (b *HNSWConfigBuilder) WithDistFunc(fn DistFunc) *HNSWConfigBuilder {
	b.cfg.Dist = fn
	return b
}

func (b *HNSWConfigBuilder) WithDistType(distType string) *HNSWConfigBuilder {
	switch distType {
	case "l2", "l2-squared", "euclidean":
		b.cfg.Dist = func(a, b Vector) float32 { return l2SquaredDist(a, b) }
	case "cosine":
		b.cfg.Dist = func(a, b Vector) float32 { return cosineDist(a, b) }
	case "dot", "dot-product":
		b.cfg.Dist = func(a, b Vector) float32 { return dotDist(a, b) }
	case "hamming":
		b.cfg.Dist = func(a, b Vector) float32 { return hammingDist(a, b) }
	}
	return b
}

func (b *HNSWConfigBuilder) Build() HNSWConfig {
	return b.cfg
}

func (c HNSWConfig) Validate() error {
	if c.M < 2 {
		return fmt.Errorf("M must be >= 2, got %d", c.M)
	}
	if c.M > 128 {
		return fmt.Errorf("M must be <= 128, got %d", c.M)
	}
	if c.MMax < c.M {
		return fmt.Errorf("MMax (%d) must be >= M (%d)", c.MMax, c.M)
	}
	if c.MMax0 < c.M {
		return fmt.Errorf("MMax0 (%d) must be >= M (%d)", c.MMax0, c.M)
	}
	if c.EfConstruction < c.M {
		return fmt.Errorf("EfConstruction (%d) must be >= M (%d)", c.EfConstruction, c.M)
	}
	if c.EfSearch < 1 {
		return fmt.Errorf("EfSearch must be >= 1, got %d", c.EfSearch)
	}
	if c.ML <= 0 {
		return fmt.Errorf("ML must be > 0, got %f", c.ML)
	}
	if c.Dist == nil {
		return fmt.Errorf("distance function is nil")
	}
	return nil
}

func (c HNSWConfig) String() string {
	return fmt.Sprintf("HNSW{M=%d MMax=%d MMax0=%d efC=%d efS=%d ML=%.4f}",
		c.M, c.MMax, c.MMax0, c.EfConstruction, c.EfSearch, c.ML)
}

func (c HNSWConfig) EstimateMemory(numVectors, dim int) int64 {
	nodeSize := int64(dim*4 + 16)
	avgConns := int64(c.M)
	connSize := avgConns * 8
	metaOverhead := int64(200)
	perNode := nodeSize + connSize + metaOverhead
	return int64(numVectors) * perNode
}

func RecommendM(dim int) int {
	if dim <= 32 {
		return 8
	}
	if dim <= 128 {
		return 16
	}
	if dim <= 512 {
		return 32
	}
	return 48
}

func RecommendEfConstruction(m int) int {
	return m * 12
}

func RecommendEfSearch(m int) int {
	return m * 4
}

var presetConfigs = map[string]HNSWConfig{
	"fast": {
		M:              8,
		MMax:           8,
		MMax0:          16,
		EfConstruction: 64,
		EfSearch:       20,
		ML:             1.0 / math.Log(8),
		Dist:           L2Distance,
	},
	"balanced": {
		M:              16,
		MMax:           16,
		MMax0:          32,
		EfConstruction: 200,
		EfSearch:       50,
		ML:             1.0 / math.Log(16),
		Dist:           L2Distance,
	},
	"accurate": {
		M:              32,
		MMax:           32,
		MMax0:          64,
		EfConstruction: 400,
		EfSearch:       200,
		ML:             1.0 / math.Log(32),
		Dist:           L2Distance,
	},
	"large": {
		M:              48,
		MMax:           48,
		MMax0:          96,
		EfConstruction: 600,
		EfSearch:       300,
		ML:             1.0 / math.Log(48),
		Dist:           L2Distance,
	},
}

func PresetConfig(name string) (HNSWConfig, error) {
	cfg, ok := presetConfigs[name]
	if !ok {
		return HNSWConfig{}, fmt.Errorf("unknown preset: %s (available: fast, balanced, accurate, large)", name)
	}
	return cfg, nil
}
