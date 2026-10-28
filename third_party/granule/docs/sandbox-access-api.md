# Granule sandbox access API

Status: implemented. `pkg/sandbox` (server, client, dial-out `Acceptor`) landed with #1456. Then came the hardened `tcp://` listen mode, `cmd/granule-shim` wiring, `Dial`/`DialRelay`, and [granule relay](sandbox-relay.md). Slice 2 (multiplexed port forwards over the access channel, and migrating `grain`/`grit` callers) is listed in [section 8](#8-slices-and-follow-ups).

## 1. Why

`grain` and the planned `grit` CI service (`bwsalmon/grain` `docs/design/grit-split.md`) reach granule sandboxes today only by tunnelling through the Kubernetes API server:

- `kubectl exec <pod> -- granule exec -- <argv>` for commands and file writes;
- `granule forward` over `kubectl exec -i` for the git proxy, package cache and model proxy.

On GKE that path (`apiserver → konnectivity → kubelet`) fails intermittently with:

```text
unable to upgrade connection: error dialing backend: dial timeout, backstop
```

Because every command and every port-forward reconnect traverses the API server's SPDY/WebSocket upgrade path, a transient konnectivity or kubelet backstop timeout aborts running CI steps and turns CI red for hours at a time.

The way to reach a sandbox belongs **in granule**, as one API and one Go client (`pkg/sandbox`) that both `grain` and `grit` import (`grain` vendors granule under `third_party/granule`). That way `grain` and `grit` cannot drift apart in how they access sandboxes. CI semantics (steps, artifacts, test reports, attestations) stay out of granule and remain in `grit`'s thin `grit pod` layer, which drives this API.

---

## 2. Architecture and boundary

```text
+-------------------------------------------------------------------+
| Sandbox Pod / Host Container                                      |
|                                                                   |
|  +-----------------------+          virtio-vsock (port 1024)      |
|  | granule-shim ("run")  | <====================================> |
|  |  - boots CHV VM       |          +--------------------------+  |
|  |  - pkg/sandbox.Server |          | Cloud Hypervisor Guest   |  |
|  |  - holds exec buffers |          |  - cmd/granule-agent     |  |
|  +-----------------------+          |  - workload processes    |  |
|        ^             |              +--------------------------+  |
|        | listen      | dial-out (option for unreachable networks) |
+--------|-------------|--------------------------------------------+
         |             v
  tcp :7443 (TLS 1.3   Controller Acceptor
  + per-sandbox token)
         ^
         |---- in-cluster controller: sandbox.Dial("tls://<podIP>:7443")
         '---- granule relay <== one session == off-cluster controller (sandbox.DialRelay)
```

1. **Pod-side wrapper (`cmd/granule-shim` in `run` mode, outside the VM)** serves the sandbox access API via `pkg/sandbox.Server`.
2. **Wrapper ↔ guest agent (`cmd/granule-agent`)** continues to use `virtio-vsock` (`pkg/guestexec` and `pkg/execwire`), which is local to the pod/container and requires no guest network configuration.
3. **Connection modes**, configured at sandbox creation time:
   - **Listen mode over TCP** (`GRANULE_ACCESS_LISTEN=tcp://0.0.0.0:7443`) is the **primary Kubernetes topology**. The wrapper serves TLS 1.3 and requires the per-sandbox token on every connection. Whatever trusted component sits in the cluster dials the pod IP directly: an in-cluster controller with `sandbox.Dial`, or [granule relay](sandbox-relay.md) on behalf of an off-cluster controller, which reaches the relay with `sandbox.DialRelay`.
   - **Listen mode on a Unix socket** (`unix:/path/to/access.sock` or `/path/to/access.sock`) is for granule running locally on a VM host, with no network involved.
   - **Dial-out mode** (`GRANULE_ACCESS_DIAL`) is the option for networks a controller cannot reach into, such as other clusters or NAT'd pools. The wrapper connects *out* over TLS 1.3 to a controller address given at creation time, authenticates with its per-sandbox token (and optional mTLS client certificate), and keeps one long-lived multiplexed connection open with exponential backoff reconnect.

When neither `GRANULE_ACCESS_LISTEN` nor `GRANULE_ACCESS_DIAL` is set, the access server does not start and `granule run` behaves identically to existing deployments. `granule exec`, `granule cp`, and `granule forward` over `kubectl exec` / `docker exec` remain available in all modes as the bootstrap and fallback path.

