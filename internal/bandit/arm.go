// Package bandit implements hierarchical multi-armed bandit algorithms
// for IP prefix optimization using Thompson Sampling.
package bandit

import (
	"math"
	"net/netip"
	"sync"
)

// ArmNode represents a single arm in the hierarchical bandit tree.
// Each node corresponds to a CIDR prefix and maintains Bayesian statistics
// for both success rate (Beta distribution) and latency (Normal-Gamma distribution).
type ArmNode struct {
	Prefix   netip.Prefix
	Parent   *ArmNode
	Children []*ArmNode

	// Beta distribution parameters for success rate modeling.
	// Prior: Alpha=1, Beta=1 (uniform)
	// Posterior: Alpha += successes, Beta += failures
	Alpha float64
	Beta  float64

	// Normal-Gamma parameters for latency modeling.
	// This is the conjugate prior for unknown mean and variance.
	// Mu: estimated mean latency
	// Lambda: precision of the mean estimate (number of observations)
	// AlphaNG, BetaNG: Gamma distribution parameters for precision
	Mu      float64
	Lambda  float64
	AlphaNG float64
	BetaNG  float64

	// Raw statistics
	Samples    int
	Successes  int
	Failures   int
	SumLatency float64
	SumSqDiff  float64 // Sum of squared differences from mean (for Welford)

	// Split state
	IsSplit bool

	mu sync.RWMutex
}

// NewArmNode creates a new arm node with uninformative priors.
func NewArmNode(prefix netip.Prefix, parent *ArmNode) *ArmNode {
	return &ArmNode{
		Prefix:   prefix.Masked(),
		Parent:   parent,
		Children: nil,

		// Uninformative Beta prior (uniform on [0,1])
		Alpha: 1.0,
		Beta:  1.0,

		// Weakly informative Normal-Gamma prior
		// Mu=0 (will be updated), Lambda=0.001 (weak prior on mean)
		// AlphaNG=1, BetaNG=1 (weakly informative on variance)
		Mu:      0,
		Lambda:  0.001,
		AlphaNG: 1.0,
		BetaNG:  1.0,
	}
}

// Update updates the arm statistics with a new probe result.
// latencyMS is the observed latency in milliseconds (ignored if success=false).
// timeoutMS is the timeout value used for failed probes.
func (a *ArmNode) Update(success bool, latencyMS float64, timeoutMS float64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.Samples++

	if success {
		a.Successes++
		a.Alpha++

		// Update Normal-Gamma posterior using Bayesian update
		// See: https://www.cs.ubc.ca/~murphyk/Papers/bayesGauss.pdf
		oldMu := a.Mu
		oldLambda := a.Lambda

		// Update precision-weighted mean
		a.Lambda = oldLambda + 1
		a.Mu = (oldLambda*oldMu + latencyMS) / a.Lambda

		// Update sum of squared differences (for variance estimation)
		a.SumLatency += latencyMS
		if a.Successes > 1 {
			// Welford's online algorithm for variance
			delta := latencyMS - oldMu
			a.SumSqDiff += delta * (latencyMS - a.Mu)
		}

		// Update Gamma parameters for precision
		a.AlphaNG += 0.5
		if a.Successes > 1 {
			a.BetaNG += 0.5 * (latencyMS - oldMu) * (latencyMS - a.Mu) * oldLambda / a.Lambda
		}
	} else {
		a.Failures++
		a.Beta++

		// For failed probes, we use the timeout as a pessimistic latency estimate
		// but with lower weight to avoid dominating the posterior
		penaltyLatency := timeoutMS * 2
		oldMu := a.Mu
		oldLambda := a.Lambda

		// Weaker update for failures (0.5 weight)
		weight := 0.5
		a.Lambda = oldLambda + weight
		a.Mu = (oldLambda*oldMu + weight*penaltyLatency) / a.Lambda
	}
}

// Stats returns a snapshot of the arm's statistics.
func (a *ArmNode) Stats() ArmStats {
	a.mu.RLock()
	defer a.mu.RUnlock()

	var variance float64
	if a.Successes > 1 {
		variance = a.SumSqDiff / float64(a.Successes-1)
	}

	successRate := a.Alpha / (a.Alpha + a.Beta)

	return ArmStats{
		Prefix:      a.Prefix,
		Samples:     a.Samples,
		Successes:   a.Successes,
		Failures:    a.Failures,
		MeanLatency: a.Mu,
		VarLatency:  variance,
		SuccessRate: successRate,
		IsSplit:     a.IsSplit,
	}
}

// GetPosteriorParams returns the posterior distribution parameters for Thompson Sampling.
func (a *ArmNode) GetPosteriorParams() (alpha, beta, mu, lambda, alphaNG, betaNG float64) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.Alpha, a.Beta, a.Mu, a.Lambda, a.AlphaNG, a.BetaNG
}

// MarkSplit marks this arm as having been split into children.
func (a *ArmNode) MarkSplit() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.IsSplit = true
}

// AddChild adds a child node to this arm.
func (a *ArmNode) AddChild(child *ArmNode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.Children = append(a.Children, child)
}

// CanSplit returns true if this arm can be split (has enough samples and isn't already split).
func (a *ArmNode) CanSplit(minSamples int, maxBitsV4, maxBitsV6 int) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.IsSplit {
		return false
	}
	if a.Samples < minSamples {
		return false
	}

	bits := a.Prefix.Bits()
	if a.Prefix.Addr().Is4() {
		return bits < maxBitsV4
	}
	return bits < maxBitsV6
}

// InformationGain estimates the potential information gain from splitting this arm.
// Higher values indicate more uncertainty that could be resolved by splitting.
func (a *ArmNode) InformationGain() float64 {
	a.mu.RLock()
	defer a.mu.RUnlock()

	if a.Samples == 0 {
		return math.Inf(1) // Unexplored arms have infinite potential
	}

	// Combine uncertainty from both success rate and latency
	// Beta distribution variance: alpha*beta / ((alpha+beta)^2 * (alpha+beta+1))
	ab := a.Alpha + a.Beta
	successVariance := (a.Alpha * a.Beta) / (ab * ab * (ab + 1))

	// Latency uncertainty (inverse of precision)
	latencyUncertainty := 1.0 / (a.Lambda + 1)

	// Weight by number of samples (more samples = more confident split decision)
	sampleWeight := math.Log(float64(a.Samples) + 1)

	return (successVariance + latencyUncertainty) * sampleWeight
}

// ArmStats holds a snapshot of arm statistics.
type ArmStats struct {
	Prefix      netip.Prefix
	Samples     int
	Successes   int
	Failures    int
	MeanLatency float64
	VarLatency  float64
	SuccessRate float64
	IsSplit     bool
}

// Score returns a deterministic score for this arm (lower is better).
// Used for ranking when not using Thompson Sampling.
func (s ArmStats) Score(timeoutMS float64) float64 {
	if s.Samples == 0 {
		return timeoutMS * 2
	}

	// Combine latency and failure rate
	failPenalty := (1 - s.SuccessRate) * timeoutMS
	return s.MeanLatency + failPenalty
}
