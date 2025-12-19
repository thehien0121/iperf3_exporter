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

// Package iperf provides functionality for running iperf3 tests and parsing results.
package iperf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// execCommand is a variable that allows tests to mock exec.Command.
var execCommand = exec.Command

// execCommandContext is a variable that allows tests to mock exec.CommandContext.
var execCommandContext = exec.CommandContext

// lookPath is a variable that allows tests to mock exec.LookPath.
var lookPath = exec.LookPath

// ResetExecCommand resets the execCommand variables to the default implementation.
func ResetExecCommand() {
	execCommand = exec.Command
	execCommandContext = exec.CommandContext
}

// Runner defines the interface for running iperf3 tests.
type Runner interface {
	Run(ctx context.Context, cfg Config) Result
}

// DefaultRunner is the default implementation of the Runner interface.
type DefaultRunner struct {
	Logger *slog.Logger
}

// NewRunner creates a new default iperf3 runner.
func NewRunner(logger *slog.Logger) Runner {
	return &DefaultRunner{
		Logger: logger,
	}
}

// Result represents the parsed result from an iperf3 test.
type Result struct {
	Success               bool
	SentSeconds           float64
	SentBytes             float64
	SentBitsPerSecond     float64
	ReceivedSeconds       float64
	ReceivedBytes         float64
	ReceivedBitsPerSecond float64
	// TCP-specific fields
	Retransmits float64
	// UDP-specific fields
	SentPackets         float64
	SentJitter          float64
	SentLostPackets     float64
	SentLostPercent     float64
	ReceivedPackets     float64
	ReceivedJitter      float64
	ReceivedLostPackets float64
	ReceivedLostPercent float64
	UDPMode             bool
	Error               error
}

// rawResult collects the partial result from the iperf3 run.
type rawResult struct {
	Start struct {
		TestStart struct {
			Protocol string `json:"protocol"`
		} `json:"test_start"`
	} `json:"start"`
	End struct {
		// TCP mode uses these fields
		SumSent struct {
			Seconds       float64 `json:"seconds"`
			Bytes         float64 `json:"bytes"`
			BitsPerSecond float64 `json:"bits_per_second"`
			Retransmits   float64 `json:"retransmits"`
		} `json:"sum_sent"`
		SumReceived struct {
			Seconds       float64 `json:"seconds"`
			Bytes         float64 `json:"bytes"`
			BitsPerSecond float64 `json:"bits_per_second"`
		} `json:"sum_received"`

		// UDP mode specific structure
		Streams []struct {
			UDP UDPInfo `json:"udp"`
		} `json:"streams"`
		Sum UDPInfo `json:"sum"`
	} `json:"end"`
}

// UDPInfo contains the UDP specific metrics
type UDPInfo struct {
	Socket        int     `json:"socket,omitempty"`
	Start         float64 `json:"start,omitempty"`
	End           float64 `json:"end,omitempty"`
	Seconds       float64 `json:"seconds,omitempty"`
	Bytes         float64 `json:"bytes,omitempty"`
	BitsPerSecond float64 `json:"bits_per_second,omitempty"`
	JitterMs      float64 `json:"jitter_ms,omitempty"`
	LostPackets   float64 `json:"lost_packets,omitempty"`
	Packets       float64 `json:"packets,omitempty"`
	LostPercent   float64 `json:"lost_percent,omitempty"`
	Sender        bool    `json:"sender,omitempty"`
}

// Config represents the configuration for an iperf3 test.
type Config struct {
	Target      string
	Port        int
	Period      time.Duration
	Timeout     time.Duration
	ReverseMode bool
	UDPMode     bool
	Bitrate     string
	BindAddress string // Source IP address to bind to (-B parameter) - Enhanced by ThanhDeptr
	Parallel    int    // Number of parallel streams (-P parameter) - Enhanced by ThanhDeptr
	Logger      *slog.Logger
}

var bitratePattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?([KMG])?(\/[0-9]+)?$`)

// ValidateBitrate validates the bitrate format.
func ValidateBitrate(bitrate string) bool {
	if bitrate == "" {
		return true
	}

	return bitratePattern.MatchString(bitrate)
}

// Run executes an iperf3 test with the given configuration and returns the parsed results.
// This is a convenience function that uses the DefaultRunner.
func Run(ctx context.Context, cfg Config) Result {
	runner := NewRunner(cfg.Logger)

	return runner.Run(ctx, cfg)
}

// Run executes an iperf3 test with the given configuration and returns the parsed results.
func (r *DefaultRunner) Run(ctx context.Context, cfg Config) Result {
	// Create a result with default values
	result := Result{
		Success: false,
	}

	// Validate bitrate if provided
	if cfg.Bitrate != "" && !ValidateBitrate(cfg.Bitrate) {
		result.Error = fmt.Errorf("invalid bitrate format: %s", cfg.Bitrate)
		cfg.Logger.Error("Invalid bitrate format", "bitrate", cfg.Bitrate)

		return result
	}

	// Prepare iperf3 command arguments
	iperfArgs := []string{
		"-J",
		"-t", strconv.FormatFloat(cfg.Period.Seconds(), 'f', 0, 64),
		"-c", cfg.Target,
		"-p", strconv.Itoa(cfg.Port),
	}

	if cfg.ReverseMode {
		iperfArgs = append(iperfArgs, "-R")
	}

	if cfg.UDPMode {
		iperfArgs = append(iperfArgs, "-u")
	}

	// Enhanced features by ThanhDeptr
	if cfg.BindAddress != "" {
		iperfArgs = append(iperfArgs, "-B", cfg.BindAddress)
	}

	if cfg.Parallel > 0 {
		iperfArgs = append(iperfArgs, "-P", strconv.Itoa(cfg.Parallel))
	}

	// Apply bitrate:
	// - For UDP: use specified bitrate or default to "1M" if none specified (iperf3 defaults to 1Mbps for UDP)
	// - For TCP: only apply if explicitly specified (iperf3 defaults to unlimited for TCP)
	if cfg.UDPMode {
		if cfg.Bitrate != "" {
			iperfArgs = append(iperfArgs, "-b", cfg.Bitrate)
		} else {
			// Default to 1Mbps for UDP if not specified
			iperfArgs = append(iperfArgs, "-b", "1M")
			cfg.Logger.Debug("Using default 1Mbps bitrate for UDP mode")
		}
	} else if cfg.Bitrate != "" {
		// Only apply bitrate for TCP if explicitly specified
		iperfArgs = append(iperfArgs, "-b", cfg.Bitrate)
	}

	// Create command with context
	// #nosec G204 - GetIperfCmd returns a hardcoded string and iperfArgs are validated
	var cmd *exec.Cmd
	if ctx != nil {
		// Use the mockable execCommandContext for context-aware commands
		cmd = execCommandContext(ctx, GetIperfCmd(), iperfArgs...)
	} else {
		cmd = execCommand(GetIperfCmd(), iperfArgs...)
	}

	// Execute the command with retry logic
	cfg.Logger.Debug("Running iperf3 command",
		"target", cfg.Target,
		"port", cfg.Port,
		"period", cfg.Period,
		"reverse", cfg.ReverseMode,
		"udp", cfg.UDPMode,
		"bitrate", cfg.Bitrate,
		"bind_address", cfg.BindAddress,
		"parallel", cfg.Parallel,
		"command", GetIperfCmd(),
		"args", iperfArgs,
	)

	// Retry logic for recoverable errors
	const maxRetries = 3
	const retryDelay = 3 * time.Second

	var output []byte
	var err error
	var lastCommandString string
	var lastIperf3Output string

	// **BẮT ĐẦU VÒNG LẶP THỬ LẠI**
	for attempt := 1; attempt <= maxRetries; attempt++ {
		// Luôn kiểm tra xem context đã bị hủy chưa (do scrape timeout)
		if ctx.Err() != nil {
			cfg.Logger.Warn("Probe cancelled before retry attempt, stopping.", "attempt", attempt)
			err = ctx.Err() // Gán lỗi context để báo cáo ở cuối
			break
		}

		if attempt > 1 {
			cfg.Logger.Info("Retrying iPerf3 test due to recoverable error...", 
				"attempt", attempt, 
				"max_retries", maxRetries,
				"error", err.Error(),
				"output", lastIperf3Output)
			time.Sleep(retryDelay) // Chỉ chờ nếu đây là lần thử lại
		}

		// Tạo lại command cho mỗi lần thử
		if ctx != nil {
			cmd = execCommandContext(ctx, GetIperfCmd(), iperfArgs...)
		} else {
			cmd = execCommand(GetIperfCmd(), iperfArgs...)
		}

		lastCommandString = cmd.String() // Lưu lại để ghi log
		output, err = cmd.CombinedOutput()
		lastIperf3Output = string(bytes.TrimSpace(output)) // Lưu lại output của lần chạy cuối

		// **Nếu thành công, thoát khỏi vòng lặp ngay lập tức**
		if err == nil {
			break
		}

		// **Phân tích lỗi để quyết định có thử lại hay không**
		var isRetryable bool
		
		// Check for specific iperf3 error messages
		if lastIperf3Output != "" {
			isRetryable = strings.Contains(lastIperf3Output, "Connection refused") ||
						  strings.Contains(lastIperf3Output, "the server is busy") ||
						  strings.Contains(lastIperf3Output, "Connection reset by peer")
		} else {
			// If no output but we have an error, check if it's a retryable runtime error
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				// Retry on exit code -1 (often process killed) or other recoverable codes
				isRetryable = exitErr.ExitCode() == -1 || exitErr.ExitCode() == 1
			} else {
				// For non-exit errors (like context cancelled), don't retry
				isRetryable = false
			}
		}

		// Log retry decision for debugging
		if attempt < maxRetries {
			if isRetryable {
				cfg.Logger.Debug("Error is retryable, will retry", 
					"attempt", attempt, 
					"error", err.Error(),
					"output", lastIperf3Output)
			} else {
				cfg.Logger.Debug("Error is not retryable, stopping", 
					"attempt", attempt, 
					"error", err.Error(),
					"output", lastIperf3Output)
			}
		}

		// Nếu lỗi không thể phục hồi, hoặc đã hết số lần thử, thoát vòng lặp
		if !isRetryable || attempt == maxRetries {
			break
		}
	}
	// **KẾT THÚC VÒNG LẶP THỬ LẠI**

	// Enhanced error handling with detailed categorization
	if err != nil {
		var exitErr *exec.ExitError

		// Check if this is an exit error (process exited with non-zero code)
		if errors.As(err, &exitErr) {
			// This is the case of "exit status 1" or similar
			cfg.Logger.Error(
				"iPerf3 test failed after all retries", // Updated message
				"reason", "iperf3 reported a final connection error.",
				"exit_code", exitErr.ExitCode(),
				// This line is most important, it prints detailed error from iperf3
				"iperf3_output", lastIperf3Output,
				"command", lastCommandString,
			)

			result.Error = fmt.Errorf("iperf3 execution failed with exit code %d: %s", exitErr.ExitCode(), lastIperf3Output)

			// Handle other error cases (e.g., process killed)
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			cfg.Logger.Warn(
				"iPerf3 process was killed",
				"reason", "Process was likely killed by scrape timeout.",
				"error_details", err.Error(),
				"command", lastCommandString,
			)

			result.Error = fmt.Errorf("iperf3 execution timed out or was canceled: %w", err)

		} else {
			cfg.Logger.Error(
				"Failed to execute iperf3 command",
				"reason", "An unexpected execution error occurred.",
				"error_details", err.Error(),
				"command", lastCommandString,
			)

			result.Error = fmt.Errorf("iperf3 execution failed: %w", err)
		}

		return result
	}

	// If no error, continue processing output as normal...
	out := output

	// Parse the JSON output
	var raw rawResult
	if err := json.Unmarshal(out, &raw); err != nil {
		cfg.Logger.Error("Failed to parse iperf3 result",
			"err", err,
		)

		result.Error = fmt.Errorf("failed to parse iperf3 result: %w", err)

		return result
	}

	// Set UDPMode based on user configuration
	result.UDPMode = cfg.UDPMode
	result.Success = true

	// Handle different metrics based on the protocol mode
	if !cfg.UDPMode {
		// TCP Mode - use TCP-specific JSON fields
		result.SentSeconds = raw.End.SumSent.Seconds
		result.SentBytes = raw.End.SumSent.Bytes
		result.SentBitsPerSecond = raw.End.SumSent.BitsPerSecond
		result.ReceivedSeconds = raw.End.SumReceived.Seconds
		result.ReceivedBytes = raw.End.SumReceived.Bytes
		result.ReceivedBitsPerSecond = raw.End.SumReceived.BitsPerSecond
		result.Retransmits = raw.End.SumSent.Retransmits
	} else if cfg.UDPMode {
		// UDP Mode - use UDP-specific JSON fields from streams[0].udp and sum
		// Add boundary check before accessing Streams[0]
		if len(raw.End.Streams) > 0 {
			// Common metrics using sender (streams[0].udp) data
			result.SentSeconds = raw.End.Streams[0].UDP.Seconds
			result.SentBytes = raw.End.Streams[0].UDP.Bytes
			result.SentBitsPerSecond = raw.End.Streams[0].UDP.BitsPerSecond

			// UDP-specific metrics from streams[0].udp
			result.SentPackets = raw.End.Streams[0].UDP.Packets
			result.SentJitter = raw.End.Streams[0].UDP.JitterMs
			result.SentLostPackets = raw.End.Streams[0].UDP.LostPackets
			result.SentLostPercent = raw.End.Streams[0].UDP.LostPercent
		} else {
			cfg.Logger.Warn("UDP mode: no streams found in iperf3 result")
		}

		// Common metrics using receiver (end.sum) data
		// Some versions of iperf3 might not include complete sum data for UDP
		// Access these fields safely to avoid potential issues
		result.ReceivedSeconds = raw.End.Sum.Seconds
		result.ReceivedBytes = raw.End.Sum.Bytes
		result.ReceivedBitsPerSecond = raw.End.Sum.BitsPerSecond

		// UDP-specific metrics from end.sum
		result.ReceivedPackets = raw.End.Sum.Packets
		result.ReceivedJitter = raw.End.Sum.JitterMs
		result.ReceivedLostPackets = raw.End.Sum.LostPackets
		result.ReceivedLostPercent = raw.End.Sum.LostPercent

		// Check for invalid/missing receiver metrics and log a warning
		// This can happen with some versions of iperf3
		if result.ReceivedBitsPerSecond <= 0 && result.ReceivedBytes <= 0 {
			cfg.Logger.Warn("UDP mode: missing or invalid receiver metrics in iperf3 result",
				"received_bits_per_second", result.ReceivedBitsPerSecond,
				"received_bytes", result.ReceivedBytes)
		}
	}

	// Enhanced logging with protocol-specific metrics
	if cfg.UDPMode {
		cfg.Logger.Debug("iperf3 UDP test completed successfully",
			"target", cfg.Target,
			"sent_bps", result.SentBitsPerSecond,
			"received_bps", result.ReceivedBitsPerSecond,
			"sent_jitter", result.SentJitter,
			"received_jitter", result.ReceivedJitter,
			"sent_lost_percent", result.SentLostPercent,
			"received_lost_percent", result.ReceivedLostPercent,
		)
	} else {
		cfg.Logger.Debug("iperf3 TCP test completed successfully",
			"target", cfg.Target,
			"sent_bps", result.SentBitsPerSecond,
			"received_bps", result.ReceivedBitsPerSecond,
			"retransmits", result.Retransmits,
		)
	}

	return result
}

// CheckIperf3Exists verifies that the iperf3 command exists and is executable.
func CheckIperf3Exists() error {
	_, err := lookPath(GetIperfCmd())

	return err
}
