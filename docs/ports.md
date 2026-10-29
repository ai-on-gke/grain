# Ports

grain's fixed ports, who owns each one, and the convention new ones
follow.

## The convention

**Ports grain listens on by default and by convention start with 42:
4200-4299.** A 42xx port on a grain host is grain's, and a port outside
that range belongs to somebody else. The daemon UI's 4242 set the
pattern.

This applies to *fixed* ports, the ones an operator types, a firewall
rule names, or a bookmark, unit file or manifest remembers. It does not
apply to ports picked at run time: ephemeral listeners, sandbox
forwards, per-attempt capability data planes, and test servers on `:0`.
Ports below 1024 are fixed by their protocols (80, 443, 53) and are not
in scope either.

The range is split so that adding one kind of port never moves another:

| Range     | For                                                        | Allocated                                    |
|-----------|------------------------------------------------------------|----------------------------------------------|
| 4242      | the daemon's UI and API                                    | `ports.UI`                                   |
| 4243-4249 | further daemon services that operators or remote machines connect to | 4243 is suggested for `-executor-addr` (`ports.DaemonServicesMin`); 4244 is the sandbox-access acceptor (`ports.SandboxAccess`) |
| 4280-4289 | persistent sandboxes' `host_port` (staging environments)   | 4280: the staging grain (`ports.StagingGrain`) |
| 4290-4299 | dev and demo servers                                       | 4290: `grain demo` (`ports.Demo`)            |
| 4251      | the gchat capability server's data plane (loopback) | `intree.DefaultGChatPort` (`pkg/capability/intree/gchat.go`) |
| 4200-4241, 4252-4279 | unallocated                                     |                                              |

The constants are in [`pkg/ports`](../pkg/ports/ports.go). Code names
them rather than writing the number. Do not use `pkg/portpick` for this:
it ports commits between paired branches and has nothing to do with
network ports.

### Allocating a new port

1. Pick the next free port in the band it belongs to and add a constant
   to `pkg/ports`.
2. Add a row to the table below.
3. If it genuinely cannot be in 42xx (another program's well-known port,
   a protocol-fixed one), add it to the allowlist in
   `pkg/ports/lint_test.go` with a reason, and add a row here that says
   why.

### The lint

`pkg/ports`'s `TestNoFixedPortsOutsideTheConvention` (run by the `go unit`
CI job) walks every non-test `.go` file outside `third_party/`, `tests/`
and `ui/`. It fails on a fixed port outside 42xx that is not
allowlisted. It recognises three shapes:

- a string literal with a `host:port` or `:port` in it, such as
  `"127.0.0.1:8420"` or `"http://grain.internal:8422/api"`;
- an integer given to a name with "port" in it: `const GitPort = 9728`,
  `Port: 9728`, `port = 9728`;
- an integer default of a flag whose name has "port" in it.

`TestAllowlistIsUsed` fails on an allowlist entry that no longer matches
anything, so a stale exception cannot hide a new port. Comments,
Markdown, shell and YAML are not linted. For those, this file is the
record.

## Every fixed port

Class:

- **operator**: an operator, browser or remote machine connects to it.
- **sandbox**: a sandbox connects to it. The number is written into a
  sandbox's environment, an image or an agent's notes.
- **internal**: fixed, but only grain's own processes use it.
- **third-party**: another program's well-known port.

### Under the convention

