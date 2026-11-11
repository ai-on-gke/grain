# Sandbox-to-Host & Cross-Run Credential Isolation Audit (2026-11-10)

## Scope & Threat Model

Any non-admin user who can file or steer a task controls what its agent executes inside the assigned sandbox (including passwordless `root` inside granule guest VMs). This audit evaluates whether code running inside a sandbox—or driving the host-side agent CLI via MCP—can reach **host credentials**, **deployment-wide secrets**, or **another run's credentials** across all seven boundary paths:

1. Cloud metadata server (`169.254.169.254`, `fd20:ce::254`, `metadata.google.internal`)
2. `gcp-key` token listener (`http://127.0.0.1:<port>/runs/<attempt_id>/token`)
3. Git proxy (`192.168.9.1:9728`-style gateway listener)
4. Daemon API and UI (`singleTenantTrusted`, `/api/*`, `/api/mcp`, sandbox token lifetime/replay)
5. Host services (Docker socket, `hostd`/`agentd`, `host-script`, Postgres, `pkgcache`, `actionscache`, `modelproxy`, capability listeners)
6. Cross-sandbox isolation (peer ports, shared caches, volumes, forwarded credential ports)
7. Agent CLI host side (MCP file tools, attachments, native tool stripping, `HostToolGrant` / `https://grain.dev/capabilities/corp#research`)

Methodology combined static analysis of `ai-on-gke/grain` at `563ec374f` across all sandbox backends (`GranuleSandboxes`, `ContainerSandboxes`, `K8sSandboxes`, `K8sGranuleSandboxes`, `HostSandboxes`) with live non-destructive probes from inside an active granule VM sandbox (`192.168.9.14/24`, gateway `192.168.9.1`). No live `k8s` or `k8s-granule` cluster was reachable from this run; Kubernetes backends were audited in code and verified via the Calico `kind` integration suite (`TestK8sSandboxesAgainstARealKindCluster`).

---

## Summary Matrix

