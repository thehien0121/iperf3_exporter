// Copyright 2019 Edgard Castro
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package collector provides the Prometheus collector for iperf3 metrics.
package collector

import (
	"context"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/edgard/iperf3_exporter/internal/iperf"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	namespace = "iperf3"
)

// Metrics about the iperf3 exporter itself.
var (
	IperfDuration = prometheus.NewSummary(
		prometheus.SummaryOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "duration_seconds"),
			Help: "Duration of collections by the iperf3 exporter.",
		},
	)
	IperfErrors = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: prometheus.BuildFQName(namespace, "exporter", "errors_total"),
			Help: "Errors raised by the iperf3 exporter.",
		},
	)
)

// PingResult stores the results of a ping execution.
type PingResult struct {
	Success      bool
	PacketLoss   float64
	AvgLatencyMs float64
	MaxLatencyMs float64
	MinLatencyMs float64
}

// ProbeConfig represents the configuration for a single probe.
type ProbeConfig struct {
	Target        string
	Port          int
	Period        time.Duration
	Timeout       time.Duration
	ReverseMode   bool
	Bidirectional bool // Enhanced feature by ThanhDeptr: run both upload and download tests
	UDPMode       bool
	Bitrate       string
	BindAddress   string          // Source IP address to bind to (-B parameter)
	Parallel      int             // Number of parallel streams (-P parameter)
	Context       context.Context // Request context for proper cancellation
}

// Collector implements the prometheus.Collector interface for iperf3 and ping metrics.
type Collector struct {
	target        string
	port          int
	period        time.Duration
	timeout       time.Duration
	mutex         sync.RWMutex
	reverse       bool
	bidirectional bool
	udpMode       bool
	bitrate       string
	bindAddress   string
	parallel      int
	context       context.Context
	logger        *slog.Logger
	runner        iperf.Runner

	// --- Metrics ---
	// iPerf3
	up                    *prometheus.Desc
	bandwidthUploadMbps   *prometheus.Desc
	bandwidthDownloadMbps *prometheus.Desc
	retransmits           *prometheus.Desc // TCP only
	jitter                *prometheus.Desc // UDP only

	// Ping
	pingUp                *prometheus.Desc
	pingPacketLossPercent *prometheus.Desc
	pingLatencyAvgMs      *prometheus.Desc
	pingLatencyMaxMs      *prometheus.Desc
	pingLatencyMinMs      *prometheus.Desc
}

// NewCollector creates a new Collector for iperf3 metrics.
func NewCollector(config ProbeConfig, logger *slog.Logger) *Collector {
	return NewCollectorWithRunner(config, logger, iperf.NewRunner(logger))
}

