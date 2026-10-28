# Shibu: namespaces, grants and allocators for agents

> Superseded twice: first by the earlier Silo project ([bwsalmon/silo](https://github.com/bwsalmon/silo/blob/main/docs/silo/README.md)), and then by the Grain and Silo design, whose hub connection grain has since removed ([hub-removed.md](hub-removed.md)). This document is kept for reference.

## Status

Design, no code. This records the design bsalmon and the director worked
out in the director chat (grain task 1550), written up in task 1589, and
revised after a second director chat (grain task 1679, 2026-10-03).

The 1679 revision made `grant` the one general primitive
([Grants](#grants)). It also settled how a VM and its processes get
their Shibu identity, and decided most of the findings the first
version left open. It was written up in task 1741.

An addendum from the same chat, written up in task 1746, makes
**agents core**: a composite call, `create_agent`, built from other
core pieces, with no agent allocator
([4.1](#41-agents-a-kernel-composite)). The addendum also brings a harness
interface with adapters for existing agent CLIs, two placements for a
harness (`same-vm` and `split`), a `messages` endpoint on every agent,
one namespace per human, a metered root `/model`, and a minimal
write-only secret store into the core
([F17](#f17-the-agent-allocator-overlaps-the-vm-allocator-and-the-credential-rule)).

A third revision from the same chat, written up in task 1750, does
four things:

- It splits Shibu into **three layers**: a kernel, a standard
  distribution installed under `/sys/`, and additional components
  ([Layers](#layers-kernel-standard-distribution-additional-components)).
  Where it differs from the 1746 addendum, it wins. In particular, the
  metered model proxy and the harness runner move out of the core into
  the distribution, as `/sys/model` and `/sys/runner`.
- It restates the credential rule as **a VM that holds a credential
  never runs untrusted code**
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)).
- It adds the **`/sys` spec**, which says which implementation of each
  distribution contract runs, and how root edits it
  ([2.3](#23-the-standard-distribution-and-the-sys-spec)).
- It **decides section 7**: grain is refactored to run on top of Shibu
  ([7.2](#72-the-decision-grain-runs-on-shibu)). The migration plan is
  in [`shibu-migration.md`](shibu-migration.md).

A fourth revision from the same chat, written up in task 1752, settles
**non-HTTP services**, databases above all: unmodified Postgres with
unmodified clients. It replaces 1751, which recorded only the
native-port design.

- **In v1**, a non-HTTP service runs over TCP tunnelled inside an HTTP
  endpoint. The web proxy accepts WebSocket and `CONNECT` upgrades, and
  a small shim in the standard distribution runs at each end
  ([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)).
  Revoking a grant closes the tunnels opened through it
  ([Grants](#grants)). The kernel needs no L4 work.
- **Beyond v1**, native `tcp` endpoints are added, as an upgrade in
  transparency and performance
  ([2.4](#24-beyond-v1-native-port-endpoints)). Nothing in v1 changes
  for them.

A fifth revision from the same chat, written up in task 1762, settles
tokens and how a secret is read. Where it conflicts with earlier text,
it wins.

- **v1 has no attenuated tokens.** Grants and ambient identity leave
  them nothing to do that a VM boundary or a grant doesn't do better
  ([2.2.11](#2211-no-attenuated-tokens)). A bearer grant is noted
  beyond v1, to be added only if a real case appears
  ([2.5](#25-beyond-v1-bearer-grants)).
- **A secret has a grantable `read` op**, and a `read` grant is the
  only way to read one ([2.2.24](#2224-secret-store)).
- **Credential-holding namespaces are kernel-enforced.** A namespace
  holding a secret-`read` grant can't host an agent's tools, create
  agents, or widen its egress
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). The tenth
  and twelfth revisions remove these refusals; the mark stays, as
  information
  ([A.17](#a17-trusted-and-untrusted-vms-and-runners-and-the-credential-rule)).

A sixth revision from the same chat, written up in task 1763, settles
lifecycle. Where it conflicts with earlier text, it wins.

- **Namespaces are deleted, never exited.** A namespace is a container
  for VMs and agents. The things in it exit; the namespace is deleted,
  explicitly, by its lease, or by the lifecycle policy it was created
  with (`delete=explicit | when-idle | on-lease`)
  ([2.2.6](#226-leases-and-ttls)).
- **VMs and agents carry exit status.** Each has a status and, once it
  has exited, a reason ([2.2.17](#2217-exit-status-and-reason)). An
  agent ends by calling `exit(status, reason)`, or by being stopped.
- **Results are kept by deposit areas** (`keep=`), and by `move`
  during the **`deleting`** grace state
  ([2.2.4](#224-lifecycle-and-keeping-results)).

A seventh revision from the same chat, written up in task 1765, closes
open questions 6.5–6.7. Where it conflicts with earlier text, it wins.

- **Anyone can implement an allocator.** "Allocator" is a contract
  (`create`, `delete`, `usage`, `list`), not a kernel-only object. Any
  namespace can implement one, and grants work on it as on a kernel
  allocator. Only root changes the kernel's own allocators
  ([Component allocators](#component-allocators)).
- **Backends are kernel driver plugins.** A backend driver contract
  covers VMs, volumes, the per-VM network and placement classes. v1
  ships one driver, docker + granule. Drivers are where root allocators
  come from ([Backend drivers](#backend-drivers)).
- **Git and forge are separate distribution contracts.** `/sys/git`
  serves git, from GitHub or hosted on Shibu. `/sys/forge` serves pull
  requests, reviews, checks and merges
  ([2.3](#sysgit-and-sysforge)).

An eighth revision, decided in a later director chat (grain task 1766,
2026-10-03) and written up in task 1771, takes **the UI out of the
kernel**. Where it conflicts with earlier text, it wins.

- **The UI is a replaceable component.** Everything can be done
  through the API, and the UI is a principal acting as a namespace
  through it, so the UI is `/sys/ui`, an optional distribution
  component behind a `ui` contract. Its conformance rule is **CLI
  parity**: anything a UI can do, the CLI can do
  ([2.6.1](#261-the-ui-is-a-replaceable-component),
  [CLI parity](#cli-parity)).
- **Agents appear in any UI through conventions**, never by talking to
  a UI: typed `messages`, status labels on the agent object, an
  `artifacts` endpoint, and requests that stay in the API
  ([2.6](#26-the-ui-and-the-sysui-conventions)).
- **Agents can serve panes.** Any agent or component can add a pane to
  the UI from its own endpoint. Seeing one is a grant, and every pane
  is isolated on an origin of its own namespace, inside a frame the UI
  draws, with a narrow bridge and no credential entry
  ([2.6.6](#266-agent-served-panes)). This decides
  [F30](#f30-component-ui-panels), and [4.4](#44-a-pane-served-by-an-agent)
  walks through it.

A ninth revision, decided in the same director chat (grain task 1766,
2026-10-04) and written up in task 1870, takes **warm pools out of the
kernel**. Where it conflicts with earlier text, it wins.

- **`adopt` is a kernel primitive.** A component allocator answering a
  `create` may satisfy it with a VM, volume or snapshot clone it
  already owns. The kernel re-homes that resource into the create's
  target and moves its charge to the grant the create named
  ([2.2.2](#222-caps-reservations-and-adoption)). This is the hand-off
  `move` can't make, because `move` works only within the caller's
  subtree.
- **The `warm-pool` allocator kind is gone.** A warm pool is an
  optional standard-distribution component built on `adopt`
  ([2.2.2](#222-caps-reservations-and-adoption)). Caps and
  reservations stay in the kernel.
- **`requests` is a convention, and the CLI says so.** `shibu requests`
  and `shibu approve` are convention commands, not kernel ones
  ([CLI parity](#cli-parity)).

A tenth revision, decided in the same director chat (grain task 1766,
2026-10-04) and written up in task 1884, separates **where an agent's
harness runs from where its tools run**, and makes trust a matter of
each owner's grants. Where it conflicts with earlier text, it wins.

- **No special sandbox; any VM can expose `mcp`.** A VM may serve a
  standard `mcp` endpoint with the VM tools (`exec`, `read_file`,
  `write_file`, `edit_file`, `list_dir`), granted by op like any
  endpoint. The agent tools (`send_message`, `ask`, `exit` and the
  rest) move to an **agent control MCP** the kernel serves from the
  agent's own endpoints, so an agent with no VM at all is valid. A
  grant of `exec` lends the VM's identity and is the strongest grant op
  ([VM `mcp` and the agent control MCP](#vm-mcp-and-the-agent-control-mcp)).
- **`runs_in` vs `tools`.** An agent's `runs_in` is the runner endpoint
  its harness runs in, which fixes the harness's identity; its `tools`
  are the VM `mcp` endpoints it holds grants on, zero or more.
  `same-vm` and `split` are now just two combinations of the two
  ([Placements](#placements)).
- **`runner/v1` is a contract any VM can serve.** A runner launches,
  resumes, stops and reports harness sessions, and says which adapters
  it can run and how many sessions it has free. It is the only way a
  harness is launched ([Runners](#runners)).
- **The model is a launch parameter.** There is no kernel model
  connection. Who may use which models is a grant on a runner (op
  `launch`), and how the runner reaches the model is its own
  configuration. `/sys/model` is one way for a runner to reach models
  without holding a key, no longer the definition of model access.
  Subscription and OAuth logins can't be proxied
  ([2.2.23](#2223-model-access-and-budgets)).
- **Trust is a matter of perspective.** The "trusted vs untrusted VM or
  runner" distinction and the kernel rule "a VM that holds a credential
  never runs untrusted code" are gone. Each owner decides where its own
  things go, through grants; the core enforces grants and identity on
  every call and relies on no VM or runner to protect it
  ([Trust](#trust), [2.2.7](#227-credentials-only-behind-proxy-endpoints)).

An eleventh revision, decided in the same director chat (grain task
1766, 2026-10-04) and written up in task 1885, **simplifies the
namespace lifecycle to a lease and a state**. Where it conflicts with
earlier text, it wins.

- **No lifecycle policy.** A namespace is deleted by `delete` or when
  its `lease` expires, and by nothing else. `delete_policy`
  (`explicit | when-idle | on-lease`) is gone
  ([2.2.6](#226-leases-and-ttls)).
- **Idle deletion is a policy outside the kernel.** "Delete when every
  VM and agent has exited" is built on `watch` + `delete`: by the
  creator itself, or by an optional standard-distribution **reaper**
  component. `armed` and the `idle` cause are gone; job-style
  namespaces carry a lease or are deleted by their parent or a reaper
  ([2.2.6](#226-leases-and-ttls)).
- **No grace.** `delete` and lease expiry stop every VM with its usual
  stop signal and timeout, end agents, drop queued creates, revoke
  received grants and delete everything. `deleting` is brief and
  non-configurable; there is no rescue by `move` during it. Results are
  kept by deposit areas declared up front, or by the parent moving or
  snapshotting what it wants **before** it calls `delete`
  ([2.2.4](#224-lifecycle-and-keeping-results)).
- **A lease warns through `watch`.** The event `lease.expiring` fires a
  lead time before a lease expires; the lead time is set on the watch,
  not on the namespace ([2.2.6](#226-leases-and-ttls)).

A twelfth revision, decided in the same director chat (grain task
1766, 2026-10-04) and written up in task 1886, **takes boundaries out
of v1** and records the policy model that replaces them later. Where it
conflicts with earlier text, it wins.

- **No boundaries in v1.** `set_boundary`, the namespace's `boundary`
  and `effective_boundary`, the `boundary_violation` error and the
  policy keys (`grants_out`, `grants_in`, `require_lease`, `max_lease`,
  `allocators_from`) are gone ([A.22](#a22-boundaries)). F33 is open
  again ([F33](#f33-a-component-allocator-can-misreport-usage)).
- **`regrant=false` is the default.** A received grant can be used but
  not passed on unless its granter set `regrant=true`, and a re-grant
  always narrows. Objects a namespace owns by the tree are unaffected.
  In v1 this is what stops a subtree passing on what it received
  ([Grants](#grants)).
- **Policy, beyond v1.** Allow is grants only, which may later carry
  conditions. Deny is predicates attached to a subtree, evaluated per
  call in the web proxy, failing closed. Policy only subtracts
  ([2.7](#27-beyond-v1-policy)).
- **No `read` grant is refused for what the grantee runs.** The
  credential-holding mark is information only, with no egress rule tied
  to it; an owner asks the exposure query before granting `read`
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). The fifth
  revision's kernel-enforced credential-holding namespaces, already
  restated by the tenth, are gone entirely.

A thirteenth revision, decided in the same director chat (grain task
1766, 2026-10-04) and written up in task 1887, **tightens the kernel
API** to the smallest one that loses no generality. Where it conflicts
with earlier text, it wins. The kernel is now: namespaces, VMs,
volumes, endpoints, grants, secrets, leases, labels, `move`, `adopt`,
`watch`/`audit`/`cost` and `batch`. Everything else is a
standard-distribution component built on those.

- **Egress is a grant.** The namespace egress policy (`internet`,
  `endpoints`, `none`), egress objects, the `egress` allocator kind,
  `set_egress` and the effective-egress inheritance rules are gone.
  Outside access is a grant on the standard-distribution `/sys/egress`
  endpoint, a forward proxy, with a `hosts` limit; no grant, no outside
  access. Every VM can still reach `shibu.internal` and its own
  namespace's endpoints ([2.2.13](#2213-egress-policy), [A.23](#a23-egress-policy-egress-objects-and-the-egress-allocator)).
- **Five endpoint kinds.** The kernel keeps `http`, `volume`,
  `allocator`, `control` and `secret`. `messages`, `artifacts`, `job`,
  `agent`, `model`, `public`, `resource`, `mcp` and `runner` are `http`
  endpoints served by the kernel or a component, granted by HTTP
  methods, `read`/`write`/`tunnel` and `paths`, plus whatever ops and
  limits their contract defines ([Endpoint](#endpoint), [A.24](#a24-endpoint-kinds-beyond-the-five)).
- **Simpler secrets.** `use`, kernel-issued bound `read` grants,
  `bound_reads`, `retire_secret_version` and `oauth_start` are gone. A
  secret is written with `put_secret` and read through an ordinary
  `read` grant from its owner, usually leased with `regrant: false`.
  Rotation is writing a new value; the store keeps the previous one
  readable for a fixed short overlap. Logins are obtained by the runner
  or the UI and written with `put_secret`, and `create_agent`'s
  `credential` is a reference the runner reads through its own grant
  ([2.2.24](#2224-secret-store), [A.25](#a25-use-grants-bound-reads-version-retirement-and-the-kernel-login-flow)).
- **Public exposure is a component.** The `public-endpoint` object, its
  allocator and its `endpoints` dimension are gone. Exposure is granting
  an endpoint to the standard-distribution `/sys/ingress`, which serves
  the public URL and decides who may reach it. The kernel's resource
  kinds are VM and volume, with images and snapshots as volumes
  ([2.2.12](#2212-pre-approved-publish), [A.26](#a26-the-public-endpoint-kind-and-its-allocator)).
- **Trimmed allocator limits.** `cap`, `into`, `shapes`, `images` and
  `budget` remain. Per-dimension `reserve` is replaced by a boolean
  `reserved` on the grant (the whole cap set aside), so every limit
  narrows; `kinds`, `classes`, labels-on-create and percentage limits
  are gone; every budget is `{unit, per_day | total}`
  ([Quota](#quota), [2.2.2](#222-caps-reservations-and-adoption),
  [2.2.9](#229-time-based-budgets), [A.27](#a27-allocator-limits-reserve-kinds-classes-labels-and-percentages)).
- **Fewer operations.** `snapshot` and `clone` are `create` with
  `spec.from`; `usage` is part of `get` and `cost`; the `control`
  endpoint's ops are `read` and `write`; `apply` is a
  standard-distribution reconciler built on the atomic kernel `batch`,
  and reconciles `/sys` too ([2.2.8](#228-orchestration-surface),
  [A.28](#a28-snapshot-clone-usage-per-operation-control-ops-and-apply-in-the-kernel)).
- **`create_agent` leaves the kernel.** It is create namespace (with
  `agent`), grants, then the runner's `launch`: a standard-distribution
  helper (library, CLI and `/sys/agents`). The kernel keeps the agent
  object, its status labels and exit status, its standard endpoints and
  the session binding ([`create_agent`](#create_agent),
  [A.29](#a29-create_agent-as-a-kernel-composite)).

A fourteenth revision, decided in the same director chat (grain task
1766, 2026-10-04) and written up in task 1904, **sets v1 defaults for
the remaining open questions and proposed findings**, and decides
where the kernel keeps its own state. Where it conflicts with earlier
text, it wins.

- **Kernel state store.** v1 keeps the kernel's state in an embedded
  SQLite database inside the kernel process, behind a replaceable
  contract, `state/v1`, so a Postgres-compatible store can replace it
  later without API changes. Running the store as a bootstrap root VM
  is recorded as a future option and left open
  ([2.2.27](#2227-kernel-state-store)).
- **Open questions 6.8–6.13 are decided for v1**
  ([6](#6-open-questions)). Grants on services are paths and methods
  only (6.8). A front-door caller's assertion names the person, and an
  anonymous caller through `/sys/ingress` is marked anonymous (6.9). A
  vendor limit is a session pause, with no scheduler (6.10). The kernel
  keeps `messages` history with the agent object, capped per agent
  (6.11). `runner/v1` requires container isolation or better, and a
  `launch` grant can require more (6.12). `limits={usage: reported}`
  on a `launch` grant accepts a runner's reports as charges (6.13).
- **Findings F21–F24, F26 and F32 are decided for v1**
  ([5](#5-findings)): audit and the exposure query for leaks (F21), a
  grant-chain index for cascading revokes (F22), last-use filters and
  unused grants in `cost` and `audit` (F23), no `regrant` depth limit
  (F24), the isolation floor of 6.12 (F26), and the reaper re-checking
  idleness at the moment it deletes (F32).

The body of the document states only the current design. Ideas that
these revisions considered and dropped, and why, are in
[Appendix A](#appendix-a-ideas-considered-and-dropped).

The document separates three kinds of statement:

- **Decided.** Section 2 (the model) and section 3 (the operations).
  They are stated as the model, not as proposals. Section 4's
  walk-throughs use only decided parts.
- **Findings.** Section 5 lists the places where the walk-throughs
  found the model awkward, ambiguous or missing something. Each finding
  has a status line saying what became of it. Since the fourteenth
  revision (task 1904) **none is still a proposal**: F21–F24, F26 and
  F32, the last ones proposed, are **Decided for v1**, and each keeps
  its earlier analysis under its status line. What remains undecided
  in section 5 is F33 (**Open**, waiting on policy beyond v1), F11
  (**Punted**), and the risks recorded without a conclusion, F28 and
  F31. A few decided findings still carry *suggested* extras that are
  not adopted (F34's write-off, F35's periodic origin test).
- **Open.** Section 6 lists questions that were raised and deliberately
  left unanswered. Every one of them, 6.1–6.13, has since been answered
  or decided for v1, and each is kept in place so that references
  resolve. What is still open is marked **Open** where it lives: the
  kernel state store as a bootstrap root VM
  ([2.2.27](#2227-kernel-state-store)), F33, and the beyond-v1
  sections (2.4, 2.5, 2.7).

Section 7 looks at how Shibu relates to grain's own code, and records
the decision that grain runs on top of Shibu. The three ways the two
could fit together, weighed before that decision, are kept as
background (7.3).

Every section in 2, 3 and 4 says which layer it belongs to, in an
italic line under its heading. Findings and open questions are given a
layer in the tables at the top of sections 5 and 6.

The API calls in this document use an illustrative notation
(`create(...)`, `grant(...)`). The design fixes the operations and what
they mean. It does not fix their names, wire format or argument order.

## Contents

1. [Motivation](#1-motivation)
2. [The model](#2-the-model)
   - [Layers: kernel, standard distribution, additional components](#layers-kernel-standard-distribution-additional-components)
   - [2.1 Kernel model](#21-kernel-model-decided)
   - [2.2 Factory-driven additions](#22-factory-driven-additions-decided)
   - [2.3 The standard distribution and the `/sys` spec](#23-the-standard-distribution-and-the-sys-spec)
   - [2.4 Beyond v1: native port endpoints](#24-beyond-v1-native-port-endpoints)
   - [2.5 Beyond v1: bearer grants](#25-beyond-v1-bearer-grants)
   - [2.6 The UI and the `/sys/ui` conventions](#26-the-ui-and-the-sysui-conventions)
   - [2.7 Beyond v1: policy](#27-beyond-v1-policy)
3. [Operations](#3-operations), including [CLI parity](#cli-parity)
4. [Walk-throughs](#4-walk-throughs)
   - [4.1 Agents: a kernel composite](#41-agents-a-kernel-composite)
   - [4.2 Full software factory](#42-full-software-factory)
   - [4.3 Repo (tentative)](#43-repo-tentative)
   - [4.4 A pane served by an agent](#44-a-pane-served-by-an-agent)
5. [Findings](#5-findings)
6. [Open questions](#6-open-questions)
7. [Relationship to grain](#7-relationship-to-grain)

[Appendix A: Ideas considered and dropped](#appendix-a-ideas-considered-and-dropped)

The migration plan from grain to Shibu is a separate document,
[`shibu-migration.md`](shibu-migration.md).

The API specification derived from this design, which fixes the names
and shapes this document calls illustrative and specifies the contracts
for every pluggable component, is [`shibu-spec/`](shibu-spec/README.md).

---

## 1. Motivation

An agent that does real work soon needs infrastructure of its own: a
sandbox to run code in, a disk to keep a checkout on, somebody else to
review its work, a CI run, a preview URL, a model budget for a helper.
Today it can only get these by asking the system that started it.
Whatever that system thought to offer is all the agent can have, and
whatever it hands out is hard to take back.

grain is a worked example of the problem. Everything a grain run can
hold is something grain's daemon specifically arranged:

- a capability (`pkg/capability/*`) that places a credential or mints a
  token;
- a sandbox shape charged to the deployment-wide ledger
  (`model.Ledger`);
- a team's worker and CI counts (`Team.MaxWorkers`, `MaxCIJobs`);
- a git or model proxy token (`pkg/gitproxy`, `pkg/modelproxy`);
- a sub-task, which reaches the queue as a *proposal* and needs a human
  or an auto-merge rule to approve it.

Each of these was built separately and has its own idea of who may do
what, for how long, and at whose expense. None of them lets an agent
hand part of what it holds to a helper it starts.

Shibu is the general form of that machinery. It is a lightweight
infrastructure layer that lets agents **delegate permissions** and
**build infrastructure** from inside their own VMs, under quota they
already hold and can grant onward, and without any credential entering
a VM that runs their tools
([2.2.7](#227-credentials-only-behind-proxy-endpoints)).

It has three parts:

- **An API** that agents call from inside VMs. Every call is made *as* a
  namespace, which Shibu knows from where the call came from
  ([Identity](#identity)).
- **A web proxy** that serves endpoints by name and enforces visibility,
  grants and egress. Every request from a VM to anything goes through
  it.
- **A UI** for people to manage and talk to agents. The UI is not an
  admin backdoor: it is a principal acting as a namespace (the person's
  own `/users/<name>`, or the root for root-level work) through the same
  API ([2.2.18](#2218-human-as-a-principal)). So it isn't part of the
  kernel: it is a replaceable component, and anything it can do, the
  CLI can do ([2.6](#26-the-ui-and-the-sysui-conventions)).

The design target is concrete: **Shibu has to be good enough to build a
development pipeline, a software factory, on.** The factory walk-through
in [4.2](#42-full-software-factory) is the main test of the model, and
[section 5](#5-findings) records where the model did not fit.

---

## 2. The model

### Layers: kernel, standard distribution, additional components

Shibu has three layers. Each is built only on the ones before it.

**Rule of thumb:** if compromising it compromises everyone, it goes in
the kernel. If it changes when a vendor changes, it goes in the
distribution.

**Kernel.** What every Shibu has, and what everything else trusts:

- namespaces and the tree, identity ([Identity](#identity)), grants and
  allocators, including the allocator contract that any namespace can
  implement ([Component allocators](#component-allocators));
- VMs, and the **backend drivers** that provide them: a driver contract,
  with docker + granule as v1's one driver
  ([Backend drivers](#backend-drivers));
- the web proxy, with caller identity
  ([2.2.22](#2222-caller-assertion),
  [F8](#f8-proxies-need-the-callers-identity)), the only route out
  of a VM, which sends outside traffic to `/sys/egress`
  ([2.2.13](#2213-egress-policy)). It carries WebSocket and `CONNECT` tunnels on HTTP endpoints
  ([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)).
  Beyond v1 it gains an L4 path for `tcp` endpoints
  ([2.4](#24-beyond-v1-native-port-endpoints));
- the control API in [section 3](#3-operations), including `batch`
  ([2.2.8](#228-orchestration-surface));
- the front door, where people and other callers with no VM
  authenticate ([Identity](#identity)), and `shibu`, the reference CLI
  for the API ([CLI parity](#cli-parity)). The UI is not in the kernel
  (below);
- **agents**: `create_agent`, the harness interface and its contract,
  the agent control MCP, `messages` with its typed messages,
  `artifacts`, and budgets
  ([4.1](#41-agents-a-kernel-composite), [2.2.25](#2225-messages),
  [2.6](#26-the-ui-and-the-sysui-conventions));
- the **`runner/v1`** and **VM `mcp`** contracts, which any VM may serve
  ([Runners](#runners),
  [VM `mcp`](#vm-mcp-and-the-agent-control-mcp));
- one namespace per human, `/users/<name>`
  ([2.2.18](#2218-human-as-a-principal));
- **secrets**, with `read` grants as the only way to read one
  ([2.2.24](#2224-secret-store)), and the **exposure query** that says
  who can `exec` on, or reach, a VM or runner
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)).

Secrets are in the kernel because whatever stores them can impersonate
everyone. The store is part of the trusted computing base wherever it
runs, so moving it to a separate VM would add a second trusted thing
rather than shrink the first. The kernel secret store has **pluggable
storage backends** (built-in, Vault, GCP Secret Manager), the way
Kubernetes Secrets have KMS providers.

**Standard distribution.** Components that install starts under
`/sys/`, each behind a named, versioned **contract**
([2.3](#23-the-standard-distribution-and-the-sys-spec)). There are two
classes.

- **Bootstrap set: replaceable, not removable.** Install starts these
  so that the first agent has somewhere to run and a model to reach. If
  one is missing, or fails its contract, a `create_agent` that names it
  fails with a clear error rather than half-starting an agent.
  - `/sys/runner`, a pool runner serving `runner/v1`, with the Claude
    Code, Codex and Gemini/Antigravity adapters in its image
    ([Runners](#runners));
  - `/sys/model`, the metered model proxy: one way for a runner to
    reach models without holding a key
    ([2.2.23](#2223-model-access-and-budgets)).

  Neither is the only way. Any VM may serve `runner/v1`, and a runner
  may reach its models however it is configured; `create_agent` needs
  some runner the caller holds a `launch` grant on, not these two in
  particular.
- **Optional: may be turned off.** An agent starts without them; it
  just can't use what they provide until it is granted one.
  - `/sys/git`, git over smart HTTP, backed by GitHub or hosted on
    Shibu. Without it an agent cannot reach repos until it is granted
    some other git endpoint ([2.3](#sysgit-and-sysforge));
  - `/sys/forge`, pull requests, reviews, checks and merges. Without it
    a repo still works for agents, with no PR flow
    ([2.3](#sysgit-and-sysforge));
  - `/sys/ui`, the UI, including the credential and config forms it
    builds from adapter manifests. Without it, Shibu is driven from the
    CLI. Any conforming UI can replace it; the conformance rule is CLI
    parity ([2.6](#26-the-ui-and-the-sysui-conventions));
  - credential proxies: GitHub, GCP, inbound HMAC and the registry
    ([F9](#f9-secrets-have-no-shibu-home));
  - the package cache;
  - the tunnel shim, `shibu forward` and `shibu serve`, shipped in the
    base images
    ([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints));
  - the `/sys` reconciler, which drives the `/sys` spec from a config
    repo ([2.3](#23-the-standard-distribution-and-the-sys-spec)).

**Additional components.** Everything else, built by Shibu's users or
their agents on the two layers above, using only kernel operations and
distribution contracts:

- inboxes, approval policies and escalation
  ([F14](#f14-inbox-requests-need-structure));
- leak tooling: idle signals and reports
  ([F12](#f12-leaks-in-long-lived-namespaces));
- priority and fairness policies for the admission queue
  ([2.2.21](#2221-admission-queue));
- the software factory in [4.2](#42-full-software-factory);
- panes that agents and components serve into the UI
  ([2.6.6](#266-agent-served-panes));
- grain's own components, once grain runs on Shibu
  ([7.2](#72-the-decision-grain-runs-on-shibu)).

Rate limits are punted and in no layer yet
([F11](#f11-rate-limits-should-be-hierarchical)).

The kernel depends on the distribution only through contracts, never
through an implementation. Nothing in the kernel or the distribution
depends on an additional component.

**Trust** (decided, 1884). There is no kernel notion of a trusted or
an untrusted VM or runner. Each owner decides where its own things go,
and says so only through grants: a secret's owner decides which
namespaces may `read` it, a VM's owner who may `exec` on it, a runner's
owner who may `launch` on it. The kernel and web proxy enforce those
grants and identity on every call, and rely on no VM or runner to
protect the core ([Trust](#trust)).

- Distribution components that hold credentials (`/sys/model`,
  `/sys/runner`, GitHub-backed `/sys/git` and `/sys/forge`, the
  credential proxies) receive them through **`read` grants** on the
  secrets, listed in the `/sys` spec
  ([2.3](#23-the-standard-distribution-and-the-sys-spec)). A `read`
  grant is the only way to read a secret
  ([2.2.24](#2224-secret-store)).
- Keeping a credential away from code its owner doesn't trust is the
  owner's choice of grants, not a kernel placement rule. The exposure
  query shows the owner who could reach it
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)).

**How agents appear in the UI** (decided, 1771). The UI is a
distribution component, and agents never talk to it directly. They
publish standard endpoints and labels (typed `messages`, status labels,
`artifacts`, and panes they serve themselves), and whichever UI is
running renders them, as the CLI does
([2.6](#26-the-ui-and-the-sysui-conventions)). Any agent or component
can serve a pane, isolated on its own origin inside a frame the UI
draws ([2.6.6](#266-agent-served-panes)). This decides
[F30](#f30-component-ui-panels). Harness and adapter UI for keys
stays declarative-only: credentials are entered only in the UI's own
manifest-driven forms.

### 2.1 Kernel model (decided)

*Kernel.* Everything in 2.1 is in the kernel.

#### Identity

How a VM and its processes authenticate.

**Ambient identity by origin.** Nothing is handed into a VM for its own
identity.

- Every packet a VM sends leaves through a network path Shibu controls:
  a per-VM tap or vsock, with source checks. It reaches the web proxy,
  so the proxy knows which VM is calling, and therefore which namespace.
- A process in the VM calls the well-known control endpoint
  (`http://shibu.internal/`, also exported in the VM as
  `SHIBU_ENDPOINT`), or any endpoint by name, with no token. This is
  how the GCE metadata server and workload identity work.
- There is nothing to exfiltrate. A request from anywhere else is not
  that VM, whatever it carries.

**Kernel invariant on backends: a VM's network origin is unspoofable.**
That means one VM per interface, no shared bridges where a VM could
claim another VM's address, and source checks enforced by the backend
driver ([Backend drivers](#backend-drivers)). A driver that cannot
guarantee this fails its conformance suite and cannot host Shibu VMs
([F35](#f35-a-driver-must-prove-an-unspoofable-origin-before-it-is-admitted)).
[7.1](#pkggranule-and-pkgk8s) and
[F25](#f25-the-backend-carries-the-whole-identity-model) say what this
asks of grain's backends.

**A VM is the trust boundary.** Every process in a VM has the VM's full
ambient authority. Untrusted code must therefore run in a *different*
VM, in a different namespace. An agent `a/x` that runs code it did not
write runs it in its sandbox `a/x/sbx`. That split is a rule, not a
habit. It is also why v1 has no attenuated tokens
([2.2.11](#2211-no-attenuated-tokens)): a process that narrows
itself is not protected from code running beside it.

The same holds for a VM's `mcp` endpoint
([VM `mcp`](#vm-mcp-and-the-agent-control-mcp)). A command run through
its `exec` tool runs in that VM, with that VM's origin, so it uses the
egress and grants of the VM's home namespace, whoever called the tool.
A grant of `exec` on another namespace's VM therefore **lends that
VM's identity** to the grantee. That is deliberate, and it makes `exec`
the strongest op a grant can carry. An agent created by `create_agent`
holds `exec` on its own VM, so the code its tools run has the
authority of the VM's namespace, by default the agent's own
([F29](#f29-an-agents-tools-run-with-the-agents-authority)). An agent
that runs code which must not have its authority uses a VM in another
namespace as its tools, as the factory's workers do with shared build
VMs ([4.2.5](#425-worker-agents-and-their-sandboxes)).

**Callers with no VM.** The UI and other clients outside Shibu have no
VM origin. They authenticate at the front door (an identity list like
grain's `pkg/access`, behind IAP or similar), and each identity is
mapped to the namespace it acts as, normally its own `/users/<name>`
([2.2.18](#2218-human-as-a-principal)).

**Runner sessions.** Every harness runs in a session of a runner, the
agent's `runs_in` ([Runners](#runners)). Its identity follows from
where that runner is:

- **A runner on a VM in the agent's own namespace** (the `same-vm`
  case): the session calls out with that VM's origin, like any process
  in the VM.
- **Any other runner** (a pool runner such as `/sys/runner`): the
  session acts *as* the agent's namespace through a **session
  binding** that the kernel issues to the runner when it accepts the
  `launch`, and the caller assertion says so: `ns=<agent>, via=runner`
  ([2.2.22](#2222-caller-assertion)).

The binding is **kernel-internal**. It is how a runner acts as an
agent's namespace, not a token anyone can mint, see or pass on. The
kernel issues one only for a launch it accepted, and only for the
agent that launch was for. A runner can't mint one, widen one, or use
one after its session has ended.

#### Namespace

A namespace is a hierarchical path: `a/`, `a/x/`, `a/x/sbx/`.

- A path maps to a URL folder on the main Shibu domain
  (`https://<shibu>/a/x/`) and to a folder in the git repo space.
- Every object, namespaces included, also has a **stable ID**. The path
  is the object's name. References, grants included, use the ID, so
  moving or renaming things doesn't break them.
- Every VM belongs to exactly one namespace.
- Each namespace has a default **control endpoint**. Through it, using
  the allocators and grants it holds, the namespace creates its
  sub-namespaces, VMs, volumes and endpoints, and issues grants.

**Lifecycle.** A namespace is a container. It can hold several VMs and
agents, and those are what exit
([2.2.17](#2217-exit-status-and-reason)). A namespace never exits; it
is **deleted**:

- explicitly, by itself or an ancestor: `delete(ns)`;
- when its **lease** expires ([2.2.6](#226-leases-and-ttls)).

Those are the only two ways. "Delete it once every VM and agent in it
has exited" is a policy built on `watch` and `delete`, by the creator
or by an optional reaper component, not a kernel state
([2.2.6](#226-leases-and-ttls)).

A namespace's state is `active` or `deleting`. `deleting` is brief and
can't be configured: every VM in the namespace is stopped with its
usual stop signal and timeout, nothing new can be created in it, and
then everything in it is deleted, the namespace's received grants are
revoked, and charges drop off their payers
([2.2.4](#224-lifecycle-and-keeping-results)). A crash never deletes a
namespace: a crashed VM is an exited VM, and its namespace goes only
by `delete` or its lease.

#### The tree

The tree is kept for exactly three things:

- **Naming:** paths and URL folders, with stable IDs.
- **Lifetime:** a child is deleted with its parent.
- **A parent's implicit authority over its own subtree:** it can always
  see, stop and revoke anything below it. "See" includes reaching every
  endpoint in its subtree, so it can also grant those endpoints onward,
  narrowed ([Grants](#grants)).

Everything else is carried by grants: cross-namespace visibility, use,
control and quota. Siblings are not isolated by structure. A sibling
sees nothing of another until something is granted, and anyone
holding an object may grant it, if it owns the object or received it
with `regrant=true` ([Grants](#grants)). v1 has no way to confine
further what a subtree grants; that is policy, beyond v1
([2.7](#27-beyond-v1-policy)).

#### Endpoint

An endpoint is a name in a namespace pointing at one of five kinds of
thing, and the kernel has no other endpoint kinds:

- `http`: a VM port, or a path the kernel serves itself;
- `volume`: a volume (images and snapshots are volumes);
- `control`: a namespace's control endpoint;
- `allocator`: an allocator;
- `secret`: a secret ([2.2.24](#2224-secret-store)).

Every service is an **HTTP endpoint**: the web proxy speaks HTTP to it,
and a grant on it names HTTP methods, `read` (`GET`, `HEAD`,
`OPTIONS`), `write` (the rest) and `tunnel`, narrowed by `paths`. A
service that needs more defines it in its contract: ops (`exec`,
`launch`, `fetch`, `push`) and limits (a budget, `hosts`, `repos`)
that its implementation enforces from the caller assertion, and that
the kernel stores and narrows like any other. The kernel and the proxy
don't know what such a name means: there is no vocabulary a service
declares to them, and names like `fetch` or `call` are documentation
of paths and methods
([6.8](#68-how-a-service-declares-its-operations)). A non-HTTP service is reached through one by a
tunnel, which a grant allows with the `tunnel` op
([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)). A
native `tcp` endpoint kind comes beyond v1
([2.4](#24-beyond-v1-native-port-endpoints)).

An agent's namespace also has the agent's standard endpoints, `agent`,
`job`, `messages`, `artifacts`, `exit`, `usage` and the agent control
MCP, `agent-mcp` ([4.1](#harness-interface)). Shibu serves them itself,
and they are `http` endpoints, granted by paths and methods like any
other: `read` to read a chat, `write` to post to it.

Any VM may also serve two **standard VM endpoints**, each an `http`
endpoint behind a kernel contract:

- **`mcp`** (`<vm>/mcp`): the VM tools, `exec`, `read_file`,
  `write_file`, `edit_file` and `list_dir`, as MCP over streamable
  HTTP ([VM `mcp`](#vm-mcp-and-the-agent-control-mcp));
- **`runner`** (`<vm>/runner`): harness sessions, under `runner/v1`
  ([Runners](#runners)).

A grant appears in the grantee as an ordinary endpoint, a handle, so
whatever a namespace holds, it holds as endpoints.

#### Visibility

A VM can reach:

- every endpoint in its own namespace and in all of that namespace's
  descendants;
- every endpoint granted to its namespace;
- every endpoint granted with `subtree=true` to one of its ancestors.

It cannot see upward otherwise. What it can *do* with an endpoint is
whatever the grant's `ops` and `limits` allow.

#### Grants

A grant gives a namespace access to an endpoint, narrowed. It is the one
general primitive.

```
grant(object, to=<ns>, ops=[...], limits={...}, subtree=false,
      regrant=false, lease=<ttl>) -> grant_id
revoke(grant_id, kill=false)   # cascades to re-grants
list_grants(path)              # given and received, for audit
```

- **What can be granted.** Any endpoint: a VM port, a volume, a control
  endpoint or an allocator. A namespace can grant what it owns (its own
  endpoints and those in its subtree) and what it received with
  `regrant=true`. A grant's object can also be a namespace used as a
  folder of endpoints (`svc/images/`), which covers every endpoint in
  it, including ones created later.
- **Narrowing only.** `ops` and `limits` can only narrow what the
  granter holds. Each kind of endpoint has its own narrowing
  vocabulary:

  | Endpoint kind | Narrowed by |
  | --- | --- |
  | `http` (a VM port, or a service the kernel serves) | paths, methods, `read` vs `write`. The web proxy enforces it, and the service sees it in the caller assertion ([2.2.22](#2222-caller-assertion)). `tunnel` allows a WebSocket or `CONNECT` upgrade, limited by `max_connections`, `bandwidth` and `max_duration` ([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)). A contract may add ops and limits its implementation enforces: `exec` on a VM's `mcp`, `launch` and a budget on a runner, a budget, vendors and models on `/sys/model`, `hosts` on `/sys/egress`. An agent's `messages` is `read` to read and `write` to post ([2.2.25](#2225-messages)). |
  | `volume` | read vs read-write, sub-path. |
  | `control` | `read` (get, list, watch, audit, cost) or `write` (everything else). |
  | `allocator` | `cap`, `into` (where created objects may be placed), `shapes`, `images` and `budget`, and the `reserved` flag on the grant ([Quota](#quota)). |
  | `secret` | `write`: the grantee may write a new value, never read it. `read`: the grantee may read the value, and its namespace is marked credential-holding, as information ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). A `read` grant is the only way to read a secret ([2.2.24](#2224-secret-store)). The kernel refuses no `read` grant because of what the grantee runs; the owner checks who could reach the secret with the exposure query first. |

  There are no other endpoint kinds ([Endpoint](#endpoint)).

- **An allocator grant is a sub-allocator.** For an allocator, the
  amount is simply its form of narrowing. The grantee's handle behaves
  like a sub-allocator over the original, with its own counter.
- **`subtree=true`** makes the grant visible in the grantee's whole
  subtree. A shared service is granted once, at the top, and from there
  reaches every short-lived sandbox beneath. Control endpoints and
  allocators can **never** be granted with `subtree=true`, so a sandbox
  never picks up its parent's quota or control. They can be granted
  onward explicitly. A secret with `read`, and a VM `mcp` with `exec`,
  can't be granted with `subtree=true` either: each hands a whole
  subtree, including namespaces created after the grant, a secret's
  value or a VM's identity, so the owner's exposure check
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)) would be
  answered for readers it never named.
- **`regrant=false` is the default.** A received grant can be used but
  not passed on, unless the granter set `regrant=true`. With it, the
  grantee may grant onward, and every re-grant still narrows. An
  ancestor of the grantee, acting through its authority over its
  subtree, needs the same flag to grant from that handle. Objects a
  namespace owns by the tree are unaffected: it may always grant them,
  narrowed. The default is deliberate:
  - **Access stays where the granter put it.** A grant reaches the
    namespace it names and no further, so there is no transitive sprawl,
    and no confused deputy passing on authority it was lent to use.
  - **Delegation is a choice the granter makes.** `regrant=true` is for
    grants meant to be split: allocator caps, or a model budget on a
    runner, handed to a lead agent to divide among its workers.
  - **In v1 it is what stops a subtree passing on what it received.**
    It does not stop a subtree granting its own objects outward. That
    is policy, beyond v1 ([2.7](#27-beyond-v1-policy)).
- **Every limit narrows.** A reservation is not a limit but the
  boolean `reserved` on an allocator grant: `reserved=true` sets the
  whole cap aside from the granter's headroom, whether or not the
  grantee uses it, and can only be issued from reserved headroom. A
  guarantee plus burst is two grants ([Quota](#quota)).
- **Leases.** A grant can carry a lease. When it expires, the grant is
  revoked as if by `revoke(grant_id)`.
- **Audit.** Every grant and revoke is an audit line, naming the
  granter, the grantee, the object, the ops and limits, and the chain
  the grant was issued from.

**Revoking.** Revocation cascades to every grant issued from the revoked
one. For a VM port, volume or control endpoint, revocation stops new
requests at once, and in-flight requests are allowed to finish. For an
allocator grant, it blocks new creates at once. Objects already created
through it run until they are deleted or their lease expires.
`kill=true` deletes them too; it is the lender's emergency stop.

**Revoking closes connections.** When a grant ends, the web proxy
**closes every open tunnel and connection made through it**, and every
one made through a grant it cascades to. That applies to an explicit
revoke, an expired lease, and a grant made invalid by the validity rule
below ([F13](#f13-what-happens-to-an-alias-when-its-target-moves-or-dies)).
A tunnel can stay open for days, so letting it finish like an in-flight
request would let a revoked grant outlive its revocation. Plain HTTP
requests keep the per-request rule above
([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)).

**Validity.** A grant is valid only while its grantor could still issue
it:

- When the target is deleted, the grant dies, and the grantee gets a
  `watch` event.
- Grants reference stable IDs, so they survive a `move` of the target,
  unless the move takes the object out of the grantor's authority. In
  that case the grant is revoked, cascading to re-grants, with an event.
- When the grantor's own authority ends (its received grant is revoked,
  or its namespace is deleted), its grants are revoked with it.
- When a namespace is deleted, every grant it received is revoked, as
  part of its deletion ([2.2.4](#224-lifecycle-and-keeping-results)).

v1 has no boundaries: no policy an ancestor sets to confine which
grants cross into or out of a subtree. What they were for is carried
over as requirements on the policy model beyond v1
([2.7](#27-beyond-v1-policy)); the dropped design is in
[A.22](#a22-boundaries).

#### Trust

Trust is a matter of perspective (1884). There is no kernel class of
trusted VMs or runners, and no kernel rule about what may run beside
what. Each owner decides where its own things go, and expresses that
only through grants: who may `read` its secret, `exec` on its VM,
`launch` on its runner, or call its endpoint. The design holds to
three principles.

- **Least privilege.** Grants are narrow: by op, by limit and by scope
  ([Grants](#grants)). `exec` on a VM is granted apart from `read`, a
  `launch` grant on a runner can name the adapters and models it
  covers, and a grant reaches a subtree only with `subtree=true`.
- **Short-lived credentials.** Grants and delivered secrets carry
  leases ([2.2.6](#226-leases-and-ttls)). A secret's `read` grant is
  revocable, and a value that may have leaked is rotated
  ([2.2.24](#2224-secret-store)).
- **The core never trusts what's outside it.** The kernel and the web
  proxy enforce, on every endpoint call and without relying on any VM's
  or runner's behaviour: reachability and ops; leases and wall-clock
  time; identity, by VM origin or session binding ([Identity](#identity));
  and grant chains, with their caps and narrowing. A runner can't widen
  its own grants, mint identities, or affect anything outside what it
  was granted.

What the core does *not* enforce is anything inside a grantee. Limits
in a grant that only the grantee can apply, such as the models,
adapters and budget in a `launch` grant on a runner, are handed to it
in the caller assertion ([2.2.22](#2222-caller-assertion)). Relying on
a runner to apply them is the granter's choice. A granter who doesn't
want to rely on it grants model access only through `/sys/model`, where
tokens are metered and refused outside the runner
([2.2.23](#2223-model-access-and-budgets)).

Each owner can see what its grants add up to. The **exposure query**
says who can `exec` on, or reach, a given VM or runner, and therefore
who could reach a secret delivered there
([2.2.7](#227-credentials-only-behind-proxy-endpoints)).

#### Allocator

An allocator is a special endpoint that holds quota and supports
`create` and `delete`.

- `create` takes a target in the caller's subtree, or inside the
  grant's `into`. It creates the resource, writes the resource's handle
  to that target, and charges the allocator or grant named in the call.
- Kinds of allocator, the kernel's two resource kinds:
  - **VM**: cpu, ram, ephemeral storage; each kernel VM allocator
    offers one placement class;
  - **volume**, images and snapshots included.
- These are the **kernel allocators**. There is no egress or
  public-endpoint allocator: outside access is a grant on `/sys/egress`
  ([2.2.13](#2213-egress-policy)) and public exposure a grant to
  `/sys/ingress` ([2.2.12](#2212-pre-approved-publish)). Their roots come from the backend
  drivers ([Backend drivers](#backend-drivers)), and only root changes
  them.
- A custom quota is a new kind of allocator, and any namespace can
  implement one: a **component allocator**
  ([Component allocators](#component-allocators)). **Holding an
  allocator, or a grant of one, is the permission.** There is no
  separate ACL saying who may create VMs: a namespace holding a VM
  allocator grant with headroom may, and one without may not.

The allocator contract is the same for both kinds: `create`, `delete`,
`usage` and `list`, served behind an endpoint. Each allocator endpoint
makes one kind ([section 3](#allocator-contract)).

There is no **model** allocator kind and no agent allocator kind. An
agent is made by the standard distribution's `create_agent` helper,
which charges any VM it creates to an ordinary VM allocator or grant,
and launches its harness through a `launch` grant on a runner, which carries the model and the
budget ([4.1](#41-agents-a-kernel-composite),
[2.2.23](#2223-model-access-and-budgets)). Counting agents is a label
query, not a quota.

#### Quota

**Quota is a chain of grants.**

- **Root allocators are the only quota anyone owns.** The kernel's are
  the backend drivers' capacity, held by `/`
  ([Backend drivers](#backend-drivers)). A component allocator is the
  root of its own chain, owned by the namespace that implements it
  ([Component allocators](#component-allocators)). Everything below a
  root is a chain of grants. Each charge is recorded against every link of its chain, so
  `cost(ns)` still adds up per namespace, and each link's headroom is
  checked on every `create`.
- **One payer, one home.** Every resource lives in exactly one
  namespace, its **home**, which decides its visibility and lifetime.
  It is charged to exactly one allocator or grant, its **payer**. The
  two may differ.
- A resource is deleted when its home is deleted, its lease expires, it is
  explicitly deleted, or its payer allocator is torn down.
- When a resource is deleted, its charge drops off the payer. Nothing
  "returns", because the charge never left the lender's books: the
  borrower held only the right to make the lender pay.

Two shapes follow from that:

- **Lend** (home = borrower, payer = lender). For example, a `c17-1` VM
  drawn from `ci`'s pool. It dies with the borrower.
- **Deposit** (home = payer = lender). The borrower may only create,
  and only where the grant's `limits.into` says. For example, `rel`
  writing an image into `svc/images`. It outlives the borrower.

**What a quota grant limits.** `cap` per dimension, `into` (deposit
areas), `shapes`, `images` and `budget`, and nothing else; every limit
narrows. A grant is **shared** by default: its cap bounds what the
grantee may draw from headroom it shares with the granter's other
grantees, and the caps a namespace grants may add up to more than it
holds. With `reserved: true` the whole cap is set aside from the
granter's headroom at grant time, used or not. A guarantee plus burst
is two grants, one reserved and one shared, and a reserved grant can
only be issued from reserved headroom
([2.2.2](#222-caps-reservations-and-adoption)).

**Revoking a quota grant** blocks new creates immediately. Existing
objects run until they are deleted or their lease expires. `kill=true`
deletes them.

**No headroom moves between siblings.** Quota originates only at the
roots and flows down grant chains. Grants let others *spend* it. A
resource's payer changes only through an explicit `move`, or when a
component allocator hands it over with `adopt`
([2.2.2](#222-caps-reservations-and-adoption)).

#### Component allocators

*Kernel: the allocator contract, and the grants, accounting and cleanup
around a component allocator. The component's own code is the
component's.*

"Allocator" is a contract, not a kernel-only object. The contract is
`create`, `delete`, `usage` and `list` behind an endpoint
([section 3](#allocator-contract)). **Any namespace can implement its
own allocator**: an agent, a VM or a service. For example:

- a GPU-pool service that hands out slices of GPUs it holds;
- a cloud-account broker that creates projects or accounts;
- a credits pool for a paid API.

The kernel treats such an endpoint as an allocator, so **grants work on
it exactly as on a kernel allocator**: narrowing, caps and
reservations, `regrant`, leases, and `revoke` with `kill`
([Grants](#grants)).

**The owner controls its own allocator's quota.** The implementing
namespace is the root of that allocator's grant chain. It defines its
kinds and amounts, and can raise, lower and revoke freely.

**No component can change the core system allocators.** The kernel and
driver root allocators (cpu, ram, storage, VM placement classes) and the
model budget at `/sys/model` are changed only by root, through the
`/sys` spec and grants
([2.3](#23-the-standard-distribution-and-the-sys-spec)). A component
allocator cannot mint kernel quota. The kernel resources it uses to
fulfil its own creates (VMs, volumes) come from the kernel grants it
already holds, and are charged to it like anything else. A GPU slice
handed out by a pool is charged once to the pool's grantee, in GPU-pool
units. The VM behind it is charged once to the pool, in kernel units.
Nothing is counted twice.

A component allocator may also answer a `create` with a kernel
resource it already owns, instead of making a new one. The kernel
**adopts** it: it re-homes the resource into the create's target and
moves its charge to the grant the create named
([2.2.2](#222-caps-reservations-and-adoption)). That is how a warm pool
hands out a pre-booted VM.

**Accounting.**

- The kernel enforces caps and headroom on a component allocator's
  grants using only what it can count itself: the creates made through
  each grant, and the amounts declared in each create's spec. A create
  that would take a link of the chain over its cap is refused before the
  component sees it.
- The component's own `usage` reports appear in `cost()`, marked
  **reported**. They never loosen a limit
  ([F33](#f33-a-component-allocator-can-misreport-usage)).

**Existence and cleanup.**

- The kernel is the source of truth for *what exists*. It records every
  handle created through a grant: its home, its payer, and the grant it
  came through.
- When a namespace is deleted
  ([2.2.4](#224-lifecycle-and-keeping-results)), the kernel calls
  the allocator's `delete` for each handle homed there, with retries.
  It does the same for `revoke(kill=true)` and an expired lease.
- A `delete` that keeps failing is a visible **leak**. The handle is
  kept and charged to the grantor, and the leak shows in `watch` and in
  the UI until a retried `delete` succeeds
  ([F34](#f34-cleanup-leaks-when-a-component-allocator-is-down)).

**The invariants still hold.** One home, one payer and grant-based
access apply to component-allocated resources as seen through their
handles. Trusting a component allocator is the grantee's decision,
made when it accepts or uses the grant. v1 has no way for an ancestor
to forbid a subtree using allocators from outside it; that is a
requirement on policy beyond v1 ([2.7](#27-beyond-v1-policy),
[F33](#f33-a-component-allocator-can-misreport-usage)).

#### Move

- `move(object, from, to, payer=<allocator or grant>)` can be called by
  something in namespace N when `from` and `to` are both within N's
  subtree. Either may be N itself.
- `move` re-homes the object: after the move, its visibility and
  lifetime are the destination's.
- The payer stays the same unless `payer=` names a new one, which must
  be an allocator or grant the caller holds and which allows the
  destination. Apart from `adopt`
  ([2.2.2](#222-caps-reservations-and-adoption)), a move is the only
  way a resource's payer changes.
- Grants on the object survive the move, subject to the validity rule
  ([Grants](#grants)).

Handing quota from one namespace to another is not a use of `move`.
That is a grant.

#### VM

A VM is created through a VM allocator or a grant of one. It takes:

- a shape: cpu, ram, ephemeral storage;
- a base image ([2.2.14](#2214-images));
- a placement class and a restart policy
  ([2.2.15](#2215-placement-classes-and-restart-policy));
- the volumes attached to it, and the endpoints on its ports.

Every VM has its own status (running, restarting, exited, preempted),
an exit code and a reason once it exits, and a restart count. Status
changes are `watch` events ([2.2.17](#2217-exit-status-and-reason)).

#### Backend drivers

*Kernel. A driver is a plug-in the kernel loads, chosen in the `/sys`
spec.*

VMs and volumes come from **backend drivers**. The kernel defines a
**driver contract**, and every driver implements it
([section 3](#driver-contract)):

- **VMs:** create, start, stop and delete, with a shape and an image
  ([VM](#vm), [2.2.14](#2214-images));
- **volumes:** attach, snapshot and clone
  ([2.2.3](#223-volume-snapshot-and-clone));
- **network:** a per-VM interface with an **unspoofable origin**
  ([Identity](#identity)), and an egress enforcement point
  ([2.2.13](#2213-egress-policy));
- **events:** status and preemption, which feed a VM's status and exit
  reason ([2.2.17](#2217-exit-status-and-reason));
- **placement classes:** which of them the driver offers
  ([2.2.15](#2215-placement-classes-and-restart-policy));
- **capacity:** what it can host, exposed as root allocators (below).

Each contract version has a **conformance suite**, as the distribution
contracts do ([2.3](#23-the-standard-distribution-and-the-sys-spec)).
It includes a test that tries to spoof another VM's origin. A driver
that fails it is not admitted
([F35](#f35-a-driver-must-prove-an-unspoofable-origin-before-it-is-admitted)).

**v1 ships one driver: docker + granule VMs**, which is what grain runs
today (`pkg/granule`; [7.1](#pkggranule-and-pkgk8s)). The contract is
there so that adding a driver is easy. Planned drivers, and what each
has to provide to meet it:

| Driver | Status | What it has to provide |
| --- | --- | --- |
| docker + granule | v1 | a per-VM tap with source-address checks and no shared bridge, egress routed to the web proxy, volumes as docker volumes or disk images, host capacity readings (`pkg/sysstat`) |
| k8s | planned; `pkg/k8s` is its seed | a pod per VM under Kata or gVisor-style isolation, since a plain container is not a VM boundary; a CNI or network policy enforcing one source address per pod and no pod-to-pod traffic; egress through the proxy; an origin map updated before a pod IP is reused; PVCs with CSI snapshots for volumes |
| GCE | planned | an instance per VM on its own NIC with anti-spoofing; egress through the proxy by firewall and routes; persistent disks and their snapshots; preemption notices from spot instances; the project's quotas as capacity |
| AWS | planned | an instance per VM with its own ENI and source/destination checks on; egress through the proxy by security groups and routes; EBS volumes and snapshots; spot interruption notices; the account's service quotas as capacity |

**Several drivers run at once.** Each placement class maps to a driver,
for example `standard` → docker + granule and `gpu` → GCE. Drivers are
configured in the `/sys` spec
([2.3](#23-the-standard-distribution-and-the-sys-spec)), with the same
staged swap and conformance suite as a distribution component. **A
driver swap cannot migrate running VMs.** It applies to new creates;
existing VMs stay on the old driver and drain, by exiting or being
deleted, before the old driver is removed.

**Root allocators come from drivers.** Each driver exposes its capacity
as root allocators held by `/`: a docker host's cpu, ram and disk, or a
GCE project's quota. Root grants down from them
([Quota](#quota)). This is where root allocators come from.

### 2.2 Factory-driven additions (decided)

The factory exercise forced these additions. They are part of the model,
not proposals. Each says which layer it belongs to.

#### 2.2.1 Subtree grants

*Kernel.* Described [above](#grants). A grant made with `subtree=true` is
visible in the grantee's whole subtree. Control endpoints and
allocators can never be granted with `subtree=true`.

#### 2.2.2 Caps, reservations and adoption

*Kernel: caps, reservations and `adopt`. Standard distribution: the
warm pool built on `adopt`.* These are limits on allocator grants:

- **Cap.** An allocator grant's amount is a cap. The grantee draws from
  the granter's headroom up to that cap, so the caps a namespace grants
  can add up to more than it holds.
- **Reservation.** When a grantee needs guaranteed capacity, the grant
  is made `reserved: true`, a boolean on the grant: its whole cap comes
  off the granter's headroom at grant time, used or not. A guarantee
  plus burst is two grants, a reserved one for the guarantee and a
  shared one for the burst. A reserved grant can only be issued from
  reserved headroom: an allocator owned by the tree, or a reserved
  grant whose cap isn't yet reserved onward. So every limit narrows,
  with no exception.

**Adopt.** A component allocator
([Component allocators](#component-allocators)) that answers a `create`
may satisfy it with a resource it already owns, instead of making a new
one: a VM, or a writable volume (such as a clone of a snapshot). Its answer
names the existing resource, and the kernel **adopts** it:

- The kernel re-homes the resource into the create's target, so its
  visibility and lifetime are the target's from then on. It applies the
  rest of the create as it would to a new resource: the lease, the
  labels, and for a VM the volumes to attach and the endpoints on its
  ports.
- Its charge moves to the grant the create named, which becomes its
  payer. In the allocator's own dimensions it is charged there, like
  anything else the allocator hands out. In kernel units it stays on the
  kernel grant the allocator made it through, because a component
  allocator can't mint kernel quota. Nothing is counted twice.
- For a VM, the web proxy remaps the VM's origin to its new namespace
  ([Identity](#identity)). Nothing else is rebound, and no credential is
  injected. Grants the old home gave on the resource end, and anything
  the old home held through the resource's ports is removed.
- The kernel checks two things before it adopts: the resource must be
  owned by the allocator's own namespace, homed there or in its
  subtree; and it must be of the kind and shape the create asked for
  (for a VM, its shape and image, and the class of the allocator's own
  VM grant; for a volume, its size and the volume it was cloned from). Either failure fails the create, and the
  resource stays where it was.

The invariants hold through an adopt. The resource has one home and one
payer before and after, and access to it is by the tree and by grants
as for any object. `adopt` is the hand-off `move` can't make: `move`
works only within the caller's subtree, and the target of a create is
usually in an unrelated one. It is not a separate call. It is a way of
answering `create`, so the caller sees an ordinary `create`.

**Warm pools** are built on `adopt`, as an optional standard-distribution
component (`warm-pool`, [`extension-points.md`](shibu-spec/extension-points.md#warm-poolv1)).
A pool is a component allocator holding a VM allocator grant. It keeps
N pre-booted VMs per shape and image in its own namespace, charged to
that grant:

```
create_namespace("factory/ci/pool", lease=7d, grants=[
    grant(ci/vm-spot, ops=[create], limits={cap: 160 cpu, 640Gi}),   # → ci/pool/vm-alloc
])
create(ci/pool/vm-alloc, target=ci/pool/vm, lease=7d,
       spec={shape: 2cpu/4Gi, image: svc/images/warm-pool,            # the distribution's shibu/warm-pool
             env: {POOLS: "8cpu/32Gi/100Gi svc/images/ci-base size=6"}})
# the pool's VM declares its allocator endpoint, ci/pool/alloc
```

- A `create` through the pool, or through any grant of it, is answered
  with a waiting VM that matches the spec, which the kernel adopts into
  the create's target. If none is waiting, the pool cold-boots one
  through its VM grant and hands that over the same way.
- The pool refills on every take. A pooled VM is never returned to the
  pool: delete destroys it.
- It exposes its size and refill stats (waiting, booting, hits, misses,
  boot time) on its own endpoint.

A short-lived sandbox is then a create through a pool grant instead of
a full boot. The same pattern covers **pre-cloned volumes**, a pool of
clones of a warm snapshot handed out by `adopt`, and **pre-warmed agent
VMs**, a pool of `agent-base` sandbox VMs.

#### 2.2.3 Volume snapshot and clone

*Kernel.* There are no `snapshot` or `clone` operations, and no
snapshot or image kinds. A snapshot is a read-only volume, and a clone
a writable one, each made by an ordinary `create` through a volume
allocator with `spec: {from: <volume | snapshot>}`; an image is a
read-only volume made with `from: {oci: …}`. Each is charged to the
allocator or grant the `create` names, like any volume.

```
create(svc/volume-alloc, target=svc/repo-warm-1002,
       spec={kind: volume, from: svc/repo-warm, read_only: true})   # a snapshot
create(ci/c17-1/vols, target=ci/c17-1/src,
       spec={kind: volume, from: svc/repo-warm-1002})              # a clone of it
```

#### 2.2.4 Lifecycle and keeping results

*Kernel.*

**Deposit areas** are the standard way to keep a result. A deposit area
puts the data in its final home while the namespace is still running,
so a crash loses nothing. There are no declared outputs and no drain
at exit. When a parent
creates a child, it hands the child a deposit grant into the parent's
own storage:

```
grant(parent/vol-alloc, to=child, ops=[create],
      limits={into: "parent/keep/<child>/*", bytes: 5Gi})
```

The sugar is `create_namespace(..., keep=5Gi)`, and the
`create_agent` helper's `keep=5Gi` passes it through.

- The area is `<caller>/keep/<child>/`, in the caller's namespace, and
  appears in the child as the handle `<child>/keep`. It is charged to a
  volume allocator or grant the caller holds;
  `keep={bytes: 5Gi, from: <allocator>}` names it when there is more
  than one, and `keep={..., into: <path>}` puts the area elsewhere in
  the caller's subtree.
- A child that decides late what to keep, such as a worker's notes,
  writes or copies it into the area. It is a deposit
  ([Quota](#quota)): home and payer are the parent's from the start, so
  nothing moves at the end, nothing changes payer, and a crash loses
  nothing that was written.
- What is in the area is the parent's to delete or lease. A parent that
  wants it to expire leases what is in it with `set_lease`
  ([2.2.6](#226-leases-and-ttls)). Requiring every
  deposit to carry a lease is a policy beyond v1
  ([2.7](#27-beyond-v1-policy)).

Where the destination is a specific place, such as `ci/results/` or
`svc/images/`, an ordinary deposit grant into that place does the same
job ([4.2.6](#426-ci), [4.2.9](#429-release-and-deploy)).

**Keeping results without a grace period.** Deletion has no grace
period and no rescue, so whatever outlives a namespace is arranged
before deletion starts, in one of two ways:

- **Up front:** a deposit area (`keep=`) declared when the child is
  created, or a deposit grant into a specific place. The child writes
  there as it goes.
- **Before `delete`:** the parent moves or snapshots what it wants and
  only then calls `delete`. Ancestors already have authority over their
  subtree, so `move` and `snapshot` need no new permission. Kernel
  artifacts are written into the namespace as ordinary volumes when a
  VM or agent exits: a VM's core dump, an agent's transcript and its
  final logs. A parent that wants them waits for the VMs and agents to
  exit, or stops them, then moves the artifacts out, then deletes.

A parent whose child has a lease can be warned before it expires: a
`watch` with a lead time delivers `lease.expiring`
([2.2.6](#226-leases-and-ttls)), which is the moment to move or
snapshot.

**What `delete` does.** When a namespace's deletion starts, by `delete`
or by its lease ([2.2.6](#226-leases-and-ttls)), it and every namespace
below it enter **`deleting`**, a brief state that can't be configured:

1. **Stop.** Every VM is stopped with its usual stop signal and timeout
   (for a harness, the runner's SIGTERM and 30 seconds), and every
   agent ends, with exit reason `deleted`
   ([2.2.17](#2217-exit-status-and-reason)). Nothing new can be created
   in the subtree, and creates waiting in an admission queue for it are
   dropped.
2. **Revoke.** Every grant the namespace and its subtree received is
   revoked, cascading to the grants issued from them.
3. **Delete.** Every object is deleted: kernel objects directly, and
   component-allocated handles through their allocator's `delete`
   ([Component allocators](#component-allocators)). Every charge drops
   off its payer.

Entering `deleting` is a `watch` event, carrying the cause (`delete`,
`lease` or `ancestor`), and so is the namespace's removal. Neither is a
chance to act: by the time a watcher sees `deleting`, the namespace's
VMs are already stopping and nothing can be moved out.

**The trade-off.** "Keep this when I die" needs either a deposit area
arranged up front, or a parent that keeps what it wants before it
deletes. A namespace can't decide at the last moment, on its own, to
leave something behind, and nothing can be rescued once deletion has
started, including when a lease expires unwatched. The `keep=` sugar
makes the first a one-word part of creating a child, so it is the
default pattern. The rescue during a `deleting` grace period that an
earlier revision had is dropped
([A.21](#a21-the-deleting-grace-period-and-rescue-by-move)).

#### 2.2.5 Asking for a grant

*Additional component; nothing in the kernel.* Asking for a grant is a
call to an endpoint the other party exposes. There is no request inbox
in the kernel. Inboxes, approval policies and escalation are services
agents can build ([F14](#f14-inbox-requests-need-structure)).

#### 2.2.6 Leases and TTLs

*Kernel.* Any object can have a lease, grants included. When the lease
expires, the object is deleted, or the grant revoked. For a namespace,
expiry starts its deletion ([2.2.4](#224-lifecycle-and-keeping-results)).

**A namespace has a lease and a state, and nothing else about its
lifetime.** It is deleted by `delete(ns)`, called by itself or an
ancestor, or when its optional `lease` expires. There is no lifecycle
policy.

- **Default.** No namespace gets a lease by default, so a namespace
  without one lives until something deletes it. `create_agent` is the
  same ([4.1](#41-agents-a-kernel-composite)), so a finished agent's
  chat stays readable.
- **Removing a lease** with `set_lease(ns, null)` is allowed. v1 has
  no way to require one; requiring or limiting leases in a subtree is a
  policy beyond v1 ([2.7](#27-beyond-v1-policy)).
- **Warning.** `watch` delivers `lease.expiring` for an object a lead
  time before its lease expires. The lead time is a parameter of the
  watch (`watch(path, selector, lease_warning=10m)`), not of the
  namespace, so each watcher picks its own and the namespace stores
  nothing more than its lease. Extending the lease with `set_lease`
  cancels the expiry.
- **Crashes.** A crashed VM is an exited VM, with reason `failed`,
  `oom` and so on. Its namespace is not deleted for it.

**Deleting when idle is a policy, not kernel state.** "Delete this
namespace once every VM and agent in it has exited" is built on `watch`
and `delete`:

- **By the creator.** A parent that created a job-style namespace (a CI
  run, a build, a one-shot agent) watches it, and calls `delete` when
  the last VM or agent exits, after keeping what it wants.
- **By a reaper.** An optional standard-distribution **reaper**
  component, granted `watch` and `delete` on a subtree, does the same
  for namespaces labelled for it (`reaper.shibu.dev/reap: when-idle`,
  [`reaper/v1`](shibu-spec/extension-points.md#reaperv1)). What
  counts as idle (whether a `restarting` VM or a create waiting in an
  admission queue counts as running, a settle time) is the reaper's
  rule, not the kernel's. Whatever the rule, the reaper re-checks
  idleness at the moment it deletes, so a create that landed since it
  last looked keeps the namespace
  ([F32](#f32-when-idle-races-with-a-create-in-flight)).
- **By a lease.** A job-style namespace that nobody watches carries a
  lease, which bounds it even if its creator and the reaper are gone.

The kernel has no `armed` flag, no idle check and no `idle` deletion
cause; the dropped design is in
[A.20](#a20-lifecycle-policies-delete_policy-when-idle-and-armed).

#### 2.2.7 Credentials only behind proxy endpoints

*Kernel: the secret store, `read` grants and the exposure query. The
components that hold credentials are in the standard distribution.*

A VM authenticates as itself by origin ([Identity](#identity)), and a
service that needs to know who is calling reads the signed caller
assertion ([2.2.22](#2222-caller-assertion)). Where a credential can be
attached on the way out, a proxy endpoint that holds it attaches it.
**Access to a credential means a grant on its proxy.** Holding the
credential itself means a **`read` grant** on the secret, the only way
to read one ([2.2.24](#2224-secret-store)). Proxies are the preferred
pattern because a key a VM never holds can't leak from it, but they are
a pattern, not a kernel rule.

**Who holds a credential is the secret owner's decision.** A VM or
runner can hold a secret only if the secret's owner granted `read` on
it to that VM's or runner's namespace, an ordinary grant, usually
leased and with `regrant: false`. The kernel issues no grants of its
own on secrets. Nothing else gets the value. Whether that namespace also runs code the owner
wouldn't trust with the key is a question about who else can reach it,
and the owner answers it with the exposure query, below, not with a
kernel placement rule. The kernel enforces no "a VM that holds a
credential never runs untrusted code" rule, and has no refusals tied
to holding one; that rule and its refusals are in
[A.17](#a17-trusted-and-untrusted-vms-and-runners-and-the-credential-rule).

Which namespaces hold credentials, in a standard install:

- **Distribution components that need one**: `/sys/model` for API
  keys ([2.2.23](#2223-model-access-and-budgets)), `/sys/runner` for
  the subscription and OAuth logins a CLI has to hold itself
  ([Runners](#runners)), `/sys/git` for its GitHub App key, and the
  credential proxies for GitHub, GCP, inbound HMAC and the registry
  ([F9](#f9-secrets-have-no-shibu-home)). Each receives root's keys
  through `read` grants listed in the `/sys` spec
  ([2.3](#23-the-standard-distribution-and-the-sys-spec)), for example
  `grant(/sys/secrets/gh-app-key, to=/sys/git, ops=[read])`, and any
  other owner's keys through `read` grants that owner issues, for
  example `grant(/users/alice/secrets/anthropic, to=/sys/model,
  ops=[read], lease=30d)`. A component told to use a secret by
  reference uses it only for the namespaces the `read` grant's `for`
  limit names, by default the granter's subtree, so holding its endpoint lends nobody else's key
  ([2.2.24](#2224-secret-store)).
- **Any runner whose owner was granted a login.** A team that runs its
  own runner on a subscription holds `read` on its login secret
  ([Runners](#runners)).
- **Any other namespace given a `read` grant.**

The kernel marks a namespace that holds any secret-`read` grant as
**credential-holding**. The mark is information, shown by `list`
([section 3](#3-operations)) and by the exposure query; the kernel
refuses nothing because of it. In particular:

- **No `read` grant is refused for what the grantee runs.** A `read`
  grant into a subtree that holds agent VMs, VM `mcp` endpoints or
  runners is the owner's call, made after asking the exposure query,
  below.
- **No egress rule is tied to the mark.** A credential-holding
  namespace reaches outside hosts only through its `/sys/egress` grant,
  like any other ([2.2.13](#2213-egress-policy)).
- **One restriction stays: a `read` grant names its reader.** It
  can't carry `subtree=true`, because a subtree grant would deliver the
  value to every namespace below the grantee, including ones created
  after the owner checked the exposure, so the owner's answer would
  cover readers it never named ([Grants](#grants)). An owner who wants
  several readers grants each one.

**The exposure query.** `exposure(object)` takes a VM, a runner
endpoint or a secret and answers, from the grant graph:

- for a VM: every namespace that can `exec` on it, or write files
  through its `mcp`, and through which grant chain; every namespace
  that can reach any endpoint on it; and every runner session that
  runs there;
- for a runner: every namespace holding `launch` on it, the agents with
  sessions on it now, and the VMs it runs on, with the VM answer for
  each;
- for a secret: every namespace holding `read` on it, and the VM answer for each VM in those namespaces.

The answer is computed by the kernel, so it holds whatever the VMs and
runners involved do. It can't see inside a VM: two processes in one VM
share its authority ([Identity](#identity)), so the query treats every
holder of `exec` on a VM as able to read everything that VM holds. A
secret's owner who doesn't like the answer revokes the `read` grant, or
asks the VM's owner to revoke an `exec` grant. Every grant that changes
the answer is an audit line, and a `watch` on an exposure reports the
change.

How the rest of the arrangement is kept:

- **What an image runs is its owner's decision.** The kernel can't see
  what an image runs, and can't prove one benign. Root decides for the
  distribution's own components by naming them in the `/sys` spec
  ([2.3](#23-the-standard-distribution-and-the-sys-spec)); a team
  decides for its own runners and VMs. A secret's owner who doesn't
  trust that decision doesn't grant `read`. The case where root itself
  installs a hostile credential-holding image is
  [F31](#f31-root-can-still-install-a-malicious-credential-holding-image).
- **The distribution's runner is configured to keep tools out.**
  `/sys/runner` disables every CLI's built-in tools and gives each
  session only the VM `mcp` endpoints in its agent's `tools`, so no
  tool call runs beside its logins. That is how `/sys/runner` is built
  and what its conformance suite checks ([Runners](#runners)); the
  kernel doesn't enforce it, and a granter who relies on it is
  trusting root's choice of runner.
- **Revoking a `read` grant stops further reads.** It can't un-read a
  value the holder already has, so the remedy for a key that may have
  leaked is rotation, writing a new value ([2.2.24](#2224-secret-store)).

#### 2.2.8 Orchestration surface

*Kernel.*

- `get`, `list` and `watch`, where `watch` is an event stream, and
  `get` of a grant or an allocator includes its usage;
- labels and metadata on every object;
- an audit and cost log, `audit` and `cost`;
- **grant last use**: the kernel records when each grant was last
  used, `list` can filter grants by it, and `cost` and `audit` show the
  grants under a path that went unused in their period. There is no
  automatic expiry beyond leases
  ([F23](#f23-grant-sprawl-in-long-lived-namespaces));
- **`batch`**: a list of kernel operations run as one, all or nothing,
  with an idempotency key and a dry run.

Declarative `apply` of a whole subtree, its grants included, is not in
the kernel. It is the standard-distribution **reconciler**, which diffs
a document against `list` and commits the difference with one `batch`;
`/sys` is reconciled by the same component
([2.3](#23-the-standard-distribution-and-the-sys-spec)). A `control`
endpoint's grantable ops are just `read` (get, list, watch, audit,
cost) and `write` (everything else).

#### 2.2.9 Time-based budgets

*Kernel.* Allocators and allocator grants can carry budgets over time
as well as point-in-time limits. Every budget has one shape,
`{unit, per_day | total}`, with the units `usd`, `tokens`, `hours`,
`turns`, `cpu_hours` and `vm_hours`: `{unit: usd, per_day: 20}` is
$20 a day. A grant on a metered
endpoint, such as `/sys/model`, can carry one too; the endpoint enforces it
([2.2.23](#2223-model-access-and-budgets)). A budget can also be in
hours or turns per day, which is how a session on a subscription is
limited; the runner enforces it, as it does every limit in a `launch`
grant ([Trust](#trust)).

#### 2.2.10 Stable IDs

*Kernel.* Described [above](#namespace).

#### 2.2.11 No attenuated tokens

*Kernel: not in v1 (1762).* v1 has no attenuated tokens: no call mints
a token that narrows its holder's authority. With grants and ambient
identity ([Identity](#identity)), the one job left for such a token
would be narrowing authority inside your own VM, for a subprocess or a
callback, and a token can't do that job:

- **A VM is the trust boundary.** Every process in a VM has the VM's
  ambient authority, so a narrowed token can't protect a process from
  code running beside it. Code that must not have the authority runs
  in another VM, in a sub-namespace that holds only what it is granted
  ([4.2.5](#425-worker-agents-and-their-sandboxes)).
- **Anything outside the VM gets a grant**, which is durable, audited,
  revocable and bound by lease ([Grants](#grants)).
- **External callers**, such as webhooks, come in through
  `/sys/ingress`, to an endpoint granted to it
  ([2.2.12](#2212-pre-approved-publish)), with an ingress verifier in
  front of it
  ([F9](#f9-secrets-have-no-shibu-home)).

A runner's session binding is not a token in this sense. It is
kernel-internal, the way a runner acts as an agent's namespace
([Identity](#identity)). A bearer grant, for a recipient known only by
the credential it presents, is noted beyond v1
([2.5](#25-beyond-v1-bearer-grants)).

#### 2.2.12 Pre-approved publish

*Standard distribution: `/sys/ingress`.* Public exposure is a grant:
a namespace publishes an `http` endpoint by **granting it to the
standard-distribution `/sys/ingress` component**, which serves the
public URL and decides who may reach it, an audience (people or
groups, behind an IAP-style sign-in it runs) or anyone, from the
grant's labels. Revoking the grant unpublishes it. There is no
public-endpoint object, allocator or `endpoints` dimension
([A.26](#a26-the-public-endpoint-kind-and-its-allocator)).

```
grant(lead/vm-hook, to=/sys/ingress, ops=[POST], as=factory-lead-webhook,
      labels={ingress.shibu.dev/audience: anyone,
              ingress.shibu.dev/verifier: /factory/lead/gh-hook-verify})
# → https://ingress.<shibu>/factory-lead-webhook
```

Who may publish is whoever may grant the endpoint, which by default is
its owner; `/sys/ingress` may apply policy of its own, such as which
namespaces may use `anyone`. Pre-approval is then root granting a
namespace's endpoints nothing special: an owner that may not publish
asks someone who may ([2.2.5](#225-asking-for-a-grant)). Beyond v1, a
`tcp` route on an L4 load balancer is the same component's business
([2.4](#24-beyond-v1-native-port-endpoints)).

#### 2.2.13 Egress policy

*Kernel: no route out but the web proxy. Standard distribution:
`/sys/egress`.* There is no egress policy. Every VM can always reach
`shibu.internal` (the control API) and its own namespace's endpoints,
and reaches any other Shibu endpoint exactly as its grants allow.
**Outside access is a grant** on the standard-distribution
`/sys/egress` endpoint, a forward proxy, with a `hosts` limit of
`host:port` globs. No grant means no outside access.

```
grant(/sys/egress, to=/factory, ops=[tunnel], limits={hosts: ["*:443"]}, regrant=true, as=egress)
grant(factory/egress, to=factory/ci, ops=[tunnel],
      limits={hosts: ["proxy.golang.org:443", "registry.npmjs.org:443"]})
```

It narrows, leases, revokes and audits like any grant, and revoking it
closes every connection opened through it. What used to be `none` is
holding no such grant; what used to be "specific endpoints only" is
what grants already give. The egress objects, the `egress` allocator,
`set_egress` and inheritance rules are gone
([A.23](#a23-egress-policy-egress-objects-and-the-egress-allocator)).

#### 2.2.14 Images

*Kernel.* Images are read-only volumes, made by `create` with
`from: {oci: …}` or from another volume. A VM can boot from an image it can
see. A shared image store is typically granted down with `subtree=true`.

#### 2.2.15 Placement classes and restart policy

*Kernel.*

- Placement classes: standard, spot/preemptible, GPU, region. Each
  class is offered by a backend driver, and the `/sys` spec maps it to
  one ([Backend drivers](#backend-drivers)). Each kernel VM allocator
  (`/vm-std`, `/vm-spot`) offers one class, so which class a grantee
  may use is which allocator it is granted; there is no `classes`
  limit.
- Restart policy: `never`, `on-failure`, `always`.

#### 2.2.16 Control-plane rate limits

*Punted; in no layer yet.* Rate limits are not in the kernel. When
they are added they are a *rate* limit, a token bucket at each link of
the grant chain, not a count limit
([F11](#f11-rate-limits-should-be-hierarchical)). Until then a runaway
namespace is bounded by its quota and stopped by its parent
([4.2.11](#4211-failure-cases)).

#### 2.2.17 Exit status and reason

*Kernel.* VMs and agents exit. Namespaces don't: they have a state,
`active` or `deleting`, and are deleted
([2.2.4](#224-lifecycle-and-keeping-results)). A namespace has no
exit status or reason.

**VMs.** Every VM has:

- a status: `running`, `restarting`, `exited` or `preempted`;
- once it exits, an exit code and a reason: `completed`, `failed`,
  `preempted`, `oom` or `deleted` (stopped because it, or its
  namespace, was deleted);
- a restart count.

Each status change is a `watch` event. A VM preempted three times and a
test that failed three times look different to the parent.

**Agents.** An agent exits when it calls `exit(status, reason)` because
its job is done, or when its creator or an ancestor stops it through its
control endpoint, or a grant of `stop` on it. It is `running` or
`exited`. Once it has exited it has the status it reported
(`succeeded`, `failed`) and a reason: the one it gave, or `stopped`,
`out-of-quota` (its model budget ran out, or a vendor's limit lasted
beyond its session's lease,
[6.10](#610-per-vendor-rate-limits-on-shared-subscriptions)) or
`deleted`.

- Exiting ends its harness session and its VMs.
- The status and reason live on the **agent object**
  ([4.1](#41-agents-a-kernel-composite)), and each change is a `watch`
  event.
- Its `messages` and transcript stay readable for as long as its
  namespace exists ([2.2.25](#2225-messages)).

The parent watches for these, and the UI shows them.

#### 2.2.18 Human as a principal

*Kernel.* The UI acts *as* a namespace, through the same API. There is no
separate admin path.

- **One namespace per human.** Each human principal acts as their own
  namespace, `/users/<name>`. It is the home for:
  - their credentials, in the secret store
    (`/users/alice/secrets/anthropic`);
  - the agents they create there;
  - the chats shared with them, as grants of other agents' `messages`
    endpoints ([2.2.25](#2225-messages)).

  It also gives grants to a person somewhere to land: `to=/users/alice`.
- **Root.** The installer, or a root operator, acts as `/` when doing
  root-level things, such as granting root allocators or bootstrapping
  ([4.1](#bootstrap)).
- A person's identity is mapped to the namespaces they may act as: their
  own, and `/` for root operators. The UI acts as one of them at a
  time, and defaults to `/users/<name>`.

Like any namespace, `/users/<name>` holds nothing until something is
granted to it. The root grants it whatever that person may spend.

#### 2.2.19 Dry-run and quota preview

*Kernel.* `create`, `grant` and `batch` accept a dry run that reports what
would be created and what it would charge along the grant chain, without
creating anything.

#### 2.2.20 API versioning and idempotency keys

*Kernel.* The API is versioned. `create` and `grant` accept an idempotency
key, so an agent that retries after a timeout doesn't create two of the
same thing.

#### 2.2.21 Admission queue

*Kernel, minimal.* `create` takes an optional `wait=<timeout>`. When the
allocator or grant is full, anywhere along its chain, the request queues
FIFO and is admitted as charges free up. Waiters are visible on `watch`
and in the UI, so "the factory is full" is visible rather than hidden in
retry loops.

It is in the kernel because only the allocator sees charges drop off.
Priority and fairness policies, such as a queue keyed on a label, are
additional components.

#### 2.2.22 Caller assertion

*Kernel.* The web proxy tells every endpoint who is calling, in a form the
endpoint can verify.

- It strips any incoming `Shibu-*` headers.
- It stamps a **signed**, short-lived `Shibu-Caller` assertion
  (JWT-like) on every request it forwards, carrying:
  - the caller's namespace stable ID and path, and the VM ID, from
    ambient identity ([Identity](#identity));
  - the grant ID the request came through, with its `ops` and `limits`;
  - `via=runner` and the runner's endpoint when the request came from a
    harness session acting as the agent's namespace through a session
    binding ([Identity](#identity));
  - for a caller with no VM, who it is
    ([6.9](#69-callers-with-no-vm)): a front-door caller's assertion
    names both the namespace it acts as and the person (identity
    provider and subject); an anonymous caller of an endpoint published
    through `/sys/ingress` arrives with an assertion that names
    `/sys/ingress` and marks the caller as anonymous;
  - on a request *to* a runner (a `launch`), the `launch` grant's
    limits: the adapters, models, budget, sessions, isolation and
    lease the granter allowed. The runner applies them; the kernel has already
    checked the op, the lease and the grant chain
    ([Trust](#trust)).
- Shibu publishes the verification key on a well-known endpoint. A
  service can verify the assertion even when it is not directly behind
  the proxy, and apply its own policy: a git service allowing only `mq`
  to push to `main`, for example.

**Carried per transport.** What the assertion says is the same however
the caller connects; how it reaches the service depends on the
transport.

- **HTTP:** the signed `Shibu-Caller` header, on every request.
- **Tunnels (v1):** the header is stamped once, on the upgrade request
  that opens the tunnel, and the grant is checked there
  ([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)). The
  server-side shim can pass it on to the local service. The
  assertion's short lifetime does not bound the tunnel: the kernel
  closes the tunnel when the grant ends ([Grants](#grants)).
- **Native TCP (beyond v1):** mTLS or PROXY protocol v2
  ([2.4](#24-beyond-v1-native-port-endpoints)).

#### 2.2.23 Model access and budgets

*Kernel: `launch` grants on runners and the meter. Standard
distribution, bootstrap set: `/sys/model`.*

**The model is a launch parameter.** There is no kernel object for a
model connection. A harness is launched by a runner with
`launch(adapter, model, budget, session binding, tools)`
([Runners](#runners)), and *which* model is one of those arguments. How
the runner reaches that model, whether with an API key, a subscription
or OAuth login, or a self-hosted model, is the runner's own
configuration.

**Model access is a grant on a runner.** Who may use which models is a
grant on a runner endpoint (an `http` endpoint speaking `runner/v1`)
with the contract op `launch`, optionally limited by `adapters`,
`models`, `budget`, `sessions` (how many at once) and a lease:

```
grant(/sys/runner, to=/factory/lead, ops=[launch],
      limits={adapters: [claude-code], models: [claude-opus-*],
              budget: {unit: usd, per_day: 200}, sessions: 20}, regrant=true)
```

The runner's own policy maps each grantee to one of the accounts it is
configured with (a team's API key, an organisation's subscription
seat). It sees the grantee and the grant's limits in the caller
assertion of every `launch` ([2.2.22](#2222-caller-assertion)). A
namespace that hands model access down does so by granting its runner
handle onward, narrowed, like any other grant.

**`/sys/model`** is one way for a runner to reach models without
holding a key. It is a root-level model proxy in the standard
distribution. A harness calls it with a base URL and a dummy key, and
`/sys/model` attaches the key from the secret store outside the VM,
meters tokens and cost, and charges them against a grant on
`/sys/model`. A budget there is a grant limit
(`{unit: usd, per_day: 20}`), and `/sys/model` refuses calls once it
is spent. The key it attaches is named by the grant's `credential`
limit, a reference to a secret whose owner granted `/sys/model` `read`
on it. A runner that uses
`/sys/model` passes the launching agent's grant on it to the session,
so the meter charges the agent's chain. It is no longer the definition
of model access: a runner with its own key, or its own model, never
touches it.

**Subscriptions and OAuth logins can't be proxied.** A vendor CLI on a
subscription drops out of subscription mode when its base URL is
redirected, so there is no way to put `/sys/model`, or any proxy, on
that path. The login lives in the runner: the runner's namespace holds
a `read` grant on the login secret from the secret's owner
([2.2.24](#2224-secret-store)), and the CLI talks to the vendor
directly from the runner. Budgets for those sessions are in time or
turns (`{unit: hours, per_day: 4}`, `{unit: turns, total: 500}`,
[2.2.9](#229-time-based-budgets)), and the runner enforces them.

**Billing.** A runner reports usage per session, in tokens, money,
hours or turns, through the meter, charged to the launching agent's
payer along its `launch` grant's chain. Whether to accept a runner's
reports is the payer's and the granter's choice: a granter who doesn't
want to rely on a runner's reports, or on its enforcement of
`budget`, grants model access only through runners that reach models
through `/sys/model`, whose metering happens outside the runner
([Trust](#trust)). A payer accepts a runner's reports with
`limits={usage: reported}` on the `launch` grant; without it the
reports appear in `cost` marked **reported** and are not charged, and
a session whose runner stops reporting is stopped at the end of its
lease ([6.13](#613-accepting-a-runners-usage-reports)). grain's
`pkg/modelproxy` does no metering today
([7.1](#pkggitproxy-and-pkgmodelproxy)).

#### 2.2.24 Secret store

*Kernel.* Model credentials are needed before any agent exists, and
whatever stores secrets can impersonate everyone, so a **versioned**
secret store is part of the kernel
([Layers](#layers-kernel-standard-distribution-additional-components)).

- `put_secret(ns/name, value)` writes a new value. It is write-only:
  there is no `get`, and writing a secret doesn't let the writer read
  it back.
- A secret is an object in the store, scoped to a namespace, typically
  `/users/<name>` (`/users/alice/secrets/anthropic`) or `/sys/secrets`
  for the distribution's own keys. It has two ops, granted like any
  other ([Grants](#grants)): narrowing only, cascading revoke, leases,
  and no passing on without `regrant=true`.
- **`write`** lets the grantee `put_secret` a new value, and nothing
  else. The owner has it by the tree; a runner that runs a login flow
  for a secret is granted it.
- **`read`** lets the grantee read the value:
  `grant(/sys/secrets/gh-app-key, to=/sys/git, ops=[read])`. A `read`
  grant is **the only way to read a secret**, and the grantee's
  namespace is marked credential-holding. Which namespaces get one is
  the secret owner's decision, and the kernel refuses none because of
  what the grantee's subtree runs. Before granting, the owner asks the
  exposure query who could reach the VMs the value would be delivered
  to ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). A `read`
  grant names its reader: it can't carry `subtree=true`
  ([Grants](#grants)). The usual form is leased, with
  `regrant: false`.
- **References.** Anything that needs a key attached on someone's
  behalf (a runner, `/sys/model`, a credential proxy) holds its own
  `read` grant from the owner; a caller names the secret to it by path,
  a reference, and never holds `read` itself. A component uses a
  reference only for the namespace that granted it `read` on that
  secret, within that grant's `for` limit (path globs; by default the
  granter's subtree), so holding the component's endpoint lends no one
  else's key. There is no `use` grant and the kernel issues
  no grants on secrets ([A.25](#a25-use-grants-bound-reads-version-retirement-and-the-kernel-login-flow)).
- A secret is written by a human through the UI's manifest-driven forms
  or the CLI's `shibu secret put`, which prompts from the same manifest
  ([4.1](#harness-interface)), or by the runner or the UI at the end of
  an OAuth or subscription login flow it runs, with `put_secret`. The
  kernel runs no login flows.

**Reads.**

- Every read is an audit line naming the secret, the version read, the
  reader's namespace and the `read` grant it came through.
- Revoking a `read` grant, or letting its lease expire, stops further
  reads at once. It can't un-read a value already held. The remedy for
  a value that may have leaked is **rotation**, which is just writing a
  new value. The store keeps the previous value readable for a fixed
  short overlap, the same for every secret (the `/sys` spec's
  `secrets.overlap`), so readers that cached it can finish; that is a
  property of the store, not an API, and there is no version to
  retire ([F9](#f9-secrets-have-no-shibu-home)).
- The distribution's credential-holding components hold `read` grants
  on root's keys they need, listed in the `/sys` spec
  ([2.3](#23-the-standard-distribution-and-the-sys-spec)): `/sys/git`
  on its GitHub App key, for example. Other owners' keys reach them
  through `read` grants those owners issue.
- A **runner's login**, such as a team's subscription, is a `read`
  grant from the secret's owner to the runner's namespace. The runner can hold that login only because the owner
  granted it ([2.2.23](#2223-model-access-and-budgets)).

**Storage backends.** The store's API, its access rule and its audit
are the kernel's. Where the encrypted values are kept is pluggable:
built-in (in the kernel's own database, encrypted at rest), HashiCorp
Vault, or GCP Secret Manager. This works like Kubernetes Secrets with
KMS providers. The backend is chosen in the `/sys` spec, but its code
runs inside the kernel: it is not a distribution component, because
anything that can read every secret is part of the trusted computing
base wherever it runs.

Rotation and credential-proxy kinds
([F9](#f9-secrets-have-no-shibu-home)) build on this same store.

#### 2.2.25 `messages`

*Kernel.* Every agent has a `messages` endpoint: a two-way channel
between the agent and its creator, whether a human or a parent agent.
It is grain's chat, generalised.

- It is per agent and not routed, so it is not a request inbox
  ([2.2.5](#225-asking-for-a-grant)).
- An agent is always created in its creator's subtree
  ([4.1](#41-agents-a-kernel-composite)), so the creator reaches its
  `messages` by the tree's implicit authority. Nothing has to be
  granted.
- The UI renders as a chat session every `messages` endpoint the person
  can reach, whether in their subtree or granted to them. So the UI
  lists every agent a person can talk to, without any per-agent code.
- **Sharing a chat is a grant.** `messages` is an `http` endpoint, so
  `grant(<agent>/messages, to=/users/alice, ops=[write])` lets Alice
  read and post (`GET` and `POST`), and a read-only observer gets
  `ops=[read]`.
- A parent agent holds its workers' `messages` endpoints. It can talk to
  them itself, or grant a person access when a person needs to step in.
- `messages` outlives the agent's exit. It stays readable for as long
  as the agent's namespace exists, which without a lease is until
  somebody deletes it
  ([2.2.6](#226-leases-and-ttls)).
- **The history is the kernel's** ([6.11](#611-where-an-agents-messages-history-lives)).
  It is kept with the agent object, in the agent's namespace, in the
  kernel's state store ([2.2.27](#2227-kernel-state-store)), and lives
  exactly as long as that namespace. It is capped per agent at a stated
  size, dropping the oldest messages first. Anyone holding `read` on
  `messages` can read all of what is kept, including a grantee added
  later. Results meant to outlive the agent go to a deposit area
  ([2.2.4](#224-lifecycle-and-keeping-results)).

#### 2.2.26 Non-HTTP services: tunnels over HTTP endpoints

*Kernel: upgrades in the web proxy, the `tunnel` op and the rule that
revoking closes connections. Standard distribution, optional: the
shim.*

In v1, Shibu serves non-HTTP services, such as a database, by
tunnelling TCP inside HTTP endpoints. Servers and clients stay
unmodified. The kernel needs no L4 path: everything still enters
through the web proxy as an HTTP request.

**HTTP endpoints accept upgrades.** The web proxy supports a WebSocket
upgrade and HTTP `CONNECT` on any HTTP endpoint. Once the upgrade
succeeds, it carries raw bytes in both directions until either end
closes or the grant ends.

- **The checks happen when the tunnel is opened.** The upgrade request
  is an ordinary request to the endpoint. The proxy checks the grant
  on it, including its paths and methods, and stamps the caller
  assertion on it ([2.2.22](#2222-caller-assertion)). Nothing is
  checked per byte after that.
- **`tunnel` is a separate op.** A grant allows an upgrade only if its
  `ops` include `tunnel`, so plain HTTP access and tunnelling are
  granted separately. `ops=[GET]` lets a caller read a service's
  status page but not open a tunnel to it. The endpoint's owner, and
  anything holding it by the tree, has `tunnel` like every other op.
- **Limits are coarse.** A `tunnel` grant can carry `max_connections`
  (open tunnels at once), `bandwidth`, and `max_duration` for each
  tunnel. These are the HTTP endpoint's narrowing vocabulary for
  tunnels ([Grants](#grants)).
- **Revoking closes the tunnel.** Tunnels are long-lived, so revoking a
  grant, letting its lease expire, or the grant becoming invalid under
  the validity rule
  ([F13](#f13-what-happens-to-an-alias-when-its-target-moves-or-dies))
  closes every tunnel and connection made through it
  ([Grants](#grants)).

**The shim.** It is an optional component of the standard distribution
([Layers](#layers-kernel-standard-distribution-additional-components)),
shipped in the base images as the `shibu` command. There is one
command for each end.

- **Client side: `shibu forward <endpoint> <local-port>`.** For
  example, `shibu forward factory/svc/db 5432`. It listens on
  localhost, and opens a tunnel through the web proxy for each
  connection it accepts. Ordinary `psql`, drivers and ORMs connect to
  `localhost:5432`.
- **Server side: `shibu serve <endpoint> --to localhost:5432`.** It runs
  next to the service, in the same VM, and listens on the port the
  endpoint points at. It accepts upgrades from the web proxy and
  forwards the tunnelled bytes to the local port.
- **The shim uses WebSocket by default**, because it passes through
  ordinary HTTP intermediaries. `CONNECT` is there for clients that
  speak it already, such as an SSH `ProxyCommand` or `curl
  --proxytunnel`.
- **Passing the caller on.** `shibu serve` can hand the caller
  assertion to the local service as a PROXY protocol v2 header, with
  the signed assertion in a TLV, for services that read one. Otherwise
  the service sees every connection as coming from localhost.
- **Optionally transparent.** The shim can hide the localhost hop
  through config: an `/etc/hosts` entry that points the service's
  usual name at localhost, or a helper that writes the connection
  string (`DATABASE_URL=postgres://localhost:5432/app`). This is an
  option, not a requirement.

**What it works for.** Postgres, MySQL, Redis, SSH and other plain-TCP
protocols, with no change to servers or clients. gRPC needs nothing
extra: it is already HTTP/2, so it is an HTTP endpoint as it stands.
[4.2.3](#423-shared-services-in-factorysvc) has a Postgres example.

**Trade-offs.**

- There is a shim process at each end.
- Each connection pays a little added latency for the extra hop and
  the framing.
- Narrowing goes no finer than "may open a tunnel", plus the
  connection, bandwidth and time limits. The proxy can't see inside the
  protocol. Finer policy, such as read-only Postgres access, belongs to
  one of two places. It can belong to the service, which applies it to
  the identity in the tunnel's caller assertion where the server-side
  shim passes it on. Or it can belong to a protocol-aware proxy
  component in front of the service.

**Credentials for non-HTTP upstreams.** A credential-injecting proxy for
a non-HTTP protocol (a Postgres proxy that logs in with a stored
password, an SSH proxy that holds a key) is a component behind a
tunnel. It receives its secret through a `read` grant, which makes its
namespace credential-holding; like the other credential-holding
components, it is normally named in the `/sys` spec with that grant
([2.2.7](#227-credentials-only-behind-proxy-endpoints),
[F9](#f9-secrets-have-no-shibu-home)).

Native `tcp` endpoints, which remove the shim, come beyond v1
([2.4](#24-beyond-v1-native-port-endpoints)).

#### 2.2.27 Kernel state store

*Kernel. Decided for v1 (1904); the store as a VM is Open.*

Everything the kernel knows lives in one store: namespaces, VMs and
volumes as objects, endpoints, grants and their chains, leases,
labels, agent objects and their `messages`, the meter's charges, the
audit log and, for the built-in backend, the secret store's encrypted
values ([2.2.24](#2224-secret-store)).

**v1: embedded SQLite.** The kernel keeps its state in an SQLite
database inside the kernel process, on the kernel host's disk. There
is no separate database service, so there is nothing to start before
the kernel and no startup-ordering problem: the kernel opens its file,
loads its drivers and starts the reconciler
([Bootstrap order](#bootstrap-order)). A deployment backs the store up
by copying the database file consistently (SQLite's online backup), on
the kernel host.

**A replaceable contract, `state/v1`.** The store is behind a contract
the kernel calls, not a schema the API exposes. Like the secret
storage backend, its code runs inside the kernel, because whatever
holds the kernel's state can rewrite every grant. The contract asks
for:

- transactions that are serializable across the objects one operation
  touches, which `batch`, the grant-chain walk and cascading revokes
  rely on ([2.2.8](#228-orchestration-surface),
  [Implementation note](#implementation-note-walking-the-grant-chain));
- durability when a commit returns;
- an ordered change log, which `watch` and `audit` are served from;
- an index of grants by the grant they were issued from
  ([F22](#f22-cascading-revokes-can-be-expensive)).

v1 ships one implementation, SQLite. A Postgres-compatible store can
replace it later without any change to the kernel API, the web proxy
or any component: nothing outside the kernel sees which store it uses.
When Postgres comes in is bsalmon's decision, with the option below.

**Future option, Open: the store as a bootstrap root VM.** Recorded,
not decided. A Postgres-compatible store could run as a VM in the tree,
for example `/sys/state`, instead of beside the kernel:

- **Started by the driver at boot.** The backend driver starts it
  directly when the kernel boots, not through an allocator, since no
  allocator can run before the store does
  ([Backend drivers](#backend-drivers)).
- **Pinned.** It can't be deleted, moved or adopted, and it has no
  lease.
- **Visible like any VM.** It is in the tree, with `watch`, `audit`
  and `cost`, and its volume is backed up with volume snapshots
  ([2.2.3](#223-volume-snapshot-and-clone)).
- **Recovered without itself.** It needs a recovery path that doesn't
  depend on the store: the driver can start it, and an operator can
  restore it from a snapshot, with only the driver's configuration on
  the kernel host.

The trade-offs are why it is open:

- **Bootstrap ordering.** The kernel needs the store before it can
  load anything that is kept in it, so the driver and the store's VM
  have to be configured outside the store, on the kernel host, and the
  kernel can't serve a single call until the VM is up.
- **Circular failure.** The store's VM is a Shibu object kept by the
  kernel whose state it holds. If the store is down, the kernel can't
  record the store's own status, and recovery has to step outside the
  model.

**Services' own databases are separate.** A service that needs a
database, such as the factory's Postgres in `svc/db`, runs it as an
ordinary VM behind an ordinary endpoint
([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints),
[4.2.3](#423-shared-services-in-factorysvc)). The kernel state store
is never reachable through an endpoint, and no grant reaches it.

### 2.3 The standard distribution and the `/sys` spec

*Kernel: the contracts, `batch`, and the plug-ins it loads. Standard
distribution: the components the spec names, and the reconciler, which
applies the spec and keeps its history.*

#### Contracts, not implementations

The kernel depends on the standard distribution only through **named,
versioned contracts**, never through an implementation. Each contract
comes with a **conformance test suite** that any implementation has to
pass.

| Contract | Endpoint | Class | What the kernel relies on |
| --- | --- | --- | --- |
| `model` | `/sys/model` | bootstrap | one way for runners to reach models without holding a key: routes by vendor and model, attaches credentials it reads through `read` grants their owners issue it, meters and enforces budgets on grants outside any runner ([2.2.23](#2223-model-access-and-budgets)) |
| `runner` | `/sys/runner`, and any VM that serves `runner/v1` | bootstrap (`/sys/runner`) | a pool runner: launches harness sessions for many agents under kernel-issued bindings, one isolated session each, with the adapters in its image; enforces the limits of the launch grant; runs logins and writes them with `put_secret`; gives each session only its agent's VM `mcp` tools ([Runners](#runners)) |
| `egress` | `/sys/egress` | bootstrap | the only way out: a forward proxy whose VMs alone have a route outside; a grant on it with a `hosts` limit is outside access ([2.2.13](#2213-egress-policy)) |
| `ingress` | `/sys/ingress` | optional | public exposure: serves each endpoint granted to it at a public URL, to the audience the grant's labels name or to anyone ([2.2.12](#2212-pre-approved-publish)) |
| `git` | `/sys/git` | optional | git smart-HTTP, with per-caller policy on the caller assertion; GitHub-backed or Shibu-hosted, and the same endpoint either way ([`/sys/git` and `/sys/forge`](#sysgit-and-sysforge)) |
| `forge` | `/sys/forge` | optional | pull requests, reviews, checks and merges on repos served by `/sys/git`; GitHub-backed or a minimal hosted one ([`/sys/git` and `/sys/forge`](#sysgit-and-sysforge)) |
| `credential-proxy` | `/sys/cred/<kind>` | optional | the `credential-proxy` and `ingress-verifier` kinds of [F9](#f9-secrets-have-no-shibu-home), for GitHub, GCP, inbound HMAC and the registry |
| `pkgcache` | `/sys/pkgcache` | optional | the package-registry cache |
| `warm-pool` | `/sys/pool`, and in any namespace that runs the image | optional | a component allocator that keeps pre-booted VMs (or pre-cloned volumes) per shape and image and hands them out with `adopt` ([2.2.2](#222-caps-reservations-and-adoption)); the kernel relies on nothing from it beyond the allocator contract |
| `reconciler` | `/sys/reconciler`, and in any namespace that runs the image | bootstrap (`/sys/reconciler`) | declarative `apply` of a subtree, and of the `/sys` spec (below): diffs a document against `list` and commits it with one `batch` ([2.2.8](#228-orchestration-surface)) |
| `agents` | `/sys/agents`; also a library and `shibu agent create` | optional | the `create_agent` helper: kernel operations plus `runner/v1` ([4.1](#41-agents-a-kernel-composite)) |
| `reaper` | `/sys/reaper`, and in any namespace that runs the image | optional | deletes labelled namespaces once every VM and agent in them has exited, using only `watch` and `delete` ([2.2.6](#226-leases-and-ttls)); the kernel relies on nothing from it |
| `ui` | `/sys/ui` | optional | the UI, served at the front door and acting only as the signed-in person through the API; CLI parity; renders the `/sys/ui` conventions; frames agent-served panes on their own origins; builds credential forms from adapter manifests ([2.6](#26-the-ui-and-the-sysui-conventions)) |

The secret store's **storage backend** and the **backend drivers** are
also chosen in the spec, but they are not contracts between the kernel
and a component: they are plug-ins the kernel loads
([2.2.24](#2224-secret-store), [Backend drivers](#backend-drivers)).
Drivers still have a conformance suite, and go through the staged swap
below.

Distribution endpoints reach every namespace as root grants with
`subtree=true`, narrowed per endpoint: `/sys/model` is granted with no
budget, so a namespace can call it only through a budget someone grants
it; `/sys/git`, `/sys/forge` and the credential proxies serve only
callers holding a grant on a scope of them.

- **Replacing** a component means starting another implementation of
  its contract and re-pointing the endpoint.
- **Turning one off** means revoking the root grant. Only optional
  components can be turned off.
- Only root can do either. A replacement inherits the credential rule
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)).

#### The `/sys` spec

`/sys` has one declarative source, the **`/sys` spec**. The kernel has
no `apply`: the bootstrap reconciler applies the spec with `batch`, as
root, as it would any subtree, and runs the staged swaps; the kernel's
only `/sys` operation is a root-only one that loads its own plug-ins
(drivers and the secrets backend) ([2.2.8](#228-orchestration-surface)).
For each contract, and for the secrets storage backend, the spec says:

- which implementation runs: an image or a repo ref, and its version;
- its shape and its `egress` hosts, which the reconciler turns into
  the component's only grant on `/sys/egress`: least privilege for a
  component that may hold keys ([Trust](#trust));
- its `reads`: which of root's secrets it may read. Other owners grant
  a component `read` on their own secrets directly
  ([2.2.24](#2224-secret-store)).

For example:

```yaml
secrets: {backend: builtin}            # or vault: {...}, gcp-secret-manager: {...}
drivers:
  granule:
    driver: shibu/driver-granule:1.0.0
    contract: driver/v1
    hosts: [host-1, host-2]            # each host's cpu, ram and disk become root allocators
    classes: [standard, spot]
  # gce: {driver: shibu/driver-gce:0.3.0, contract: driver/v1, project: my-proj,
  #       reads: [/sys/secrets/gce-sa], classes: [gpu]}
components:
  model:
    image: shibu/model:1.4.2
    contract: model/v1
    shape: {cpu: 2, ram: 4Gi}
    egress: [api.anthropic.com:443, api.openai.com:443, generativelanguage.googleapis.com:443]
    reads: []                          # key owners grant /sys/model read on their own keys
  runner:
    image: shibu/runner:2.0.1
    contract: runner/v1
    adapters: [claude-code, codex, gemini]
    egress: [vendors-of-adapters]
    reads: []                          # login owners grant /sys/runner read on their own logins
  egress:
    image: shibu/egress:1.0.0
    contract: egress/v1
    network: outside                   # the only component with a route out
  ingress:
    image: shibu/ingress:1.0.0
    contract: ingress/v1
  git:
    image: shibu/git-github:0.9.0      # or shibu/git-hosted, with a volume and no reads
    contract: git/v1
    egress: [github.com:443]
    reads: [/sys/secrets/gh-app-key]   # a grant(/sys/secrets/gh-app-key, to=/sys/git, ops=[read])
  forge:
    image: shibu/forge-github:0.4.0
    contract: forge/v1
    egress: [api.github.com:443]
    reads: [/sys/secrets/gh-app-key]
  pkgcache: {image: shibu/pkgcache:1.1.0, contract: pkgcache/v1}
  ui:
    image: shibu/ui:1.0.0
    contract: ui/v1                    # no reads: the UI holds no credential
    nav: [/grain/lead, /grain/mq]      # trusted to place panes in main navigation (2.6.6)
  reconciler: {image: shibu/reconciler:1.0.0, contract: reconciler/v1, mode: ui}
```

The reconciler issues each `read` and `/sys/egress` grant to the
component as root, in its `batch`, and revokes it when the entry goes.
Every component holding a `read` grant is credential-holding, and
`list` shows it as such
([2.2.7](#227-credentials-only-behind-proxy-endpoints)).

**Install** starts the kernel with its default plug-ins and the
reconciler, which applies a default spec baked into the install: the
standard distribution. Bootstrap needs no repo, and no network beyond the image
registry.

Root edits the spec in one of two modes.

- **UI mode, the default.** Root edits the spec in the UI. The
  reconciler applies it directly, and keeps a version history so that any earlier
  version can be applied again to roll back.
- **Repo mode, opt-in.** Root points `/sys` at a config repo.
  - The **reconciler**, switched to `mode: repo`, watches the repo and
    applies each change with `batch`.
  - The repo is then the source of truth. UI edits become pull requests
    against it, which the reconciler opens, instead of direct applies.
    This is the model grain
    already uses for its state repo, `grain/config`.
  - The kernel never needs git itself, so there is no bootstrap
    chicken-and-egg: the kernel starts, then the reconciler in UI
    mode, and only then can it reach a repo. Switching it back to
    `mode: ui` returns `/sys` to UI mode, starting from the last spec
    it applied.

#### Staged swap

The reconciler replaces any `/sys` component in four steps:

1. start the new implementation alongside the old one;
2. run the contract's conformance suite against it;
3. re-point the `/sys/...` endpoint;
4. keep the old one warm for a rollback window.

If conformance fails, nothing changes. A bad commit to the config repo,
or a bad edit in the UI, can't take out a bootstrap component.

For a backend driver, step 3 re-points new creates only. VMs already
running on the old driver can't be migrated, so they drain there, and
the old driver stays loaded until its last VM is gone
([Backend drivers](#backend-drivers)).

#### `/sys/git` and `/sys/forge`

*Standard distribution, optional.*

Repos aren't a kernel concept. Git and forge are two separate optional
contracts.

**`/sys/git`** is smart-HTTP git, with per-caller policy applied to the
caller assertion the web proxy stamps on each request
([2.2.22](#2222-caller-assertion),
[F8](#f8-proxies-need-the-callers-identity)). Who may fetch and who may
push where is the implementation's policy on that assertion. It has two
implementations, and both are supported:

- **GitHub-backed.** A proxy that reaches GitHub with the GitHub App
  key, which it holds through a secret `read` grant
  ([2.2.24](#2224-secret-store)). It is credential-holding
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). This is
  grain's `pkg/gitproxy` today ([7.1](#pkggitproxy-and-pkgmodelproxy)).
- **Shibu-hosted.** A git server, bare repos on a volume or a
  Gitea-like server, in a namespace that holds no credential. Its
  volumes are charged like any volume.

Agents see the same endpoint either way:
`https://<shibu>/sys/git/<owner>/<repo>.git`. **Both can run at once**,
for example hosted repos for scratch and internal work beside GitHub
for real projects, or a hosted mirror of a GitHub repo. The `/sys` spec
then lists both implementations, and says which repo paths each serves:

```yaml
  git:
    contract: git/v1
    implementations:
      - {image: shibu/git-github:0.9.0, repos: ["bwsalmon/*"], egress: [github.com:443],
         reads: [/sys/secrets/gh-app-key]}
      - {image: shibu/git-hosted:0.2.0, repos: ["scratch/*", "internal/*"], volume: 200Gi}
```

**`/sys/forge`** is separate from git. Pull requests, reviews, checks
and merges get their own optional contract, modelled on grain's
`pkg/forge` abstraction. It has two implementations:

- **GitHub-backed**, holding the GitHub App key through a `read` grant,
  as GitHub-backed git does. It is grain's `pkg/forge`, `pkg/github`
  and `pkg/checkpost` today.
- **Minimal hosted**, for repos served by hosted git: pull requests,
  reviews, check statuses and merges, with no issues or Actions.

Grain's merge-queue and CI components talk to `/sys/forge`, not to
GitHub ([7.2](#72-the-decision-grain-runs-on-shibu)). **A repo with git
but no forge still works for agents**: they can fetch and push. It just
has no PR flow.

#### Bootstrap order

1. The kernel opens its embedded state store
   ([2.2.27](#2227-kernel-state-store)), starts with its default
   plug-ins, then the reconciler, which applies the default spec:
   `/sys/egress` before anything that needs to reach outside, then
   `/sys/model` and `/sys/runner`.
2. The installer is `/users/<name>`, a human principal who may act as
   `/` ([2.2.18](#2218-human-as-a-principal)).
3. They set a model credential through the UI's key form, or
   `shibu secret put` ([4.1](#harness-interface)), and grant the
   runner or `/sys/model` `read` on it.
4. They run the `create_agent` helper (`shibu agent create /bootstrap …`),
   and chat with the agent over its `messages` ([4.1](#bootstrap)).
5. Everything else is built from there. That optionally includes
   switching `/sys` to repo mode.

### 2.4 Beyond v1: native port endpoints

*Kernel: the `tcp` endpoint kind, DNS names, the L4 path in the web
proxy. Standard distribution: `tcp` routes in `/sys/ingress`. Not in v1.*

This section is decided, but it is **not part of v1**. It is additive:
nothing in v1 changes, tunnels keep working
([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)), and
HTTP endpoints keep their URL folders. It is an upgrade in
transparency and performance over tunnels.

- **A `tcp` endpoint kind.** It points at a VM port and is granted like
  any other endpoint. Its narrowing vocabulary is coarse: `connect`,
  `max_connections`, `bandwidth`, and a time window.
- **DNS names for every endpoint.** Every endpoint, of both kinds, gets
  a DNS name inside VMs, such as `db.svc.factory.shibu.internal:5432`.
  - A per-caller kernel resolver answers only for endpoints the caller
    can reach ([Visibility](#visibility)). For any other name it says
    the name doesn't exist.
  - Each name maps to a virtual IP and port that the web proxy listens
    on for that VM.
  - For an HTTP endpoint the DNS name is in addition to its URL folder.
- **The web proxy gains an L4 path.** Visibility and the grant are
  enforced when the connection is made. TCP egress to outside
  hosts still goes through `/sys/egress`, by grant
  ([2.2.13](#2213-egress-policy)). Revoking a
  grant closes its connections, as for tunnels ([Grants](#grants)).
- **Caller identity is carried per transport**
  ([2.2.22](#2222-caller-assertion)). The assertion's content is
  unchanged.
  - Preferably **mTLS**: the proxy presents a short-lived client
    certificate identifying the caller's namespace and grant,
    SPIFFE-style.
  - Or else **PROXY protocol v2**, with the signed assertion in a TLV.
  - For a service that reads neither, the grant is enforced at connect
    time only, and the service doesn't learn who is calling.
- **`/sys/ingress` gains `tcp` routes**: a port on an L4 load
  balancer, for a `tcp` endpoint granted to it
  ([2.2.12](#2212-pre-approved-publish)).

**What it buys over tunnels:** no shim, real service names, and less
latency.

**UDP is deferred further.** Identity per flow is harder to carry, and
QUIC and HTTP/3 can ride on the HTTP kind.

### 2.5 Beyond v1: bearer grants

*Kernel. Not in v1, and not decided: to be added only if a real case
appears (1762).*

A **bearer grant** would be a grant whose recipient is whoever presents
the credential it returns, rather than a namespace known by origin. It
would be revocable and bound by lease like any grant, narrowed like
any grant, and audited at each use. It is the one thing attenuated
tokens could do that a grant can't: give access to a caller with no
namespace. v1 has no case for it. Callers outside the VM get grants,
and external callers come in through `/sys/ingress`, to an endpoint
granted to it, with an ingress verifier ([2.2.11](#2211-no-attenuated-tokens)).

### 2.6 The UI and the `/sys/ui` conventions

*Standard distribution: the `ui` contract and `/sys/ui`, its default
implementation. Kernel: the API and its reference CLI, the front door,
the `messages` and `artifacts` endpoints, labels, and the web proxy's
pane origins. The conventions themselves are names and schemas on
kernel objects. The kernel stores them and, except where this section
says so, doesn't interpret them.*

Decided by bsalmon in the director chat (grain task 1766, 2026-10-03),
and written up in task 1771.

#### 2.6.1 The UI is a replaceable component

Everything a person does in Shibu can be done through the API, and so
from the CLI. The UI is a principal acting as a namespace through that
same API, with no backdoor ([2.2.18](#2218-human-as-a-principal)). So
it doesn't have to be the kernel's. It is a **standard-distribution
component**, `/sys/ui`, behind a `ui` contract, and root can replace it
as it replaces `/sys/git`
([2.3](#23-the-standard-distribution-and-the-sys-spec)).

- **It holds no authority of its own.** `/sys/ui` serves the UI's
  assets at the front door. The UI runs in the person's browser, which
  authenticates at the front door like any caller with no VM
  ([Identity](#identity)), and calls the API as the namespace the
  person is acting as. `/sys/ui` holds no grants beyond its root grant
  and no `read` grant on any secret, so it is not credential-holding.
- **It is still trusted with what people type into it.** A person
  enters credentials in the UI's own forms
  ([4.1](#harness-interface)), so a malicious UI image could take them
  on the way to `put_secret`. Choosing the image is root's trust
  decision, as it is for any credential-holding image
  ([F31](#f31-root-can-still-install-a-malicious-credential-holding-image)).
  Only root can replace `/sys/ui`, and only through the `/sys` spec.
- **It is optional.** Without it, Shibu is driven from the CLI.
  `create_agent` doesn't depend on it.

**The conformance rule is CLI parity.** Anything a UI can do, the CLI
can do. The CLI, `shibu`, is the kernel's reference client of its API
([section 3](#3-operations)), so a UI that needed something the CLI
couldn't do would be using something that isn't in the API. The UI
mostly adds rendering: it can draw controls where the CLI prints text.
The `ui` contract's conformance suite drives both through the same
scenarios. The commands are listed in
[CLI parity](#cli-parity) in section 3. For example:

```
shibu chat factory/lead                       # the agent's messages, as a chat in the terminal
shibu approve factory/desk/requests/31        # answer a request (a convention command), as the acting namespace
shibu open factory/lead/pipeline              # open a pane in a browser, through the proxy
```

The rest of 2.6 is how agents appear in whichever UI is running. Agents
never talk to a particular UI. They publish standard endpoints and
labels, and any UI that conforms to the `ui` contract renders them, as
the CLI does.

#### 2.6.2 `messages` types

`messages` is kernel ([2.2.25](#2225-messages)). Each message carries a
`type` from a small vocabulary, and every message also carries a
`text` field, so a client that knows none of the types still has
something to show.

| Type | Carries | Answered by |
| --- | --- | --- |
| `text` | `text` | |
| `question` | `text`, and optional `choices` | a reply naming the question, with a choice or free text. The agent blocks until it is answered |
| `approval` | `text`, and what is to be approved | a reply naming it, with `approve` or `deny` and a reason |
| `progress` | `text`, and an optional fraction done | |
| `attachment` | a file, or a link, with a name and a media type | |

- A reply is itself a message, with `reply_to` naming the question or
  approval. Answering needs `write` on the endpoint, like any message.
- A terminal renders every type as text: a question's choices as a
  numbered list, an approval as a prompt for `approve` or `deny`. A
  rich UI may draw buttons, a progress bar or a file preview.
- The harness adapter maps `question` and `approval` onto the CLI it
  runs, as it maps `messages` itself ([4.1](#harness-interface)). While
  one is unanswered, the agent's `phase` is `waiting-on-human`
  (2.6.3).
- An `attachment` that names a volume or a Shibu endpoint is a
  reference, not a grant. A reader who can't reach the volume can't
  open it.

#### 2.6.3 Status labels

Every agent object carries three labels, set with `label`, so that
every UI can draw the same list of agents without any per-agent code:

- `title`: what the agent is, in a few words ("planner",
  "item 17: retry the flaky upload");
- `status`: one line on what it is doing now;
- `phase`: one of `working`, `waiting-on-human`, `done` or `failed`.

The agent sets `title` and `status`, and its creator may set `title`
when it creates it. The adapter sets `phase` to `waiting-on-human`
while a `question` or `approval` it posted is open, and back to
`working` when it is answered. The kernel sets `phase` when the agent
exits: `done` for `succeeded`, `failed` for anything else, with the
reason on the agent object as before
([2.2.17](#2217-exit-status-and-reason)).

A UI sorts and filters on `phase`. "Everything waiting on me" is
`list(path, selector=kind:agent,phase:waiting-on-human)` over the
person's subtree and the grants they hold.

#### 2.6.4 Artifacts

Every agent has a standard **`artifacts`** endpoint, next to `messages`
([4.1](#harness-interface)). It is where an agent lists what it
produced, instead of burying it in chat text. Each entry has a kind
(`pr`, `url`, `file` or `volume`), a title and a reference, and the
agent adds entries as it goes. It is append-only.

- A UI shows an agent's artifacts beside its chat. The CLI lists them
  with `shibu artifacts <agent>`.
- Reading them needs `read` on `<agent>/artifacts`. The creator has it
  through the tree; anyone else is granted it, as for `messages`.
- As with attachments, an entry is a reference. A `volume` entry
  doesn't give the reader the volume.
- `artifacts` stays readable, like `messages`, for as long as the
  agent's namespace exists.

Any other namespace, such as a service or a component, may serve an
`artifacts` endpoint of the same shape, and a UI shows it the same way.

#### 2.6.5 Requests stay in the API

Requests for quota, for a grant or for a secret go through grants and
the request path that already exists: a call to an endpoint the other
party exposes, by convention `requests`
([2.2.5](#225-asking-for-a-grant),
[F14](#f14-inbox-requests-need-structure)), or an `approval` message to
the agent's creator (2.6.2).

- A UI only displays them, and answers them as its principal. An answer
  is a `grant`, a smaller grant, a lease, or a reply with `deny`. The UI
  can approve nothing that the namespace it is acting as couldn't
  grant through the API.
- `shibu approve <request>` does the same from the CLI. It is a
  **convention command**: it is `grant(...)` followed by
  `POST <ns>/requests/<id>/answer` on the `requests` endpoint, so a
  namespace that serves no `requests` endpoint has no requests to
  approve ([CLI parity](#cli-parity)).
- **A request for a secret is answered in the UI's own form**, never in
  chat or in a pane. The person stores the value with `put_secret`
  through the form (or `shibu secret put`), then grants `read` on it to
  whatever will use it for the agent (its runner, `/sys/model` or
  `/sys/cred`), leased, and answers with the reference. The agent never
  sees the value
  ([2.2.24](#2224-secret-store)).

#### 2.6.6 Agent-served panes

An agent, or any component, can add a pane to the UI by serving it from
one of its own endpoints, in any namespace. This decides
[F30](#f30-component-ui-panels).

**Declaration.** A namespace lists its panes at a well-known endpoint,
`<ns>/ui/panes`:

```
GET <ns>/ui/panes →
[
  {id: "pipeline", title: "Pipeline status", path: "<ns>/ui/pipeline",
   icon: "chart", placement: "tab"},        # placement: tab | sidebar | full
]
```

`path` is an HTTP endpoint in the namespace's own folder, by
convention `<ns>/ui/<id>`, served by its VM like any other endpoint
([Endpoint](#endpoint)). Placement is a hint. `tab` puts the pane in a
tab on the agent's page, `sidebar` puts it beside the agent's chat, and
`full` gives it the whole content area, still inside the frame
described below.

**Visibility is a grant.** The UI fetches a pane through the web proxy
*as the viewing principal*. A person sees a pane only if they can reach
its endpoint: through the tree, for the agent's creator and its
ancestors, or through a grant.

- Sharing one is
  `grant(<ns>/ui/<pane>, to=/users/bob, ops=[read])`. It is narrowed,
  leased and revoked like any grant, and bob can't pass it on unless
  it carries `regrant=true` ([Grants](#grants)).
- A UI finds panes in two places: in `<ns>/ui/panes` for each agent the
  viewer can see, and in the viewer's grant handles on `*/ui/*` for
  panes granted to them. A pane whose declaration the viewer can't read
  is shown under its id.
- The serving agent sees the viewer's namespace in the caller
  assertion, as it does for any caller
  ([2.2.22](#2222-caller-assertion)), and can tailor what it shows.

**Isolation (required).** Pane content is untrusted, whoever serves it.

- **A separate origin per namespace.** The UI renders a pane in a
  sandboxed iframe on an origin of the serving namespace's own,
  `<ns-id>.panes.<shibu>`. It uses the namespace's stable ID
  ([2.2.10](#2210-stable-ids)), not its path, so a namespace re-created
  at the same path doesn't inherit the old one's browser storage.
- **No UI credentials.** The kernel's web proxy serves each pane origin
  and accepts requests there only for that namespace's `ui/` endpoints:
  `https://<ns-id>.panes.<shibu>/<id>` is `<ns>/ui/<id>`.
  The front door gives each pane origin a session of its own, scoped to
  that origin and that namespace. The UI's own session cookie is
  host-only (`__Host-`), so it never reaches a pane origin, and a pane
  origin can't overwrite it. The worst a pane can do
  with the viewer's identity is call its own namespace's `ui/`
  endpoints, which the viewer could reach anyway.
- **Framing.** The iframe is sandboxed without top-level navigation or
  popups that escape the sandbox. Pane origins answer with a
  `frame-ancestors` policy naming only the UI's origin, so no other
  site can frame a pane, and the UI's origin can't be framed at all.
- **A frame the UI controls.** The UI draws its own frame around every
  pane, outside the iframe, showing the namespace and agent serving it.
  A pane can't draw over it, so it can't pass itself off as the UI or
  as another agent. `full` placement keeps the frame.
- **No credential entry in a pane.** Credentials and secrets are
  entered only in the UI's own manifest-driven forms
  ([4.1](#harness-interface)), never in a pane, and the bridge below
  has no way to reach one. This is the rule that a harness never
  renders UI for keys
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)), extended to
  everything that serves a pane. Shibu never asks for a credential on a
  `*.panes.` origin.

**Bridge.** A pane talks to the UI only through a narrow `postMessage`
API. The UI accepts a message only from the pane's own iframe and
origin, and ignores everything that isn't one of these:

| Message | What the UI does |
| --- | --- |
| `resize` | resizes the pane, within limits the UI sets |
| `navigate` | goes to another agent or pane, by its Shibu path. Never to an arbitrary URL |
| `open-messages` | opens the serving agent's `messages` |
| `post` | shows the viewer a typed message (2.6.2) for the serving agent's `messages`, exactly as it will be sent. If the viewer confirms, the UI posts it **as the viewer**, which needs the viewer's own `send` on that endpoint |

**CLI fallback.** `shibu open <ns>/<pane>` opens the pane's URL,
through the proxy, in a browser. There the pane is a top-level page on
its own origin: the browser's address bar names the namespace, and
there is no bridge. A UI that doesn't support panes lists them as links
that do the same.

**Main navigation.** Untrusted panes appear only under their own agent
or namespace. Components that root has installed may also be trusted to
place panes in the UI's main navigation. Root lists them in the `/sys`
spec's `ui` entry, and grain's views reach the UI this way
([7.2](#72-the-decision-grain-runs-on-shibu)). Being trusted for
navigation changes nothing about isolation: a trusted pane is still on
its own origin, inside the UI's frame, with the same bridge.

[4.4](#44-a-pane-served-by-an-agent) walks through a pane end to end.

### 2.7 Beyond v1: policy

*Kernel: the web proxy's call-time check. Not in v1. The model is
recorded (bsalmon, director chat, grain task 1766, 2026-10-04; written
up in task 1886) so that nothing in v1 closes it off; its syntax, its
runtime and its API are not decided.*

v1 has no boundaries ([A.22](#a22-boundaries)). Its only confinement
beyond the tree and narrowing is `regrant=false` by default
([Grants](#grants)), which stops a subtree passing on what it received
but not granting its own objects outward. Policy is the general form,
and it has two halves.

**Allow is grants, and only grants.** Nothing else ever lets a caller
do anything: not a role, not a rule, not a policy document. A grant may
later carry **conditions**, such as a time window, a request attribute
or a usage threshold, which narrow it further; a condition is part of
the grant, and like the rest of a grant it can only narrow.

**Deny is predicates attached to a subtree.** A namespace may attach a
deny predicate to its own subtree, or to the subtree of any namespace
below it. A predicate binds everything in that subtree and never the
namespace that set it, nor its ancestors, so the setter can still act
across the line itself. A namespace can't remove or loosen a predicate
an ancestor attached.

**The decision.** A call is allowed if, and only if:

- tree ownership allows it, or some valid grant (with its conditions)
  allows it; and
- no deny predicate attached to any namespace on the path fires.

Anything else is denied. Default deny; deny wins.

**Evaluated at call time, in the web proxy.** Predicates may become
arbitrary code, so they can't all be checked once, when a grant is
issued, and trusted from then on. The web proxy, which already checks
reachability, ops, leases, identity and grant chains on every call
([Trust](#trust)), evaluates the predicates on the path as part of the
same check. Control-plane calls (`grant`, `create`, `set_lease` and the
rest) reach the kernel through the proxy too, so they are decided the
same way.

**Rules for predicates.**

- **Fail closed.** A deny predicate that errors or times out denies. An
  allow condition on a grant that errors or times out doesn't allow.
- **Sandboxed, deterministic and time-bounded.** Predicates run in a
  runtime such as CEL or WASM, with no I/O, no clock of their own and a
  hard time limit. They see only declared inputs: the caller's
  identity, the op, the object, the request's attributes, the time, and
  usage counters.
- **Explainable and logged.** Every decision can say which grant
  allowed the call, or which predicate denied it and why, and every
  decision is an audit line ([2.2.8](#228-orchestration-surface)).

**Optional optimisation: a sweep.** A predicate that depends only on
static facts (paths, kinds, ops) can be checked against existing grants
whenever something relevant changes: a predicate is attached, a grant
is issued, or a namespace is moved. Grants it would always deny are
ended with reason `invalid`, atomically with the change, so they stop
showing up as handles. The sweep only tidies: the call-time check stays
authoritative, and a grant the sweep missed is still denied when used.

**Requirements carried over from boundaries.** Whatever syntax policy
takes, it must be able to express what the dropped boundaries did
([A.22](#a22-boundaries)):

- **fence grants out of a subtree**: nothing in `factory/` may be
  granted outside it, except chats to people (4.2.2), or nothing in
  `svc` may grant `cloud-prod` at all (4.2.3);
- **refuse grants into a subtree**: only `read` grants from outside
  into the reviewers (4.2.4), or no secret-`read` grants into `lead`
  ([F31](#f31-root-can-still-install-a-malicious-credential-holding-image));
- **require or limit leases** on objects and grants in a subtree
  ([F12](#f12-leaks-in-long-lived-namespaces),
  [F23](#f23-grant-sprawl-in-long-lived-namespaces));
- **limit which component allocators a subtree uses**
  ([F33](#f33-a-component-allocator-can-misreport-usage)).

**Two commitments, stated plainly**, which v1 is built not to break:

- **Authorization is evaluated per call, in the proxy.** Nothing about
  a grant is settled for good when it is issued; the check on every
  call is the one that counts.
- **Grants are the only allow mechanism, so policy only subtracts.**
  Adding a policy can make a call fail that would have succeeded, and
  never the reverse.

---

## 3. Operations

*Kernel.* This is the minimal API surface the model implies. The
kernel is namespaces, VMs, volumes, endpoints, grants, secrets, leases,
labels, `move`, `adopt`, `watch`/`audit`/`cost` and `batch`;
everything else, `create_agent` and declarative `apply` included, is a
standard-distribution component built on those. Names and
signatures are illustrative. Every call is made *as* a namespace: the
caller's ambient identity ([Identity](#identity)). There are no
tokens in v1 ([2.2.11](#2211-no-attenuated-tokens)).

### Namespaces

| Operation | What it does |
| --- | --- |
| `create_namespace(path, labels, lease)` | Creates an **empty** child namespace in the caller's subtree. It holds no quota, and reaches nothing outside Shibu, until something is granted to it. |
| `create_namespace(..., grants=[...])` | Atomic sugar: creates the namespace and issues the listed grants to it in one call, as a `batch` would. |
| `create_namespace(..., agent={adapter, model, runs_in, tools, job, config, title})` | Makes it an agent namespace: the kernel creates the agent object and its standard endpoints ([4.1](#41-agents-a-kernel-composite)). It launches nothing. |
| `create_namespace(..., keep)` | A namespace has no lifecycle policy: it is deleted by `delete` or when its `lease` expires ([2.2.6](#226-leases-and-ttls)). `keep=<bytes>` is sugar for a deposit area `<caller>/keep/<child>/` ([2.2.4](#224-lifecycle-and-keeping-results)). |
| `delete(ns)` | Deletes a namespace: the namespace itself or an ancestor may call it. The namespace and its subtree enter `deleting` briefly: every VM is stopped with its usual stop signal and timeout, agents end, queued creates are dropped, every grant the subtree received is revoked, cascading to the grants issued from them, and every object is deleted, component-allocated handles through their allocator's `delete`. There is no grace period; whatever the caller wants to keep it moves or snapshots first ([2.2.4](#224-lifecycle-and-keeping-results)). |
| `set_lease(object, ttl)` | Sets a lease on any object, grants included ([2.2.6](#226-leases-and-ttls)). |

A parent gives a new child its quota by granting allocators to it:

```
create_namespace("a/x", lease=8h)
grant(a/vm-alloc, to=a/x, ops=[create], limits={cap: 8 cpu, 32Gi})
```

Caps are drawn from the parent's pool, so what the child creates
through them is a lend ([Quota](#quota)). The grant has no `regrant`,
so `a/x` can spend the cap but not hand it on; a parent that wants the
child to split it among children of its own adds `regrant=true`. When a
namespace is deleted, every grant it received is revoked.

### Grants

| Operation | What it does |
| --- | --- |
| `grant(object, to, ops, limits, subtree, regrant, reserved, lease)` | Gives `to` access to an endpoint, narrowed ([Grants](#grants)). Returns a grant ID, and the grant appears in `to` as a handle. `regrant` defaults to false: `to` can use the grant but not pass it on. `reserved`, on an allocator grant, sets its whole cap aside ([Quota](#quota)). Outside access is a grant on `/sys/egress` ([2.2.13](#2213-egress-policy)); public exposure is a grant to `/sys/ingress` ([2.2.12](#2212-pre-approved-publish)). |
| `revoke(grant_id, kill)` | Revokes a grant, cascading to re-grants, and closes the tunnels and connections open through them ([Grants](#grants)). `kill=true` also deletes objects created through an allocator grant. |
| `list_grants(path)` | Lists the grants a namespace has given and received, for audit. |

### Resources (through allocators)

| Operation | What it does |
| --- | --- |
| `create(allocator, target, spec, idempotency_key, dry_run, wait)` | Creates a VM or a volume (an image, snapshot or clone with `spec.from`), or a component allocator's resource, and writes its handle to `target`. `target` must be in the caller's subtree, or inside the grant's `into`. The charge goes to the named allocator or grant. With `wait`, a full allocator queues the request ([2.2.21](#2221-admission-queue)). |
| `delete(object)` | Deletes a resource. Its charge drops off its payer. |
| `move(object, from, to, payer)` | Re-homes an object within the caller's subtree, optionally changing its payer ([Move](#move)). It is also how a parent keeps something from a child before it deletes the child ([2.2.4](#224-lifecycle-and-keeping-results)). |

Taking a VM from a warm pool is a `create` through a grant of the
pool, which answers it by adopting a waiting VM into the target
([2.2.2](#222-caps-reservations-and-adoption)). There is no separate
`claim`, and no separate `adopt` call.

### Allocator contract

The operations above reach an allocator through its endpoint. Every
allocator serves the same contract, and any namespace can implement it
([Component allocators](#component-allocators)):

| Operation | What it does |
| --- | --- |
| `create(spec, target)` | Creates a resource of the allocator's one kind, with the amounts declared in `spec`, and returns its handle. The kernel checks caps and headroom on every link of the grant chain first, from the declared amounts, and records the handle. A component allocator may instead name a VM or writable volume its own namespace owns, of the kind and shape asked for, and the kernel **adopts** it into `target` ([2.2.2](#222-caps-reservations-and-adoption)). |
| `delete(handle)` | Deletes a resource. The kernel calls it on `delete`, on `revoke(kill=true)`, on an expired lease, and on the deletion of the handle's home, with retries. |
| `usage(handle or grant)` | Reports what a resource or a grant has used; the kernel shows it in `get` and `cost`. |
| `list(grant)` | Lists the resources the allocator holds for a grant, so the kernel can reconcile them against the handles it recorded. |

| | Kernel allocators | Component allocators |
| --- | --- | --- |
| Kinds | VM, volume ([Allocator](#allocator)) | whatever the implementing namespace defines, one per allocator endpoint |
| Implemented by | the kernel, over the backend drivers | any namespace: an agent, a VM or a service |
| Root of the chain | `/`, from driver capacity ([Backend drivers](#backend-drivers)) | the implementing namespace |
| Changed by | root only, through the `/sys` spec and grants | the owner, freely |
| Limits enforced from | the kernel's own counts | the kernel's own counts: creates made through each grant, and amounts declared at create time |
| `usage` in `cost()` | counted | **reported**, and marked so. It never loosens a limit |
| Grants | narrowing, caps, `reserved`, `regrant`, leases, `revoke` | the same |

### Driver contract

Not a caller API: the kernel calls these on a backend driver
([Backend drivers](#backend-drivers)).

| Area | What the driver provides |
| --- | --- |
| VMs | create, start, stop and delete, with a shape and an image |
| Volumes | create, attach, and copy from a volume or an OCI image (snapshots, clones and images) |
| Network | a per-VM interface with an unspoofable origin, and an egress enforcement point that drops everything but the web proxy, except for `/sys/egress`'s own VMs |
| Events | status changes and preemption, which feed VM status and exit reasons ([2.2.17](#2217-exit-status-and-reason)) |
| Placement | the placement classes it offers |
| Capacity | root allocators: a host's cpu, ram and disk, or a cloud project's quota |

### Agents, messages and secrets

| Operation | What it does |
| --- | --- |
| `create_agent(path, spec, payer, idempotency_key, dry_run)` | *Standard distribution, not kernel.* A helper (library, CLI and `/sys/agents`) defined in kernel operations plus `runner/v1` ([4.1](#41-agents-a-kernel-composite)): one `batch` that creates the namespace `path` with `agent`, an optional `lease` and `keep` area, by default a fresh VM in `path` charged to `payer`, the agent's grants on its `tools`, the launch grant on its `runs_in` runner carrying the budget, and the listed `grants`; then the runner's `launch`; then, if the launch is refused, `delete` of the namespace. |
| `launch(runner, agent, model, tools, credential)` / `resume` / `stop` / `status` | The `runner/v1` contract a runner endpoint serves ([Runners](#runners)), called by any holder of `launch` on it. A pool runner asks the kernel for the session binding, which the kernel issues only to the agent's `runs_in` runner, while the agent's namespace holds a launch grant on it. |
| `exposure(object)` | Says who can `exec` on, or reach, a VM or runner, and who holds `read` on a secret, with the grant chains ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). Answers only about objects the caller owns or can see. |
| `POST <agent>/messages` / `GET <agent>/messages?since=` | Talks on an agent's `messages` channel, an `http` endpoint, from either end ([2.2.25](#2225-messages)). Needs `write` or `read` on the endpoint: by the tree for the creator, by a grant for anyone else. A message has a type: `text`, `question`, `approval`, `progress` or `attachment`, and a reply names what it answers ([2.6.2](#262-messages-types)). |
| `POST <agent>/artifacts` / `GET <agent>/artifacts` | Adds to, or reads, the append-only list of what an agent produced ([2.6.4](#264-artifacts)). |
| `put_secret(ns/name, value)` | Writes a new value of a secret the caller owns or holds `write` on ([2.2.24](#2224-secret-store)). Write-only: there is no `get`. Rotation is writing again. A secret has two grantable ops, `read` and `write`. |
| `read(secret)` | The secret store's `read` op. Returns the current value to a caller holding a `read` grant on the secret; nothing else can read one. Each read is an audit line naming the version ([2.2.24](#2224-secret-store)). Holding a `read` grant marks the caller's namespace credential-holding, which `list` and `exposure` show ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). |
| `exit(status, reason)` | Called by an agent on its own `exit` endpoint when its job is done. Ends its harness session and its VMs, and records the status and reason on the agent object ([2.2.17](#2217-exit-status-and-reason)). Its namespace is not deleted: only `delete` or its lease does that ([2.2.6](#226-leases-and-ttls)). |
| `stop(agent)` | The creator or an ancestor, or a holder of `write` on the agent's control endpoint or its `agent` endpoint, ends the agent. Its reason is `stopped`. |

An agent's `messages` and transcript stay readable while its namespace
exists. Without a lease, that is until somebody deletes it.

### Orchestration

| Operation | What it does |
| --- | --- |
| `list(path, selector)` / `watch(path, selector)` | Lists objects, or streams events about them, across the caller's subtree and the grants it holds. Grant deaths, admission waiters, a namespace entering `deleting` (with its cause), `lease.expiring` a lead time the watch chooses before a lease expires ([2.2.6](#226-leases-and-ttls)), component-allocator leaks, and every VM and agent status change are events. VM status is running, restarting, exited or preempted, with an exit code, a reason and a restart count ([2.2.17](#2217-exit-status-and-reason)). A namespace's entry shows whether it is **credential-holding**, a property the kernel sets while it holds a secret-`read` grant ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). |
| `get(object)` | Returns one object. For an allocator, a grant or a resource it includes `usage`: per dimension and budget, the charge, the cap, what is reserved and the headroom, at every link the caller can see. |
| `label(object, k=v)` | Sets labels and metadata on any object. |
| `batch(ops, idempotency_key, dry_run)` | Runs a list of kernel operations as one, all or nothing, each checked as if called alone against the state the earlier ones left ([2.2.8](#228-orchestration-surface)). |
| `apply(subtree_spec, dry_run)` | *Standard distribution, not kernel.* The reconciler diffs a document against `list` and commits the difference with one `batch` ([2.2.8](#228-orchestration-surface)). Applied to `/sys` by root, it reconciles the standard distribution to the `/sys` spec, with a staged swap for every component it replaces ([2.3](#23-the-standard-distribution-and-the-sys-spec)). |
| `audit(path, since)` / `cost(path, since)` | Reads the audit and cost log for the caller's subtree. Cost is attributed along grant chains. A component allocator's own `usage` reports appear marked **reported**, and **leaks** (handles whose `delete` keeps failing) appear charged to the grantor ([Component allocators](#component-allocators)). |

### CLI parity

The UI is a replaceable component, and the rule it has to conform to is
that anything a UI can do, the CLI can do
([2.6.1](#261-the-ui-is-a-replaceable-component)). The CLI, `shibu`,
is the kernel's reference client. Each thing a person does in a UI has
a command, and each command is a sequence of the calls above, made as
the namespace the person is acting as. The names are illustrative,
like the rest of the API.

Rows marked *convention* drive a convention endpoint, not a kernel
operation. A **request** is a call to the `requests` endpoint a
namespace may serve, asking it for a grant
([2.6.5](#265-requests-stay-in-the-api)). The kernel has no requests,
so a namespace without that endpoint simply has none.

| In a UI | From the CLI | Calls |
| --- | --- | --- |
| act as a namespace | `shibu --as factory …` | any, as that namespace ([2.2.18](#2218-human-as-a-principal)) |
| the list of agents | `shibu agents [path] [--phase waiting-on-human]` | `list(path, selector=kind:agent)`, with the status labels ([2.6.3](#263-status-labels)) |
| a chat | `shibu chat <agent>` | `GET` and `POST` on `<agent>/messages`, every type rendered as text ([2.6.2](#262-messages-types)) |
| answer a question or an approval in a chat | inside `shibu chat`, or `shibu reply <agent> <message> …` | `POST <agent>/messages` with `reply_to` |
| answer a request (convention) | `shibu approve <request> [--deny --reason …]` | `grant(...)`, then `POST <ns>/requests/<id>/answer` on the `requests` convention endpoint ([2.6.5](#265-requests-stay-in-the-api)) |
| an agent's artifacts | `shibu artifacts <agent>` | `GET <agent>/artifacts` ([2.6.4](#264-artifacts)) |
| a pane | `shibu open <ns>/<pane>`, in a browser | a fetch through the web proxy, as the viewer ([2.6.6](#266-agent-served-panes)) |
| create an agent | `shibu agent create <path> --adapter … --model … [--runs-in <runner>] [--tools <vm>/mcp …]` | the `create_agent` helper: `batch`, then the runner's `launch` ([4.1](#create_agent)) |
| runners and their capacity | `shibu runners [path]` | `list(selector=endpoint:runner)`, then `capacity` and `adapters` on each ([Runners](#runners)) |
| who can reach a VM, runner or secret | `shibu exposure <object>` | `exposure` ([2.2.7](#227-credentials-only-behind-proxy-endpoints)) |
| a credential form | `shibu secret put <ns/name> [--adapter claude-code]`, prompting from the same manifest | `put_secret` ([2.2.24](#2224-secret-store)) |
| grants, quota and cost | `shibu grants`, `shibu usage`, `shibu cost` | `list_grants`, `get`, `cost` |
| publish an endpoint | `shibu publish <endpoint> --as <name> --audience …` | `grant(..., to=/sys/ingress)` ([2.2.12](#2212-pre-approved-publish)) |
| edit the `/sys` spec | `shibu sys apply <spec>` | the reconciler's `apply`, then `batch` ([2.3](#23-the-standard-distribution-and-the-sys-spec)) |

### Implementation note: walking the grant chain

Each `create` checks headroom at every link of its grant chain, so a
deep chain costs a walk per create. Implement it as a single walk with
cached headroom per link, and take per-link locks in a fixed order (root
first) so that concurrent creates cannot deadlock. Cascading revokes
walk the same chains in the other direction, through a **grant-chain
index**, grants indexed by the grant they were issued from, so a revoke
walks the index rather than scanning grants. The index is a requirement
on the kernel and its state store, not part of the API
([F22](#f22-cascading-revokes-can-be-expensive),
[2.2.27](#2227-kernel-state-store)).

The recommended shape is one namespace per agent, plus one per sandbox.
Nothing in the model limits depth, and nothing at depth 4 (the factory's
deepest) was expensive. A design that gave every tool call its own
namespace would pay for a create, a set of grants, a chain walk and a
delete on every call.

---
## 4. Walk-throughs

### 4.1 Agents: a kernel composite

*Kernel: the agent object (status labels, exit status), its standard
endpoints and the session binding, the harness contract, the
`runner/v1` and VM `mcp` contracts, the agent control MCP, `messages`
and budgets. Standard distribution: the `create_agent` helper (a
library, the CLI and `/sys/agents`); in the bootstrap set, a pool
runner with its adapters (`/sys/runner`), the agent base image that
serves `mcp`, and the model proxy (`/sys/model`).*

The kernel's agent concept is small (1887): an **agent object** in a
namespace made with `create_namespace(..., agent=…)`, its status labels
and exit status, its standard endpoints, and the session binding it
issues to the agent's runner. Making an agent is not a kernel call. It
is create namespace, grants, then the runner's `launch`, which the
standard distribution's **`create_agent` helper** does in one
`batch` and one `launch`; anything it does, a caller can do itself
([A.29](#a29-create_agent-as-a-kernel-composite)). There is
no agent allocator. An agent lives in a namespace labelled
`kind: agent`, with a harness session on a runner, the VM tools it
holds grants on, and some standard endpoints. Counting agents is a
label query.

An agent has two separate properties (1884):

- **`runs_in`**: a **runner endpoint** handle, where the harness runs
  ([Runners](#runners)). It determines the harness's identity: the
  runner VM's origin when that VM is in the agent's namespace, or a
  session binding, `ns=<agent>, via=runner`, otherwise
  ([Identity](#identity)).
- **`tools`**: the VM **`mcp`** endpoints the agent holds grants on,
  zero, one or many ([VM `mcp`](#vm-mcp-and-the-agent-control-mcp)).
  Every tool call that touches a filesystem or runs a command runs in
  one of them.

The agent itself is an **agent object** in that namespace (1763). It
is the thing that exits: it carries the agent's status, and once it
has exited, its status and reason
([2.2.17](#2217-exit-status-and-reason)). It charges nothing of its
own. The namespace is the agent's container and is deleted, not
exited, by `delete` or its lease ([2.2.6](#226-leases-and-ttls)).

Why an agent is a primitive and not a recipe is in
[F17](#f17-the-agent-allocator-overlaps-the-vm-allocator-and-the-credential-rule).

#### `create_agent`

*Standard distribution.* A helper defined in kernel operations plus
`runner/v1`, shipped as a library, as `shibu agent create`, and as the
optional `/sys/agents` component; each acts as its caller.

```
create_agent(path, payer=<VM allocator or grant>, spec={
    adapter: claude-code | codex | gemini | <one the runner ships>,
    model: claude-opus-4,            # a launch parameter (2.2.23)
    runs_in: <runner handle>,        # optional; default: the new VM's own runner
    tools: [<vm>/mcp, ...],          # optional; default: the new VM's mcp
    credential: <secret ref>,        # optional reference, read by the runner through its own grant
    job: "<prompt / task>",
    config: {...},                   # non-secret adapter settings, from the manifest
    budget: {unit: usd, per_day: 20} | {unit: hours, per_day: 4} | {unit: turns, total: 500},
    regrant_launch: false,           # true: the agent may launch agents of its own
    shape: {cpu: 2, ram: 8Gi},       # for the VM it creates, if any
    grants: [...],                   # allocators/endpoints the agent may use, /sys/egress included
    lease: 8h,                       # optional; the only automatic deletion (2.2.6)
    keep: 5Gi,                       # optional deposit area in the caller (2.2.4)
}) -> agent_id
```

It makes, in **one `batch`**, all or nothing:

- **the namespace `path`**, with `create_namespace(..., agent={adapter,
  model, runs_in, tools, job, config, title})`, which must be in the
  caller's subtree, with the optional `lease`. The kernel makes the
  **agent object**, which records the agent's status and its exit
  ([2.2.17](#2217-exit-status-and-reason)), and the **standard
  endpoints** `agent`, `job`, `messages`, `artifacts`, `exit`, `usage`
  and the agent control MCP, `agent-mcp`
  ([Harness interface](#harness-interface));
- **the deposit area** `keep`, if asked for: a deposit grant into
  `<caller>/keep/<name>/`, appearing as `<path>/keep`
  ([2.2.4](#224-lifecycle-and-keeping-results));
- **by default, a fresh VM**, homed in `path` as `<path>/vm` and charged
  to `payer`, a VM allocator or grant the caller holds, booted from the
  agent base image, which serves both `mcp` and `runner`. When `runs_in`
  and `tools` are left out, the agent runs in that VM's runner and uses
  that VM's `mcp`: the `same-vm` combination
  ([Placements](#placements)). No VM is created when both name existing
  handles, or when `tools` is `[]` and `runs_in` names a runner
  elsewhere;
- **the agent's grants on its `tools`**: on each VM `mcp` named, the
  ops the caller can grant from (`exec`, `read`, `write`), narrowed as
  the entry says. The caller must hold those ops, by the tree or by a
  grant with `regrant`;
- **the agent's launch grant**: a grant of `runs_in` to `path`, with
  the `launch` op and the `budget`, narrowed from the caller's own
  ([2.2.23](#2223-model-access-and-budgets)), so the caller must own
  the runner by the tree or hold its `launch` grant with `regrant`.
  `regrant_launch: true` gives it `regrant`, as `<path>/runner`, so
  the agent can launch agents of its own;
- **the `grants`**, issued by the caller to `path`, exactly as in
  `create_namespace(..., grants=[...])`. Outside access is one of them,
  a grant on `/sys/egress` ([2.2.13](#2213-egress-policy)).

Then it calls the runner's **`launch`** on `runs_in`, through the
caller's own launch grant, naming the agent, the `model`, the `tools`
and the `credential` ([Runners](#runners)). The runner reads the agent
object, gets the session binding from the kernel (issued only to the
agent's `runs_in`, and only while the agent's namespace holds a launch
grant on it), and starts the session under the agent's launch grant.
If the launch is refused, the helper deletes the namespace, so nothing
is left behind.

A `credential` is a **reference**. The runner, or `/sys/model` if the
runner reaches models that way, reads it through its own `read` grant
from the secret's owner, and uses it only for the namespaces that
grant's `for` names, by default the owner's subtree
([2.2.24](#2224-secret-store)); the caller and the agent never hold
`read`. With no `credential`, the runner uses whichever of its own
accounts its policy maps the caller to. `dry_run` is the batch's dry
run, reporting the charges along each grant chain
([2.2.19](#2219-dry-run-and-quota-preview)), and the runner's answer
to a dry-run `launch`.

Defaults the call doesn't spell out:

- A VM the call creates restarts `on-failure`. When a runner restarts,
  it relaunches its sessions with `resume`, in the same `runs_in`
  ([Runners](#runners)).
- The namespace reaches nothing outside Shibu unless `grants` include
  a grant on `/sys/egress` ([2.2.13](#2213-egress-policy)).
- The namespace is labelled `kind: agent` and `adapter: <name>`. More
  labels are set with `label`.
- A missing `lease` is allowed, for long-lived agents.
- An agent's namespace is not deleted when the agent exits, so a
  finished agent's `messages` and transcript stay readable until its
  creator deletes the namespace or the lease expires. A parent that
  wants a throwaway worker's namespace gone once it exits deletes it
  on the agent's exit event, itself or through a reaper
  ([2.2.6](#226-leases-and-ttls)).
- `shape` can also carry `volumes`, attached to the VM the call
  creates. They can be volumes in the caller's subtree, or handles
  created by the call's own `grants`
  ([4.2.4](#424-intake-and-planning-in-factorylead)).

**Pinning.** A VM whose runner hosts a live session is pinned to it:
it can't be adopted away ([2.2.2](#222-caps-reservations-and-adoption))
or moved while the session lives, and deleting it stops the agent, with
reason `stopped`. `runs_in` may change only between sessions: on an
`idle` or ended session, the agent's creator may point it at another
runner it holds `launch` on, with `POST <agent>/agent/runs_in`, and the next message resumes the session
there if the adapter can, or starts a new one. `tools` may change at
any time, by granting or revoking `mcp` ops; the session sees the
change at its next turn.

Other agents and the UI see the result as an ordinary namespace holding
an agent. Its parent can see it, stop the agent and watch it for its
exit status, delete the namespace, and its `cost` adds up along its
grant chains like anything else.

#### Harness interface

Shibu does not ship one required harness. The kernel defines an
interface, the **harness contract**, and existing agent CLIs plug into
it through **adapters**. Adapters ship in a runner's image and are
installed by the runner's operator: `/sys/runner`'s by root, through
the `/sys` spec, with the three major frameworks, **Claude Code, Codex
and Gemini/Antigravity**; a team's own runner's by that team. A runner
says which adapters it can run (`adapters`, [Runners](#runners)), and
who trusts an adapter is the question of who grants `launch` on, or
`exec` to, that runner ([F27](#f27-adapter-trust)).

Which layer owns what:

| Piece | Layer |
| --- | --- |
| the agent object, its standard endpoints, the harness contract, the `runner/v1` contract, the session binding and `put_secret` | kernel |
| `create_agent`, the helper that makes an agent from kernel operations and `runner/v1`; the OAuth and subscription login flows, run by the runner or the UI | standard distribution |
| the agent control MCP (`agent-mcp`): `send_message`, `ask`, `request_approval`, `attach`, `post_artifact`, `set_status`, `exit` | kernel, served from the agent's own endpoints |
| the VM `mcp` contract: `exec`, `read_file`, `write_file`, `edit_file`, `list_dir`, on any VM | kernel |
| the `mcp` server itself, in the agent base image, and the runner in the same image | standard distribution |
| the credential and config forms built from the manifest | standard distribution, `/sys/ui`; the CLI prompts from the same manifest ([2.6](#26-the-ui-and-the-sysui-conventions)) |
| the adapters and the pool runner | standard distribution, `/sys/runner`; or whoever runs another runner |
| the MCP path from a session to a VM `mcp` in another namespace | kernel: it is the web proxy, carrying the session's calls as the agent |
| tools of an application's own (grain's `propose_task`, for example) | additional component: an MCP server the application serves, granted to the agent like any endpoint and listed in its `tools` |

**Tool surface.** The harness's built-in tools are always disabled,
and its MCP configuration is strict. It lists the agent control MCP,
each VM `mcp` in `tools`, and any application MCP servers, and nothing
else. Every command and file operation happens in a VM the agent holds
`mcp` ops on; an agent with no VM `mcp` has no exec or files at all.

**Control surface.** These are Shibu endpoints of the agent's
namespace, served by the kernel whatever VMs the agent has:

| Endpoint | What it is |
| --- | --- |
| `job` | What the agent was asked to do, and the `config` values beside it. |
| `messages` | The two-way channel with the agent's creator ([2.2.25](#2225-messages)), with typed messages ([2.6.2](#262-messages-types)). |
| `artifacts` | What the agent produced: pull requests, URLs, files and volumes ([2.6.4](#264-artifacts)). |
| `exit(status, reason)` | The agent's exit ([2.2.17](#2217-exit-status-and-reason)). It ends the harness session and the VMs in the agent's namespace and records the status and reason on the agent object. The namespace stays, with the transcript and final logs written into it, until `delete` or its lease deletes it ([2.2.4](#224-lifecycle-and-keeping-results)). |
| `agent-mcp` | The agent control MCP: the tools above as MCP, over the same endpoints ([below](#vm-mcp-and-the-agent-control-mcp)). |

The agent's usage, whatever its runner reports and whatever
`/sys/model` meters, is read from its `launch` grant, so the UI shows
it the same way for every runner
([2.2.23](#2223-model-access-and-budgets)).

**Adapter manifest.** A declarative file that comes with the adapter. It
says how to:

- launch, resume and stop the CLI headless;
- disable its built-in tools;
- point it at its MCP servers;
- deliver credentials, by environment variable or file;
- point its model base URL at `/sys/model`, where the runner reaches
  models that way;
- map `job`, `messages` (with its `question` and `approval` types,
  [2.6.2](#262-messages-types)) and `exit` onto the CLI.

For example:

```yaml
name: claude-code
model: {protocol: anthropic-messages, base_url_env: ANTHROPIC_BASE_URL}
credentials:
  - id: anthropic
    kind: api-key | oauth          # oauth/subscription: the runner or the UI runs the login flow
    label: "Anthropic API key or Claude subscription"
    inject: {header: x-api-key}    # api-key, through /sys/model: attached upstream
    deliver: {env: CLAUDE_CODE_OAUTH_TOKEN}   # oauth, or a key the runner holds: handed to the CLI
config:
  - {id: effort, type: enum, options: [low, high], default: high}
```

**The UI for keys is declarative only.** The UI (`/sys/ui`) builds its
credential and config forms from the manifest, and `shibu secret put`
prompts from the same manifest
([2.6](#26-the-ui-and-the-sysui-conventions)). A harness never renders
UI for keys inside Shibu: a harness could phish keys. Nor does a pane
an agent serves ([2.6.6](#266-agent-served-panes)). A credential
entered in the form goes straight to `put_secret`
([2.2.24](#2224-secret-store)); for an `oauth` credential, the UI or
the runner runs the vendor's login flow and writes the result with
`put_secret`. The kernel runs no login flow. `config` values
are delivered to the harness next to `job`.

#### VM `mcp` and the agent control MCP

There is no special sandbox VM (1884). Today's single tool server is
split in two.

**VM `mcp`.** Any VM may expose a standard `mcp` endpoint, `<vm>/mcp`,
serving MCP over streamable HTTP with the **VM tools**: `exec`,
`read_file`, `write_file`, `edit_file` and `list_dir`.

- It is optional per VM, and on by default in agent images.
- It is an `http` endpoint speaking the `mcp/v1` contract, granted
  with `read` (`read_file`, `list_dir`), `write` (`write_file`,
  `edit_file`) and the contract op `exec`. A
  reviewer can get read-only file tools on another agent's VM:
  `grant(lead/workers/w17/vm/mcp, to=lead/reviewers/r17, ops=[read])`.
  The server refuses a tool call the grant's ops, passed in the
  caller assertion, don't cover.
- **`exec` lends identity.** A command run through `exec` runs in the
  VM, with the VM's origin, so it uses the grants of the VM's home
  namespace, its `/sys/egress` grant included, not the caller's ([Identity](#identity)).
  A grant of `exec` on someone else's VM is a deliberate lending of
  that identity, and of whatever the VM holds. It is the strongest op
  a grant can carry, and the exposure query lists every holder of it
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)).

**Agent control MCP.** The **agent tools**, `send_message`, `ask`,
`request_approval`, `attach`, `post_artifact`, `set_status` and `exit`,
are an MCP server the kernel serves at `<agent>/agent-mcp`, over the
agent's `messages`, `artifacts`, labels and `exit`. It is independent of
any VM, so an agent with **zero VMs** is valid: a planner that only
reads, talks and creates other agents needs a runner session and its
control MCP, nothing more
([4.2.4](#424-intake-and-planning-in-factorylead)). `attach` takes a
URL, or a path in one of the agent's VMs, read through that VM's `mcp`.

#### Runners

**Runners are a contract, `runner/v1`.** Any VM may expose a `runner`
endpoint, and **the runner launches every harness**: there is no other
launch path.

- A **same-vm runner** is the VM's own: usually one or a few sessions,
  all in the VM's namespace, with the VM's origin as their identity.
  The agent base image serves one beside `mcp`.
- A **pool runner** is a few VMs serving many agents from many
  namespaces, with one isolated session each, in its own container or
  better ([6.12](#612-session-isolation-inside-pool-runners)). Each
  session acts as its agent through a session binding.
  `/sys/runner` is the distribution's pool runner; a team can run its
  own.

**The contract:**

| Call | What it does |
| --- | --- |
| `launch(agent, model, tools, credential)` | Starts a harness session for an agent whose `runs_in` is this runner, and returns a session id. Any holder of `launch` on the runner may call it, the `create_agent` helper among them; the caller assertion carries that grant's limits. A pool runner then asks the kernel for the agent's session binding, and runs the session under the launch grant the agent's namespace holds. |
| `resume(session)` | Resumes a session from its saved state, for a new turn or after a restart. |
| `stop(session, reason)` | Ends a session. |
| `status(session)` | The session's state, turns, hours and last error. |
| events | A stream of session events: state changes, the exit the harness called, usage reports and vendor limits. A vendor limit is a pause: status `waiting`, reason `vendor-limit`, with the reset time when known ([6.10](#610-per-vendor-rate-limits-on-shared-subscriptions)). |
| `capacity` | How many more sessions it can take, and the isolation between sessions it provides: `container`, `microvm` or `vm` ([6.12](#612-session-isolation-inside-pool-runners)). |
| `adapters` | The [adapter manifests](#harness-interface) it can run. |

**Restart.** When a runner restarts, it relaunches each of its sessions
with `resume`, in the same `runs_in`. If it can't, the agent fails,
with reason `harness-failed`. A session never silently moves to another
runner; moving one is a `runs_in` change ([Pinning](#create_agent)).

**What the kernel enforces, and what it doesn't.** The web proxy checks
the `launch` op, the grant's lease and its chain on every call to the
runner; the kernel issues the binding only to the agent's `runs_in`
runner while the agent's namespace holds a live launch grant on it, refuses the binding after the session
ends, and stamps `via=runner` on the session's calls. A runner can't
widen its grants or mint identities. Inside a session, the runner
applies the `launch` grant's limits (adapters, models, budget,
sessions) and keeps sessions apart; relying on that is the granter's
choice ([Trust](#trust)). Sessions are kept apart by a container or
better, and a `launch` grant may require more
(`limits={isolation: microvm}`), which the runner checks against what
it provides ([6.12](#612-session-isolation-inside-pool-runners)).

**Who runs a runner, and what it holds.** A runner holds whatever logins
its owner was granted `read` on: a subscription can only be used from a
runner whose namespace holds it
([2.2.23](#2223-model-access-and-budgets)). The distribution's
`/sys/runner` holds logins only through the `read` grants their owners
issue it, and uses each only for its owner's subtree; it is built so that no tool call runs on it: built-in tools
off, and only its agents' VM `mcp` endpoints in each session.

**Model access and billing** are a grant on the runner, op `launch`,
and the runner's reports per session
([2.2.23](#2223-model-access-and-budgets)).

#### Placements

A placement is no longer a mode of `create_agent`. It is a combination
of `runs_in` and `tools`, and the two named ones are just common
combinations:

| Combination | `runs_in` | `tools` | Harness identity |
| --- | --- | --- | --- |
| **`same-vm`** | the runner of a VM in the agent's namespace | that same VM's `mcp` | the VM's origin |
| **`split`** | a pool runner, such as `/sys/runner` | VM `mcp` endpoints elsewhere, typically a VM in the agent's namespace | session binding, `via=runner` |
| **planner** | a pool runner | none | session binding, `via=runner` |
| **shared build VM** | any runner | `exec` on a VM in another namespace, such as a team's build VM | as `runs_in`; commands run as the build VM's namespace |

Other combinations are allowed: a same-vm runner with extra VMs in
`tools`, or a team's own pool runner with the agent's own VM.

- **Subscriptions and OAuth** need a runner that holds the login, which
  means a runner the login's owner chose to grant `read` to, usually
  leased
  ([2.2.23](#2223-model-access-and-budgets)). grain does exactly this
  today: it runs
  `claude -p --tools '' --mcp-config … --strict-mcp-config` on the host,
  with the OAuth token in the CLI's environment and every tool call
  redirected into the sandbox.
- **API keys** work anywhere: through `/sys/model`, so no VM holds the
  key, or held by the runner.
- **Who pays for a pool runner.** A shared runner session is its
  operator's overhead and is charged to nobody's VM grant; the agent
  pays through its launch grant's budget. A runner VM the
  `create_agent` helper creates is charged to `payer`.

**Placement is invisible from outside.** The namespace, the budget,
`messages` and the exit behave the same in every combination, so the UI
and other agents can't tell which one an agent is using. The one
difference visible to a service is `via=runner` on calls from a session
that acts through a binding.

#### Bootstrap

This is the bootstrap order of
[2.3](#23-the-standard-distribution-and-the-sys-spec), in detail.

1. **Install** starts the kernel, which creates `/`, the root
   allocators and the installer's own namespace `/users/<name>`
   ([2.2.18](#2218-human-as-a-principal)), and starts the reconciler,
   which applies the default `/sys` spec. That starts the standard
   distribution, including the bootstrap set `/sys/egress`,
   `/sys/model` and `/sys/runner`, and the UI, `/sys/ui`. It needs no repo, and no network beyond the image
   registry.
2. **The human**, in a Shibu UI session, sets a credential through the
   UI's manifest-driven form, which stores it as
   `/users/alice/secrets/anthropic`. Then, acting as `/`, they let the
   runner use it for `/bootstrap`, and create the bootstrap agent with
   the helper:

   ```
   grant(/users/alice/secrets/anthropic, to=/sys/runner, ops=[read],
         limits={for: ["/bootstrap/**"]}, lease=90d)
   create_agent("/bootstrap", payer=/vm-std, spec={
       adapter: claude-code,
       model: claude-opus-4,
       runs_in: /sys/runner,                         # root reaches it by the tree
       credential: /users/alice/secrets/anthropic,   # a reference; the runner reads it
       job: "build the factory per design X",
       budget: {unit: usd, per_day: 100},
       regrant_launch: true,
       shape: {cpu: 4, ram: 16Gi},                   # its own VM, for tools
       grants: [
           grant(/sys/runner, ops=[launch], limits={adapters: [claude-code]}, regrant=true),
           grant(/vm-std,  ops=[create], limits={cap: 64 cpu, 256Gi}, regrant=true),
           grant(/volume,  ops=[create], limits={cap: 10Ti},          regrant=true),
           grant(/sys/egress, ops=[tunnel], limits={hosts: ["*:443"]}, regrant=true),
           …
       ],
   })
   grant(/bootstrap/messages, to=/users/alice, ops=[write])
   ```

   The UI then opens `/bootstrap/messages` as a chat. The last grant
   puts the chat in the human's own list, so they don't have to act as
   `/` to talk to it. The agent's grants carry `regrant=true` because
   handing them on is its job; the chat grant doesn't, so alice can
   talk to the agent but not share the chat.
3. **Everything else** (`svc`, `ci`, `lead`, the worker agents) is built
   by that agent through grants. A worker agent is just the
   `create_agent` helper run by another agent, under quota and a `launch` grant it was
   granted. Because
   creates land in the caller's subtree, what it builds lives under
   `/bootstrap/`.
   Root can also switch `/sys` to repo mode at this point
   ([2.3](#23-the-standard-distribution-and-the-sys-spec)), with a
   config repo the bootstrap agent helped set up.

The factory in [4.2](#42-full-software-factory) is built by the human
step by step, so that each step can be shown. Every step in it is a
call the bootstrap agent could make instead, from the grants above.

### 4.2 Full software factory

*Additional components, on the kernel and the standard distribution.*

This is the main walk-through. It builds a factory end to end and
follows changes through it. Where grain already does the same job, the
grain equivalent is noted as **grain today**, because grain *is* a
software factory and is the most concrete example available.

Quotas are illustrative but consistent with each other, so the
arithmetic in the borrowing and out-of-quota steps can be checked.
Grants are written `grant(object, to=…, …)`; inside
`create_namespace(..., grants=[...])` the `to=` is the new namespace and
is left out. Each grant's handle in the grantee is named in a comment.

#### 4.2.1 The shape

*Additional components.* The factory is one. `/` and `/sys` are the
kernel and the standard distribution it is built on.

```
/                                  root. Holds the drivers' root allocators.
├── sys/                           the standard distribution (2.3)
│   ├── model      → VM port       metered model proxy (bootstrap set; credential-holding)
│   ├── runner     → VM port       split harness runner and adapters (bootstrap set; credential-holding)
│   └── cred/      → VM ports      credential proxies (optional; credential-holding)
├── users/alice/                   the human's namespace
│   └── secrets/anthropic → secret the factory's model credential (an API key)
└── factory/                       created by the human via the UI
    ├── svc/                       shared services, long-lived
    │   ├── git            → VM port   Shibu-hosted git (or a GitHub mirror)
    │   ├── repos/<name>   → volume    repo volumes behind git
    │   ├── repo-warm      → volume    snapshot source: checkout + caches
    │   ├── pkgcache       → VM port   apt/npm/pypi/go/docker cache
    │   ├── build-cache    → volume    snapshot source for CI
    │   ├── artifacts      → VM port   artifact store (HTTP)
    │   ├── db             → VM port   shared Postgres, reached by tunnel (2.2.26)
    │   ├── db-data        → volume    Postgres data directory
    │   ├── images/        → volumes   read-only images, one per volume
    │   ├── gh-proxy       → cred proxy  a /sys/cred endpoint for the GitHub App key
    │   ├── cloud-preview  → cred proxy  a /sys/cred endpoint for the preview cloud credential
    │   └── prod/
    │       └── cloud-prod → cred proxy  a /sys/cred endpoint for the production cloud credential
    ├── lead/                      planner agent (create_agent) and everything it starts
    │   ├── (no VM)                the planner runs in /sys/runner with no tools VM
    │   ├── intake-vm              receives webhooks and writes work items
    │   ├── webhook  → intake-vm :8443, granted to /sys/ingress (published)
    │   ├── reviews/                reviews, deposited by reviewers
    │   ├── keep/                  deposit areas for its workers (keep=, 2.2.4)
    │   └── workers/               the planner's workers, under one cap
    │       ├── build/             shared build VMs b1…b4, workers' tools for code that must not have their authority
    │       ├── w17/               worker agent for item 17 (its own VM, for edits, is here)
    │       ├── w18/ …
    │       └── r17/               reviewer agent for change 17
    ├── ci/                        CI controller and per-change CI runs
    │   ├── pool/                  warm pool of ci-base VMs (a component allocator, 2.2.2)
    │   ├── results/               CI results, deposited by runs
    │   ├── c17-1/
    │   └── …
    ├── mq/                        merge queue
    │   └── b42/                   one batch's CI
    └── rel/                       release and deploy
        ├── build-<sha>/
        └── preview-17/
```

There is no foreman, no controller holding the whole factory's
credential: the namespace that holds a thing grants it
([F1](#f1-cross-sibling-wiring-needs-a-live-principal-at-the-common-ancestor)).

**grain today:**

- `factory/` is a grain deployment.
- `svc/` is the controller's proxies: the git proxy and the package
  cache. grain's model proxy is `/sys/model`. Under the layering, a
  factory could use the distribution's `/sys/git` and `/sys/pkgcache`
  instead of running its own; this walk-through keeps its own `git` and
  `pkgcache` in `svc` to show how a namespace builds services.
- `lead/` is the director plus the dispatcher.
- `wN/` is a task, and `wN`'s own VM is its run's sandbox. grain's
  host-side `claude -p` is the `split` combination: a pool runner, with
  the sandbox's `mcp` as its tools ([4.1](#placements)).
- `ci/` is grain CI.
- `mq/` is the merge queue.
- `rel/` is the release workflow and the release-candidate schedule.

#### 4.2.2 Bootstrap

*Additional components, set up through kernel calls.*

The human, alice, logs into the UI. It acts as `/users/alice` by
default, and she switches to the root namespace `/` for this root-level
work ([2.2.18](#2218-human-as-a-principal)). The root holds `/sys/model`
and the root allocators, which the backend drivers expose from their
capacity ([Backend drivers](#backend-drivers)).

Step 0, as `/users/alice`: she enters the factory's model credential, an
Anthropic API key, in the form the UI builds from the `claude-code`
manifest ([4.1](#harness-interface)). The UI calls
`put_secret("/users/alice/secrets/anthropic", …)`
([2.2.24](#2224-secret-store)).

This walk-through has the human build the factory step by step. The
bootstrap agent in [4.1](#bootstrap) could make the same calls from its
grants instead.

Step 1: create `factory/` and grant it quota, with a preview first and
then the real call.

```
create_namespace("factory", dry_run=true, grants=[…])          # quota preview (2.2.19)

create_namespace("factory", idempotency_key="boot-1", grants=[
    grant(/vm-std,          ops=[create], limits={cap: 32 cpu, 128Gi}, reserved=true, regrant=true, as=vm-guaranteed),  # → factory/vm-guaranteed
    grant(/vm-std,          ops=[create], limits={cap: 64 cpu, 256Gi}, regrant=true),  # → factory/vm-std, the burst
    grant(/vm-spot,         ops=[create], limits={cap: 256 cpu, 1Ti},  regrant=true),  # → factory/vm-spot
    grant(/volume,          ops=[create], limits={cap: 20Ti},          regrant=true),  # → factory/volume-alloc
    grant(/sys/egress,      ops=[tunnel], limits={hosts: ["*:443"]},   regrant=true),  # → factory/egress
    grant(/sys/runner,      ops=[launch], limits={budget: {unit: usd, per_day: 2000},
                                                     adapters: [claude-code, codex]}, regrant=true),  # → factory/runner
])
grant(/users/alice/secrets/anthropic, to=/sys/model, ops=[read],
      limits={for: ["/factory/**"]}, lease=90d)   # /sys/model may attach alice's key for the factory
```

Nothing in the factory reaches outside Shibu except through its grant
on `/sys/egress`, which only `svc/` will get, narrowed to the hosts its
proxies need ([2.2.13](#2213-egress-policy)). The factory's guaranteed
32 cpu and its burst to 96 are two grants of `/vm-std`, one
`reserved` and one shared ([Quota](#quota)). It holds nothing to
publish with: public exposure is a grant to `/sys/ingress`, which any
owner of an endpoint can make, and which root's `/sys/ingress` may
refuse to serve by its own policy ([2.2.12](#2212-pre-approved-publish)).

Every grant to `factory` carries `regrant=true`, because the factory's
whole job is to split them among `svc`, `lead`, `ci`, `mq` and `rel`.
Below it, each grant carries `regrant` only where its grantee splits it
again; elsewhere the default, `regrant=false`, means the grantee can use
what it was given but not hand it on, so nothing the factory received
can leave it through a namespace inside it ([Grants](#grants)). That is
all v1 confines. A namespace inside the factory can still grant its own
objects outward, such as an agent sharing its own `messages` with a
person ([2.2.25](#2225-messages)), which the factory wants. Fencing
everything else in, so that nothing inside `factory/` may be granted
outside it except a chat, is a policy beyond v1
([2.7](#27-beyond-v1-policy)); until then the factory relies on the
narrow grants it issues and on audit
([F21](#f21-visibility-leaks-are-a-matter-of-policy-and-audit)).

There is no model credit card. The factory's model spend is its grant on
`/sys/runner`, $2000/day, which `/sys/model` meters and enforces
([2.2.23](#2223-model-access-and-budgets)). The factory can name
alice's API key as a `credential` reference when it makes agents, but
can never read it: `/sys/model` holds the only `read` grant, from
alice, limited `for` the factory's subtree, so the factory is not
credential-holding, and no namespace outside it can spend the key
through `/sys/model` ([2.2.24](#2224-secret-store)).

From here on the human acts as `factory` in the UI for the rest of the
bootstrap.

#### 4.2.3 Shared services in `factory/svc/`

*Additional components, using the distribution's credential proxy.*

Step 2: create `svc/`. It is the only namespace with a grant on
`/sys/egress`, because the proxies have to reach GitHub, the cloud and
the package registries. Model calls go
through `/sys/model`, which is Shibu's own and outside the factory.

```
create_namespace("factory/svc", grants=[
    grant(factory/vm-guaranteed, ops=[create], limits={cap: 16 cpu, 64Gi}, reserved=true),  # → svc/vm-guaranteed
    grant(factory/volume-alloc,  ops=[create], limits={cap: 12Ti}, regrant=true),          # → svc/volume-alloc
    grant(factory/egress,        ops=[tunnel], limits={hosts: ["github.com:443", "api.github.com:443",
              "*.googleapis.com:443", "proxy.golang.org:443", "registry.npmjs.org:443", "pypi.org:443",
              "files.pythonhosted.org:443"]}),                                              # → svc/egress
])
```

No lease: shared services live as long as the factory. `cloud-prod`
lives in `svc/prod`, so `svc` owns it by the tree and could grant it;
`svc`'s controller is written never to, and the factory audits every
grant of it. Forbidding `svc` to grant it out, while `factory` still
can, is a deny predicate `factory` attaches to the `svc` subtree, a
policy beyond v1 ([2.7](#27-beyond-v1-policy)).

Step 3: an `svc` controller VM, or the human, creates the services.
Each is a VM with `restart: always` and an endpoint on a port. The
volumes are created through `svc/volume-alloc`.

- **git.** A git server VM, with one volume per repo under
  `svc/repos/`. The repo's path in Shibu's git space is
  `factory/svc/repos/<name>`. This is the Shibu-hosted shape of the
  `git` contract, run by the factory itself rather than by `/sys/git`
  ([`/sys/git` and `/sys/forge`](#sysgit-and-sysforge)). It could as
  well mirror real GitHub repos through `gh-proxy`. The walk-through
  works either way, because pushes go through a proxy.
- **pkgcache.** A cache VM. It is the only route to public package
  registries.
- **artifacts.** An HTTP artifact store. Its backing volume is charged to
  `svc`.
- **db.** A shared Postgres for workers' and CI's integration tests,
  holding test fixtures and nothing secret. It is unmodified Postgres,
  reached through a tunnel (below).
- **images.** A namespace of read-only image volumes (`agent-base`,
  `ci-base`, `controller`), and where the image builder's output is
  deposited ([4.2.9](#429-release-and-deploy)).
- **repo-warm and build-cache.** Volumes that a nightly `svc` job keeps
  current (fresh checkout, warmed `node_modules` / Go module cache /
  build cache). They are snapshotted, and the snapshots are what workers
  and CI clone ([2.2.3](#223-volume-snapshot-and-clone)).
- **Credential proxies:** `gh-proxy`, `cloud-preview`,
  `prod/cloud-prod`. Each attaches its secret to outgoing requests
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). They are not
  VMs `svc` runs. `svc` could be given a `read` grant on a key, and
  the kernel would allow it, but then the key would sit on a VM in
  `svc`, reachable by everything the exposure query lists for that VM
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)). The factory
  keeps its keys out of its own tree instead. Each
  proxy is an endpoint that `svc` creates through the distribution's
  credential proxy, `/sys/cred`, naming a secret in `svc` by reference
  ([F9](#f9-secrets-have-no-shibu-home)). `svc` has first granted
  `/sys/cred` `read` on that secret, leased and `for` `svc`'s own
  subtree ([2.2.24](#2224-secret-store)). The endpoint lands in `svc`,
  and `svc` grants it like anything else it holds. There is no model
  proxy in `svc`: `/sys/model` is a distribution component at the root
  ([2.2.23](#2223-model-access-and-budgets)).

The secrets are written with the kernel's `put_secret`
([2.2.24](#2224-secret-store)). Only `/sys/cred`, which is
credential-holding, reads their values, through the `read` grants
`svc` gave it. Each read is an audit line naming the version.

Step 4: grant the services to the working namespaces. `svc` holds them,
so `svc` grants them, through its controller or the human acting as
`svc`. Each is granted once, at the top, with `subtree=true` where
everything below needs it.

| Endpoint | To | ops / limits | Subtree | Why |
| --- | --- | --- | --- | --- |
| `svc/git` | `lead`, `mq` | fetch, push | yes | workers, sandboxes and the merge queue push |
| `svc/git` | `ci`, `rel` | fetch | yes | CI and builds only read |
| `svc/pkgcache` | `lead`, `ci`, `mq`, `rel` | GET | yes | the only package route for namespaces with no `/sys/egress` grant |
| `svc/images/` | `lead`, `ci`, `mq`, `rel` | read | yes | every VM boots from it ([2.2.14](#2214-images)) |
| `svc/repo-warm` (snapshot) | `lead` | read | yes | workers clone from it |
| `svc/build-cache` (snapshot) | `ci`, `mq` | read | yes | CI clones from it |
| `svc/artifacts` | `ci`, `mq` | read, write | yes | upload and fetch |
| `svc/artifacts` | `rel` | read | yes | builds fetch binaries |
| `svc/db` | `lead` | tunnel, `max_connections: 40`, regrant | no | the planner grants each worker its own share (below) |
| `svc/db` | `ci` | tunnel, `max_connections: 40` | yes | integration tests |
| `svc/gh-proxy` | `lead`, `mq` | pull requests, issues | yes | pull-request/issue API |
| `svc/gh-proxy` | `rel` | releases | yes | release notes and tags |
| `svc/cloud-preview` | `rel` | deploy | yes | preview deploys |
| `svc/prod/cloud-prod` | *nobody* | — | — | only per release, by the human ([4.2.9](#429-release-and-deploy)) |

Model access is not in the table, because `svc` doesn't hold it. It is
a `launch` grant on `/sys/runner`, which the factory hands down from
`factory/runner` as each agent's `budget` (4.2.4, 4.2.8). It is never
granted with `subtree`, so only the namespaces somebody gives a
`launch` grant can start a harness at all, and nothing in a VM can call
a model as its namespace; the shared build VMs in `workers/build`
can't.

What the table shows:

- **Who holds a credential is who holds a grant on its proxy.** `ci/`
  has no grant on `gh-proxy`, so a malicious test cannot open pull
  requests. Nobody holds `cloud-prod`.
- **A grant carries a mode.** CI's git grant is fetch-only, enforced by
  the web proxy on the git smart-HTTP paths. Policy finer than paths and
  methods, such as which branches `lead` may push to, is the git
  service's own, applied to the caller assertion
  ([2.2.22](#2222-caller-assertion)): the assertion names the namespace
  and the grant, so `git` knows a push came from `lead/workers/w17`.

**grain today:** the git proxy authorizes per *task scope* (the repo it
writes and the repos it reads). A Shibu git service applies the same
per-caller policy to the caller assertion.

**A database over a tunnel.** `svc/db` is unmodified Postgres, and its
clients are unmodified `psql` and drivers. The tunnel and the shim do
the rest ([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)).

The db VM runs Postgres on its own localhost, with `shibu serve` beside
it on the port the endpoint points at:

```
create(svc/volume-alloc, target=svc/db-data, spec={size: 200Gi})
create(svc/vm-std, target=svc/db-vm,
       spec={shape: 4cpu/16Gi, image: svc/images/postgres, restart: always,
             volumes: {/var/lib/postgresql: svc/db-data},
             endpoints: {8080: svc/db}})

# inside db-vm, beside postgres listening on localhost:5432:
shibu serve factory/svc/db --to localhost:5432 --proxy-protocol
```

`svc` grants it as in the table. `lead` gets it without `subtree`, so
that the planner can give each worker its own grant with its own lease,
and take it back:

```
grant(svc/db, to=factory/lead, ops=[tunnel], limits={max_connections: 40}, regrant=true)   # → lead/db
grant(svc/db, to=factory/ci,   ops=[tunnel], limits={max_connections: 40}, subtree=true)   # → ci/db

# later, as the planner, when it starts w17 (4.2.5):
g = grant(lead/db, to=lead/workers/w17, ops=[tunnel],
          limits={max_connections: 4, max_duration: 8h}, lease=8h)                         # → w17/db
```

In `w17`'s sandbox VM, the worker reaches the database the usual way,
say to check a migration it wrote. `workers/build` gets nothing: the
grant is neither `subtree` nor `regrant`.

```
shibu forward factory/svc/db 5432 &
psql postgres://test@localhost:5432/app_test -c 'select 1'
```

Each connection `psql` makes to `localhost:5432` is a WebSocket upgrade
through the web proxy. The proxy checks `w17`'s grant, stamps a caller
assertion naming `lead/workers/w17` and grant `g`, and opens the
tunnel. `shibu serve` passes the assertion on to Postgres as a PROXY
protocol v2 header, so a policy in the database (or in a
protocol-aware proxy in front of it) could, say, give `ci` read-only
access. The web proxy can't do that itself: all it knows is "may open
a tunnel".

When `w17` is done, or misbehaves, the planner revokes:

```
revoke(g)
```

The web proxy closes `w17`'s open tunnels at once, and `psql` sees the
connection drop. It doesn't wait for them to finish, as it would for an
HTTP request ([Grants](#grants)). The same happens if `g`'s lease
expires, or if `svc` revokes `lead/db`, which cascades to every
worker's grant. `ci/db` is untouched.

#### 4.2.4 Intake and planning in `factory/lead/`

*Additional components.*

Step 5: create the planner agent. `create_agent` makes `lead/` itself
the planner's namespace (4.1). The human does it, acting as `factory`.
The planner reads, talks and creates other agents; it runs no commands
of its own, so it has **no VM**: it runs in the distribution's pool
runner, and its `tools` are empty.

```
create_agent("factory/lead", payer=factory/vm-std, idempotency_key="planner-1", spec={
    adapter: claude-code,
    model: claude-opus-4,
    runs_in: factory/runner,                       # factory's launch grant on /sys/runner
    tools: [],                                     # a planner with zero VMs
    credential: /users/alice/secrets/anthropic,    # a reference; /sys/model attaches it
    job: "<planner prompt>",
    budget: {unit: usd, per_day: 1200},            # narrowed from factory/runner's budget
    regrant_launch: true,                          # → lead/runner, for its workers
    grants: [
        grant(factory/vm-std,       ops=[create], limits={cap: 64 cpu, 256Gi}, regrant=true),  # → lead/vm-std
        grant(factory/volume-alloc, ops=[create], limits={cap: 4Ti},           regrant=true),  # → lead/volume-alloc
    ],
})                                                 # no lease: long-lived
```

The planner is long-lived: no lease, and if `/sys/runner` restarts it
relaunches the planner's session with `resume` (4.1). It holds
everything `lead` holds, because it *is* `lead`. It is the only agent
that does. It has no VM, so nothing is charged to `factory/vm-std` for
it; its 64-cpu grant is for what it creates. Its harness acts as `lead`
through its session binding, with `via=runner`.

The human now has the planner's chat: `factory/lead/messages` is in
`factory`'s subtree, so the UI lists it while she acts as `factory`. To
see it from her own namespace, she grants it to herself, still acting
as `factory`:

```
grant(factory/lead/messages, to=/users/alice, ops=[write])
```

`factory/runner` is the factory's `launch` grant on `/sys/runner`,
made when the factory was created (4.2.2), limited to the adapters the
factory pays for. The credential is an API key, which
`/sys/runner` reaches the model with through `/sys/model`, so no VM
holds it. Had alice stored a Claude subscription instead, the same
runner would hold the login through a `read` grant from alice, leased
and `for` the factory, with a `{unit: hours, per_day: …}` budget. Nothing else in this walk-through would
change.

**grain today:** the director is an ordinary task that is never
finished (`docs/director.md`).

Step 6: the planner puts its workers under one cap, and keeps the rest
for itself. This is the split that resolves
[F10](#f10-a-supervisor-shares-its-budget-with-what-it-supervises).

```
create_namespace("factory/lead/workers", grants=[
    grant(lead/runner,        ops=[launch], limits={budget: {unit: usd, per_day: 960}}, regrant=true),  # → workers/runner; lead keeps $240
    grant(lead/vm-std,        ops=[create], limits={cap: 56 cpu, 224Gi}, regrant=true),  # → workers/vm-std
    grant(lead/volume-alloc,  ops=[create], limits={cap: 3500Gi},        regrant=true),  # → workers/volume-alloc
])
```

However much the workers spend, $240/day of model budget and 8 cpu stay
with the planner. `lead/runner` is the planner's launch grant from
step 5, and its `regrant` is what lets it hand on $960 of its $1200.
The amount is absolute: if the planner's own budget changes, it
re-grants. These three grants carry `regrant` because the planner
splits them again, per worker. No grant on the credential is needed:
workers name it as a reference, and `/sys/model`'s `read` grant is
already limited `for` the factory.

Step 7: webhook ingress. The planner has no VM, so it creates a small
one to receive webhooks, and publishes its intake port by granting it
to `/sys/ingress` ([2.2.12](#2212-pre-approved-publish)).

```
create(allocator=lead/vm-std, target=lead/intake-vm,
       spec={shape: 1cpu/2Gi, image: svc/images/intake, endpoints: {"8443": webhook}})
grant(lead/gh-hook-verify, to=/sys/ingress, ops=[POST], as=factory-lead-hook-verify)
grant(lead/webhook, to=/sys/ingress, ops=[POST], limits={paths: ["/github"]},
      as=factory-lead-webhook,
      labels={ingress.shibu.dev/audience: anyone,
              ingress.shibu.dev/verifier: /factory/lead/gh-hook-verify})
# → https://ingress.<shibu>/factory-lead-webhook
```

GitHub issue and PR events (or a form a human fills in) arrive here. The
intake VM turns each into a work item, a row on a volume the planner
reads through `intake-vm`'s `mcp`, which it holds `read` on by the
tree.

A webhook carries an HMAC signature, and checking it needs the webhook
secret, which never enters the intake VM. The distribution credential
proxy's `ingress-verifier` kind, which `/sys/ingress` calls before it
forwards anything, checks it ([F9](#f9-secrets-have-no-shibu-home)).
It reads the secret through the `read` grant `lead` gave `/sys/cred`,
so the key sits in credential-holding `/sys/cred`, not in `lead`,
which runs agents. This
is also how an external caller reaches Shibu without a token
([2.2.11](#2211-no-attenuated-tokens)).

Step 8: the planner plans item 17 and spawns a worker. The planner
decides the budget and the lease. This is the delegation step Shibu
exists for. It is the `create_agent` helper run by an agent, from the
workers' grants.

Once, before its first worker, the planner sets up **shared build
VMs**: a namespace holding a few VMs that run the code its workers
didn't write, such as a change's full test suite, and that holds
nothing else (step 10):

```
create_namespace("factory/lead/workers/build")                       # no /sys/egress grant
for b in b1 b2 b3 b4:
    create(allocator=workers/vm-std, target=workers/build/$b,
           spec={shape: 8cpu/32Gi, image: svc/images/agent-base})       # serves mcp
```

Then, per item, it clones the warm repo snapshot into the workers'
namespace, so the checkout outlives the worker and can be handed to a
reviewer (4.2.7), and creates the worker. The worker runs in the pool
runner. Its `tools` are its own VM, created by the call, with the
checkout attached, and `exec` on one shared build VM:

```
create(allocator=workers/volume-alloc, target=workers/src-17,
       spec={kind: volume, from: svc/repo-warm-1002})              # a clone of the warm snapshot (2.2.3)

create_agent("factory/lead/workers/w17", payer=workers/vm-std, idempotency_key="item-17", spec={
    adapter: claude-code,
    model: claude-sonnet-4,
    runs_in: workers/runner,                                                # → the pool runner
    tools: [w17/vm/mcp,                                                     # created by this call
            {endpoint: workers/build/b2/mcp, ops: [exec, read, write]}],
    credential: /users/alice/secrets/anthropic,                             # a reference
    job: "<item 17 description>",
    budget: {unit: usd, per_day: 40},                                       # narrowed from workers/runner
    shape: {cpu: 4, ram: 16Gi, volumes: {/src: w17/src}},
    grants: [
        grant(workers/src-17,       ops=[read, write]),                         # → w17/src
        grant(workers/vm-std,       ops=[create], limits={cap: 12 cpu, 48Gi}),  # → w17/vm-alloc
        grant(workers/volume-alloc, ops=[create], limits={cap: 300Gi}),         # → w17/volume-alloc
    ],
    keep: {bytes: 1Gi, from: workers/volume-alloc},                          # → w17/keep, into lead/keep/w17/
    lease: 8h,
})
label("factory/lead/workers/w17", item=17, kind=worker, work=17)
```

`keep` gives the worker a deposit area for what it decides late to
keep, such as its notes
([2.2.4](#224-lifecycle-and-keeping-results)). The worker's exit
doesn't delete its namespace, so its chat and transcript stay readable
after it exits, until the planner deletes it or the lease does.

The planner, in `lead`, can issue grants from `lead/workers`' handles
because they are in its subtree and carry `regrant`
([Grants](#grants)). The worker's own VM, 4 cpu, is charged to
`workers/vm-std` and lives in `w17`, with the checkout mounted at
`/src`. Its edits run there, as `w17`. Its harness runs in the pool
runner, as `w17` with `via=runner`. Its 12-cpu grant is for what it
creates itself.

`shape.volumes` attaches volumes to the VM the call creates.
It may name volumes in the caller's subtree or handles the call's own
`grants` create, as `w17/src` is here.

`w17` already sees `git`, `pkgcache`, `images` and `repo-warm` through
the subtree grants to `lead`. The planner grants nothing else per
worker.

The planner created `w17`, so `w17/messages` is the planner's channel
to the worker. If the worker gets stuck on something only a person can
answer, the planner grants the chat to alice, and it appears in her
UI:

```
grant(lead/workers/w17/messages, to=/users/alice, ops=[write], lease=1d)
```

**grain today:** filing a task and dispatching it. The worker's cap,
lease and budget correspond to the run budget (2h), `MaxWorkers`, and
the ledger charge for the sandbox shape. Approval of the sub-task is
replaced by the planner *holding* the quota: it can spawn `w17` because
it has headroom, not because a human approved it. This is grain's
sub-task-proposal question, and Shibu answers it with quota.

#### 4.2.5 Worker agents and their sandboxes

*Additional components, using kernel agents.*

Step 9: the worker's workspace is ready when it starts. Its own VM,
`w17/vm`, has the checkout at `/src` (step 8). Its harness edits files,
runs quick builds and calls `git` through that VM's `mcp`.

Everything that runs there has `w17`'s authority: its VM and volume
grants, and push through `git`. That is how `create_agent` works
([F29](#f29-an-agents-tools-run-with-the-agents-authority)), and it is
the right authority for the worker's own edits and builds. Its model
budget is not among it: that is the `launch` grant the runner holds the
session under, and nothing in the VM can call the model as `w17`.

Step 10: code that should not have that authority runs on a shared
build VM. The change's full test suite runs code from the repo and its
dependencies, which the worker didn't write, so it runs in a different
VM in a different namespace ([Identity](#identity)). The worker pushes
its branch, then calls `exec` on `workers/build/b2` to fetch it and run
the tests there:

```
exec(workers/build/b2/mcp, "git -C /work/17 fetch origin w/17 && make -C /work/17 test")
```

`exec` lends `b2`'s identity
([VM `mcp`](#vm-mcp-and-the-agent-control-mcp)): the tests run as
`workers/build`, not as `w17`. Several workers share each build VM, and
each holds `exec` on it, so they share that identity and can see each
other's directories under `/work`. That is the planner's choice: the
build VMs hold nothing worth taking, and the workers are the planner's
own. A planner that wants workers apart gives each its own build VM.

Step 11: `workers/build` holds no allocator, no `launch` grant, no
secret and no control endpoint except its own. Code running in it can
fetch packages and fetch from `git`, and nothing else. It cannot spawn
VMs, spend model budget, push, or change a worker's checkout. No token
is involved: it is simply what `workers/build` holds.

A test runner that must not push runs on a build VM, never in the
worker's own VM. Any process in the worker's VM can make every request
the worker can ([2.2.11](#2211-no-attenuated-tokens)).

Step 12: the worker edits code in its own VM and pushes branch
`w/17` through `git`. The git service sees a caller assertion naming
`lead/workers/w17` and the subtree grant to `lead` it came through, and
its policy allows `w/17` and refuses `main`.

**grain today:** the git proxy's per-sandbox token plus the task's write
scope (`grain/task-N` only, never the default branch).

#### 4.2.6 CI

*Additional components.*

Step 13: create `ci/`, a long-lived namespace holding a CI controller
VM, and its warm pool, `ci/pool/`. The pool is the standard
distribution's `warm-pool` component, run inside `ci`'s own subtree
([2.2.2](#222-caps-reservations-and-adoption)).

```
create_namespace("factory/ci", grants=[
    grant(factory/vm-spot,      ops=[create], limits={cap: 32 cpu, 128Gi}, reserved=true, regrant=true, as=vm-spot-guaranteed),  # → ci/vm-spot-guaranteed
    grant(factory/vm-spot,      ops=[create], limits={cap: 160 cpu, 640Gi}, regrant=true),                        # → ci/vm-spot, the burst
    grant(factory/vm-std,       ops=[create], limits={cap: 8 cpu, 32Gi}),                                           # → ci/vm-std
    grant(factory/volume-alloc, ops=[create], limits={cap: 3Ti}, regrant=true),                                     # → ci/volume-alloc
])

create_namespace("factory/ci/pool", lease=7d, grants=[
    grant(ci/vm-spot, ops=[create], limits={cap: 160 cpu, 640Gi}),   # → ci/pool/vm-alloc
])
create(ci/pool/vm-alloc, target=ci/pool/vm, lease=7d,
       spec={shape: 2cpu/4Gi, image: svc/images/warm-pool,
             env: {POOLS: "8cpu/32Gi/100Gi svc/images/ci-base size=6"}})
# the pool's VM declares its allocator endpoint, ci/pool/alloc
```

The pool's own VM is small. It keeps six waiting 8-cpu `ci-base` VMs,
48 cpu, in `ci/pool`, charged to its grant of `ci/vm-spot`, inside the
160-cpu burst grant. The controller's own CI VMs that must not wait
for spot capacity draw on `ci/vm-spot-guaranteed`, the 32 cpu the
factory set aside for `ci` with a `reserved` grant. The controller
gives every object it creates directly in `ci` a lease: the pool and its
VM get 7 days, which the controller renews, so whatever a crashed
controller leaves behind expires within a week. v1 can't make a lease
compulsory in `ci`; requiring one is a policy beyond v1
([2.7](#27-beyond-v1-policy)). `ci` owns
`ci/pool/alloc` by the tree, so it can grant it onward.

Step 14: a push to `w/17` needs to reach the CI controller. Shibu's
`watch` covers *Shibu objects*, and a git push is not one, so the git
server calls the CI controller. `ci` holds `ci/hooks`, so `ci` grants
it, once:

```
grant(ci/hooks, to=factory/svc, ops=[POST], limits={paths: ["/push"]})
```

Step 15: for each change, the CI controller creates a CI namespace with
a lease, a lend from its pool and a deposit into `ci/results/`. CI
namespaces are leased and reaped: the controller deletes each one when
its last VM exits, and the lease bounds it if the controller is gone
([2.2.6](#226-leases-and-ttls)).

```
create_namespace("factory/ci/c17-1", lease=45m,                 # no /sys/egress grant: pkgcache only
    labels={change: 17, sha: abc123, work: 17}, grants=[
    grant(ci/pool/alloc,    ops=[create], limits={cap: {cpu: 32, vms: 4}}),               # → c17-1/vms
    grant(ci/volume-alloc,  ops=[create], limits={cap: 100Gi}),                           # → c17-1/volume-alloc
    grant(ci/volume-alloc,  ops=[create], limits={into: "ci/results/c17-1", bytes: 5Gi}), # → c17-1/results-alloc
])

create(allocator=c17-1/volume-alloc, target=c17-1/cache,
       spec={kind: volume, from: svc/build-cache-latest})     # a clone of the newest cache snapshot
create(allocator=c17-1/results-alloc, target=ci/results/c17-1, spec={size: 5Gi, lease: 14d})
create(allocator=c17-1/vms, target=c17-1/vm-{1..4},
       spec={shape: 8cpu/32Gi, image: ci-base, restart: on-failure}, wait=10m)
```

The pool answers each VM create with a waiting `ci-base` VM, and the
kernel adopts it: the VM is re-homed from `ci/pool` to `c17-1`, its
origin now maps to `c17-1`, and its payer is `c17-1/vms`. The pool
boots a replacement.

The job VMs shard the suite, upload binaries to `artifacts` (an HTTP
service, not a Shibu object), and write JUnit and logs to
`ci/results/c17-1`. A shard that ran its tests exits 0 whatever they
found: the verdict is in the JUnit. A non-zero exit means the job
itself broke, which is what `on-failure` restarts. Nothing calls an
exit on `c17-1`. The controller, watching, sees the fourth VM exit
`completed`, and deletes `c17-1` itself
([2.2.4](#224-lifecycle-and-keeping-results)):

1. **Keep.** There is nothing to keep, because the results volume was
   created in `ci/results/` and charged to `ci` from the start, so it
   outlives `c17-1`, with its own name and a 14-day lease. Had a VM
   dumped core, the controller would `move` the dump into
   `ci/results/c17-1` now, before it deletes.
2. **Delete.** The controller calls `delete(c17-1)`. `c17-1` enters
   `deleting`, cause `delete`. Nothing is left running to stop.
   `c17-1`'s three grants are revoked, the cache clone and the four
   exited VMs are deleted, and their charges drop off `ci`'s books.

`watch` on `factory/ci` shows each VM's exit and then the `deleting`
event. A factory that would rather not write this loop in its
controller runs the standard reaper on `factory/ci` and labels each run
for it; the lease is the backstop either way. Results don't pile up, because they carry a lease, and names
don't collide, because each run deposits under its own name
([F5](#f5-moved-outputs-pile-up-at-the-parent-and-only-go-one-level)).

**Borrowing under load.** Thirty pushes land in ten minutes, each change
one `cN-1` with a 32-cpu grant:

- The grants sum to 960 cpu against `ci`'s 192. That is fine, because
  caps are limits, not reservations
  ([2.2.2](#222-caps-reservations-and-adoption)).
- Each create walks two chains. In the pool's dimensions it is checked
  and charged on `cN-1/vms` → `ci/pool/alloc`. The VM that answers it
  is charged in kernel units on `ci/pool/vm-alloc` → `ci/vm-spot` →
  `factory/vm-spot` → `/vm-spot`, where the pool made it.
- The pool declares a capacity of 192 cpu, the size of its VM grant,
  and doesn't refill beyond that grant's headroom. At 24 VMs (192 cpu)
  the next create cannot be admitted. It carries `wait=10m`, so it
  queues FIFO on `ci/pool/alloc` and is admitted as
  earlier runs' VMs exit and their namespaces are deleted
  ([2.2.21](#2221-admission-queue)). The controller knows the waiting
  run has a create in flight, so it doesn't delete it as finished. The
  queue shows
  on `watch` and in the UI.
- Six of the first VMs were waiting in the pool and were adopted. The
  other eighteen are cold boots by the pool, adopted and charged the
  same way.

**Paying from the change's budget.** A factory can instead charge a
change's CI to the worker that made it. The planner grants part of
`w17`'s VM quota to `ci`, labelled so that only change 17's runs can use
it ([F19](#f19-cost-attribution-for-a-unit-of-work-spans-subtrees)):

```
grant(lead/workers/w17/vm-alloc, to=factory/ci, ops=[create], limits={cpu: 16, label: {work: 17}})
```

The CI controller then names that grant when it creates `c17-1`'s VMs,
and `cost("factory/lead/workers/w17")` includes them. This walk-through
keeps CI on `ci`'s own pool.

**Preemption.** `c17-1/vm-3` is a spot VM, and the backend takes it
back.

- Its status changes to `preempted`, then `restarting`, and its restart
  count goes to 1. Each is a `watch` event
  ([2.2.17](#2217-exit-status-and-reason)).
- `restart: on-failure` restarts it on a fresh VM. Its ephemeral storage
  is gone. Its attached volumes are not, because they belong to `c17-1`
  and `ci`.
- The CI controller sees the events and re-runs vm-3's shard.
- If preemption keeps happening until the lease runs out, the lease
  deletes `c17-1`. Its `deleting` event says `lease`, and `vm-3`'s
  status history says `preempted` with a restart count, not `failed`,
  so the controller retries rather than reporting a failure. The
  namespace has no exit reason of its own; the VMs carry it.

**grain today:**

- CI jobs are admitted against the shared `model.Ledger` and per-team
  `MaxCIJobs`, and they wait for room. They do not fail.
- Job results are cached by content fingerprint. A Shibu CI would build
  that itself, keyed on volume snapshots.

#### 4.2.7 Review

*Additional components.*

Step 16: the worker tells the planner item 17 is ready, by calling the
planner's intake endpoint (or on `w17/messages`, which the planner
holds). The planner creates a reviewer agent from the workers' grants,
as in step 8. Each grant is made by whoever holds the thing, with a
mode, and the reviewer is a different adapter, to get a second opinion
from another vendor:

```
create_agent("factory/lead/workers/r17", payer=workers/vm-std, spec={
    adapter: codex,
    credential: /users/alice/secrets/openai,                                   # a reference
    job: "review item 17",
    model: gpt-5,
    runs_in: workers/runner,
    tools: [r17/vm/mcp,                                                         # its own VM, created by this call
            {endpoint: w17/vm/mcp, ops: [read]}],                               # read-only file tools on the worker's VM
    budget: {unit: usd, per_day: 10},
    shape: {cpu: 4, ram: 16Gi, volumes: {/src: r17/src}},
    grants: [
        grant(lead/workers/src-17, ops=[read]),                                 # → r17/src, the working tree, read-only
        grant(ci/results/c17-1,    ops=[read]),                                 # → r17/results, CI results, regranted
        grant(lead/volume-alloc,   ops=[create],
              limits={into: "lead/reviews/17", bytes: 1Gi}),                   # where the review goes
    ],
    lease: 1h,                                                                  # a throwaway worker
})
label("factory/lead/workers/r17", item=17, kind=reviewer, work=17)
```

`/users/alice/secrets/openai` is a second credential, which alice
grants `/sys/model` `read` on the same way as the Anthropic one, `for`
the factory.

- **The working tree.** `src-17` is in the planner's subtree, so the
  planner grants it, read-only ([F3](#f3-aliases-carry-no-mode)).
- **CI results.** These are in `ci`, a sibling. The CI controller
  granted each run's results to `lead` as the run finished, with
  `regrant=true` and a 14-day lease:
  `grant(ci/results/c17-1, to=factory/lead, ops=[read], regrant=true, lease=14d)`.
  The planner regrants it to the reviewer. No ancestor is involved
  ([F2](#f2-alias-churn-for-per-change-collaboration)).
- **The review.** The reviewer deposits it into `lead/reviews/17`,
  charged to `lead`. It outlives `r17`. That is why the planner can
  delete `r17` as soon as the reviewer calls
  `exit(succeeded, "reviewed")`: nothing in its namespace is worth
  keeping, and the planner reads the review, not the chat. The 1-hour
  lease deletes it if the planner doesn't.
- **Most of what the reviewer reads is in git.** The branch `w/17` is
  visible through the subtree grant on `git`. The grants above add the
  working tree and the results, which git doesn't carry.

The planner can pass the CI results on only because the CI controller
granted them with `regrant=true`. Its grant to the reviewer carries no
`regrant`, so `r17` can read the results but not hand them on; nor can
it pass on the working tree or the deposit grant ([Grants](#grants)).

**grain today:** a review task gets read access to the PR through the
git proxy. Reviews are posted to GitHub. There is no Shibu-level
equivalent of either.

#### 4.2.8 Merge queue

*Additional components.*

Step 17: create `mq/`, a long-lived namespace with its own spot quota
and a model budget for repairs.

```
create_namespace("factory/mq", grants=[
    grant(factory/vm-spot,      ops=[create], limits={cap: 64 cpu, 256Gi}, regrant=true),  # → mq/vm-spot
    grant(factory/vm-std,       ops=[create], limits={cap: 4 cpu, 16Gi}),                  # → mq/vm-std
    grant(factory/volume-alloc, ops=[create], limits={cap: 1Ti}, regrant=true),            # → mq/volume-alloc
    grant(factory/runner,       ops=[launch], limits={budget: {unit: usd, per_day: 100}}, regrant=true),  # → mq/runner, for repair agents
])
```

`mq` itself is not an agent. It is a controller VM in an ordinary
namespace, and it uses `mq/runner` only to give repair agents their
budgets.

`mq` holds its `enqueue` endpoint, so it grants it to `lead`, **without
`subtree`**, so that only the planner can enqueue, not the workers:

```
grant(mq/enqueue, to=factory/lead, ops=[POST])
```

The merge queue:

1. takes items 17, 18 and 19;
2. makes a batch ref in git (`mq/batch-42` = `main` + `w/17` + `w/18` +
   `w/19`);
3. creates `factory/mq/b42` with a 45m lease, a 64-cpu grant and a
   deposit grant for its results, exactly like 4.2.6;
4. on success, pushes `main` through `git`, which the git service allows
   only for callers whose assertion names `mq`.

On failure it bisects, creating `b42a` and `b42b`. If `main` itself is
broken, it starts a repair agent:

```
create_agent("factory/mq/fix-42", payer=mq/vm-std, spec={
    adapter: claude-code, credential: /users/alice/secrets/anthropic,   # a reference
    job: "main is red at batch 42; repair it", budget: {unit: usd, per_day: 25},
    shape: {cpu: 4, ram: 16Gi}, lease: 2h,
})
```

Its budget comes from `mq/runner`, and `mq` holds its `messages`.

If a worker's change keeps breaking `main`, the merge queue should be
able to stop the worker. The worker belongs to `lead`, so the planner
grants `mq` exactly that, once per worker it enqueues
([F16](#f16-control-endpoint-aliasing-came-up-three-times)):

```
grant(lead/workers/w17/control, to=factory/mq, ops=[stop])
```

Choices made at this step:

- **The merge queue runs its own CI namespaces** instead of asking `ci`.
  The other way is a grant of `ci/pool/alloc` to `mq`, which keeps one
  CI pool and charges `mq`'s batch CI to `ci`. Both are a single grant
  now. The model doesn't prefer either.
- **The change itself is handed over through git, not through Shibu.**
  The worker's checkout could be granted to `mq` read-only, but nothing
  needs it: the branch is in git. Work flows through application
  services (git, the artifact store, the merge queue's `enqueue`), and
  grants carry infrastructure. That is a stated design principle
  ([F1](#f1-cross-sibling-wiring-needs-a-live-principal-at-the-common-ancestor)).

**grain today:** `docs/merge-queue-batching.md`. It covers the throwaway
`grain-merge-queue/<branch>` ref, one suite per batch, bisection, and
merge-repair runs.

#### 4.2.9 Release and deploy

*Additional components.*

Step 18: create `rel/`, and have `svc` give it the right to publish
images.

```
create_namespace("factory/rel", grants=[
    grant(factory/vm-std,       ops=[create], limits={cap: 16 cpu, 64Gi}, regrant=true),  # → rel/vm-std
    grant(factory/volume-alloc, ops=[create], limits={cap: 1Ti}),                         # → rel/volume-alloc
])

# as svc:
grant(svc/volume-alloc, to=factory/rel, ops=[create],
      limits={into: "svc/images/*", bytes: 20Gi}, regrant=true)                        # → rel/images-alloc
```

`rel` holds a grant on `cloud-preview` (step 4). It holds nothing on
`cloud-prod`.

**Build.** On each landed `main`, `rel` creates `rel/build-<sha>` with a
1h lease, and regrants it a deposit for exactly one image:

```
create_namespace("factory/rel/build-r42", lease=1h, grants=[
    grant(rel/vm-std,       ops=[create], limits={cap: 8 cpu, 32Gi}),                        # → build-r42/vm-alloc
    grant(rel/images-alloc, ops=[create], limits={into: "svc/images/r42", bytes: 20Gi}),     # → build-r42/image-alloc
])
create(allocator=build-r42/image-alloc, target=svc/images/r42, spec={…})                      # as build-r42
```

The image is created in `svc/images/`, charged to `svc`, so it is
visible wherever `svc/images/` is granted as soon as it exists, and it
outlives the build. Nothing has to move it, and nothing at `factory`
has to act ([F1](#f1-cross-sibling-wiring-needs-a-live-principal-at-the-common-ancestor),
[F5](#f5-moved-outputs-pile-up-at-the-parent-and-only-go-one-level)).

**Preview.** For change 17, `rel` creates `rel/preview-17` with a 24h
lease. It holds:

- a VM booted from the new image;
- an endpoint `web` on port 8080;
- a grant of `web` to `/sys/ingress`, which publishes it
  ([2.2.12](#2212-pre-approved-publish)):

```
grant(rel/preview-17/web, to=/sys/ingress, ops=[read], as=factory-preview-17, lease=24h,
      labels={ingress.shibu.dev/audience: "alice@acme.com,team:acme-eng"})
# → https://ingress.<shibu>/factory-preview-17
```

`rel` owns `web` by the tree, so it may grant it. The grant's lease
matches the preview's, and the grant ends anyway when `preview-17` is
deleted, so nothing is left pointing at nothing
([F18](#f18-inside-the-callers-namespace-needs-to-mean-the-subtree)).
The preview is visible only to the audience the label names, behind
`/sys/ingress`'s sign-in, and only for `GET`s. There is no
public-endpoint quota to run out of. How many previews `rel` may
publish, or whether it may publish to `anyone`, is `/sys/ingress`'s
policy, or a deny predicate beyond v1 ([2.7](#27-beyond-v1-policy)),
not a kernel allocator.

The factory still runs a small approvals service, `factory/desk`, an
additional component ([F14](#f14-inbox-requests-need-structure)), for
what `rel` can't grant itself.

**Production.** `rel` cannot deploy to production. It asks the desk,
whose policy sends anything touching `cloud-prod` to a human. The human
sees the request in the UI and, acting as `factory`, grants:

```
grant(factory/svc/prod/cloud-prod, to=factory/rel/deploy-r42, ops=[deploy], lease=30m)
```

`rel/deploy-r42` pushes the image and rolls out through `cloud-prod`.
The grant expires on its own. In v1 `svc` could make the same grant,
since it owns `cloud-prod` by the tree; that it doesn't is its
controller's rule and the factory's audit, until a deny predicate on
`svc` can forbid it (step 2, [2.7](#27-beyond-v1-policy)).

Here production is outside Shibu, a cloud reached through a credential
proxy. If production were Shibu VMs in a `factory/prod` namespace, the
same human approval would grant `rel/deploy-r42` the prod namespace's
control endpoint and a deposit into it:

```
grant(factory/prod/control,   to=factory/rel/deploy-r42, ops=[stop, list], lease=30m)
grant(factory/prod/vm-alloc,  to=factory/rel/deploy-r42, ops=[create],
      limits={into: "factory/prod/*"}, lease=30m)
```

A control-endpoint grant, narrowed by operation, is how this is done
([F16](#f16-control-endpoint-aliasing-came-up-three-times)).

**grain today:**

- `release-candidate` is cut daily.
- `registry-push` and `package-publish` are `CIOnly` capabilities,
  granted only to trusted post-merge events.
- Production deploys are not grain's business.

#### 4.2.10 Observability and cost

*Additional components, reading the kernel's `watch`, `audit` and `cost`.*

- **Watch.** The UI, acting as `/`, runs `watch("factory", selector=*)`
  and renders the tree, its grants and the admission queues. The planner
  watches `factory/lead` to supervise its workers. The CI controller
  watches `factory/ci`. Every VM and agent status change and exit
  ([2.2.17](#2217-exit-status-and-reason)), every namespace entering
  `deleting`, and every grant death is an event on the parent's or the
  grantee's stream.
- **Audit and cost log.** Every `create`, `grant`, `revoke`, `move` and
  secret `read` is logged with the caller's namespace ID; a `read` also
  names the secret's version. Each charge is
  recorded against every link of its grant chain. "What did item 17
  cost?" is `cost("factory/lead/workers/w17")` plus
  `cost("factory/lead/workers/r17")`, plus whatever was charged to
  grants carrying `label: {work: 17}`, such as CI paid from `w17`'s
  budget (4.2.6). The merge queue's batch is shared by three items and
  is split by convention
  ([F19](#f19-cost-attribution-for-a-unit-of-work-spans-subtrees)).
- **Budget hit.** At 15:10 `lead/workers/runner` reaches its budget,
  $960/day:
  - `/sys/model` refuses further calls from every agent under
    `lead/workers`. The factory's runner reaches models through
    `/sys/model`, so it is `/sys/model`, outside the runner, that knows
    how much a call costs and charges it along the session's `launch`
    grant chain ([2.2.23](#2223-model-access-and-budgets)). An agent on
    a subscription would hit its runner-enforced turn or time budget
    instead, with the same exit.
  - `w23`'s worker can no longer make progress. Its agent exits
    `out-of-quota`. Its namespace stays, since an agent's exit never
    deletes it, so the planner can read the transcript and decide what to do with the
    item.
  - The planner is in `lead`, outside `lead/workers`, and still has
    $240/day to think with
    ([F10](#f10-a-supervisor-shares-its-budget-with-what-it-supervises)).
  - It can hold item 23 until the budget resets at 00:00, or ask for
    more: a call to the factory's `desk`, whose policy (if `factory`
    gave it a regrantable slice of `factory/runner`) grants up to $200
    and sends the rest to the human. The planner can also just ask
    alice on `factory/lead/messages`.

#### 4.2.11 Failure cases

*Additional components, and how the kernel bounds them.*

**A runaway agent.**

1. `w31`'s worker loops, creating and deleting VMs.
2. Rate limits are not in the kernel
   ([2.2.16](#2216-control-plane-rate-limits)). Each sandbox is charged
   to `w31/vm-alloc`, so the loop can never hold more than `w31`'s
   12 cpu, and every create and delete is an audit event.
3. The planner sees the churn on its `watch` and stops `w31`'s agent.
   The agent exits `stopped`, which ends its session and its VMs. The
   planner reads the transcript to see what went wrong, then deletes
   the namespace.

When rate limits are added, they are token buckets along the grant chain
([F11](#f11-rate-limits-should-be-hierarchical)), so `w31` cannot
multiply its limit by creating sub-namespaces.

**A lease expires mid-job.**

1. The CI controller watches `factory/ci` with `lease_warning=5m`, so
   at 40 minutes it gets `lease.expiring` for `c22-1`, with the suite
   at 75%. It could extend the lease with `set_lease`; its policy is
   not to, for a run this far over its usual time.
2. `c22-1`'s 45m lease expires with the suite at 80%.
3. `c22-1` enters `deleting`, cause `lease`. Its running VMs are
   stopped with their usual signal and timeout and exit `deleted`, its
   grants are revoked, and its VMs and cache clone are deleted. There
   is no grace period.
4. `ci/results/c22-1` was deposited into `ci` from the start, so it
   survives, holding the partial JUnit and logs.

The CI controller sees the `deleting` event, the VMs' `deleted` exits
and the partial results, and marks the change "timed out" with logs
instead of "lost". A crash of a
VM in the middle of writing results is the same case: the results are
whatever was durably written.

**A leaked VM.**

"Leaked" means something specific here. Everything is charged to a
payer, so no VM is orphaned in the accounting sense. A leaked VM is one
doing nothing in a namespace with no lease, such as a debug VM a worker
created in `factory/lead` through a planner bug.

- **Prevention.** The CI controller leases everything it creates
  directly in `ci` (step 13), so nothing it made can leak for ever, and
  the planner can do the same in `lead`. In v1 that is each creator's
  discipline: making a lease compulsory in a subtree, so a buggy
  creator can't skip it, is a policy beyond v1
  ([2.7](#27-beyond-v1-policy)).
- **Reclaim.** Finding idle VMs needs an idle signal, and a "charged,
  idle, no lease" report. Both are leak tooling, an additional component
  ([F12](#f12-leaks-in-long-lived-namespaces)). The parent deletes what
  it finds, and the charge drops off the payer.

**A worker needs something only a sibling has.**

`w18` needs the generated client that `w17` is building. They are
siblings.

1. `w18` asks the planner, through the planner's intake endpoint.
2. The planner grants `grant(lead/workers/w17/gen-client, to=lead/workers/w18, ops=[read])`.
   `w17` could equally have granted it, since `w17` holds it.
3. If the dependency were in `factory/ci`, `ci` would grant it in the
   same way. No ancestor is involved.

If `w17`'s agent exits, `gen-client` stays: an agent's exit doesn't
delete its namespace, and only the agent has ended. When `w17` is
deleted, `gen-client` goes with it unless it was kept first
([F13](#f13-what-happens-to-an-alias-when-its-target-moves-or-dies)):

- If it is deleted, the grant to `w18` dies, and `w18` gets a `watch`
  event.
- If the planner moves the client to `lead/workers` before it calls
  `delete(w17)`, the planner's grant survives the move, because the
  planner still has authority over the client in its new place. A
  grant `w17` had issued itself is revoked when `w17` is deleted, with
  an event, because `w17` no longer exists to issue it.

Once `w17` is `deleting` it is too late to move anything. A planner
that wants the client to outlive `w17` without having to remember
gives `w17` a deposit area up front, `keep=` or a deposit into
`lead/shared/`, and `w17` builds the client there.

**A worker asks for more quota.**

1. `w17`'s tests need a 16-cpu sandbox. `w17/vm-alloc` has a 12-cpu cap.
2. It asks the planner.
3. `lead/workers` has headroom (its cap is 56 and it is using 40), so
   the planner grants a second, short one:
   `grant(workers/vm-std, to=lead/workers/w17, ops=[create], limits={cap: 8 cpu}, lease=1h)`.
4. If `lead/workers` were at its cap, the worker could wait
   (`create(..., wait=30m)`), or the planner could ask the desk.

Partial approval is a smaller grant, and a condition is a lease or a
limit. There is no inbox format to define
([F14](#f14-inbox-requests-need-structure)).

#### 4.2.12 Teardown

*Additional components.*

Item 17 is merged and its preview has been looked at.

1. `r17` already ended itself: the reviewer called
   `exit(succeeded, "reviewed")` once its review was deposited in
   `lead/reviews/17`, charged to `lead`. The planner, watching, saw
   the exit and called `delete(r17)`, so its VM was deleted and its
   received grants (the working tree, the CI results, the deposit, its
   VM and model grants) were revoked. The review survives.
2. The planner tells `w17` to finish, on `w17/messages`. The worker
   has been writing `worklog` (its notes) into its deposit area,
   `w17/keep`, which is `lead/keep/w17/` and charged to
   `workers/volume-alloc` from the start
   ([2.2.4](#224-lifecycle-and-keeping-results)). Then it calls
   `exit(succeeded, "merged")`.
   - The agent's harness session ends and its VM stops. Its
     status and reason are on the agent object, and its transcript and
     final logs are written into `w17`.
   - `w17` has no lease and nobody has deleted it, so it stays. Alice,
     who was granted
     `w17/messages`, can still read the chat.
3. A day later, the planner calls `delete(lead/workers/w17)`. `w17`
   enters `deleting`; nothing is running in it. Its `exec` grant on
   `workers/build/b2` is revoked with it, and the build VM stays for the
   next worker. The planner has nothing to keep first: `worklog` is
   already in `lead/keep/w17/`. Deletion has no grace period:
   - everything in `w17` is deleted, including the transcript;
   - the grant of `w17/control` to `mq` dies with the namespace;
   - `w17`'s received grants are revoked, including the CI grant
     labelled `work: 17`.
4. `rel/preview-17` expires after 24h, and its grant to
   `/sys/ingress` ends with it, which unpublishes it (4.2.9).
5. The planner deletes `lead/workers/src-17`, the checkout it cloned
   in step 8. Its charge drops off `workers/volume-alloc`.
6. The CI results in `ci/results/` and the review in `lead/reviews/`
   expire with their leases. The worklog stays in `lead/keep/w17/`
   until something deletes it or gives it a lease.

If the whole factory were shut down, the human, acting as `/`, would
first keep what it wants, then call `delete(factory)`:

1. `/` moves or snapshots what it wants to keep, such as the repo volumes in `svc/repos/`, to
   `/users/alice/`, naming `/volume` as the payer, since each was
   charged to a grant inside `factory` that is about to be revoked
   ([Move](#move)). Nothing has to have been declared in advance; it
   only has to happen before the `delete`.
2. `delete(factory)`: `factory` and everything under it enter
   `deleting` at once. Every VM is stopped, every agent ends with
   reason `deleted`, and nothing new can be created anywhere in the
   tree.
3. Everything left is deleted, every grant `factory` received is
   revoked, cascading through the whole tree, and every charge drops
   off its payer.

**grain today:** a run ends and its sandbox is destroyed. The branch
survives because it was pushed (git again). The transcript and closing
note are in grain's store. No artifacts other than CI's cache are
retained.

### 4.3 Repo (tentative)

*Standard distribution, optional: `/sys/git`, or a git service an
additional component runs.*

A repo is a git service endpoint backed by a volume, under the
namespace's folder in the git space: `https://<shibu>/<ns>/<repo>.git`
is the git server's endpoint plus the repo's path. The volume is charged
to the namespace that owns the repo, like any volume. Credentials for
any upstream (GitHub) live in a proxy
([2.2.7](#227-credentials-only-behind-proxy-endpoints)). Who may push
where is the git server's policy, applied to the caller assertion the
web proxy stamps on each request ([2.2.22](#2222-caller-assertion)).

Both are supported: a GitHub-backed proxy and a Shibu-hosted git
server serve the same `/sys/git` contract, and can run side by side.
Pull requests and reviews are a separate contract, `/sys/forge`
([`/sys/git` and `/sys/forge`](#sysgit-and-sysforge)).

### 4.4 A pane served by an agent

*Additional components: the factory's planner and its pane. Kernel: the
web proxy and grants. Standard distribution: the stock UI,
`/sys/ui`.*

This follows [2.6.6](#266-agent-served-panes) end to end, in the
factory of [4.2](#42-full-software-factory). The planner,
`factory/lead`, publishes a "pipeline status" pane: the items in
flight, which worker has each, and where each change is in CI and the
merge queue. bob, an engineer who doesn't run the factory, gets to see
it.

Step 1: the planner serves the pane. It runs a small web server on a VM
of its own rather than in its sandbox VM, so the pane stays up while
the sandbox restarts, and puts two endpoints on it:

```
create(lead/vm-std, target=lead/ui-vm,
       spec={shape: 1cpu/2Gi, image: svc/images/node, restart: always,
             volumes: {/srv: lead/ui-src},                 # the server the planner wrote
             endpoints: {8080: lead/ui/panes, 8081: lead/ui/pipeline}})

GET factory/lead/ui/panes →
[{id: "pipeline", title: "Pipeline status", path: "factory/lead/ui/pipeline",
  icon: "chart", placement: "tab"}]
```

The pane shows what the planner already knows: its work items, its
workers' status labels ([2.6.3](#263-status-labels)), and the CI and
merge-queue verdicts it is told about. It reads nothing on the viewer's
behalf.

Step 2: alice sees it with nothing granted. Acting as `factory` in the
stock UI, she has the planner in her agent list by the tree. The UI
reads `factory/lead/ui/panes` as `factory` and adds a "Pipeline status"
tab to the planner's page. The pane loads in a sandboxed iframe on
`https://<lead-id>.panes.<shibu>/pipeline`, inside a frame the UI draws that
reads "served by factory/lead". The planner isn't listed in the `/sys`
spec's `nav`, so the pane appears only under the planner, not in the
UI's main navigation.

Step 3: sharing it with bob. alice asks the planner in its chat to
share the pane with bob. The pane is the planner's own, in `lead` by
the tree, so it needs nobody's permission, and it grants from its own
namespace, without `regrant`, so bob can look but not share it on:

```
grant(factory/lead/ui/pipeline, to=/users/bob, ops=[read], lease=30d)
grant(factory/lead/ui/panes,    to=/users/bob, ops=[read], lease=30d)   # so his UI has the title
```

bob gets no grant on the planner's `messages`, its workers, or anything
else.

Step 4: bob opens it in the stock UI. Acting as `/users/bob`, his agent
list doesn't include the planner, since he can't reach its `messages`.
The UI finds the pane among his grant handles on `*/ui/*`, lists it as
"Pipeline status, served by factory/lead", and opens it framed as in
step 2. The web proxy fetches it as `/users/bob`, so the planner sees
`ns=/users/bob` in the caller assertion
([2.2.22](#2222-caller-assertion)) and leaves out the model spend it
shows alice.

The bridge adds nothing to what bob holds:

- He clicks worker `w17` in the pane, and the pane sends `navigate` to
  `factory/lead/workers/w17`. The UI goes there as bob, and says that
  he can't see it.
- He clicks "ask the planner about item 17", and the pane sends `post`
  with a `question` for the planner. The UI would show him the message
  to confirm, but bob has no `write` on `factory/lead/messages`, so it
  says so and posts nothing.

Step 5: bob opens it from the CLI:

```
$ shibu open factory/lead/pipeline
opening https://<lead-id>.panes.<shibu>/pipeline in your browser
```

The browser loads the pane top-level on the planner's pane origin,
signed in at the front door for that origin only. There is no frame and
no bridge; the address bar names the namespace, and the page holds no
session of the UI's. `shibu open factory/lead` lists the panes bob can
reach there.

Step 6: it ends. The grants run out after 30 days, or the planner or
alice revokes them sooner. The web proxy refuses bob's next request at
once, and his UI drops the pane. If the planner's namespace is ever
deleted and re-created at the same path, its pane origin changes with
its stable ID, so nothing a browser stored for the old pane carries
over.

**grain today:** grain's own views, such as the board, the merge queue
and CI runs, are part of one React app with full access to the UI's
session. Under Shibu they become panes of grain's components, which root
trusts for the main navigation, but which are isolated in the same way
as the planner's ([F30](#f30-component-ui-panels)).

---
## 5. Findings

These are the places where the walk-throughs, mainly the factory in
4.2, found the model awkward, ambiguous, in need of a workaround, or
missing something. F1–F20 came from the first version of the model, and
the 1679 revision settled each of them. Every finding starts with a
status line:

- **Resolved:** the grants model removes the problem. The walk-through
  in section 4 now shows the resolution.
- **Kernel:** the suggested fix was adopted into the kernel.
- **Additional component:** the problem is real, and the answer is a
  component built on the kernel, not a kernel change.
- **Distribution:** the answer is a component of the standard
  distribution ([2.3](#23-the-standard-distribution-and-the-sys-spec)).
- **Changed:** the finding was reframed.
- **Punted:** deliberately left for later.
- **Open:** a decided answer was withdrawn, and the finding waits on a
  later design.

F21–F25 are new. They are risks the grants model introduces, and they
were **proposals**. F25 became a kernel requirement in 1765; the 1904
revision decides F21–F24 for v1.

F26–F29 came with kernel agents (the 1746 addendum). F26 and F29 were
proposals: 1884 resolved F29, and 1904 decides F26. F27 records a decision and what it leaves open. F28 records
a risk with no conclusion.

F30 came with the layering (task 1750) as a proposal. Task 1771
decided it, and extended it to panes served by any agent or component.

F31 is new with credential-holding namespaces (task 1762). It records a
risk and its mitigations.

F32 came with the lifecycle policies (task 1763) as a proposal. The
1885 revision removed those policies, so it is moot; 1904 records what
it asks of the reaper.

F33–F35 are new with component allocators and backend drivers (task
1765). They record residual risks of decided designs, and what bounds
them.

The 1884 revision (runners and VM `mcp`) resolves F29, and restates
F17, F26, F27 and F31 as each owner's decision rather than a kernel
rule.

The 1886 revision (no boundaries in v1) reopens F33, whose
`allocators_from` mitigation was a boundary key, and points F12, F21,
F23, F24 and F31 at the policy model beyond v1
([2.7](#27-beyond-v1-policy)).

The 1904 revision decides F21–F24, F26 and F32 for v1. Each keeps its
earlier analysis below its status line, marked as such, so the
reasoning and the options not taken stay readable. No finding is a
proposal any more.

Which layer each finding concerns:

| Layer | Findings |
| --- | --- |
| Kernel | F1–F3, F5–F8, F10, F13, F15–F25, F29, F32–F35 |
| Kernel and standard distribution | F4 (the kernel's `adopt`; the warm pool is a distribution component), F9 (the store is the kernel's, the credential proxies the distribution's), F30 (the kernel's web proxy serves each namespace's pane origin; `/sys/ui` frames the panes), F31 (the kernel marks credential-holding and answers the exposure query; owners choose the images) |
| Standard distribution | F26, F27, F28 (`/sys/runner` and its adapters, and any other runner) |
| Additional components | F12, F14 |
| No layer yet | F11 (punted) |

The headings keep the first version's wording, so that links to them
still work.

### F1. Cross-sibling wiring needs a live principal at the common ancestor

**Status: Resolved by grants.**

*Was:* the factory's long-lived parts (`svc`, `lead`, `ci`, `mq`, `rel`)
are siblings, and aliases and moves between them could only be made by
`factory`. Something had to run *as* `factory`: the human, or a foreman
VM holding the whole factory credential.

*Now:* whoever holds a thing grants it. `ci` grants `ci/hooks` to
`svc`, `mq` grants `enqueue` to `lead`, and `svc` gives `rel` a deposit
grant into `svc/images/`. There is no foreman (4.2.1). The principle
is stated: work flows through application services such as git, and
grants carry infrastructure (4.2.8).

### F2. Alias churn for per-change collaboration

**Status: Resolved by grants.**

*Was:* every reviewer needed per-change aliases from the planner, every
CI result needed a per-change alias from `factory`, and every
cross-worker dependency was an inbox request.

*Now:* per-change grants are still made, but by the holder, in one call,
with no ancestor round trip: `ci` grants each run's results to `lead`
with `regrant`, and the planner regrants them to the reviewer (4.2.7).
Selector alias rules were not adopted.
[F23](#f23-grant-sprawl-in-long-lived-namespaces) covers what per-change
grants cost over time.

### F3. Aliases carry no mode

**Status: Resolved by grants.**

*Was:* aliasing a worker's checkout gave the reviewer write access to
it.

*Now:* a grant carries `ops` and `limits`. For a volume that is read vs
read-write and a sub-path, so the reviewer gets `ops=[read]` on the
live checkout (4.2.7). For a VM port it is paths, methods and read vs
write, enforced by the web proxy and visible to the service in the
caller assertion.

### F4. Warm pools do not sit cleanly on namespace-bound allocators

**Status: Resolved by `adopt` and a distribution pool (1870).**

*Was:* a waiting VM had no clear home or payer, a claim was a move the
claimant couldn't make, and nothing said whether a split could use its
parent's pool or what had to be rebound at claim time.

*Now (1870):* the kernel has a general hand-off, `adopt`, and a warm
pool is built on it. A pool is an optional standard-distribution
component: a component allocator holding a VM allocator grant, whose
waiting VMs live in its own namespace and are charged to that grant. A
claim is a `create` through a grant of the pool, answered by adopting a
waiting VM: the kernel re-homes it to the target and moves its charge
to the grant the create named. Anything with a grant chain from the
pool can use it. Nothing needs rebinding except the web proxy's map
from origin to namespace, because no credential is injected. A pooled
VM is never returned. Agents use a pool of `agent-base` VMs in the same
way (2.2.2, 4.2.6).

The 1741 resolution, a kernel `warm-pool` allocator kind, is in
[A.16](#a16-a-warm-pool-allocator-kind-in-the-kernel).

### F5. Moved outputs pile up at the parent and only go one level

**Status: Resolved (1763): outputs removed; deposit areas. (1885:
no rescue during `deleting`.)**

*Was:* outputs drained only one level, piled up with no lease, collided
on names, and moved charges in surprising ways.

*Now:* where the destination is known in advance, the producer gets a
**deposit grant** and creates the object in its final home, with its
final name, payer and lease (CI results in 4.2.6, images in 4.2.9).
Nothing drains, nothing moves, and nothing changes payer.

*1763:* there are no declared outputs. A late decision about what to
keep goes into a deposit area the parent arranged with `keep=`, which
is already the object's final home, payer and name.

*1885:* there is no grace period, so nothing is moved out of a
`deleting` namespace. What nobody planned to keep, the parent moves or
snapshots before it calls `delete` (2.2.4).

### F6. No admission queue: a full allocator can only fail

**Status: Kernel, minimally.**

*Was:* a full allocator could only fail a `create`, so every caller had
to write its own retry loop.

*Now:* `create` takes `wait=<timeout>`, and a full allocator or grant
queues the request FIFO and admits it as charges drop off. Waiters are
visible on `watch` and in the UI. It is in the kernel because only the
allocator sees charges drop off. Priority and fairness policies are
additional components ([2.2.21](#2221-admission-queue)).

### F7. Exit reasons need `preempted` and VM-level status

**Status: Kernel.**

Exit status is on VMs only, and on agents; namespaces have no exit
reason (1763). Every VM has its own status (running, restarting,
exited, preempted), an exit code and a reason (`completed`, `failed`,
`preempted`, `oom`, `deleted`), and a restart count, and each change is
a `watch` event ([2.2.17](#2217-exit-status-and-reason)). A parent that
needs to know a run was lost to preemption reads it from the VMs, not
from the namespace (4.2.6).

### F8. Proxies need the caller's identity

**Status: Kernel.**

The web proxy strips incoming `Shibu-*` headers and stamps a signed,
short-lived `Shibu-Caller` assertion carrying the caller's namespace,
its VM, and the grant the request came through with its `ops` and
`limits`. The verification key is published, so services can
verify assertions even when they are not directly behind the proxy
([2.2.22](#2222-caller-assertion)). The identity in it comes from the
VM's network origin ([Identity](#identity)).

The assertion is carried per transport. In v1, a tunnel's assertion is
made on its upgrade request
([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)). Native
TCP, with mTLS or PROXY protocol v2, comes beyond v1
([2.4](#24-beyond-v1-native-port-endpoints)).

### F9. Secrets have no Shibu home

**Status: Split. The secret store is in the kernel, with pluggable
storage backends (1750). Credential proxies are optional components of
the standard distribution (1750; they were an extension).**

The kernel keeps the credential rule, *a VM that holds a credential
never runs untrusted code*
([2.2.7](#227-credentials-only-behind-proxy-endpoints)), plus caller
assertions (F8) and ambient identity. It holds the secret store
([2.2.24](#2224-secret-store)), because model credentials are needed at
bootstrap and because whatever stores secrets can impersonate everyone.
Its readers are the credential-holding distribution components, which
receive values through `read` grants (1762). The credential proxies are
one of those, the optional `credential-proxy` contract at `/sys/cred`
([2.3](#23-the-standard-distribution-and-the-sys-spec)):

- **The secret store** is the kernel's. `put_secret(ns/name, value)` is
  write-only. Each write is a new value; rotation is writing again. There is no `get`. Where the
  values are kept is the store's storage backend: built-in, Vault or
  GCP Secret Manager.
- **`read` is a grantable op on a secret**, and the only way to read
  one ([2.2.24](#2224-secret-store)). `/sys/cred` receives its keys by
  `read` grant: from the owner of each secret an endpoint names as its
  `secret_ref`, or from root, listed in its `/sys`
  spec entry. Holding one makes `/sys/cred` credential-holding
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)).
- **A `credential-proxy` endpoint kind**, created through `/sys/cred` in
  the caller's namespace:
  `{upstream, secret_ref, inject: header|basic|github-app|gcp-sa|registry}`.
  `secret_ref` is a reference: `/sys/cred` reads it through a `read`
  grant from the secret's owner, `for` the caller; the caller never
  gets `read`.
  `github-app` and
  `gcp-sa` mint short-lived upstream tokens per request rather than
  sending the stored key.
- **Access** to a credential is a grant on its proxy endpoint.
- **Rotation** is writing a new value. The previous one stays readable
  for the store's fixed overlap. Every read is audited, naming the version read. Revoking
  a `read` grant stops further reads but can't un-read a value already
  held, so rotation is also the remedy for a suspected leak.
- **An `ingress-verifier` kind** (`hmac-sha256` first), which
  `/sys/ingress` calls before forwarding to a published endpoint, checks webhook signatures with a stored secret
  (4.2.4).
- **A streaming registry proxy** for image pushes. It removes grain's
  registry-login exception ([7.1](#pkggitproxy-and-pkgmodelproxy)).

Turning `/sys/cred` off leaves existing proxy endpoints unreachable, as
for any revoked grant. The kernel is unaffected.

This is on the critical path of grain's migration: once grain runs as
an additional component, it can no longer hold its GitHub App key or
GCP keys itself ([`shibu-migration.md`](shibu-migration.md)).

**Upstreams that don't speak HTTP** (decided, 1752): in v1, a
credential-injecting proxy for a non-HTTP protocol, such as a Postgres
proxy that logs in with a stored password or an SSH proxy, is a
component behind a tunnel. It receives its secret through a `read`
grant
([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)).

Open within the component: whether a secret can ever be exported out of
Shibu. The lean is never: an operator who needs the raw secret breaks
glass outside the model. A `read` grant hands a value only to a
credential-holding namespace inside Shibu.

### F10. A supervisor shares its budget with what it supervises

**Status: Resolved by caps, and by a `reserved` grant.**

The planner grants its workers' namespace a capped share of its own
grants (`budget: {unit: usd, per_day: 960}` of its $1200 on the launch
grant), and keeps the rest. Percentage limits were dropped (1887): a
planner whose own budget changes re-grants
([A.27](#a27-allocator-limits-reserve-kinds-classes-labels-and-percentages)).
However much the workers spend, the planner can still think (4.2.4,
4.2.10). Where the planner needs a guarantee rather than a ceiling on
the others, it grants with `reserved=true`, or splits a guarantee and a
burst into two grants. Supervision needs reserved
budget, and grants are how it is expressed.

### F11. Rate limits should be hierarchical

**Status: Punted.**

Rate limits are not in the kernel
([2.2.16](#2216-control-plane-rate-limits)). When they are added they
are a *rate* limit, a token bucket at each link of the grant chain, not
a count limit, so a namespace cannot multiply its limit by creating
children.

### F12. Leaks in long-lived namespaces

**Status: Additional component.** Requiring a lease is a policy
beyond v1 ([2.7](#27-beyond-v1-policy)).

In v1 the creator of an object in a long-lived namespace gives it a
lease, as the CI controller does in `ci` (4.2.6), and a reaper deletes
idle job-style namespaces ([2.2.6](#226-leases-and-ttls)). The kernel
can't yet make a lease compulsory: that was the `require_lease`
boundary, dropped with boundaries ([A.22](#a22-boundaries)), and it is
carried over as a requirement on policy. Idle and liveness signals, and
"charged, idle, no lease" reports, are leak tooling built on `list`,
`watch` and the audit log.

### F13. What happens to an alias when its target moves or dies

**Status: Resolved by the validity rule.**

A grant is valid only while its grantor could still issue it. When the
target is deleted, the grant dies and the grantee gets a `watch` event.
Grants reference stable IDs and survive a `move`, unless the move takes
the target out of the grantor's authority; then they are revoked,
cascading, with an event ([Grants](#grants), 4.2.11). A grant that
becomes invalid closes its open tunnels and connections, as a revoke
does ([2.2.26](#2226-non-http-services-tunnels-over-http-endpoints)).

### F14. Inbox requests need structure

**Status: Additional component; the inbox is removed from the kernel.**

Asking for a grant is a call to an endpoint the other party exposes. An
answer is a grant, so partial approval is a smaller grant and a
condition is a lease or a limit. Inboxes, standing approval policies and
escalation are services agents build, like the factory's `desk`
(4.2.9). The convention this document suggests is an endpoint called
`requests` in the namespace that can grant, taking
`{want, for, lease, why}`. Nothing in the kernel depends on it. This
also answers [6.3](#63-sharing-between-peers-with-no-willing-common-ancestor):
a peer offers an endpoint, others call it, and it grants.

### F15. Deep nesting is cheap in the model and expensive in operation

**Status: Changed: not a model issue.**

Depth costs a walk of the grant chain on every `create`. That is an
implementation concern, covered by an
[implementation note](#implementation-note-walking-the-grant-chain) in
section 3: a single walk with cached headroom and fixed-order locking,
and a recommended depth of one namespace per agent plus one per sandbox.

### F16. Control-endpoint aliasing came up three times

**Status: Resolved by grants.**

A control endpoint can be granted, narrowed by operation. The merge
queue gets `ops=[stop]` on a worker (4.2.8), a deploy into a
Shibu-managed `factory/prod` gets a time-boxed control grant (4.2.9),
and there is no foreman, which would be control-endpoint delegation by
sharing a credential (4.2.1). [6.2](#62-aliasing-a-control-endpoint) is closed.

### F17. The agent allocator overlaps the VM allocator and the credential rule

**Status: Changed twice. Agents are now a kernel composite, with no agent
allocator.**

An agent is a kernel composite, `create_agent`
([4.1](#41-agents-a-kernel-composite)), built from kernel pieces and the
bootstrap set's contracts. There is no agent allocator. Any VM it
creates is charged to an ordinary VM allocator or grant, and its model
access and budget are a `launch` grant on a runner (1884). An agent
allocator would be the only allocator that charges another, and the
composite avoids that. "30 agents" is a label query, not a quota. A
warm pool of agent-base VMs, a component built on `adopt`, covers fast
start-up. Budgets need metering, by `/sys/model` outside the runner or
by the runner's own reports
([2.2.23](#2223-model-access-and-budgets),
[7.1](#pkggitproxy-and-pkgmodelproxy)).

An agent is a primitive call, not a recipe of other calls, for two
reasons:

- **Bootstrap needs a first actor.** Something has to exist before any
  agent can build anything. A recipe is a sequence of calls somebody
  makes, and it cannot make itself. A human in the UI needs a single
  call that produces a working agent they can talk to.
- **A harness needs an identity it can't be handed.** A session on a
  runner outside the agent's namespace acts as the agent through a
  session binding, which only the kernel issues, and only for a
  `launch` it accepted ([Identity](#identity)). That can't be put
  together from a VM create and a grant.

The composite relies beyond the kernel only on the `runner/v1`
contract, served by `/sys/runner` from the bootstrap set or by any
other runner the caller holds `launch` on. If the runner refuses the
launch or fails its contract, `create_agent` fails with a clear error
and creates nothing ([Runners](#runners)).

The recipe this finding was first settled with is in
[Appendix A](#a7-an-agent-as-a-recipe).

### F18. "Inside the caller's namespace" needs to mean the subtree

**Status: Resolved by grants.**

A `create`'s target must be in the caller's subtree, or inside the
grant's `into`. The charge goes to the allocator or grant the call
names. `rel` publishes the preview's endpoint in `preview-17` by
granting it to `/sys/ingress`, and the grant dies with the preview
(4.2.9).

### F19. Cost attribution for a unit of work spans subtrees

**Status: Resolved by grants.**

Each charge is recorded along its grant chain, so a factory that wants
change 17's CI on `w17`'s bill grants part of `w17`'s VM quota to `ci`,
and `cost(w17)` includes it (4.2.6). Across chains, cost is grouped by
label: whoever creates the work labels it (`work: 17`), as the CI
controller labels each `c17-*` namespace, and `cost(group_by=label:work)`
adds it up. A grant can no longer force labels onto what is created
through it (1887,
[A.27](#a27-allocator-limits-reserve-kinds-classes-labels-and-percentages)).
Shared work such as batches is split by convention.

### F20. Application events are outside `watch`

**Status: Unchanged (minor).**

A git push, a review being posted, a CI request: the events that drive
a factory are application-level, and `watch` covers Shibu objects only.
Each component that needs one gets a granted endpoint to receive it
(`ci/hooks`, 4.2.6). Either accept this, since webhooks between services
are normal and a grant is cheap, or let a namespace publish custom
events onto its own `watch` stream. The second widens Shibu's scope.

### F21. Visibility leaks are a matter of policy and audit

**Status: Decided for v1 (1904).** Audit and the exposure query
([2.2.7](#227-credentials-only-behind-proxy-endpoints)) are the answer
in v1, and nothing else is added: no extension of the exposure query
to every endpoint, and no kernel mechanism beyond `regrant=false` as
the default. Fencing a subtree waits on policy beyond v1
([2.7](#27-beyond-v1-policy)).

*As proposed:*

*The risk.* Siblings are not isolated by structure
([The tree](#the-tree)). Anyone holding an object can grant it to
anyone, if it owns the object or received it with `regrant`, and in v1
nothing else stands in the way. A careless worker that holds a read
grant with `regrant` can pass it to a namespace nobody intended, and any
namespace can grant its own objects outward.

*Suggested mitigations:*

- `regrant=false` is the default (decided in 1886, [Grants](#grants)),
  so a received grant can't be passed on unless its granter chose to
  allow it. Make the UI show every grant that has `regrant`.
- Once policy exists, fence each top-level namespace with a deny
  predicate against grants out of it, as the factory would
  ([2.7](#27-beyond-v1-policy)).
- The exposure query
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)) answers "who
  can reach X" for VMs, runners and secrets. Extend it to every
  endpoint, the transitive closure over grants and subtree visibility,
  so a leak can be found rather than only prevented.

### F22. Cascading revokes can be expensive

**Status: Decided for v1 (1904).** The kernel keeps a **grant-chain
index**, grants indexed by the grant they were issued from, and a
cascading revoke walks that index rather than scanning grants. This is
an implementation requirement on the kernel and its state store
([2.2.27](#2227-kernel-state-store)), not an API change. The other two
suggestions below, asynchronous cleanup behind a revoked-link set and a
blast radius in `dry_run`, are not part of the decision.

*As proposed:*

Revoking a grant revokes everything issued from it, and a namespace's
deletion revokes everything it received. Revoking `factory`'s grant on
`/vm-spot` walks every CI run, batch and sandbox in the tree. With
`kill=true` it also deletes their VMs. That is the right meaning, but
it can be a large, slow operation, and it happens on a common path
(every delete).

*Suggested handling:*

- Make revocation logically immediate (the web proxy and allocators
  stop honouring the revoked chain at once, by checking a revoked-link
  set) and the cleanup asynchronous.
- Report a revoke's blast radius in `dry_run` before it happens.
- Index grants by the link they were issued from, so the cascade is a
  walk of an index rather than a scan.

### F23. Grant sprawl in long-lived namespaces

**Status: Decided for v1 (1904).** The kernel records when each grant
was last used. `list` can filter grants by last use, and `cost` and
`audit` show the grants under a path that went unused in the period
they cover ([2.2.8](#228-orchestration-surface)). There is no automatic
expiry beyond leases: a grant without a lease lives until it is revoked
or becomes invalid.

*As proposed:*

Per-change grants (results to `lead`, `stop` on each worker to `mq`,
read on each checkout to each reviewer) accumulate in long-lived
namespaces. Most die with their target or grantee, but a grant from a
long-lived object to a long-lived namespace lives for ever unless
someone revokes it.

*Suggested handling:*

- Recommend a lease on every per-change grant, as 4.2.7 does, and,
  beyond v1, let a policy require one ([2.7](#27-beyond-v1-policy)).
- Show grants per namespace in the UI with their age and last use, so
  an unused grant is visible.

### F24. Does `regrant` need a depth limit?

**Status: Decided for v1 (1904): no depth limit.** `regrant=false` as
the default ([Grants](#grants)) covers it: every link past the first
is one a granter chose to allow. `regrant_depth` and a deployment-wide
maximum are not adopted.

*As proposed:*

A chain of regrants can be arbitrarily long, and each link is something
the original granter did not choose. `lead` → `w17` → anything `w17`
regrants to is out of the planner's sight unless it lists grants.
Narrowing-only keeps the authority bounded, but not the number of
holders.

*Options:*

- No limit; rely on `regrant=false` by default and, beyond v1, on deny
  predicates ([2.7](#27-beyond-v1-policy)).
- `limits={regrant_depth: n}`, decremented at each regrant. It is a
  narrowing, so it fits the model.
- A deployment-wide maximum chain length, which also bounds the cost of
  the walk in the implementation note.

The lean was the second, with no default limit. 1904 chose the first.

### F25. The backend carries the whole identity model

**Status: Kernel (1765).** The guarantee is part of the backend driver
contract, and its conformance suite tests it
([Backend drivers](#backend-drivers),
[F35](#f35-a-driver-must-prove-an-unspoofable-origin-before-it-is-admitted)).
Preferring vsock is still only a suggestion.

Ambient identity ([Identity](#identity)) is only as strong as the
backend's guarantee that a VM cannot send packets that appear to come
from another VM. If that fails, one VM acts as another, and every grant
the other holds is exposed. The obligation falls on every backend
(granule, k8s), not on Shibu's own code, and the backends were not built
with it in mind ([7.1](#pkggranule-and-pkgk8s)).

*Suggested handling:*

- State the guarantee as a backend conformance requirement: one VM per
  interface, no shared L2 between VMs, source-address checks on every
  interface, and the web proxy reachable only through those interfaces.
- Write a conformance test that tries to spoof another VM's origin, and
  run it against every backend before it is allowed to host VMs.
- Prefer vsock where the backend offers it, because its addressing is
  assigned by the host and not by the guest.

### F26. The shared runner is a high-value target

**Status: Decided for v1 (1904), by
[6.12](#612-session-isolation-inside-pool-runners).** Restated as each
owner's decision in 1884. `runner/v1` requires container-level
isolation or better between sessions; a runner reports the isolation it
provides (`container`, `microvm` or `vm`) in `capacity`; a `launch`
grant may require a minimum (`limits={isolation: microvm}`); and the
exposure query counts a runner's other sessions as able to reach a
session only when the runner's isolation is below what the granter
required. The rest of the suggested handling below stays advice to
runner operators.

Since 1884 this is a question each owner answers for itself: a secret's
owner decides whether to grant `read` to a pool runner, and the
exposure query shows who else can reach it
([2.2.7](#227-credentials-only-behind-proxy-endpoints)). The core
doesn't rely on any runner to protect the core ([Trust](#trust)), so a
broken runner exposes what its owners chose to put there, and nothing
the kernel enforces. The risk below is still real for `/sys/runner`,
and for any runner shared across owners.

A shared runner pool holds the subscription and OAuth tokens of every
agent running on it at once. That makes it the most valuable
thing in Shibu to break into, and it runs third-party CLIs that take
untrusted input: the job, the messages, and every tool result the
sandbox sends back. A CLI bug that turns a crafted tool result into code
execution in the harness would put an attacker next to other agents'
tokens.

Decided in [4.1](#placements):

- each session is its own container or better (1904; an OS user is no
  longer enough), holding only its own agent's credential;
- the runner never executes tools, and runs only the adapters in its
  image;
- its egress goes only to the vendors and, through the web proxy, to
  agents' VM `mcp` endpoints.

The runner is a distribution component, `/sys/runner`, that root can
replace. Each of the points above is part of the `runner`
contract, and a replacement has to pass the contract's conformance
suite before the endpoint is re-pointed to it
([2.3](#23-the-standard-distribution-and-the-sys-spec)).

*Suggested handling, beyond that:*

- Make the per-session boundary a container or microVM rather than an
  OS user, so a CLI compromise has to escape a kernel boundary to reach
  another session's token. *Adopted in 1904 as the contract's floor
  (container), with microVM or VM available to a granter that requires
  it.*
- Deliver each credential into the session's own memory or tmpfs at
  launch, and never write it to shared disk or a shared environment.
- Scope each per-session binding to exactly one agent namespace and
  one session. The runner can act only as the agents it currently
  hosts, and each session only as its own, so a compromised session
  can't act as another agent through the gateway.
- Offer a dedicated harness VM per agent (already allowed in 4.1) as
  the default for credentials an operator marks high-value, and the
  shared pool for the rest.
- Audit every credential delivery to a session, as the secret store
  audits every read ([2.2.24](#2224-secret-store)).

### F27. Adapter trust

**Status: Adapters ship in a runner's image and are installed by the
runner's operator (1884). Residual risk recorded.**

Since 1884, any VM may run a runner, so "only root installs adapters"
holds for `/sys/runner`, not for every runner. Whoever operates a
runner chooses its adapters, and whoever grants `launch` on it, or
`read` on a login to it, is trusting that choice. The text below is
about `/sys/runner`.

An adapter manifest says how to launch a CLI, how to deliver a
credential to it, and where its egress may go. An adapter in a harness
VM runs next to a real credential. A malicious or careless adapter could
deliver the credential somewhere it shouldn't, leave a CLI's built-in
tools switched on (so it executes tools in the harness VM, breaking the
rule in [2.2.7](#227-credentials-only-behind-proxy-endpoints)), or widen
egress.

So only root installs adapters. They are part of `/sys/runner`, and
changing them is a change to the `/sys` spec
([2.3](#23-the-standard-distribution-and-the-sys-spec)). The standard
distribution ships Claude Code, Codex and Gemini/Antigravity. A
namespace cannot install one, and an agent cannot either, however much
quota it holds. The UI's credential forms come from the
manifest's declarations, never from the harness
([4.1](#harness-interface)).

Still open within this:

- The CLIs change often. A CLI update can add a built-in tool or change
  a flag, and an adapter that pinned the old behaviour may stop
  disabling tools. Adapters should pin the CLI version, and the runner
  should check, at launch, that the harness offers no tools but the MCP
  server (grain's `--tools ''` plus `--strict-mcp-config` is the
  model). The `runner` contract's conformance suite should test it.
- A same-vm runner in a namespace's own VM, with no credential, needs
  much less trust in its adapters. Its operator is that namespace,
  which installs what it likes in its own image.

### F28. CLI vendors' terms for subscriptions in automated runners

**Status: Risk. No conclusion.**

`split` placement exists so that a person's Claude, ChatGPT or Gemini
subscription can drive agents. Vendors' consumer terms may restrict
automated or unattended use, sharing an account between agents, or use
from hosted infrastructure, and they may change. A shared runner that
runs many sessions from one subscription, or a subscription used by a
factory of agents, is where this would bite. grain already runs
`claude -p` under an OAuth token this way.

This document records the risk and draws no conclusion. It does not
change the design: an operator can turn `oauth` credentials off, and an
API key in `same-vm` works for every adapter. Rate limits on shared
subscriptions are in [6.10](#610-per-vendor-rate-limits-on-shared-subscriptions).

### F29. An agent's tools run with the agent's authority

**Status: Resolved (1884): `exec` lends the VM's identity, and an
agent's `tools` can be anywhere.**

1741's identity rule is that a VM is the trust boundary, so untrusted
code runs in a different VM in a different namespace
([Identity](#identity)). Before 1884, `create_agent` always put the
agent's sandbox VM, where every tool call ran, in the agent's own
namespace, so code its tools ran (a build script, a test suite, a
dependency's install hook) could do anything the agent could.

*Now:* `exec` on a VM's `mcp` runs the command with that VM's
identity, the identity of the VM's home namespace, whoever calls it
([VM `mcp`](#vm-mcp-and-the-agent-control-mcp)). So where an agent's
code runs is where its `tools` are:

- `create_agent`'s default VM is in the agent's namespace, and code run
  there has the agent's namespace's grants. It can't spend the agent's
  model budget, which is the runner's `launch` grant, not something the
  VM holds, and it can't steal a credential that isn't there.
- An agent whose tools are a VM in another namespace, such as the
  factory's shared build VMs
  ([4.2.5](#425-worker-agents-and-their-sandboxes)), runs code with
  that namespace's authority, and only that.
- Granting `exec` on a VM to another namespace lends that VM's
  identity. It is the strongest grant op, and the exposure query lists
  every holder ([2.2.7](#227-credentials-only-behind-proxy-endpoints)).

The earlier options, a `<path>/sbx` sub-namespace per agent, are
covered by naming a VM in it in `tools`.

### F30. Component UI panels

**Status: Decided (1771), and extended to agent-served panes. Settled
in [2.6.6](#266-agent-served-panes).**

Additional components such as grain's want views of their own (a
backlog, a merge queue, CI runs), and the generic views (namespaces,
grants, `messages`) can't render them.

The decision (1771):

- The UI isn't the kernel's. It is a replaceable distribution
  component, `/sys/ui`, held to CLI parity
  ([2.6.1](#261-the-ui-is-a-replaceable-component)).
- **Any agent or component, in any namespace, can serve a pane**,
  declared at `<ns>/ui/panes`. Seeing one is a grant on its endpoint
  ([2.6.6](#266-agent-served-panes)).
- Every pane is required to have a separate origin per namespace, with
  no access to the UI's session or another pane; a frame the UI draws,
  naming who is serving the pane; calls made as the viewer, through the
  web proxy; and no credential entry in a pane, only in the UI's own
  manifest-driven forms ([4.1](#harness-interface)).
- A pane reaches the UI only through a narrow `postMessage` bridge:
  resize, navigate, open the agent's `messages`, and post a typed
  message as the viewer once the viewer confirms it.
- **Operator-installed components** can be trusted by root to place
  panes in the UI's main navigation. Untrusted panes appear only under
  their own agent.
- Harness and adapter UI for keys remains declarative-only. An adapter
  declares forms in its manifest and the UI renders them. A pane the
  agent serves is untrusted content like any other, and is never where
  a key is entered.

grain's UI views become panes of grain's components, trusted for the
main navigation ([7.2](#72-the-decision-grain-runs-on-shibu)).

### F31. Root can still install a malicious credential-holding image

**Status: Risk, with mitigations (1762; restated 1884).**

Since 1884 the kernel enforces no placement rule for keys: a secret's
owner decides who may `read` it, and the exposure query shows who else
can reach where it is delivered
([2.2.7](#227-credentials-only-behind-proxy-endpoints)). The kernel
can't check what a credential-holding image itself runs. Root can still name a malicious or careless image in the `/sys`
spec, or a credential-holding namespace's creator can boot one, and
that image holds the key. A `/sys/runner` replacement whose CLI turned
a crafted tool result into code execution is the same risk by another
route ([F26](#f26-the-shared-runner-is-a-high-value-target)).

*Mitigations:*

- **Audit.** Every `read` is an audit line naming the secret, its
  version, the reader and the grant ([2.2.24](#2224-secret-store)), and
  every `/sys` spec change is in the spec's history
  ([2.3](#23-the-standard-distribution-and-the-sys-spec)). A key read
  by a component that has no business with it shows up.
- **Conformance.** A `/sys` replacement has to pass its contract's
  conformance suite before its endpoint is re-pointed
  ([Staged swap](#staged-swap)). The suite can check that the runner
  disables a CLI's built-in tools and that a component's traffic stays
  inside its declared egress. It can't prove the image benign.
- **No passing on.** A `read` grant can't carry `subtree=true`, and
  without `regrant=true`, the default, its holder can't pass it on
  ([Grants](#grants)), so a key reaches only the namespaces its owner
  named.
- **Policy, beyond v1.** An ancestor confining where `read` grants may
  go in its subtree, so nothing there becomes credential-holding without
  its say, is a deny predicate ([2.7](#27-beyond-v1-policy)). v1 has no
  such control; it was a boundary, dropped with boundaries
  ([A.22](#a22-boundaries)).
- **Narrow egress.** The `/sys` spec fixes each component's egress
  ([2.3](#23-the-standard-distribution-and-the-sys-spec)), so a bad
  `/sys` image can send a key only where it was already allowed to
  talk. Outside `/sys`, narrow egress is the namespace owner's choice.
- **Exposure.** A secret's owner can ask, at any time, who could reach
  every place its secret is delivered, and revoke.

### F32. `when-idle` races with a create in flight

**Status: Moot (1885); decided for v1 (1904).** The kernel no longer
has `when-idle`: a namespace is deleted only by `delete` or its lease
([2.2.6](#226-leases-and-ttls)), so the kernel has no idle check to
race. The same race now belongs to whoever deletes on idleness, the
creator or the reaper component. Decided: **the reaper must re-check
idleness at the moment it deletes**, not act on an idleness it saw
earlier, so a create that landed in between keeps the namespace. The
rest of the handling below is advice to the reaper and to creators:
count in-flight and queued creates, and allow a settle time. The policy
itself is in
[A.20](#a20-lifecycle-policies-delete_policy-when-idle-and-armed).

*As proposed in 1763:*

`when-idle` deletes a namespace once every VM and agent in it has
exited ([2.2.6](#226-leases-and-ttls)). Idleness is a moment, and a
create can be on its way at that moment:

- an agent in a CI run's namespace is about to create a VM, but its
  last VM exits first;
- the parent creates the namespace and then its first VM, a second
  call;
- a create is waiting in the admission queue
  ([2.2.21](#2221-admission-queue)) when the last running VM exits;
- an agent calls `exit` just after starting a create that has not
  returned.

If the kernel takes the namespace to be idle in any of these, it enters
`deleting`, and the create fails or, worse, succeeds into a namespace
that is about to go.

*Suggested handling:*

- **Arm on first use.** A namespace is not idle until it has held at
  least one VM or agent. 2.2.6 already says this, so creating the
  namespace and then its VMs is safe.
- **In-flight creates count.** A create accepted for the namespace,
  including one waiting in the admission queue, keeps it from being
  idle until it is admitted or abandoned. 2.2.6 already counts the
  queue.
- **One order.** The idle check and a create into the namespace take
  the namespace's lock, so a create either lands before the namespace
  is idle or fails with "namespace is deleting". It never lands after.
- **An optional settle time,** `delete={when-idle, after: 30s}`, for
  workloads that hand off between VMs and briefly have none running.
  Not adopted; the alternative is to keep a supervisor running.

A VM with restart policy `always` never exits for good, so a
`when-idle` namespace that holds one is never idle. That is
deliberate, and a lease still bounds it.

### F33. A component allocator can misreport usage

**Status: Open (reopened in 1886).** The risk is bounded by design,
but the mitigation that let an ancestor keep a subtree off outside
allocators went with boundaries. It is carried over as a requirement on
policy beyond v1 ([2.7](#27-beyond-v1-policy)).

A component allocator does its own metering, and its `usage` reports
are whatever it says ([Component allocators](#component-allocators)).
A buggy or dishonest one can under-report, so its grantees look
cheaper than they are, or over-report, so they look dearer.

What bounds it:

- **Reports never set limits.** Caps and headroom on its grants are
  enforced from what the kernel counts itself: creates made through
  each grant, and the amounts declared in each create. A lie can change
  only the **reported** figures in `cost()`, which are marked as such.
- **It can't touch kernel quota.** Whatever it uses to fulfil a create
  is charged to its own kernel grants, which the kernel counts. A pool
  that under-reports still pays, in kernel units, for every VM it runs.
- **Trusting it is the grantee's choice**, made when it accepts or uses
  the grant. In v1 that choice is each grantee's alone: no ancestor can
  forbid a subtree to use allocators implemented outside it. The
  `allocators_from` boundary key did that and is gone
  ([A.22](#a22-boundaries)); limiting which component allocators a
  subtree uses is a requirement on policy
  ([2.7](#27-beyond-v1-policy)).

What it leaves: a grantee that pays for a component's service in
another currency (money, credits) on the strength of its reports has
only the component's word. Usage that the kernel can't see, such as
GPU-seconds inside a VM, can't be checked against anything but the
declared amounts.

### F34. Cleanup leaks when a component allocator is down

**Status: Kernel (1765). Leaks are visible by design. A way to write
one off is proposed.**

The kernel deletes a component-allocated resource by calling the
component's `delete` ([Component allocators](#component-allocators)).
If the component is down, deleted, or broken, that call fails, and the
resource may live on: a cloud account still billing, a GPU slice still
held.

Decided:

- The kernel keeps the handle and retries.
- A handle whose `delete` keeps failing is a **leak**: charged to the
  grantor, shown in `watch` and in the UI, and counted against the
  grantor's headroom on that allocator.
- The kernel's record of handles is the source of truth for what should
  exist, so a component that comes back can reconcile against it with
  `list`.

*Suggested handling:*

- Let the allocator's owner, or root for a component that is gone for
  good, **write off** a leak: drop the handle with an audit line saying
  the resource may still exist outside Shibu.
- If a component allocator's namespace is itself deleted while it holds
  live handles, keep its endpoint in `deleting` until they are deleted
  or written off.
- Show leaks per component allocator, so a grantee can see how
  reliable an allocator has been before it trusts it.

### F35. A driver must prove an unspoofable origin before it is admitted

**Status: Kernel (1765).**

Ambient identity rests on every driver keeping a VM's origin
unspoofable ([Identity](#identity),
[F25](#f25-the-backend-carries-the-whole-identity-model)). With several
drivers at once, the weakest one decides how strong identity is for
every VM, because a spoofed origin can claim any namespace's VM, not
only the ones on that driver.

Decided:

- The driver contract's conformance suite includes an origin test: a VM
  tries to send traffic as another VM, on the same driver and across
  drivers, and to reach the web proxy other than through its own
  interface. A driver that fails is **not admitted**: the `/sys` spec
  that names it does not apply, and no placement class maps to it
  ([Backend drivers](#backend-drivers)).
- The suite runs again on every staged swap, so a driver upgrade that
  breaks the guarantee is refused like a new driver.

What it leaves: a test shows that the spoofs it tried failed, not that
none can succeed. A driver can also break the guarantee at run time,
through a misconfigured host or a reused address (a k8s pod IP,
[7.1](#pkggranule-and-pkgk8s)). *Suggested:* run the origin test
periodically against live drivers, not only at admission, and treat a
failure as an incident that stops new creates on that driver.

---

## 6. Open questions

These were raised in the design discussion and deliberately not
answered at the time. Where the walk-throughs produced evidence for one,
the finding is cross-referenced. 6.1–6.7 have since been answered or
closed, and 6.8–6.13 were decided for v1 in the 1904 revision. All are
kept so that references to them resolve; each decided question keeps
the question as it was asked under its decision. No question in this
section is open now. The open items left in the design are marked
**Open** where they live
([2.2.27](#2227-kernel-state-store), [F33](#f33-a-component-allocator-can-misreport-usage)).

Which layer each question concerns:

| Layer | Questions |
| --- | --- |
| Kernel | 6.1–6.6, 6.8, 6.9 (the front door's assertion), 6.11, 6.12 (the `isolation` limit and the exposure query), 6.13 (the meter) |
| Standard distribution | 6.7 (`/sys/git`, `/sys/forge`), 6.9 (`/sys/ingress`), 6.10 (`/sys/runner`), 6.12 (`runner/v1`) |

### 6.1 Volumes as endpoints

**Answered: yes.** Volumes are endpoints, and are granted like any other
endpoint, with their own narrowing vocabulary: read vs read-write, and
sub-path ([Grants](#grants), [F3](#f3-aliases-carry-no-mode)).

### 6.2 Aliasing a control endpoint

**Closed.** A control endpoint can be granted, narrowed by operation
(`ops=[stop]`, `ops=[list]`, …). See
[F16](#f16-control-endpoint-aliasing-came-up-three-times) and 4.2.8.

### 6.3 Sharing between peers with no willing common ancestor

**Closed.** No common ancestor is needed. A peer that holds something
grants it. A peer that wants something calls an endpoint the holder
exposes, and the holder grants
([F14](#f14-inbox-requests-need-structure)).

### 6.4 Delete-on-exit for non-outputs

**Closed: superseded (1763).** Namespaces don't exit, and there are no
declared outputs. A namespace is deleted by `delete` or its lease
(1885), and deleting everything in it is the rule, not a "for now"
one. What should outlive it goes in a deposit area, or is moved out by
the parent before it deletes
([2.2.4](#224-lifecycle-and-keeping-results)).

### 6.5 Trust in third-party allocators

**Answered (1765):** see [Component allocators](#component-allocators).

### 6.6 Backends

**Answered (1765):** see [Backend drivers](#backend-drivers).

### 6.7 Repos: GitHub or Shibu-hosted

**Answered (1765):** see [`/sys/git` and `/sys/forge`](#sysgit-and-sysforge).

### 6.8 How a service declares its operations

**Decided for v1 (1904).** v1 grants on a service use paths and
methods only (with `read`, `write` and `tunnel`, which stand for
methods). Service-level names, such as `fetch` on git or `call` on
`/sys/model`, are documentation, not a vocabulary the kernel or the
web proxy knows. A contract may say which paths and methods a name
stands for, and a contract op the service enforces itself from the
caller assertion (`exec`, `launch`) is carried by the kernel as an
opaque string it narrows as a set; neither gives the kernel or the
proxy a meaning for it ([Endpoint](#endpoint)). Revisit this together
with the policy language beyond v1 ([2.7](#27-beyond-v1-policy)).

*As asked:* a grant on a VM port is narrowed by paths, methods and read vs write.
That is enough for the web proxy to enforce, but `ops=[fetch]` on git or
`ops=[call]` on `/sys/model` are service-level names. Whether a
service declares a vocabulary of named operations that map to paths, or
grants are always written in paths and methods with names only as
documentation, was open until 1904.

### 6.9 Callers with no VM

**Decided for v1 (1904).**

- **Front-door callers.** A front-door caller's assertion names both
  the namespace it acts as and the person: the identity provider and
  the subject it authenticated
  ([2.2.22](#2222-caller-assertion)).
- **Anonymous callers.** `/sys/ingress` handles the anonymous callers
  of a public endpoint entirely
  ([2.2.12](#2212-pre-approved-publish)). They reach the backend with
  an assertion for `/sys/ingress`'s call: it names `/sys/ingress` and
  marks the caller as anonymous.

*As asked:* the UI and other external clients authenticate at the
front door and act as a namespace ([Identity](#identity)). They mint
no tokens: v1 has none ([2.2.11](#2211-no-attenuated-tokens)). Open
until 1904: whether a caller assertion for them names the person as
well as the namespace, and what assertion, if any, the anonymous
callers of an endpoint published through `/sys/ingress` get.

### 6.10 Per-vendor rate limits on shared subscriptions

**Decided for v1 (1904): no scheduler in v1.**

- **A vendor limit is a pause.** When a vendor's limit is hit, the
  runner reports the session as paused: status `waiting`, reason
  `vendor-limit`, with the reset time when the vendor gives one. The
  agent stays `running`, and the runner retries after the reset
  ([Runners](#runners)).
- **Bounded by the lease.** If the limit lasts beyond the session's
  lease, the session exits, and the agent with it, with reason
  `out-of-quota` ([2.2.17](#2217-exit-status-and-reason)).
- **Sharing is runner policy.** How many agents may share a
  subscription is the runner's own policy, not a kernel feature.

*As asked:* a subscription carries the vendor's own rate limits and usage caps,
per account, and Shibu can't see them in advance. Several `split`
agents on one subscription, in one shared runner or across several,
share those limits. One busy agent can starve the others, and a
vendor's limit shows up as a failed model call rather than as a
Shibu budget.

Open, until 1904:

- whether the runner should schedule sessions per subscription, and
  queue them like the admission queue
  ([2.2.21](#2221-admission-queue));
- whether a vendor's limit should surface as an exit reason
  (`out-of-quota`), or as a pause the agent waits out;
- whether a turn or hour budget per agent is enough, or a subscription
  needs a cap on how many agents may use it at once.

Vendors' terms on such use are a separate risk
([F28](#f28-cli-vendors-terms-for-subscriptions-in-automated-runners)).

### 6.11 Where an agent's `messages` history lives

**Decided for v1 (1904)** ([2.2.25](#2225-messages)):

- **Where.** With the agent object, in the agent's namespace, stored
  by the kernel in its state store
  ([2.2.27](#2227-kernel-state-store)).
- **How long.** Exactly as long as that namespace.
- **Cap.** Capped per agent at a stated size, dropping the oldest
  messages first.
- **Who reads it.** Anyone holding `read` on `messages` can read all of
  what is kept, including a grantee added later.
- **What should outlive the agent** goes to a deposit area
  ([2.2.4](#224-lifecycle-and-keeping-results)).

*As asked (partly answered in 1763):* `messages` and the transcript
survive the agent's exit, and stay readable for as long as the agent's
namespace exists. Open until 1904: where the history is stored, whether
it is capped while the namespace lives, and whether a grantee added
later can read what was said before the grant.

### 6.12 Session isolation inside pool runners

**Decided for v1 (1904).** This also decides
[F26](#f26-the-shared-runner-is-a-high-value-target).

- **A floor.** `runner/v1` requires container-level isolation or better
  between sessions. An OS user alone is not enough.
- **Reported.** A runner reports the isolation it provides in
  `capacity`: `container`, `microvm` or `vm`.
- **Required by the granter.** A `launch` grant may require a minimum,
  `limits={isolation: microvm}`. Like every limit it narrows: a
  re-grant may require more, never less. A runner refuses a launch it
  can't isolate to that level.
- **Exposure.** The exposure query
  ([2.2.7](#227-credentials-only-behind-proxy-endpoints)) counts the
  other sessions on the same runner VM as able to reach a session only
  when the runner's isolation is below what the granter required.

*As asked:* a pool runner serves many agents from many namespaces, one session each
([Runners](#runners)). The kernel keeps their identities apart: each
session's binding names one agent and dies with the session. What keeps
one session from reading another's login, transcript or files on the
same runner VM is the runner's own isolation, an OS user or a
container, which the kernel can't see.

Open, until 1904:

- whether `runner/v1` should require a minimum isolation (OS user,
  container, microVM) and say which one a runner provides in
  `capacity`, so that a granter can ask for one in a `launch` grant
  (`limits={isolation: microvm}`);
- whether the conformance suite's cross-session checks are enough to
  rely on, or a high-value login should always get a runner of its own
  ([F26](#f26-the-shared-runner-is-a-high-value-target));
- whether the exposure query should count every other session on the
  same runner VM as able to reach a session's login.

### 6.13 Accepting a runner's usage reports

**Decided for v1 (1904)** ([2.2.23](#2223-model-access-and-budgets)):

- **Accepting.** `limits={usage: reported}` on a `launch` grant accepts
  that runner's reports as charges, against the launching agent's
  payer along the grant's chain.
- **Not accepting.** Without it, the runner's reports appear in `cost`
  marked **reported**, as a component allocator's are
  ([Component allocators](#component-allocators)), and are not charged.
- **Silence.** A session whose runner stops reporting is stopped at the
  end of its lease.

*As asked:* a runner reports each session's usage, charged to the launching
agent's payer ([2.2.23](#2223-model-access-and-budgets)). Whether to
accept a runner's reports is the payer's and the granter's choice. A
report from `/sys/model`, metered outside the runner, needs no such
choice.

Open, until 1904:

- how a payer marks a runner's reports as accepted: a flag on the
  `launch` grant (`limits={usage: reported}`), a label on the runner,
  or a list kept by the payer;
- how reports from a runner that isn't accepted appear in `cost`:
  marked **reported**, as a component allocator's are
  ([Component allocators](#component-allocators)), or not charged at
  all;
- what happens to a session whose runner stops reporting.

---
## 7. Relationship to grain

*Decided (1750): grain is refactored to run on top of Shibu.* This
section maps each overlapping piece of grain onto Shibu (7.1), records
the decision (7.2), and keeps the three options that were weighed
before it as background (7.3). The package-by-package map and the
migration plan are in [`shibu-migration.md`](shibu-migration.md).

### 7.1 Piece by piece

#### `pkg/capability` and `pkg/access`

**`pkg/capability`** is grain's nearest thing to allocators. Each
subpackage is a `model.CapabilityProvider` with a `CapabilitySpec`, and
its `Provision` field says what kind of thing it is (`pkg/model/task.go`):

- **MINT** (`gcp-key`, `gemini-key`, `modelproxy`) mints a short-lived
  credential and records a `Lease` that is revoked when the run ends or
  `MaxLease` passes.
- **PLACE** (`repo-read:*`, `registry-push`, `package-publish`) places
  operator-owned material, or opens a scope on a proxy.
- **SELECT** names which credential to use.
- **GRANT** (`console`, `memory`, `self-debug`) needs no credential: it
  is a switch, often one that turns on extra MCP tools.

How these map:

| grain | Shibu |
| --- | --- |
| a capability that opens a proxy scope (`repo-read:*`, `modelproxy`) | a grant on a proxy endpoint, narrowed to the scope ([2.2.7](#227-credentials-only-behind-proxy-endpoints)) |
| a MINT capability that writes a key into the sandbox (`gcp-key`, `gemini-key`) | not allowed as it is: the key would have to move behind a proxy endpoint |
| `Lease` and `MaxLease` | a lease on the grant or endpoint ([2.2.6](#226-leases-and-ttls)) |
| a grant by label, approved by a human | a grant from whoever holds the thing, asked for by calling an endpoint they expose; approval flows are an additional component ([F14](#f14-inbox-requests-need-structure)) |
| `CIOnly` (publishing only from trusted post-merge CI) | which namespaces hold a grant on the publishing proxy |
| GRANT switches (`console`, `memory`, `self-debug`) | no equivalent. They are grain features, not infrastructure, and stay in grain, as grants on endpoints of grain's own components |
| `Folder.offers` in `docs/data-model.md` (capabilities a subtree inherits) | a grant with `subtree=true` |

The capabilities that place a raw key into the sandbox are the ones
Shibu's rule forbids. Moving them behind proxies is work grain would
have to do whatever it decides about Shibu.

**`pkg/access`** is the list of identities allowed into grain's UI and
API: users, service accounts and groups, normalised to IAM member
strings (`AccessAccount`, `Store`, with hooks that fire on every
change). In Shibu terms it decides who may act as the root principal or
as some namespace through the UI ([2.2.18](#2218-human-as-a-principal)).
Shibu needs the same thing, plus a mapping from each identity to the
namespace it acts as: its own `/users/<name>`, and `/` for root
operators. grain once had teams and roles for that, a team being roughly
a namespace directly under the deployment; the single-tenant reset
(`docs/single-tenant-reset.md`) removed them, so a grain now has one
actor, its owner, and the hub holds the rest.

#### `pkg/granule` and `pkg/k8s`

Both are thin wrappers over a CLI: `granule` for Docker-hosted VMs and
`kubectl` for pods. They create, inspect and delete sandboxes, and do
nothing else. Admission is decided elsewhere, by `model.Ledger` (one
deployment-wide budget of live sandbox shapes, shared by the agent and
CI pools) and the per-team `MaxWorkers` / `MaxCIJobs` counts.

In Shibu, these are **kernel backend drivers behind the root
allocators** ([Backend drivers](#backend-drivers)). `pkg/granule`,
with `pkg/lifecycle`'s image assembly, `pkg/sandboxaddr` and
`pkg/sandboximage`, is v1's one driver, docker + granule. `pkg/k8s` is
the seed of the second. The
ledger and the team counts become the VM allocator's accounting and the
allocator grants under it. The ledger's own history is why the
admission queue is in the kernel
([2.2.21](#2221-admission-queue)): the ledger exists because two pools
with their own budgets wasted capacity, and it admits work by queueing.

They also take on the kernel invariant that a VM's network origin is
unspoofable ([Identity](#identity),
[F25](#f25-the-backend-carries-the-whole-identity-model)). Neither does
today, because neither needs to: grain authenticates sandboxes by the
tokens it hands them, not by where their traffic comes from.

- **`pkg/granule`** would have to give each VM its own interface (or
  vsock) with source-address checks, keep VMs off any bridge they share,
  and route every packet a VM sends to the web proxy.
- **`pkg/k8s`** would have to do the same for pods, run under Kata or
  gVisor-style isolation so that a pod is a VM boundary: a network policy or
  CNI that enforces one source address per pod, denies pod-to-pod
  traffic, and sends egress through the proxy. A pod's IP is reused
  after it is deleted, so the proxy's map from origin to namespace has
  to be updated before the address is handed out again.

#### `pkg/gitproxy` and `pkg/modelproxy`

These are what 2.2.7 cites as the model for credential proxies, and
they are already shaped like Shibu endpoints:

- **`pkg/gitproxy`** serves the smart-HTTP paths. It authenticates the
  caller by a per-sandbox token, checks the caller's live task scope
  against the repo (default deny), picks a credential from a ladder
  (the task's override, otherwise owner/repo), forwards, and writes an
  audit line. It also serves a local repository under a stable alias,
  and hands out registry logins for publishing.
- **`pkg/modelproxy`** checks a per-run token and swaps it for the
  deployment's vendor key. It deliberately does no per-model policy and
  no accounting.

In Shibu the git proxy becomes the GitHub-backed implementation of
`/sys/git` ([`/sys/git` and `/sys/forge`](#sysgit-and-sysforge)), and
the model proxy becomes `/sys/model`; both are standard-distribution
components
([2.3](#23-the-standard-distribution-and-the-sys-spec),
[2.2.23](#2223-model-access-and-budgets)). Two changes would be
needed:

1. The per-sandbox token goes away. A sandbox is identified by its
   network origin ([Identity](#identity)), and the proxy reads the
   signed caller assertion the web proxy stamps on each request
   ([2.2.22](#2222-caller-assertion)). The task-scope check becomes a
   policy on that assertion, which names the grant the request came
   through. Where a narrower scope is needed than the namespace holds,
   such as a helper that should only fetch, the helper runs in a
   sub-namespace granted only fetch; v1 has no attenuated tokens
   ([2.2.11](#2211-no-attenuated-tokens)).
   `/sys/git` receives its GitHub App key through a `read` grant listed
   in the `/sys` spec, which makes it credential-holding
   ([2.2.7](#227-credentials-only-behind-proxy-endpoints)).
2. **`pkg/modelproxy` has to start metering calls.** `create_agent`
   ([4.1](#41-agents-a-kernel-composite)) puts each API-key agent's
   budget on its grant on `/sys/model`, and the proxy is the only thing
   that sees what each call costs. This is required, not optional.
   It would also route by vendor and model name, and attach the
   agent's own API key from the secret store instead of one
   deployment-wide key ([2.2.24](#2224-secret-store)).

The registry-login path, which hands a real credential to `docker
push`, is the exception to 2.2.7 that grain already has. It exists
because image pushes are too large to buffer through the proxy. The
credential proxy's streaming registry proxy removes it
([F9](#f9-secrets-have-no-shibu-home)).

#### `pkg/iapreconciler`

This reconciles a list of desired members onto the IAP IAM policy of a
GCP backend service (`roles/iap.httpsResourceAccessor`), with retries on
concurrent edits. `cmd/grain/daemon_iap.go` runs it whenever an
`access` account changes. It is the piece that turns "this person may
use grain" into "this person can get through Google's front door".

In Shibu it belongs to **`/sys/ingress`**, the standard-distribution
component that serves every endpoint granted to it at a public URL and
decides who may reach it ([2.2.12](#2212-pre-approved-publish)): an
audience (IAP members, reconciled the same way) or an explicit
"anyone". On a GCP backend, the reconciler is close to that component
as it is.

#### Tasks, agents, sub-task proposals, approval, CI and the merge queue

| grain | Shibu |
| --- | --- |
| a task | an agent made by `create_agent` (4.1), with a lease (the run budget) and an exit status on its agent object |
| a run | the agent's harness session; a retry resumes it, or starts a new one in the same namespace |
| a run's sandbox | a VM in the agent's `tools`, serving `mcp`; grain's host-side runner is a pool runner |
| grain's host-side `claude -p --tools '' --mcp-config … --strict-mcp-config` with the OAuth token in its env | the `split` placement, with the host as a harness VM ([4.1](#placements)) |
| grain's agent frameworks (Claude, Codex, Gemini/Antigravity) | the adapters Shibu ships ([4.1](#harness-interface)) |
| a task's chat, `ask_question`, comments | the agent's `messages` endpoint ([2.2.25](#2225-messages)) |
| a deployment's agent credentials (Claude OAuth token, vendor API keys) | secrets in the kernel store, in `/users/<name>`, read by a runner or `/sys/model` through a `read` grant from their owner ([2.2.24](#2224-secret-store)) |
| the director | a long-lived agent in a namespace, like `factory/lead`'s planner (4.2.4) |
| a sub-task proposal | `create_agent` under quota the proposer holds, or a call to an endpoint of someone who holds more |
| "filed approved" / auto-merge inheritance | an approval policy in an inbox service, an additional component (F14) |
| a team's limits | allocator grants from the deployment's allocators |
| grain CI | a CI controller and per-job leased namespaces it deletes when their VMs exit, depositing results into the controller's namespace (4.2.6) |
| the merge queue | `mq` and its batch namespaces (4.2.8) |
| grain's state repo, transcripts, closing notes | application state. Shibu has no equivalent and shouldn't |

The biggest difference is approval. grain decides "may this agent start
that work" by asking a human, or by checking whether auto-merge was
inherited. Shibu decides it by quota: holding headroom on a VM grant and
budget on a model grant *is* the permission. A grain on Shibu would
replace most proposal approvals with grants, and keep human approval
for the requests an approvals service sends to a person.

Grain's knowledge of GitHub (pull requests, review comments, checks)
maps to the GitHub-backed implementation of `/sys/forge`, which grain's
merge-queue and CI components call
([`/sys/git` and `/sys/forge`](#sysgit-and-sysforge)). What does not
map: grain's policy on top of the forge (when to merge, what to batch),
its prompt building, its memory and its UI for tasks. Those are a
factory built *on* infrastructure, which is what 4.2 is. They are not
infrastructure.

### 7.2 The decision: grain runs on Shibu

*Decided by bsalmon and the director, task 1679, 2026-10-03.*

The end state is **Option A** (7.3): Shibu is the platform, and grain is
a set of additional components on it, in namespaces such as
`grain/lead`, `grain/ci` and `grain/mq`.

- grain's VM backends, web proxy and public edges, secrets, capability
  and ledger accounting, and the agent contract go into the **Shibu
  kernel**.
- grain's model proxy, harness runners and adapters, tool server, git
  proxy, forge and GitHub client, and package cache go into the
  **standard distribution**, as `/sys/model`, `/sys/runner`,
  `/sys/git`, `/sys/forge` and the package cache.
- The generic parts of grain's UI (agent chat, credential forms and the
  system views) seed the distribution's UI, `/sys/ui`
  ([2.6](#26-the-ui-and-the-sysui-conventions)).
- What is grain's own stays grain's, as **additional components**: the
  backlog, dispatcher and approvals, CI, the merge queue, repo rules,
  state export, grain's own MCP tools and grain's UI
  views (as panes trusted for the UI's main navigation,
  [2.6.6](#266-agent-served-panes)).

The migration does not jump to A. It is strangler-style, because grain
lands about 95 merges a day on itself and cannot stop for a rewrite. It
passes through **Option B's shape as a waypoint**: first the kernel and
distribution are carved out behind an in-process `pkg/shibu` API in
this repository, and only later split into their own process and their
own repository. The phases, their exit criteria and the invariants
that hold throughout are in [`shibu-migration.md`](shibu-migration.md).

What made the choice:

- A's process boundary is what makes the model's guarantees hold by
  construction rather than discipline: once grain is a component it is
  untrusted, and can't go around grants.
- The layering gives grain a smaller thing to depend on. grain needs
  the kernel and the bootstrap set; the optional distribution
  components can be adopted as they become ready.
- B's main advantage, the small first step, is kept by using B as the
  waypoint.

The costs A was charged with in 7.3 still apply, and the migration plan
names the ones that are hardest: the shared schema in `pkg/model`,
grain's own credentials, and `pkg/mcp`.

### 7.3 Background: the three options weighed

These are the three ways of fitting Shibu and grain together that were
weighed before the decision. 7.2 chose A as the end state and B's shape
as a waypoint. They are kept because the trade-offs still describe what
the migration has to manage.

#### Option A: a separate Shibu service, with grain as a client

Shibu is its own service with its own API, web proxy and UI. grain runs
as an application in a namespace (`grain/`), calls `create_agent` for
its tasks and Shibu's other operations for CI namespaces and sandboxes,
and stops driving `granule` or `kubectl` itself. grain's own harness
code gives way to Shibu's adapters and runners.

- **For:** the boundary is enforced by a process boundary, so grain
  cannot bypass the model. Other factories and agents can use Shibu
  without grain. Shibu can be secured, versioned and deployed on its own
  schedule. The audit and cost log covers grain and everything else
  in one place.
- **Against:** two systems to deploy and operate, with an API between
  them that has to be versioned ([2.2.20](#2220-api-versioning-and-idempotency-keys)).
  grain's proxies and ledger have to be rebuilt as Shibu services before
  grain can move. A Shibu outage stops grain. Single-node deployments
  get heavier.

#### Option B: Shibu as a layer inside grain

Shibu's model becomes grain's internal infrastructure layer: a package
(or set of packages) that owns namespaces, allocators, grants and the
web proxy. grain's dispatcher, CI and merge queue call it in-process.
The UI gains a namespace view.

- **For:** the smallest step from today. The ledger, proxies and
  capability providers are refactored in place instead of rewritten. One
  binary, one store and one deployment. Ideas can be tried quickly, and
  the factory findings can be tested against real grain workloads.
- **Against:** nothing stops grain code from going around the layer, so
  the model's guarantees hold only by discipline. Shibu can't be used
  without grain. Its API, which agents call from inside VMs, would be
  served by grain, so grain's availability and security become Shibu's.

#### Option C: a hybrid

Split along the line the factory walk-through drew: Shibu owns the
infrastructure primitives, and grain owns the factory. One version:

- the **data plane** (web proxy, `/sys/model`, the secret store, harness
  runners, credential proxies, VM and volume backends) is a separate
  component from the start, because it is what
  establishes identity, enforces grants and keeps secrets out of VMs;
- the **control plane** (namespaces, allocators, grants, leases) starts
  as a library inside grain with a stable API boundary, so it can be
  moved out later without changing callers.

Another version is the reverse: Shibu is separate, and grain keeps its
own proxies for the capabilities Shibu doesn't model yet.

- **For:** the security-critical part is isolated early, and the part
  still being designed (the proposals in section 5) stays easy to
  change. It is the most incremental path, and it can end at either A
  or B.
- **Against:** the line has to be drawn carefully and kept, or the
  result has the costs of both. For a while there are two ways to do
  things (grain capabilities and Shibu grants), and both have to be
  kept consistent.

The choice was made in [7.2](#72-the-decision-grain-runs-on-shibu):
A as the end state, reached through B's shape. C's first version, with
the data plane separate from the start, was not chosen; the security
argument for it is met instead by the restated credential rule and by
moving grain's credentials behind distribution credential proxies before
grain becomes a component.

---
## Appendix A: Ideas considered and dropped

Earlier revisions of this document had, or proposed, each of the ideas
below, and later revisions dropped them. None of them is part of the
design. Each entry says what the idea was, why it was dropped, and
which sections it relates to. The findings in section 5 still describe
their problems as found in the first version; this appendix describes
the designs they were found in.

### A.1 Aliases

**What it was.** The first version's `alias` and `unalias` made an
endpoint of one namespace appear in another. An inherited alias reached
the target's whole subtree. Whether a control endpoint could be
aliased was left open ([6.2](#62-aliasing-a-control-endpoint)).

**Why it was dropped (1679).** An alias carried no mode, so aliasing a
worker's checkout to a reviewer gave the reviewer write access, unless
the checkout was snapshotted first
([F3](#f3-aliases-carry-no-mode)). Aliases between siblings could only
be made by their common ancestor, so every per-change link was a round
trip to it
([F1](#f1-cross-sibling-wiring-needs-a-live-principal-at-the-common-ancestor),
[F2](#f2-alias-churn-for-per-change-collaboration)), and nothing said
what happened to an alias when its target moved or died
([F13](#f13-what-happens-to-an-alias-when-its-target-moves-or-dies)).
A grant does the same job with `ops` and `limits`, is made by whoever
holds the object, and has a validity rule. `subtree=true` replaces the
inherited alias, and a control-endpoint grant narrowed by operation
replaces control-endpoint aliasing
([F16](#f16-control-endpoint-aliasing-came-up-three-times)).

**Relates to:** [Grants](#grants), [2.2.1](#221-subtree-grants),
[4.2.7](#427-review), [4.2.9](#429-release-and-deploy), F1–F3, F13,
F16, 6.2.

### A.2 Siblings isolated by structure

**What it was.** In the first version the tree itself isolated
siblings. Visibility, use, control and quota between them were
structural, and only a common ancestor could connect two siblings, so
a leak needed a mistake at a specific, high place in the tree.

**Why it was dropped (1679).** It needed a live principal at every
common ancestor to do the wiring
([F1](#f1-cross-sibling-wiring-needs-a-live-principal-at-the-common-ancestor)),
which in the factory was a foreman (A.3). Grants carry all of it. In
v1, `regrant=false` by default keeps a received grant where it was put,
and isolation beyond that is policy, beyond v1
([2.7](#27-beyond-v1-policy)); the boundaries that once did it are
[A.22](#a22-boundaries). The cost is that a careless holder can now
grant something on
([F21](#f21-visibility-leaks-are-a-matter-of-policy-and-audit)).

**Relates to:** [The tree](#the-tree), [Grants](#grants),
[2.7](#27-beyond-v1-policy), F1, F21.

### A.3 A foreman

**What it was.** The first version's factory needed a controller VM
acting as `factory`, holding the whole factory's credential, because
only the common ancestor could wire `svc`, `lead`, `ci`, `mq` and `rel`
together.

**Why it was dropped (1679).** It followed from structural isolation
(A.2), and it was control-endpoint delegation by sharing a credential.
With grants, the namespace that holds a thing grants it, and an
approvals service such as `factory/desk` holds only the quota it hands
out.

**Relates to:** [4.2.1](#421-the-shape),
[4.2.9](#429-release-and-deploy), F1, F16.

### A.4 Splits, claims and handing quota over by `move`

**What it was.** In the first version, `create_namespace` could split
the parent's quota into the child, and the child's share rolled back up
to the parent when it exited. A warm-pool VM was taken with a separate
`claim`. `move` was also used to hand quota from one namespace to
another.

**Why it was dropped (1679).** Once caps were drawn from the parent's
pool, a split already was a lend, so an allocator grant does the job,
and the roll-up became the general rule that a deleted namespace's
received grants are revoked. A claim was a move the claimant couldn't
make ([F4](#f4-warm-pools-do-not-sit-cleanly-on-namespace-bound-allocators));
it is a `create` through a grant of a pool, answered by `adopt`. `move` stays, for
re-homing only.

**Relates to:** [Namespaces](#namespaces) in section 3,
[Move](#move), [2.2.2](#222-caps-reservations-and-adoption), F4.

### A.5 A request inbox in the kernel

**What it was.** The first version gave every control endpoint an
upward request inbox, with `request`, `approve`, `deny` and `escalate`
calls.

**Why it was dropped (1679).** Requests needed structure the kernel
would have had to define: partial approval, conditions, standing
policies and escalation
([F14](#f14-inbox-requests-need-structure)). An answer is a grant, so
partial approval is a smaller grant and a condition is a lease or a
limit. Asking for a grant is a call to an endpoint the other party
exposes, and inboxes and approval policies are additional components.

**Relates to:** [2.2.5](#225-asking-for-a-grant),
[2.2.25](#2225-messages), [2.6.5](#265-requests-stay-in-the-api), F14.

### A.6 A model allocator kind

**What it was.** The first version had a **model** allocator kind.
Each model endpoint it created was one agent.

**Why it was dropped (1741).** An agent allocator would be the only
allocator that charges another, and counting agents is a label query,
not a quota. An agent's sandbox VM is charged to an ordinary VM
allocator, and its budget is a grant on `/sys/model`.

**Relates to:** [Allocator](#allocator), [4.1](#41-agents-a-kernel-composite),
[F17](#f17-the-agent-allocator-overlaps-the-vm-allocator-and-the-credential-rule).

### A.7 An agent as a recipe

**What it was.** The 1741 revision made an agent a recipe, not a
primitive: a `create` of a VM, a grant on the model proxy with a budget
limit, and by convention its own sub-namespace, all applied atomically
with `apply`, with no credential to inject.

**Why it was dropped (1746).** Bootstrap needs a first actor, and a
recipe is a sequence of calls somebody makes, so it can't make itself.
Subscriptions need the `split` placement: the CLI holds the OAuth
token itself, so there is a credential to deliver, and keeping the
place that holds it from running tools is something only Shibu can
arrange. `create_agent` is a kernel composite instead, still with no
agent allocator.

**Relates to:** [4.1](#41-agents-a-kernel-composite),
[F17](#f17-the-agent-allocator-overlaps-the-vm-allocator-and-the-credential-rule).

### A.8 Attenuated tokens

**What it was.** The 1741 revision had `mint_token`, which minted a
token that could only narrow its holder's authority. The caller
assertion carried the scope of any token presented. The factory's
worker minted a fetch-only token for a quick test runner in its own
VM. F29 listed an option to have the tool server run each exec under
an attenuated token.

**Why it was dropped (1762).** With grants and ambient identity, a
token's one job was narrowing authority inside your own VM, and a VM is
the trust boundary: any process in the worker's VM could make the same
requests without the token, so it contained nothing. Code that must
not have the authority runs in another VM, in a sub-namespace. Anything
outside the VM gets a grant, and external callers come in through a
`public-endpoint` with an ingress verifier. A bearer grant is noted
beyond v1 for a recipient with no namespace.

**Relates to:** [Identity](#identity),
[2.2.11](#2211-no-attenuated-tokens),
[2.5](#25-beyond-v1-bearer-grants),
[4.2.5](#425-worker-agents-and-their-sandboxes),
[F8](#f8-proxies-need-the-callers-identity),
[F29](#f29-an-agents-tools-run-with-the-agents-authority).

### A.9 "Secrets never enter VMs"

**What it was.** The first two revisions' credential rule was "secrets
never enter VMs", with one stated exception: harness VMs, which hold
the CLI's subscription token.

**Why it was dropped (1750).** The exception showed what the rule was
for. Harness VMs and the distribution's credential-holding components
have to hold keys, and what makes that safe is that they never run
untrusted code. The rule is now stated that way: a VM that holds a
credential never runs untrusted code.

**Relates to:**
[Layers](#layers-kernel-standard-distribution-additional-components),
[2.2.7](#227-credentials-only-behind-proxy-endpoints).

### A.10 Secrets that only a proxy can reference

**What it was.** Before 1750, a credential proxy for anything but the
model was an extension: "a VM somebody runs, as grain's `pkg/gitproxy`
is today". From 1750 to 1762, a secret could only be referenced by a
proxy, never read, and a VM somebody ran got no secret from the kernel
at all.

**Why it was dropped (1762).** Components such as GitHub-backed
`/sys/git` need the raw key. A grantable `read` op makes that explicit
and audited, and lets the kernel mark the reader's namespace
credential-holding and refuse to put an agent's tools there. Credential
proxies became optional distribution components at `/sys/cred` (1750).

**Relates to:** [2.2.7](#227-credentials-only-behind-proxy-endpoints),
[2.2.24](#2224-secret-store),
[F9](#f9-secrets-have-no-shibu-home).

### A.11 Declared outputs and the two-phase exit

**What it was.** The first version let a namespace declare objects as
outputs with `declare_output`. They were drained to the parent in a
two-phase exit.

**Why it was dropped (1763).** A drain only worked if the exit ran, so
a crash lost the outputs. Outputs also drained only one level, piled
up at the parent with no lease, collided on names, and moved charges in
surprising ways
([F5](#f5-moved-outputs-pile-up-at-the-parent-and-only-go-one-level)).
A deposit area (`keep=`) puts the data in its final home while the
namespace is still running, and the parent moves what nobody planned
to keep before it deletes (1885).

**Relates to:** [2.2.4](#224-lifecycle-and-keeping-results), F5,
[6.4](#64-delete-on-exit-for-non-outputs).

### A.12 Namespace exit

**What it was.** Namespaces exited, through `exit` on a namespace, and
carried exit statuses and reasons such as `succeeded`,
`lease-expired` and `killed`.

**Why it was dropped (1763).** A namespace is a container for VMs and
agents, and those are what exit. A namespace is `active` or
`deleting`, and is deleted explicitly or by its lease. A parent that needs to know why work ended reads it
from the VMs and agents.

**Relates to:** [Namespace](#namespace), [2.2.6](#226-leases-and-ttls),
[2.2.17](#2217-exit-status-and-reason),
[F7](#f7-exit-reasons-need-preempted-and-vm-level-status),
[6.4](#64-delete-on-exit-for-non-outputs).

### A.13 Per-namespace control-plane limits

**What it was.** The first version gave each namespace its own limits
on control-plane calls.

**Why it was dropped.** A per-namespace count is multiplied by creating
children. When rate limits are added they are token buckets at each
link of the grant chain. Until then they are punted, and a runaway
namespace is bounded by its quota.

**Relates to:** [2.2.16](#2216-control-plane-rate-limits),
[F11](#f11-rate-limits-should-be-hierarchical).

### A.14 A kernel UI shell with panels for operator components only

**What it was.** The 1750 revision had the kernel own the UI shell,
and proposed that only operator-installed components could contribute
a UI panel, framed by the kernel.

**Why it was dropped (1771).** The UI is a principal acting as a
namespace through the API, so it doesn't have to be the kernel's; it
is `/sys/ui`, held to CLI parity. The isolation proposed for panels
(an origin per namespace, a frame the UI draws, a narrow bridge, no
credential entry) is what makes a pane safe whoever serves it, so any
agent or component can serve one. Operator-installed components keep
one difference: root may trust them for the main navigation.

**Relates to:** [2.6.1](#261-the-ui-is-a-replaceable-component),
[2.6.6](#266-agent-served-panes), [F30](#f30-component-ui-panels).

### A.15 Earlier names

These were renames, not changes to the design:

- "core", in the first two revisions, is "kernel" throughout (1750).
- Before 1750, the finding statuses **Kernel** and **Additional
  component** were called **Core** and **Extension**.

### A.16 A warm-pool allocator kind in the kernel

**What it was.** The 1741 revision resolved F4 by making the warm pool
a kernel allocator kind, `warm-pool`, created through the VM allocator
that paid for it, with its own object type, create spec and
dimensions.

**Why it was dropped (1870).** It was in the kernel only because a
claim re-homes a live VM from the pool's namespace into an unrelated
target and moves its charge, which nothing else could do. That ability
is general, so it became the kernel primitive `adopt`, and the pool
became an optional standard-distribution component, `warm-pool/v1`,
built on it.

**Relates to:** [2.2.2](#222-caps-reservations-and-adoption),
[F4](#f4-warm-pools-do-not-sit-cleanly-on-namespace-bound-allocators).

### A.17 Trusted and untrusted VMs and runners, and the credential rule

**What it was.** From 1750, the design rested on one rule: *a VM that
holds a credential never runs untrusted code*. 1762 made part of it
kernel-enforced. A namespace holding any secret-`read` grant was
**credential-holding**, and the kernel refused to place an agent's
sandbox VM, tool server or `same-vm` harness in it, to create agents in
or under it, to widen its egress past what was declared, and to accept
a `read` grant into a subtree that already ran an agent's tools.
`split` harness VMs in `/sys/runner` were credential-holding by
construction, and the runner was trusted to run no tools.

**Why it was dropped (1884).** "Untrusted" had no owner: it meant
untrusted by whom. Once any VM can be a runner and any VM can expose
tools to any agent that holds a grant, the kernel can't classify code
as trusted or not; each owner can only decide where its own things go.
So trust is expressed only through grants: a secret's owner grants
`read`, a VM's owner grants `exec`, a runner's owner grants `launch`.
The core enforces those grants and identity on every call and relies on
no VM or runner, and the exposure query shows an owner who could reach
what it has granted. The credential-holding mark stays, as information.

**Relates to:** [Trust](#trust),
[2.2.7](#227-credentials-only-behind-proxy-endpoints), F26, F27, F29,
F31.

### A.18 A special sandbox VM with one tool server

**What it was.** Every agent had exactly one **sandbox VM**, `<path>/vm`,
booted from the adapter's tool-server image. Its tool server
(`tool-server/v1`, at `<path>/tools`) served both the VM tools
(`exec`, files) and the agent tools (`send_message`, `ask`, `exit` and
the rest), could be called only by the agent's own namespace, and could
not be granted elsewhere. Placement was an enum on `create_agent`,
`same-vm` or `split`, derived from the credential kinds.

**Why it was dropped (1884).** Tying the agent tools to a VM meant an
agent couldn't exist without one, and a reviewer couldn't be given
read-only file tools on another agent's VM. Splitting the server gives
every VM an optional, grantable `mcp` and every agent a kernel-served
control MCP. Placement became `runs_in` plus `tools`, and `same-vm` and
`split` are two of their combinations.

**Relates to:** [VM `mcp`](#vm-mcp-and-the-agent-control-mcp),
[Placements](#placements), F29.

### A.19 `/sys/model` as the definition of model access

**What it was.** An agent's model access was its grant on `/sys/model`,
`<path>/model`, with its budget as that grant's limits, and
`create_agent` couldn't run without `/sys/model`. Subscriptions were
the exception, run in `split` with the runner enforcing time and turn
budgets, and `/sys/model` reporting that usage read-only. Routing
subscriptions through `/sys/model` was considered so that one proxy saw
every model call.

**Why it was dropped (1884).** A model is a parameter of a launch, and
how a runner reaches it is the runner's configuration: an API key, a
login, or a self-hosted model. Making `/sys/model` the definition put a
kernel dependency on one way of doing it. Routing subscriptions through
it can't work: redirecting a vendor CLI's base URL drops it out of
subscription mode. Model access is now a `launch` grant on a runner,
and `/sys/model` is one way for a runner to reach models without
holding a key.

**Relates to:** [2.2.23](#2223-model-access-and-budgets),
[Runners](#runners), F17.

### A.20 Lifecycle policies: `delete_policy`, `when-idle` and `armed`

**What it was.** The 1763 revision gave every namespace a lifecycle
policy, `delete=explicit | when-idle | on-lease`, stored as
`delete_policy`. `explicit` was the default, and `on-lease` the default
when a lease was given. `when-idle` deleted the namespace, with cause
`idle`, once every VM and agent in its subtree had exited, and only
after it was `armed`: it had held at least one VM or agent. `on-lease`
made a lease mandatory, so creating it without one failed and removing
the lease of an `on-lease` namespace was refused.

**Why it was dropped (1885).** `on-lease` only named what a lease
already does. `when-idle` put a policy in the kernel that needed its own
state (`armed`), its own cause, and a race with creates in flight
([F32](#f32-when-idle-races-with-a-create-in-flight)), and what "idle"
means (a restarting VM, a queued create, a settle time) differs by
workload. It is built instead on `watch` + `delete`: by the creator, or
by an optional standard-distribution **reaper** component. Job-style
namespaces carry a lease or are deleted by their parent or a reaper.

**Relates to:** [Namespace](#namespace), [2.2.6](#226-leases-and-ttls),
F32.

### A.21 The `deleting` grace period and rescue by `move`

**What it was.** From 1763, deletion entered a `deleting` grace state:
everything was stopped at once, and then for a grace period (`grace=`,
10 minutes by default, stored per namespace, overridable by
`delete(ns, grace)` and settable by `create_agent`'s `spec.grace`) any
ancestor could `move` objects out to somewhere outside the deleting
subtree, with payer rules for objects charged to grants inside it. A
second `delete` could only shorten the grace period, a descendant
already `deleting` ended at whichever end came first, and the state
carried an `ends_at`.

**Why it was dropped (1885).** Grace existed only so an ancestor could
rescue objects, and nothing inside the namespace was warned by it. It
added stored state, an argument on three calls, nested-deadline rules
and a special case in `move`, for something a parent can do before it
calls `delete`. Results are kept by deposit areas declared up front,
or by the parent moving or snapshotting what it wants first. `delete`
now stops VMs with their usual stop signal and timeout and then
deletes, and `deleting` is brief and non-configurable.

**Relates to:** [2.2.4](#224-lifecycle-and-keeping-results),
[Move](#move), F5, [A.11](#a11-declared-outputs-and-the-two-phase-exit).

### A.22 Boundaries

**What it was.** From 1679, a **boundary** was a policy an ancestor set
on part of its subtree with `set_boundary(ns, policy)`, confining which
grants could cross it. Its keys were `grants_out` (no object in the
subtree may be granted outside a path), `grants_in` (grants from
outside into the subtree only if they matched a rule), `require_lease`
(every object and grant created directly in the namespace must carry a
lease), `max_lease`, and `allocators_from` (component allocators only
from inside a path, the mitigation for F33). A boundary bound
everything below its setter, never the setter; a namespace could
tighten a boundary set above it, never loosen it. Each namespace showed
its `boundary` and its `effective_boundary`. `grant`,
`create_namespace`, `set_lease` and creates were checked against every
boundary on the path, failing with `boundary_violation`, sixth in the
enforcement order. The factory fenced `/factory` with
`grants_out: "within factory/"` and a chat exception, `svc/prod` with
`grants_out: "none"`, the reviewers with `grants_in`, and `ci` with
`require_lease`.

**Why it was dropped (1886).** Boundaries were a fixed set of keys,
checked when a grant or object was created. The policy wanted later is
evaluated per call and may run arbitrary predicates over the request
and the time ([2.7](#27-beyond-v1-policy)), so boundaries would have
been a second, narrower mechanism to keep consistent with it. v1
keeps one confinement instead, `regrant=false` by default, which stops
a subtree passing on what it received ([Grants](#grants)). What
boundaries did beyond that is carried over as requirements on the
policy model beyond v1, where deny predicates attached to a subtree are
evaluated per call in the web proxy and policy only subtracts
([2.7](#27-beyond-v1-policy)). Until then, F33 is open again, and leases
in long-lived namespaces are each creator's discipline (F12).

**Relates to:** [Grants](#grants), [The tree](#the-tree),
[2.7](#27-beyond-v1-policy), [4.2](#42-full-software-factory), F12,
F21, F23, F24, F31, F33.

### A.23 Egress policy, egress objects and the egress allocator

**What it was.** Each namespace had an egress policy, `internet`,
`endpoints` (with an optional `allow` list of Shibu endpoints) or
`none`, set at `create_namespace` and changed with `set_egress`. A
namespace's effective egress was no looser than its parent's, plus any
**egress objects** (lists of `host:port`) homed in it or an ancestor,
created through an `egress` allocator with a `hosts` dimension. `/sys`
components were confined to their spec entry's egress, and earlier
revisions tied egress restrictions to credential-holding namespaces.

**Why it was dropped (1887).** It was a second authorization system
beside grants, with inheritance rules of its own. Outside access is
now a grant on `/sys/egress`, narrowed by `hosts` like any limit,
leased, revoked and audited like any grant, and `none` is simply
holding no such grant. What `endpoints` with `allow` did is what grants
already do: a namespace reaches only the endpoints it holds.

**Relates to:** [2.2.13](#2213-egress-policy), [Grants](#grants),
[2.2.7](#227-credentials-only-behind-proxy-endpoints).

### A.24 Endpoint kinds beyond the five

**What it was.** The kernel had an endpoint kind per thing it pointed
at: `agent`, `job`, `messages`, `artifacts`, `exit`, `agent-mcp`,
`usage`, `model`, `mcp`, `runner`, `public` and `resource`, each with
its own ops (`send`, `post`, `call`, `stop`, `launch`, …).

**Why it was dropped (1887).** Every one of them is reached over HTTP
through the web proxy, so the kernel needed nothing per kind but a
vocabulary. As `http` endpoints, they are granted by methods,
`read`/`write`/`tunnel` and `paths`, and a contract that needs more
(`exec` on `mcp`, `launch` on a runner, `fetch`/`push` on git) defines
ops and limits its implementation enforces from the caller assertion.
Nothing is lost: each old op is a method or a contract op.

**Relates to:** [Endpoint](#endpoint), [6.8](#68-how-a-service-declares-its-operations).

### A.25 `use` grants, bound reads, version retirement and the kernel login flow

**What it was.** A secret could be granted with `use`, which let the
holder name it to `create_agent` or a `/sys/cred` endpoint without
reading it. The kernel then issued a **bound** `read` grant to the
runner, `/sys/model` or `/sys/cred`, tied to one session, grant or
endpoint and checked with `bound_to`, and those components found their
secrets with `bound_reads`. Versions had states (`current`, `grace`,
`retired`) with a per-write `retire_previous_after` and a
`retire_secret_version` operation, and the kernel ran OAuth logins
itself with `oauth_start` and a callback.

**Why it was dropped (1887).** It was a second, kernel-issued kind of
grant, invisible to the owner's own grant list, built to let one
namespace spend another's key through a shared component. With trust a
matter of each owner's grants, the owner grants the component `read`
directly, leased and `regrant: false`, and a component uses a
reference only for the namespace that granted it (the reference rule
in [`credential-proxy`, `model` and `runner`](shibu-spec/extension-points.md#common-to-every-distribution-contract)).
Rotation is writing a new value, with a fixed overlap in the store.
A login flow needs a browser and vendor-specific code, which belong in
the runner or the UI.

**Relates to:** [2.2.24](#2224-secret-store), [2.2.7](#227-credentials-only-behind-proxy-endpoints),
[2.2.23](#2223-model-access-and-budgets), F9.

### A.26 The `public-endpoint` kind and its allocator

**What it was.** A `public-endpoint` object, created through a
`public-endpoint` allocator with an `endpoints` dimension, was served
at its own path, skipped the front door, and could name a `/sys/cred`
verifier.

**Why it was dropped (1887).** Publishing is a decision about who may
reach an endpoint, which is what a grant says. Granting the endpoint to
`/sys/ingress` lets that component serve it and decide the audience
(anyone, or an identity-aware sign-in), with any policy it likes,
without a kernel object, allocator or special case in the front door.
Counting public endpoints, if wanted, is the ingress component's
business.

**Relates to:** [2.2.12](#2212-pre-approved-publish), [6.9](#69-callers-with-no-vm).

### A.27 Allocator limits: `reserve`, `kinds`, `classes`, labels and percentages

**What it was.** An allocator grant could carry `reserve`, a promise
per dimension taken off the granter's headroom (the one limit that did
not narrow); `kinds`, the kinds that could be created; `classes`, the
placement classes allowed; `labels`, set by the kernel on everything
created through it; and any numeric limit as a percentage of the
granter's own, re-resolved at every check. Budgets were a set of keys
(`usd_per_day`, `tokens_total`, `turns`, `cpu_hours_per_day`, …).

**Why it was dropped (1887).** A boolean `reserved` on the grant does
what `reserve` did, with two grants for a guarantee plus burst, and
leaves every limit narrowing. An allocator makes one kind, and a
kernel VM allocator offers one class, so which allocator is granted
says both. Labels belong to whoever creates the object. Percentages
made a grant's meaning change under it; a granter that wants to follow
its own limit re-grants. One budget shape, `{unit, per_day | total}`,
replaces the key list.

**Relates to:** [Quota](#quota), [2.2.2](#222-caps-reservations-and-adoption),
[2.2.9](#229-time-based-budgets), [2.2.15](#2215-placement-classes-and-restart-policy), F10, F19.

### A.28 `snapshot`, `clone`, `usage`, per-operation control ops, and `apply` in the kernel

**What it was.** `snapshot` and `clone` were operations of their own,
and snapshots and images were object kinds; `usage` was an operation;
a `control` grant listed the operations it allowed by name
(`list`, `watch`, `grant`, `set_egress`, `apply`, …); and `apply` was a
kernel operation that diffed a declarative document against a subtree,
or against `/sys`, and committed atomically.

**Why it was dropped (1887).** A snapshot is a read-only volume made
from a volume, and a clone a volume made from a snapshot, so `create`
with `spec.from` covers both. Usage is a field of what `get` returns,
and of `cost` over time. Per-operation control grants were never
narrower in practice than "may look" and "may change". Diffing a
document is policy-free work any component can do; what needs the
kernel is atomicity, which the small `batch` op provides, and the
standard reconciler is built on it, for subtrees and for `/sys` alike.

**Relates to:** [2.2.3](#223-volume-snapshot-and-clone),
[2.2.8](#228-orchestration-surface), [The `/sys` spec](#the-sys-spec).

### A.29 `create_agent` as a kernel composite

**What it was.** From 1746 `create_agent` was a kernel call that
atomically created the agent namespace, its VM, its grants, a bound
`read` on its credential, and the harness session, the kernel itself
calling the runner's `launch`.

**Why it was dropped (1887).** After the runner contract, nothing in
it needs the kernel but the agent object and the session binding: it
is create namespace, grants, then `runner.launch`. As a
standard-distribution helper (library, CLI and `/sys/agents`), defined
in kernel operations plus `runner/v1`, it can be replaced or
reimplemented by anyone, and a caller can do every step itself. The
namespace and its grants are still one atomic `batch`; a failed launch
is cleaned up by deleting the namespace.

**Relates to:** [4.1](#41-agents-a-kernel-composite), [`create_agent`](#create_agent),
[Runners](#runners).
