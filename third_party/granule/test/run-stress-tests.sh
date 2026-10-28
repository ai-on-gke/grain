#!/usr/bin/env bash
# run-stress-tests.sh - Comprehensive Granule Stress Test Orchestrator
# Executes stress test suites covering high load, storage, and networking.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

SUITE="${1:-all}"
REPORT_FILE="${REPORT_FILE:-/tmp/granule-stress-report.log}"

echo "=========================================================="
echo " Granule Stress Testing Suite: ${SUITE}"
echo " Host: $(hostname), Arch: $(uname -m), Kernel: $(uname -r)"
echo " Report file: ${REPORT_FILE}"
echo "=========================================================="

mkdir -p "$(dirname "${REPORT_FILE}")"
: > "${REPORT_FILE}"

run_test() {
  local name="$1"
  local cmd="$2"
  echo ""
  echo "----------------------------------------------------------"
  echo " Running: ${name}"
  echo " Command: ${cmd}"
  echo "----------------------------------------------------------"
  
  local start_time
  start_time=$(date +%s%N)
  
  if eval "${cmd}" 2>&1 | tee -a "${REPORT_FILE}"; then
    local end_time
    end_time=$(date +%s%N)
    local duration_ms=$(( (end_time - start_time) / 1000000 ))
    echo " [PASS] ${name} (${duration_ms}ms)" | tee -a "${REPORT_FILE}"
  else
    local end_time
    end_time=$(date +%s%N)
    local duration_ms=$(( (end_time - start_time) / 1000000 ))
    echo " [FAIL] ${name} (${duration_ms}ms)" | tee -a "${REPORT_FILE}"
    return 1
  fi
}

passed=0
failed=0

record_pass() {
  passed=$((passed + 1))
}

record_fail() {
  failed=$((failed + 1))
}

run_suite_load() {
  echo "==> Executing High Load Stress Suite..."
  if run_test "MemAgent Pressure Storm & In-Flight Dedup" \
    "go test -v -race -run 'TestStress_MemAgentPressureStorm' ./test/stress/..."; then
    record_pass
  else
    record_fail
  fi
  
  if run_test "Agent High Concurrency & Piped I/O Streams" \
    "go test -v -race -run 'TestStress_AgentExecutionHighLoad|TestStress_ConcurrentSessions' ./test/stress/... ./pkg/agent/..."; then
    record_pass
  else
    record_fail
  fi

  if run_test "Agent High Throughput & Rapid Signal Cancellation" \
    "go test -v -race -run 'TestStress_MassiveThroughputStreaming|TestStress_RapidSignalsAndCancellation' ./pkg/agent/..."; then
    record_pass
  else
    record_fail
  fi
}

run_suite_storage() {
  echo "==> Executing Storage & Reflink Stress Suite..."
  if run_test "QCOW2 Synthesis, Resize & Tier Transitions" \
    "go test -v -race -run 'TestStress_ConcurrentDescriptorSynthesisAndResize|TestStress_StorageHighConcurrency' ./pkg/qcow2/... ./test/stress/..."; then
    record_pass
  else
    record_fail
  fi

  if run_test "Reflink & QCOW2 Layering SHA256 Integrity" \
    "go test -v -race -run 'TestStress_ConcurrentReflinkAndExtentCloning' ./pkg/qcow2/..."; then
    record_pass
  else
    record_fail
  fi

  if run_test "QCOW2 Header & Throughput Under Max Load" \
    "go test -v -race -run 'TestStress_DiskQcowThroughput' ./pkg/qcow2/... ./test/stress/..."; then
    record_pass
  else
    record_fail
  fi
}

run_suite_network() {
  echo "==> Executing Kernel Spliced Networking Stress Suite..."
  if [ "$(id -u)" -eq 0 ]; then
    if run_test "High Packet Rate TAP-to-veth Splice Datapath" \
      "env GRANULE_NETNS_TESTS=required go test -v -run 'TestStress_SpliceHighPacketThroughput' ./pkg/netshim/..."; then
      record_pass
    else
      record_fail
    fi

    if run_test "Splice MTU Boundary Frame Delivery (60B - 1514B)" \
      "env GRANULE_NETNS_TESTS=required go test -v -run 'TestStress_SpliceMTUBoundaries' ./pkg/netshim/..."; then
      record_pass
    else
      record_fail
    fi

    if run_test "Concurrent Isolated Splice Pairs" \
      "env GRANULE_NETNS_TESTS=required go test -v -run 'TestStress_SpliceConcurrentPairs' ./pkg/netshim/..."; then
      record_pass
    else
      record_fail
    fi
  else
    if run_test "High Packet Rate TAP-to-veth Splice Datapath (sudo)" \
      "sudo env PATH=\"$PATH\" GRANULE_NETNS_TESTS=required go test -v -run 'TestStress_SpliceHighPacketThroughput' ./pkg/netshim/..."; then
      record_pass
    else
      record_fail
    fi

    if run_test "Splice MTU Boundary Frame Delivery (sudo)" \
      "sudo env PATH=\"$PATH\" GRANULE_NETNS_TESTS=required go test -v -run 'TestStress_SpliceMTUBoundaries' ./pkg/netshim/..."; then
      record_pass
    else
      record_fail
    fi

    if run_test "Concurrent Isolated Splice Pairs (sudo)" \
      "sudo env PATH=\"$PATH\" GRANULE_NETNS_TESTS=required go test -v -run 'TestStress_SpliceConcurrentPairs' ./pkg/netshim/..."; then
      record_pass
    else
      record_fail
    fi
  fi
}

case "${SUITE}" in
  load)
    run_suite_load
    ;;
  storage)
    run_suite_storage
    ;;
  network)
    run_suite_network
    ;;
  all)
    run_suite_load
    run_suite_storage
    run_suite_network
    ;;
  *)
    echo "Unknown suite: ${SUITE}. Choose from: load, storage, network, all."
    exit 1
    ;;
esac

echo ""
echo "=========================================================="
echo " Stress Test Results Summary"
echo " Passed: ${passed}, Failed: ${failed}"
echo " Full logs recorded in ${REPORT_FILE}"
echo "=========================================================="

if [ "${failed}" -gt 0 ]; then
  exit 1
fi