---

## 3. Transport and multiplexing choice

### Candidates considered

| Candidate | Pros | Cons |
|---|---|---|
| **gRPC bidirectional streams** | Ecosystem familiarity, codegen | Pulls `google.golang.org/grpc`, `golang.org/x/net/http2`, and `google.golang.org/protobuf` into a tree whose `go.mod` has only three dependencies and is vendored into `grain`. In dial-out mode the TCP/TLS client (the pod wrapper) is the RPC server, so standard gRPC still requires a custom stream multiplexer or inverted listener underneath it. |
| **HTTP/2 bidirectional streams** | Standard framing | In Go's `net/http` and `golang.org/x/net/http2`, only the HTTP/2 client can initiate streams (`RoundTrip`). In dial-out mode the *controller* accepts the TCP/TLS connection from the wrapper and must then initiate new streams *toward* the wrapper for each `Exec`, `PutFiles`, `GetFile`, and `Health` call. |
| **`pkg/guestforward.Session` (yamux-style framed streams) over TLS 1.3 / Unix / vsock** | Zero external dependencies, already in this tree and battle-tested, symmetric stream initiation (either end can `Open` or `Accept` streams), credit-based 256 KiB per-stream flow control, built-in ping/pong keepalive and half-close (`CloseWrite`) | Requires a small JSON handshake at connection start for version and token verification. |

### Decision: `pkg/guestforward.Session` over TLS 1.3 (TCP listen, dial-out) or a Unix socket (local listen)

Granule already has a small, self-contained, credit-controlled stream multiplexer in `pkg/guestforward/mux.go` (9-byte frame headers, odd/even stream IDs so both peers can open streams concurrently without collision, per-stream receive windows with `frameWindow` credits, `frameFin` half-closes, `frameReset` aborts, and `framePing`/`framePong` keepalives).

Using `pkg/guestforward.Session` over TLS 1.3 (for dial-out) and Unix/vsock sockets (for listen) gives two decisive properties:

1. **Role inversion is trivial.** Once the initial handshake completes on a connection, the controller side runs `guestforward.NewSession(conn, true, cfg)` (opening odd-numbered streams for RPCs) and the wrapper side runs `guestforward.NewSession(conn, false, cfg)` (accepting RPC streams and opening even-numbered streams for remote port-forward connections), regardless of which peer dialed the underlying socket.
2. **Zero new dependencies in `go.mod` or `vendor/`.** `cmd/granule-shim` stays a small static binary in a `FROM scratch` image, and `grain`'s `third_party/granule` vendor tree stays dependency-free.

### Connection handshake

Before handing the byte stream to `guestforward.NewSession`, the wrapper and client exchange a single newline-terminated JSON handshake line (bounded by 64 KiB and a handshake deadline):

1. **In dial-out mode**:
   - Wrapper dials the controller over TLS (`MinVersion: tls.VersionTLS13`) and sends:
     ```json
     {"protocol":"granule-sandbox/1","sandbox_id":"sbx-123","token":"<per-sandbox-token>","agent_version":"granule-1","contract_version":3}
     ```
   - Controller `Acceptor` verifies:
     1. `protocol == "granule-sandbox/1"`;
     2. `sandbox_id` was registered with `Acceptor.RegisterSandbox` / `Acceptor.MintToken` and has not expired or been revoked;
     3. `token` matches the token bound to that exact `sandbox_id` (via `subtle.ConstantTimeCompare`), and if mTLS client certificates are enabled, the verified client certificate's CommonName or URI/DNS SAN matches `sandbox_id`;
   - Controller replies with `{"ok":true,"protocol":"granule-sandbox/1"}` (or `{"ok":false,"error":"..."}` and closes the connection).
   - Both sides start `guestforward.NewSession` on the connection.

2. **In listen mode**:
   - Client dials the wrapper's TCP listener (always TLS 1.3; directly or through granule relay, which splices the encrypted stream without reading it) or its Unix socket, and sends:
     ```json
     {"protocol":"granule-sandbox/1","token":"<per-sandbox-token>"}
     ```
   - On TCP the token is mandatory: a wrapper started without one refuses to listen, and a connection without the right token is refused. On a Unix socket the token is checked when the wrapper has one.
   - Wrapper replies with `{"ok":true,"protocol":"granule-sandbox/1","sandbox_id":"...","agent_version":"...","contract_version":3}`.
   - Both sides start `guestforward.NewSession` on the connection.

