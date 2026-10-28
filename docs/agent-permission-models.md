# Agent Permission Models in Grain

An agent running in a Grain sandbox executes untrusted repository code (tests, build scripts, dependencies) alongside LLM-chosen commands. Permission design in Grain therefore separates **two questions**:

1. **Where do credentials live and how are they scoped?** (Options 1–4: Image → VM/Pod Identity → Minted Per-Sandbox Lease → Active Proxy)
2. **What surface does the agent invoke?** (CLIs and SDKs inside `run_command` vs. structured MCP tools outside the sandbox)

In Grain, **Option 3 (Minted Temporary Credentials)** and **Option 4 (Active Proxies + Host/Capability-Server MCP Tools)** are the primary patterns because they preserve per-task capability grants (`model.Grant`), work consistently across all sandbox backends (Granule microVMs and Kubernetes Pods), and bound blast radius.

---

## Part 1: Credential & Permission Delivery Models (Options 1–4)

| Dimension | Option 1: Bake Fixed Creds into Image | Option 2: Bake Permissions into VM / Pod | Option 3: Minted Temporary Credentials | Option 4: Active Proxy |
| :--- | :--- | :--- | :--- | :--- |
| **Secret in Sandbox?** | Yes (static on disk in image layer) | No static file (ambient metadata token) | Yes (short-lived key/token placed `0600`) | **No** (only an opaque per-run proxy token) |
| **Granularity** | Per-deployment / per-image | Per-VM / per-Pod (`ServiceAccount`) | **Per-task / per-attempt** (`model.Grant`) | **Per-request / per-verb / per-resource** |
| **Exfiltration Risk** | **Critical** — any run or anyone pulling the image gets permanent creds | **Medium** — token can be exfiltrated via `169.254.169.254` / SA mount until TTL (~1h) | **Low–Medium** — usable outside sandbox only until `Revoke` or `Deadline` | **Near Zero** — proxy token is useless off the sandbox bridge and dies on `Release` |
| **Revocation** | Rebuild & redeploy image + rotate upstream | Delete Pod/VM or change IAM binding (delayed) | Delete key/token on `Revoke` + `Reaper` sweep | **Instant** in-memory revocation on controller |
| **Protocol Effort** | Zero (any CLI/SDK works) | Zero for cloud SDKs supporting ADC/Workload ID | Low (1 `Mint`/`Revoke` client per provider) | High (requires L7 proxy per upstream protocol) |

### Option 1: Bake Fixed Credentials into the Sandbox Image

* **Mechanism:** Static credentials (keys, tokens, certificates) are baked into the container or VM filesystem at image build time.
* **Tradeoffs:** Zero per-run dispatch latency and works completely offline, **but** breaks Grain’s per-task `Grant` model (every task and CI job booting the image gets the credential), makes rotation require an image rebuild and redeploy, and risks leaking secrets if the image is ever pushed to a registry.
* **How to implement in Grain:**
  * *Granule VMs and K8s/container pods (`pkg/sandboximage`):* Build a custom image `FROM` `ghcr.io/bwsalmon/grain/guest-base` or `ghcr.io/bwsalmon/grain/k8s-sandbox-base` and name it in `model.Config.SandboxImage` (Settings → Sandboxes → Custom sandbox image). The image lives in whatever registry you push it to, so anything baked into it is readable by everyone who can pull from that registry (`docs/sandbox-image-contract.md`).
  * *K8s Pods (`Dockerfile.k8s-sandbox` / `pkg/k8s`):* Bake into a custom sandbox image or populate read-only `subPath` mounts via the unprivileged `k8s.Setup` init container.
  * *Safer alternative in Grain (`ProvisionPlace`):* Rather than baking secrets into an image layer, implement a `ProvisionPlace` capability (`pkg/model/task.go`) that resolves standing material from `pkg/secrets` via `CapabilityContext.Credentials.Resolve` and writes a `model.Placement` (`0600`) into the sandbox only for runs holding the grant (for example, `pkg/capability/packagepublish`).

