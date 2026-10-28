# Security audit 1: grain's Grain/Silo changes on `main`

> **Historical record.** The Silo hub connection this audit covers was
> removed from grain on 2026-10-19 (task 4826): `pkg/hubnode`, the
> daemon's hub registration, `grain hub`, the Silo claims in the local
> policy and the hub UI are gone, and the code paths named below no
> longer exist. The local policy (`pkg/policy`) and its audit log stay.
> [docs/hub-removed.md](../../hub-removed.md) says how to restore the hub
> from git history. The findings are kept as written.

- **Date:** 2026-10-16
- **Auditor:** grain task 4274 (agent), for task 4193
- **Base audited:** `origin/main` at `4d545ebb5`
- **Method:** bwsalmon/silo `docs/threat-model.md` (task 4269), every
  invariant that applies to a Grain (INV-3, INV-6, INV-11, INV-12,
  INV-17, INV-21, INV-23), plus the checklist in the audit task. The
  code was read, and the hub-offer findings were reproduced with a
  throwaway test against the real `hubTasks` and `ui.Client`. That
  test is not committed.

## Scope

| Task | PR / merge | What it added | Status |
| --- | --- | --- | --- |
| 4156 | #3959, `11babf810` | Local PEP: `pkg/policy`, `docs/local-policy.md`, the dispatch hold (`heldByPolicy`), the pre-sandbox check in `runOne` | audited |
| 4165 | #3962, `9bcbc7b0b` | Hub node registration (`pkg/hubnode`, `pkg/daemon/hub.go`) | audited |
| 4172 | #3970, `c6d57de21` | `grain hub login`, node token storage (`pkg/hubnode/login.go`, `cmd/grain/hub.go`) | audited |
| 4173 | #3969, `3414af599` | Accepting hub offers, resumes and cancels (`hubTasks`, `Config.Serve`) | audited |
| 4174 | (not merged) | Event streaming, view-through | **not on main**: skipped. Grain closes `view` streams unread today |
| 4159 | #3964, `f5e57906d` | `-host-sandboxes` re-enabled, `CheckHostSandboxes` | audited |
| 4192 | #3971, `9a6bef1c3` | `-host-sandbox-env` ambient credential passthrough | audited |
| 4176 | #3968, `7f7329269` | Silo signed claims (`pkg/policy/silo.go`, `pkg/jwks`), `grain create -author/-team`, `-repo`, `-sandbox-backend`, `-model-backend` | audited |
| 4175 | (not merged) | Single-tenant strip-out | **not on main**: skipped |

## Status of task 4190's four findings on PR #3959

All four are **still open** on `main`. No commit after `11babf810`
touches the code involved.

| 4190 finding | Status | This audit |
| --- | --- | --- |
| Proposals with an empty `Creator` bypass approval | open, and wider than proposals: ports, base-branch repairs, reviews, path triggers and hub offers with no submitter all end up "local" | GSA1-02 (high), GSA1-01 (critical, the hub variant) |
| `deny: [repo-write:*]` matches nothing | open; `docs/local-policy.md:90` still uses it as the example | GSA1-03 (high) |
| Hold/admitter grant mismatch | open, but it fails closed: no bypass found, only minting before the check and a cleared wait marker | GSA1-08 (medium) |
| Capability-implied reads (`self-debug` → bwsalmon/grain) not covered by `repos:` | open | GSA1-04 (high) |

## Findings

