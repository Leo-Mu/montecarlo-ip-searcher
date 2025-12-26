package bandit

import (
	"math"
	"math/rand"
	"net/netip"
	"sync"
)

// ThompsonSampler implements Thompson Sampling for arm selection.
// It uses posterior sampling to balance exploration and exploitation.
type ThompsonSampler struct {
	rng *rand.Rand
	mu  sync.Mutex

	// Penalty factor for failed probes when computing combined score
	failurePenalty float64

	// Timeout in milliseconds (used for score normalization)
	timeoutMS float64
}

// NewThompsonSampler creates a new Thompson Sampler.
func NewThompsonSampler(seed int64, timeoutMS float64) *ThompsonSampler {
	return &ThompsonSampler{
		rng:            rand.New(rand.NewSource(seed)),
		failurePenalty: 2.0, // Failed probes count as 2x timeout
		timeoutMS:      timeoutMS,
	}
}

// SampleScore samples a score from the arm's posterior distribution.
// Lower scores are better (represent lower latency with higher success rate).
func (s *ThompsonSampler) SampleScore(node *ArmNode) float64 {
	alpha, beta, mu, lambda, alphaNG, betaNG := node.GetPosteriorParams()
	stats := node.Stats()

	s.mu.Lock()
	defer s.mu.Unlock()

	// For nodes with very few samples, use optimistic initialization
	// This encourages exploration of unknown regions
	if stats.Samples < 3 {
		// Optimistic score: assume it could be good
		// Random value between 0 and 0.5 * timeout gives unexplored nodes a chance
		return s.rng.Float64() * s.timeoutMS * 0.5
	}

	// Sample success rate from Beta distribution
	successRate := s.sampleBeta(alpha, beta)

	// Sample latency from Normal-Gamma posterior
	precision := s.sampleGamma(alphaNG, betaNG)
	if precision <= 0 {
		precision = 0.001
	}

	// Variance of the mean estimate - higher for nodes with few samples
	variance := 1.0 / (lambda * precision)
	if variance <= 0 {
		variance = s.timeoutMS * s.timeoutMS
	}

	// Add extra variance for nodes with fewer samples (exploration bonus)
	if stats.Samples < 10 {
		explorationFactor := float64(10-stats.Samples) / 10.0
		variance *= (1 + explorationFactor*2)
	}

	latency := s.sampleNormal(mu, math.Sqrt(variance))

	// Ensure latency is positive
	if latency < 1 {
		latency = 1
	}

	// Combined score: latency + failure penalty
	failureRate := 1 - successRate
	score := latency + failureRate*s.timeoutMS*s.failurePenalty

	return score
}

// SelectBest selects the best arm from candidates using Thompson Sampling.
// Returns the selected node and its sampled score.
func (s *ThompsonSampler) SelectBest(candidates []*ArmNode) (*ArmNode, float64) {
	if len(candidates) == 0 {
		return nil, math.Inf(1)
	}

	var best *ArmNode
	bestScore := math.Inf(1)

	for _, node := range candidates {
		score := s.SampleScore(node)
		if score < bestScore {
			bestScore = score
			best = node
		}
	}

	return best, bestScore
}

// SelectBestN selects the top N arms from candidates using Thompson Sampling.
// Returns the selected nodes sorted by sampled score (best first).
func (s *ThompsonSampler) SelectBestN(candidates []*ArmNode, n int) []*ArmNode {
	if len(candidates) == 0 {
		return nil
	}

	type scored struct {
		node  *ArmNode
		score float64
	}

	scored_nodes := make([]scored, len(candidates))
	for i, node := range candidates {
		scored_nodes[i] = scored{node: node, score: s.SampleScore(node)}
	}

	// Partial sort to get top N
	for i := 0; i < n && i < len(scored_nodes); i++ {
		minIdx := i
		for j := i + 1; j < len(scored_nodes); j++ {
			if scored_nodes[j].score < scored_nodes[minIdx].score {
				minIdx = j
			}
		}
		scored_nodes[i], scored_nodes[minIdx] = scored_nodes[minIdx], scored_nodes[i]
	}

	if n > len(scored_nodes) {
		n = len(scored_nodes)
	}

	result := make([]*ArmNode, n)
	for i := 0; i < n; i++ {
		result[i] = scored_nodes[i].node
	}
	return result
}