### Option 2: Bake Permissions into the Sandbox VM / Pod Identity

* **Mechanism:** Attach a cloud or cluster identity directly to the sandbox compute instance (such as a Kubernetes `ServiceAccount` bound via GKE Workload Identity, or a GCE VM service account exposed through the `169.254.169.254` metadata server).
* **Tradeoffs:** Eliminates static key files on disk—cloud CLIs and SDKs pick up ambient credentials automatically via Application Default Credentials (ADC). **However:**
  1. It couples capabilities to the compute substrate: Kubernetes Pods have native `ServiceAccount` projected tokens, whereas Granule microVMs (`-platform=granule` or `-platform=k8s-granule`) share a host node and have no distinct cloud metadata identity per microVM without a host shim.
  2. Every process in the sandbox (including untrusted `npm test` or `go test` child processes) gets ambient access to the metadata server unless restricted by UID or firewall rules.
  3. Coarse granularity unless Pod/VM identities are dynamically constructed per combination of task grants.
* **How to implement in Grain:**
  * *K8s backend (`pkg/orchestrator/k8s_sandboxes.go`, `pkg/k8s/k8s.go`):* Today `PodOptions.ServiceAccount` is fixed per deployment (defaulting to the unprivileged `default` SA with no API token mounted). To make it grant-driven, map a task’s resolved grants in `K8sSandboxes.Acquire` to a dedicated K8s `ServiceAccount` annotated for GKE Workload Identity (or mount projected service account tokens per grant).
  * *Granule backend (`pkg/orchestrator/granule_sandboxes.go`):* Run a per-sandbox HTTP metadata shim on the host tap/bridge intercepting `169.254.169.254:80` (alongside `granule_firewall.go`), identifying the calling sandbox by tap interface and serving short-lived access tokens for that task’s granted IAM identity.

### Option 3: Temporary Credentials Minted for a Sandbox

* **Mechanism:** At dispatch time (`Materialize`), the controller or Capability Server uses a narrow "minter" credential to mint a short-lived, attempt-scoped credential (such as a leased GCP service-account key, a GitHub App installation token, or an ephemeral test repository), writes it into the sandbox filesystem (`Placement` with `0600`), records a `model.Lease` bounded by `Deadline`/`MaxLease`, and revokes it when the run ends (`Revoke`), backed by a periodic `Reaper` sweep against the upstream cloud API.
* **Tradeoffs:**
  * *Pros:* Per-task and per-attempt scoping (`model.Grant`); works identically across Granule VMs, K8s Pods, and CI jobs (`CapabilitySpec.SupportsCI()`); works with standard CLIs (`gcloud`, `gh`, `terraform`) and SDKs unmodified; and integrates with admission control (`model.ScarceResourceProvider` queues runs with `ResourceExhaustedError` when a finite pool is full rather than burning a sandbox).
  * *Cons:* Raw credential material resides in the sandbox filesystem for the duration of the attempt (an exfiltrated key is usable outside the sandbox until `Revoke` or `Deadline` fires), and cloud IAM APIs impose quota/rate limits on key creation (for example, GCP’s 10-keys-per-SA limit, mitigated by numbered account pools in `pkg/capability/gcpkey`).