| ID | Severity | Title | Where |
| --- | --- | --- | --- |
| GSA1-01 | **critical** | A hub offer with an empty (or local-looking) submitter is filed as the operator and runs with no approval and no attestation | `pkg/daemon/hub.go:142-175`, `pkg/ui/client.go:1843-1847`, `pkg/policy/policy.go:263-270` |
| GSA1-02 | high | Agent proposals, ports, repairs, reviews and path-trigger tasks don't inherit the root requester, so they skip approval and attestation | `pkg/ui/client.go:1843`, `pkg/policy/policy.go:263-266`, `pkg/orchestrator/{port,basebranchci,review,pathtrigger,finish}.go` |
| GSA1-03 | high | Capability `allow`/`deny` match exactly, so `repo-write:*` (the documented example) denies nothing | `pkg/policy/policy.go:297-307`, `docs/local-policy.md:90` |
| GSA1-04 | high | Capability-implied repository reads (`self-debug` → bwsalmon/grain) bypass `repos:` | `pkg/orchestrator/checkout.go:439-447`, `pkg/policy/policy.go:244-259` |
| GSA1-05 | high | Hub-placed and other non-local tasks run on unisolated host sandboxes with the operator's ambient credentials (silo O-8), and `-hub-url` doesn't count as "shared" | `pkg/daemon/sandbox_backends.go:243-300`, `pkg/orchestrator/orchestrator.go:882` |
| GSA1-06 | high | `-hub-url` is accepted without `-policy`, so hub tasks run with no PEP at all (silo O-3) | `pkg/daemon/hub.go:85-96`, `pkg/policy/policy.go:211-213,232-234` |
| GSA1-07 | medium | grain CI jobs (workflow capabilities and reads) never pass through the local policy | `pkg/daemon/run.go:1049-1064` (policy wired only into `orchestrator.Deps`), `pkg/daemon/ci.go:641,734-738` |
| GSA1-08 | medium | The hold and the admitter see different grants; the admitter mints scarce grants before the policy recheck and clears the policy wait marker | `pkg/orchestrator/cycle.go:1043-1066,1080-1110,1582-1588` |
| GSA1-09 | medium | The daemon's `-hub-url` accepts `http://` to any host, sending the node token in cleartext | `pkg/hubnode/hubnode.go:96-121`; silo `v2/hub/rendezvous/client.go:152-158` |
| GSA1-10 | medium | Signed claims: `exp` and `aud` are optional, there is no replay binding, and nothing ever stores or applies an attestation | `pkg/policy/silo.go:96-163,182-205`; no caller of `ApplySiloClaims`/`SetSiloAttestation` |
| GSA1-11 | medium | Hub offers are filed `Approved: true` with the submitter recorded as their own approver (INV-17, silo O-4) | `pkg/daemon/hub.go:166-174` |
| GSA1-12 | medium | `-host-sandbox-env` values travel on argv (`/proc/*/cmdline`) and are never redacted from tool output, transcripts or commits | `pkg/agent/agent.go:549-561`, `pkg/mcp/sandbox_tools.go:325-333` |
| GSA1-13 | medium | Release images are built with go1.26.6 (13 reachable stdlib vulns) although `go.mod` pins toolchain go1.26.9 | `Makefile:84,430`, `Dockerfile:92,141`, `go.mod:3,5` |
| GSA1-14 | low | Policy hygiene: a merge artifact duplicates validation in `checkNetwork`, and a second YAML document is silently ignored | `pkg/policy/policy.go:117-121,317-328` |
| GSA1-15 | low | The hub gets the local policy's details and internal errors back as refusal reasons | `pkg/hubnode/hubnode.go:183-188` |
| GSA1-16 | low | An offer with an empty repo skips the node's repo list and lands on `-default-target-repo`; queued hub tasks aren't counted against slots | `pkg/hubnode/hubnode.go:126-131,198-210`, `pkg/daemon/hub.go:143-149` |
| GSA1-17 | low | HTTP callers may set `proposalKey`, and can take over the `silo-hub-task:<id>` mapping | `pkg/ui/client.go:1385,1551-1556`, `pkg/daemon/hub.go:113` |
| GSA1-18 | low | Host-sandbox run directories are created 0755 | `pkg/orchestrator/orchestrator.go:928` |
| GSA1-19 | low | JWKS fetch follows redirects (including to http) and accepts any RSA modulus size | `pkg/jwks/jwks.go:84-91,172-185` |
| GSA1-20 | low | Two independently loaded policy instances: the hub's copy lacks `-repo` additions and has its own key cache | `pkg/daemon/hub.go:85-96`, `pkg/daemon/run.go:1049-1064`, `pkg/daemon/local_settings.go:109-118` |
| GSA1-21 | low | Docs and flags still say `-sandbox-backend host` was removed | `pkg/daemon/local_settings.go:49-50`, `docs/local-policy.md:49-51` |

---

### GSA1-01 (critical): a hub offer with an empty or local-looking submitter runs as the operator, with no approval and no attestation

**Where.** `pkg/daemon/hub.go:142-175` (`hubTasks.Start`),
`pkg/ui/client.go:1843-1847`, `pkg/policy/policy.go:263-270`
(`IsLocal`, `checkApproval`).

**Description.** `hubTasks.Start` files an offer with
`Creator: spec.Submitter`, under the principal
`Human{ID: spec.Submitter}`, with `Approved: true`.