---

## 4. Stream operations

Each operation opens one multiplexed `guestforward.Stream` whose opening metadata (`st.Meta()`) is a JSON `StreamOpen` object:

```go
type StreamOpen struct {
    Op string `json:"op"` // "exec", "reattach", "signal", "put-files", "get-file", "health", "forward"
    // Operation-specific request payload follows in JSON fields.
}
```

If the wrapper accepts the operation, it calls `st.Confirm(replyBytes)` with a JSON reply; if it refuses (bad arguments, unknown exec ID, guest unreachable), it calls `st.Reject(reason)` which returns a structured error to `sess.Open`.

### 4.1 `exec`, `reattach`, and `signal`

#### Starting a command (`op: "exec"`)

- **Request (`ExecRequest`)**:
  - `ID string`: optional caller-supplied exec ID; if empty, the wrapper generates a random 128-bit hex ID.
  - `Argv []string`: command and arguments (shell-quoted into a POSIX command line for `granule-agent`, matching `guestexec.Run`).
  - `Line string`: raw shell command line (used when `Argv` is empty).
  - `Dir string`: working directory inside the guest (must be absolute if non-empty).
  - `Env []string`: `KEY=VALUE` environment overrides.
  - `User string`: guest user account (defaults to `GRANULE_EXEC_USER` / `root`).
  - `TTY bool`, `Cols uint16`, `Rows uint16`, `Term string`.
  - `Detachable bool`: when true (the default for `Client.StartExec` and reconnectable `Client.Exec`), a transport drop between the client and the wrapper leaves the guest command running and buffers its output in the wrapper for `Reattach`.
- **Confirm reply (`ExecReply`)**:
  - `ID string`: the assigned exec ID.
- **Stream framing (after `Confirm`)**:
  - Uses `pkg/execwire` frames directly on the stream:
    - Client → wrapper: `TypeStdin`, `TypeStdinClose`, `TypeWinsize`, `TypeSignal`.
    - Wrapper → client: `TypeStdout`, `TypeStderr`, `TypeNote`, and a final `TypeExit` (4-byte big-endian signed exit code).
  - **Real exit status vs. transport cut**:
    - A command that exits (with code 0, non-zero, or `-1` on signal death) always writes `execwire.TypeExit` before closing its write half (`st.CloseWrite()`).
    - If the connection drops before `TypeExit` is read, `st.Read` returns `guestforward.ErrSessionClosed` / `io.EOF` without having seen `TypeExit`, which the client surfaces as `sandbox.ErrTransportCut` (or transparently recovers from by reattaching when `Reconnect` is enabled).

#### Detach and reattach (`op: "reattach"`)

Why reattach lives in the pod-side wrapper:
- The unreliable hop is **controller ↔ pod-side wrapper** (across the cluster network).
- The **pod-side wrapper ↔ guest `granule-agent`** hop is `virtio-vsock` inside the same container, which never drops while the VM is alive.
- When `Server` starts an exec session, it creates a `managedExec` in the wrapper that holds the vsock connection to `granule-agent` and records every `TypeStdout`, `TypeStderr`, and `TypeNote` frame into a bounded replay log indexed by cumulative output byte offset (`uint64`), along with the terminal `TypeExit` code once the command finishes.
- When a client's stream breaks mid-command:
  - The wrapper detaches the broken client stream without cancelling the vsock session.
  - The guest command keeps running; its stdout/stderr continue accumulating in the wrapper's replay buffer.
- When the client reconnects and opens `op: "reattach"` with `ReattachRequest{ID: execID, Offset: receivedBytes}`:
  - The wrapper confirms the stream, immediately replays all buffered output frames from `Offset` onward, and then attaches the stream to live output (or writes `TypeExit` immediately if the command finished while detached).
  - Stdin, window resizes, and signals continue to work on the reattached stream.

#### Signalling and killing (`op: "signal"`)

- `Signal(ctx, execID, sig)` opens a short control stream `op: "signal"` with `SignalRequest{ID: execID, Signal: sig}` (or sends `TypeSignal` on an attached stream).
- `Kill(ctx, execID)` sends `SIGKILL` (`syscall.SIGKILL`, 9) and terminates the `managedExec`.

### 4.2 `files`: `PutFiles`, `PutFile`, and `GetFile`

#### Batched multi-file put (`op: "put-files"`)

