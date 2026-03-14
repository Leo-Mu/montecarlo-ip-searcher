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

	// CSV 字段名称更清晰，便于程序解析和过滤
	header := []string{
		"rank", "ip", "prefix",
		"latency_ok", "http_code", "error",
		"connect_ms", "tls_ms", "ttfb_ms", "total_ms",
		"score_ms", "samples_prefix", "ok_prefix", "fail_prefix",
		"download_tested", "download_ok", "download_mbps", "download_ms", "download_bytes", "download_error",
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

		// 判断是否有下载测速
		downloadTested := r.DownloadOK || r.DownloadError != "" || r.DownloadMS != 0 || r.DownloadBytes != 0

		rec := []string{
			strconv.Itoa(i + 1),
			r.IP.String(),
			r.Prefix.String(),
			strconv.FormatBool(r.OK), // latency_ok: 延迟测试是否成功
			strconv.Itoa(r.Status),   // http_code: HTTP 状态码
			r.Error,                  // error: 错误信息
			strconv.FormatInt(r.ConnectMS, 10),
			strconv.FormatInt(r.TLSMS, 10),
			strconv.FormatInt(r.TTFBMS, 10),
			strconv.FormatInt(r.TotalMS, 10),
			fmt.Sprintf("%.2f", r.ScoreMS),
			strconv.Itoa(r.PrefixSamples),
			strconv.Itoa(r.PrefixOK),
			strconv.Itoa(r.PrefixFail),
			strconv.FormatBool(downloadTested), // download_tested: 是否进行了下载测速
			strconv.FormatBool(r.DownloadOK),   // download_ok: 下载测速是否成功
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

		// Build download test info string - 更清晰的状态标识
		dl := ""
		hasDownloadTest := r.DownloadOK || r.DownloadError != "" || r.DownloadMS != 0 || r.DownloadBytes != 0
		if hasDownloadTest {
			if r.DownloadOK {
				// 测速成功：显示速度和时间
				dl = fmt.Sprintf("\tdl=ok\tdl_mbps=%.2f\tdl_ms=%d\tdl_bytes=%d",
					r.DownloadMbps, r.DownloadMS, r.DownloadBytes)
			} else {
				// 测速失败：只显示失败状态和错误信息，避免无意义的0值
				dl = fmt.Sprintf("\tdl=failed")
				if r.DownloadError != "" {
					dl += fmt.Sprintf("\tdl_err=%s", r.DownloadError)
				}
			}
		}

		// 基础延迟测试状态
		latencyStatus := "ok"
		if !r.OK {
			latencyStatus = "failed"
		}

		_, err := fmt.Fprintf(w, "%d\t%s\t%.1fms\tlatency=%s\thttp_code=%d\tprefix=%s\tcolo=%s%s\n",
			i+1, r.IP.String(), r.ScoreMS, latencyStatus, r.Status, r.Prefix.String(), colo, dl)
		if err != nil {
			return err
		}
	}
	return nil
}