| # | Path | Sandbox Backends | Result Before PR | Severity | Disposition |
|---|------|------------------|------------------|----------|-------------|
| 1 | Cloud metadata server | `granule`, `container` | IPv4 `169.254.0.0/16` and `fe80::/10` blocked by host `nft` (live-confirmed), **except** `openFenceAlways` omitted `fc00::/7` (GCE/GKE IPv6 metadata `fd20:ce::254`, EC2 `fd00:ec2::254`), `::ffff:0:0/96`, and `0.0.0.0/8` | **Medium** | **Fixed in this PR** (`pkg/orchestrator/granule_firewall.go`) |
| 1 | Cloud metadata server | `k8s`, `k8s-granule` | `deploy/kubernetes/networkpolicy.yaml` blocks `169.254.0.0/16` & `fc00::/7`, **but** is not bundled in `grain.yaml` / `grain-granule.yaml`, `-k8s-network-policy` is opt-in and only gates `NetworkNone`, `K8sSandboxes` lacks `unfencedOpenNetwork`, and `K8sGranuleSandboxes` (`CHV_NET_MODE=inline`) installs no in-pod metadata firewall | **High** (on GKE/K8s without `networkpolicy.yaml` + Workload Identity) | **Follow-up task filed** + `real-kubernetes` metadata assertion added |
| 2 | `gcp-key` token listener | All backends | Host listener binds `127.0.0.1` only (`dataplane.go:34`), forwarded per-sandbox with 32-byte `portforward` token + `/proc` socket UID check; `/runs/<attempt_id>/token` and `/id-token` check per-attempt 32-byte `subject_token` in constant time (`401 invalid_grant` with no enumeration oracle) and invalidate immediately on `Release` | **Pass** (Informational: already-minted Google OAuth/OIDC tokens live up to lease/1h) | Regression test added (`TestGCPKeyTokenListenerIsolatesAttemptsAndRequiresLoopback`) |
| 3 | Git proxy (`:9728`) | All backends | Repo scope, push ref-update parsing (`refupdates.go`), TLS enforcement, and HTTP 3xx redirect refusal (`http.ErrUseLastResponse`) hold (live-confirmed). **Defense-in-depth gap:** `HandleRequest` and `ServeGitSignHTTP` ignored `(TaskID, Attempt)` bindings on `Grant`, and `recreate_sandbox` downgraded `MintSandboxGrant` to unbound `MintSandboxToken` | **Low** | **Fixed in this PR** (`pkg/gitproxy/core.go`, `sign.go`, `pkg/orchestrator/recreate.go`) |
| 4 | Daemon API & UI | All backends | UI (`:4242`) unreachable from sandbox (live-confirmed); `/api/*` and `/api/mcp` via `:9728` require live `TaskID` and enforce `agentAPIGate` (`sandbox-cli` channel blocks `internal`, `secrets:read`, `settings:write`, `capabilities:admin`). **Defense-in-depth gap:** `forwardToAPI` left `Cookie` and non-`x-grain-*` proxy/assertion headers intact | **Low** | **Fixed in this PR** (`pkg/gitproxy/namespace.go`) |
| 5 & 6 | Shared Go build cache (`pkgcache` `/gocache/`) | All backends (`GOCACHEPROG`) | `pkg/daemon/packagecache.go:343` starts `pkgcache` with `JobTokens: nil` and `SplitGoCacheTrust: false`. Any `NetworkOpen` sandbox can `PUT` unauthenticated `action -> output` entries into `/gocache/action/` and `/gocache/trusted/action/` (live-confirmed `200 stored`), poisoning `go build`/`go test` in privileged task and CI sandboxes | **Critical** | **Follow-up task filed** + `knownGap` test added (`TestPackageCacheGoCacheRejectsUnauthenticatedWrites`) |
| 3 & 5 | `pkgcache` GHCR auto-auth & GitHub pack cache | All backends | `pkgcache` receives deployment-wide `secrets/github` credentials (`packagecache.go:338`). Any sandbox can request `GET /token?service=ghcr.io&scope=repository:<owner>/<private-repo>:pull` (`Host: ghcr.io`) and receive a minted GHCR pull JWT without sandbox auth or `GitScope` check (`docker.go:230,531`); `serveGitUploadPack` serves cached packfiles without repo auth | **High** | **Follow-up task filed** + `knownGap` test added (`TestPackageCacheGHCRTokenChecksSandboxRepoScope`) |
| 5 | Other host services (Docker, `hostd`, `host-script`, Postgres, `actionscache`, `modelproxy`) | All backends | Host Docker socket never mounted into sandboxes; `hostd`/`agentd` use `0600` Unix sockets (`SO_PEERCRED`); `host-script`, `gchat`, `figma`, and Postgres bind `127.0.0.1` and are blocked by host `nft` (live-confirmed); `:9730`, `:9731`, `:9732`, `:9744+` enforce random bearer tokens (live-confirmed `401`) | **Pass** | Covered by real-VM & unit security suites |
| 6 | Cross-sandbox network & bridge ICC | `granule`, `container` | `openFence.write` placed `th dport 53 accept` **before** dropping the sandbox's own bridge CIDR, and `GranuleSandboxes.ensureNetwork` omitted `enable_icc=false`, allowing two `NetworkOpen` root sandboxes on `192.168.9.0/24` to communicate over TCP/UDP port 53 | **Medium** | **Fixed in this PR** (`pkg/orchestrator/granule_firewall.go`, `granule_sandboxes.go`) |
| 7 | Agent CLI host side | Claude, Codex, Antigravity | Claude runs with `--tools ""` + `--strict-mcp-config`; Codex runs in isolated `CODEX_HOME` with shell/exec/apply_patch disabled and `clearRunFiles` unlinking `auth.json`; Antigravity enforces `grain-native-tool-denial` `PreToolUse` hook (`hook.go`), admitting only published MCP tools and exact read-only `HostToolGrant` tools when call recording succeeds; `ResponseAttachments` reads `path` strictly via `SandboxFileReader` | **Pass** | Regression test added (`TestAgentCLIHostContainment`) |

---

## Detailed Path Findings

