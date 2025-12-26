package bandit

import (
	"math"
	"net/netip"
	"sync"
)

// SearchHead represents a single search head in multi-head search.
// Each head maintains its own sampler and focus area for diversity.
type SearchHead struct {
	ID      int
	Sampler *ThompsonSampler

	// Current focus area (the prefix this head is exploring)
	CurrentFocus netip.Prefix

	// History of explored prefixes (for diversity computation)
	History     []netip.Prefix
	historySize int

	mu sync.RWMutex
}

// NewSearchHead creates a new search head.
func NewSearchHead(id int, seed int64, timeoutMS float64, historySize int) *SearchHead {
	return &SearchHead{
		ID:          id,
		Sampler:     NewThompsonSampler(seed, timeoutMS),
		History:     make([]netip.Prefix, 0, historySize),
		historySize: historySize,
	}
}

// SetFocus updates the current focus prefix.
func (h *SearchHead) SetFocus(prefix netip.Prefix) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.CurrentFocus = prefix
	h.History = append(h.History, prefix)

	// Keep only recent history
	if len(h.History) > h.historySize {
		h.History = h.History[len(h.History)-h.historySize:]
	}
}

// GetFocus returns the current focus prefix.
func (h *SearchHead) GetFocus() netip.Prefix {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.CurrentFocus
}

// GetHistory returns a copy of the exploration history.
func (h *SearchHead) GetHistory() []netip.Prefix {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]netip.Prefix, len(h.History))
	copy(result, h.History)
	return result
}

// HeadManager manages multiple search heads with diversity preservation.
type HeadManager struct {
	heads []*SearchHead
	mu    sync.RWMutex

	// Diversity parameters
	diversityWeight float64 // Weight for diversity penalty
	repulsionDecay  float64 // Decay factor for distance-based repulsion
}

// HeadManagerConfig holds configuration for the head manager.
type HeadManagerConfig struct {
	NumHeads        int
	TimeoutMS       float64
	BaseSeed        int64
	HistorySize     int
	DiversityWeight float64
	RepulsionDecay  float64
}

// DefaultHeadManagerConfig returns sensible defaults.
func DefaultHeadManagerConfig() HeadManagerConfig {
	return HeadManagerConfig{
		NumHeads:        4,
		TimeoutMS:       3000,
		BaseSeed:        0,
		HistorySize:     32,
		DiversityWeight: 0.3,
		RepulsionDecay:  0.5,
	}
}

// NewHeadManager creates a new head manager with the specified number of heads.
func NewHeadManager(cfg HeadManagerConfig) *HeadManager {
	heads := make([]*SearchHead, cfg.NumHeads)
	for i := 0; i < cfg.NumHeads; i++ {
		// Each head gets a different seed for independent sampling
		seed := cfg.BaseSeed + int64(i*9973)
		heads[i] = NewSearchHead(i, seed, cfg.TimeoutMS, cfg.HistorySize)
	}

	return &HeadManager{
		heads:           heads,
		diversityWeight: cfg.DiversityWeight,
		repulsionDecay:  cfg.RepulsionDecay,
	}
}

// NumHeads returns the number of search heads.
func (m *HeadManager) NumHeads() int {
	return len(m.heads)
}

// GetHead returns the head at the given index.
func (m *HeadManager) GetHead(idx int) *SearchHead {
	if idx < 0 || idx >= len(m.heads) {
		return nil
	}
	return m.heads[idx]
}

// SelectNextPrefix selects the next prefix for a head to explore,
// considering both Thompson Sampling scores and diversity penalties.
// It also gives a bonus to finer prefixes (children of good parents).
func (m *HeadManager) SelectNextPrefix(head *SearchHead, tree *ArmTree, beamWidth int) netip.Prefix {
	candidates := tree.LeafNodes()
	if len(candidates) == 0 {
		return netip.Prefix{}
	}

	// Get what other heads are currently exploring
	otherFocuses := m.getOtherHeadFocuses(head.ID)

	// Score each candidate with diversity penalty
	type scoredCandidate struct {
		node     *ArmNode
		combined float64
	}

	scored := make([]scoredCandidate, len(candidates))
	for i, node := range candidates {
		// Thompson Sampling score (lower is better)
		tsScore := head.Sampler.SampleScore(node)

		// Diversity penalty (repulsion from other heads)
		penalty := m.computeDiversityPenalty(node.Prefix, otherFocuses)

		// Depth bonus: prefer drilling into finer prefixes
		// This encourages exploitation of promising sub-regions
		depthBonus := 0.0
		bits := node.Prefix.Bits()
		if node.Prefix.Addr().Is4() {
			// For IPv4: /24 is max, /16 is starting point
			// Give up to 20% bonus for finer prefixes
			depthBonus = float64(bits-16) / 8.0 * 0.2
		} else {
			// For IPv6: /56 is max, /32 is typical starting point
			depthBonus = float64(bits-32) / 24.0 * 0.2
		}
		if depthBonus < 0 {
			depthBonus = 0
		}

		// Combined score (lower is better)
		// Apply diversity penalty and depth bonus
		combined := tsScore * (1 + m.diversityWeight*penalty) * (1 - depthBonus)

		scored[i] = scoredCandidate{
			node:     node,
			combined: combined,
		}
	}

	// Find the best candidate
	best := scored[0]
	for _, s := range scored[1:] {
		if s.combined < best.combined {
			best = s
		}
	}

	// Update head's focus
	head.SetFocus(best.node.Prefix)

	return best.node.Prefix
}

