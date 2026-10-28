# Granule Sandbox Network Isolation & Boot Latency Walkthrough (2026-09-22)

## Result

Pass. The end-to-end verification and performance test suite for Granule sandbox VM network isolation, inner Docker daemon subnet allocation, host gateway non-interference, and VM boot latency completed successfully across all test scenarios. Real Granule VM guest instances (`grain-guest:e2e-test`) were created, verified, and torn down under `GRAIN_REAL_VM_TESTS=1`.

The walkthrough verified commit changes against `docs/test-plans/2026-09-22.md` §4 and validated the network overhaul from PR #1801 (`58964c664`).

## Environment and method

- Host OS: Debian GNU/Linux 12 (bookworm), Linux kernel 6.1.0-50-amd64 x86_64.
- Hardware & Virtualization: QEMU/KVM with nested virtualization enabled (`/dev/kvm`).
- Guest Toolchains: Granule v0.4.0, Docker CE 29.8.1 (containerd 2.2.3, runc 1.3.4), Go 1.26.6, Node v20.19.0.
- Test Suite: `pkg/orchestrator` (`TestGranuleSandboxGuestNetworkIsolationAndBootLatency`, `TestGranuleSandboxesAgainstARealDockerBackedVM`, `TestGranuleSandboxesAcquireCreatesTwoRealVMsConcurrently`).
- Network Topology:
  - Host Gateway Proxy: `172.30.255.1` (proxies git, npm, apt, and external package traffic).
  - VM Guest Management & TAP interface: `172.18.0.0/16` (or `172.17.0.0/16` host flat net).
  - Inner Guest Docker Daemon: Subnet `10.240.0.1/24` (`docker0`), Address Pool `10.241.0.0/16` (size `/24`).

## Test Scenarios & Outcomes

| Scenario | Execution & Checks | Outcome |
| --- | --- | --- |
| VM Boot Latency & Initramfs Resume Bypass | Booted a fresh Granule sandbox VM using real guest rootfs. Measured elapsed time from `Acquire` request to guest agent readiness (~48.5s). Inspected guest kernel `dmesg` to verify `RESUME=none` and `noresume` bypassed the 105-second suspend/resume timeout stall (`Gave up waiting for suspend/resume device`). | Pass. Cold boot completed cleanly without any initramfs resume timeout stalls. |
| Inner Docker Subnet Configuration | Verified guest `/etc/docker/daemon.json` contains `bip: 10.240.0.1/24` and `default-address-pools: [{"base": "10.241.0.0/16", "size": 24}]`. Started docker service and confirmed `docker0` bridge interface was assigned `10.240.0.1/24`. | Pass. Inner dockerd properly isolated on `10.240.0.1/24`. |
| Nested Container Execution & ICMP Connectivity | Executed `docker run --rm alpine ping -c 1 10.240.0.1` inside the Granule VM. Verified 0% packet loss (0.162ms roundtrip) to the `docker0` bridge. Built and executed a custom container image inside the guest (`docker build -t nested-test-img . && docker run --rm nested-test-img`). | Pass. Nested container run and build operations succeeded with full intra-bridge network functionality. |
| Host Gateway Isolation & Egress Non-Interference | Ran concurrent nested container ping loops against `10.240.0.1` while simultaneously executing host gateway downloads and git/curl operations routed through `172.30.255.1`. | Pass. No `ECONNREFUSED` errors, route hijacking, or packet collisions observed. Outer host proxy traffic and inner docker traffic remain completely isolated. |
| Dynamic Runtime Repair (`ensureGuestDockerNetwork`) | Staged a conflicting configuration in the guest (`/etc/docker/daemon.json` set to `172.30.255.1/24` and active `docker0` on `172.30.255.1`). Invoked `ensureGuestDockerNetwork`. Verified that the conflicting `docker0` bridge was removed, `/etc/docker/daemon.json` was repaired back to `10.240.0.1/24`, dockerd was cleanly restarted, and subsequent container runs succeeded. | Pass. Runtime repair successfully restores proper subnet allocation and recovers container functionality. |

## Latency Measurements

- Cold VM Acquisition & Boot: 48.58s (includes QEMU boot, ext4 root resize, systemd initialization, and SSH forwarder readiness).
- Post-boot Docker Daemon Initialization: 1.12s.
- Nested Container Execution: 0.16s (with cached layers).
- Subnet Repair & Dockerd Restart: 2.34s.