### Path 1: Cloud Metadata Server (`169.254.169.254` / `fd20:ce::254` / `metadata.google.internal`)

- **Granule VM (`GranuleSandboxes`) & Docker (`ContainerSandboxes`):**
  - Host firewall chains (`pkg/orchestrator/granule_firewall.go:574-758`, shared by `ContainerSandboxes` via `pkg/orchestrator/container_fence.go:173`) hook `inet` (`forward`, `input`) and `bridge` (`forward`, `prerouting` by `iifname` with `ether type != arp` so guest source-IP spoofing cannot bypass the chain).
  - **Live granule VM test (`192.168.9.14`):** Probes to `http://169.254.169.254/computeMetadata/v1/` (`:80`), `169.254.169.252:988` (GKE metadata proxy), `[fd20:ce::254]:80`, `[fd00:ec2::254]:80`, and `metadata.google.internal` all timed out or returned `NXDOMAIN` (`exit=28` / `exit=7`).
  - **Gap found & fixed in this PR (Medium):** `openFenceAlways` (`granule_firewall.go:680-685`) previously listed only `127.0.0.0/8`, `169.254.0.0/16`, `::1/128`, and `fe80::/10`. Unlike `deploy/kubernetes/networkpolicy.yaml`, it omitted `fc00::/7` (which covers GCE/GKE IPv6 metadata `fd20:ce::254` and AWS EC2 IPv6 IMDS `fd00:ec2::254`), `::ffff:0:0/96` (IPv4-mapped IPv6), and `0.0.0.0/8`. In addition, `scripts/setup.sh` treated `ip6tables` rules for `fd20:ce::254` as best-effort (`|| echo`), and standalone `grain daemon` deployments run without `setup.sh`. `openFenceAlways` now drops `0.0.0.0/8`, `::ffff:0:0/96`, and `fc00::/7` unconditionally.
  - Note on `ContainerFenceAuto` (`container_fence.go:108`): if `grain daemon` runs in Docker mode without `CAP_NET_ADMIN` / `nft`, `ContainerFenceAuto` logs a warning, blocks external capability grants via `OpenNetworkUnfenced`, and still runs containers unless `-container-sandbox-fence=require` is set (`scripts/setup.sh` sets `require`).
- **Kubernetes (`K8sSandboxes` & `K8sGranuleSandboxes`) — High (Deployment-Dependent, Follow-Up Filed):**
  - `deploy/kubernetes/networkpolicy.yaml` (`grain-sandbox-egress`) blocks `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10`, `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`, `192.168.0.0/16`, `::1/128`, `fc00::/7`, and `fe80::/10`.
  - However: (1) `deploy/kubernetes/grain.yaml` and `deploy/kubernetes/grain-granule.yaml` do not include `grain-sandbox-egress`; (2) `-k8s-network-policy` defaults to `false` and only refuses `NetworkNone` tasks (`k8s_sandboxes.go:271-278`), never `NetworkOpen` tasks; (3) `K8sSandboxes` does not implement `unfencedOpenNetwork` (`container_fence.go:67`) when `NetworkPolicyEnforced` is `false`; and (4) `K8sGranuleSandboxes` (`k8s_granule_sandboxes.go:154-205`) launches privileged pods with `CHV_NET_MODE=inline` without installing in-pod nftables rules to drop `169.254.0.0/16` / `fc00::/7`. On a GKE cluster without `networkpolicy.yaml` + CNI enforcement and without Workload Identity / metadata concealment, any `NetworkOpen` pod can fetch the GKE node service account token from `169.254.169.254`.

### Path 2: `gcp-key` Token Listener (`http://127.0.0.1:<port>/runs/<attempt_id>/token`)