* **How to implement in Grain:**
  * *In-process registry (`pkg/capability/`):* Implement `model.CapabilityProvider` with `Provision: model.ProvisionMint` (see `pkg/capability/gcpkey`, `pkg/capability/geminikey`, `pkg/capability/githubsandbox`):
    1. Resolve a narrow **minter** credential (`CapabilityContext.Credentials.Resolve("gcp-key-minter")`)—never the agent account’s own identity, so a leaked agent key cannot mint itself a replacement.
    2. Mint the key/token and return `model.Materialization{Lease: &model.Lease{ExpiresAt: cc.Deadline}, Placements: []model.Placement{{Path: "/home/debian/...", Mode: "0600", Content: raw}}}`.
    3. Implement `Revoke(ctx, cc, lease)` for normal teardown and `model.Reaper` (`Reap(ctx, creds, before)`) to delete orphaned keys older than the daemon’s live-work trailing edge.
  * *Target Capability Server (`docs/capabilities-spec.md` §6.1):* Expose `mint_<method>({attempt_id, deadline, args})` returning `placements`, an optional `0700` `setup` script (e.g., `gcloud auth activate-service-account`), and `prompt_section`, paired with `release_<method>({attempt_id})`.

### Option 4: Active Proxy

* **Mechanism:** The standing upstream credential never leaves the controller or Capability Server. Grain mints an opaque per-run bearer token valid only against Grain’s proxy and places the proxy endpoint and bearer token into the sandbox. The proxy authenticates the sandbox token, enforces request-level policy, attaches the upstream credential, and forwards the request.
* **Tradeoffs:**
  * *Pros:* **Strongest network-level security.** Even if a malicious dependency steals the sandbox’s proxy token, it is useless outside the sandbox network and dies the instant the run finishes. Crucially, an active proxy enforces **sub-credential policy** that cloud IAM cannot express: `pkg/gitproxy` authorizes pushes only to the run’s assigned task branch on its target repository, blocks force-pushes to protected branches, and blocks cloning `grain/config` if it ever held `secrets.enc`; `pkg/modelproxy` attributes token spend and hides team API keys.
  * *Cons:* Requires building and maintaining a protocol-aware L7 proxy for each upstream service, and requires client tools/SDKs to support custom base URLs or credential helpers (`ANTHROPIC_BASE_URL`, `git credential.helper`).
* **How to implement in Grain:**
  * *In-process today (`pkg/gitproxy`, `pkg/modelproxy`, `pkg/pkgcache`):*
    1. Bind an HTTP listener on the controller reachable from the sandbox network (`192.168.9.1:9728` for `gitproxy`).
    2. Mint a per-sandbox bearer token (`gitproxy.TokenRegistry` + `keepSandboxTokenFresh` in `pkg/orchestrator/sandboxtoken.go`, or `pkg/capability/modelproxy` placing `/home/debian/.<vendor>-proxy-{url,token}`).
    3. Authorize each request against live store state (`gitproxy.ModelAuthorizer` checking `Store.GitScope(ctx, sandbox)`), inject the upstream credential (`CredentialSet`), and stream the request.
  * *Target Capability Server (`docs/capabilities-spec.md` §6.3):* `mint_<method>` returns `ports: [{"host_port": 9728, "sandbox_port": 9728}]` (forwarded into sandbox loopback `127.0.0.1:9728`) plus a per-attempt token under `/home/debian/.<cap>/<attempt_id>/` and optional `setup_root` / `setup` scripts.

---

## Part 2: The Execution Surface Fork — MCP Tools vs. CLIs

Orthogonal to where credentials live is **how the agent invokes privileged operations**: by running CLIs/SDKs inside `run_command` or by calling structured MCP tools served outside the sandbox.

