package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Summary represents the consolidated benchmark summary (summary.json).
type Summary struct {
	Suite      string            `json:"suite"`
	Version    string            `json:"version"`
	Status     string            `json:"status"`
	Dimensions SummaryDimensions `json:"dimensions"`
}

// SummaryDimensions holds the performance metric dimensions.
type SummaryDimensions struct {
	StartupLatency    StartupDimension `json:"startup_latency_ms"`
	ComputeEfficiency ComputeDimension `json:"compute_cpu_efficiency"`
	SyscallOverhead   SyscallDimension `json:"syscall_overhead"`
	MemoryBandwidth   MemoryDimension  `json:"memory_bandwidth"`
	StorageIO         StorageDimension `json:"storage_io"`
	NetworkDatapath   NetworkDimension `json:"network_datapath"`
}

// StartupDimension represents microVM boot latency metrics.
type StartupDimension struct {
	TargetThresholdMs float64 `json:"target_threshold_ms"`
	MicroVMVMMInitMs  float64 `json:"microvm_vmm_init_ms"`
	Status            string  `json:"status"`
}

// ComputeDimension represents compute and CPU prime throughput comparison.
type ComputeDimension struct {
	SingleThreadPctHost float64 `json:"single_thread_pct_host"`
	MultiThreadPctHost  float64 `json:"multi_thread_pct_host"`
	CryptoPctHost       float64 `json:"crypto_pct_host"`
	Status              string  `json:"status"`
}

// SyscallDimension represents in-kernel direct syscall overhead metrics.
type SyscallDimension struct {
	DirectKernelGetpidNs float64 `json:"direct_kernel_getpid_ns"`
	HostBaselineNs       float64 `json:"host_baseline_ns"`
	GVisorSentryNs       float64 `json:"gvisor_sentry_ns"`
	Status               string  `json:"status"`
}

// MemoryDimension represents memory bandwidth and dynamic expansion latency metrics.
type MemoryDimension struct {
	SeqRead1MbPctHost   float64 `json:"seq_read_1mb_pct_host"`
	SeqWrite1MbPctHost  float64 `json:"seq_write_1mb_pct_host"`
	HotplugLatencyP50Ms float64 `json:"hotplug_latency_p50_ms"`
	Status              string  `json:"status"`
}

// StorageDimension represents storage throughput and IOPS metrics.
type StorageDimension struct {
	FlatSeqWriteMbS   float64 `json:"flat_seq_write_mb_s"`
	DeltaSeqWriteMbS  float64 `json:"delta_seq_write_mb_s"`
	FlatRandWriteIops float64 `json:"flat_rand_write_iops"`
	Status            string  `json:"status"`
}

// NetworkDimension represents spliced TAP vs Docker bridge network metrics.
type NetworkDimension struct {
	SplicedTapTcpMbps   float64 `json:"spliced_tap_tcp_mbps"`
	DockerBridgeTcpMbps float64 `json:"docker_bridge_tcp_mbps"`
	EfficiencyPctDocker float64 `json:"efficiency_pct_docker"`
	Status              string  `json:"status"`
}

// ComputeBenchmarkSummary represents the compute module telemetry.
type ComputeBenchmarkSummary struct {
	Environment string             `json:"environment"`
	Benchmarks  []ComputeBenchmark `json:"benchmarks"`
}

// ComputeBenchmark represents an individual compute workload result.
type ComputeBenchmark struct {
	Name           string `json:"name"`
	Metric         string `json:"metric"`
	Host           string `json:"host"`
	Docker         string `json:"docker"`
	Granule        string `json:"granule"`
	GVisor         string `json:"gvisor,omitempty"`
	GranulePctHost string `json:"granule_pct_host"`
}

// MemoryBenchmarkSummary represents sysbench memory performance telemetry.
type MemoryBenchmarkSummary struct {
	Performance map[string]map[string]MemoryPerfMetric `json:"performance"`
}