When `Submitter` is empty, `ui.Client.CreateTask` falls back to the
client's own actor (`c.Config.Actor`, which is the daemon's
`-actor`, or the OS user name), so the task is recorded as the
operator's own. `Policy.checkApproval` then returns early for any
requester `IsLocal` accepts. The operator is normally one of
`local_requesters`, and an empty requester is local by definition. The
early return comes **before** the `silo_issuer` attestation check, so
the task needs:

- no approval;
- no signed claim, even on a Grain whose owner set `silo_issuer`
  precisely so that hub claims are verified.

The same happens when a hostile hub simply names the owner
(`Submitter: "me@example.com"`).

`Config.start` only checks the repo against the node's list and a free
slot, and `CheckClaims` checks repos, capabilities and network. Nothing
on the offer path refuses an empty submitter.

**Reproduction.** A throwaway test in `pkg/daemon` used the real
`hubTasks`, `ui.Client` and `policy.Parse`.

The policy was:

```yaml
repos: [acme/*]
local_requesters: [operator]
silo_issuer: https://silo.example
silo_keys_url: https://silo.example/keys
```

The daemon actor was `operator`. Results:

| Offer | Submitter | Filed as | Dispatch `Check` |
| --- | --- | --- | --- |
| a | `alice` | `creator=alice approval=human:alice` | "no Silo attestation" (held, correct) |
| b | `""` | `creator=operator approval=human:` | **`<nil>`: runs** |
| d | `operator` | `creator=operator approval=human:operator` | **`<nil>`: runs** |

With GSA1-05 (host sandboxes) this is code execution on the owner's
workstation, as the owner, on a hub's say-so alone, and that includes
reading the node token.

**Violated invariants.**

- INV-12, which is the limit the owner's accepted risk relies on: "a
  compromised hub … must not be able to get a task past a Grain's local
  policy".
- INV-17.
- `docs/local-policy.md`'s promise that, under `silo_issuer`, an
  external requester's claims are trusted only as signed.
- The audit question "can a forged claim skip the approval check?": it
  can.

**Fix.**

1. Refuse an offer whose `Submitter` is empty (`hubTasks.Start` or
   `hubnode.Config.start`).
2. Never let the hub path fall back to `Config.Actor`: pass an explicit
   creator, and fail if there isn't one.
3. Make a hub-filed task (tagged `silo-hub-task:`, or carrying a new
   `WorkOrigin`/flag) **never** `IsLocal`, whatever name it carries:
   either its claims verify under `silo_issuer`, or it needs a local
   human approval by someone other than the requester.
4. Add regression tests for offers b and d.

---

### GSA1-02 (high): derived tasks don't inherit the root requester, so they skip approval

This is task 4190's finding 1, and it is still open.

**Where.**

- `pkg/ui/client.go:1843`: the `proposedBy == nil` branch is the only
  place `Creator` is set. A proposal, which has `ProposedBy` set, keeps
  `Creator: ""` (`client.go:2059`).
- Ports (`pkg/orchestrator/port.go:692-744`), base-branch repairs
  (`basebranchci.go:1958-1976`), release triage
  (`basebranchrelease.go:608`), reviews (`review.go:452-480`), path
  triggers (`pathtrigger.go:394-399`) and end-of-run proposals
  (`finish.go:1520-1544`) build `model.Task`s with no `Creator`.
- `pkg/policy/policy.go:263-266`: `IsLocal("") == true`.

**Description.** An external request that a human approved runs an
agent. If that task may auto-merge, any `propose_task` call the agent
makes (for example after a prompt injection in the repository) is filed
approved, with an empty requester. The policy then treats the child as
the owner's own: no approval and no attestation. Its repositories,
capabilities and network are still checked, but who it is for is lost.

The same applies to a port or repair of an external task's merge, and
to a review of it. `Requested-by:` trailers on the derived work are
also lost.

**Reproduction.** File an external task (`-author bob@example.com`,
with `local_requesters: [me]`), approve it as somebody else, and let it
auto-merge. From the run, call `propose_task` with `auto_merge` left to
default. The child task has `Creator == ""`, and `policy.Check` returns
nil with no approval rule applied.

**Violated invariants.** INV-17 ("a task filed by … an agent is
`proposed` until such an approval"), and the audit requirement that
tasks filed by agents, ports and repairs inherit their root requester.

**Fix.** Give `model.Task` a root requester:

- `CreateTask` copies the `ProposedBy` parent's `Creator` (and its
  `Team`) onto the proposal;
- ports, repairs, reviews and path triggers copy the source task's
  `Creator`;
