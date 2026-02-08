package probe

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

type DownloadConfig struct {
	Timeout time.Duration
	Bytes   int64
	SNI      string
	HostName string
	Path     string
	// CustomURL indicates the user supplied a custom download URL.
	// When true, the Path is used as-is (no "?bytes=N" appended).
	CustomURL bool
}

type DownloadResult struct {
	IP      netip.Addr `json:"ip"`
	OK      bool       `json:"ok"`
	Status  int        `json:"status"`
	Error   string     `json:"error,omitempty"`
	Bytes   int64      `json:"bytes"`
	TotalMS int64      `json:"total_ms"`
	Mbps    float64    `json:"mbps"`
	When    time.Time  `json:"when"`
}

type DownloadProber struct {
	cfg    DownloadConfig
	client *http.Client
}

func NewDownloadProber(cfg DownloadConfig) *DownloadProber {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 45 * time.Second
	}
	// Default endpoint needs ?bytes=N in URL; custom URL can use Bytes==0 for "no limit".
	if cfg.Bytes <= 0 && !cfg.CustomURL {
		cfg.Bytes = 50_000_000
	}
	if cfg.SNI == "" {
		cfg.SNI = "speed.cloudflare.com"
	}
	if cfg.HostName == "" {
		cfg.HostName = "speed.cloudflare.com"
	}
	if cfg.Path == "" {
		cfg.Path = "/__down"
	}

	transport := &http.Transport{
		Proxy: nil, // critical: ignore HTTP(S)_PROXY and NO_PROXY env vars
		DialContext: (&net.Dialer{
			Timeout:   cfg.Timeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			ServerName: cfg.SNI,
		},
	}

	return &DownloadProber{
		cfg: cfg,
		client: &http.Client{
			Transport: transport,
			Timeout:   cfg.Timeout,
		},
	}
}

func (p *DownloadProber) Download(ctx context.Context, ip netip.Addr) DownloadResult {
	start := time.Now()
	out := DownloadResult{
		IP:   ip,
		When: start,
	}

	host := ip.String()
	if ip.Is6() {
		host = "[" + host + "]"
	}

	var url string
	if p.cfg.CustomURL {
		// User-supplied URL: use Path as-is (may already contain query params).
		url = "https://" + host + p.cfg.Path
	} else {
		// Default Cloudflare speed-test endpoint: append ?bytes=N.
		url = "https://" + host + p.cfg.Path + "?bytes=" + strconv.FormatInt(p.cfg.Bytes, 10)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		out.Error = err.Error()
		out.TotalMS = time.Since(start).Milliseconds()
		return out
	}
	req.Host = p.cfg.HostName
	req.Header.Set("User-Agent", "mcis/0.1")
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			out.Error = "timeout"
		} else {
			out.Error = err.Error()
		}
		out.TotalMS = time.Since(start).Milliseconds()
		return out
	}
	defer func() { _ = resp.Body.Close() }()

	out.Status = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		out.Error = fmt.Sprintf("http_status_%d", resp.StatusCode)
		out.TotalMS = time.Since(start).Milliseconds()
		return out
	}

	var n int64
	if p.cfg.Bytes == 0 {
		// No limit: read until EOF (custom URL only).
		n, err = io.Copy(io.Discard, resp.Body)
	} else {
		// Read at most cfg.Bytes.
		n, err = io.CopyN(io.Discard, resp.Body, p.cfg.Bytes)
	}
	elapsed := time.Since(start)
	out.TotalMS = elapsed.Milliseconds()
	out.Bytes = n
	if elapsed > 0 {
		out.Mbps = (float64(n) * 8) / elapsed.Seconds() / 1e6
	}

	if err != nil && !errors.Is(err, io.EOF) {
		// Normalize common timeout/cancel signals so output is stable.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			out.Error = "timeout"
		} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			out.Error = "canceled"
		} else {
			out.Error = err.Error()
		}
		return out
	}

	out.OK = true
	return out
}