// MemoryPerfMetric represents throughput and latency of a memory workload.
type MemoryPerfMetric struct {
	ThroughputMiBS float64 `json:"throughput_mib_s"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
}

// HotplugLatencyStep represents virtio-mem dynamic expansion latency.
type HotplugLatencyStep struct {
	Step                 string  `json:"step"`
	APITimeMs            float64 `json:"api_time_ms"`
	GuestOnlineTimeMs    float64 `json:"guest_online_time_ms"`
	TotalExpansionTimeMs float64 `json:"total_expansion_time_ms"`
	APIStddev            float64 `json:"api_stddev,omitempty"`
	TotalStddev          float64 `json:"total_stddev,omitempty"`
}

// FootprintSummary represents host process memory footprint stats.
type FootprintSummary map[string]map[string]ProcessMemoryStats

// ProcessMemoryStats holds memory breakdown values in MiB.
type ProcessMemoryStats struct {
	RssMB          float64 `json:"rss_mb"`
	PssMB          float64 `json:"pss_mb"`
	PrivateDirtyMB float64 `json:"private_dirty_mb"`
	SharedMB       float64 `json:"shared_mb"`
}

// MemAgentSummary represents dynamic memory agent expansion logs and steps.
type MemAgentSummary struct {
	Initial string                     `json:"initial"`
	Step1   string                     `json:"step1"`
	Step2   string                     `json:"step2"`
	Step3   string                     `json:"step3"`
	Logs    []string                   `json:"logs"`
	Metrics *MemoryElasticityTelemetry `json:"metrics,omitempty"`
}

// NetworkBenchmarkSummary represents network benchmark telemetry.
type NetworkBenchmarkSummary struct {
	Host    NetworkEnvironmentSummary `json:"host"`
	Docker  NetworkEnvironmentSummary `json:"docker"`
	Granule NetworkEnvironmentSummary `json:"granule"`
}

// NetworkEnvironmentSummary represents network telemetry for a test environment.
type NetworkEnvironmentSummary struct {
	TCPSingleMbps float64             `json:"tcp_single_mbps"`
	TCPMultiMbps  float64             `json:"tcp_multi_mbps"`
	UDPMbps       float64             `json:"udp_mbps"`
	LatencyUS     NetworkLatencyStats `json:"latency_us"`
	HTTPBench     NetworkHTTPStats    `json:"http_bench"`
}

// NetworkLatencyStats represents TCP ping-pong latency statistics.
type NetworkLatencyStats struct {
	AvgUS   float64 `json:"avg_us"`
	MinUS   float64 `json:"min_us"`
	MaxUS   float64 `json:"max_us"`
	P50US   float64 `json:"p50_us"`
	P95US   float64 `json:"p95_us"`
	P99US   float64 `json:"p99_us"`
	Samples int     `json:"samples"`
}

// NetworkHTTPStats represents HTTP benchmark statistics.
type NetworkHTTPStats struct {
	RPS       float64 `json:"rps"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
	Completed int     `json:"completed"`
	Errors    int     `json:"errors"`
}

// StorageBenchmarkSummary represents the storage module telemetry map.
type StorageBenchmarkSummary map[string]StorageBenchmarkItem

// StorageBenchmarkItem represents one storage workload across environments.
type StorageBenchmarkItem struct {
	Name    string                 `json:"name"`
	Host    map[string]interface{} `json:"host,omitempty"`
	Flat    map[string]interface{} `json:"flat,omitempty"`
	Vmdk    map[string]interface{} `json:"vmdk,omitempty"`
	Qcow64k map[string]interface{} `json:"qcow_64k,omitempty"`
	Qcow2m  map[string]interface{} `json:"qcow_2m,omitempty"`
}

// MicrobenchmarksSummary represents microbenchmark results.
type MicrobenchmarksSummary struct {
	Host    MicrobenchMetrics `json:"host"`
	Docker  MicrobenchMetrics `json:"docker"`
	Granule MicrobenchMetrics `json:"granule"`
}

// MicrobenchMetrics represents microbenchmark metrics for an environment.
type MicrobenchMetrics struct {
	CPUEventsPerSec   float64 `json:"cpu_events_per_sec"`
	MemoryReadMiBS    float64 `json:"memory_read_mib_s"`
	DiskRandWriteIOPS float64 `json:"disk_randwrite_iops"`
}

// LoadSummary loads and parses summary.json.
func LoadSummary(path string) (*Summary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read summary file: %w", err)
	}
	var s Summary
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to unmarshal summary JSON: %w", err)
	}
	return &s, nil
}

// LoadComputeSummary loads compute/compute_benchmark_summary.json.
func LoadComputeSummary(path string) (*ComputeBenchmarkSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read compute summary: %w", err)
	}
	var c ComputeBenchmarkSummary
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("failed to unmarshal compute summary: %w", err)
	}
	return &c, nil
}

