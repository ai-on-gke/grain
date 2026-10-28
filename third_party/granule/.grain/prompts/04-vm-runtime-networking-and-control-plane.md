# Nightly Prompt: VM Runtime, Spliced Networking & Agent Control Plane

## Objective
Exercise and validate the core microVM runtime, Cloud-Hypervisor hypervisor integration (`pkg/hypervisor`), in-kernel `splice()` networking (`pkg/netshim`), in-guest agent protocol (`cmd/granule-agent`, `pkg/agent`, `pkg/execwire`), memory agent (`cmd/granule-mem-agent`, `pkg/memagent`), and the VM lifecycle CLI (`pkg/cli`, `pkg/dockervm`).

## Context & Key Workflows
Granule provides high-performance microVM execution inside containers without custom host daemons. Key architectural invariants to test nightly include:
1. **VM Lifecycle Subcommands**: `granule vm create`, `granule vm run`, `granule vm exec`, `granule vm shell`, `granule vm wait`, `granule vm status`, `granule vm update`, `granule vm delete`, `granule vm list`.
2. **Zero-Datapath Interposition Networking**: In-kernel `splice()` datapath between virtio-net and host veth/TAP devices, bypassing userspace supervisor overhead.
3. **Network Isolation Modes**: Standard bridged networking vs. isolated network mode (`CHV_NET_MODE=none`).
4. **Port Publishing**: Forwarding host ports and declared guest ports (`-publish-declared`).
5. **Direct Control Plane over Vsock/UDS**: Executing commands, streaming stdout/stderr, allocating interactive PTYs, handling signals (SIGINT/SIGTERM/SIGWINCH), and reporting accurate exit codes independent of guest networking health.
6. **Dynamic Memory Ballooning**: Memory reporting and balloon inflation/deflation via `granule-mem-agent`.

## Execution Steps

### 1. Execute Runtime, Agent, and Hypervisor Package Tests
Run tests across the runtime and agent packages:
```bash
go test -v ./pkg/hypervisor/... ./pkg/agent/... ./pkg/execwire/... ./pkg/dockervm/... ./pkg/memagent/... ./pkg/vmspec/...
```
- Verify Cloud-Hypervisor command-line argument generation and socket binding logic.
- Verify vsock/UDS wire protocol framing, multiplexed stdio streams, and exit status encoding.
- Verify memory ballooning calculation and agent event handling.

### 2. Spliced Networking and Netns Integration Tests (as Root)
Run the in-kernel network namespace and splice integration tests:
```bash
sudo modprobe tun || true
sudo env "PATH=$PATH" GRANULE_NETNS_TESTS=required go test -v -count=1 ./pkg/netshim/...
```
- Assert that network interfaces (`tap0`, `veth`), tc filters, and bridge routing rules are created and destroyed cleanly without leaking resources in the host namespace.

### 3. CLI VM Command Surface Validation
Build and test the `granule vm` CLI flag parser and help diagnostics:
```bash
go build -o /tmp/granule ./cmd/granule

for subcmd in create run exec shell wait status update delete list; do
  /tmp/granule vm "$subcmd" -h > /dev/null
done
```
- Verify that `granule vm list -status` and `granule vm status` correctly reflect published port flags and guest state schemas.

### 4. Build and Verify Agent Binaries
Compile the supervisor and guest-side agent binaries to confirm build integrity:
```bash
go build -o /tmp/granule-shim ./cmd/granule-shim
go build -o /tmp/granule-agent ./cmd/granule-agent
go build -o /tmp/granule-mem-agent ./cmd/granule-mem-agent
go build -o /tmp/granule-container ./cmd/granule-container
```

## Evaluation & Reporting Criteria
1. **Control Plane Fidelity**: Verify stdout, stderr, and exit codes are faithfully forwarded without corruption or truncation.
2. **Datapath Efficiency**: Confirm `splice()` networking setup succeeds without userspace proxy fallbacks.
3. **Resource Leak Checks**: Ensure network namespaces, tap descriptors, and vsock sockets are cleaned up upon VM termination.
4. **Failure Diagnosis**: Provide logs and stack traces if any socket timeout, race condition, or permission error occurs.
