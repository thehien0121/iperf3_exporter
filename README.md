# iPerf3 Exporter v3.1.1 (Enhanced by ThanhDeptr)

A Prometheus exporter for iPerf3 network performance metrics with advanced mutex mechanism to prevent concurrent test conflicts.

---

> **This is a fork of [edgard/iperf3_exporter](https://github.com/edgard/iperf3_exporter) with additional enhancements by ThanhDeptr**

---

[![Go Report Card](https://goreportcard.com/badge/github.com/edgard/iperf3_exporter)](https://goreportcard.com/report/github.com/edgard/iperf3_exporter)
[![Docker Pulls](https://img.shields.io/docker/pulls/hatanthanh/iperf3_exporter.svg)](https://hub.docker.com/r/hatanthanh/iperf3_exporter)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://github.com/edgard/iperf3_exporter/blob/master/LICENSE)

## New Features (by ThanhDeptr)

- **Mutex Mechanism**: Prevents concurrent iPerf3 tests with global locking system
- **Retry Logic**: Automatic retry mechanism for recoverable network errors
- **Bidirectional Testing**: Simultaneous upload/download bandwidth measurement
- **Bind Address Support**: Configure specific network interfaces for multi-WAN testing
- **Parallel Streams**: Enhanced performance with configurable parallel connections
- **Advanced Metrics**: Calculated bandwidth, latency, and packet loss metrics
- **Ping Integration**: Combined iPerf3 + Ping testing for comprehensive network monitoring
- **BusyBox Ping Support**: Compatible with Alpine Linux/BusyBox ping format
- **Optimized Logging**: Clean, concise logging for better monitoring and debugging

---

## Mutex Mechanism

The enhanced exporter includes a global mutex system that prevents concurrent iPerf3 tests from conflicting with each other. This is especially useful when monitoring multiple WAN connections simultaneously:

- **Global Lock**: Only one iPerf3 test runs at a time across all requests
- **Queue Management**: Subsequent requests wait for the current test to complete
- **Timeout Protection**: Requests timeout after 500 seconds to prevent deadlocks
- **Lock Status Endpoint**: Monitor lock status via `/lock-status` endpoint

## Retry Logic

The exporter now includes intelligent retry mechanism to handle temporary network issues and improve test reliability:

- **Automatic Retry**: Up to 3 retry attempts for recoverable errors
- **Smart Error Detection**: Retries on "Connection refused", "server busy", and "Connection reset by peer"
- **Configurable Delay**: 3-second delay between retry attempts
- **Context Awareness**: Respects timeout and cancellation from Prometheus
- **Enhanced Logging**: Detailed error reporting with iPerf3 output for debugging

The iPerf3 exporter allows iPerf3 probing of endpoints for Prometheus monitoring, enabling you to measure network performance metrics like bandwidth, jitter, and packet loss.

## Features

- Measure network bandwidth between hosts
- Monitor network performance over time
- Support for both TCP and UDP tests
- Configurable test parameters (duration, bitrate, etc.)
- TLS support for secure communication
- Basic authentication for access control
- Health and readiness endpoints for monitoring
- Prometheus metrics for exporter itself

## Installation & Usage

### Using Docker (Recommended)

```bash
docker run -d \
  --user="root" \
  --name iperf3_exporter_vn \
  --network host \
  --cap-add=NET_ADMIN \
  --cap-add=NET_RAW \
  --restart unless-stopped \
  hatanthanh/iperf3_exporter:v3.1.1 \
  --web.listen-address=:9580
```

**Docker Command Explanation:**
- `--user="root"`: Run container as root user for full network access
- `--name iperf3_exporter_vn`: Custom container name for Vietnamese deployment
- `--network host`: Use host networking for direct network interface access
- `--cap-add=NET_ADMIN --cap-add=NET_RAW`: Required capabilities for network testing
- `--restart unless-stopped`: Auto-restart container unless manually stopped
- `--web.listen-address=:9580`: Custom port 9580 for metrics endpoint

### From Binaries

```bash
# Download from releases
curl -L -o iperf3_exporter https://github.com/edgard/iperf3_exporter/releases/download/VERSION/iperf3_exporter-VERSION.PLATFORM
chmod +x iperf3_exporter
./iperf3_exporter --help
```

### Configuration

The exporter can be configured using command-line flags:

- `--web.listen-address`: Address to listen on (default: `:9579`, custom: `:9580`)
- `--web.telemetry-path`: Path under which to expose metrics (default: `/metrics`)
- `--log.level`: Log level (default: `info`)

### Prometheus Configuration

Add the following to your `prometheus.yml`:

```yaml
scrape_configs:
  - job_name: 'iperf3'
    static_configs:
      - targets: ['localhost:9580']
    metrics_path: '/probe'
    params:
      target: ['your-target-host']
      port: ['5201']
      period: ['10s']
      bidirectional: ['true']
      bind_address: ['192.168.1.100']
      parallel: ['2']
```

## API Endpoints

- `/metrics`: Prometheus metrics
- `/probe`: iPerf3 probe endpoint
- `/health`: Health check endpoint
- `/ready`: Readiness check endpoint
- `/lock-status`: Mutex lock status (new feature)

## Parameters

- `target`: Target host for iPerf3 test
- `port`: Port number (default: 5201)
- `period`: Test duration (default: 10s)
- `bidirectional`: Enable bidirectional testing (default: false)
- `bind_address`: Bind to specific network interface
- `parallel`: Number of parallel streams (default: 1)

## Metrics

The exporter in v3.1.1 exposes only the most important (core) network metrics. Metrics are now grouped, simplified, and designed for clarity and focus:

**iPerf3 Metrics (Focus Only):**
- `iperf3_up`: Whether the last iPerf3 probe was successful
- `iperf3_bandwidth_upload_mbps`: Upload bandwidth in Mbps
- `iperf3_bandwidth_download_mbps`: Download bandwidth in Mbps
- `iperf3_retransmits`: TCP retransmit count (quality/stability)
- `iperf3_jitter_ms`: UDP jitter, milliseconds (latency fluctuation)

**Ping Metrics (v3.1.1):**
- `ping_up`: Whether ping probe to target was successful (1=success, 0=failure)
- `ping_packet_loss_percent`: Percent packet loss for the test (0-100%)
- `ping_latency_average_ms`: Average round-trip latency in milliseconds
- `ping_latency_maximum_ms`: Maximum round-trip latency in milliseconds  
- `ping_latency_minimum_ms`: Minimum round-trip latency in milliseconds

> **Ping Integration**: The exporter now automatically runs ping tests after each iPerf3 test to provide comprehensive network monitoring. Ping metrics are collected using the same target and bind_address parameters as the iPerf3 test.

> **NOTE:** Metrics such as `sent_bytes`, `received_bytes`, `sent_seconds`, etc. have been removed in favor of clarity. You get core, actionable, and easy-to-visualize metrics for direct use in Grafana dashboards and alerting.


## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Submit a pull request

## License

This project is released under Apache License 2.0, see [LICENSE](https://github.com/edgard/iperf3_exporter/blob/master/LICENSE).

**Original Repository**: [edgard/iperf3_exporter](https://github.com/edgard/iperf3_exporter)  
**Enhanced Fork v2**: [Thanhdeptr/iperf3_exporter_mutex](https://github.com/Thanhdeptr/iperf3_exporter_mutex)
