package engine

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/bandit"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/cidr"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/probe"
)

// Engine is the core search engine using hierarchical Thompson Sampling.
type Engine struct {
	cfg      Config
	probeCfg probe.Config

	tree        *bandit.ArmTree
	headManager *bandit.HeadManager
	topN        *TopNCollector

	// Worker coordination
	tasks chan probeTask
	done  chan probeDone

	// Statistics
	submitted int64
	completed int64

	// Deduplication using atomic map
	seenIPs sync.Map
}

type probeTask struct {
	headID int
	prefix netip.Prefix
	ip     netip.Addr
}

type probeDone struct {
	task   probeTask
	result probe.Result
}

// New creates a new search engine.
func New(cfg Config, probeCfg probe.Config) *Engine {
	cfg.ApplyDefaults()
	return &Engine{
		cfg:      cfg,
		probeCfg: probeCfg,
	}
}

// Run executes the search with the given CIDRs.
func (e *Engine) Run(ctx context.Context, req Request) (Response, error) {
	if err := e.cfg.Validate(); err != nil {
		return Response{}, err
	}

	// Load prefixes
	prefixes, err := loadPrefixes(req)
	if err != nil {
		return Response{}, err
	}
	if len(prefixes) == 0 {
		return Response{}, errors.New("no CIDR provided (use --cidr or --cidr-file)")
	}

	// Initialize seed
	seed := e.cfg.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}

	// Initialize components
	timeoutMS := req.TimeoutMS()
	e.tree = bandit.NewArmTree(prefixes, e.cfg.ToTreeConfig())
	e.headManager = bandit.NewHeadManager(e.cfg.ToHeadManagerConfig(timeoutMS))
	e.topN = NewTopNCollector(e.cfg.TopN)

	// Initialize channels
	e.tasks = make(chan probeTask, e.cfg.Concurrency*2)
	e.done = make(chan probeDone, e.cfg.Concurrency*2)

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < e.cfg.Concurrency; i++ {
		wg.Add(1)
		go e.worker(ctx, &wg, req.Probe)
	}

	// Run main event-driven scheduling loop
	err = e.schedule(ctx, timeoutMS)

	// Cleanup
	close(e.tasks)
	wg.Wait()
	close(e.done)

	// Drain any remaining results
	for d := range e.done {
		e.processOneResult(d, timeoutMS)
	}

	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return Response{}, err
	}

	return Response{Top: e.topN.Snapshot()}, nil
}

// schedule is the main event-driven scheduling loop.
func (e *Engine) schedule(ctx context.Context, timeoutMS float64) error {
	start := time.Now()
	lastLog := time.Now()
	lastSplit := int64(0)

	// Initial fill - submit initial batch of tasks
	initialBatch := e.cfg.Concurrency * 2
	if initialBatch > e.cfg.Budget {
		initialBatch = e.cfg.Budget
	}

	for i := 0; i < initialBatch; i++ {
		headID := i % e.cfg.Heads
		if err := e.submitOneTask(ctx, headID); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
		}
	}

	// Main event loop - process results and submit new tasks
	for atomic.LoadInt64(&e.completed) < int64(e.cfg.Budget) {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case d := <-e.done:
			// Process the completed probe
			e.processOneResult(d, timeoutMS)
			completed := atomic.AddInt64(&e.completed, 1)

			// Check if we need to split - more aggressive splitting
			if completed-lastSplit >= int64(e.cfg.SplitInterval) {
				e.trySplit()
				lastSplit = completed
			}

			// Submit replacement task if we haven't reached budget
			submitted := atomic.LoadInt64(&e.submitted)
			if submitted < int64(e.cfg.Budget) {
				headID := int(submitted) % e.cfg.Heads
				if err := e.submitOneTask(ctx, headID); err != nil {
					// Non-fatal, continue
				}
			}

			// Verbose logging
			if e.cfg.Verbose && time.Since(lastLog) > time.Second {
				best := e.topN.Best()
				elapsed := time.Since(start).Truncate(100 * time.Millisecond)
				fmt.Fprintf(os.Stderr, "progress: %d/%d done, best=%.1fms ip=%s prefix=%s elapsed=%s nodes=%d\n",
					completed, e.cfg.Budget, best.ScoreMS, best.IP.String(), best.Prefix.String(), elapsed, e.tree.Size())
				lastLog = time.Now()
			}
		}
	}

	return nil
}

