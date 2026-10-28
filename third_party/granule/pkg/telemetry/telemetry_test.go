package telemetry

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root containing go.mod")
		}
		dir = parent
	}
}

func TestIngestAllTelemetryResults(t *testing.T) {
	root := findRepoRoot(t)
	resultsDir := filepath.Join(root, "benchmarks", "results")

	// 1. Consolidated Summary
	summaryPath := filepath.Join(resultsDir, "summary.json")
	summary, err := LoadSummary(summaryPath)
	if err != nil {
		t.Fatalf("failed to load summary.json: %v", err)
	}
	if summary.Suite != "granule-unified-benchmarks" {
		t.Errorf("expected suite granule-unified-benchmarks, got %s", summary.Suite)
	}
	if summary.Status != "PASS" {
		t.Errorf("expected overall status PASS, got %s", summary.Status)
	}

	// Validate Threshold Assertions
	violations := ValidateThresholds(summary)
	if len(violations) > 0 {
		for _, v := range violations {
			t.Errorf("threshold violation: %s", v)
		}
	}

	// 2. Compute Summary
	computePath := filepath.Join(resultsDir, "compute", "compute_benchmark_summary.json")
	computeSum, err := LoadComputeSummary(computePath)
	if err != nil {
		t.Fatalf("failed to load compute summary: %v", err)
	}
	if len(computeSum.Benchmarks) == 0 {
		t.Errorf("expected compute benchmarks in summary, got 0")
	}

	// 3. Memory Summary & Telemetry
	memDir := filepath.Join(resultsDir, "memory")
	memSum, latSteps, footprint, agent, err := LoadMemoryTelemetry(memDir)
	if err != nil {
		t.Fatalf("failed to load memory telemetry: %v", err)
	}
	if len(memSum.Performance) == 0 {
		t.Errorf("expected memory performance entries, got 0")
	}
	if len(latSteps) == 0 {
		t.Errorf("expected latency steps, got 0")
	}
	for _, step := range latSteps {
		if strings.Contains(step.Step, "+1024MB") && step.TotalExpansionTimeMs > 500.0 {
			t.Errorf("stepped expansion %s took %.2fms, expected < 500ms", step.Step, step.TotalExpansionTimeMs)
		}
	}
	if len(footprint) == 0 {
		t.Errorf("expected footprint results, got 0")
	}
	if len(agent.Logs) == 0 {
		t.Errorf("expected memagent logs, got 0")
	}
	if agent.Metrics == nil {
		t.Errorf("expected memagent structured metrics in agent_results.json, got nil")
	} else {
		if agent.Metrics.EventsTotal[StatusSuccess] == 0 {
			t.Errorf("expected success events > 0 in memagent telemetry")
		}
		if agent.Metrics.AllocatedBytes == 0 {
			t.Errorf("expected allocated bytes > 0 in memagent telemetry")
		}
		if agent.Metrics.CeilingSaturationRatio <= 0 {
			t.Errorf("expected ceiling saturation ratio > 0 in memagent telemetry")
		}
	}

	// 4. Network Summary
	netPath := filepath.Join(resultsDir, "network", "network_benchmark_summary.json")
	netSum, err := LoadNetworkSummary(netPath)
	if err != nil {
		t.Fatalf("failed to load network summary: %v", err)
	}
	if netSum.Granule.TCPSingleMbps <= 0 {
		t.Errorf("invalid Granule TCP bandwidth: %.2f", netSum.Granule.TCPSingleMbps)
	}

	// 5. Storage Summary
	storagePath := filepath.Join(resultsDir, "storage", "summary_storage_benchmark.json")
	storageSum, err := LoadStorageSummary(storagePath)
	if err != nil {
		t.Fatalf("failed to load storage summary: %v", err)
	}
	if len(storageSum.Fio) == 0 {
		t.Errorf("expected storage benchmark entries, got 0")
	}
	for backend, workloads := range storageSum.Fio {
		if r, ok := workloads["rand_read"]; ok && r.Read.IOPS <= 0 {
			t.Errorf("storage backend %s: invalid random read IOPS %.0f", backend, r.Read.IOPS)
		}
	}

	// 6. Microbenchmarks Summary
	microPath := filepath.Join(resultsDir, "microbenchmarks", "microbenchmarks_summary.json")
	microSum, err := LoadMicrobenchmarksSummary(microPath)
	if err != nil {
		t.Fatalf("failed to load microbenchmarks summary: %v", err)
	}
	if microSum.Granule.CPUEventsPerSec <= 0 {
		t.Errorf("invalid Granule microbenchmark CPU events: %.2f", microSum.Granule.CPUEventsPerSec)
	}
}