// NewCollectorWithRunner creates a new Collector for iperf3 metrics with a custom runner.
func NewCollectorWithRunner(config ProbeConfig, logger *slog.Logger, runner iperf.Runner) *Collector {
	// Common labels for iPerf3 metrics
	iperfLabels := []string{"target", "port", "direction"}
	// Common labels for Ping metrics
	pingLabels := []string{"target"}

	return &Collector{
		target:        config.Target,
		port:          config.Port,
		period:        config.Period,
		timeout:       config.Timeout,
		reverse:       config.ReverseMode,
		bidirectional: config.Bidirectional,
		udpMode:       config.UDPMode,
		bitrate:       config.Bitrate,
		bindAddress:   config.BindAddress,
		parallel:      config.Parallel,
		context:       config.Context,
		logger:        logger,
		runner:        runner,

		// --- Define metrics with labels ---
		// iPerf3 Metrics
		up: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "up"),
			"Was the last iperf3 probe successful (1 for success, 0 for failure).",
			iperfLabels, nil,
		),
		bandwidthUploadMbps: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "bandwidth_upload_mbps"),
			"Upload bandwidth in Mbps.",
			iperfLabels, nil,
		),
		bandwidthDownloadMbps: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "bandwidth_download_mbps"),
			"Download bandwidth in Mbps.",
			iperfLabels, nil,
		),
		retransmits: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "retransmits"),
			"Total retransmits for the last TCP test run.",
			iperfLabels, nil,
		),
		jitter: prometheus.NewDesc(
			prometheus.BuildFQName(namespace, "", "jitter_ms"),
			"Jitter in milliseconds for received packets in UDP mode.",
			iperfLabels, nil,
		),
		// Ping Metrics
		pingUp: prometheus.NewDesc(
			prometheus.BuildFQName("ping", "", "up"),
			"Was the last ping probe successful (1 for success, 0 for failure).",
			pingLabels, nil,
		),
		pingPacketLossPercent: prometheus.NewDesc(
			prometheus.BuildFQName("ping", "", "packet_loss_percent"),
			"Ping packet loss in percent.",
			pingLabels, nil,
		),
		pingLatencyAvgMs: prometheus.NewDesc(
			prometheus.BuildFQName("ping", "", "latency_average_ms"),
			"Ping average latency in milliseconds.",
			pingLabels, nil,
		),
		pingLatencyMaxMs: prometheus.NewDesc(
			prometheus.BuildFQName("ping", "", "latency_maximum_ms"),
			"Ping maximum latency in milliseconds.",
			pingLabels, nil,
		),
		pingLatencyMinMs: prometheus.NewDesc(
			prometheus.BuildFQName("ping", "", "latency_minimum_ms"),
			"Ping minimum latency in milliseconds.",
			pingLabels, nil,
		),
	}
}

// Describe implements the prometheus.Collector interface.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	// iPerf3
	ch <- c.up
	ch <- c.bandwidthUploadMbps
	ch <- c.bandwidthDownloadMbps
	ch <- c.retransmits
	ch <- c.jitter

	// Ping
	ch <- c.pingUp
	ch <- c.pingPacketLossPercent
	ch <- c.pingLatencyAvgMs
	ch <- c.pingLatencyMaxMs
	ch <- c.pingLatencyMinMs
}

// Collect implements the prometheus.Collector interface.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var ctx context.Context
	var cancel context.CancelFunc
	if c.context != nil {
		ctx, cancel = context.WithTimeout(c.context, c.timeout)
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), c.timeout)
	}
	defer cancel()

	// --- Run iPerf3 Test ---
	iperfLabelValues := []string{c.target, strconv.Itoa(c.port)}
	if c.bidirectional {
		c.logger.Debug("Running bidirectional test sequentially", "target", c.target)

		uploadCtx, uploadCancel := context.WithTimeout(c.context, c.timeout)
		defer uploadCancel()
		uploadResult := c.runner.Run(uploadCtx, iperf.Config{
			Target: c.target, Port: c.port, Period: c.period, Timeout: c.timeout,
			ReverseMode: false, UDPMode: c.udpMode, Bitrate: c.bitrate,
			BindAddress: c.bindAddress, Parallel: c.parallel, Logger: c.logger,
		})
		c.processIperfResult(ch, uploadResult, append(iperfLabelValues, "upload"))

		time.Sleep(35 * time.Second)

		downloadCtx, downloadCancel := context.WithTimeout(c.context, c.timeout)
		defer downloadCancel()
		downloadResult := c.runner.Run(downloadCtx, iperf.Config{
			Target: c.target, Port: c.port, Period: c.period, Timeout: c.timeout,
			ReverseMode: true, UDPMode: c.udpMode, Bitrate: c.bitrate,
			BindAddress: c.bindAddress, Parallel: c.parallel, Logger: c.logger,
		})
		c.processIperfResult(ch, downloadResult, append(iperfLabelValues, "download"))
	} else {
		result := c.runner.Run(ctx, iperf.Config{
			Target: c.target, Port: c.port, Period: c.period, Timeout: c.timeout,
			ReverseMode: c.reverse, UDPMode: c.udpMode, Bitrate: c.bitrate,
			BindAddress: c.bindAddress, Parallel: c.parallel, Logger: c.logger,
		})
		direction := "upload"
		if c.reverse {
			direction = "download"
		}
		c.processIperfResult(ch, result, append(iperfLabelValues, direction))
	}

	// Give the network stack a moment to recover after the intense iperf3 test.
	c.logger.Debug("Pausing for 2 seconds before running ping...")
	time.Sleep(2 * time.Second)

	// --- Run Ping Test ---
	// Create a NEW, independent context for ping with its own 10-second timeout.
	c.logger.Debug("Creating a new independent context for ping test")
	pingCtx, pingCancel := context.WithTimeout(context.Background(),30*time.Second)
	defer pingCancel()

	pingLabelValues := []string{c.target}
	pingResult := c.runPing(pingCtx) // <--- Use the new pingCtx
	c.processPingResult(ch, pingResult, pingLabelValues)
}