- **Host Binding & Port Forwarding (`Pass`):**
  - `gcpTokens` (`pkg/capability/intree/gcp_token.go:53`) binds via `Server.listen` (`pkg/capability/intree/dataplane.go:29-36`), which rejects any non-loopback IP (`!ip.IsLoopback()`). Default listen address is `127.0.0.1:4252`.
  - **Live granule VM test:** `192.168.9.1:4252` timed out from `192.168.9.14` (both unbound on gateway IP and dropped by host `nft`).
  - When a task holds `gcp#gcp-key` in token mode, `portforward.Relay` (`pkg/capability/portforward/relay.go:57-230`) registers a 32-byte `crypto/rand` forward token bound to the specific `127.0.0.1:<port>` target and verifies `/proc/net/tcp*` socket UID ownership (`verifiedDial`, `owner.go`) before dialing. Inside the guest, `GuestForwarder` (`guest.go:88`) binds `127.0.0.1:<sandbox_port>` only.
- **Per-Attempt Subject Token, Cross-Attempt Guessing & Release (`Pass`):**
  - Each minted attempt receives an independent 32-byte `crypto/rand` hex `subject_token` (`intree.go:356`), placed mode `0600` at `/home/debian/.gcp-keys/.<set>-token`.
  - `/runs/<attempt_id>/token` (`gcp_token.go:165-177`) and `/runs/<attempt_id>/id-token` (`gcp_token.go:235-248`) look up `t.attempts[attemptID]` and verify `subtle.ConstantTimeCompare([]byte(subject), []byte(a.token)) == 1`. Unknown `attempt_id`, empty `subject_token`, and wrong `subject_token` all return identical `401 {"error":"invalid_grant"}`—no enumeration oracle exists.
  - `/runs/<attempt_id>/introspect` (`gcp_token.go:303-325`) stores only `sha256(accessToken)` per attempt and returns `200 {"active":false}` for unknown attempts or tokens minted for other attempts.
  - `Release(attemptID)` (`gcp_token.go:108-112` & `capability_materialize.go`) immediately deletes `t.attempts[attemptID]` and closes the `portforward` registration plus active TCP connections.
  - **Informational note:** Already-minted Google OAuth2 access tokens remain valid at Google APIs until `min(1h, max_lease, run_deadline - now + 1s)` (`pkg/capability/gcpsets/token.go:83-102`), and `/id-token` mints standard 1-hour Google-signed OIDC JWTs (`token.go:154-175`), which Google IAM does not support revoking mid-flight once handed to the sandbox.

### Path 3: Git Proxy (`192.168.9.1:9728`)

- **Authentication, Scope & Ref Enforcement (`Pass`):**
  - Sandbox identity is resolved strictly from `Authorization: Basic` / `Bearer` against `SandboxTokens` (`pkg/gitproxy/core.go:260`, `tokens.go:321`)—never from client IP or forwarding headers.
  - `ModelAuthorizer.Authorize` (`authorize.go:68-124`) resolves `Store.GitScope(ctx, sandbox)` from the currently live run: fetches are restricted to `Target` + `Reads` (+ deployment source only when deployment-wide `grain#source` is held), pushes to `Target` / `Writes` only, and the encrypted state repo is unconditionally refused (`ForbiddenSet`).
  - `AuthorizeRefUpdates` (`refupdates.go:39-145`) parses smart-HTTP pkt-line commands ahead of both remote and local forwarding, restricting pushes to `refs/heads/<task.Branch>` unless the run holds `repo#write` with `any-ref` (`GitPushAnyRefGrant`).
  - **Upstream Credential Protection (`Pass`):** `ValidateForwardTarget` (`forward.go:36-60`) refuses non-loopback plain HTTP so tokens never traverse cleartext networks; `RealForwarder` (`forward.go:246-249`) sets `CheckRedirect: http.ErrUseLastResponse` so upstream `301`/`302`/`307` responses never replay `Authorization` to a redirect target; inside the sandbox, `~/.git-credentials` holds only the per-attempt proxy token (`x-access-token:<sandbox_token>@192.168.9.1:9728`), never a GitHub PAT or App installation token (live-confirmed).
- **Gap Found & Fixed in This PR (Low / Defense-in-Depth):**
  - `tokens.go:80` documents that when `(TaskID, Attempt)` are bound on a `Grant`, the proxy refuses the token if the sandbox later runs a different task or newer attempt. `/fs/`, `/cli/`, `/api/`, and `/cap/` enforced this via `resolveSandboxScope`, whereas `HandleRequest` (`core.go:263`) and `ServeGitSignHTTP` (`sign.go:49`) called `Authenticate` / `authenticateSandbox` and ignored `Grant.TaskID` and `Grant.Attempt`. Furthermore, `sandboxRecreation` (`cycle.go:2872`, `adopt.go:520`) passed unbound `MintSandboxToken` instead of `MintSandboxGrant`. Both are fixed in this PR.