- an empty `Creator` counts as local only when the task's `WorkOrigin`
  is one grain itself starts (schedule, suite, CLI);
- a derived task of a non-local root needs the root's attestation or a
  fresh approval.

---

### GSA1-03 (high): capability rules don't glob, so the documented `deny: [repo-write:*]` denies nothing

This is task 4190's finding 2, and it is still open.

**Where.** `pkg/policy/policy.go:297-307` (`checkCapabilities` uses
`containsFold`), `docs/local-policy.md:90` (the example policy).

**Description.** Capabilities such as `repo-write:acme/x`,
`gcp-key:<name>` and `repo-read:o/r` are parameterised. `allow` and
`deny` are compared to the grant by exact, case-folded equality, so:

- `deny: [repo-write:*]` matches no grant at all;
- `allow: [gcp-key]` refuses `gcp-key:team-a`, though an owner would
  expect the family to be allowed;
- `deny: [gcp-key]` doesn't deny `gcp-key:team-a`.

An owner who copies the documentation believes write access to other
repositories is denied when it isn't.

**Reproduction.** Run `policy.Parse("repos: [o/*]\ncapabilities: {deny: [\"repo-write:*\"]}")`,
then `Check` a task with `Grants: [{Capability: "repo-write:o/other"}]`.
It returns nil.