Sandbox setup typically writes several files before the first build step (git credentials, `.gitconfig`, CA bundle, wrapper scripts, environment files). Doing that one `kubectl exec` per file costs 5–10 round trips.

- **Request (`PutFilesRequest`)**:
  - `Files []FileMeta`: each entry has `Path` (absolute guest path), `Size int64`, `Mode uint32` (`fs.FileMode`, default `0644`), `Owner string` (optional `user`, `user:group`, or `uid:gid`), `Atomic bool` (default `true`), `MkdirParents bool` (default `true`).
- **Stream body**:
  - After `st.Confirm`, the client streams the raw bytes of each file back-to-back (matching `Size` for each entry) and calls `st.CloseWrite()`.
- **Wrapper execution**:
  - The wrapper packs the batch into a single POSIX tar stream / staging script over **one** vsock exec session to `granule-agent`:
    - For each file, if `MkdirParents` is set, creates `dirname(path)` (`mkdir -p`).
    - If `Atomic` is true, extracts to a sibling temporary file `<dir>/.granule-put-<random>` on the same filesystem, sets `chmod` and `chown`, and runs `mv -f <tmp> <path>` so readers inside the guest never observe a half-written file.
  - Wrapper writes a JSON `PutFilesResult` frame and closes the stream.

#### Single file get (`op: "get-file"`)

- **Request (`GetFileRequest`)**: `Path string` (absolute path in the guest).
- **Confirm reply (`GetFileReply`)**: `Mode uint32`, `Size int64`.
- **Stream body**: raw file bytes streamed from the guest over vsock, followed by `st.CloseWrite()`.

### 4.3 `forwards` (`op: "forward"`)

- Bridges a multiplexed forwarding session (`pkg/guestforward`) over the sandbox access channel directly to `granule-agent`'s `execwire.Request{Forward: true}` vsock session.
- Supports both directions over the same control channel:
  - **Remote (`guest 127.0.0.1:<port> → controller target`)**: for the git proxy, package cache, and model proxy.
  - **Local (`controller listener → guest 127.0.0.1:<port>`)**: for reaching an HTTP server or debugger inside the guest.

### 4.4 `health/info` (`op: "health"`)

- **Request**: `op: "health"`.
- **Confirm reply (`HealthInfo`)**:
  - `GuestUp bool`: whether the guest answers `guestexec.Ready` over vsock (including checking `GRANULE_READY_FILE` if configured).
  - `ReadyDeclared bool`: false if `GRANULE_READY_FILE` is configured and not yet present inside the guest.
  - `ReadyError string`: diagnostic message when `GuestUp` is false.
  - `AgentVersion string`: wrapper/agent version string.
  - `ContractVersion int`: guest boot contract version (`pkg/guestimage.Contract`).
  - `SandboxID string`: the sandbox identifier.

---

## 5. Hard security requirements

Bringing back a control channel must not introduce an escape route from an untrusted guest workload to the controller or the cluster network.

### 5.1 Control channel terminates in the pod-side wrapper; guest has no route to the controller

1. **Termination outside the VM**: `pkg/sandbox.Server` runs inside `cmd/granule-shim` (`granule run`) in the pod's container namespace, outside Cloud Hypervisor. The guest only ever sees `virtio-vsock` connections initiated by the host (`VMADDR_CID_HOST`).
2. **Token isolation**:
   - The per-sandbox token is passed to `granule run` via `GRANULE_ACCESS_TOKEN_FILE` (recommended: a mounted volume or tmpfs file that `granule run` reads once at startup and immediately `os.Remove`s) or `GRANULE_ACCESS_TOKEN` (which `granule run` reads and immediately `os.Unsetenv`s before starting any child process).
   - The token is never placed on `CHV_CMDLINE` (`/proc/cmdline`), never passed to `guestexec` environment variables, and never written to any disk or virtio-fs share attached to the VM.