func TestValidatePerformanceThresholds(t *testing.T) {
	root := findRepoRoot(t)
	summaryPath := filepath.Join(root, "benchmarks", "results", "summary.json")
	summary, err := LoadSummary(summaryPath)
	if err != nil {
		t.Fatalf("failed to load summary: %v", err)
	}

	// 1. MicroVM boot latency < 100ms
	if summary.Dimensions.StartupLatency.MicroVMVMMInitMs >= 100.0 {
		t.Errorf("FAIL: Boot latency is %.2f ms, must be < 100.0 ms", summary.Dimensions.StartupLatency.MicroVMVMMInitMs)
	}

	// 2. Syscall latency < 60ns
	if summary.Dimensions.SyscallOverhead.DirectKernelGetpidNs >= 60.0 {
		t.Errorf("FAIL: Syscall overhead is %.2f ns, must be < 60.0 ns", summary.Dimensions.SyscallOverhead.DirectKernelGetpidNs)
	}

	// 3. Memory expansion < 500ms
	if summary.Dimensions.MemoryBandwidth.HotplugLatencyP50Ms >= 500.0 {
		t.Errorf("FAIL: Dynamic memory expansion is %.2f ms, must be < 500.0 ms", summary.Dimensions.MemoryBandwidth.HotplugLatencyP50Ms)
	}

	// 4. Spliced TAP network efficiency > 90%
	if summary.Dimensions.NetworkDatapath.EfficiencyPctDocker < 90.0 {
		t.Errorf("FAIL: Spliced network throughput efficiency is %.2f%%, must be >= 90.0%%", summary.Dimensions.NetworkDatapath.EfficiencyPctDocker)
	}
}

func TestBenchmarkRunnerDryRun(t *testing.T) {
	root := findRepoRoot(t)
	runnerPath := filepath.Join(root, "benchmarks", "run_all_benchmarks.sh")

	cmd := exec.Command(runnerPath, "--dry-run")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run_all_benchmarks.sh --dry-run failed: %v\nOutput: %s", err, string(out))
	}

	output := string(out)
	for _, expected := range []string{
		"[1/5] Running Startup & Lifecycle Benchmarks...",
		"[2/5] Running Compute & Syscall Benchmarks...",
		"[3/5] Running Memory Subsystem & Hotplug Benchmarks...",
		"[4/5] Running Storage & Filesystem Datapath Benchmarks...",
		"[5/5] Running Network & Spliced Datapath Benchmarks...",
		"Benchmark run completed successfully",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("expected dry-run output to contain %q, but was missing", expected)
		}
	}
}

func TestBenchmarkRunnerFullExecution(t *testing.T) {
	root := findRepoRoot(t)
	runnerPath := filepath.Join(root, "benchmarks", "run_all_benchmarks.sh")
	outDir := t.TempDir()

	cmd := exec.Command(runnerPath, "--suite=all", "--out-dir="+outDir)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run_all_benchmarks.sh execution failed: %v\nOutput: %s", err, string(out))
	}

	// Verify that summary.json was generated in the target directory
	summaryFile := filepath.Join(outDir, "summary.json")
	summary, err := LoadSummary(summaryFile)
	if err != nil {
		t.Fatalf("failed to load generated summary.json: %v", err)
	}
	if summary.Status != "PASS" {
		t.Errorf("expected generated summary status PASS, got %s", summary.Status)
	}
}
