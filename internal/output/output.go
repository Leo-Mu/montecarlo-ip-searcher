package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/Leo-Mu/montecarlo-ip-searcher/internal/engine"
)

// WriteJSONL writes results as JSON Lines format.
func WriteJSONL(w io.Writer, rows []engine.TopResult) error {
	enc := json.NewEncoder(w)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// WriteCSV writes results as CSV format.
func WriteCSV(w io.Writer, rows []engine.TopResult) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	header := []string{
		"rank", "ip", "prefix",
		"ok", "status",
		"connect_ms", "tls_ms", "ttfb_ms", "total_ms",
		"score_ms", "samples_prefix", "ok_prefix", "fail_prefix",
		"download_ok", "download_mbps", "download_ms", "download_bytes", "download_error",
		"colo",
	}
	if err := cw.Write(header); err != nil {
		return err
	}

	for i, r := range rows {
		colo := ""
		if r.Trace != nil {
			colo = r.Trace["colo"]
		}
		rec := []string{
			strconv.Itoa(i + 1),
			r.IP.String(),
			r.Prefix.String(),
			strconv.FormatBool(r.OK),
			strconv.Itoa(r.Status),
			strconv.FormatInt(r.ConnectMS, 10),
			strconv.FormatInt(r.TLSMS, 10),
			strconv.FormatInt(r.TTFBMS, 10),
			strconv.FormatInt(r.TotalMS, 10),
			fmt.Sprintf("%.2f", r.ScoreMS),
			strconv.Itoa(r.PrefixSamples),
			strconv.Itoa(r.PrefixOK),
			strconv.Itoa(r.PrefixFail),
			strconv.FormatBool(r.DownloadOK),
			fmt.Sprintf("%.2f", r.DownloadMbps),
			strconv.FormatInt(r.DownloadMS, 10),
			strconv.FormatInt(r.DownloadBytes, 10),
			r.DownloadError,
			colo,
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteText writes results as human-readable text format.
func WriteText(w io.Writer, rows []engine.TopResult) error {
	// Ensure stable output
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].ScoreMS < rows[j].ScoreMS })
	for i, r := range rows {
		colo := ""
		if r.Trace != nil {
			colo = r.Trace["colo"]
		}
		dl := ""
		if r.DownloadOK || r.DownloadError != "" || r.DownloadMS != 0 || r.DownloadBytes != 0 {
			dl = fmt.Sprintf("\tdl_ok=%v\tdl_mbps=%.2f\tdl_ms=%d", r.DownloadOK, r.DownloadMbps, r.DownloadMS)
			if r.DownloadError != "" {
				dl += "\tdl_err=" + r.DownloadError
			}
		}
		_, err := fmt.Fprintf(w, "%d\t%s\t%.1fms\tok=%v\tstatus=%d\tprefix=%s\tcolo=%s%s\n",
			i+1, r.IP.String(), r.ScoreMS, r.OK, r.Status, r.Prefix.String(), colo, dl)
		if err != nil {
			return err
		}
	}
	return nil
}