3. **Network isolation between the guest and the controller (`pkg/netshim`)**:
   - In a sandbox with `CHV_NET_MODE=none` (no guest network), the VM has no NIC at all while the pod's `eth0` remains available to `granule-shim` in the pod network namespace to dial the controller.
   - In a sandbox with `CHV_NET_MODE=inline` or `external` (where `netshim` splices the pod's `eth0` onto the guest's `tap-<vm>` so the guest has network access, including open egress):
     - Normally, `netshim`'s `spliceGuest` redirects all ingress frames on `eth0` to `tap-<vm>` and all allowed egress frames on `tap-<vm>` to `eth0`.
     - When controller IPs are configured (`NETSHIM_CONTROLLER_IPS` or automatically resolved from `GRANULE_ACCESS_DIAL`), `netshim` installs two sets of `tc u32` filters at the top of the ingress qdiscs:
       1. **On `tap-<vm>` ingress (traffic sent by the guest)**: priority-1 `TC_ACT_SHOT` drop filters drop every IPv4 packet (`dst == controllerIP`), ARP packet (`tpa == controllerIP`), and IPv6 packet (`dst == controllerIP6`) from the guest before the guest identity allow rules run. Even when the guest has open internet/cluster egress, its packets to the controller are dropped at the tap before reaching `eth0` or the pod's network stack.
       2. **On `eth0` (`ext`) ingress (traffic arriving from the network)**: priority-1 `TC_ACT_OK` pass filters match every IPv4/IPv6 packet with `src == controllerIP` and hand it to the pod's own kernel network stack (where `granule-shim`'s dial-out socket lives) instead of redirecting it to `tap-<vm>`. In addition, ARP replies (`op == 2`) and IPv6 Neighbour Advertisements (`icmpv6 type == 136`) arriving on `eth0` are mirrored (`TCA_EGRESS_MIRROR` with `TC_ACT_PIPE`) to `tap-<vm>` and passed (`TC_ACT_OK`) to `eth0` so both the pod's kernel stack and the guest can resolve the gateway's MAC address, while not a single controller packet ever enters `tap-<vm>`.

   - When the wrapper listens on TCP (`GRANULE_ACCESS_LISTEN=tcp://...`), `granule-shim` sets `NETSHIM_ACCESS_PORTS` to that port. `netshim` then installs `eth0` ingress pass filters for TCP segments to the port (IPv4 with a 20-byte header, and IPv6), handing them to the pod's own stack from any source. They never reach `tap-<vm>`. granule relay and in-cluster controllers can therefore connect without their pod IPs being known in advance, while the guest, which owns the rest of the pod's ingress, never sees control traffic. A guest connecting to the pod IP reaches only itself, since it holds that address.
4. **Proven against a real guest:** `test/sandbox-access-vm.sh` (CI: `sandbox access VM`) boots a guest under `granule-shim` with a TCP listener and inline networking, then drives exec, `PutFiles` and reattach from a peer network namespace, both directly and through granule relay. It checks that the guest, which owns the pod's address and has open egress, cannot connect to the listener and sees no token on its command line, in any process environment or in its files. It also checks that the token file is unlinked and that cloud-hypervisor does not inherit the token. `pkg/netshim`'s kernel tests (`TestSpliceGuestWithControllers_*`, `TestSpliceGuestWithAccess_KeepsAccessPortInPod`) prove the filters at the packet level.

### 5.2 Kubernetes: listen with NetworkPolicy, relay for off-cluster controllers

- In Kubernetes, sandbox pods listen on TCP 7443 (`GRANULE_ACCESS_LISTEN=tcp://0.0.0.0:7443`) and are dialed by the relay or an in-cluster controller. See [sandbox-relay.md](sandbox-relay.md).
- `deploy/k8s/sandbox-access-networkpolicy.yaml`:
  1. `granule-sandbox-access-ingress` applies to every sandbox pod (`granule.gke.io/sandbox: "true"`). It denies ingress and egress by default, and its only ingress allows TCP 7443 from the relay pods (`app.kubernetes.io/name: granule-relay`) and the in-cluster controller's pods. Sandbox pods are not among them, so sandbox-to-sandbox traffic is blocked. NetworkPolicy is the first line of defence; the TLS and token check stays mandatory because sandboxes run untrusted agents.
  2. `granule-relay` confines the relay's egress to sandbox pods' access port and the API server.
  3. `granule-sandbox-guest-egress` applies only to sandboxes that opt into guest network access (`granule.gke.io/network-access: "open"`).
- Sandboxes that use dial-out instead replace the ingress rule with an egress rule to the controller on its port.

### 5.3 Per-sandbox authentication

