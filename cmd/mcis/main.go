package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/dns"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/engine"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/output"
	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/probe"
)

type repeatStringFlag []string

func (r *repeatStringFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatStringFlag) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func main() {
	var (
		cidrs     repeatStringFlag
		cidrFile  string
		budget    int
		topN      int
		concur    int
		heads     int
		beam      int
		timeout   time.Duration
		host      string
		sni       string
		hostHdr   string
		path      string
		dlTop     int
		dlBytes   int64
		dlTimeout time.Duration
		dlURL     string
		outFmt    string
		outPath   string
		splitV4   int
		splitV6   int
		minSplit  int
		maxBitsV4 int
		maxBitsV6 int
		seed      int64
		verbose   bool

		// DNS upload flags
		dnsProvider    string
		dnsToken       string
		dnsZone        string
		dnsSubdomain   string
		dnsUploadCount int
		dnsTeamID      string

		// New engine parameters
		diversityWeight float64
		splitInterval   int

		// Probe rounds configuration
		rounds    int
		skipFirst int

		// Colo filter
		coloAllow   string
		coloExclude string
	)

	flag.Var(&cidrs, "cidr", "CIDR to search (repeatable). Example: 1.1.0.0/16 or 2606:4700::/32")
	flag.StringVar(&cidrFile, "cidr-file", "", "Path to a file containing CIDRs (one per line, # comment supported)")
	flag.IntVar(&budget, "budget", 2000, "Total probe budget (number of IPs to probe)")
	flag.IntVar(&topN, "top", 20, "Top N IPs to output")
	flag.IntVar(&concur, "concurrency", 200, "Probe concurrency")
	flag.IntVar(&heads, "heads", 4, "Number of search heads (diversification)")
	flag.IntVar(&beam, "beam", 32, "Beam width per head (kept candidate prefixes)")
	flag.DurationVar(&timeout, "timeout", 3*time.Second, "Per-probe timeout")
	flag.StringVar(&host, "host", "example.com", "Host name used for BOTH TLS SNI and HTTP Host header (recommended)")
	flag.StringVar(&sni, "sni", "", "TLS SNI server name (deprecated: use --host)")
	flag.StringVar(&hostHdr, "host-header", "", "HTTP Host header (deprecated: use --host)")
	flag.StringVar(&path, "path", "/cdn-cgi/trace", "HTTP path to request")
	flag.IntVar(&dlTop, "download-top", 5, "After search, run download speed test for top N IPs (0 to disable)")
	flag.Int64Var(&dlBytes, "download-bytes", 0, "Download test size in bytes; 0 = 50M for default endpoint, no limit for custom URL (default: 0)")
	flag.DurationVar(&dlTimeout, "download-timeout", 45*time.Second, "Per-IP download test timeout")
	flag.StringVar(&dlURL, "download-url", "", "Custom download test URL (e.g. https://myhost.com/path/to/file). Overrides default speed.cloudflare.com")
	flag.StringVar(&outFmt, "out", "jsonl", "Output format: jsonl|csv|text")
	flag.StringVar(&outPath, "out-file", "", "Write output to file (default: stdout)")
	flag.IntVar(&splitV4, "split-step-v4", 2, "When splitting an IPv4 prefix, increase prefix bits by this step")
	flag.IntVar(&splitV6, "split-step-v6", 4, "When splitting an IPv6 prefix, increase prefix bits by this step")
	flag.IntVar(&minSplit, "min-samples-split", 5, "Minimum samples on a prefix before it can be split")
	flag.IntVar(&maxBitsV4, "max-bits-v4", 24, "Maximum IPv4 prefix bits to drill down to")
	flag.IntVar(&maxBitsV6, "max-bits-v6", 56, "Maximum IPv6 prefix bits to drill down to")
	flag.Int64Var(&seed, "seed", 0, "Random seed (0 = time-based)")
	flag.BoolVar(&verbose, "v", false, "Verbose progress to stderr")

	// DNS upload flags
	flag.StringVar(&dnsProvider, "dns-provider", "", "DNS provider for uploading results (cloudflare|vercel)")
	flag.StringVar(&dnsToken, "dns-token", "", "DNS provider API token (or use CF_API_TOKEN/VERCEL_TOKEN env)")
	flag.StringVar(&dnsZone, "dns-zone", "", "DNS zone ID (Cloudflare) or domain (Vercel) (or use CF_ZONE_ID env)")
	flag.StringVar(&dnsSubdomain, "dns-subdomain", "", "Subdomain to update (e.g., 'cf' for cf.example.com)")
	flag.IntVar(&dnsUploadCount, "dns-upload-count", 0, "Number of IPs to upload (default: same as --download-top)")
	flag.StringVar(&dnsTeamID, "dns-team-id", "", "Vercel Team ID (optional, or use VERCEL_TEAM_ID env)")

	// New engine parameters
	flag.Float64Var(&diversityWeight, "diversity-weight", 0.3, "Weight for head diversity (0-1, higher = more exploration)")
	flag.IntVar(&splitInterval, "split-interval", 20, "Check for split opportunities every N samples")

	// Probe rounds configuration
	flag.IntVar(&rounds, "rounds", 6, "Number of probe rounds per IP (default: 6)")
	flag.IntVar(&skipFirst, "skip-first", 1, "Skip first N rounds when calculating average (default: 1, skips handshake overhead)")

	// Colo filter (CDN node filter by trace colo)
	flag.StringVar(&coloAllow, "colo", "", "Comma-separated colo whitelist; only these CDN nodes enter results (e.g. HKG,SJC)")
	flag.StringVar(&coloExclude, "colo-exclude", "", "Comma-separated colo blacklist; exclude these CDN nodes from results (e.g. LAX,DFW)")

	flag.Parse()

	// Colo: at most one of allow vs exclude
	if coloAllow != "" && coloExclude != "" {
		fmt.Fprintln(os.Stderr, "error: cannot use both --colo and --colo-exclude; use only one")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Unify host: by default use --host for both SNI and Host header.
	if sni == "" {
		sni = host
	}
	if hostHdr == "" {
		hostHdr = host
	}

	// Parse colo lists (comma-separated, trim spaces)
	parseColoList := func(s string) []string {
		if s == "" {
			return nil
		}
		parts := strings.Split(s, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}

	// Build engine config
	cfg := engine.Config{
		Budget:          budget,
		TopN:            topN,
		Concurrency:     concur,
		Heads:           heads,
		Beam:            beam,
		SplitStepV4:     splitV4,
		SplitStepV6:     splitV6,
		MinSamplesSplit: minSplit,
		MaxBitsV4:       maxBitsV4,
		MaxBitsV6:       maxBitsV6,
		Seed:            seed,
		Verbose:         verbose,
		DiversityWeight: diversityWeight,
		SplitInterval:   splitInterval,
		ColoAllow:       parseColoList(coloAllow),
		ColoBlock:       parseColoList(coloExclude),
	}

	probeCfg := probe.Config{
		Timeout:    timeout,
		SNI:        sni,
		HostHeader: hostHdr,
		Path:       path,
		Rounds:     rounds,
		SkipFirst:  skipFirst,
	}

	req := engine.Request{
		CIDRs:    []string(cidrs),
		CIDRFile: cidrFile,
		Probe:    probeCfg,
	}

	// Create and run engine
	eng := engine.New(cfg, probeCfg)
	res, err := eng.Run(ctx, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// Download speed test
	if dlTop < 0 {
		dlTop = 0
	}
	if dlTop > 0 {
		if dlTop > len(res.Top) {
			dlTop = len(res.Top)
		}
		// Default Bytes=0 (no limit); when no custom URL use 50M.
		if dlBytes == 0 && dlURL == "" {
			dlBytes = 50_000_000
		}
		dlCfg := probe.DownloadConfig{
			Timeout: dlTimeout,
			Bytes:   dlBytes,
		}
		if dlURL != "" {
			u, err := url.Parse(dlURL)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error: invalid --download-url:", err)
				os.Exit(1)
			}
			if u.Hostname() == "" {
				fmt.Fprintln(os.Stderr, "error: --download-url must include a hostname (e.g. https://myhost.com/path/to/file)")
				os.Exit(1)
			}
			dlCfg.SNI = u.Hostname()
			dlCfg.HostName = u.Hostname()
			dlCfg.Path = u.Path
			if u.RawQuery != "" {
				dlCfg.Path = u.Path + "?" + u.RawQuery
			}
			dlCfg.CustomURL = true
		}
		dlp := probe.NewDownloadProber(dlCfg)
		if verbose {
			if dlURL != "" {
				bytesDesc := fmt.Sprintf("max %d bytes", dlCfg.Bytes)
				if dlCfg.Bytes == 0 {
					bytesDesc = "full file (no limit)"
				}
				fmt.Fprintf(os.Stderr, "download: using custom URL host=%s path=%s (top %d IPs, %s)\n",
					dlCfg.HostName, dlCfg.Path, dlTop, bytesDesc)
			} else {
				fmt.Fprintf(os.Stderr, "download: using default speed.cloudflare.com/__down (top %d IPs, %d bytes)\n",
					dlTop, dlBytes)
			}
		}
		for i := 0; i < dlTop; i++ {
			r := &res.Top[i]
			dctx, dcancel := context.WithTimeout(ctx, dlTimeout)
			dr := dlp.Download(dctx, r.IP)
			dcancel()
			r.DownloadOK = dr.OK
			r.DownloadBytes = dr.Bytes
			r.DownloadMS = dr.TotalMS
			r.DownloadMbps = dr.Mbps
			r.DownloadError = dr.Error
			if verbose {
				fmt.Fprintf(os.Stderr, "download: rank=%d ip=%s ok=%v mbps=%.2f ms=%d bytes=%d err=%s\n",
					i+1, r.IP.String(), dr.OK, dr.Mbps, dr.TotalMS, dr.Bytes, dr.Error)
			}
		}
	}

	// DNS upload
	if dnsProvider != "" {
		if dnsSubdomain == "" {
			fmt.Fprintln(os.Stderr, "error: --dns-subdomain is required when --dns-provider is set")
			os.Exit(1)
		}
		if dlTop <= 0 {
			fmt.Fprintln(os.Stderr, "error: --download-top must be > 0 when using DNS upload")
			os.Exit(1)
		}

		dnsCfg := dns.Config{
			Provider:    dnsProvider,
			Token:       dnsToken,
			Zone:        dnsZone,
			Subdomain:   dnsSubdomain,
			UploadCount: dnsUploadCount,
			TeamID:      dnsTeamID,
		}

		provider, err := dns.NewProvider(dnsCfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}

		// Collect IPs from download-tested results only
		type dlResult struct {
			IP   netip.Addr
			Mbps float64
		}
		var candidates []dlResult
		for i := 0; i < dlTop && i < len(res.Top); i++ {
			r := res.Top[i]
			if r.DownloadOK {
				candidates = append(candidates, dlResult{IP: r.IP, Mbps: r.DownloadMbps})
			}
		}

		// Sort by download speed (highest first)
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].Mbps > candidates[j].Mbps
		})

		// Determine how many IPs to upload
		uploadN := dnsCfg.UploadCount
		if uploadN <= 0 {
			uploadN = dlTop
		}
		if uploadN > len(candidates) {
			uploadN = len(candidates)
		}

		// Collect IPs to upload
		var ipsToUpload []netip.Addr
		for i := 0; i < uploadN; i++ {
			ipsToUpload = append(ipsToUpload, candidates[i].IP)
		}

		if len(ipsToUpload) > 0 {
			if verbose {
				fmt.Fprintf(os.Stderr, "dns: uploading %d IPs to %s (subdomain: %s), sorted by download speed...\n",
					len(ipsToUpload), provider.Name(), dnsSubdomain)
				for i, ip := range ipsToUpload {
					fmt.Fprintf(os.Stderr, "  %d. %s (%.2f Mbps)\n", i+1, ip.String(), candidates[i].Mbps)
				}
			}
			if err := dns.Upload(ctx, provider, dnsSubdomain, ipsToUpload, verbose); err != nil {
				fmt.Fprintln(os.Stderr, "dns upload error:", err)
				os.Exit(1)
			}
		} else {
			if verbose {
				fmt.Fprintln(os.Stderr, "dns: no successful download-tested IPs to upload")
			}
		}
	}

	// Output
	var w *os.File = os.Stdout
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		defer func() {
			_ = f.Close()
		}()
		w = f
	}

	switch outFmt {
	case "jsonl":
		if err := output.WriteJSONL(w, res.Top); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "csv":
		if err := output.WriteCSV(w, res.Top); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "text":
		if err := output.WriteText(w, res.Top); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "debug":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
	default:
		fmt.Fprintln(os.Stderr, "error: unknown -out:", outFmt)
		os.Exit(1)
	}
}
