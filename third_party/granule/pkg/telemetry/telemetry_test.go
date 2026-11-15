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

	microCmd := exec.Command(runnerPath, "--suite=microbenchmarks", "--dry-run")
	microCmd.Dir = root
	microOut, err := microCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run_all_benchmarks.sh --suite=microbenchmarks --dry-run failed: %v\nOutput: %s", err, string(microOut))
	}
	if !strings.Contains(string(microOut), "[DRY-RUN] Microbenchmarks suite configured successfully:") {
		t.Errorf("expected microbenchmarks dry-run output, got: %s", string(microOut))
	}
}

func TestMicrobenchmarkRunnerSetupAndCleanWorktree(t *testing.T) {
	root := findRepoRoot(t)
	srcScript := filepath.Join(root, "benchmarks", "microbenchmarks", "run-bench.sh")
	scriptBytes, err := os.ReadFile(srcScript)
	if err != nil {
		t.Fatalf("failed to read %s: %v", srcScript, err)
	}

	writeStub := func(t *testing.T, path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatalf("failed to write stub %s: %v", path, err)
		}
	}

	const dockerStub = `#!/usr/bin/env bash
set -euo pipefail
case "${1:-}" in
  inspect)
    echo "false"
    ;;
  image)
    exit 1
    ;;
  build)
    if [ "${!#}" = "-" ]; then
      cat > "$BENCH_DOCKERFILE_CAPTURE"
    fi
    ;;
  run|exec)
    echo "events per second: 1000.00 (1000.00 MiB/sec) IOPS=1000"
    ;;
  rm)
    ;;
esac
`
	const sysbenchStub = `#!/usr/bin/env bash
echo "events per second: 1000.00 (1000.00 MiB/sec)"
`
	const fioStub = `#!/usr/bin/env bash
echo "$*" >> "$FIO_LOG"
if [ "${FIO_FAIL:-}" = "1" ]; then
  echo "fio: looks like your file system does not support direct=1/buffered=0" >&2
  echo "fio: destination does not support O_DIRECT" >&2
  exit 1
fi
echo "IOPS=1000"
`

	type runResult struct {
		out                []byte
		err                error
		sudoLines          []string
		fioLines           []string
		capturedDockerfile string
	}

	runScript := func(t *testing.T, preinstallDocker, preinstallSysbench, preinstallFio bool, extraEnv ...string) runResult {
		t.Helper()
		tmp := t.TempDir()
		fakeRoot := filepath.Join(tmp, "repo")
		scriptDir := filepath.Join(fakeRoot, "benchmarks", "microbenchmarks")
		if err := os.MkdirAll(scriptDir, 0o755); err != nil {
			t.Fatalf("mkdir scriptDir: %v", err)
		}
		fakeScript := filepath.Join(scriptDir, "run-bench.sh")
		if err := os.WriteFile(fakeScript, scriptBytes, 0o755); err != nil {
			t.Fatalf("write fakeScript: %v", err)
		}

		binDir := filepath.Join(tmp, "bin")
		if err := os.MkdirAll(binDir, 0o755); err != nil {
			t.Fatalf("mkdir binDir: %v", err)
		}
		for _, tool := range []string{"bash", "cat", "chmod", "dirname", "env", "grep"} {
			realPath, err := exec.LookPath(tool)
			if err != nil {
				t.Fatalf("lookpath %s: %v", tool, err)
			}
			if err := os.Symlink(realPath, filepath.Join(binDir, tool)); err != nil {
				t.Fatalf("symlink %s: %v", tool, err)
			}
		}

		writeStub(t, filepath.Join(binDir, "rm"), "#!/usr/bin/env bash\nexit 0\n")
		writeStub(t, filepath.Join(binDir, "lscpu"), "#!/usr/bin/env bash\necho 'Model name: Fake CPU'\n")

		dockerTemplate := filepath.Join(tmp, "docker.stub")
		sysbenchTemplate := filepath.Join(tmp, "sysbench.stub")
		fioTemplate := filepath.Join(tmp, "fio.stub")
		writeStub(t, dockerTemplate, dockerStub)
		writeStub(t, sysbenchTemplate, sysbenchStub)
		writeStub(t, fioTemplate, fioStub)

		if preinstallDocker {
			writeStub(t, filepath.Join(binDir, "docker"), dockerStub)
		}
		if preinstallSysbench {
			writeStub(t, filepath.Join(binDir, "sysbench"), sysbenchStub)
		}
		if preinstallFio {
			writeStub(t, filepath.Join(binDir, "fio"), fioStub)
		}

		sudoLog := filepath.Join(tmp, "sudo.log")
		fioLog := filepath.Join(tmp, "fio.log")
		dockerfileCapture := filepath.Join(tmp, "Dockerfile.captured")
		writeStub(t, filepath.Join(binDir, "sudo"), `#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "$SUDO_LOG"
case "${1:-}" in
  apt-get)
    if [ "${2:-}" = "install" ]; then
      for pkg in "${@:3}"; do
        case "$pkg" in
          docker.io) cat "$DOCKER_STUB" > "$BIN_DIR/docker" && chmod +x "$BIN_DIR/docker" ;;
          sysbench)  cat "$SYSBENCH_STUB" > "$BIN_DIR/sysbench" && chmod +x "$BIN_DIR/sysbench" ;;
          fio)       cat "$FIO_STUB" > "$BIN_DIR/fio" && chmod +x "$BIN_DIR/fio" ;;
        esac
      done
    fi
    ;;
  usermod)
    ;;
  *)
    exec "$@"
    ;;
esac
`)

		cmd := exec.Command(filepath.Join(binDir, "bash"), fakeScript)
		cmd.Dir = fakeRoot
		cmd.Env = []string{
			"PATH=" + binDir,
			"HOME=" + tmp,
			"USER=benchuser",
			"BIN_DIR=" + binDir,
			"SUDO_LOG=" + sudoLog,
			"BENCH_DOCKERFILE_CAPTURE=" + dockerfileCapture,
			"DOCKER_STUB=" + dockerTemplate,
			"SYSBENCH_STUB=" + sysbenchTemplate,
			"FIO_STUB=" + fioTemplate,
			"FIO_LOG=" + fioLog,
		}
		cmd.Env = append(cmd.Env, extraEnv...)
		out, err := cmd.CombinedOutput()
		res := runResult{out: out, err: err}
		readLines := func(path string) []string {
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var lines []string
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if line != "" {
					lines = append(lines, line)
				}
			}
			return lines
		}
		res.sudoLines = readLines(sudoLog)
		res.fioLines = readLines(fioLog)
		if captured, err := os.ReadFile(dockerfileCapture); err == nil {
			res.capturedDockerfile = string(captured)
		}

		entries, err := os.ReadDir(fakeRoot)
		if err != nil {
			t.Fatalf("readdir fakeRoot: %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != "benchmarks" {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("expected repo root to contain only [benchmarks], got %v", names)
		}
		return res
	}

	runCase := func(t *testing.T, preinstallDocker, preinstallSysbench, preinstallFio bool) (sudoLines []string, capturedDockerfile string) {
		t.Helper()
		res := runScript(t, preinstallDocker, preinstallSysbench, preinstallFio)
		if res.err != nil {
			t.Fatalf("run-bench.sh failed: %v\nOutput:\n%s", res.err, string(res.out))
		}
		if res.capturedDockerfile == "" {
			t.Fatalf("expected a bench:local Dockerfile to be captured")
		}
		return res.sudoLines, res.capturedDockerfile
	}
	hasLine := func(lines []string, want string) bool {
		for _, l := range lines {
			if l == want {
				return true
			}
		}
		return false
	}
	hasPrefix := func(lines []string, prefix string) bool {
		for _, l := range lines {
			if strings.HasPrefix(l, prefix) {
				return true
			}
		}
		return false
	}

	t.Run("docker_present_installs_only_missing_tools", func(t *testing.T) {
		lines, captured := runCase(t, true, false, true)
		if !hasLine(lines, "apt-get install -y sysbench") {
			t.Errorf("expected only sysbench to be installed, sudo calls: %v", lines)
		}
		for _, l := range lines {
			if strings.HasPrefix(l, "apt-get install") && strings.Contains(l, "docker.io") {
				t.Errorf("must not install docker.io when docker is already present: %q", l)
			}
		}
		if hasPrefix(lines, "usermod") {
			t.Errorf("must not run usermod when docker.io was not installed: %v", lines)
		}
		if !hasLine(lines, "docker build -t bench:local -") {
			t.Errorf("expected bench:local to build from stdin, sudo calls: %v", lines)
		}
		if !strings.Contains(captured, "FROM ubuntu:24.04") {
			t.Errorf("expected bench:local Dockerfile on stdin, got: %q", captured)
		}
	})

	t.Run("all_tools_present_skips_apt", func(t *testing.T) {
		lines, _ := runCase(t, true, true, true)
		if hasPrefix(lines, "apt-get") || hasPrefix(lines, "usermod") {
			t.Errorf("expected no apt-get or usermod calls when all tools exist, got: %v", lines)
		}
	})

	t.Run("missing_all_installs_docker_io_and_adds_group", func(t *testing.T) {
		lines, _ := runCase(t, false, false, false)
		if !hasLine(lines, "apt-get install -y docker.io sysbench fio") {
			t.Errorf("expected docker.io sysbench fio install, sudo calls: %v", lines)
		}
		if !hasLine(lines, "usermod -aG docker benchuser") {
			t.Errorf("expected usermod for benchuser when docker.io installed, sudo calls: %v", lines)
		}
	})

	// fio runs with --direct=1, which tmpfs rejects, and /tmp is tmpfs on
	// stock Debian/systemd hosts, so every disk benchmark must target the
	// disk-backed /var/tmp rather than /tmp.
	t.Run("fio_targets_disk_backed_var_tmp", func(t *testing.T) {
		res := runScript(t, true, true, true)
		if res.err != nil {
			t.Fatalf("run-bench.sh failed: %v\nOutput:\n%s", res.err, string(res.out))
		}
		if !hasPrefix(res.fioLines, "--name=host-randwrite --filename=/var/tmp/test.fio ") {
			t.Errorf("expected host fio run against /var/tmp/test.fio, fio calls: %v", res.fioLines)
		}
		if !hasPrefix(res.sudoLines, "docker run --rm -v /var/tmp:/data bench:local fio --name=dock-randwrite --filename=/data/docker_test.fio ") {
			t.Errorf("expected docker fio run with /var/tmp mounted at /data, sudo calls: %v", res.sudoLines)
		}
		if !hasPrefix(res.sudoLines, "docker exec myvm granule exec -- fio --name=granule-randwrite --filename=/var/tmp/granule_test.fio ") {
			t.Errorf("expected granule fio run against /var/tmp/granule_test.fio, sudo calls: %v", res.sudoLines)
		}
		if !hasLine(res.sudoLines, "docker exec myvm granule exec -- rm -f /var/tmp/granule_test.fio") {
			t.Errorf("expected the granule fio test file to be removed, sudo calls: %v", res.sudoLines)
		}
		for _, l := range append(append([]string{}, res.fioLines...), res.sudoLines...) {
			if (strings.Contains(l, "/tmp/") && !strings.Contains(l, "/var/tmp/")) || strings.Contains(l, " /tmp:") {
				t.Errorf("fio must not target tmpfs-backed /tmp: %q", l)
			}
		}
	})

	t.Run("fio_scratch_dir_is_overridable", func(t *testing.T) {
		scratch := t.TempDir()
		res := runScript(t, true, true, true, "BENCH_SCRATCH_DIR="+scratch)
		if res.err != nil {
			t.Fatalf("run-bench.sh failed: %v\nOutput:\n%s", res.err, string(res.out))
		}
		if !hasPrefix(res.fioLines, "--name=host-randwrite --filename="+scratch+"/test.fio ") {
			t.Errorf("expected host fio run against BENCH_SCRATCH_DIR, fio calls: %v", res.fioLines)
		}
		if !hasPrefix(res.sudoLines, "docker run --rm -v "+scratch+":/data bench:local fio ") {
			t.Errorf("expected docker fio run with BENCH_SCRATCH_DIR mounted at /data, sudo calls: %v", res.sudoLines)
		}
		if !hasLine(res.sudoLines, "rm -f "+scratch+"/test.fio "+scratch+"/docker_test.fio") {
			t.Errorf("expected cleanup of the fio files in BENCH_SCRATCH_DIR, sudo calls: %v", res.sudoLines)
		}
	})

	t.Run("fio_failure_surfaces_diagnostic", func(t *testing.T) {
		res := runScript(t, true, true, true, "FIO_FAIL=1")
		if res.err == nil {
			t.Fatalf("expected run-bench.sh to fail when fio fails, output:\n%s", string(res.out))
		}
		out := string(res.out)
		for _, want := range []string{
			"fio failed with exit status 1",
			"fio: destination does not support O_DIRECT",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("expected output to contain %q, got:\n%s", want, out)
			}
		}
		if strings.Contains(out, "=== [2] Docker Native ===") {
			t.Errorf("expected run-bench.sh to stop after the failing host fio run, got:\n%s", out)
		}
	})
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