// LoadMemoryTelemetry loads all memory benchmark telemetry files from results/memory.
func LoadMemoryTelemetry(dir string) (*MemoryBenchmarkSummary, []HotplugLatencyStep, FootprintSummary, *MemAgentSummary, error) {
	summaryPath := filepath.Join(dir, "memory_benchmark_summary.json")
	perfData, err := os.ReadFile(summaryPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to read memory summary: %w", err)
	}
	var memSummary MemoryBenchmarkSummary
	if err := json.Unmarshal(perfData, &memSummary); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to unmarshal memory summary: %w", err)
	}

	latPath := filepath.Join(dir, "latency_results.json")
	latData, err := os.ReadFile(latPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to read latency results: %w", err)
	}
	var latSteps []HotplugLatencyStep
	if err := json.Unmarshal(latData, &latSteps); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to unmarshal latency results: %w", err)
	}

	fpPath := filepath.Join(dir, "footprint_results.json")
	fpData, err := os.ReadFile(fpPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to read footprint results: %w", err)
	}
	var footprint FootprintSummary
	if err := json.Unmarshal(fpData, &footprint); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to unmarshal footprint results: %w", err)
	}

	agentPath := filepath.Join(dir, "agent_results.json")
	agentData, err := os.ReadFile(agentPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to read agent results: %w", err)
	}
	var agent MemAgentSummary
	if err := json.Unmarshal(agentData, &agent); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to unmarshal agent results: %w", err)
	}

	return &memSummary, latSteps, footprint, &agent, nil
}

// LoadNetworkSummary loads network/network_benchmark_summary.json.
func LoadNetworkSummary(path string) (*NetworkBenchmarkSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read network summary: %w", err)
	}
	var n NetworkBenchmarkSummary
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, fmt.Errorf("failed to unmarshal network summary: %w", err)
	}
	return &n, nil
}

// LoadStorageSummary loads storage/summary_storage_benchmark.json.
func LoadStorageSummary(path string) (StorageBenchmarkSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read storage summary: %w", err)
	}
	var s StorageBenchmarkSummary
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to unmarshal storage summary: %w", err)
	}
	return s, nil
}

// LoadMicrobenchmarksSummary loads microbenchmarks/microbenchmarks_summary.json.
func LoadMicrobenchmarksSummary(path string) (*MicrobenchmarksSummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read microbenchmarks summary: %w", err)
	}
	var m MicrobenchmarksSummary
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to unmarshal microbenchmarks summary: %w", err)
	}
	return &m, nil
}

// ValidateThresholds checks that summary metrics adhere to Sub-Part 7 thresholds.
func ValidateThresholds(s *Summary) []string {
	var violations []string

	if s.Status != "PASS" {
		violations = append(violations, fmt.Sprintf("overall summary status is %q, expected PASS", s.Status))
	}

	// 1. Boot latency < 100ms
	if s.Dimensions.StartupLatency.MicroVMVMMInitMs > 100.0 {
		violations = append(violations, fmt.Sprintf("boot latency %.2fms exceeds 100ms threshold", s.Dimensions.StartupLatency.MicroVMVMMInitMs))
	}

	// 2. Syscall getpid latency < 60ns
	if s.Dimensions.SyscallOverhead.DirectKernelGetpidNs > 60.0 {
		violations = append(violations, fmt.Sprintf("direct syscall getpid latency %.2fns exceeds 60ns threshold", s.Dimensions.SyscallOverhead.DirectKernelGetpidNs))
	}

	// 3. Memory expansion < 500ms
	if s.Dimensions.MemoryBandwidth.HotplugLatencyP50Ms > 500.0 {
		violations = append(violations, fmt.Sprintf("virtio-mem hotplug latency p50 %.2fms exceeds 500ms threshold", s.Dimensions.MemoryBandwidth.HotplugLatencyP50Ms))
	}

	// 4. Network throughput > 90% Docker bridge
	if s.Dimensions.NetworkDatapath.EfficiencyPctDocker < 90.0 {
		violations = append(violations, fmt.Sprintf("spliced network throughput efficiency %.2f%% is below 90%% target", s.Dimensions.NetworkDatapath.EfficiencyPctDocker))
	}

	return violations
}
