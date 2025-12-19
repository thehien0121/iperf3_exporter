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

// Package server provides the HTTP server for the iperf3 exporter.
package server

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"strconv"
	"time"

	"github.com/edgard/iperf3_exporter/internal/collector"
	"github.com/edgard/iperf3_exporter/internal/config"
	"github.com/edgard/iperf3_exporter/internal/iperf"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/exporter-toolkit/web"
)

// Server represents the HTTP server for the iperf3 exporter.
type Server struct {
	config *config.Config
	logger *slog.Logger
	server *http.Server
}

// New creates a new Server.
func New(cfg *config.Config) *Server {
	return &Server{
		config: cfg,
		logger: cfg.Logger,
	}
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	// Initialize global test lock
	InitGlobalTestLock(s.logger)

	// Register version and process collectors
	prometheus.MustRegister(versioncollector.NewCollector("iperf3_exporter"))
	prometheus.MustRegister(collectors.NewBuildInfoCollector())
	prometheus.MustRegister(collector.IperfDuration)
	prometheus.MustRegister(collector.IperfErrors)

	// Create router
	mux := http.NewServeMux()

	// Add middleware
	var handler http.Handler = mux
	handler = s.withLogging(handler)

	// Register handlers
	mux.Handle(s.config.MetricsPath, promhttp.Handler())
	mux.HandleFunc(s.config.ProbePath, s.probeHandler)
	mux.HandleFunc("/", s.indexHandler)
	mux.HandleFunc("/health", s.healthHandler)
	mux.HandleFunc("/ready", s.readyHandler)
	mux.HandleFunc("/lock-status", s.lockStatusHandler)

	// Register pprof handlers
	mux.HandleFunc("/debug/pprof/", http.DefaultServeMux.ServeHTTP)
	mux.HandleFunc("/debug/pprof/cmdline", http.DefaultServeMux.ServeHTTP)
	mux.HandleFunc("/debug/pprof/profile", http.DefaultServeMux.ServeHTTP)
	mux.HandleFunc("/debug/pprof/symbol", http.DefaultServeMux.ServeHTTP)
	mux.HandleFunc("/debug/pprof/trace", http.DefaultServeMux.ServeHTTP)
	mux.HandleFunc("/debug/pprof/heap", http.DefaultServeMux.ServeHTTP)

	// Create HTTP server
	// Increased timeouts to accommodate bidirectional tests (2 * 30s + overhead)
	s.server = &http.Server{
		Handler:      handler,
		ReadTimeout:  120 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	// Start server using exporter-toolkit
	if err := web.ListenAndServe(s.server, s.config.WebConfig, s.logger); err != nil {
		return fmt.Errorf("error starting server: %w", err)
	}

	return nil
}

// Stop stops the HTTP server.
func (s *Server) Stop(ctx context.Context) error {
	s.logger.Info("Stopping iperf3 exporter")

	return s.server.Shutdown(ctx)
}

// probeHandler handles requests to the /probe endpoint.
func (s *Server) probeHandler(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if target == "" {
		http.Error(w, "'target' parameter must be specified", http.StatusBadRequest)
		collector.IperfErrors.Inc()

		return
	}

	var targetPort int

	port := r.URL.Query().Get("port")
	if port != "" {
		var err error

		targetPort, err = strconv.Atoi(port)
		if err != nil {
			http.Error(w, fmt.Sprintf("'port' parameter must be an integer: %s", err), http.StatusBadRequest)
			collector.IperfErrors.Inc()

			return
		}
	}

	if targetPort == 0 {
		targetPort = 5201
	}

	var reverseMode bool
	var bidirectionalMode bool

	reverseParam := r.URL.Query().Get("reverse_mode")
	if reverseParam != "" {
		var err error

		reverseMode, err = strconv.ParseBool(reverseParam)
		if err != nil {
			http.Error(w, fmt.Sprintf("'reverse_mode' parameter must be true or false (boolean): %s", err), http.StatusBadRequest)
			collector.IperfErrors.Inc()

			return
		}
	}

	// Check for bidirectional mode (enhanced feature by ThanhDeptr)
	bidirectionalParam := r.URL.Query().Get("bidirectional")
	if bidirectionalParam != "" {
		var err error

		bidirectionalMode, err = strconv.ParseBool(bidirectionalParam)
		if err != nil {
			http.Error(w, fmt.Sprintf("'bidirectional' parameter must be true or false (boolean): %s", err), http.StatusBadRequest)
			collector.IperfErrors.Inc()

			return
		}
	}

	var udpMode bool

	udpModeParam := r.URL.Query().Get("udp_mode")
	if udpModeParam != "" {
		var err error

		udpMode, err = strconv.ParseBool(udpModeParam)
		if err != nil {
			http.Error(w, fmt.Sprintf("'udp_mode' parameter must be true or false (boolean): %s", err), http.StatusBadRequest)
			collector.IperfErrors.Inc()

			return
		}
	}

	bitrate := r.URL.Query().Get("bitrate")
	if bitrate != "" && !iperf.ValidateBitrate(bitrate) {
		http.Error(w, "bitrate must provided as #[KMG][/#], target bitrate in bits/sec (0 for unlimited), (default 1 Mbit/sec for UDP, unlimited for TCP) (optional slash and packet count for burst mode)", http.StatusBadRequest)
		collector.IperfErrors.Inc()

		return
	}

	// Note: In UDP mode, iperf3 requires a bitrate (defaults to 1Mbps if not specified)
	// Add a log message for clarity if udpMode is enabled but no bitrate specified
	if udpMode && bitrate == "" {
		s.logger.Info("UDP mode is enabled but no bitrate specified - iperf3 will use the default of 1Mbps")
	}

	var runPeriod time.Duration

	period := r.URL.Query().Get("period")
	if period != "" {
		var err error

		runPeriod, err = time.ParseDuration(period)
		if err != nil {
			http.Error(w, fmt.Sprintf("'period' parameter must be a duration: %s", err), http.StatusBadRequest)
			collector.IperfErrors.Inc()

			return
		}
	}

	if runPeriod.Seconds() == 0 {
		runPeriod = time.Second * 5
	}

	// If a timeout is configured via the Prometheus header, add it to the request.
	var timeoutSeconds float64

	if v := r.Header.Get("X-Prometheus-Scrape-Timeout-Seconds"); v != "" {
		var err error

		timeoutSeconds, err = strconv.ParseFloat(v, 64)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse timeout from Prometheus header: %s", err), http.StatusInternalServerError)
			collector.IperfErrors.Inc()

			return
		}
	}

	if timeoutSeconds == 0 {
		if s.config.Timeout.Seconds() > 0 {
			timeoutSeconds = s.config.Timeout.Seconds()
		} else {
			timeoutSeconds = 30
		}
	}

	// Ensure run period is less than timeout to avoid premature termination
	if runPeriod.Seconds() >= timeoutSeconds {
		runPeriod = time.Duration(timeoutSeconds*0.9) * time.Second
	}

	runTimeout := time.Duration(timeoutSeconds * float64(time.Second))

	start := time.Now()
	registry := prometheus.NewRegistry()

	// Parse bind_address parameter (enhanced feature by ThanhDeptr)
	bindAddress := r.URL.Query().Get("bind_address")

	// Parse parallel parameter (enhanced feature by ThanhDeptr)
	var parallel int
	parallelParam := r.URL.Query().Get("parallel")
	if parallelParam != "" {
		var err error
		parallel, err = strconv.Atoi(parallelParam)
		if err != nil {
			http.Error(w, fmt.Sprintf("'parallel' parameter must be an integer: %s", err), http.StatusBadRequest)
			collector.IperfErrors.Inc()

			return
		}
		if parallel < 1 {
			http.Error(w, "'parallel' parameter must be at least 1", http.StatusBadRequest)
			collector.IperfErrors.Inc()

			return
		}
	}

	// Debug logging to verify parameters are parsed correctly
	s.logger.Info("Probe parameters parsed",
		"target", target,
		"port", targetPort,
		"bind_address", bindAddress,
		"parallel", parallel,
		"reverse_mode", reverseMode,
		"bidirectional", bidirectionalMode,
		"period", runPeriod,
	)

	// Try to acquire global test lock with 500s timeout
	requesterID := fmt.Sprintf("%s:%d", target, targetPort)
	testLock := GetGlobalTestLock()

	lockCtx, lockCancel := context.WithTimeout(r.Context(), 500*time.Second)
	defer lockCancel()

	if !testLock.TryLock(lockCtx, requesterID) {
		s.logger.Error("Failed to acquire test lock within 500s timeout", "requester", requesterID)
		http.Error(w, "iPerf3 test lock timeout: server is busy and wait queue is full", http.StatusServiceUnavailable)
		collector.IperfErrors.Inc()
		return
	}

	// Ensure lock is released when handler exits
	defer func() {
		testLock.Unlock(requesterID)
		s.logger.Info("Released test lock", "requester", requesterID)
	}()

	// Create collector with probe configuration
	probeConfig := collector.ProbeConfig{
		Target:        target,
		Port:          targetPort,
		Period:        runPeriod,
		Timeout:       runTimeout,
		ReverseMode:   reverseMode,
		Bidirectional: bidirectionalMode,
		UDPMode:       udpMode,
		Bitrate:       bitrate,
		BindAddress:   bindAddress,
		Parallel:      parallel,
		Context:       r.Context(), // Request context for proper cancellation
	}

	c := collector.NewCollector(probeConfig, s.logger)
	registry.MustRegister(c)

	// 1. Actively collect metrics.
	//    The Gather function will call c.Collect(), which will run iperf3 with the context above.
	//    If the client disconnects, iperf3 is killed, and Gather will return an error immediately.
	metricFamilies, err := registry.Gather()
	if err != nil {
		s.logger.Error("Failed to gather metrics", "error", err, "requester", requesterID)
		http.Error(w, fmt.Sprintf("Failed to gather metrics: %v", err), http.StatusInternalServerError)
		collector.IperfErrors.Inc()
		return // Exit safely, lock will be released
	}

	// 2. Encode all metrics into a buffer in memory.
	//    This operation is very fast and does not depend on the network.
	var buf bytes.Buffer
	encoder := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range metricFamilies {
		if err := encoder.Encode(mf); err != nil {
			s.logger.Error("Failed to encode metric family", "error", err, "requester", requesterID)
			http.Error(w, fmt.Sprintf("Failed to encode metrics: %v", err), http.StatusInternalServerError)
			collector.IperfErrors.Inc()
			return // Exit safely
		}
	}

	// 3. Write the entire buffer to ResponseWriter in a single operation.
	w.Header().Set("Content-Type", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		s.logger.Warn("Error writing response body", "error", err, "requester", requesterID)
	}

	duration := time.Since(start).Seconds()
	collector.IperfDuration.Observe(duration)
}