**Violated invariant.** INV-12 (the policy's capability limit), and the
audit item "glob semantics".

**Fix.** Match `allow` and `deny` with `path.Match` (as `repos:` does),
and treat a bare family name (`gcp-key`) as also matching
`gcp-key:*`. Validate the patterns in `validate()`. Add tests for
`repo-write:*`, `gcp-key`, and `gcp-key:team-a`.

---

### GSA1-04 (high): capability-implied reads bypass `repos:`

This is task 4190's finding 4, and it is still open.

**Where.** `pkg/orchestrator/checkout.go:439-447`
(`prepareGrainSourceAt` clones `model.GrainSourceRepo()` for any task
holding `self-debug`), and `pkg/policy/policy.go:244-259`
(`checkRepos` sees only `Target`, `Writes` and `Reads`).

**Description.** A task granted `self-debug` gets a clone of
bwsalmon/grain in its sandbox, plus the read-only source tools, even
when the policy's `repos:` doesn't name bwsalmon/grain. Any other
capability whose provider grants a read scope has the same gap.

**Reproduction.** Use the policy `repos: [acme/*]` and a task targeting
`acme/x` with `Grants: [{Capability: "self-debug"}]`. `Check` returns
nil, and the run's sandbox has `~/grain-src`.

**Violated invariant.** INV-12 (repositories the task "may target,
write or read").

**Fix.** Give `checkRepos` every repository a task will be able to
read, including the implied ones. Either have the capability registry
expose `ImpliedReads(task)` and append them, or have
`prepareGrainSourceAt` (and any other implicit grant) consult the
policy and skip the clone. A policy that doesn't allow the implied repo
then refuses the capability.

**Resolution (after this audit).** Task 4337 removed the implied read
instead: `self-debug` now serves `grain-src/` from the deployment's
baked-in source and implies no repository (`model.CapabilityRepos`), so
there is nothing for `repos:` to refuse. Explicit `repo-read:` and
`repo-write:` grants are still checked against `repos:`; the PEP matrix
in `pkg/orchestrator/pep_matrix_internal_test.go` covers both.

---

### GSA1-05 (high): hub-placed and non-local tasks reach unisolated host sandboxes and ambient credentials

This is silo O-8 / T-TB7-1.

**Where.**

- `pkg/daemon/sandbox_backends.go:243-300` (`CheckHostSandboxes`,
  `hostSandboxSharingFlags`): `-hub-url` isn't in the list of settings
  that make the daemon "reachable by others".
- `pkg/orchestrator/orchestrator.go:882` (`HostSandboxes.WithEnv`):
  one environment for every run.
- `pkg/daemon/run.go` (the `cfg.HostSandboxes` branch).

**Description.** A host sandbox is a directory. Commands run as the
daemon's user, on the host's network and filesystem, and `HOME` is
pinned only for convenience. From such a run, an agent following a
prompt injection can:

- read the node token (`<data-dir>/hub-node-token`, A-3), the daemon's
  database and secrets, model credentials, `~/.ssh/*`,
  `~/.config/gcloud`, `~/.config/gh` and the local API token, all by
  absolute path;
- use the opted-in `SSH_AUTH_SOCK` to push to any repository the
  owner's keys reach. That bypasses both the git proxy's write scope
  and the policy's `repos:`;
- use `CLOUDSDK_CONFIG` to mint the owner's gcloud tokens;
- call the daemon's loopback API as the operator, because
  `-ui-trust-loopback` defaults to true. From there it can approve
  tasks, change grants, or file tasks with any `-author`.

`CheckHostSandboxes` lets this run on a daemon connected to a hub, and
neither the policy nor the backend tells local tasks from hub-placed
ones. So every hub task, and any external-requester task, gets the
owner's identity. Combined with GSA1-01, the hub needs no human at all.

**Reproduction.** Start
`grain daemon -host-sandboxes -sandbox-dir … -host-sandbox-env SSH_AUTH_SOCK -hub-url https://hub -policy p.yml -ui-addr 127.0.0.1:4242`.
It starts. Then make an offer (GSA1-01's offer b) whose prompt says
`cat ~/…/hub-node-token; ssh-add -l`. Both work.

**Violated invariant.** INV-21 ("runs host sandboxes on a reachable
daemon only with `-allow-unisolated-host-sandboxes`"; a hub-connected
daemon is reachable), and T-TB7-1 / T-TB7-2 ("the agent must never be
able to read the node token").

**Fix.**

1. `hostSandboxSharingFlags` adds `-hub-url`, so a hub-connected
   daemon needs `-allow-unisolated-host-sandboxes`.
2. Add a policy rule (on by default) that refuses non-local tasks,
   including hub-tagged ones, on a host-sandbox backend. Failing that,
   pass `-host-sandbox-env` only to runs whose root requester is local.
3. Have the hub registration declare the node as unisolated (a
   label), so the hub can tell submitters.
4. Recommend `-ui-trust-loopback=false` with host sandboxes, or refuse
   to start without it.

---

### GSA1-06 (high): `-hub-url` without `-policy` runs hub tasks with no PEP

This is silo O-3 / T-TB3-2.

**Where.** `pkg/daemon/hub.go:85-96` (`localPolicy` stays nil when
`PolicyFile` is empty), `pkg/policy/policy.go:211-213,232-234` (a nil
policy allows everything), `pkg/daemon/run.go:1049-1064`.

**Description.** Nothing refuses a daemon started with `-hub-url` and
no `-policy`. Every offer is then bounded only by grain's team
configuration, and the trust limit the threat model rests on doesn't
exist.

**Reproduction.** Run `grain daemon -hub-url https://hub …` with no
`-policy`. The node registers, and offers are filed and dispatched.

**Violated invariant.** INV-12, second clause ("a Grain connected to a
hub runs with a local policy").

**Fix.** Have `checkConfig` refuse `-hub-url` without `-policy`, and
add a test. Optionally also refuse an empty `repos:` in that case.

---

### GSA1-07 (medium): grain CI jobs bypass the local policy

**Where.** The policy reaches only `orchestrator.Deps.Policy`
(`pkg/daemon/run.go:1064`). The CI executor (`pkg/daemon/ci.go`,
`pkg/ci`) never consults it. CI grants repo read and write scopes from
workflow declarations (`pkg/daemon/ci.go:641,734-738`).

**Description.** A workflow under `.grain/workflows` on a branch the
agent pushed can declare `reads:` and capabilities. Grain CI resolves
these against deployment rules and runs them in a sandbox, without the
owner's `repos:`, `capabilities.deny` or `network` limits. The fix task
must confirm exactly how far that reaches. In particular, check whether
a branch's own workflow definition is honoured for capability-bearing
jobs on push/PR events, and whether CI sandboxes use the host backend.

**Violated invariant.** INV-12 ("a Grain refuses, and never runs, a
task its local policy does not allow"). A CI job is work run for a
task.

**Fix.** Check each CI job against the same policy (repos, capability
patterns and network) before its sandbox is built. Refuse a job the
policy refuses, and say so in its log.

---

### GSA1-08 (medium): hold and admitter see different grants

This is task 4190's finding 3, still open, and rated **medium**: it
fails closed.

**Where.** `pkg/orchestrator/cycle.go`:

- `heldByPolicy` (1286-1310) reads the task first;
- `grantAdmitter` (1043-1066) then appends `grain-ci` (`GrantByDefault`)
  and calls `ClearGrantWait`, which clears **any** wait, including the
  `local-policy` one;
- it then resolves and materializes scarce grants (1080-1110) from its
  own read of the task;
- `runOne` (1582-1588) checks the policy only afterwards.

**Description.** Nothing runs that the policy refuses, because `runOne`
re-checks before anything is built. But:

1. An edit that adds a denied scarce capability between the hold and
   the admitter gets that capability **minted** (a pool service
   account lease, say) before the recheck refuses and revokes it.
2. A policy whose `allow` omits `grain-ci` passes the hold. The
   admitter adds `grain-ci`, `runOne` refuses, and the run becomes a
   setup failure instead of a clean `local-policy` hold.
3. The admitter wipes the hold's explanation off the task.

**Fix.** Run the policy check inside the admitter, against the exact
task it will materialize for and after its default grants are added.
Do that before `ResolveGrants`/`MaterializeScarceGrants`, and make the
admitter clear only its own wait capabilities. Keep `runOne`'s check as
the backstop.

---

### GSA1-09 (medium): the daemon's `-hub-url` accepts plaintext http

**Where.** `pkg/hubnode/hubnode.go:96-121` (`Config.Client` passes
`cfg.Hub` through), and silo `v2/hub/rendezvous/client.go:152-158`
(`http` becomes `ws`).

**Description.** `grain hub login` refuses a non-https hub except on
loopback (`secureURL`, `pkg/hubnode/login.go:316-333`). The daemon
doesn't. `-hub-url http://hub.example.com` sends
`Authorization: Bearer <node token>` in cleartext on every reconnect,
and accepts offers from anyone on the path.

**Violated invariant.** INV-6 / INV-23 (node token secrecy), and the
audit item on node token handling.

**Fix.** Apply `secureURL` to `cfg.Hub` in `Config.Client` (or in
`checkConfig`), with the same loopback exception. Add a test.

---

### GSA1-10 (medium): signed-claim verification is incomplete and unwired

**Where.** `pkg/policy/silo.go:96-163` (`VerifySiloClaims`),
`182-205` (`ApplySiloClaims`), and `pkg/model/silo_attestation.go`.

**Description.**

- `exp` is optional ("Expiry is checked when the token has one"). An
  attestation without one is valid forever.
- `aud` is checked only when the owner sets `silo_audience`, so by
  default an attestation minted for another node verifies here.
- Nothing binds the token to the hub task ID or the node. With no
  `jti`, a hub can replay an old attestation on a new offer with the
  same title, body and repositories, which re-runs approved work.
- No production code calls `ApplySiloClaims` or `SetSiloAttestation`.
  Hub offers never carry an attestation, so a `silo_issuer` policy
  holds every external hub task forever, while GSA1-01's local and
  empty requesters pass unverified. The feature currently gives no
  protection against a hostile hub.

What is sound:

- algorithms are pinned to ES256/RS256 and to the key's type;
- `kid` is required, and `iss` and `iat` are checked;
- the spec digest binds the claims to repos, base, title and body.

**Fix.**

1. Require `exp`, with a short maximum lifetime (for example, refuse
   `exp - iat > 24h`).
2. Require `silo_audience` with `silo_issuer`, defaulting to the
   node ID.
3. Add the hub task ID to the signed claims and compare it with the
   task's `silo-hub-task:` tag.
4. Wire it: the offer carries the token, `hubTasks.Start` calls
   `ApplySiloClaims` and `SetSiloAttestation` under `silo_issuer`, and
   refuses on failure. Hub-filed tasks are never local (GSA1-01).

Downgrade: removing `silo_issuer` is an owner edit (sound). The only
downgrade path a hub has is GSA1-01.

---

### GSA1-11 (medium): hub offers are filed approved, with the submitter as approver

This is silo O-4 / INV-17.

**Where.** `pkg/daemon/hub.go:166-174` (`Approved: true` under the
submitter's principal).

**Description.** The task's `Approval` is the submitter. Under a
default policy this is refused as self-approval (good). Under
`allow_unapproved_external: true`, or with no `-policy` (GSA1-06), the
hub's word alone dispatches it. INV-17 says a Grain does not treat a
hub task as approved unless it carries `approved_by`.

**Fix.**

- File hub offers unapproved unless the offer carries an `approved_by`
  (once silo adds it), recorded as that approver.
- Otherwise leave the task `proposed` for a local human.
- Refuse at offer time when the policy can't run it without an
  approval nobody here will give.

---

### GSA1-12 (medium): ambient credential values leak through argv and output

**Where.** `pkg/agent/agent.go:549-561` (`SandboxEnvArgs`: the values
become `-sandbox-env NAME=VALUE` arguments of the forked
`grain mcpserver`), and `pkg/mcp/sandbox_tools.go:325-333`.

**Description.**

- Every value `-host-sandbox-env` resolves, including a proxy URL with
  a password or a token-bearing variable, sits in
  `/proc/<pid>/cmdline` for the run's lifetime. Every local user, and
  `ps`, can read it there.
- Nothing redacts those values from `run_command` output. An agent
  running `env`, `gcloud auth print-access-token` or `cat
  $CLOUDSDK_CONFIG/...` puts them in the transcript stored in grain's
  database. From there they reach a commit, and once 4174 lands, events
  streamed to the hub.

The flag's own help says "do not name a variable that holds a secret".
Proxy credentials are the documented use case.

**Fix.**

1. Pass the values to the mcpserver over an inherited file descriptor,
   or a 0600 file, rather than argv.
2. Redact every passed-through value, and `gcloud`-style token
   patterns, from tool output before it is recorded. Do the same in
   whatever 4174 streams.
3. Refuse literal `NAME=VALUE` entries whose value contains
   `user:pass@`.

---

### GSA1-13 (medium): release images are built with a vulnerable Go

**Where.** `Makefile:84` (`GO_VERSION` is read from the `go` directive,
1.26.6), `Dockerfile:92,141` (`GO_VERSION=1.26.6`,
`GOTOOLCHAIN=local`), `go.mod:3,5` (`go 1.26.6`,
`toolchain go1.26.9`), and `Makefile:430` (`make vuln` runs
govulncheck with `GOTOOLCHAIN=auto`, so it checks 1.26.9).

**Description.** `govulncheck ./...` under go1.26.6 reports 13
stdlib vulnerabilities reachable from grain's code, all fixed in
go1.26.9. Among them:

- GO-2026-6617 (HTTP/2 server HPACK race crash);
- GO-2026-6603 and GO-2026-6611 (HTTP/2 server memory and CPU
  exhaustion);
- GO-2026-6609 (unbounded Range parsing);
- GO-2026-6613 and GO-2026-6605 (HTTP/1 desync);
- GO-2026-6607 (crypto/tls ECH);
- GO-2026-6599 and GO-2026-6600 (html/template).

Commit `63d016cdd` meant to fix these by adding the `toolchain` line,
but the image build ignores that line, and the vuln target checks a
different compiler from the one that ships. No module vulnerability is
reachable; GO-2026-5932, `x/crypto/openpgp`, isn't called.

The only new module across the audited merges is
`github.com/bwsalmon/silo/v2`, which is first-party.

**Fix.** Raise the `go` directive to 1.26.9, or make the Makefile read
`toolchain`, and bump `Dockerfile`'s default `GO_VERSION` and digest.
Have `make vuln` use `GOTOOLCHAIN=local`, so it scans the shipped
compiler.

---

### GSA1-14 to GSA1-21 (low)

- **GSA1-14.** `checkNetwork` (`policy.go:317-328`) repeats
  `validate()`'s `silo_*` checks, which is a merge artifact. It is
  harmless, but it makes the network check fail for a reason unrelated
  to network. Separately, `Parse` (`policy.go:117-121`) decodes only
  the first YAML document, so a second document (`---`) is silently
  ignored. Fix: delete the duplicate, and refuse a file with more than
  one document.
- **GSA1-15.** `Config.offer` sends `err.Error()` to the hub as the
  refusal reason (`hubnode.go:183-188`). Policy errors name repos and
  capabilities, and `CreateTask` errors name teams and roles, so a hub
  can map a node's policy by probing. Fix: send a classed reason
  ("policy", "busy", "not served"), and log the detail locally.
- **GSA1-16.** `Config.start` checks the node's repo list only when
  `spec.Repo != ""` (`hubnode.go:204`). An empty repo is filed against
  `-default-target-repo` (`CreateTask`). It is still policy-checked at
  dispatch, but never refused at offer. `free()` counts only live
  sandboxes, so a hub can queue unboundedly many tasks. Fix: refuse
  empty-repo offers, or resolve the repo and check it at offer; count
  queued hub tasks against slots.
- **GSA1-17.** `CreateTaskRequest.ProposalKey` is accepted over HTTP
  (`client.go:1385`). A team member who files a task with
  `proposalKey: "silo-hub-task:<id>"` makes the later offer `<id>` map
  to their task (`client.go:1551-1556`), which the hub can then cancel
  and whose state it reports. Fix: strip `ProposalKey` from HTTP
  callers that aren't the daemon's own MCP path, or reserve the
  `silo-hub-task:` prefix.
- **GSA1-18.** Host-sandbox run directories are `MkdirAll(root, 0o755)`
  (`orchestrator.go:928`), so other local users can read private
  checkouts. Fix: 0o700.
- **GSA1-19.** `jwks.Fetch` uses a client that follows redirects,
  including https→http, and `publicKey` accepts any RSA modulus. Fix:
  refuse redirects off https, and refuse RSA moduli under 2048 bits.
- **GSA1-20.** The policy is loaded twice (`hub.go:85-96`,
  `run.go:1049-1064`). `attachLocalRepos` appends `-repo` to the
  dispatch copy only, so hub offers for a `-repo` repository are
  refused at offer (fail-closed, but surprising), and each copy has its
  own key cache. Fix: load once and pass the same `*policy.Policy` to
  both.
- **GSA1-21.** `-sandbox-backend host` is refused with "host sandboxes
  have been removed" (`local_settings.go:49-50`), and
  `docs/local-policy.md:49-51` says the same, contradicting 4159. Fix
  the message and the docs, or accept `host` behind `CheckHostSandboxes`.

## Checked and found sound

**Policy (4156).**

- An unreadable or unparsable `-policy` stops the daemon (`run.go`) and
  keeps the node off the hub (`hub.go:89-95`).
- `KnownFields(true)` rejects misspelt keys.
- Repo patterns are validated with `path.Match`, case-folded, and
  host-aware.
- The hold fails closed when the task or its attestation can't be read
  (`cycle.go:1286-1310,1317-1328`).
- `runOne` re-checks before any clone or sandbox.
- A config read failure maps the default network to `open`, the widest,
  so a network-limited policy refuses.
- Unknown network values are refused.
- Hub offers are pre-checked with `CheckClaims` against the deployment
  network.

**Hostile hub (4165/4173).**

- `Config.Serve` serves only offer, resume and cancel. Anything else is
  closed unread (INV-11).
- The first line has a 30-second deadline, and lines are bounded at
  1 MiB (`tasks.Stream`).
- Resume and cancel reach only tasks carrying the hub's own
  `silo-hub-task:` tag (but see GSA1-17).
- The team comes from grain's own `RepoOwningTeam`, not from the hub's
  `spec.Team`.
- `CreateTask` enforces the submitter's team role (a non-member is
  refused; a proposer's offer is closed and refused).
- Only `{state, reason}` strings go up the stream. There is no event
  streaming and no view-through on `main` (4174 isn't merged), so
  there's no path-traversal surface and no SILO_ONLY content or secrets
  in streamed events today (INV-3 is not yet applicable).

**Node token (4172).**

- It is written 0600 through a temp file and rename, in a 0700
  directory (`login.go:287-310`).
- It is re-read on each reconnect, so rotation needs no restart.
- Minting again revokes the old token.
- It is never logged; only its ID is printed.
- `grain hub login` uses PKCE, `state` and `nonce`, a 127.0.0.1-only
  redirect listener, and `secureURL` for the issuer and the hub.
- No isolated backend was found mounting the data directory. That was
  checked by grep, not exhaustively.
- On host sandboxes the token is readable: see GSA1-05.

**Host sandboxes (4159/4192).**

- Refused with a non-loopback `-ui-addr`, `-executor-addr`, IAP or run
  hosts, unless `-allow-unisolated-host-sandboxes` is given.
- Not selectable from Settings (`sandboxBackendAvailable`).
- `HOME` is pinned and refused in `-host-sandbox-env`, even if
  validation is skipped (`sandboxEnv`).
- `-host-sandbox-env` requires `-host-sandboxes`, and passthrough is
  opt-in by name only.
- The log prints names, not values.
- Network-`none` tasks are refused, not run unisolated.
- `ReapOrphans` is confined to `<sandbox-dir>/runs`.
- Per-run git credentials are 0600, and the claude MCP config is 0600
  and removed after the run.

**Signed claims (4176):** see GSA1-10 for what is sound.

**`-author` (4176).** It is accepted only from principal-less (operator)
callers; a signed-in user naming anyone else gets a 403
(`tasks.go:2161-2166`). It isn't reachable from the hub path.

**Single-tenant (4175):** not merged. The hub connection can't reach
the loopback UI, because no `http` stream is served.

**Dependencies:** see GSA1-13.