### Path 4: Daemon API & UI from a Sandbox

- **Direct UI Port & `singleTenantTrusted` (`Pass`):**
  - The daemon UI (`127.0.0.1:4242` by default, or `-ui-addr`) is not in `SandboxServicePorts` (`pkg/daemon/sandboxnet.go:27-68`) and was confirmed unreachable (`exit=28` timeout) from `192.168.9.14`.
  - Sandboxes reach the agent API through `gitproxy.ServeAPIHTTP` (`pkg/gitproxy/namespace.go:193-261`) on `:9728`. `forwardToAPI` sets `in.RemoteAddr = ""` (which `ui.isLoopbackRequest` treats as `singleTenantTrusted = true` in single-tenant mode) **only after** `authenticateSandboxGrant` and `resolveSandboxScope` verify a live run with a non-empty `modelScope.TaskID`, strip `Authorization` and `X-Grain-*`, and inject `X-Grain-Acting-Task: <taskID>` + `X-Grain-Channel: sandbox-cli`.
  - Every route on `ui.Server` passes through `agentAPIGate` (`pkg/ui/agent_api_gate.go:203`), which enforces `routeScopes` (`route_scopes.go`): `internal` routes (`/api/auth/*`, `/api/hostd/install.sh`, `/api/events`), ungranted scopes (`secrets:read`), and scopes forbidden on `sandbox-cli` (`settings:write`, `capabilities:admin`) all return `403` (live-confirmed from `192.168.9.14`). `POST /api/mcp` (`mcp_endpoint.go:79-145`) resolves the same `SandboxAPIGrantsOf` scope set and dispatches subrequests back through `s.ServeHTTP` (`agentAPIGate`).
- **Token Lifetime & Cross-Sandbox Replay:**
  - Sandbox proxy tokens have a 24h TTL (`DefaultSandboxTokenTTL`), refreshed hourly while live (`cycle.go:2756`) and deleted on teardown (`Revoke`). Even if unrevoked before teardown completes, `SandboxNamespaceScope` and `GitScope` return `live = false` the instant `FinishRun` commits.
  - Because `AuthenticateGrant` authenticates bearer possession rather than client IP, Sandbox B could only replay Sandbox A's token while Sandbox A's attempt is still live if Sandbox A actively exfiltrated its own token to Sandbox B (or via a cross-sandbox vulnerability such as Finding 1 below).
- **Gap Found & Fixed in This PR (Low / Defense-in-Depth):**
  - `forwardToAPI` now also strips `Cookie`, `Proxy-Authorization`, `Forwarded`, `X-Real-Ip`, `X-User-LDAP`, `X-Forwarded-*`, `X-Auth-Request-*`, `Cf-Access-*`, `Tailscale-User-*`, and IAP/Hub assertion headers before invoking `p.API`.

### Path 5 & Path 6: Host Services & Cross-Sandbox Isolation

