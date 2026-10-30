# Grain Capability Architecture & Control-Plane Protocol Specification

**Status:** Normative specification of the target capability architecture. It is **not yet implemented in this repository**: the capabilities a deployment offers today are the in-process Go registry under `pkg/capability/` described in [`docs/ci-capabilities.md`](ci-capabilities.md). This document specifies what replaces that registry; until the migration lands, `pkg/capability/` describes the present and this document the destination. The protocol layer of §2–§5 (everything up to materialization in a sandbox) is implemented in `pkg/capability/protocol`. Grants of capability references are accepted wherever a `capabilities:` list is (recorded as `<ref>` or `<ref>={json args}` strings, normalized at creation and again at admission: `pkg/model/capability_ref.go`), checked against `grain_config.capability_policy` (§4.2.2), and minted for agent task runs at admission (`pkg/orchestrator/capability_pass.go`) and for `.grain/workflows` CI jobs at their grant admission, before the job's sandbox is built (`pkg/daemon/ci_capability_pass.go`; a job's references need not be in the repository's CI capabilities, and an untrusted run's are denied unless a policy rule opts them in). A minted pass is materialized into the agent run's sandbox before the agent starts (`pkg/orchestrator/capability_materialize.go`, `capability_endpoints.go`) and into a CI job's after its checkout and before its first step, its endpoints forwarded over the capability port relay and listed in `/home/debian/.grain/mcp.json` (`pkg/orchestrator/capability_ci.go`).

Capabilities are how Grain equips an isolated sandbox VM or CI container with external access and tools for the duration of a single attempt: a configured Capability Server mints temporary credentials and files into the sandbox filesystem (`placements` — a leased cloud key, an API token, a `.git-credentials` entry, or a static skill/playbook; §6.1, §6.4), exposes an attempt-scoped MCP toolset to the agent and CI container (`mcp_endpoint` at `/runs/<attempt_id>/mcp`; §6.2), bridges authenticated host-side proxies and tunnels such as a Git proxy, a model proxy, or a database tunnel into sandbox loopback (`ports`; §6.3), runs setup commands inside the sandbox before the agent starts — the one `setup_root` command that checks the attempt's primary repository out into the root working directory, and per-grant `setup` commands that clone additional repositories beside it or activate what was placed (§5.5, §6.3) — and tells the agent what was provisioned and how to use it (`prompt_section`). Every stateful resource is revoked by `release_<method>` when the attempt ends, or expires at `deadline` if that call never arrives.