// processIperfResult processes iperf3 results and emits metrics.
func (c *Collector) processIperfResult(ch chan<- prometheus.Metric, result iperf.Result, labelValues []string) {
	if result.Success {
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 1, labelValues...)

		// Calculate and emit bandwidth based on direction
		direction := labelValues[2] // "upload" or "download"
		if direction == "upload" {
			// For upload test: client sends data, use SentBitsPerSecond
			uploadMbps := result.SentBitsPerSecond / 1000000
			ch <- prometheus.MustNewConstMetric(c.bandwidthUploadMbps, prometheus.GaugeValue, uploadMbps, labelValues...)
		} else {
			// For download test: client receives data, use ReceivedBitsPerSecond
			downloadMbps := result.ReceivedBitsPerSecond / 1000000
			ch <- prometheus.MustNewConstMetric(c.bandwidthDownloadMbps, prometheus.GaugeValue, downloadMbps, labelValues...)
		}

		// Emit mode-specific metrics
		if result.UDPMode {
			ch <- prometheus.MustNewConstMetric(c.jitter, prometheus.GaugeValue, result.ReceivedJitter, labelValues...)
			ch <- prometheus.MustNewConstMetric(c.retransmits, prometheus.GaugeValue, 0, labelValues...) // Not applicable for UDP
		} else {
			ch <- prometheus.MustNewConstMetric(c.retransmits, prometheus.GaugeValue, result.Retransmits, labelValues...)
			ch <- prometheus.MustNewConstMetric(c.jitter, prometheus.GaugeValue, 0, labelValues...) // Not applicable for TCP
		}
	} else {
		// Emit 0 for all metrics on failure
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, 0, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.bandwidthUploadMbps, prometheus.GaugeValue, 0, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.bandwidthDownloadMbps, prometheus.GaugeValue, 0, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.retransmits, prometheus.GaugeValue, 0, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.jitter, prometheus.GaugeValue, 0, labelValues...)
		IperfErrors.Inc()
	}
}

// processPingResult processes ping results and emits metrics.
func (c *Collector) processPingResult(ch chan<- prometheus.Metric, result PingResult, labelValues []string) {
	if result.Success {
		ch <- prometheus.MustNewConstMetric(c.pingUp, prometheus.GaugeValue, 1, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.pingPacketLossPercent, prometheus.GaugeValue, result.PacketLoss, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.pingLatencyAvgMs, prometheus.GaugeValue, result.AvgLatencyMs, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.pingLatencyMaxMs, prometheus.GaugeValue, result.MaxLatencyMs, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.pingLatencyMinMs, prometheus.GaugeValue, result.MinLatencyMs, labelValues...)
	} else {
		ch <- prometheus.MustNewConstMetric(c.pingUp, prometheus.GaugeValue, 0, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.pingPacketLossPercent, prometheus.GaugeValue, 100, labelValues...) // Report 100% loss on failure
		ch <- prometheus.MustNewConstMetric(c.pingLatencyAvgMs, prometheus.GaugeValue, 0, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.pingLatencyMaxMs, prometheus.GaugeValue, 0, labelValues...)
		ch <- prometheus.MustNewConstMetric(c.pingLatencyMinMs, prometheus.GaugeValue, 0, labelValues...)
	}
}

