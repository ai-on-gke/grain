# Migrating grain onto Shibu

## Status

Plan, no code. Written up in task 1750 from the decision bsalmon and the
director made in the director chat (grain task 1679, 2026-10-03): grain
is refactored to run on top of Shibu
([`shibu.md` 7.2](shibu.md#72-the-decision-grain-runs-on-shibu)).

This is Phase 0 of the plan below. It is documentation only.

Task 1765 updated the package map for backend drivers and for the
separate `/sys/git` and `/sys/forge` contracts
([`shibu.md`, Backend drivers](shibu.md#backend-drivers),
[`/sys/git` and `/sys/forge`](shibu.md#sysgit-and-sysforge)).

Task 1771 moved the UI out of the kernel into the distribution, as
`/sys/ui`, and decided agent-served panes, which settles
[`shibu.md` F30](shibu.md#f30-component-ui-panels)
([`shibu.md` 2.6](shibu.md#26-the-ui-and-the-sysui-conventions)). The
end state, the `ui` row of the package map and Phase 0's exit criteria
follow it.

Everything here is **decided** unless it is marked *Proposed* or listed
under [Unplaced and split packages](#unplaced-and-split-packages), which
records the judgement calls this plan had to make for bsalmon to
confirm.

## Contents

1. [End state](#1-end-state)
2. [Package map](#2-package-map)
3. [Phases](#3-phases)
4. [Invariants that hold throughout](#4-invariants-that-hold-throughout)
5. [Known hard parts](#5-known-hard-parts)
6. [Open questions](#6-open-questions)

---

## 1. End state

Shibu's three layers ([`shibu.md`, Layers](shibu.md#layers-kernel-standard-distribution-additional-components)),
with grain at the top:

```
Shibu kernel            namespaces, grants, allocators, VMs and backend drivers, web proxy,
                        control API and apply(), front door and CLI, agents, secrets
Standard distribution   /sys/model, /sys/runner (bootstrap set)
                        /sys/git, /sys/forge, /sys/cred, /sys/pkgcache, /sys/ui,
                        /sys/reconciler (optional)
grain                   additional components in grain/…: lead (backlog, dispatcher,
                        approvals), ci, mq, repo rules, state export,
                        grain's MCP tools, grain's UI panes
```

When the migration is done:

- grain holds no credential. Its GitHub App key, GCP keys and model keys
  are in the kernel secret store. grain reaches git through `/sys/git`,
  pull requests and checks through `/sys/forge`, and GCP through
  `/sys/cred` endpoints, all granted to it.
- grain creates no VM directly. Its task sandboxes are `create_agent`
  sandbox VMs, and its CI jobs are VMs created through allocator grants,
  in namespaces under `grain/ci` and `grain/mq`.
- A grain task is a Shibu agent. A run is the agent's harness session.
  A task's chat is the agent's `messages`
  ([`shibu.md` 7.1](shibu.md#tasks-agents-sub-task-proposals-approval-ci-and-the-merge-queue)).
- A team's limits are allocator grants from grain's own grants; the
  deployment-wide ledger is the root VM allocator.
- grain's views are panes served by grain's components, which root
  trusts for the UI's main navigation
  ([`shibu.md` 2.6.6](shibu.md#266-agent-served-panes)). The generic
  views are `/sys/ui`'s.
- Everything grain's UI does for a person, the `shibu` CLI can also do
  ([`shibu.md`, CLI parity](shibu.md#cli-parity)).
- Shibu lives in its own repository, and grain depends on it through
  its versioned API.

---

## 2. Package map

Every package under `pkg/` is placed below. Sizes are non-test Go lines
at the time of writing, to show where the work is. `cmd/` is covered
at the end.

### Into the Shibu kernel

| Package | Lines | Becomes |
| --- | --- | --- |
| `granule` | 492 | the first backend driver, docker + granule, v1's only one ([`shibu.md`, Backend drivers](shibu.md#backend-drivers)): VM create, start, stop and delete |
| `lifecycle` | 1,340 | the first driver: image assembly ([`shibu.md` 2.2.14](shibu.md#2214-images)). Split: see below |
| `sandboxaddr` | 442 | the first driver: per-VM address allocation, the basis of unspoofable origin ([F25](shibu.md#f25-the-backend-carries-the-whole-identity-model), [F35](shibu.md#f35-a-driver-must-prove-an-unspoofable-origin-before-it-is-admitted)) |
| `sandboximage` | 511 | the first driver: the base image it boots |
| `k8s` | 850 | the seed of the second driver, Kubernetes with Kata or gVisor-style isolation. Planned, not in v1 |
| `relay` | 1,691 | web proxy: the public rendezvous relay |
| `tunnel` (`connector`, `gateway`, `mux`, `origin`, `via`) | 2,280 | web proxy: public endpoints and their edge |
| `iapreconciler` | 370 | web proxy: audiences on public endpoints ([`shibu.md` 7.1](shibu.md#pkgiapreconciler)) |
| `secrets` | 1,094 | the kernel secret store, its built-in storage backend |
| `secretsrepo` | 850 | *to assess* as a storage backend: ciphertext kept in a git repo of its own. It needs git, which the kernel otherwise never does, so it may be better as an export target than a backend |
| `capability` (framework) | part of 7,789 | grants: the provider registry, leases, `MaxLease`, team scoping. Per-capability placement is below |
| `access` | 449 | front-door identities, mapped to `/users/<name>` and `/` ([`shibu.md` 2.2.18](shibu.md#2218-human-as-a-principal)) |
| `model.Ledger` (in `model`) | 340 | the root VM allocator's accounting, with admission queueing ([`shibu.md` 2.2.21](shibu.md#2221-admission-queue)) |
| `metrics` | 3,459 | `watch` and `cost`. Split: see below |
| `sysstat` | 1,202 | host capacity readings for the root allocators and `watch` |
| `hosttop`, `systemlog` | 276, 375 | the kernel's operator views of its own host. *Placed by this plan* |
| `agent` (contract parts) | part of 13,998 | the harness contract: `agent.Framework`'s seam, `toolroster` (the tool surface), `usagelimit` and `maxturns` (budgets), `refusal` and `runfailure` (exit reasons) |
| `hostinit` | 1,941 | the Shibu installer: host bring-up and the default `/sys` spec. *Placed by this plan*; grain-specific steps move to grain's component install |
| `upgrade` | 3,644 | kernel self-upgrade. *Placed by this plan*; `/sys` components upgrade through the staged swap instead, and grain's components through their own deploys |

### Into the standard distribution

| Package | Lines | Becomes |
| --- | --- | --- |
| `modelproxy`, plus metering | 1,394 | `/sys/model`. Metering is new work ([`shibu.md` 7.1](shibu.md#pkggitproxy-and-pkgmodelproxy)) |
| `agent` (`claude`, `codex`, `antigravity`, `custom`, `credcheck`, `sessionstore`, the host runner) | most of 13,998 | `/sys/runner`: the adapters and the `split` runner |
| `modelexec` | 2,756 | `/sys/runner`: running harnesses on a remote host, with tool calls tunnelled back to the sandbox |
| `console` | 905 | `/sys/runner`: interactive sessions, a CLI on a pty for a person to sit at. *Placed by this plan* |
| `mcp` (tool-server half) | part of 14,807 | the in-VM tool server. The kernel owns its **contract** (an MCP server in the sandbox that is the harness's only tool); the distribution owns the **implementation**, shipped under the `runner` contract as the sandbox base image ([`shibu.md` 4.1](shibu.md#harness-interface)) |
| `gitproxy` | 4,487 | `/sys/git`, its GitHub-backed implementation ([`shibu.md`, `/sys/git` and `/sys/forge`](shibu.md#sysgit-and-sysforge)). Its registry-login path becomes `/sys/cred`'s streaming registry proxy. The Shibu-hosted implementation is new work |
| `forge`, `github`, `checkpost` | 2,060, 6,629, 1,200 | `/sys/forge`, its GitHub-backed implementation, used by grain's `ci` and `mq` components. `forge` is the contract's model. `checkpost` is its checks half: CI reports a verdict there and the merge queue reads it, and it holds no credential. The minimal hosted implementation is new work |
| `pkgcache` | 5,369 | `/sys/pkgcache` |
| `actionscache` | 1,237 | *to decide*: `/sys/pkgcache` (a cache every sandbox reads) or grain CI (it is a GitHub Actions cache, fed by CI). This plan leans to grain CI |
| `capability/modelproxy` | — | grants on `/sys/model` |
| `capability/reporead`, `capability/repowrite`, `capability/githubtoken` | — | grants on `/sys/git` scopes |
| `capability/gcpkey`, `capability/geminikey`, `capability/registrypush`, `capability/packagepublish`, `capability/githubsandbox` | — | `/sys/cred` endpoints and grants on them. They currently place raw keys in sandboxes, which the credential rule forbids ([`shibu.md` 7.1](shibu.md#pkgcapability-and-pkgaccess)) |
| `gcpsetup` | 610 | `/sys/cred`'s GCP kind: the service accounts it mints from. *Placed by this plan* |

### Grain components

Additional components, in namespaces such as `grain/lead`, `grain/ci`
and `grain/mq`.

| Package | Lines | Component |
| --- | --- | --- |
| `orchestrator` (backlog, dispatch, approvals) | most of 39,634 | `grain/lead`: backlog, dispatcher, proposal approval, auto-merge inheritance |
| `dispatch` | 864 | `grain/lead`: which tasks run now |
| `orchestrator` (merge queue) | part of 39,634 | `grain/mq` |
| `ci` | 15,023 | `grain/ci` |
| `cifault` | 275 | `grain/ci` |
| `grit` | 203 | `grain/ci`: GitHub Actions jobs in disposable VMs. *Placed by this plan* |
| `repodecl`, `declschema` | 1,946, 1,022 | repo rules: triggers, schedules, templates, suites |
| `trustboundary` | 224 | repo rules: which changes touch grain's trust boundary. *Placed by this plan* |
| `staterepo`, `backup` | 12,004, 407 | state export (`grain/config`) |
| `statemachine` | 1,446 | the task lifecycle runtime that `lifecycle` drives. Follows the task-lifecycle half of `lifecycle` into grain. *Placed by this plan* |
| `mcp` (grain-tool half) | most of 14,807 | grain's own tools (`propose_task`, `wait_for_checks`, merge-queue and CI tools, memory, chat, issues), served by a grain component as a second MCP server and granted to grain's agents |
| `ui` | 43,890 | grain's views, as panes served by grain's components ([`shibu.md` 2.6.6](shibu.md#266-agent-served-panes)). The generic parts (agent chat, secrets forms, system views) seed `/sys/ui`, the distribution's UI. See [5.4](#54-others-worth-naming) |
| `capability/console`, `memory`, `selfdebug`, `selfrepair`, `selfupgrade`, `grainlog`, `grainci`, `bootstrap`, `deploymenthealth`, `dbaccess`, `pipelinecontrol`, `quarantine`, `readissues`, `releasenotes`, `teambroadcast`, `admincaps` | — | grain's GRANT switches: grants on endpoints of grain's own components. `selfrepair` (a shell on the host) has no place once grain is untrusted; see [6](#6-open-questions) |
| `model` (grain half) | most of 46,974 | grain's state: tasks, runs, PRs, branches, templates, schedules, suites, merge batches, memory, releases. See [5.1](#51-pkgmodel-one-schema-two-owners) |

### Shared libraries

These are small, dependency-free helpers with no layer of their own. In
Phase 1 they stay where they are and both sides import them. In Phase 3
the kernel takes a copy, so that the Shibu repository depends on
nothing in grain's.

| Package | Lines | Note |
| --- | --- | --- |
| `apierror` | 123 | what an HTTP API may say about a failure |
| `procgroup` | 100 | cancellation of a process tree |
| `safepath` | 87 | path containment |
| `version` | 114 | build identity. Each side stamps its own |

### Unplaced and split packages

The judgement calls, for bsalmon to confirm:

- **`lifecycle`** is in the brief's kernel list, but it does two jobs.
  Image assembly (a deployment's container half plus a repository's
  guest disk) is kernel image work. Serving a repository's **task
  lifecycle definition** from a sandbox, through `statemachine`, is
  grain's. This plan splits it that way.
- **`metrics`** is in the brief's kernel list as `watch`/`cost`, but
  most of it measures **task** throughput and stage latency, which are
  grain concepts. This plan puts resource and cost accounting in the
  kernel and leaves task metrics in grain.
- **`secretsrepo`** as a storage backend: needs git in the kernel,
  which the `/sys` spec design avoids. Assess before Phase 3.
- **`actionscache`**: distribution package cache, or grain CI. Leaning
  grain CI.
- **`console`**, **`hostinit`**, **`upgrade`**, **`hosttop`**,
  **`systemlog`**, **`gcpsetup`**, **`grit`**, **`trustboundary`** and
  **`statemachine`** are not in the brief. Their placement above is
  this plan's.
- **`capability/selfrepair`** and **`capability/dbaccess`** give a task
  the host or the database. Once grain is an untrusted component, the
  host and the kernel's database are not grain's to give. They need a
  kernel-side equivalent (an operator break-glass) or have to go.

No package was left without a placement.

### `cmd/`

- `cmd/grain` is today's single binary. Its daemon wiring
  (`daemon.go`, `statemanager.go`, `daemon_iap.go`) splits along the
  same lines as the packages: in Phase 1 it constructs `pkg/shibu`
  and hands it to grain's code; in Phase 3 it becomes two binaries.
- `cmd/grain-relay` and `cmd/grain-connect` go with `relay` and
  `tunnel` into the kernel. (`cmd/grain-tunnel-gateway` was removed in
  grain/task-2099; the relay is the one remote-access edge.)
- `cmd/checkpost` goes with `checkpost` into `/sys/forge`. `cmd/grit`
  stays with grain CI.
- `cmd/ghcr-prune` is a grain maintenance tool and stays with grain.

---

## 3. Phases

Strangler-style. grain lands about 95 merges a day on itself, so there
is no stop-the-world rewrite. Each phase is many small pull requests,
each of which leaves `main` working.

### Phase 0: documentation

This document and the 1750 revision of [`shibu.md`](shibu.md).

**Exit criteria:** both are merged and bsalmon has confirmed or changed
the calls in [Unplaced and split packages](#unplaced-and-split-packages).
[`shibu.md` F30](shibu.md#f30-component-ui-panels), the other call this
used to wait on, was decided in task 1771.

### Phase 1: carve the kernel and distribution behind `pkg/shibu`

This is Option B's shape ([`shibu.md` 7.3](shibu.md#73-background-the-three-options-weighed)),
used as a waypoint.

- Add `pkg/shibu`, an in-process Go API whose shape follows the
  kernel's operations ([`shibu.md` section 3](shibu.md#3-operations),
  specified in [`shibu-spec/kernel-api.md`](shibu-spec/kernel-api.md)):
  create and delete VMs through an allocator, put and use secrets,
  start an agent session, proxy and audit.
- Move the kernel and distribution packages behind it, one at a time.
  Packages move under `pkg/shibu/...` (kernel) and
  `pkg/shibu/sys/...` (distribution), or keep their paths with an
  import rule that only `pkg/shibu` may call them.
- grain's code calls `pkg/shibu`, in-process, instead of the packages
  directly.
- Define each distribution contract as a Go interface in
  `pkg/shibu/sys`, following
  [`shibu-spec/extension-points.md`](shibu-spec/extension-points.md), with its conformance suite as an ordinary Go test
  package that runs against today's implementation.
- **No behaviour change.** Capabilities, the ledger, tokens and the
  host-side runner keep working exactly as they do; they are only
  reached through the new API.

**Exit criteria:**

- Nothing outside `pkg/shibu` imports a kernel or distribution package.
  A lint check enforces it in grain CI.
- Each of `model`, `runner`, `git`, `credential-proxy` and `pkgcache`
  has a contract interface and a conformance suite that passes against
  the current implementation.
- The kernel's state (VMs, secrets, ledger, leases, access accounts)
  is in its own tables behind `pkg/shibu`, even if still in the same
  database ([5.1](#51-pkgmodel-one-schema-two-owners)).
- Throughput, CI duration and merge-queue latency are within noise of
  where they were before the phase started.

### Phase 2: grants and allocators replace capabilities and the ledger

Still in-process.

- Each team becomes a namespace under `grain/`, and its `MaxWorkers` and
  `MaxCIJobs` become allocator grants from the root VM allocator,
  which takes over `model.Ledger`'s accounting and queueing.
- Each task's sandbox is created through its team's allocator grant,
  in a namespace for the task.
- Capabilities become grants, following the
  [capability rows of the package map](#into-the-standard-distribution):
  proxy scopes become grants on `/sys/git` and `/sys/model`, MINT
  capabilities that place keys move behind `/sys/cred` endpoints, and
  GRANT switches become grants on grain's own endpoints. A capability
  approved on a proposal becomes a grant made when the task starts.
- Per-sandbox tokens give way to caller identity where the backend can
  already guarantee origin; elsewhere a token stands in for it, behind
  the same `pkg/shibu` check, until the backend can
  ([`shibu.md` F25](shibu.md#f25-the-backend-carries-the-whole-identity-model)).
- `/sys/model` starts metering, and task budgets become grant limits on
  it.
- The `credential-proxy` contract gets its first implementations:
  GitHub App, GCP service account, HMAC ingress, and the streaming
  registry proxy ([`shibu.md` F9](shibu.md#f9-secrets-have-no-shibu-home)).

**Exit criteria:**

- `model.CapabilityProvider` and `model.Ledger` are gone; nothing
  outside `pkg/shibu` decides who may hold what, or how much.
- No sandbox receives a raw credential. `gcp-key`, `gemini-key`,
  `registry-push`, `package-publish` and `github-sandbox` all work
  through `/sys/cred`.
- `cost(grain/<team>)` agrees with today's per-team usage reports.
- Every grain credential is in the kernel secret store, and only `/sys`
  components hold `read` grants on it.

### Phase 3: split the process; grain becomes components

- Shibu's kernel and distribution run as their own process or service,
  and `pkg/shibu`'s in-process API becomes a client of its versioned
  API ([`shibu.md` 2.2.20](shibu.md#2220-api-versioning-and-idempotency-keys)).
- grain's components run as Shibu agents and services in namespaces:
  `grain/lead` (backlog, dispatcher, approvals), `grain/ci`, `grain/mq`,
  and the others in [the package map](#grain-components).
- grain's UI views become panes of grain's components in the Shibu UI,
  each on its own origin
  ([`shibu.md` 2.6.6](shibu.md#266-agent-served-panes)).
- **Shibu moves to its own repository** at this point, not before.
  Until then it changes with grain in this repository, where the
  merge queue and CI already test both together.

**Exit criteria:**

- grain runs with no host access and no credential: it is an untrusted
  component, reaching everything through grants.
- The Shibu repository builds, tests and releases on its own, and
  grain pins a released Shibu API version.
- A second, non-grain agent (the `/bootstrap` agent of
  [`shibu.md` 4.1](shibu.md#bootstrap)) runs on the same Shibu without
  grain.
- The standard distribution can be replaced component by component
  through the `/sys` spec's staged swap
  ([`shibu.md` 2.3](shibu.md#23-the-standard-distribution-and-the-sys-spec)).

---

## 4. Invariants that hold throughout

Every pull request in every phase keeps these:

- **The merge queue and CI never stop.** grain builds grain. Each change
  is small enough to land through the normal queue, and a change that
  breaks the queue is reverted, not fixed forward.
- **No flag day.** There is never a release where everything switches
  at once. Old and new paths run side by side behind `pkg/shibu`, with
  the old path removed only once the new one has carried production
  traffic.
- **No credential widens.** No step gives any sandbox, component or
  agent a credential it did not already have. Credentials only move
  *behind* proxies.
- **The trust boundary is not relaxed to make a step easier.**
  Changes to it go through the same review `trustboundary` already
  enforces.
- **State stays exportable.** `grain/config` keeps exporting the whole
  database, kernel tables included, until Phase 3 gives the kernel its
  own export.
- **Rollback is a revert.** Until Phase 3, every phase can be undone by
  reverting its pull requests; nothing depends on a one-way data
  migration that has no reverse.
- **Deployments other than this one keep working.** Single-host
  deployments (`grain init`) and Kubernetes deployments both stay
  supported through every phase.

---

## 5. Known hard parts

### 5.1 `pkg/model`: one schema, two owners

`pkg/model` is about 47,000 lines. Shibu's state and grain's state share
one schema and one store today, and splitting it is most of the work.

- **Shibu's state:** VMs and sandbox usage (`sandboxusage`,
  `sandbox_backend`, `standing_sandbox`, `sandbox_guest_cache`), the
  ledger (`ledger`, `limits`), capabilities and leases (`capability`,
  `capability_teams`, `executor_token`),
  access accounts (`access_account_store`, `user`), secrets references,
  and a sandbox's network access (`network_access`), which becomes
  egress policy.
- **grain's state:** tasks and runs (`task`, `ending`, `nothing_needed`,
  `supersede`, `suspended_session`), PRs and branches, CI runs, merge
  batches, templates, schedules and suites, memory, releases, custom
  fields, boards.
- **Shared, and the hard part:** `team` (a team is both a grain team
  and, after Phase 2, a namespace with allocator grants), `store.go`
  (8,200 lines of one store interface), `schema.go` (one migration
  history), and foreign keys from grain rows to sandbox and capability
  rows.

The plan: in Phase 1, draw the line in the schema without moving the
data: kernel tables get their own store interface behind `pkg/shibu`,
and grain rows refer to kernel objects only by stable ID, never by join.
In Phase 2 the team/namespace overlap is resolved by making the
namespace the source of truth for limits and the grain team a label on
it. In Phase 3 the kernel tables move to the kernel's own database.

### 5.2 Credentials: grain becomes untrusted

Once grain is a component, it is untrusted. Its GitHub App key, GCP
keys, model keys and registry credentials must move behind distribution
credential proxies, which receive them from kernel secrets through
`read` grants
([`shibu.md` 2.2.7](shibu.md#227-credentials-only-behind-proxy-endpoints)).
grain can't take a `read` grant itself: that would make its namespace
credential-holding, and the kernel refuses to create agents in or under
a credential-holding namespace.

That puts the credential-proxy extension of
[`shibu.md` F9](shibu.md#f9-secrets-have-no-shibu-home), now the
`/sys/cred` contract, **on the critical path for Phase 3**. In
particular:

- GitHub App: `/sys/git` and `/sys/forge`, the GitHub-backed
  implementations, hold the App key through `read` grants listed in the
  `/sys` spec. grain's pull-request, review and check calls go to
  `/sys/forge` and never reach the key. Any other GitHub API call grain
  still makes goes through `/sys/cred`'s GitHub kind, which mints
  per-request installation tokens.
- GCP: service-account tokens minted per request, replacing the keys
  `gcp-key` places in sandboxes today.
- Inbound webhooks: HMAC verification in front of grain's public
  endpoint.
- The registry: a streaming proxy for image pushes, replacing
  `gitproxy`'s registry-login path.

This is why Phase 2 builds `/sys/cred` and moves every capability that
places a raw key, before Phase 3 needs it.

### 5.3 `pkg/mcp`: tool server and grain tools in one package

`pkg/mcp` (about 15,000 lines) mixes two things:

- the **tool server** that runs commands, files and network in the
  sandbox (`exec_conn`, `exec_stream`, `docker_exec_runner`,
  `kubectl_exec_runner`, `container_exec_runner`, `sandbox_tools`,
  `managed_http`), which
  becomes the distribution's in-VM tool server under the kernel's
  tool-server contract;
- **grain's own tools** (`grain_ci`, `merge_queue_tool`, `chat_tool`,
  `memory_tool`, `issue_tools`, `metrics_tools`, `task_tools`,
  `open_pull_request_tool`, `git_reverter`, …), which become a grain component's MCP server,
  granted to grain's agents.

Today both are served as one server over one connection, and the
harness is configured with exactly one MCP server. The split needs the
harness contract to allow a second, granted MCP server next to the
tool server ([`shibu.md` 4.1](shibu.md#harness-interface)), and grain's
tools to reach grain's state through grain's API rather than the
in-process store. Do it in Phase 1, by moving the files into two
packages, before the process split makes it urgent.

### 5.4 Others worth naming

- **`pkg/ui`** (about 44,000 lines) is one React app with grain's views
  and the generic ones mixed. It splits into `/sys/ui`, the
  distribution's UI, and grain's panes
  ([`shibu.md` 2.6](shibu.md#26-the-ui-and-the-sysui-conventions)).
  Today grain's views share the app's session and call grain's API
  in-process; as panes, each runs on its own origin, calls only its
  component's endpoints as the viewer, and reaches the UI only through
  the bridge. Views that enter credentials stay in `/sys/ui`'s forms.
  Every action they offer needs a CLI command too.
- **`pkg/orchestrator`** (about 40,000 lines) holds the dispatcher, the
  approvals logic and the merge queue together. It stays in grain, but
  it splits into `grain/lead` and `grain/mq` components in Phase 3.
- **The host-side runner.** grain runs `claude -p` on the controller
  host today. In Phase 3 it runs in `/sys/runner`'s harness VMs, which
  is a change in where credentials and sessions live
  ([`shibu.md` F26](shibu.md#f26-the-shared-runner-is-a-high-value-target)).
- **Self-management capabilities** (`selfrepair`, `selfupgrade`,
  `dbaccess`) give tasks the host. See [6](#6-open-questions).

---

## 6. Open questions

- What replaces `selfrepair`, `selfupgrade` and `dbaccess` once grain is
  untrusted: a kernel operator break-glass that an agent can be granted
  for a lease, or nothing.
- Whether `secretsrepo` is a storage backend or an export target.
- ~~Whether the kernel's database in Phase 3 is the same SQLite/Postgres
  choice grain has today (`pkg/model/sqlite`, `pkg/model/postgres`).~~
  Decided for v1 (task 1904): the kernel keeps its state in an embedded
  SQLite database inside the kernel process, behind the `state/v1`
  contract, so a Postgres-compatible store can replace it later without
  API changes ([`shibu.md` 2.2.27](shibu.md#2227-kernel-state-store)).
- How long Phase 1's import rule can hold with about 95 merges a day,
  and whether it needs to land first, as a lint that starts in warning
  mode.
