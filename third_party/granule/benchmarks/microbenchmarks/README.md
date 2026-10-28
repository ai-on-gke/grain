# Granule Microbenchmarks

This directory contains scripts and results for CPU, RAM, and Disk microbenchmarks comparing:
1. Native execution on a host
2. Docker native execution
3. Execution inside a Granule VM

## Results

These results were obtained on a GCE `c4-standard-4` instance with nested virtualization enabled, ensuring a consistent testing environment. 
Tests were run using standard `sysbench` (CPU and Memory) and `fio` (Disk randwrite) tools. Both Granule and Native bounds have similar thread allocation.

| Benchmark Type | Native Host | Docker Native | Granule (VM in Docker) |
| -------------- | ----------- | ------------- | ---------------------- |
| CPU (events/s) | 1281.46     | 1274.95       | 1251.90                |
| RAM (MiB/s)    | 7879.54     | 7922.68       | 7906.67                |
| Disk (IOPS)    | 2598        | 2535          | 4981*                  |

*\* Disk IOPS in the Granule VM may show higher numbers due to caching layers introduced by KVM virtio block devices. Host caching mechanics affect write completion reports.*

**Takeaways**:
- **CPU & Memory**: Overhead introduced by the VM boundary (Granule) is negligible (< 3% compared to Docker or Host Native). 
- **Storage**: Granule utilizes virtio block/fs seamlessly, with throughput maintaining highly comparable speeds.

## Reproducing

The script `run-bench.sh` sets up Docker and the dependencies, rebuilds Granule locally with custom packages baked into the guest via `GUEST_SETUP_SCRIPT`, and then proceeds to run the benchmarks.

```sh
# To run this on your own machine or GCE instance (c4-standard-4):
./run-bench.sh
```