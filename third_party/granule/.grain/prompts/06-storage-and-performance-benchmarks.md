# Nightly Prompt: Unified Performance & Benchmark Verification

## Objective
Execute and validate the Granule benchmark suites across all subsystems (Startup & Lifecycle, Compute & Syscalls, Memory & Elasticity, Storage I/O, Network Datapaths, and Microbenchmarks), record results in `benchmarks/results/`, and assert zero regressions in microVM boot latency (<100ms), syscall latency (<60ns), memory expansion speed (<500ms), and network/storage datapaths.

## Context & Key Workflows
Granule delivers hardware-isolated microVM security with container-native ergonomics and near-bare-metal performance:
1. **Sub-100ms MicroVM Startup**: Cloud-Hypervisor direct kernel boot bypassing legacy BIOS/firmware.
2. **Compute & Syscall Efficiency**: Direct in-guest Linux kernel execution with <60ns syscall latency (vs 1,850ns in gVisor).
3. **Dynamic Memory Elasticity**: `virtio-mem` stepped dynamic memory expansion (<500ms) with host RSS footprint under 150MB.
4. **Storage Datapaths**: Flat Multi-Extent VMDK overlays (64MB extents with opportunistic reflink cloning) and raw flat file storage without disk layer bloat.
5. **In-Kernel Spliced Networking**: Spliced veth-to-TAP datapath delivering >90% of Docker bridge throughput with low latency.
6. **Unified Master Runner**: Orchestrating benchmarks and storing telemetry in `benchmarks/results/` via `./benchmarks/run_all_benchmarks.sh`.

## Execution Steps

### 1. Storage & QCOW2 Package Tests
Run tests for storage and QCOW2 overlay management:
```bash
go test -v ./pkg/qcow2/...
```
- Verify QCOW2 header parsing, 2MB cluster sizing, virtual size querying, resizing, small layer extension, flatten replay, and reflink probing.

### 2. Validate Benchmark Scripts & Dry-Run Execution
Verify syntax and dry-run execution of the unified benchmark runner:
```bash
# Check syntax of all benchmark scripts
find benchmarks -name "*.sh" -exec bash -n {} +

# Execute unified runner dry-run validation
./benchmarks/run_all_benchmarks.sh --dry-run
```

### 3. Run Benchmark Suite & Record Results
Execute benchmark runs and record output to the `benchmarks/results/` directory:
```bash
# Run unified benchmarks and record results in benchmarks/results
./benchmarks/run_all_benchmarks.sh --suite=all --out-dir=benchmarks/results
```

### 4. Verify Results & Invariants
Verify that benchmark results are recorded and conform to required thresholds:
- MicroVM initialization overhead remains under the 100ms target threshold.
- Memory hotplug latency remains within expected bounds (<500ms total expansion time).
- Storage direct I/O and network throughput telemetry are properly generated in `benchmarks/results/`.

## Evaluation & Reporting Criteria
1. **Benchmark Tooling Health**: Confirm all benchmark scripts, Python runners, and templates are error-free.
2. **Performance Regressions**: Report any detected regressions in boot latency, memory footprint, syscall overhead, or I/O throughput.
3. **Data Integrity**: Confirm benchmark output files in `benchmarks/results/` adhere to expected JSON schema definitions for telemetry ingestion.