| Port | Class | Owner | Where |
|------|-------|-------|-------|
| 4242 | operator | daemon UI/API | `grain daemon -ui-addr` default `0.0.0.0:4242` (`pkg/cli/daemon.go`), the CLI's `http://127.0.0.1:4242` (`pkg/cli/main.go`), `hostinit.DefaultUIAddr`, `scripts/setup.sh` and `google/scripts/cloudtop-install.sh` `GRAIN_UI_ADDR`, `scripts/client.sh`, `ui/vite.config.js` dev proxy |
| 4243 | operator | remote-executor listener (suggested) | `-executor-addr` is off by default; its help suggests `:4243` |
| 4244 | operator | sandbox-access acceptor: k8s-granule sandbox pods' wrappers dial out to it over mTLS | `ports.SandboxAccess`, `sandboxaccess.DefaultPort`; `-sandbox-access-addr=:4244` and the `grain-access` Service in `deploy/kubernetes/grain-granule.yaml`; `deploy/kubernetes/sandbox-access-networkpolicy.yaml` |
| 4280 | operator | the staging grain persistent sandbox | a persistent sandbox's `host_port` (grain/config `tables/persistent_sandbox.json`); see [below](#persistent-sandboxes) |
| 4251 | operator | gchat capability data plane (container, or on the host via hostd) | `-listen 127.0.0.1:4251`, `intree.DefaultGChatPort`; the built-in entry's args and hostd's `grain-gchat` entry |
| 4290 | operator | `grain demo` | `-addr` default `127.0.0.1:4290` (`pkg/cli/demo.go`); was `127.0.0.1:8420` |
| 4295, 4296 | developer | `tests/bench/tunnelbench` (tunnel-vs-direct benchmark) | endpoint server `-addr :4295` and the forward process's CPU stats; a throwaway benchmark, never part of a deployment |

### Grandfathered: sandbox-facing

These were fixed before the convention and are written into places
grain cannot rewrite from one side: sandbox images, agent prompts,
memory notes (`http://192.168.9.1:9728/...`, `http://192.168.9.1:9729`)
and the environment of every running sandbox, which is written once
when the sandbox is created and has to keep working across an upgrade.
Moving them is a breaking change, and the operator has to decide it
explicitly. See [Migrating the sandbox block](#migrating-the-sandbox-block).

| Port | Class | Owner | Where |
|------|-------|-------|-------|
| 9728 | sandbox | git proxy; the base of the `-sandbox-port-base` block | `cli.DefaultSandboxPortBase`, `daemon.OffsetGitProxy`, `intree.DefaultGitPort`, `deploy/kubernetes/grain.yaml` (`-sandbox-port-base=9728`, the `grain-sandbox` Service) |
| 9729 | sandbox | package cache (block offset 1) | `daemon.OffsetPackageCache`; pip/apt/docker config in sandboxes (`pkg/pkgcache`) |
| 9729+ | sandbox | model-proxy capability server's data planes, one per vendor | `intree.DefaultModelProxyPort` (a separate capability server process, not the daemon's block) |
| 9730 | sandbox | Actions cache (block offset 2) | `daemon.OffsetActionsCache` |
| 9731 | sandbox | capability port relay (block offset 3) | `daemon.OffsetCapabilityPorts` |
| 9744-9746 | sandbox | daemon model proxies: claude, openai, gemini (block offsets 16-18) | `daemon.OffsetModelProxyBase`, `deploy/kubernetes/grain.yaml` |
| 9728-9983 | sandbox | the whole block (256 ports, aligned) | firewall rules, nftables, `pkg/daemon/sandboxnet.go` |
| 9727 | sandbox | self-debug capability data plane | `-listen 127.0.0.1:9727` (`pkg/capability/intree/selfdebug.go`) |
| 9733 | sandbox | host-script capability data plane | `intree.DefaultHostScriptPort` |
| 38017 | sandbox | first guest-loopback port for a CI job's capability endpoints | `orchestrator.ciEndpointPortBase` (inside the guest only) |

### Grandfathered: operator-facing, not moved

These are outside 42xx, but moving them changes something an operator or
an external client already points at. They stay where they are until an
operator decides otherwise.

| Port | Class | Owner | Why it stays |
|------|-------|-------|--------------|
| 8420 | operator | the UI's default before 4242 | `hostinit.LegacyUIAddr`: a unit an older `grain init` wrote is pinned to `127.0.0.1:8420` rather than moved (`settleUIAddr`). `scripts/client.sh -r 8420` reaches such an install |
| 8422 | operator | remote-executor listener, by custom | Not a default (`-executor-addr` is empty), but `scripts/run-remote-executor.sh`, `pkg/ui/executor_install.sh` and existing executors' `--server` name `:8422`. New deployments should use 4243 |
| 8080 | operator | the daemon's port inside a Kubernetes pod | `deploy/kubernetes/grain.yaml` and `grain-granule.yaml` `-ui-addr=0.0.0.0:8080`, `containerPort: 8080`. The Service publishes port 80, so a move is cheap inside the manifest, but an operator's own Ingress or `kubectl port-forward` may name the pod port |
| 8080 | operator | Terraform `ui_port` default | `deploy/terraform/gcp/variables.tf`. A changed default moves the load balancer backend and firewall rule on the next `terraform apply` |
| 8080 | operator | checkpost | `cmd/checkpost serve -addr :8080`, a separate service whose deployment publishes that port |
| 8080 | operator | github-webhook gateway | `cmd/github-webhook serve -addr :8080`, a separate service whose deployment (`deploy/kubernetes/github-webhook.yaml`) publishes that port; `-github-webhook-url`'s help example names it |