// sampleBeta samples from a Beta(alpha, beta) distribution.
func (s *ThompsonSampler) sampleBeta(alpha, beta float64) float64 {
	// Use the gamma distribution method: Beta(a,b) = Gamma(a,1) / (Gamma(a,1) + Gamma(b,1))
	if alpha <= 0 {
		alpha = 1
	}
	if beta <= 0 {
		beta = 1
	}

	x := s.sampleGamma(alpha, 1)
	y := s.sampleGamma(beta, 1)

	if x+y == 0 {
		return 0.5
	}
	return x / (x + y)
}

// sampleGamma samples from a Gamma(alpha, beta) distribution.
// Uses the Marsaglia and Tsang method.
func (s *ThompsonSampler) sampleGamma(alpha, beta float64) float64 {
	if alpha <= 0 {
		alpha = 1
	}
	if beta <= 0 {
		beta = 1
	}

	if alpha < 1 {
		// Use the transformation: Gamma(a) = Gamma(a+1) * U^(1/a)
		u := s.rng.Float64()
		return s.sampleGamma(alpha+1, beta) * math.Pow(u, 1/alpha)
	}

	// Marsaglia and Tsang's method
	d := alpha - 1.0/3.0
	c := 1.0 / math.Sqrt(9*d)

	for {
		var x, v float64
		for {
			x = s.rng.NormFloat64()
			v = 1 + c*x
			if v > 0 {
				break
			}
		}

		v = v * v * v
		u := s.rng.Float64()

		if u < 1-0.0331*(x*x)*(x*x) {
			return d * v / beta
		}

		if math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v / beta
		}
	}
}

// sampleNormal samples from a Normal(mu, sigma) distribution.
func (s *ThompsonSampler) sampleNormal(mu, sigma float64) float64 {
	return mu + sigma*s.rng.NormFloat64()
}

// SampleIP samples a random IP address from the given prefix.
func (s *ThompsonSampler) SampleIP(prefix netip.Prefix) netip.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()

	return sampleAddrFromPrefix(prefix, s.rng)
}

// SampleUniform returns a uniform random number in [0, 1).
func (s *ThompsonSampler) SampleUniform() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rng.Float64()
}

// sampleAddrFromPrefix generates a random address within a prefix.
func sampleAddrFromPrefix(p netip.Prefix, rng *rand.Rand) netip.Addr {
	p = p.Masked()

	if p.Addr().Is4() {
		return sampleAddr4(p, rng)
	}
	return sampleAddr6(p, rng)
}

func sampleAddr4(p netip.Prefix, rng *rand.Rand) netip.Addr {
	a := p.Addr().As4()
	hostBits := 32 - p.Bits()

	if hostBits == 0 {
		return p.Addr()
	}

	base := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	mask := uint32((1 << hostBits) - 1)
	host := uint32(rng.Uint64()) & mask

	ip := base | host
	return netip.AddrFrom4([4]byte{
		byte(ip >> 24),
		byte(ip >> 16),
		byte(ip >> 8),
		byte(ip),
	})
}

func sampleAddr6(p netip.Prefix, rng *rand.Rand) netip.Addr {
	a := p.Addr().As16()
	hostBits := 128 - p.Bits()

	if hostBits == 0 {
		return p.Addr()
	}

	// Generate random host portion
	var result [16]byte
	copy(result[:], a[:])

	// Fill random bits from the end
	bitsRemaining := hostBits
	for i := 15; i >= 0 && bitsRemaining > 0; i-- {
		if bitsRemaining >= 8 {
			result[i] = byte(rng.Uint64())
			bitsRemaining -= 8
		} else {
			mask := byte((1 << bitsRemaining) - 1)
			result[i] = (result[i] & ^mask) | (byte(rng.Uint64()) & mask)
			bitsRemaining = 0
		}
	}

	return netip.AddrFrom16(result)
}