- **Listen mode:** the controller generates a random token per sandbox and hands it to the sandbox pod as `GRANULE_ACCESS_TOKEN_FILE` (a Secret volume), along with a TLS certificate and key for the listener. It keeps the token, and the CA that verifies the certificate, for its own `Dial`/`RelayClient.Client` calls. The wrapper compares tokens with `subtle.ConstantTimeCompare`.
- **Dial-out mode:** the controller's `sandbox.Acceptor` mints a unique token per sandbox (`Acceptor.Issue(sandboxID, ttl)`), bound to that `sandboxID` and an expiration timestamp `expiresAt` (matching the sandbox's lifetime).
- All dial-out connections use TLS 1.3 (`MinVersion: tls.VersionTLS13`), verifying the controller's certificate against the configured CA pool (`GRANULE_ACCESS_CA_FILE`), with optional mutual TLS (`GRANULE_ACCESS_CERT_FILE` / `GRANULE_ACCESS_KEY_FILE` where the client certificate's CN or SAN must equal `sandboxID`).
- During the opening handshake, `Acceptor` checks that:
  1. `sandboxID` was issued by this controller;
  2. `time.Now()` is before `expiresAt`;
  3. the presented token matches the token bound to `sandboxID` (`subtle.ConstantTimeCompare`);
  4. if mTLS client cert verification is active, the client cert's identity matches `sandboxID`.
- A token for `sandbox-a` is rejected when presented with `sandbox-b`, an expired or revoked token is rejected, and `Acceptor.Revoke(sandboxID)` invalidates the token and closes any active session when the sandbox terminates.

---

## 6. Configuration (`cmd/granule-shim`)

These are read by `granule run` (`cmd/granule-shim/access.go`). With neither `GRANULE_ACCESS_LISTEN` nor `GRANULE_ACCESS_DIAL` set, the access server does not start.

| Environment variable | Description |
|---|---|
| `GRANULE_ACCESS_LISTEN` | Listen address: `tcp://host:port` (TLS 1.3, token mandatory; the Kubernetes topology), or `unix:/path/to/sock` / `/path/to/sock` for a local VM host. |
| `GRANULE_ACCESS_TLS_CERT_FILE` | PEM server certificate for a `tcp://` listener. Required with `tcp://`. |
| `GRANULE_ACCESS_TLS_KEY_FILE` | PEM private key for that certificate. Required with `tcp://`. |
| `GRANULE_ACCESS_DIAL` | Controller `https://host:port` (or `tls://host:port`) address for dial-out mode. |
| `GRANULE_ACCESS_SANDBOX_ID` | Sandbox identifier: sent in the dial-out handshake (required with `GRANULE_ACCESS_DIAL`), and checked against a listen-mode client's, when the client gives one. |
| `GRANULE_ACCESS_TOKEN` | Per-sandbox bearer token. Unset at startup, before cloud-hypervisor or any other child starts, so no child inherits it. The kernel still shows it in `granule-shim`'s own `/proc/<pid>/environ`, which is visible to the container but never to the guest, so prefer the file. |
| `GRANULE_ACCESS_TOKEN_FILE` | Path to a file containing the token, preferred over `GRANULE_ACCESS_TOKEN`. Read and unlinked at startup; a read-only mount is left with a warning. The VM never sees this container's filesystem. |
| `GRANULE_ACCESS_CA_FILE` | PEM CA bundle used to verify the controller's TLS certificate in dial-out mode. |
| `GRANULE_ACCESS_CERT_FILE` | Optional PEM client certificate for mTLS in dial-out mode. |
| `GRANULE_ACCESS_KEY_FILE` | Optional PEM client private key for mTLS in dial-out mode. |
| `GRANULE_ACCESS_CONTROLLER_IPS` | Comma-separated controller addresses for `netshim` to keep away from the guest. They are appended to `NETSHIM_CONTROLLER_IPS`, as is a literal-IP `GRANULE_ACCESS_DIAL` host. |
| `NETSHIM_CONTROLLER_IPS` | (netshim) Addresses the guest may not send to, and whose traffic on `eth0` goes to the pod's stack. |
| `NETSHIM_ACCESS_PORTS` | (netshim) TCP ports whose ingress on `eth0` goes to the pod's stack from any source. `granule run` sets it from a `tcp://` `GRANULE_ACCESS_LISTEN`. |

`granule run` passes the two `NETSHIM_*` values to netshim for inline networking. With `CHV_NET_MODE=external`, set the same variables on the netshim init container. A `tcp://` listener with no token or no certificate fails the boot rather than serving unauthenticated.

---

## 7. Go client (`pkg/sandbox`)