- **Finding 1 — CRITICAL (Confirmed in Code & Live Test): Unauthenticated Shared Go Build Cache (`pkgcache` `/gocache/`) Enables Cross-Sandbox Code Execution & Credential Theft**
  - Every sandbox with package caching enabled—including unprivileged `NetworkOpen` user tasks, privileged tasks holding `gcp#gcp-key` or `host-script`, `NetworkNone` sandboxes, and Grain CI sandboxes—is configured with `GOCACHEPROG="/tmp/grain-gocacheprog http://192.168.9.1:9729"` (`pkg/orchestrator/packagecache.go:88-140`).
  - `startPackageCacheFrom` (`pkg/daemon/packagecache.go:343`) constructs `pkgcache.NewServer` with `JobTokens: nil` and `SplitGoCacheTrust: false`. When `s.jobTokens == nil`, `goCacheNamespace` (`pkg/pkgcache/gocache.go:167-187`) permits **unauthenticated** `PUT` requests to `/gocache/output/<sha256>`, `/gocache/action/<actionID>`, and `/gocache/trusted/action/<actionID>`.
  - **Live granule VM reproduction (`192.168.9.14`):** Unauthenticated `PUT` of a benign test blob to `http://192.168.9.1:9729/gocache/output/<sha256>` and a random unused test action key under `/gocache/trusted/action/<random_hex>` both returned `HTTP 200 stored`.
  - **Impact:** Go's `cmd/go` trusts `GOCACHEPROG` as local compiler output and verifies only that the returned archive matches the `OutputID` recorded in `GoCacheAction`, not that `OutputID` was genuinely produced from `ActionID`. Because sandboxes share a deterministic stock image and Go toolchain, a malicious `NetworkOpen` task can compute `ActionID`s for packages/tests in target repositories, poison `/gocache/action/` and `/gocache/trusted/action/` with a trojaned archive, and achieve arbitrary code execution inside another task's or CI job's sandbox when it runs `go build` or `go test`—stealing `gcp-key`, `host-script`, `api:*`, or release credentials. It also provides a write/read covert channel into `NetworkNone` sandboxes.
  - **Disposition:** Architectural fix filed as a dedicated follow-up task (wire `JobTokens` + `SplitGoCacheTrust: true` in `daemon`, refuse unauthenticated writes from untrusted task sandboxes or isolate `gocache` per trust domain) and pinned by `TestPackageCacheGoCacheRejectsUnauthenticatedWrites` (`knownGapGoCacheUnauthenticatedWrite`).
  - **Fixed in task 4000:** the daemon's package cache now runs with `JobTokens` (`NewJobTokenSet(false)`, exposed as `PackageCache.GoCacheJobs`) and `SplitGoCacheTrust: true`. A caller presenting no registered token -- every task sandbox -- reads only the trusted action namespace and has every `/gocache/` `PUT` refused with 403 (the GOCACHEPROG helper keeps its own local cache, so builds are unaffected). An untrusted registered job writes a namespace scoped to its own credential that no other job reads. Trusted action entries moved to `gocache/trusted/v2/action/`, so nothing written unauthenticated before the fix is ever served. `knownGapGoCacheUnauthenticatedWrite` is `false`; `TestPackageCacheGoCacheWritesDoNotCrossTasks` covers Task A -> Task B / trusted CI.

- **Finding 2 — HIGH (Confirmed in Code): `pkgcache` Mints Unscoped GHCR Pull Tokens Using Deployment GitHub Credentials**
  - `startPackageCacheFrom` (`pkg/daemon/packagecache.go:338-346`) passes the deployment's full `secrets/github` `CredentialSet` into `pkgcache.NewServer`.
  - In `pkg/pkgcache/docker.go:230-233, 374-398, 531-581` (`serveDockerDirect` -> `proxyDocker` -> `autoAuthDocker`), any sandbox (including a `no_repo` task or a task scoped only to `acme/public`) can send `GET /token?service=ghcr.io&scope=repository:<owner>/<any-private-repo>:pull` with `Host: ghcr.io` to `:9729` (or `:80`/`:443`). `autoAuthDocker` attaches `Authorization: Basic base64("x-access-token:<PAT_or_App_token>")` from `Credentials.PackageToken(owner, repo)` without authenticating the sandbox or checking `Store.GitScope`, and returns GHCR's minted bearer token JSON directly to the caller.
  - Additionally, `serveGitUploadPack` (`pkg/pkgcache/github.go:209-242`) caches `git-upload-pack` responses keyed only by `baseURL + "#body=" + sha256(body)` without verifying repository read scope on cache hits.
  - **Disposition:** Filed as a dedicated follow-up task and pinned by `TestPackageCacheGHCRTokenChecksSandboxRepoScope` (`knownGapGHCRTokenIgnoresSandboxScope`).