// SelectBeam selects a beam of prefixes for a head to explore.
func (m *HeadManager) SelectBeam(head *SearchHead, tree *ArmTree, beamWidth int) []netip.Prefix {
	candidates := tree.LeafNodes()
	if len(candidates) == 0 {
		return nil
	}

	otherFocuses := m.getOtherHeadFocuses(head.ID)

	// Score all candidates
	type scoredCandidate struct {
		prefix   netip.Prefix
		combined float64
	}

	scored := make([]scoredCandidate, len(candidates))
	for i, node := range candidates {
		tsScore := head.Sampler.SampleScore(node)
		penalty := m.computeDiversityPenalty(node.Prefix, otherFocuses)
		combined := tsScore * (1 + m.diversityWeight*penalty)

		scored[i] = scoredCandidate{
			prefix:   node.Prefix,
			combined: combined,
		}
	}

	// Partial sort to get top beamWidth
	for i := 0; i < beamWidth && i < len(scored); i++ {
		minIdx := i
		for j := i + 1; j < len(scored); j++ {
			if scored[j].combined < scored[minIdx].combined {
				minIdx = j
			}
		}
		scored[i], scored[minIdx] = scored[minIdx], scored[i]
	}

	if beamWidth > len(scored) {
		beamWidth = len(scored)
	}

	result := make([]netip.Prefix, beamWidth)
	for i := 0; i < beamWidth; i++ {
		result[i] = scored[i].prefix
	}

	// Update focus to best
	if len(result) > 0 {
		head.SetFocus(result[0])
	}

	return result
}

// getOtherHeadFocuses returns the current focus of all other heads.
func (m *HeadManager) getOtherHeadFocuses(excludeID int) []netip.Prefix {
	m.mu.RLock()
	defer m.mu.RUnlock()

	focuses := make([]netip.Prefix, 0, len(m.heads)-1)
	for _, head := range m.heads {
		if head.ID != excludeID {
			focus := head.GetFocus()
			if focus.IsValid() {
				focuses = append(focuses, focus)
			}
		}
	}
	return focuses
}

// computeDiversityPenalty computes a penalty based on proximity to other heads.
// Higher penalty = closer to other heads = should be avoided.
func (m *HeadManager) computeDiversityPenalty(prefix netip.Prefix, otherFocuses []netip.Prefix) float64 {
	if len(otherFocuses) == 0 {
		return 0
	}

	var totalPenalty float64
	for _, other := range otherFocuses {
		distance := prefixDistance(prefix, other)
		if distance == 0 {
			// Same prefix: maximum penalty
			totalPenalty += 1.0
		} else {
			// Inverse distance with decay
			totalPenalty += math.Pow(m.repulsionDecay, float64(distance))
		}
	}

	return totalPenalty / float64(len(otherFocuses))
}

// prefixDistance computes a distance metric between two prefixes.
// 0 = identical, larger = more different.
func prefixDistance(a, b netip.Prefix) int {
	// Different address families: maximum distance
	if a.Addr().Is4() != b.Addr().Is4() {
		return 128
	}

	// Find the common prefix length
	aBits := a.Bits()
	bBits := b.Bits()
	minBits := aBits
	if bBits < minBits {
		minBits = bBits
	}

	// Compare the network portions
	if a.Addr().Is4() {
		aBytes := a.Addr().As4()
		bBytes := b.Addr().As4()
		return compareBytes(aBytes[:], bBytes[:], minBits)
	}

	aBytes := a.Addr().As16()
	bBytes := b.Addr().As16()
	return compareBytes(aBytes[:], bBytes[:], minBits)
}

// compareBytes returns the number of matching prefix bits.
func compareBytes(a, b []byte, maxBits int) int {
	matching := 0
	for i := 0; i < len(a) && matching < maxBits; i++ {
		xor := a[i] ^ b[i]
		if xor == 0 {
			matching += 8
			if matching > maxBits {
				matching = maxBits
			}
		} else {
			// Count leading zeros in XOR
			for bit := 7; bit >= 0 && matching < maxBits; bit-- {
				if (xor>>uint(bit))&1 == 0 {
					matching++
				} else {
					break
				}
			}
			break
		}
	}

	// Distance = maxBits - matching
	return maxBits - matching
}

// RebalanceHeads reassigns heads to different areas if they've converged.
func (m *HeadManager) RebalanceHeads(tree *ArmTree) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if heads have converged (all exploring similar areas)
	focuses := make([]netip.Prefix, 0, len(m.heads))
	for _, head := range m.heads {
		focus := head.GetFocus()
		if focus.IsValid() {
			focuses = append(focuses, focus)
		}
	}

	if len(focuses) < 2 {
		return
	}

	// Compute pairwise distances
	var totalDistance int
	pairs := 0
	for i := 0; i < len(focuses); i++ {
		for j := i + 1; j < len(focuses); j++ {
			totalDistance += prefixDistance(focuses[i], focuses[j])
			pairs++
		}
	}

	if pairs == 0 {
		return
	}

	avgDistance := float64(totalDistance) / float64(pairs)

	// If average distance is too low, force rebalancing
	// Threshold: less than 4 bits of difference on average
	if avgDistance < 4 {
		leaves := tree.LeafNodes()
		if len(leaves) < len(m.heads) {
			return
		}

		// Assign each head to a different part of the search space
		for i, head := range m.heads {
			idx := (i * len(leaves)) / len(m.heads)
			head.SetFocus(leaves[idx].Prefix)
		}
	}
}

