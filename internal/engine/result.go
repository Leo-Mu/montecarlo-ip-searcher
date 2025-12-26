package engine

import (
	"container/heap"
	"net/netip"
	"sync"
)

// ProbeResult holds the result of a single probe.
type ProbeResult struct {
	IP     netip.Addr
	Prefix netip.Prefix
	HeadID int

	OK        bool
	Status    int
	Error     string
	ConnectMS int64
	TLSMS     int64
	TTFBMS    int64
	TotalMS   int64
	ScoreMS   float64
	Trace     map[string]string

	// Statistics from the prefix at the time of probe
	PrefixSamples int
	PrefixOK      int
	PrefixFail    int
}

// TopResult is the public result type for output.
type TopResult struct {
	IP     netip.Addr        `json:"ip"`
	Prefix netip.Prefix      `json:"prefix"`
	OK     bool              `json:"ok"`
	Status int               `json:"status"`
	Error  string            `json:"error,omitempty"`

	ConnectMS int64             `json:"connect_ms"`
	TLSMS     int64             `json:"tls_ms"`
	TTFBMS    int64             `json:"ttfb_ms"`
	TotalMS   int64             `json:"total_ms"`
	ScoreMS   float64           `json:"score_ms"`
	Trace     map[string]string `json:"trace,omitempty"`

	DownloadOK    bool    `json:"download_ok"`
	DownloadBytes int64   `json:"download_bytes"`
	DownloadMS    int64   `json:"download_ms"`
	DownloadMbps  float64 `json:"download_mbps"`
	DownloadError string  `json:"download_error,omitempty"`

	PrefixSamples int `json:"prefix_samples"`
	PrefixOK      int `json:"prefix_ok"`
	PrefixFail    int `json:"prefix_fail"`
}

// Response holds the complete search response.
type Response struct {
	Top []TopResult `json:"top"`
}

// topNHeap is a max-heap of TopResult ordered by ScoreMS.
// We use a max-heap so we can efficiently remove the worst result when full.
type topNHeap struct {
	items []TopResult
}

func (h topNHeap) Len() int           { return len(h.items) }
func (h topNHeap) Less(i, j int) bool { return h.items[i].ScoreMS > h.items[j].ScoreMS } // max-heap
func (h topNHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }

func (h *topNHeap) Push(x interface{}) {
	h.items = append(h.items, x.(TopResult))
}

func (h *topNHeap) Pop() interface{} {
	old := h.items
	n := len(old)
	x := old[n-1]
	h.items = old[0 : n-1]
	return x
}

// TopNCollector collects and maintains the top N results efficiently using a heap.
type TopNCollector struct {
	n      int
	heap   *topNHeap
	ipSeen map[netip.Addr]int // IP -> index in heap for dedup
	mu     sync.Mutex
}

// NewTopNCollector creates a new TopN collector with heap-based storage.
func NewTopNCollector(n int) *TopNCollector {
	h := &topNHeap{items: make([]TopResult, 0, n+1)}
	heap.Init(h)
	return &TopNCollector{
		n:      n,
		heap:   h,
		ipSeen: make(map[netip.Addr]int, n),
	}
}

// Consider adds a result to the collector if it qualifies.
func (c *TopNCollector) Consider(r TopResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.n <= 0 {
		return
	}

	// Check for duplicate IP
	if idx, exists := c.ipSeen[r.IP]; exists {
		// Only update if new score is better
		if r.ScoreMS < c.heap.items[idx].ScoreMS {
			c.heap.items[idx] = r
			heap.Fix(c.heap, idx)
		}
		return
	}

	// If heap is not full, just add
	if c.heap.Len() < c.n {
		heap.Push(c.heap, r)
		c.rebuildIPMap()
		return
	}

	// Heap is full, check if new result is better than worst
	if r.ScoreMS < c.heap.items[0].ScoreMS {
		// Remove the worst
		worst := heap.Pop(c.heap).(TopResult)
		delete(c.ipSeen, worst.IP)

		// Add the new one
		heap.Push(c.heap, r)
		c.rebuildIPMap()
	}
}

// rebuildIPMap rebuilds the IP -> index map after heap modifications.
func (c *TopNCollector) rebuildIPMap() {
	c.ipSeen = make(map[netip.Addr]int, len(c.heap.items))
	for i, item := range c.heap.items {
		c.ipSeen[item.IP] = i
	}
}

// Best returns the best result so far.
func (c *TopNCollector) Best() TopResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.heap.Len() == 0 {
		return TopResult{}
	}

	// Find minimum score (best)
	best := c.heap.items[0]
	for _, item := range c.heap.items[1:] {
		if item.ScoreMS < best.ScoreMS {
			best = item
		}
	}
	return best
}

// Snapshot returns a sorted copy of all results (best first).
func (c *TopNCollector) Snapshot() []TopResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := make([]TopResult, len(c.heap.items))
	copy(result, c.heap.items)

	// Sort by ScoreMS (ascending = best first)
	for i := 0; i < len(result); i++ {
		minIdx := i
		for j := i + 1; j < len(result); j++ {
			if result[j].ScoreMS < result[minIdx].ScoreMS {
				minIdx = j
			}
		}
		result[i], result[minIdx] = result[minIdx], result[i]
	}

	return result
}

// Len returns the current number of results.
func (c *TopNCollector) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.heap.Len()
}

// ConvertToSearchTopResult converts engine.TopResult to search.TopResult format
// for backward compatibility with existing output module.
func ConvertToSearchTopResults(results []TopResult) []TopResult {
	return results
}