// lockStatusHandler handles requests to the /lock-status endpoint
func (s *Server) lockStatusHandler(w http.ResponseWriter, r *http.Request) {
	testLock := GetGlobalTestLock()
	status := testLock.GetStatus()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Enhanced JSON response with queue information
	response := fmt.Sprintf(`{
		"is_locked": %t,
		"locked_by": "%v",
		"locked_at": "%v",
		"lock_duration": "%v",
		"queue_size": %v
	}`,
		status["is_locked"],
		status["locked_by"],
		status["locked_at"],
		status["lock_duration"],
		status["queue_size"])

	if _, err := w.Write([]byte(response)); err != nil {
		s.logger.Error("Failed to write lock status response", "error", err)
	}
}

// indexHandler handles requests to the / endpoint using the exporter-toolkit landing page.
func (s *Server) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)

		return
	}

	// Get landing page configuration from config
	landingConfig := s.config.GetLandingConfig()

	// Create and serve the landing page
	landingPage, err := web.NewLandingPage(landingConfig)
	if err != nil {
		s.logger.Warn("Failed to create landing page", "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)

		return
	}

	landingPage.ServeHTTP(w, r)
}

// healthHandler handles requests to the /health endpoint.
func (s *Server) healthHandler(w http.ResponseWriter, _ *http.Request) {
	// Check if iperf3 exists
	if err := iperf.CheckIperf3Exists(); err != nil {
		s.logger.Error("iperf3 command not found", "err", err)
		http.Error(w, "iperf3 command not found", http.StatusServiceUnavailable)

		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "OK")
}

// readyHandler handles requests to the /ready endpoint.
func (s *Server) readyHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "Ready")
}

// withLogging adds logging middleware to the HTTP handler.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a custom response writer to capture the status code
		rw := &responseWriter{
			ResponseWriter: w,
			statusCode:     http.StatusOK,
		}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)

		s.logger.Debug("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.statusCode,
			"duration", duration.String(),
			"remote_addr", r.RemoteAddr,
		)
	})
}

// responseWriter is a custom response writer that captures the status code.
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader captures the status code.
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