### Third-party and protocol-fixed

| Port | Class | Owner | Where |
|------|-------|-------|-------|
| 5432 | third-party | PostgreSQL | the embedded server (`pkg/model/postgres/embedded.go`), DSN defaults |
| 5000 | third-party | a local Docker registry | `localhost:5000/granule:latest` (`scripts/setup.sh`), `-upgrade-image` examples |
| 3128 | third-party | an HTTP proxy (Squid) | `HTTPS_PROXY` examples in `scripts/setup.sh` |
| 8443 | third-party | an example GitHub Enterprise `host:port` | validation messages in `pkg/ui/settings.go` |
| 443, 80 | sandbox | package cache interception (TLS and plain) | fixed by their protocols (`pkg/pkgcache`) |
| 53 | sandbox | sandbox DNS resolver | fixed by its protocol |

### Dynamic: not under the convention

| Ports | Owner |
|-------|-------|
| `:0` / ephemeral | test servers, `grain demo -addr 127.0.0.1:0` in e2e, the daemon with `-sandbox-port-base=0` |
| per attempt | capability `ports` grants' `host_port` (`docs/capabilities-spec.md` §5.3) |
| `-granule-base-port` | ignored, and to be removed; it used to forward 12000-12255 |
| test-only literals | `_test.go` files, `tests/`, `docs/test-runs/` and `docs/hand-runs/` (8421, 8430, 18080, 18420 and the like) are what one run happened to pick, not defaults |

## Persistent sandboxes

A persistent sandbox's `host_port` (`model.PersistentSandbox.HostPort`)
is a port on the grain host. It is fixed and operator-facing, so it comes
from 4280-4289. The staging grain is on **4280**:

```json
{
  "name": "staging",
  "repo": "bwsalmon/grain",
  "ref": "main",
  "auto_update": true,
  "command": "<what starts the staging grain, listening on 4242>",
  "sandbox_port": 4242,
  "host_port": 4280,
  "ui_subpath": "/staging/staging",
  "strip_prefix": true,
  "enabled": true
}
```

The live definition is in grain/config, not this repository.
`sandbox_port` is whatever the command listens on inside the sandbox.
It is not on the host, so it can be grain's own 4242 without colliding.
The supervisor refuses a `host_port` the daemon itself listens on
(`-ui-addr`, `-executor-addr`), and refuses two sandboxes on one port.

## Migrating the sandbox block

This is proposed, and not implemented. Moving 9728/9729 into 42xx
cannot be done in one step: a sandbox created before the upgrade holds
`http://192.168.9.1:9728` in its git remote and `:9729` in its pip, apt
and docker configuration until it is destroyed. A 256-port aligned block
does not fit in 42xx either (4096-4351 straddles the range), so the move
would also have to shrink the block or keep only the singletons in 42xx.

1. **Dual-listen for one release.** Serve every block service on both
   its old port and its new one. Hand *new* sandboxes the new URLs, and
   leave running sandboxes on the old ones. Open both ranges in the
   firewall and nftables rules, and in the Kubernetes `grain-sandbox`
   Service. Update the images, the agent prompts and the memory notes
   that name 9728/9729.
2. **Switch.** In the next release, stop listening on the old ports and
   close their rules. A sandbox older than one release is gone by then.
   A persistent sandbox that has lived that long is restarted with the
   new environment.
3. Remove 9728/9729 from the allowlist in `pkg/ports/lint_test.go` and
   from this file.

The operator has to decide this explicitly, and it has not been decided.
