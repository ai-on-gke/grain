# Test Run: VM Guest MTU Propagation and Docker Subnet Collision Avoidance (2026-09-25)

## Result

Pass. The end-to-end walkthrough and verification test suite for VM Guest Dynamic MTU Discovery, kernel parameter propagation, guest interface MTU configuration, inner Docker daemon MTU clamping, large TCP payload / Git packfile transfer integrity on non-1500 MTU networks, and dynamic Docker subnet collision repair (`guestDockerNetworkRepair`) completed successfully across all test scenarios.

The walkthrough validated the network enhancements and encapsulation fixes from PR #1818 (`541ff6c4f` / `d72a0e352`), PR #2014 (`39bde7936`), and PR #2032 (`a74a075ae`) as defined in `docs/test-plans/2026-09-25.md` §6.

## Environment and method

- Host OS: Debian GNU/Linux 12 (bookworm), Linux kernel 6.1.0-53-amd64 x86_64.
- Hardware & Virtualization: QEMU/KVM with nested virtualization enabled (`/dev/kvm`).
- Guest Toolchains & Components:
  - Granule netshim (`third_party/granule/pkg/netshim`): Dynamic host interface MTU discovery and kernel parameter synthesis (`granule.mtu=<mtu>`).
  - Guest Route Configuration: `/usr/local/libexec/granule-configure-routes` applying interface MTU and carried routes.
  - Guest Docker MTU Hook: `/usr/local/libexec/docker-configure-mtu` (systemd unit drop-in `10-mtu.conf`) configuring `/etc/docker/daemon.json`.
  - Orchestrator Subnet Repair: `guestDockerNetworkRepair` in `pkg/orchestrator/granule_sandboxes.go`.
- Test Framework & Isolation:
  - Linux Network Namespaces (`ns-host`, `ns-guest`, `ns-repair`) with virtual Ethernet (`veth`) and TAP device pairs.
  - Root/privileged netshim unit and stress test suites (`go test -v ./pkg/netshim/...`).
  - Orchestrator unit tests (`pkg/orchestrator/granule_repair_test.go`).
- Network Topologies Tested:
  - **GKE CNI Topology**: Host interface MTU 1460, guest IP `10.244.0.5/24`, gateway `10.244.0.1`.
  - **Overlay / VXLAN Topology**: Host interface MTU 1440, guest IP `10.244.0.5/24`, gateway `10.244.0.1`.
  - **10.x Host Network Collision Topology**: Host gateway `10.240.0.1/24`, testing `docker0` collision avoidance and automatic migration to alternate subnet `172.30.255.1/24` (pool `172.31.0.0/16`).

## Test Scenarios & Outcomes

| Scenario | Execution & Checks | Outcome |
| --- | --- | --- |
| Dynamic Host MTU Discovery (`netshim.DiscoverIdentity`) | Configured host veth interfaces with MTU 1460 and MTU 1440. Invoked netshim identity discovery to verify interface MTU extraction, `Identity.MTU` assignment, and command-line parameter generation (`granule.mtu=1460` and `granule.mtu=1440`). | Pass. Host MTU is dynamically discovered and accurately formatted into guest kernel command line parameters without hardcoded defaults. |
| Guest Interface MTU Configuration (`granule-configure-routes`) | Injected `granule.mtu=1460` into the guest boot command line fixture. Executed `/usr/local/libexec/granule-configure-routes`. Inspected `/sys/class/net/eth0/mtu` and `ip link show dev eth0` to assert MTU was updated from 1500 to 1460 before route installation. | Pass. Guest interface MTU clamped to 1460 prior to routing table synchronization. |
| Inner Docker Daemon MTU Clamping (`docker-configure-mtu`) | Evaluated `/usr/local/libexec/docker-configure-mtu` against guest interface MTU 1460 and 1440. Verified that `/etc/docker/daemon.json` is updated with `"mtu": 1460` (and `"mtu": 1440` respectively) while preserving `bip` and `default-address-pools` configurations. | Pass. Inner `dockerd` configuration reflects interface MTU, preventing nested container MTU mismatch. |
| Large Payload & Git Packfile Transfer Integrity | Transferred a 100MB pseudo-random payload over the 1460 MTU link via HTTP. Executed `git init`, committed a 100MB packfile, pushed via `git daemon` over the 1460 MTU link, and cloned into a clean client. Verified SHA256 integrity (`05a65d22...`). | Pass. 100MB transfer and Git packfile clone/push completed byte-identical with zero packet loss, TCP stalls, or IP fragmentation errors. |
| Docker Subnet Collision Detection (`guestDockerNetworkRepair`) | Configured a simulated host environment with default gateway on `10.240.0.1/24` and an overlapping guest `/etc/docker/daemon.json` (`bip: 10.240.0.1/24`). Executed `guestDockerNetworkRepair 10.240.0.1`. | Pass. Conflicting `docker0` bridge removed and `/etc/docker/daemon.json` automatically reconfigured to `bip: 172.30.255.1/24` and pool `172.31.0.0/16`. |
| Host Gateway & Proxy Connectivity Post-Repair | Started an HTTP/TCP service on host gateway `10.240.0.1`. Verified that post-repair guest and nested containers reach `10.240.0.1` without route hijacking, connection drops, or `ECONNREFUSED` errors. | Pass. Host gateway services remain directly reachable; `docker0` subnet conflict resolved. |

## Verification and Execution Commands

1. **Netshim Unit and Root Network Suite**:
   ```bash
   cd third_party/granule && sudo go test -v ./pkg/netshim/...
   ```
   *Results*: All tests passed, including `TestGuestConfig_GKEPodTable`, `TestWithGuestParams`, `TestGuestScriptAppliesCarriedRoutes`, `TestStress_SpliceMTUBoundaries`, `TestStress_SpliceHighPacketThroughput` (236,718 fps), `TestSplice_MovesFramesBothWays`, and `TestSetup`.

2. **Orchestrator Docker Repair & MTU Unit Suite**:
   ```bash
   cd pkg/orchestrator && go test -v -run "TestGuestDockerNetworkRepair|TestEnsureGuestDockerNetwork|TestDockerConfigureMTU" ./...
   ```
   *Results*: `TestGuestDockerNetworkRepairScript`, `TestEnsureGuestDockerNetworkInvokesRepair`, and `TestDockerConfigureMTUScript` passed.

3. **End-to-End Walkthrough Script (`scripts/test-walkthrough-mtu-docker.sh`)**:
   ```bash
   sudo ./scripts/test-walkthrough-mtu-docker.sh
   ```
   *Results*: All 5 walkthrough steps passed with complete output log verification.