- **Finding 3 — MEDIUM (Confirmed in Code, Fixed in This PR): Cross-Sandbox Port-53 Channel & Missing Bridge `enable_icc=false` on Granule Network**
  - `openFence.write` (`pkg/orchestrator/granule_firewall.go:750-758`) emitted `meta l4proto { tcp, udp } th dport 53 accept` before dropping the sandbox's own bridge subnet (`192.168.9.0/24`), and `GranuleSandboxes.ensureNetwork` (`granule_sandboxes.go:1133`) created the Docker bridge without `--opt com.docker.network.bridge.enable_icc=false`. Two `NetworkOpen` root sandboxes could bind port `53` and exchange arbitrary data directly across `192.168.9.0/24`.
  - **Fixed in this PR:** `openFence.write` now accepts DNS to `n.Gateway:53` first, drops loopback and the sandbox's own `nets` CIDRs before `th dport 53 accept`, and `ensureNetwork` sets `com.docker.network.bridge.enable_icc=false`.

- **Other Host Services & Volumes (`Pass`):**
  - **Host Docker socket:** Never mounted into `GranuleSandboxes` (each VM runs an isolated guest `dockerd`), `ContainerSandboxes` (`--cap-drop ALL --security-opt no-new-privileges`, no `/var/run/docker.sock` mount), or `K8sSandboxes` (`automountServiceAccountToken: false`, baseline Pod Security). (`K8sGranuleSandboxes` runs privileged pods for KVM/nested Docker inside its dedicated sandbox cluster/nodepool; it never mounts the controller's runtime socket.)
  - **`hostd` & `agentd`:** Listen only on mode-`0600` Unix domain sockets with `SO_PEERCRED` UID verification (`pkg/agentd/agentd.go:175-210`) or dial outbound over mTLS/token to the controller.
  - **`host-script`, `gchat`, `figma`:** Bind `127.0.0.1` via `Server.listen` (`dataplane.go:34`), forwarded only into sandboxes whose task holds the grant, and verify per-attempt 32-byte tokens via `subtle.ConstantTimeCompare` (`hostscript.go:195-215`).
  - **Postgres:** Embedded Postgres (`pkg/model/postgres/embedded.go:173`) binds `-h 127.0.0.1` with a `0700` Unix socket directory; installer Postgres (`scripts/setup.sh`) binds `listen_addresses = '127.0.0.1'` with `scram-sha-256`; `:5432` on `192.168.9.1` timed out in live testing.
  - **`actionscache` (`:9730`), `portforward` (`:9731`), `ciapi` (`:9732`), `modelproxy` (`:9744-9746`):** Live unauthenticated probes from `192.168.9.14` all returned `401` (`missing bearer token` / `invalid forward token`). Each mints per-run/per-job `crypto/rand` tokens revoked on release.
  - **Filesystem / Volumes:** Granule VMs use per-VM qcow2 overlays (`-disk-mode=overlay`); `ContainerSandboxes` and `K8sSandboxes` have no shared writable host bind mounts; `HostSandboxes` (`host` mode, explicitly documented as non-isolated) is guarded by `safepath.Within` on MCP file tools (`containment_test.go`).

### Path 7: Agent CLI Host Side

- **Native Tool Stripping & Credential Isolation (`Pass`):**
  - **Claude Code (`pkg/agent/claude/claude.go:546-549, 850-856`):** Invoked with `--tools ""` (emptying native Bash/Read/Edit/Write tools), `--mcp-config <per-run-file>`, `--strict-mcp-config`, and `--allowedTools` restricted to `grain-sandbox` and granted capability MCP servers. `HostdEnv()` passes only `CLAUDE_CODE_OAUTH_TOKEN` / Vertex vars to the host CLI process, never into the sandbox's `run_command` environment.
  - **Codex (`pkg/agent/codex/codex.go:684-748, 840-1000`):** Runs in a per-run/per-task private `CODEX_HOME` with `shell_tool = false`, `unified_exec = false`, `view_image = false`, `apps = false`, `plugins = false`, `multi_agent = false`, an empty `environments.toml`, and `apply_patch` stripped from the model catalog. `clearRunFiles` (`codex.go:995`) removes `auth.json`, `config.toml`, and `mcpserver.json` on cleanup using `context.WithoutCancel`.
  - **Antigravity (`pkg/agent/antigravity/antigravity.go` & `pkg/agent/antigravity/hook/hook.go`):** Installs the `grain-native-tool-denial` `PreToolUse` hook (`agy-tool-hook`). `DecisionFor` (`hook.go:238-282`) denies every tool call except `PublishedTools` (`mcp_grain-sandbox_*`), explicitly passed `-capability-tool` entries, and registered `HostToolGrant` read-only tools.
- **MCP File Readers & Attachments (`Pass`):**
  - `mcp.ResponseAttachments` (`pkg/mcp/response_attachments.go:84`, `sandbox_file_reader.go:40-56`) resolves `path` attachments exclusively through `SandboxFileReaderOf(tools)`—which executes `sshReadBytes` inside the remote VM/container/pod or `safepath`-confined `hostReadBytes` in `SandboxRoot`. No MCP tool (`read_file`, `write_file`, `edit_file`, `ask_question`, `comment_on_issue`, `chat`) reads arbitrary host paths or host environment variables.
- **`https://grain.dev/capabilities/corp#research` (`HostToolGrant`, `Pass`):**
  - `HostToolGrant` (`pkg/agent/antigravity/hosttools.go:30-55`, `hook.go:171-216`) maps a capability URI in compiled Go code (never from user-supplied grant arguments) to a fixed list of read-only lookup tools. Even when a task holds `https://grain.dev/capabilities/corp#research`, `DecisionFor` admits **only** those exact read-only lookup tools, appends every invocation (`tool`, `query`, `host`, `task_id`, `run_id`) to `RecordPath` **before** allowing the call, and **fails closed** (`deny`) if recording fails (`hook.go:268-270`). Built-in host shell, file-read, file-write, subagent, and workflow tools remain hard-denied.

---

## Regression Tests & Nightly Wiring

To ensure nightly runs both fast protocol/ruleset checks and live real-sandbox isolation checks (and for the follow-up task wiring `#3981`–`#3984` and `#3965` into a unified `security-attacks` nightly job), this PR adds:

1. **Package `./tests/security` (`tests/security/sandbox_to_host_test.go`)** — runs on every PR in `Tests`, via `make security`, and in nightly's `go-long` job:
   - `TestOpenFenceBlocksMetadataLoopbackAndPeerDNS` (includes kernel `nft -c -f -` validation)
   - `TestGCPKeyTokenListenerIsolatesAttemptsAndRequiresLoopback`
   - `TestGitProxyEnforcesAttemptBindingSpoofingAndRedirectSafety`
   - `TestDaemonAPIFromSandboxBlocksInternalAndUnscopedRoutes`
   - `TestAgentCLIHostContainment`
   - `TestPackageCacheGoCacheRejectsUnauthenticatedWrites` (`knownGapGoCacheUnauthenticatedWrite`)
   - `TestPackageCacheGHCRTokenChecksSandboxRepoScope` (`knownGapGHCRTokenIgnoresSandboxScope`)
2. **Package `./pkg/orchestrator` real-sandbox nightly jobs (`.github/workflows/nightly.yml`):**
   - `kvm-sandboxes` (`GRAIN_REAL_VM_TESTS=1`): `TestGranuleSandboxOpenNetworkFenceBlocksMetadataHostAndPeers` (`pkg/orchestrator/granule_docker_real_test.go`) — boots a real `NetworkOpen` granule KVM guest with `UseRealHostFirewall()` and verifies `169.254.169.254`, `fd20:ce::254`, non-service host ports, non-gateway host IPs, and sibling bridge sandboxes on `:8080` and `:53` are all unreachable.
   - `real-kubernetes` (`GRAIN_REAL_K8S_TESTS=1`): `TestK8sSandboxesAgainstARealKindCluster` (`pkg/orchestrator/k8s_sandboxes_real_test.go`) — verifies from inside a live `NetworkOpen` pod on Calico that `169.254.169.254` is blocked by `deploy/kubernetes/networkpolicy.yaml`.