// runPing executes the ping command and parses its output.
func (c *Collector) runPing(ctx context.Context) PingResult {
	// Use -I flag for ping if bind_address is provided (for Linux)
	args := []string{"-c", "10", "-W", "2", c.target}
	if c.bindAddress != "" {
		// Note: -I is for Linux. For macOS/BSD use -S.
		// This implementation assumes a Linux environment for the exporter.
		args = append([]string{"-I", c.bindAddress}, args...)
	}

	cmd := exec.CommandContext(ctx, "ping", args...)

	c.logger.Debug("Running ping command", "command", strings.Join(cmd.Args, " "))

	output, err := cmd.CombinedOutput()

	// ========================================================================
	// ### LOG MỚI (BẮT ĐẦU) ###
	// Log tóm tắt kết quả ping thay vì output đầy đủ để tránh log quá dài
	trimmedOutput := strings.TrimSpace(string(output))
	if err != nil {
		c.logger.Info("Ping command failed", "error", err, "output_length", len(trimmedOutput))
	} else {
		c.logger.Info("Ping command completed successfully", "output_length", len(trimmedOutput))
	}
	// ### LOG MỚI (KẾT THÚC) ###
	// ========================================================================

	if err != nil {
		// Log lỗi ngắn gọn, không in output đầy đủ
		c.logger.Error("Ping command execution failed", "error", err)
		return PingResult{Success: false}
	}

	// Truyền output đã được cắt tỉa (trimmed) vào hàm parse
	return c.parsePingOutput(trimmedOutput)
}

// parsePingOutput extracts metrics from the ping command's output string.
func (c *Collector) parsePingOutput(out string) PingResult {
	var loss float64 = 100.0 // Default to 100% loss
	var min, avg, max float64

	lossRegex := regexp.MustCompile(`(\d+(\.\d+)?)% packet loss`)
	if match := lossRegex.FindStringSubmatch(out); len(match) > 1 {
		loss, _ = strconv.ParseFloat(match[1], 64)
	}

	// Support both GNU ping (rtt min/avg/max/mdev) and BusyBox ping (round-trip min/avg/max)
	rttRegex := regexp.MustCompile(`(?:rtt|round-trip) min/avg/max(?:/mdev)? = ([\d.]+)/([\d.]+)/([\d.]+)(?:/([\d.]+))? ms`)
	if match := rttRegex.FindStringSubmatch(out); len(match) >= 4 {
		min, _ = strconv.ParseFloat(match[1], 64)
		avg, _ = strconv.ParseFloat(match[2], 64)
		max, _ = strconv.ParseFloat(match[3], 64)
	} else {
		// ========================================================================
		// ### LOG MỚI (BẮT ĐẦU) ###
		// Log tóm tắt lỗi parse thay vì output đầy đủ
		c.logger.Warn(
			"Failed to parse RTT from ping output",
			"reason", "Regex for RTT statistics did not match",
			"packet_loss_found", loss,
			"output_length", len(out),
		)
		// ### LOG MỚI (KẾT THÚC) ###
		// ========================================================================

		// If RTT line is not found, it's likely a complete failure (e.g., 100% loss)
		return PingResult{
			Success:    false, // Mark as failure
			PacketLoss: loss,
		}
	}

	c.logger.Debug("Ping results parsed", "loss", loss, "avg_ms", avg, "max_ms", max, "min_ms", min)
	return PingResult{
		Success:      true,
		PacketLoss:   loss,
		AvgLatencyMs: avg,
		MaxLatencyMs: max,
		MinLatencyMs: min,
	}
}