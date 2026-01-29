// Package engine implements the core search engine using hierarchical
// Thompson Sampling with multi-head diversity preservation.
package engine

import (
	"fmt"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/bandit"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/probe"
)

// Config holds all configuration for the search engine.
type Config struct {
	// Budget is the total number of probes to perform.
	Budget int

	// TopN is the number of top results to keep.
	TopN int

	// Concurrency is the number of parallel probe workers.
	Concurrency int

	// Heads is the number of search heads for diversity.
	Heads int

	// Beam is the width of the beam search per head.
	Beam int

	// SplitStepV4 is the prefix bits to add when splitting IPv4.
	SplitStepV4 int

	// SplitStepV6 is the prefix bits to add when splitting IPv6.
	SplitStepV6 int

	// MinSamplesSplit is the minimum samples before a prefix can be split.
	MinSamplesSplit int

	// MaxBitsV4 is the maximum prefix length for IPv4 drill-down.
	MaxBitsV4 int

	// MaxBitsV6 is the maximum prefix length for IPv6 drill-down.
	MaxBitsV6 int

	// Seed is the random seed (0 = time-based).
	Seed int64

	// Verbose enables progress output to stderr.
	Verbose bool

	// SplitInterval is how often to check for split opportunities (by samples).
	SplitInterval int

	// DiversityWeight controls how much diversity affects arm selection (0-1).
	DiversityWeight float64

	// ColoAllow is a whitelist of CDN colo codes; only results with colo in this list enter TopN. Empty = no filter.
	ColoAllow []string

	// ColoBlock is a blacklist of CDN colo codes; results with colo in this list do not enter TopN. Empty = no filter.
	ColoBlock []string
}

// Request holds the input for a search run.
type Request struct {
	// CIDRs is a list of CIDR strings to search.
	CIDRs []string

	// CIDRFile is a path to a file containing CIDRs.
	CIDRFile string

	// Probe is the probe configuration.
	Probe probe.Config
}

// DefaultConfig returns a configuration with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Budget:          2000,
		TopN:            20,
		Concurrency:     200,
		Heads:           4,
		Beam:            32,
		SplitStepV4:     2,
		SplitStepV6:     4,
		MinSamplesSplit: 5, // Lower threshold for faster drill-down
		MaxBitsV4:       24,
		MaxBitsV6:       56,
		Seed:            0,
		Verbose:         false,
		SplitInterval:   20, // Check more frequently
		DiversityWeight: 0.3,
	}
}

// Validate validates the configuration and returns an error if invalid.
func (c *Config) Validate() error {
	if c.Budget <= 0 {
		return fmt.Errorf("budget must be > 0, got %d", c.Budget)
	}
	if c.TopN <= 0 {
		return fmt.Errorf("topN must be > 0, got %d", c.TopN)
	}
	if c.Concurrency <= 0 {
		return fmt.Errorf("concurrency must be > 0, got %d", c.Concurrency)
	}
	if c.Heads <= 0 {
		return fmt.Errorf("heads must be > 0, got %d", c.Heads)
	}
	if c.Beam <= 0 {
		return fmt.Errorf("beam must be > 0, got %d", c.Beam)
	}
	if c.SplitStepV4 <= 0 || c.SplitStepV4 > 8 {
		return fmt.Errorf("splitStepV4 must be in [1,8], got %d", c.SplitStepV4)
	}
	if c.SplitStepV6 <= 0 || c.SplitStepV6 > 16 {
		return fmt.Errorf("splitStepV6 must be in [1,16], got %d", c.SplitStepV6)
	}
	if c.MinSamplesSplit <= 0 {
		return fmt.Errorf("minSamplesSplit must be > 0, got %d", c.MinSamplesSplit)
	}
	if c.MaxBitsV4 <= 0 || c.MaxBitsV4 > 32 {
		return fmt.Errorf("maxBitsV4 must be in [1,32], got %d", c.MaxBitsV4)
	}
	if c.MaxBitsV6 <= 0 || c.MaxBitsV6 > 128 {
		return fmt.Errorf("maxBitsV6 must be in [1,128], got %d", c.MaxBitsV6)
	}
	if c.DiversityWeight < 0 || c.DiversityWeight > 1 {
		return fmt.Errorf("diversityWeight must be in [0,1], got %f", c.DiversityWeight)
	}
	if len(c.ColoAllow) > 0 && len(c.ColoBlock) > 0 {
		return fmt.Errorf("cannot use both colo allow and colo exclude; use only one")
	}
	return nil
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()

	if c.Budget <= 0 {
		c.Budget = defaults.Budget
	}
	if c.TopN <= 0 {
		c.TopN = defaults.TopN
	}
	if c.Concurrency <= 0 {
		c.Concurrency = defaults.Concurrency
	}
	if c.Heads <= 0 {
		c.Heads = defaults.Heads
	}
	if c.Beam <= 0 {
		c.Beam = defaults.Beam
	}
	if c.SplitStepV4 <= 0 {
		c.SplitStepV4 = defaults.SplitStepV4
	}
	if c.SplitStepV6 <= 0 {
		c.SplitStepV6 = defaults.SplitStepV6
	}
	if c.MinSamplesSplit <= 0 {
		c.MinSamplesSplit = defaults.MinSamplesSplit
	}
	if c.MaxBitsV4 <= 0 {
		c.MaxBitsV4 = defaults.MaxBitsV4
	}
	if c.MaxBitsV6 <= 0 {
		c.MaxBitsV6 = defaults.MaxBitsV6
	}
	if c.SplitInterval <= 0 {
		c.SplitInterval = defaults.SplitInterval
	}
	if c.DiversityWeight <= 0 {
		c.DiversityWeight = defaults.DiversityWeight
	}
}

// ToTreeConfig converts to bandit.TreeConfig.
func (c *Config) ToTreeConfig() bandit.TreeConfig {
	return bandit.TreeConfig{
		SplitStepV4: c.SplitStepV4,
		SplitStepV6: c.SplitStepV6,
		MaxBitsV4:   c.MaxBitsV4,
		MaxBitsV6:   c.MaxBitsV6,
		MinSamples:  c.MinSamplesSplit,
	}
}

// ToHeadManagerConfig converts to bandit.HeadManagerConfig.
func (c *Config) ToHeadManagerConfig(timeoutMS float64) bandit.HeadManagerConfig {
	return bandit.HeadManagerConfig{
		NumHeads:        c.Heads,
		TimeoutMS:       timeoutMS,
		BaseSeed:        c.Seed,
		HistorySize:     c.Beam,
		DiversityWeight: c.DiversityWeight,
		RepulsionDecay:  0.5,
	}
}

// TimeoutMS returns the probe timeout in milliseconds.
func (r *Request) TimeoutMS() float64 {
	if r.Probe.Timeout <= 0 {
		return 3000
	}
	return float64(r.Probe.Timeout / time.Millisecond)
}