// submitOneTask submits a single probe task for a head.
func (e *Engine) submitOneTask(ctx context.Context, headID int) error {
	head := e.headManager.GetHead(headID % e.cfg.Heads)
	if head == nil {
		return nil
	}

	var prefix netip.Prefix

	// Exploitation mode: directly sample from known-good prefixes
	// This ensures we find multiple IPs from the best regions
	completed := atomic.LoadInt64(&e.completed)
	budget := int64(e.cfg.Budget)

	// Gradually increase exploitation rate as we progress
	// Early: 20% exploit, Late: 50% exploit
	exploitRate := 0.2 + 0.3*float64(completed)/float64(budget)
	if exploitRate > 0.5 {
		exploitRate = 0.5
	}

	if completed > 30 { // Only after initial exploration
		exploitPrefixes := e.getExploitationPrefixes()
		if len(exploitPrefixes) > 0 && head.Sampler != nil {
			if r := head.Sampler.SampleUniform(); r < exploitRate {
				// Pick a random prefix from exploit list, weighted toward better ones
				idx := int(r / exploitRate * float64(len(exploitPrefixes)))
				if idx >= len(exploitPrefixes) {
					idx = len(exploitPrefixes) - 1
				}
				prefix = exploitPrefixes[idx]
			}
		}
	}

	// If not exploiting, use Thompson Sampling with diversity
	if !prefix.IsValid() {
		prefix = e.headManager.SelectNextPrefix(head, e.tree, e.cfg.Beam)
	}

	if !prefix.IsValid() {
		// Fallback to any leaf
		leaves := e.tree.LeafNodes()
		if len(leaves) > 0 {
			prefix = leaves[headID%len(leaves)].Prefix
		}
	}

	if !prefix.IsValid() {
		return nil
	}

	ip := e.sampleIPWithDedup(prefix, head)

	select {
	case e.tasks <- probeTask{headID: headID, prefix: prefix, ip: ip}:
		atomic.AddInt64(&e.submitted, 1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processOneResult processes a single probe result.
func (e *Engine) processOneResult(d probeDone, timeoutMS float64) {
	// Update arm tree with result
	e.tree.Update(d.task.prefix, d.result.OK, float64(d.result.TotalMS), timeoutMS)

	// Get arm stats
	node := e.tree.GetNode(d.task.prefix)
	var stats bandit.ArmStats
	if node != nil {
		stats = node.Stats()
	}

	// Calculate score - use actual latency for success, penalty for failure
	score := float64(d.result.TotalMS)
	if !d.result.OK {
		score = timeoutMS * 2
	}

	// Add to top N
	e.topN.Consider(TopResult{
		IP:            d.task.ip,
		Prefix:        d.task.prefix,
		OK:            d.result.OK,
		Status:        d.result.Status,
		Error:         d.result.Error,
		ConnectMS:     d.result.ConnectMS,
		TLSMS:         d.result.TLSMS,
		TTFBMS:        d.result.TTFBMS,
		TotalMS:       d.result.TotalMS,
		ScoreMS:       score,
		Trace:         d.result.Trace,
		PrefixSamples: stats.Samples,
		PrefixOK:      stats.Successes,
		PrefixFail:    stats.Failures,
	})
}

// worker runs probe tasks.
func (e *Engine) worker(ctx context.Context, wg *sync.WaitGroup, probeCfg probe.Config) {
	defer wg.Done()

	prober := probe.NewProber(probeCfg)

	// Calculate timeout for multiple rounds
	rounds := probeCfg.Rounds
	if rounds <= 0 {
		rounds = 6
	}
	multiTimeout := probeCfg.Timeout * time.Duration(rounds)

	for task := range e.tasks {
		pctx, cancel := context.WithTimeout(ctx, multiTimeout)
		result := prober.ProbeHTTPTraceMulti(pctx, task.ip)
		cancel()

		select {
		case e.done <- probeDone{task: task, result: result}:
		case <-ctx.Done():
			return
		}
	}
}

// trySplit attempts to split promising prefixes.
// It prioritizes nodes with good performance (low latency, high success rate).
func (e *Engine) trySplit() {
	// Get more candidates - be more aggressive about splitting
	candidates := e.tree.GetSplitCandidates(e.cfg.Heads * 4)

	splitCount := 0
	maxSplits := e.cfg.Heads * 2

	for _, node := range candidates {
		if splitCount >= maxSplits {
			break
		}
		if e.tree.SplitNode(node) != nil {
			splitCount++
		}
	}

	// Periodically rebalance heads to explore new areas
	e.headManager.RebalanceHeads(e.tree)
}

// getExploitationPrefixes returns prefixes that deserve intensive exploitation.
// These are prefixes containing top-performing IPs that we should sample more from.
// Returns prefixes sorted by best score (best first), with repeats for weighting.
func (e *Engine) getExploitationPrefixes() []netip.Prefix {
	topResults := e.topN.Snapshot()
	if len(topResults) == 0 {
		return nil
	}

	// Calculate thresholds
	bestScore := topResults[0].ScoreMS
	tier1Threshold := bestScore * 1.2 // Within 20% of best
	tier2Threshold := bestScore * 1.5 // Within 50% of best

	// Track best score per prefix
	prefixBestScore := make(map[netip.Prefix]float64)
	for _, r := range topResults {
		if r.ScoreMS > tier2Threshold {
			break
		}
		if _, exists := prefixBestScore[r.Prefix]; !exists {
			prefixBestScore[r.Prefix] = r.ScoreMS
		}
	}

	// Build weighted list: tier1 prefixes appear 3x, tier2 appear 1x
	var exploitPrefixes []netip.Prefix
	for prefix, score := range prefixBestScore {
		if score <= tier1Threshold {
			// Best prefixes get 3x weight
			exploitPrefixes = append(exploitPrefixes, prefix, prefix, prefix)
		} else {
			// Good prefixes get 1x weight
			exploitPrefixes = append(exploitPrefixes, prefix)
		}
	}

	return exploitPrefixes
}

// sampleIPWithDedup samples an IP with deduplication.
func (e *Engine) sampleIPWithDedup(prefix netip.Prefix, head *bandit.SearchHead) netip.Addr {
	prefix = prefix.Masked()

	// Check if prefix has any host bits
	hostBits := 32 - prefix.Bits()
	if prefix.Addr().Is6() {
		hostBits = 128 - prefix.Bits()
	}

	if hostBits <= 0 {
		return prefix.Addr()
	}

	const maxTries = 32
	var last netip.Addr

	for i := 0; i < maxTries; i++ {
		ip := head.Sampler.SampleIP(prefix)
		last = ip

		// Use uint128 representation for efficient dedup
		key := ipToKey(ip)
		if _, loaded := e.seenIPs.LoadOrStore(key, struct{}{}); !loaded {
			return ip
		}
	}

	// Too many duplicates, return last sampled
	return last
}

// ipToKey converts an IP to a comparable key.
// Using the IP directly as netip.Addr is comparable and efficient.
func ipToKey(ip netip.Addr) netip.Addr {
	return ip
}

// loadPrefixes loads and deduplicates CIDR prefixes from the request.
func loadPrefixes(req Request) ([]netip.Prefix, error) {
	var pfxs []netip.Prefix

	if len(req.CIDRs) > 0 {
		ps, err := cidr.ParseCIDRs(req.CIDRs)
		if err != nil {
			return nil, err
		}
		pfxs = append(pfxs, ps...)
	}

	if req.CIDRFile != "" {
		ps, err := cidr.ReadCIDRsFromFile(req.CIDRFile)
		if err != nil {
			return nil, err
		}
		pfxs = append(pfxs, ps...)
	}

	// Deduplicate
	seen := make(map[netip.Prefix]struct{}, len(pfxs))
	unique := make([]netip.Prefix, 0, len(pfxs))
	for _, p := range pfxs {
		p = p.Masked()
		if _, exists := seen[p]; !exists {
			seen[p] = struct{}{}
			unique = append(unique, p)
		}
	}

	return unique, nil
}