This document specifies how Grain deployments configure and connect to Capability MCP servers in `grain/config` (and through the Settings UI and CLI) following Antigravity (`agy`)'s `mcpServers` specification shape ([`docs/agy-surface.md`](agy-surface.md)) — using **stdio** (`command`, `args`, `env`) for in-tree `grain` capabilities and `grain/config` capability plugins, and **HTTP** (`serverUrl`, `headers`) for external capability servers, with sensitive `env` and `headers` values resolved from Grain's encrypted secrets store (§3) — how tasks, standing processes, and `.grain/workflows` CI jobs request capability grants (`<capability_id>[#<method>]` with optional `with:` arguments; §2), how Grain discovers capability schemas (`mint_<method>`) and teardown tools (`release_<method>`) via `tools/list`, validates arguments, mints one grant-scoped resource per `attempt_id` (`tools/call` on `mint_<method>` with `{attempt_id, deadline, args}`; §4), materializes `placements`, `mcp_endpoint` (wired into the active agent CLI — `agy`, `claude`, `codex`, or custom — in that CLI's native format, and into CI containers), `ports`, `setup_root`, `setup`, and `prompt_section` (§5), and releases each grant when the attempt finishes (`tools/call` on `release_<method>` with `{attempt_id}`).

A concise guide for capability authors is in [`docs/capabilities.md`](capabilities.md). Where the guide ever disagrees with this specification, **this document wins**.

## Contents

1. [Terminology, Trust Model & Architecture Overview](#1-terminology-trust-model--architecture-overview)
2. [Capability Identifiers & Grant Syntax](#2-capability-identifiers--grant-syntax)
3. [Deployment Configuration & Trust Boundary](#3-deployment-configuration--trust-boundary)
4. [Control-Plane MCP Protocol](#4-control-plane-mcp-protocol)
5. [Materialized Primitives](#5-materialized-primitives)
6. [Standard Capability Patterns](#6-standard-capability-patterns)

---

## 1. Terminology, Trust Model & Architecture Overview

### 1.1 Conventions

1.1.1. The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD", "SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY", and "OPTIONAL" in this document are to be interpreted as described in BCP 14 (RFC 2119, RFC 8174) when, and only when, they appear in all capitals.

1.1.2. Grammars use ABNF (RFC 5234). JSON schemas follow JSON Schema Draft 2020-12 / Draft-07. Every timestamp on the wire MUST be an RFC 3339 `date-time` (for example, `"2026-10-19T23:00:00Z"`). Sizes are counted in bytes after decoding.

### 1.2 Terminology

**Capability Server**
: An MCP server implementing the capability control-plane protocol (`initialize`, `tools/list`, `mint_<method>`, `release_<method>`; §4), configured under `mcpServers` in `grain/config` (and editable through the Settings UI and CLI) following Antigravity (`agy`)'s `mcp_config.json` specification shape ([`docs/agy-surface.md`](agy-surface.md) lines 145–174 and 1542–1663; §3.1):
  1. **Stdio (`command`, `args`, `env`)** — an **in-tree capability** compiled into the `grain` binary (for example, `"command": "grain", "args": ["capability", "gcp"]`) or an **in-config (`grain/config`) capability plugin** committed to the `grain/config` state repository (where a relative `command` such as `"plugins/custom-plugin/server"` is resolved from the root of the checked-out `grain/config` state repository, matching custom agent backends in [`docs/custom-agent-backends.md`](custom-agent-backends.md)), which the Grain daemon spawns and communicates with over standard input/output (JSON-RPC 2.0 over stdio).
  2. **HTTP (`serverUrl`, `headers`)** — an **external capability server** running outside `grain` and `grain/config` (standalone process, `systemd` service, OCI container, sidecar, or remote HTTP service) that speaks Streamable HTTP MCP (JSON-RPC 2.0 over HTTP(S)) at `serverUrl` (for example, `"http://127.0.0.1:9802/mcp"` or `"https://cap.example.com/mcp"`).

**`capability_id`**
: The canonical `https://` URL (without query or fragment) that names a Capability Server in Grain's `mcpServers` configuration and in capability references (for example, `https://github.com/ai-on-gke/grain/capabilities/gcp`). It is an opaque lookup key: Grain never connects to the `capability_id` URL itself (§3.2.1).

**Capability Method (`#<method>`)**
: A named capability referenced as `<capability_id>[#<method>]` (for example, `gcp-key`, `git-repo`, `model-proxy`, `self-debug`, or `skill`) and advertised by a Capability Server in `tools/list` as a mint tool named **`mint_<method>`** whose `inputSchema` accepts `{attempt_id, deadline, args}`; `inputSchema.properties.args` is the schema of the method's `with:` arguments. A stateful method also advertises a teardown tool **`release_<method>`** accepting `{attempt_id}`; a stateless method MAY omit it (§4.1.2).

**Grant (`{capability, with}`)**
: An authorization permitting a task, standing process, template, schedule, or CI job to invoke one `<capability_id>#<method>` with one validated argument object (`args`).

**`attempt_id`**
: The identifier Grain assigns to **each** grant mint call within a task execution attempt or CI job attempt. It MUST match `^[A-Za-z0-9._-]{1,128}$` and MUST be unique across every deployment attached to the same Capability Server; Grain forms it as `<deployment>-<task>-<attempt>-<grant index>` (for example, `e1010c47-175-2-0` for the first grant of attempt 2 of task 175, `e1010c47-175-2-1` for the second). Every `mint_<method>` call therefore maps 1-to-1 to one resource allocation and one `release_<method>` call. The Capability Server keys everything it creates for the call (upstream credentials, `/runs/<attempt_id>/mcp`, per-attempt proxy tokens) under `attempt_id`. `attempt_id` is an identifier, not a secret; control-plane authentication uses the server's configured transport and secrets (§3.1.3).

**Secret Reference (`$secret:<name>`) & Control-Plane Authentication**
: A reference in a capability server's `env` (for stdio servers) or `headers` (for HTTP servers, such as `"Authorization": "Bearer $secret:external-cap-token"`) to a secret stored in **Grain's encrypted secrets store** (§3.1.2). Because `grain/config` is exported as plain text in Git, secret values MUST NOT be committed in cleartext in `grain/config`; Grain resolves `$secret:<name>` from the encrypted secrets store in memory at runtime when spawning a stdio server or sending control-plane and proxied data-plane HTTP requests (§3.1.2–§3.1.3).

**Placement**
: A file (`{path, mode, owner, content, encoding}`) returned by a mint call that Grain writes atomically into the run's sandbox filesystem before agent or CI steps begin (§5.1).

**MCP Endpoint (`mcp_endpoint`)**
: An OPTIONAL attempt-scoped MCP endpoint, always the server-relative path `/runs/<attempt_id>/mcp`, that Grain proxies into the active agent CLI (`agy`, `claude`, `codex`, or custom backend) in that CLI's native per-run MCP configuration alongside `grain-sandbox` and into CI containers at `/home/debian/.grain/mcp.json` — using Grain's HTTP path proxy for external HTTP capability servers or bridging stdio-backed capability tools (`mcp.StartCommandHTTP` / stdio proxy) for stdio capability servers (§5.2). One endpoint serves any number of tools via standard `tools/list`, so a grant needs at most one.

**Sandbox Port Mapping**
: A per-run TCP forward `{host_port, sandbox_port}` (both `1024..65535`; `sandbox_port` defaults to `host_port`) from a dedicated data-plane listener on the capability server host (`127.0.0.1:<host_port>`, verified to be owned by the Capability Server's own service) to loopback inside the run's sandbox (`127.0.0.1:<sandbox_port>`), reachable even under `network_access: "none"` (§5.3). Separate `host_port` and `sandbox_port` let a server bind an ephemeral host port per attempt while exposing a fixed port inside the sandbox.

**Root working directory**
: The directory in which Grain launches an attempt's agent CLI or CI steps, created empty and owned by the sandbox user `debian` before any grant is materialized. In the target architecture the checkout of the task's own repository is itself a `#git-repo` grant with `root: true`, whose `setup_root` command populates this directory (§5.5, §6.3); today's sandboxes clone it themselves into `work/` under the sandbox user's home. An attempt has exactly one root working directory.

**Setup command (`setup_root`, `setup`)**
: An executable inside the sandbox — named by a mint result as a clean absolute path, in practice a script the same grant placed with `mode: "0700"` — that Grain runs as `debian` with `cwd` set to the root working directory, after every `ports` forward and `placements` file of the pass is in place and before the agent CLI or any CI step starts (§5.5). `setup_root` is the command that initializes the root working directory, and at most one grant per attempt may return a non-empty one; `setup` runs after it, does not initialize the root, and any number of grants may return one (cloning an additional repository beside the root, activating a placed credential).

### 1.3 Trust Model

1.3.1. **The Capability Server is trusted by the operator** with the credentials bound to its `env` or `headers` (or held in an external server's own environment), and is **fully trusted inside every sandbox it serves**: its `placements` land in the sandbox filesystem, its `setup_root` and `setup` commands run there as the sandbox user, its `prompt_section` reaches the agent, and its `mcp_endpoint` tools and `ports` act with the run's authority. Attaching a third-party skill or playbook plugin/server (§6.4) confers exactly that trust; Grain limits the blast radius (reserved paths, size caps, attribution; §5) but does not sandbox the server.

1.3.2. **Loopback is not a trust boundary.** A host may run several external HTTP Capability Servers under different UIDs, other local services, and (with the local host sandbox backend) agent processes. For HTTP Capability Servers (`serverUrl`), the Grain daemon authenticates every request with that server's configured `headers` (for example, `Authorization: Bearer <token>` resolved from Grain's encrypted secrets store, §3.1.2–§3.1.3); no request on `127.0.0.1:<port>` is trusted for arriving on loopback, and `serverUrl` MUST NOT be reachable from any sandbox network namespace (§3.2.4). For stdio Capability Servers (`command`), the control plane is the private stdin/stdout pipe between the Grain daemon and the child process it spawned with an isolated environment (`env` resolved from Grain's encrypted secrets store, §3.1.3).

1.3.3. **The sandbox is untrusted by both.** Grain's proxy (§5.2.2) is the only path from a sandbox or agent CLI to a capability server's MCP tools, and it exposes only the `/runs/<attempt_id>/mcp` toolset granted to that run.

1.3.4. **`args` are untrusted input** (§4.3.2): they originate in task configuration, Grainfiles, `.grain/workflows/*.yml`, and `propose_task` calls.

### 1.4 Architecture Overview

```
┌────────────────────────────────────────────────────────────────────────────┐
│ Grain Daemon (Controller)                                                  │
│                                                                            │
│  1. Attach to each enabled server in grain/config mcpServers (§3.1)        │
│     (resolving $secret:<name> in env / headers from the secrets store)     │
│     ├─ Stdio {command, args, env}: in-tree `grain capability <name>` or    │
│     │  grain/config plugin `plugins/<name>/...` over stdin/stdout          │
│     └─ HTTP  {serverUrl, headers}: external server POST <serverUrl>        │
│        initialize, then tools/list -> mint_<method>, release_<method>      │
│                                                                            │
│  2. After the sandbox permit is acquired and before the VM boots, mint     │
│     every grant: tools/call("mint_<method>",                               │
│                  {attempt_id: "e1010c47-175-2-0", deadline, args})         │
│     │                                                                      │
│     ▼                                                                      │
│  ┌──────────────────────────────────────────────────────────────────────┐  │
│  │ Capability MCP Server (Stdio Plugin / In-Tree or External HTTP)      │  │
│  │  Receives secrets via resolved env / headers (or own environment)    │  │
│  │  Keys resources by attempt_id; expires them itself at deadline       │  │
│  │  Returns: status, placements[], mcp_endpoint, ports[], setup_root,   │  │
│  │           setup, prompt_section                                      │  │
│  └──────┬───────────────────────┬──────────────────────────┬────────────┘  │
│         │ placements            │ mcp_endpoint             │ ports         │
│         ▼                       ▼                          ▼               │
│  ┌──────────────────┐   ┌───────────────────────┐   ┌───────────────────┐  │
│  │ Sandbox VM /     │   │ HTTP path proxy or    │   │ Loopback TCP      │  │
│  │ Container files  │   │ StartCommandHTTP      │   │ forwarder; checks │  │
│  │ (default 0600,   │   │ -> /runs/<attempt_id>/│   │ the host listener │  │
│  │  reserved paths  │   │    mcp, wired into    │   │ is the server's   │  │
│  │  refused)        │   │ agy/claude/codex/     │   │ own service       │  │
│  │                  │   │ custom & CI mcp.json  │   │                   │  │
│  └──────────────────┘   └───────────────────────┘   └───────────────────┘  │
│         │ setup_root (one grant), then each setup, in grant order:         │
│         ▼ run as debian in the root working directory, before the agent    │
│           CLI or any CI step starts (§5.5); then mcp_endpoint wiring,      │
│           prompt_section blocks, and launch in that directory              │
│                                                                            │
│  3. When the attempt finishes (every grant whose method has release_):     │
│     tools/call("release_<method>", {"attempt_id": "e1010c47-175-2-0"})     │
└────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Capability Identifiers & Grant Syntax

### 2.1 Grammar of `<capability_id>[#<method>]`

2.1.1. In a Grainfile or task configuration, a capability reference is `<capability_id>[#<method>]` (for example, `https://github.com/ai-on-gke/grain/capabilities/gcp#gcp-key`, or bare `https://github.com/ai-on-gke/grain/capabilities/self-debug`) and MUST conform to:

```abnf
capability-ref = capability-id [ "#" method-name ]
capability-id  = "https://" host [ ":" port ] 1*( "/" segment-nz )
method-name    = ( LOWER / DIGIT ) 0*62( LOWER / DIGIT / "_" / "-" )
LOWER          = %x61-7A ; a-z
DIGIT          = %x30-39 ; 0-9
```

`host`, `port`, and `segment-nz` are RFC 3986's rules. This grammar is the single normative grammar for capability references; the Grainfile specification ([`docs/grainfiles-spec.md`](grainfiles-spec.md) §6.6) defers to it.

2.1.2. `<capability_id>` MUST be a valid `https://` URL with a non-empty host and at least one non-empty path segment, MUST NOT contain a query string, fragment, userinfo, or `.` / `..` segments, and MUST NOT end with `/`. Grain compares `capability_id`s byte-for-byte with no normalization: it is a lookup key into `mcpServers` (§3.1), not an address.

2.1.3. `<method>` MUST match `^[a-z0-9][a-z0-9_-]{0,62}$`. Because tool names carry the verb prefixes `mint_` and `release_` (§4.1.2), every conforming `<method>` maps unambiguously to `mint_<method>` and `release_<method>` (a method named `github-release` maps to `mint_github-release` and `release_github-release`).

2.1.4. **Single-Method Shorthand Resolution.** When `#<method>` is omitted:
1. If the attached server advertises `mint_<method>` where `<method>` equals the final path segment of `<capability_id>` (`…/self-debug` → `mint_self-debug`), the reference resolves to `<capability_id>#<that-segment>`.
2. Otherwise, if the server advertises exactly one `mint_<method>` tool, the reference resolves to that method.
3. Otherwise the reference MUST be rejected with a validation error listing the server's advertised `#<method>` names.

2.1.5. After resolution, the canonical form of every reference is the full `<capability_id>#<method>`.

### 2.2 Grant Syntax in `.task.grain.md`, `.process.grain.md`, Declarations, and CLI

2.2.1. In `.task.grain.md` and `.process.grain.md` front matter ([`docs/grainfile-v1.md`](grainfile-v1.md), [`docs/grainfiles-spec.md`](grainfiles-spec.md)), `.grain/templates/*.yml`, `.grain/workflows/*.yml`, deployment Settings, and `propose_task`, each entry in a `capabilities:` list MUST be either:

1. **Bare string:** `<capability_id>[#<method>]`. Valid if and only if the method's `inputSchema.properties.args` has no `required` property without a `default` (so `{}` validates after defaults).
2. **Grant object:** a mapping with `capability` (`string`, REQUIRED: the reference) and `with` (`object`, OPTIONAL, default `{}`: arguments validated against `inputSchema.properties.args`). Any other key on the object is an error.

```yaml
capabilities:
  - https://github.com/ai-on-gke/grain/capabilities/self-debug
  - capability: https://github.com/ai-on-gke/grain/capabilities/gcp#gcp-key
    with:
      account: infra-staging
  - capability: https://github.com/ai-on-gke/grain/capabilities/github#git-repo
    with:
      repo: bwsalmon/grain
      mode: write
      base: main
      root: true
  - capability: https://github.com/ai-on-gke/grain/capabilities/github#git-repo
    with:
      repo: bwsalmon/silo
      mode: read
      root: false
```

2.2.2. `with:` values are committed to repositories and copied into task records; they MUST NOT carry secrets. A capability that needs a secret selects it by name (`account: infra-staging`) and keeps the material on the server.

2.2.3. On the `grain` CLI, `-capability` accepts a bare reference or a reference followed by `=` and an inline JSON object (`-capability 'https://github.com/ai-on-gke/grain/capabilities/gcp#gcp-key={"account":"infra-staging"}'`).

### 2.3 Canonical Grant Identity, Multi-Instance Grants, and Sub-Task Privilege Containment

2.3.1. **Canonical grant.** After resolving `<capability_id>#<method>` and applying the `default`s of `inputSchema.properties.args` to `with:`, a grant is `(capability, args)`: the canonical reference string and the normalized argument object. Grain records the normalized `args` on the grant at admission; later schema changes on the server do not retroactively change a recorded grant (§2.3.4).

2.3.2. **Equality and deduplication.** Two grants are equal if and only if their `capability` strings are identical and their normalized `args` are deep-equal. Equal grants on one run MUST be deduplicated before minting; the first occurrence keeps its position.

2.3.3. **Multi-instance grants and `attempt_id` assignment.** A run MAY hold several grants for one `<capability_id>#<method>` with different normalized `args` (two `#git-repo` grants for two repositories — one with `root: true` for the root working directory and the other with `root: false`, §5.5.2; `#gcp-key` with `{account: "staging"}` and `{account: "prod"}`), and grants across several methods of one server. Deduplicated grants are indexed `0, 1, …` in declaration order, and the grant at index `i` is minted with `attempt_id` `<deployment>-<task>-<attempt>-<i>`.

2.3.4. **Sub-task privilege containment.** When an agent proposes a child task via `propose_task`, a child grant is covered by the parent if and only if some parent grant has the same `capability` and deep-equal normalized `args` — the parent's `args` as **recorded at the parent's admission**, the child's as normalized against the schema current when the child is filed. A child requesting any uncovered capability or argument MUST NOT inherit auto-approval or auto-merge and MUST be parked as unapproved for human approval. Two exceptions are declared by a server linked into the build, never by a remote one: a method marked person-granted (`protocol.RequirePersonGrant`, e.g. a workspace server's `#submit`) is covered by nothing, and a method with a grant policy (`protocol.SetGrantPolicy`) is judged by the grant's `args` -- `GrantByAgent` grants are covered without a parent grant (they are scoped to the task they are granted to, like a task's own branch: a `#workspace` grant with no `workspace:`), `GrantByPerson` grants by nothing (a `#workspace` grant naming a workspace). If a server's schema change alters the normalization of an existing grant (a changed `default`), the grant is no longer covered by anything recorded earlier and requires approval again.

---

## 3. Deployment Configuration & Trust Boundary

### 3.1 Capability Server Configuration: Antigravity `mcpServers` Shape & Secrets Store

3.1.1. **Antigravity `mcpServers` configuration shape in `grain/config`, Settings UI, and CLI.**
An operator configures Capability Servers in the deployment's state repository (`grain/config`) and through the Settings UI or CLI (`add`, `remove`, `list`, `enable`, `disable`) under `mcpServers`, keyed by `<capability_id>`. The entry schema follows Antigravity (`agy`)'s `mcp_config.json` specification ([`docs/agy-surface.md`](agy-surface.md) lines 145–174 and 1542–1663), supporting two transports:

1. **Stdio Transport — for in-tree capabilities and `grain/config` capability plugins:**
   - `command` (`string`, REQUIRED): The executable to run.
     - For **in-tree capabilities** compiled into the `grain` binary, `command` invokes `grain` (or its built-in capability subcommand, for example `"command": "grain", "args": ["capability", "gcp"]`).
     - For **in-config (`grain/config`) capability plugins** committed to the state repository, a relative `command` path (for example `"plugins/custom-plugin/server"`) is resolved from the root of the checked-out `grain/config` state repository (matching custom agent backends in [`docs/custom-agent-backends.md`](custom-agent-backends.md)). Any relative path containing `..` segments that escape the `grain/config` root MUST be rejected. Clean absolute paths are also accepted for host-installed executables.
   - `args` (`string[]`, OPTIONAL, default `[]`): Arguments passed to `command`.
   - `env` (`object` of `string -> string`, OPTIONAL, default `{}`): Environment variables injected into the server process (with sensitive values referenced via `$secret:<name>` and resolved from Grain's encrypted secrets store at runtime; §3.1.2).
   - `disabled` (`boolean`, OPTIONAL, default `false`): Whether the server is disabled (`enable` / `disable`). When `true`, Grain does not attach to the server or admit grants referencing its `capability_id`.
   - `run` (`string`, OPTIONAL): `"host"` starts the server on the bare machine through `grain hostd`, for a daemon that runs in the release container ([`docs/design/cli-run-location.md`](design/cli-run-location.md), "Behind grain hostd"). `command` then names a `capabilities:` entry on hostd's allowlist (`/etc/grain/hostd.yaml`; letters, digits, `-`, `_` and `.`), never a path: the allowlist says what that entry runs. The entry MUST be among hostd's offers (`hostd.Client.Offers`, kind `capability`), or the attach fails saying hostd is absent or does not offer it. `args` are refused by hostd unless the entry sets `extra_args: true`, and each `env` name must be one the entry's `env:` accepts. stdio is carried over hostd's socket. Where the host executor is not grain hostd (the bare machine, Kubernetes, a container with no hostd) the server fails to attach.

2. **HTTP Transport — for external capability servers:**
   - `serverUrl` (`string`, REQUIRED): The HTTP(S) URL of the external Capability Server's control-plane MCP endpoint (for example, `"http://127.0.0.1:9802/mcp"` or `"https://cap.example.com/mcp"`).
   - `headers` (`object` of `string -> string`, OPTIONAL, default `{}`): HTTP headers sent on every control-plane (`initialize`, `tools/list`, `tools/call`) and proxied data-plane (`/runs/<attempt_id>/mcp`, §5.2.2) request to the server — in particular `"Authorization": "Bearer $secret:<name>"`, with sensitive values resolved from Grain's encrypted secrets store at runtime (§3.1.2).
   - `disabled` (`boolean`, OPTIONAL, default `false`): Whether the server is disabled (`enable` / `disable`).

Either transport also takes:
   - `config` (`object`, OPTIONAL): The server's own configuration, for a server that advertises the `configure` tool (§4.5) — the project, region, default account, endpoints and credentials it would otherwise read from `env` or `headers`. It is validated against the server's configuration schema and sent to `configure` after every `initialize`. A secret field holds only a `$secret:<name>` reference (§4.5.2). An entry with a `config` whose server advertises no `configure` tool fails to attach rather than having its `config` ignored.

Each `mcpServers` entry MUST specify either `command` (stdio) or `serverUrl` (HTTP) and MUST NOT mix stdio-only (`command`, `args`, `env`) and HTTP-only (`serverUrl`, `headers`) fields.

```json
{
  "mcpServers": {
    "https://github.com/ai-on-gke/grain/capabilities/gcp": {
      "command": "grain",
      "args": ["capability", "gcp"],
      "env": {
        "GCP_MINTER_KEY": "$secret:gcp-minter-key"
      }
    },
    "https://github.com/my-org/capabilities/custom-plugin": {
      "command": "plugins/custom-plugin/server",
      "args": ["--mode", "prod"],
      "env": {
        "PLUGIN_API_KEY": "$secret:custom-plugin-key"
      }
    },
    "https://github.com/my-org/capabilities/external-cap": {
      "serverUrl": "http://127.0.0.1:9802/mcp",
      "headers": {
        "Authorization": "Bearer $secret:external-cap-token"
      }
    }
  }
}
```

3.1.2. **Secrets Store integration for `env` and `headers`.**
Because `grain/config` is exported as plain text in Git, secret values in `env` and `headers` (cloud IAM minter keys, plugin API keys, HTTP control-plane bearer tokens) MUST NOT be committed in cleartext in `grain/config`:
- Sensitive `env` and `headers` entries are stored in **Grain's encrypted secrets store** and referenced from `grain/config` via `$secret:<name>` (or `$secret:<secret>/<key>`), mirroring how `standing_sandbox_env` and `standing_sandbox_secret` separate plain-text configuration in `grain/config` from encrypted secret material in the secrets store.
- At runtime, when Grain spawns a stdio Capability Server or connects to an HTTP Capability Server, it resolves each `$secret:<name>` reference from the encrypted secrets store in memory and injects the resolved value into the child process's `env` or the outgoing HTTP request's `headers`.
- Grain MUST refuse to attach a server if any `$secret:<name>` referenced in its `env` or `headers` is missing or empty in the encrypted secrets store, reporting the missing secret name without revealing any secret material.
- Resolved secret values MUST NEVER be written back to `grain/config`, state exports, logs, task records, or agent prompts.
- The same holds for the secret fields of a server's `config` (§4.5.2): they are stored only in the encrypted secrets store, and `config` carries a `$secret:<name>` reference in their place.
- In the Settings UI and CLI, operators can add, edit, remove, enable, and disable capability servers and bind `env` and `headers` entries to secrets in Grain's encrypted secrets store.

3.1.3. **Transport authentication and environment isolation.**
- **Stdio servers (`command`):** Grain spawns the configured executable as a child process and communicates with it over its standard input and output pipes. The child does not inherit the Grain daemon's environment: it receives only a clean base environment (`HOME` set to a private directory, `PATH`, and locale/TLS variables `USER`, `LOGNAME`, `SHELL`, `LANG`, `LANGUAGE`, `LC_*`, `TZ`, `TMPDIR`, `TERM`, `XDG_RUNTIME_DIR`, `SSL_CERT_FILE`, `SSL_CERT_DIR`, matching [`docs/custom-agent-backends.md`](custom-agent-backends.md)) overlaid with the server's own resolved `env` map. Diagnostic output belongs on `stderr`. A session Grain starts only to probe the server — Settings' **Test**, run while the entry's own instance may already be attached — additionally carries `GRAIN_CAPABILITY_PROBE=1`; it never mints, so a server whose data plane listens on a fixed port SHOULD bind an ephemeral one for it rather than fail on the port the attached instance holds. When a probed server exits before it answers, Grain reports the end of its `stderr` with the error.
- **External HTTP servers (`serverUrl`):** Grain sends the server's resolved `headers` on every control-plane request (`initialize`, `tools/list`, `tools/call`) and every proxied data-plane request to `/runs/<attempt_id>/mcp` (§5.2.2). An external HTTP Capability Server reachable over loopback or a shared network MUST require an authentication header (for example, `Authorization: Bearer <token>` backed by at least 32 bytes of random material in Grain's encrypted secrets store) and MUST reject any request on `/mcp` or `/runs/…` that lacks a matching credential with HTTP `401 Unauthorized`.

### 3.2 Trust Boundary Invariants

3.2.1. **Unmapped `capability_id` refusal and no requests to `capability_id`.** Grain MUST NOT spawn any command or connect to any URL other than the `command` or `serverUrl` configured for that exact `capability_id` in `mcpServers`, and MUST NEVER make a network request to the `capability_id` URL itself. A task, process, template, or CI job referencing an unmapped or `disabled` `capability_id` MUST be refused before admission.

3.2.2. **Operator-controlled server execution only.** Grain spawns only operator-configured stdio `command` entries (in-tree `grain` capability subcommands, executables committed to `grain/config`, or host paths configured in `mcpServers`) and connects only to operator-configured `serverUrl` endpoints. Grants and `with:` arguments cannot specify commands, plugins, containers, or URLs to run.

3.2.3. **Root credential isolation per server.** Root credentials (cloud IAM minter keys, Workload Identity bindings, GitHub App private keys, personal access tokens, vendor API keys, registry publish tokens, signing keys) MUST be scoped to the individual Capability Server that uses them — either injected solely into that server's resolved `env` / `headers` from Grain's encrypted secrets store (§3.1.2) or held in an external HTTP server's own environment. Grain MUST NOT expose one capability server's secrets to another server or pass root credentials in control-plane JSON-RPC arguments.

3.2.4. **Sandbox unreachability of the control plane.** No sandbox (VM, container, or local host sandbox) may reach a Capability Server's control-plane stdio pipes or HTTP `serverUrl` directly. A sandbox backend that cannot guarantee this (a local host sandbox sharing the daemon's network namespace when a loopback HTTP Capability Server or `ports` listener is attached) MUST NOT run attempts holding capability grants.

3.2.5. **Leased secrets held by Grain.** Grain caches each grant's `placements`, `mcp_endpoint`, `ports`, `setup_root`, and `setup` for the attempt's lifetime so they can be re-materialized after `recreate_sandbox` (§5.1.3, §5.5.5). That cache MUST be held in memory (or encrypted at rest), MUST never be logged, MUST be excluded from state exports, and MUST be discarded when the attempt's grants are released.

---

## 4. Control-Plane MCP Protocol

Every Capability Server — whether connected over **stdio** (`command`, `args`, `env` for in-tree `grain` capabilities and `grain/config` plugins) or over **Streamable HTTP** (`serverUrl`, `headers` for external capability servers) — MUST implement standard MCP JSON-RPC 2.0 on its control plane (§3.1).

**What is standard MCP:** the transport (stdio or Streamable HTTP, JSON-RPC 2.0), discovery (`initialize` and `tools/list` with `name`, `description`, `inputSchema` — no custom metadata, no custom endpoints), and invocation (`tools/call` dispatched by `params.name`, returning a tool result whose JSON is in `structuredContent` or as text in `content[0].text`). Any off-the-shelf MCP SDK (`@modelcontextprotocol/sdk`, the Python `mcp` package's FastMCP, `mcp-go`, or Grain's `pkg/mcp`) serves it over either stdio or HTTP.

**What Grain adds on top:**
1. **`mint_<method>({attempt_id, deadline, args})`** — one tool per capability method, whose `inputSchema.properties.args` is the schema of its `with:` arguments (§4.1, §4.3).
2. **The mint result `{status, placements, mcp_endpoint, ports, setup_root, setup, prompt_section}`** — what Grain materializes for that `attempt_id` (§4.3.3, §5).
3. **`release_<method>({attempt_id})`** — the teardown tool for stateful methods (§4.4).

### 4.0 Grain as an MCP Client

4.0.1. Grain behaves as a standard MCP client across both configured transports (§3.1): it sends `initialize` followed by `notifications/initialized` before other requests, and follows `nextCursor` until `tools/list` is exhausted.
- **Stdio (`command`):** Grain spawns the configured `command` with `args` and resolved `env` (§3.1.1–§3.1.3) and exchanges newline-delimited JSON-RPC 2.0 messages over the child's stdin/stdout pipes. A `run: host` server is started by `grain hostd` instead, and the same messages travel over hostd's socket.
- An HTTP server running on the host is reachable from a daemon in the release container at the same `serverUrl` (for example `http://127.0.0.1:9802/mcp`): the container runs with host networking (`scripts/setup.sh`, `docker_run_args`), so it needs no `run:`.
- **Streamable HTTP (`serverUrl`):** Grain sends `POST` requests to `serverUrl` carrying the server's resolved `headers` (§3.1.1–§3.1.3), `Accept: application/json, text/event-stream`, and `MCP-Protocol-Version`, honours an `Mcp-Session-Id` the server assigns, and accepts both plain JSON and SSE-framed responses. HTTP servers SHOULD serve the control plane statelessly (FastMCP `stateless_http=True`, or the TypeScript SDK transport without session management); a stateful server also works.

4.0.2. **Timeouts.** Grain waits at most 10 seconds for `tools/list`, 60 seconds for `mint_<method>`, and 30 seconds for `release_<method>` (deployment-configurable). A call that exceeds its timeout is a transient failure (§4.2.4).

4.0.3. **Concurrency.** Grain MAY issue `mint_` and `release_` calls for different `attempt_id`s concurrently and MUST serialize calls carrying the same `attempt_id`.

4.0.4. **Refresh.** Grain re-issues `tools/list` on attach, on configuration reload, on `notifications/tools/list_changed` when the server sends it on a session, and at least hourly.

### 4.1 Discovery (`tools/list`)

4.1.1. Grain invokes `tools/list` (`{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}`) and reads the tools by name.

4.1.2. **Verb-prefixed tool registration.**
- Every tool whose `name` is `mint_<method>` advertises capability `<method>`.
- Every tool whose `name` is `release_<method>` advertises the teardown hook for `<method>`.
- A stateful method (anything leasing credentials, registering routes, or authorizing listeners) MUST register both tools; a stateless method returning only `placements`, setup commands (§5.5), and `prompt_section` (`mint_skill`, `mint_bootstrap-playbooks`) MAY omit `release_<method>`, and Grain treats its teardown as a no-op.
- A `mint_` tool whose suffix does not match `^[a-z0-9][a-z0-9_-]{0,62}$`, a duplicate `mint_<method>`, or a `release_<method>` without its `mint_<method>` is a discovery error: Grain MUST refuse to attach the server and report the offending tool name.

```json
{
  "tools": [
    {
      "name": "mint_gcp-key",
      "description": "Mint a short-lived GCP service-account key and place it in the sandbox.",
      "inputSchema": {
        "type": "object",
        "additionalProperties": false,
        "required": ["attempt_id", "deadline", "args"],
        "properties": {
          "attempt_id": { "type": "string" },
          "deadline": { "type": "string", "format": "date-time" },
          "args": {
            "type": "object",
            "additionalProperties": false,
            "properties": {
              "account": {
                "type": "string",
                "default": "default",
                "pattern": "^[a-z0-9][a-z0-9_-]{0,62}$",
                "description": "Named GCP key configuration on the capability server to mint from."
              }
            }
          }
        }
      }
    },
    {
      "name": "release_gcp-key",
      "description": "Release the GCP service-account key minted for an attempt_id.",
      "inputSchema": {
        "type": "object",
        "additionalProperties": false,
        "required": ["attempt_id"],
        "properties": { "attempt_id": { "type": "string" } }
      }
    }
  ]
}
```

4.1.3. **Normative `tools/list` tool fields:**

| Tool | `name` | `description` | `inputSchema` |
| :--- | :--- | :--- | :--- |
| **`mint_<method>`** | REQUIRED; `<method>` matches `^[a-z0-9][a-z0-9_-]{0,62}$`. | SHOULD be present (Grain shows it to operators). | REQUIRED object schema with `required: ["attempt_id", "deadline", "args"]`. `properties.args` (`"type": "object"`, SHOULD set `"additionalProperties": false`) declares every parameter the method depends on. |
| **`release_<method>`** | REQUIRED for stateful methods; omitted for stateless ones. | SHOULD be present. | REQUIRED object schema with `required: ["attempt_id"]`, `properties: {"attempt_id": {"type": "string"}}`. |

Other standard MCP tool fields (`title`, `outputSchema`, `annotations`) are permitted and ignored.

### 4.2 Argument Validation, Policy & Admission Minting

4.2.1. **`args` validation.** Grain MUST validate a grant's `with:` object against `inputSchema.properties.args` (treating an absent `properties.args` as `{"type": "object", "additionalProperties": false}`) at three points: static declaration linting (`grain declarations check`); task, process, template, schedule, and `propose_task` creation; and grant resolution immediately before minting. Validation applies top-level `default`s first, then enforces `type`, `required`, `enum`, `const`, `pattern`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `minLength`, `maxLength`, `items` (a single schema), `minItems`, `maxItems`, `uniqueItems`, `additionalProperties`, `allOf`, `anyOf`, `oneOf`, and `not`, resolving local `$ref`s into `$defs` (which SDKs emit for nested argument models) and applying a `$ref`'s sibling keywords alongside its target. Any failure MUST reject the grant before `tools/call`. An args schema that uses any other keyword, apart from annotations that assert nothing (`title`, `description`, `default`, `examples`, `format`, `deprecated`, `readOnly`, `writeOnly`, `$comment`, `$schema`, `$id`, `$anchor`), MUST fail discovery (§4.1.2) rather than be enforced as something looser than it says.

4.2.2. **Policy admission.** Which teams, repositories, run kinds (agent task, standing process, CI job), and trigger events may hold a given `<capability_id>#<method>` is deployment and repository policy configured in Grain, not advertised by the server. Before minting, Grain MUST verify that the grant is permitted for the caller and MUST refuse the run otherwise (an agent task is parked with the refusal; a CI job concludes `errored`). Policy MUST deny grants by default to any run whose trigger carries untrusted input — a `pull_request` event from a fork or from an author without write access to the repository — unless the repository explicitly opts that capability in for that event. Grain's policy (`grain_config.capability_policy`, `model.CapabilityPolicy`) is a list of rules, each naming a reference or a pattern of one with the repositories, run kinds and events it covers: a reference no allow rule names may be held by any trusted run, one that some allow rule names only by a run one of them admits, and a `deny` rule refuses the reference to every run it covers ahead of any allow rule. Settings edits it (`GET`/`PUT /api/capability-policy`) and previews which existing grants a change would newly refuse before it is saved (`POST /api/capability-policy/preview`). Grain also applies it earlier, so a person is not left to find out at dispatch: filing a task (`POST /api/tasks`) or attaching a grant to one refuses (400, naming the deny rule) a grant the policy denies that task's repository as an agent task, and drops a repository or deployment default it denies; and the method listing a grant picker reads (`GET /api/capability-methods?kind=task&repo=owner/name`) marks each method the policy denies that run with `policyRefusal`, which the picker shows disabled with that reason. Admission's check stays the backstop for a policy saved after a task was filed.

4.2.3. **Admission minting and rollback.** Because `{attempt_id, deadline, args}` does not depend on a sandbox existing, Grain mints every grant **after the sandbox permit is acquired and before any VM boots**, so a run that cannot be granted never boots and a queued run never holds upstream resources idle:
1. Deduplicated grants are minted in index order (§2.3.3) with `tools/call("mint_<method>", {attempt_id, deadline, args})`, where `deadline` is the latest instant the attempt may still be running, including every extension and grace period Grain could grant it (§4.3.5).
2. If any mint returns `status: "refused"` or `"resource_exhausted"`, or fails transiently (§4.2.4), Grain MUST call `release_<method>({attempt_id})` for every grant of the same pass whose method advertises it — including the failed grant, to clear any partial allocation — MUST release the sandbox permit, and MUST NOT boot a VM.
3. On `resource_exhausted`, the run stays queued and is retried no earlier than `retry_after` when present (capped at one hour from now; absent or past means Grain's default backoff). On `refused`, Grain refuses the run and posts `reason` (§4.3.4).
4. Once every mint of the pass has returned `ok`, Grain checks the responses against one another before booting: if more than one grant returned a non-empty `setup_root` (§5.5.2), Grain rolls the pass back as in item 2 and refuses the run, naming the conflicting grants.

4.2.4. **Transient failures.** An HTTP error status, a stdio EOF or unexpected process exit, a JSON-RPC error, an MCP result with `isError: true`, a result that does not parse as §4.3.3, or a timeout is a transient failure: Grain rolls back the pass as in §4.2.3 and requeues the run with exponential backoff. After three consecutive transient failures of the same grant, Grain refuses the run with the last error text as `reason`.

### 4.3 Minting (`tools/call` on `mint_<method>`)

4.3.1. **Request.** Grain sends `tools/call` with `params.name` = `"mint_<method>"` and `params.arguments` = exactly `{attempt_id, deadline, args}`:

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "mint_gcp-key",
    "arguments": {
      "attempt_id": "e1010c47-175-2-0",
      "deadline": "2026-10-19T23:00:00Z",
      "args": { "account": "infra-staging" }
    }
  }
}
```

- `attempt_id` (`string`, REQUIRED): the grant's identifier (§1.2). The server MUST key every resource it creates for the call under it and MUST expire them itself when `deadline` passes.
- `deadline` (`string`, RFC 3339 `date-time`, REQUIRED): the instant after which the attempt can no longer be running.
- `args` (`object`, REQUIRED, `{}` when the method takes no arguments): the normalized `with:` object. Every parameter the capability depends on MUST be passed here and declared in `inputSchema.properties.args`.

4.3.2. **Untrusted `args`.** Capability Servers MUST treat `args` as untrusted: declare strict allowlists in `inputSchema.properties.args` (`enum`, bounds, anchored `pattern`s such as `^[a-z0-9][a-z0-9_-]{0,62}$`, `additionalProperties: false`), re-validate inside the handler, and NEVER interpolate an unvalidated `args` string into a filesystem path, shell command, SQL query, or upstream URL.

4.3.3. **Response.** The tool result's JSON (in `structuredContent`, or as a JSON string in `content[0].text`) MUST conform to:

```json
{
  "status": "ok",
  "placements": [
    {
      "path": "/home/debian/.gcp-service-account-infra-staging.json",
      "mode": "0600",
      "owner": "debian",
      "content": "{ ... minted key JSON ... }"
    },
    {
      "path": "/home/debian/.gcp-key/e1010c47-175-2-0/setup.sh",
      "mode": "0700",
      "content": "#!/bin/sh\nset -e\ngcloud auth activate-service-account --key-file=/home/debian/.gcp-service-account-infra-staging.json\n"
    }
  ],
  "mcp_endpoint": "/runs/e1010c47-175-2-0/mcp",
  "ports": [
    { "host_port": 19811, "sandbox_port": 9811 }
  ],
  "setup": "/home/debian/.gcp-key/e1010c47-175-2-0/setup.sh",
  "prompt_section": "A GCP service-account key for `infra-staging` is at `/home/debian/.gcp-service-account-infra-staging.json`; `gcloud` is already signed in with it."
}
```

| Field | Type | Presence | Meaning |
| :--- | :--- | :--- | :--- |
| `status` | `"ok"` \| `"refused"` \| `"resource_exhausted"` | REQUIRED | §4.3.4. |
| `reason` | `string` (≤ 4 KiB) | REQUIRED when `refused` | Human-readable explanation; Grain posts it to the task or CI job as untrusted text. |
| `retry_after` | RFC 3339 `date-time` | OPTIONAL when `resource_exhausted` | Earliest retry instant (§4.2.3.3). |
| `placements` | array (≤ 64 items, ≤ 1 MiB each, ≤ 8 MiB total) | OPTIONAL | Files to write (§5.1). |
| `mcp_endpoint` | `string` | OPTIONAL | Exactly `/runs/<attempt_id>/mcp` when the capability exposes tools; omitted or `""` otherwise (§5.2). |
| `ports` | array of `{host_port, sandbox_port?}` (≤ 16) | OPTIONAL | Loopback forwards (§5.3). |
| `setup_root` | `string` (clean absolute path, ≤ 4 KiB) | OPTIONAL | The command that initializes the root working directory, run there as `debian` before the agent or CI steps start; omitted or `""` when the grant does not initialize it. At most one grant per attempt may return a non-empty value (§5.5). |
| `setup` | `string` (clean absolute path, ≤ 4 KiB) | OPTIONAL | A command run in the root working directory after `setup_root`, for setup that does not initialize the root — an additional repository cloned beside it, a placed credential activated; any number of grants may return one (§5.5). |
| `prompt_section` | `string` (≤ 8 KiB) | OPTIONAL | Markdown for the agent prompt (§5.4). |

Grain MUST reject a response exceeding these limits or carrying unknown top-level fields as a transient failure (§4.2.4).

4.3.4. **`status` semantics.**
- `"ok"`: minting succeeded; the server keys all created resources under `attempt_id` and will expire them itself at `deadline` if `release_<method>` never arrives.
- `"refused"`: the request cannot be satisfied by configuration or policy (an unknown `account` or `repo`, a `deadline` beyond the upstream credential's maximum lifetime). `reason` MUST be non-empty. Grain refuses the run without booting a sandbox and posts `reason`.
- `"resource_exhausted"`: a finite upstream resource is temporarily at capacity. Grain leaves the run queued (§4.2.3.3).

4.3.5. **Idempotency, renewal, and lifetime.**
- `mint_<method>` MUST be idempotent per `attempt_id`: a repeated call with the same `attempt_id` and deep-equal `args` MUST return the same materialization without allocating again (Grain repeats a call it timed out on), and when its `deadline` is later than the stored one MUST extend the stored deadline to it. A repeated call with different `args` MUST return `refused`.
- Grain uses that repeat call as **renewal**: for a standing process or an attempt whose budget is extended, it re-calls `mint_<method>` with the same `attempt_id` and `args` and the new `deadline` before the current one passes, and reads only `status` from the reply.
- A server that cannot honour `deadline` MUST return `refused` rather than mint a shorter-lived credential.
- Grain MUST NOT let an attempt run past its grants' current `deadline` without a successful renewal; servers SHOULD apply a grace period (RECOMMENDED five minutes) after `deadline` before expiring resources.

### 4.4 Teardown (`tools/call` on `release_<method>`)

4.4.1. When an attempt finishes (succeeded, failed, cancelled, timed out, or aborted because a setup command failed, §5.5.4), for each grant minted during the attempt whose method advertises `release_<method>`, Grain MUST call it with that grant's `attempt_id`:

```json
{ "name": "release_gcp-key", "arguments": { "attempt_id": "e1010c47-175-2-0" } }
```

and the server returns `{"status": "ok"}`, optionally with the grant's audit (§4.4.4).

4.4.2. **Idempotency and cleanup.** `release_<method>` MUST be idempotent: an unknown, already-released, or expired `attempt_id` returns `{"status": "ok"}`. On release — and on its own when `deadline` (plus grace) passes — the server MUST revoke every upstream credential minted for `attempt_id`, unregister `/runs/<attempt_id>/mcp` so later requests return HTTP `404`, and invalidate per-attempt proxy tokens or close per-attempt listeners. If `release_<method>` fails transiently, Grain SHOULD retry with backoff; the server's own expiry bounds the leak either way. Orphan sweeping and TTL enforcement are internal to the server and not part of this protocol.

4.4.3. **`recreate_sandbox`.** If a run calls `recreate_sandbox` mid-attempt, every grant's `attempt_id` stays active: Grain tears down the old sandbox, re-materializes the cached `placements`, `ports`, and `mcp_endpoint` into the replacement sandbox, re-executes the cached `setup_root` and `setup` commands in its fresh root working directory (§5.5.5) — all without calling `release_<method>` or minting again — and calls `release_<method>` once when the attempt ends.

4.4.4. **Audit.** A server that keeps a record of what a grant did for its attempt (host-script's one record per call, [capabilities.md](capabilities.md)) MAY return it in the release result, and Grain stores it on the attempt's run record, under the grant (`<capability_id>#<method>`) and `attempt_id` it was returned for, and shows it in the task's attempt view:

```json
{ "status": "ok",
  "audit": [ { "time": "2026-10-23T18:00:00Z", "attempt_id": "e1010c47-175-2-0", "script": "deploy", "argv": ["--dry-run"], "exit_code": 0, "duration_ms": 812 } ],
  "audit_omitted": 0 }
```

- `audit` is an array of records, oldest first. Each record MUST be a JSON object carrying `time` (RFC 3339) and MAY carry anything else; a record that carries `attempt_id` MUST carry the one being released, so that records stay attempt-scoped. Records are untrusted text to Grain and to whoever reads them.
- At most 4096 records, each at most 64 KiB and together at most 4 MiB of JSON. A server that has more MUST leave records out rather than exceed a limit, and SHOULD count them in `audit_omitted`, a non-negative integer.
- An audit that breaks any of these rules is refused whole: Grain stores none of it and notes the refusal on the run. The release itself still succeeded, and Grain does not repeat it.
- Because Grain retries a release that timed out (§4.4.2), a server returning an audit SHOULD answer a repeated release of the same `attempt_id`, within the grace period, with the same records rather than none. Records are returned only at release: a grant expired by its deadline, or a run whose release never succeeded, leaves them in the server's own log alone.
- Grain does not redact records. Anything a server writes into one -- argv above all -- is shown to whoever can read the task, so a server SHOULD say what its records carry, and callers must not pass secrets where they are recorded.
- A CI job has no run record; Grain writes a CI grant's audit to its own log instead.


### 4.5 Server Configuration (`configure`)

4.5.1. **The `configure` tool.** A server MAY advertise a standard MCP tool named `configure` whose `inputSchema` is an object requiring `config`, with `inputSchema.properties.config` being the server's **configuration schema**: an object schema in exactly the keyword subset Grain enforces for `args` (§4.2.1), with root `$defs` carried across as for `args`. A configuration schema using any other keyword, a `configure` whose `inputSchema` does not require `config`, or a `config` that is not an object schema MUST fail discovery, naming the keyword or field, and the server MUST NOT be attached. `configure` is not a capability method: it is never granted, and `mint_`/`release_` discovery (§4.1.2) ignores it. Settings renders the configuration form from this schema with the same component that renders grant arguments.

4.5.2. **Secret fields.** A top-level `config` property marked `writeOnly: true` or `format: "password"` is a **secret field**. A secret field MUST be a string; a secret marker anywhere below the top level fails discovery. Grain stores a secret field's value only in the encrypted secrets store and records `"$secret:<name>"` in `config` in its place; a stored `config` holding a clear value in a secret field, or a `$secret:` reference in any other field, MUST be refused before anything is sent. Secret values are never exported to `grain/config`, never shown back (Settings shows only whether the referenced secret is set or missing), and never logged: Grain scrubs every resolved value out of any error it reports, as for `env` and `headers` (§3.1.2).

4.5.3. **Validation.** Grain validates `config` against the configuration schema with the same validator as `args`, applying top-level `default`s: when it is saved (with each secret field checked only for being a reference) and, in full with secrets resolved, immediately before every `configure`. A failure refuses the save, or marks the server unhealthy (§4.5.5), naming the field — and, for a secret field, never quoting its value.

4.5.4. **When Grain calls it.** Grain calls `tools/call("configure", {"config": <resolved config>})` after every `initialize` and `tools/list`, before any `mint_<method>`, and again whenever the entry's configuration changes (Grain re-attaches a changed entry, so the call follows a fresh `initialize`). An entry with no `config` is sent `{"config": {}}`. Secret fields are resolved from the store at that point, in memory only. After `configure` succeeds Grain re-issues `tools/list`, since a server's methods and their `args` schemas may depend on its configuration (for example an `enum` of configured accounts). The server MUST apply `configure` idempotently — the same configuration twice is a no-op — and MUST keep advertising `configure` once configured. It returns `{"status": "ok"}` (any non-error result is accepted), or an MCP result with `isError: true` whose text says what is wrong.

4.5.5. **Failure.** If `config` fails validation, a secret it references is missing, or `configure` fails or times out (the `tools/list` timeout, §4.0.2), Grain marks the server unhealthy with that error, closes the session, refuses every mint against it, and retries as for any failed attach. The server's status keeps its configuration schema so the form that fixes it can still be drawn. A server that advertises no `configure` tool keeps the `env`/`headers`-only configuration of §3.1.

4.5.6. **What configuration may say.** Configuration values are operator-controlled. They MAY name accounts, projects, regions and endpoints, and they bound what the server's methods can mint; per §3.2.2 a grant's `with:` can never override them. `with:` arguments still MUST NOT carry secrets: credentials belong in secret configuration fields (or `env`/`headers`), never in a grant.

4.5.7. **Grain's identity.** Two top-level configuration fields are reserved for Grain to fill: `grainDeploymentId`, the deployment id Grain puts in its branch names (`grain/<deployment>/task-<n>`; the configured `deployment_id`, else the default derived from the installation id), and `grainInstanceId`, the installation's random id, which stays the same when the deployment id is renamed. A server that needs to tell its own resources from another deployment's declares either field as a string in its configuration schema. Grain then sets it on every `configure`, overriding any stored value, and omits it from the form Settings draws, so operators never type it. A server that declares neither is sent neither. When the deployment id changes, Grain attaches the server again, so its next `configure` carries the new value. The gcp server names and marks its service accounts with these fields ([`docs/gcp-key-permission-sets.md`](gcp-key-permission-sets.md#account-names-and-ownership)).

4.5.8. **Credential sources.** A capability server describes each credential it acts with by its **source**, and SHOULD use these two words for them in its configuration schema, its Test report and its documentation:
- **configured** — a credential the operator stored in Grain's secret store and handed to the server through a secret field (§4.5.2) or a `$secret:` binding in `env`/`headers` (§3.1.2): a service-account key, a workload-identity config, a token, a webhook URL.
- **ambient** — no credential is stored: the server acts as whatever identity its own process already has where it runs. In the container that is the container's Application Default Credentials or metadata identity; for a `run: host` server started by `grain hostd` it is the identity of the account hostd runs as — on a Cloudtop, the user's own corp identity.
A server offering both names the choice per credential (`credential_source: configured | ambient`), refuses a configured credential that is missing and an ambient one that carries a stored value, and reports, when it is configured, which identity each ambient credential resolved to. Neither word names a network or a company: the same server and the same source behave alike wherever it runs.

```json
{
  "name": "configure",
  "inputSchema": {
    "type": "object",
    "required": ["config"],
    "properties": {
      "config": {
        "type": "object",
        "additionalProperties": false,
        "required": ["project"],
        "properties": {
          "project": { "type": "string", "minLength": 1 },
          "region": { "type": "string", "enum": ["eu", "us"], "default": "eu" },
          "minterKey": { "type": "string", "writeOnly": true }
        }
      }
    }
  }
}
```

stored as `"config": {"project": "p1", "minterKey": "$secret:capability-server-1a2b3c4d/minterKey"}` and sent as `{"config": {"project": "p1", "region": "eu", "minterKey": "<resolved>"}}`.

---

## 5. Materialized Primitives

When every `mint_<method>` of a pass has returned `status: "ok"`, Grain materializes the responses in this order, completing each step for every grant (in index order, §2.3.3) before starting the next, so that a later step can rely on everything an earlier one produced:
1. Record each grant (`<capability_id>#<method>`, `attempt_id`) and cache its response for `recreate_sandbox` (§3.2.5, §4.4.3).
2. Validate `ports` and verify host-side ownership (§5.3); establish the forwards (`127.0.0.1:<sandbox_port>`), so a setup command can reach a Git or model proxy.
3. Validate and atomically write `placements` (§5.1), so every credential and every placed setup script is on disk before any command runs.
4. Validate `setup_root` and `setup` (§5.5.1) and apply the single-owner rule across the pass (§5.5.2); then execute the one `setup_root` any grant returned, followed by each grant's `setup` in index order, inside the root working directory as `debian`, each under a bounded timeout (§5.5.3).
5. Validate `mcp_endpoint`, perform the data-plane `initialize` and `tools/list` handshakes to learn `serverInfo.name` and the attempt's tool roster, and wire the proxy into the active agent CLI's native per-run configuration (`agy`, `claude`, `codex`, or custom) or the CI container's `/home/debian/.grain/mcp.json` (§5.2).
6. Append each `prompt_section` as an attributed block (§5.4).
7. Launch the agent CLI or the CI job's steps with the root working directory as their working directory.

Any validation failure in steps 2–5 aborts run setup: Grain releases every grant of the pass (§4.2.3.2) and refuses the run with the validation error as `reason`. A setup command that exits non-zero or times out in step 4 aborts the attempt and releases every minted grant before the agent or any CI step starts (§5.5.4).

### 5.1 `placements` (Sandbox Files)

5.1.1. **Schema.**
- `path` (`string`, REQUIRED): absolute target path inside the sandbox.
- `mode` (`string`, OPTIONAL, default `"0600"`): four-digit octal permissions. The setuid, setgid, and sticky bits MUST be `0` (`"0600"`, `"0644"`, `"0755"`; `"4755"` is rejected).
- `owner` (`string`, OPTIONAL, default `"debian"`): owning user inside the sandbox.
- `content` (`string`, REQUIRED): file contents, UTF-8 text by default.
- `encoding` (`"utf-8"` \| `"base64"`, OPTIONAL, default `"utf-8"`): `"base64"` for binary material (PKCS#12 bundles, keytabs).

5.1.2. **Path validation, reserved paths, and collisions.**
- `path` MUST be a clean absolute path: starting with `/`, with no empty, `.`, or `..` segments. Grain MUST reject anything else before touching the sandbox.
- Grain MUST reject placements under its own and the agent CLI's reserved paths: `/home/debian/.grain/**`, the agent CLI configuration directories (`/home/debian/.claude/**`, `/home/debian/.codex/**`, `/home/debian/.gemini/**`) and Claude Code's `/home/debian/.claude.json`, the sandbox user's shell start-up files (`/home/debian/.bashrc`, `.bash_profile`, `.bash_login`, `.bash_logout`, `.profile`), Grain's own git and capability credentials (`/home/debian/.git-credentials`, `/home/debian/.gitconfig`, `/home/debian/.github-sandbox-token`), `/etc/sudoers`, `/etc/sudoers.d/**`, `/etc/passwd`, `/etc/shadow`, `/etc/group`, `/etc/cron*`, `/etc/profile.d/**`, `/etc/ld.so.preload`, and anything under `/proc`, `/sys`, `/dev`, `/boot`. The one exception under `/home/debian/.grain` is a sandbox command placed directly in `/home/debian/.grain/bin/` with a name matching `^[a-z0-9][a-z0-9_-]{0,62}$` (the host-script capability's stand-in `grain`); Grain writes nothing of its own there and puts that directory first on the `PATH` of `run_command` and of every CI step, and anything deeper, or any other name, stays reserved. Deployment policy MAY further restrict a capability to a path allowlist.
- If two grants in one run place the same `path` with different decoded `content`, `mode`, or `owner`, Grain MUST refuse the run with a placement collision error; identical placements are written once.
- Limits: at most 64 placements per grant, 1 MiB per decoded `content`, 8 MiB per grant (§4.3.3).

5.1.3. **Atomic write.** Grain MUST create parent directories as needed, write each file atomically with its `mode` and `owner`, and finish every placement of the pass before any setup command runs (§5.5.3) and before the agent CLI or any CI step starts. After `recreate_sandbox` the cached placements are written again into the new sandbox (§4.4.3).

### 5.2 `mcp_endpoint` (Attempt-Scoped MCP Server)

5.2.1. **Schema.** `mcp_endpoint` (`string`, OPTIONAL) is either absent / `""` (the capability exposes no tools) or **exactly** `/runs/<attempt_id>/mcp` for the `attempt_id` of the call. Grain MUST reject any other value — a different attempt's path, an absolute URL, a query string, a fragment, or any path that normalizes (after percent-decoding) to something else — as a validation failure. The server mounts a distinct per-attempt MCP toolset for that `attempt_id`; one endpoint serves any number of tools via standard `tools/list`, so a grant never needs more than one.

5.2.2. **Data-plane proxy Across HTTP and Stdio.** Grain MUST reach `/runs/<attempt_id>/mcp` only through its own proxy or bridge, never exposing raw capability server credentials or control-plane endpoints inside the sandbox:
- **External HTTP capability servers (`serverUrl`):** Grain's HTTP path proxy:
  - listens on loopback (on the controller/executor for the agent CLI, or forwarded into the sandbox/CI container) at a Grain-chosen port and path, and upstreams to `/runs/<attempt_id>/mcp` on the capability server's origin (`serverUrl`);
  - **replaces** the client's request path and query with the granted path `/runs/<attempt_id>/mcp` (never appends to it), so no request from the sandbox or agent CLI can address `/mcp` or another attempt's path;
  - forwards only the HTTP methods the Streamable HTTP transport uses (`POST`, `GET`, `DELETE`);
  - strips hop-by-hop headers and any client-supplied `Authorization`, injects the capability server's configured `headers` (with `$secret:<name>` values resolved from Grain's encrypted secrets store, §3.1.2–§3.1.3), and passes through `Accept`, `Content-Type`, `Mcp-Session-Id`, `MCP-Protocol-Version`, and `Last-Event-ID`;
  - never exposes raw TCP access to the capability server inside the sandbox.
  The HTTP server MUST require its configured authentication headers on `/runs/…` exactly as on `/mcp`, and MUST keep each attempt's MCP server a separate session namespace: an `Mcp-Session-Id` issued on one attempt's path MUST NOT be accepted on another's.
- **Stdio capability servers (`command` — in-tree `grain` capabilities and `grain/config` plugins):** Grain bridges the attempt-scoped capability toolset over a per-run stdio subcommand or adapts it to an authenticated loopback HTTP endpoint via `mcp.StartCommandHTTP` (depending on the active consumer's transport), scoping tool discovery and execution strictly to `attempt_id` and withholding the control-plane `mint_<method>` and `release_<method>` tools.

5.2.3. **Server name, tool discovery, and collisions.** Before the agent or CI step starts, Grain performs the data-plane `initialize` handshake and `tools/list` on each granted endpoint to read `serverInfo.name` and the attempt's tool definitions. `serverInfo.name` MUST match `^[a-z0-9][a-z0-9_-]{0,62}$` and MUST NOT be `grain-sandbox`; an endpoint whose name violates this is a validation failure. When several grants in one run report the same name, the grant with the lowest index (§2.3.3) keeps it and the others are registered as `<name>-2`, `<name>-3`, … in index order. The final `<name>` is passed back to the agent in the `prompt_section` attribution block (§5.4), so a server's own prompt text need not predict it.

5.2.4. **Per-Run Agent CLI & CI Container Wiring.**
After `initialize` and `tools/list` succeed on each granted `mcp_endpoint`, Grain translates each capability MCP server (under its final `<name>` from §5.2.3, with discovered tools `<tool>`) into the active agent CLI's native per-run configuration alongside `grain-sandbox`:
1. **Antigravity (`agy` — `pkg/agent/antigravity`):**
   - Registered in the run's private `$HOME/.gemini/config/mcp_config.json` under `mcpServers.<name>`, alongside `grain-sandbox`, with `"timeoutSeconds"` and `"tools": {"<tool>": {"eager": true}}` populated from the tools discovered via `tools/list` (so `agy` registers `mcp_<name>_<tool>` eagerly in the model's tool roster instead of hiding them as lazy manifest files behind `call_mcp_tool`),
   - `"mcp(<name>/<tool>)"` allow rules added to `$HOME/.gemini/antigravity-cli/settings.json` under `permissions.allow`, and
   - Admitted in `HookDecision` (the `PreToolUse` hook configured in `$HOME/.gemini/config/hooks.json`).
2. **Claude (`claude` — `pkg/agent/claude`):**
   - Registered in the per-run `--mcp-config` JSON file under `mcpServers.<name>` alongside `grain-sandbox`, with `mcp__<name>__<tool>` added to `--allowedTools` for every tool discovered via `tools/list`.
3. **Codex (`codex` — `pkg/agent/codex`):**
   - Registered in the per-run `$CODEX_HOME/config.toml` under `[mcp_servers.<name>]` (`url`, `http_headers`, `required = true`, `tool_timeout_sec`, `enabled_tools`, and `[mcp_servers.<name>.tools.<tool>]` with `approval_mode = "approve"`).
4. **Custom Agent Backends & CI Containers:**
   - **Custom agent backends ([`docs/custom-agent-backends.md`](custom-agent-backends.md)):** each endpoint is passed in `mcp_servers` of the custom backend JSON launch request alongside `mcp_server` (`grain-sandbox`).
   - **CI containers (`.grain/workflows` runs):** Grain writes `/home/debian/.grain/mcp.json` in the `mcpServers` form, each entry naming the sandbox-reachable proxy URL, so jobs and test scripts can call the capability's tools (including under `network_access: "none"`):

```json
{
  "mcpServers": {
    "self-debug": { "type": "http", "url": "http://127.0.0.1:38017/runs/e1010c47-175-2-0/mcp" }
  }
}
```

When the agent CLI runs on a remote executor, the executor hosts the local proxy listener and Grain carries the requests to the controller over its authenticated executor stream; the rules of §5.2.2 apply unchanged.

5.2.5. **Release.** When the attempt finishes and `release_<method>` is called (or `deadline` plus grace passes), the server MUST unregister `/runs/<attempt_id>/mcp` so no late or leaked process can invoke the tools; after `recreate_sandbox`, Grain re-points the proxy at the same still-active path (§4.4.3).

5.2.6. **Sandbox-First CLI Delivery (`grain cap`), `modelOnly` Tool Classification, and Testing ([`docs/design/sandbox-first-agent-surface.md`](design/sandbox-first-agent-surface.md)).**
Under the sandbox-first agent surface, capability tools on `/runs/<attempt_id>/mcp` are partitioned during the §5.2.3 `tools/list` probe into **sandbox-callable tools** (reached via `grain cap <name> <tool>` inside the sandbox over `http://127.0.0.1:9728/cap/<name>/mcp`) and **model-only tools** (wired exclusively to the host-side agent CLI's MCP configuration in §5.2.4 and refused on the sandbox proxy):
- **Classification and default (`modelOnly: true` for external servers):**
  1. A tool entry in `tools/list` on `/runs/<attempt_id>/mcp` MAY set `"annotations": {"modelOnly": <bool>}` (or top-level `"x-grain-model-only": <bool>`).
  2. A capability method's `mint_<method>` entry in control-plane `tools/list` (§4.1) MAY declare `"annotations": {"mcpDelivery": "sandbox" | "model_only"}` (or `"x-grain-mcp-delivery"`) as the default for all tools served on its `/runs/<attempt_id>/mcp` endpoint.
  3. **Precedence and fail-closed default:** An explicit per-tool `modelOnly` boolean wins; otherwise the method's `mcpDelivery` applies; otherwise **`modelOnly` defaults to `true` for external capability servers**. Existing external capability servers therefore remain strictly model-only on the host MCP channel until the capability author explicitly opts a tool (`"modelOnly": false`) or method (`"mcpDelivery": "sandbox"`) into sandbox CLI delivery.
- **Sandbox proxy & CI unification:** For tools with `modelOnly: false`, Grain exposes `POST http://127.0.0.1:9728/cap/<name>/mcp` inside the sandbox (authenticated by the per-attempt sandbox token and audited with the acting task and attempt) and writes `/home/debian/.grain/mcp.json` in both agent sandboxes and `.grain/workflows` CI sandboxes. Any `tools/call` on `/cap/<name>/mcp` naming a `modelOnly: true` tool MUST be rejected by the proxy with HTTP `403` before reaching the capability server (and CI jobs, which have no model channel, can only invoke `modelOnly: false` tools). `prompt_section` (§5.4) is the primary way a sandbox-delivered capability tells the agent which `grain cap <name> <tool>` commands it unlocked.
- **Built-in tool-serving capabilities and dangerous scopes:** Grain's own tool-serving capabilities (`self-debug`, non-dangerous `api:<scope>` grants, `grain-log`, `deployment-health`, `bootstrap-playbooks`, `memory`, `release-notes`, `read-issues`, `quarantine-test`, `db-read`) are delivered as scopes on the per-attempt sandbox CLI credential (`grain ls/read/grep`, `grain api`, `grain log`, `grain health`, `grain memory`, …) and revoked when the attempt ends. Any grant carrying a **dangerous** scope (`Dangerous: true` in `pkg/model/agent_api.go`: `tasks:admin`, `merge:force`, `settings:write`, `connections:write`, `secrets:write`, `capabilities:admin`, `deployment:upgrade/restart/reboot`, or a `RequiresPerson` scope set) or host/queue control (`dispatch-control`, `pipeline-control`, `self-upgrade`, `self-repair`, `db-write`, and the `teams:broadcast` scope that replaced `team-broadcast`) stays strictly model-only and is never placed on the sandbox credential.
- **Testing:** `POST /api/capability-servers/test` reports each discovered method's resolved delivery mode (`sandbox` vs. `model_only`), the default for the tools its endpoint serves; a per-attempt tool's own `modelOnly` is only seen once a grant is minted, and `grain cap list` shows its resolution. Inside a sandbox, capability authors verify discovery and execution with `grain cap list`, `grain cap <name> <tool> --help`, and `grain cap <name> <tool> --json '{...}'`.

### 5.3 `ports` (Sandbox Port Mappings)

5.3.1. **Schema.** Each item has exactly:
- `host_port` (`integer`, REQUIRED, `1024..65535`): a dedicated data-plane TCP listener on `127.0.0.1` of the capability server host, distinct from the server's `/mcp` control-plane port.
- `sandbox_port` (`integer`, OPTIONAL, default `host_port`, `1024..65535`): the loopback port inside the sandbox. A `sandbox_port` that collides with a port Grain's own guest agent uses is a validation failure; two grants in one run mapping the same `sandbox_port` to different `host_port`s are a collision and refuse the run (identical mappings are established once).

5.3.2. **Host-side ownership verification.** Before bridging, and for every forwarded connection, Grain MUST establish that the host-side endpoint belongs to the Capability Server's own service: the process owning the socket on `127.0.0.1:<host_port>` MUST belong to the same service as the Capability Server — for an external HTTP server, the same cgroup (the same `systemd` unit or container) or a descendant of the process serving `/mcp`; for a stdio capability server, the spawned child process or one of its descendants/cgroup (or for an in-tree `grain` capability, the daemon's own managed proxy listener for that capability). The check MUST be made on the **established** connection (after `connect()`, before any byte is forwarded), not only on the listener beforehand, so a listener that closes between inspection and connection cannot be replaced by another process on a reused port. A connection that fails the check MUST be closed; a grant whose `host_port` has no qualifying listener at setup MUST be refused. This is what stops a capability server from bridging a sandbox to Grain's own internal ports or to another unrelated local service.

> **Implementation note (non-normative).** On Linux the socket owner is found by matching the connection's inode in `/proc/net/tcp` (or `inet_diag`) against `/proc/<pid>/fd/*`, service membership by comparing `/proc/<pid>/cgroup`, and ancestry by walking `PPid` in `/proc/<pid>/status`. Inspecting another UID's `/proc/<pid>/fd` requires `CAP_SYS_PTRACE` or `CAP_DAC_READ_SEARCH` on the daemon, a real privilege cost that the cgroup comparison needs only for the `fd` lookup. Containerized capability servers that return `ports` must share the host network namespace (`--network=host`) and run every data-plane listener inside the same container; a Kubernetes sidecar qualifies only when it shares the daemon's network namespace and a process namespace Grain can inspect. `ports` are therefore available only for same-host Capability Servers.

5.3.3. **Forwarding and `network_access: "none"`.** For every validated mapping, Grain MUST forward TCP connections from `127.0.0.1:<sandbox_port>` in the guest to `127.0.0.1:<host_port>` on the controller host across every sandbox backend. Because mapped `ports` are explicitly authorized capability channels, they MUST remain reachable under `network_access: "none"` while all other egress stays blocked.

> **Implementation note (non-normative).** The guest end of a forward is grain's own forwarder (`portforward.GuestForwarder`), a `python3` script run as `debian` from `/home/debian/.grain/capability-ports`. Every sandbox image a backend runs therefore has to carry a sandbox user `debian` who owns `/home/debian` and a `python3` with `os`, `socket`, `sys` and `threading`: the granule guest does, and so does the stock container and Kubernetes sandbox image (`Dockerfile.k8s-sandbox`, whose `/home/agent` is a link to `/home/debian` for deployments that name it as their workspace). A custom image without them fails every grant with a port or a placement at dispatch.

5.3.4. **Isolation and teardown.** A mapping is private to the one sandbox it was materialized for. Because several runs may share one data-plane listener (a `git-proxy` on `9728`, a `model-proxy` on `9729`), the server MUST authenticate every request on `host_port` with a per-attempt token keyed under `attempt_id` and delivered through `placements` (`/home/debian/.git-repo/<attempt_id>/credentials`, `/home/debian/.<vendor>-proxy-token`) — in a per-grant file, since two grants placing different tokens at one path would collide (§5.1.2). When the attempt finishes, Grain MUST close all forwarders and call `release_<method>` for each stateful grant; after `recreate_sandbox` the cached mappings are re-established into the new sandbox (§4.4.3).

### 5.4 `prompt_section` (Agent Prompt)

5.4.1. `prompt_section` (`string`, OPTIONAL, ≤ 8 KiB) is Markdown the agent should read: where a credential was placed, what an MCP server offers, how to use a forwarded port. It MUST NOT contain secrets (prompts are persisted, displayed, and exported) and has no more authority than the task prompt.

5.4.2. Grain MUST append each non-empty `prompt_section` as a delimited block that names its origin — the canonical `<capability_id>#<method>`, the `attempt_id`, and the final MCP server name when the grant has an `mcp_endpoint` (§5.2.3) — and states that the block was supplied by a capability server rather than by the task author, so the agent can tell capability instructions from task instructions (the same structural separation [`docs/grainfiles-spec.md`](grainfiles-spec.md) §11.5 requires between prompts and references). `reason` strings (§4.3.4) are posted to task conversations under the same attribution.

### 5.5 `setup_root` and `setup` (Setup Commands)

5.5.1. **Schema.** A grant MAY name up to two commands that Grain runs inside the sandbox before the agent CLI or any CI step starts:
- `setup_root` (`string`, OPTIONAL, default `""`): the command that initializes the attempt's **root working directory** (§1.2) — the directory, created empty and owned by `debian` before materialization, in which Grain launches the agent CLI or the CI job's steps. Omitted or `""` when the grant does not initialize it.
- `setup` (`string`, OPTIONAL, default `""`): a command run in the root working directory after `setup_root`, for setup that does not initialize the root: cloning an additional repository beside it, activating a placed credential, installing a tool the grant provides. Any number of grants in an attempt may return one.

Each non-empty value MUST be a clean absolute path (the rule of §5.1.2; ≤ 4 KiB) naming an executable regular file inside the sandbox — in practice a script the same grant placed with `mode: "0700"`. That is why a command is a single path rather than a command line: everything it needs (repository, branch, credential file, target directory) is baked into the placed script or read from placed files, and no `args` string ever reaches a shell through Grain (§4.3.2). Grain executes the path directly, with no shell and no arguments, and MUST reject any other value as a validation failure (§5).

5.5.2. **Single owner of the root working directory.** An attempt has one root working directory, so **at most one** grant in an attempt may return a non-empty `setup_root`. Because every grant of a pass is minted before any VM boots (§4.2.3), Grain checks this as soon as the pass has minted: if two or more grants return a non-empty `setup_root`, Grain MUST roll the pass back (§4.2.3.2) and refuse the run, naming the conflicting grants — each canonical `<capability_id>#<method>` with its normalized `args` — in the refusal. A method that can return `setup_root` MUST therefore also offer a mode in its `args` in which it does not: `#git-repo` takes `root: true` to check the primary working tree out into the root working directory and `root: false` for additional repositories (§6.3), so a run can hold several grants of that method (§2.3.3) without contending for the root. `setup` is not subject to the rule, and a `setup` command MUST NOT initialize the root working directory in `setup_root`'s place: it works beside the root (an additional repository under `/home/debian/repos/<owner>/<repo>`) or reads what `setup_root` left there.

5.5.3. **Execution.** Once every `ports` forward of the pass is up and every grant's `placements` are on disk (§5, steps 2–3), Grain runs the one `setup_root` (if any grant returned one) and then each grant's `setup` in grant index order (§2.3.3), one at a time, never concurrently. Each command is executed:
- as the sandbox user `debian`, with `cwd` set to the root working directory, `HOME=/home/debian`, the sandbox's default `PATH`, stdin closed, and no Grain or capability token in its environment;
- under a bounded timeout — Grain waits at most 10 minutes per command (deployment-configurable) and never past the grants' `deadline` (§4.3.5);
- before any `mcp_endpoint` is wired, any `prompt_section` is appended, or the agent CLI or a CI step starts, so nothing in the run observes a half-initialized root working directory.

Grain captures each command's stdout and stderr (keeping at least the last 64 KiB) as attempt diagnostics and MUST NOT feed them into the agent prompt. The server MUST keep secrets out of that output, as it keeps them out of `prompt_section` (§5.4.1): a credential belongs in a `placements` file the script reads (§5.3.4). A setup command acts inside the sandbox with the run's authority, exactly as §1.3.1 says of every primitive.

5.5.4. **Failure.** If a command exits non-zero, times out, or cannot be executed (the path is missing or not executable), Grain MUST NOT start the agent CLI or any CI step: it MUST abort the attempt, tear down the sandbox, release every grant minted for the attempt (§4.4.1), and report the failing grant, its exit status, and the tail of its output as untrusted text — to the task's conversation for an agent task, as the `errored` conclusion for a CI job. A setup failure is an attempt failure rather than a refusal (§4.3.4): Grain MAY retry it as a fresh attempt under its ordinary attempt policy, with new `attempt_id`s and new mints.

5.5.5. **`recreate_sandbox`.** After `recreate_sandbox` (§4.4.3), Grain re-materializes the cached `ports` and `placements` into the fresh VM and then re-executes the cached `setup_root` and `setup` commands, in the same order and under the same timeout, in the new VM's root working directory, without calling `mint_<method>` again. A setup command therefore always runs in a fresh sandbox, exactly once per sandbox, and need not be idempotent within one. A command that fails during re-execution does not end the attempt mid-flight: Grain reports the failing grant, exit status, and output tail in the `recreate_sandbox` result, so the agent knows which part of its setup is missing, and the attempt continues.

---

## 6. Standard Capability Patterns

Every capability combines the primitives of §4–§5 into one of four patterns. `tools/list` for all of them follows §4.1.2.

### 6.1 Leased Cloud Credential (`#gcp-key` → `mint_gcp-key` + `release_gcp-key`)

- `mint_gcp-key({attempt_id: "e1010c47-175-2-0", deadline, args: {account: "staging"}})` mints a short-lived IAM key for `staging`, records `attempt_id → key_name` with a timer at `deadline`, and returns a `placements` entry for `/home/debian/.gcp-service-account-staging.json` (`mode: "0600"`) plus a `prompt_section`; it MAY add a `setup` command (a placed `0700` script that runs `gcloud auth activate-service-account` against the key, §5.5) so the CLI is signed in before the agent starts. If the service account is at its key limit it returns `{"status": "resource_exhausted", "retry_after": "2026-10-19T21:05:30Z"}` and the run stays queued without booting. A `deadline` past the key's maximum lifetime returns `refused`.
- `release_gcp-key({attempt_id})` deletes the recorded key and returns `{"status": "ok"}`.

### 6.2 Attempt-Scoped MCP Toolset (`#self-debug` → `mint_self-debug` + `release_self-debug`)

- `mint_self-debug({attempt_id: "e1010c47-175-2-0", deadline, args: {}})` mounts a per-attempt MCP toolset at `/runs/e1010c47-175-2-0/mcp` and returns:
  ```json
  { "status": "ok", "mcp_endpoint": "/runs/e1010c47-175-2-0/mcp",
    "prompt_section": "Host diagnostic tools are available on this grant's MCP server." }
  ```
  Grain proxies the endpoint, learns the server name (`self-debug`) and tool list from `initialize` and `tools/list`, wires the server into the active agent CLI's native per-run configuration (`agy`, `claude`, `codex`, or custom) or CI `mcp.json` (§5.2.4), and names it in the attribution block.
- `release_self-debug({attempt_id})` unregisters the path and returns `{"status": "ok"}`.

### 6.3 Authenticated Proxy Port and Working Tree (`#git-repo` / `#model-proxy`)

- `mint_git-repo({attempt_id: "e1010c47-175-2-0", deadline, args: {repo: "bwsalmon/grain", mode: "write", base: "main", branch: "grain/task-214", root: true}})` (with strict allowlist patterns on every argument; `root` is a boolean defaulting to `true`) authorizes a per-attempt token on its `git-proxy` data-plane listener (`host_port: 9728`, distinct from its `/mcp` port and owned by the same service) and returns `ports: [{"host_port": 9728, "sandbox_port": 9728}]`, two `placements` under a per-grant directory — the token in `/home/debian/.git-repo/e1010c47-175-2-0/credentials` (`mode: "0600"`; per grant, so two grants never collide on one `.git-credentials`, §5.1.2) and a clone script in `/home/debian/.git-repo/e1010c47-175-2-0/setup.sh` (`mode: "0700"`) that runs `git clone -c credential.helper="store --file=/home/debian/.git-repo/e1010c47-175-2-0/credentials" http://127.0.0.1:9728/bwsalmon/grain` and checks `branch` out on top of `base` — and, because `root` is `true`, `setup_root: "/home/debian/.git-repo/e1010c47-175-2-0/setup.sh"`: the script clones into its `cwd`, the root working directory, and the agent starts inside the checkout (§5.5). It MAY also return an `mcp_endpoint` for pull-request tools.
- **Additional repositories** are the same method with `root: false`: `mint_git-repo({attempt_id: "e1010c47-175-2-1", deadline, args: {repo: "bwsalmon/silo", mode: "read", root: false}})` returns the same shape of `ports` and `placements` but `setup` instead of `setup_root`, and its script clones into `/home/debian/repos/bwsalmon/silo`, outside the root working directory, which its `prompt_section` names. A run therefore holds one `root: true` grant and any number of `root: false` grants (§2.3.3) without contending for `setup_root` (§5.5.2); a run holding two `root: true` grants is refused before it boots, naming both; a run with no `root: true` grant starts in an empty root working directory. In the target architecture this is how a task's own repository and its extra read or write repositories reach the sandbox: one grant per repository, the task's own with `root: true`.
- `release_git-repo({attempt_id})` revokes the token and the MCP route and returns `{"status": "ok"}`. `mint_model-proxy` / `release_model-proxy` (`args: {vendor: <enum>}`) follow the same shape on `9729`, with no setup command.

### 6.4 Stateless Skill / Playbook (`#skill` → `mint_skill`, no `release_skill`)

- `mint_skill({attempt_id, deadline, args: {}})` returns `placements` for `/home/debian/.skills/<skill>/SKILL.md` and helper scripts (`mode: "0644"`) and a `prompt_section` pointing at the index. Nothing is allocated server-side, so `release_skill` is omitted from `tools/list` and teardown is a no-op. The reserved-path and size rules of §5.1.2 apply, and an attached skill server is trusted as §1.3.1 describes.