```go
// In-cluster controller: dial a sandbox pod's TCP listener directly.
client, err := sandbox.Dial(ctx, "tls://10.4.2.17:7443", sandbox.DialOptions{
    Token:     token,
    TLSConfig: &tls.Config{RootCAs: sandboxCA, ServerName: "sandbox-42"},
})

// Off-cluster controller: one session to granule relay, then a Client per sandbox.
relay, err := sandbox.DialRelay(ctx, "relay.example.com:443", sandbox.RelayCredentials{
    Token: controllerToken, TLSConfig: &tls.Config{RootCAs: relayCA},
})
client, err = relay.Client(ctx, "sandbox-42", sandbox.DialOptions{Token: token, TLSConfig: &tls.Config{RootCAs: sandboxCA}})

// Either transport behind one interface (sandbox.Connector):
var conn sandbox.Connector = sandbox.RelayConnector{Relay: relay, Options: optsFor}
client, err = conn.Connect(ctx, "sandbox-42")

// Dial-out mode: accept connections from sandboxes on the controller.
acceptor, err := sandbox.ListenTLS(":8443", serverTLSConfig)
go acceptor.Serve(ctx)
tok, err := acceptor.Issue("sandbox-42", 2*time.Hour)
client, err = acceptor.WaitClient(ctx, "sandbox-42")

// Health check:
info, err := client.Health(ctx)

// Batched multi-file put (one round trip, each file replaced atomically):
err = client.PutFiles(ctx, []sandbox.FileUpload{
    {Path: "/etc/sandbox/env", Content: []byte("CI=true\n"), Mode: 0o644},
    {Path: "/usr/local/bin/step", Content: scriptBytes, Mode: 0o755},
})

// Streaming exec with a real exit code, and reattach after a dropped transport:
proc, err := client.StartExec(ctx, sandbox.ExecRequest{
    Argv:    []string{"sh", "-c", "make test"},
    Workdir: "/work",
    Env:     []string{"GOFLAGS=-mod=vendor"},
})
code, n, err := proc.Wait(stdoutWriter, stderrWriter)
if errors.Is(err, sandbox.ErrTransportCut) {
    // Clients from Dial and RelayClient redial on their next call.
    code, err = client.Reattach(ctx, proc.ID(), n, stdoutWriter, stderrWriter)
}
```

---

## 8. Slices and follow-ups

- **Landed with #1456:**
  - This design doc and a first NetworkPolicy manifest, written for dial-out.
  - The `pkg/netshim` controller guard (`NETSHIM_CONTROLLER_IPS` / `Config.ControllerIPs`).
  - The `pkg/sandbox` library: wrapper server, wire framing, `Client`, and the controller-side dial-out `Acceptor`. It supports `Exec` (streaming, exit codes, detach/reattach, signal/kill), `PutFiles` / `PutFile` / `GetFile`, and `Health`.
  - Unit tests for framing, reconnecting mid-command with reattach, per-sandbox auth refusals, and the netshim guard.
- **Landed with the relay change (#1478):**
  - A hardened `tcp://` listen mode: TLS 1.3, and a mandatory per-sandbox token (`ErrTokenRequired`, `ErrTLSRequired`).
  - `cmd/granule-shim` wiring (`GRANULE_ACCESS_*`, token scrubbing, `netshim` plumbing), and `NETSHIM_ACCESS_PORTS`, which keeps the access port in the pod's stack under inline networking.
  - `sandbox.Dial` with automatic redial, `sandbox.DialRelay`, and `sandbox.Connector`.
  - [granule relay](sandbox-relay.md) (`pkg/relay`, `granule relay`).
  - `deploy/k8s/granule-relay.yaml`, and a listen-mode NetworkPolicy replacing the dial-out one.
  - Unit tests over the relay and direct to TCP covering exec, files, signal, and reattach after a dropped connection.
  - Refusal tests: unauthenticated controllers, bad or missing tokens, a tokenless listener, and targets outside the namespace or selector.
  - The access-port packet filter tested in a real network namespace.
  - A guest-boot integration test (`test/sandbox-access-vm.sh`, section 5.1).
- **Follow-ups:**
  - Wire `LocalForward` / `RemoteForward` end-to-end over the `pkg/sandbox` session into `pkg/guestforward.ServeAgent`.
  - Move `bwsalmon/grain` (#1479) and `grit pod` onto `pkg/sandbox`, keeping `kubectl exec` as the fallback.