| Dimension | CLIs / SDKs inside the Sandbox (`run_command`) | Host / Capability-Server MCP Tools (`tools/call`) |
| :--- | :--- | :--- |
| **Trust Boundary** | Executes **inside** the untrusted sandbox; any child process or test script in the sandbox can use the same CLI and credential | Executes **outside** the sandbox (on controller or Capability Server); untrusted repo code in `run_command` **cannot invoke MCP tools** |
| **Credential Exposure** | Requires Option 1, 2, 3, or an Option 4 network proxy | **Always Option 4 (RPC proxy):** zero credentials or network routes enter the sandbox |
| **Policy & Redaction** | Coarse (unless backed by an L7 proxy like `gitproxy`); raw stdout returns to the agent | **Fine-grained:** JSON Schema validation, read-only enforcement (`db-read`), output secret redaction (`grainlog.Redactor`), and live human confirmation (`self-upgrade`) |
| **Agent Ergonomics & Context** | **High composability, zero schema overhead:** models already know `git`, `gcloud`, `jq`, and bash pipelines; multi-step loops run in 1 turn | **Structured & auditable, but 1 turn per call:** every verb needs a JSON schema in prompt context and cannot be piped in bash |
| **CI Compatibility** | Works identically in `.grain/workflows` shell steps and agent runs (`SupportsCI() == true`) | Agent-centric (`RunGrant: true`), though target spec (§5.2) bridges `mcp_endpoint` to `~/.grain/mcp.json` |

### How Each Surface is Implemented in Grain

1. **CLI-in-Sandbox Capabilities (`ProvisionMint` / `ProvisionPlace` + Active Network Proxies):**
   * The provider returns `Placements` (and `PromptSection` telling the agent the file path and CLI activation command, such as `gcloud auth activate-service-account --key-file=/home/debian/.gcp-service-account.json`).
   * Used in Grain for: `gcp-key`, `gemini-key`, `github-sandbox`, `gitproxy` (`git`), and `modelproxy` (SDKs/CLIs via `*_BASE_URL`).
2. **MCP Tool Capabilities (`RunGrant: true` in `pkg/capability/registry.go`):**
   * The orchestrator passes `-grant <name>` to `grain mcpserver` (`pkg/cli/mcpserver.go` → `daemon.GrantedDaemonTools` in `pkg/daemon/mcpserver.go`), which registers `mcp.Tool` handlers executing on the controller (or in the target spec, mounts `mcp_endpoint: "/runs/<attempt_id>/mcp"` on a Capability Server).
   * For host-bound agent frameworks like Antigravity, `antigravity.RegisterHostToolGrant` (`google/capability/corpresearch`) punches a narrow, audited allowlist (the host's own search tools) in `pkg/cli/agyhook.go` so those read-only tools execute as the host’s corp identity without exposing the host's credentials to the sandbox.
   * Used in Grain for: `self-debug`, `grain-log`, `deployment-health`, `db-read`/`db-write`, `pipeline-control`, `self-upgrade`, `corp-research`, and PR/CI tools (`pull_request_status`, `open_pull_request`, `wait_for_checks`).

---

## Decision Matrix: Choosing the Right Model in Grain

* **Use MCP Tools (Option 4 at the RPC layer)** when:
  * The capability reaches into Grain’s own controller state, database, or host systemd journal (`self-debug`, `grain-log`, `db-read`).
  * Output must be scrubbed of secrets before entering the model context (`read_grain_log`), or writes require interactive human confirmation in chat (`self-upgrade`).
  * The action is a high-consequence workflow transition (`open_pull_request`, `propose_task`, `merge_queue`) that untrusted repository scripts inside `run_command` must never be able to trigger.
* **Use CLI + Active Proxy (Option 4 at the network layer)** when:
  * Both interactive agents and CI workflows need standard CLI/SDK ergonomics (`git`, `curl`, LLM SDKs), **and** you need sub-credential policy enforcement or zero credential exfiltration (`gitproxy`, `modelproxy`, `pkgcache`).
* **Use CLI + Minted Temporary Credentials (Option 3)** when:
  * The upstream API surface is too broad to proxy (such as arbitrary GCP APIs accessed via `gcloud` or `terraform` under `gcp-key`) and the minted identity is already scoped to a dedicated sandbox project/account with automatic lease revocation (`Revoke` + `Reaper`).
* **Avoid Options 1 & 2** for privileged secrets, reserving **Option 1** (a custom `SandboxImage`) strictly for non-secret toolchains/CA certificates and **Option 2** only when K8s-native sandbox pods need coarse pod-level Workload Identity.
