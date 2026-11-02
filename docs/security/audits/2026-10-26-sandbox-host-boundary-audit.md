# Security audit: granule exec/forward wire, state-repo sync, hostd/localexec

- **Date:** 2026-10-26
- **Auditor:** grain task 950 (agent), split out of task 907's 2.4 audit
- **Base audited:** `origin/main` at `d08db708b`
- **Method:** the three subsystems task 950 named were read against the
  tree, their own suites and `tests/security` (with the
  `.grain/baselines/*` ratchets) were run, and every finding below
  marked *reproduced* was confirmed with a throwaway test against the
  real code. Those tests are not committed; each fix task names the
  regression test to add in its place. Fix tasks were filed one per
  finding with `propose_task` (`repo: bwsalmon/grain`, `branch: main`).

## Scope

| Area | What was looked at | Status |
| --- | --- | --- |
| granule guest ↔ controller | `third_party/granule/pkg/execwire` (line and frame bounds, strict request decode, signal/exit codecs), `cmd/granule-agent` (`fromHost` peer check), `pkg/agent/session.go`, `pkg/guestexec` (dial handshake, `Ready`, cancel path), `pkg/forward` (`readLoop`, `handleIncomingOpen`, stream ids), `pkg/granule/forward.go`, `pkg/netshim` (`inline`) | audited; **finding 2** |
| State repository sync | `pkg/staterepo` (`SettingsTables`, `Apply`, `withheld.go`, `check.go`/`postcheck.go`), `pkg/orchestrator/securityhold.go` (`holdForTrustBoundary`), `repoguard.go`, `sync.go` (`StateRepoGate`), `pkg/trustboundary`, `pkg/ui/config_refusal.go` (rehearsal, `checkSettingsWritableUnrehearsed`), `pkg/model/rehearsal.go` | audited; **finding 1** |
| hostd / localexec / runhost | `pkg/hostd/allowlist.go` (`Check`, `environment`, `checkArg`, `checkPathUnder`), `owner_unix.go`, `server_linux.go` (socket mode, `SO_PEERCRED`), `pkg/safepath`, `pkg/localexec` (`snapshot.go`, `exec.go`, `approve.go`), `docs/host-scripts.md` | audited; **findings 3, 4** |

Suites: `tests/security` (ratchets included), `pkg/hostd/...`,
`pkg/localexec/...`, `pkg/runhost`, `pkg/trustboundary`, and granule's
`pkg/forward`, `pkg/execwire`, `pkg/agent`, `pkg/guestexec` all passed.
No baseline was raised.

## Findings

Severity is the auditor's reading. Each has a fix task; the task ids
are in the closing note on task 950.

### 1. A merged `grain/config` change is never held at the trust boundary — **High**, reproduced

`pkg/staterepo` `Apply` imports every table on `SettingsTables` from a
merged state-repository commit into the running daemon on the next
tick: `template_grant`/`schedule_grant` (capabilities), `grain_config`
(`network_access`, `k8s_sandbox_image`/`k8s_granule_image`,
`k8s_privileged`, `k8s_service_account`, `mcp_servers` -- the
capability servers attached, with their `$secret:` bindings --
`capability_policy`, auto-submit limits),
`repo_config`, `repo_workflow` (CI jobs and their capabilities for
repos grain does not control), `standing_sandbox` with
`standing_sandbox_secret` (any secret-store key injected as an env var
into a sandbox running any image the row names), and `gcp_key`.

The only gate on a pull request into that repository is
`holdForTrustBoundary`, which classifies the changed paths with
`trustboundary.Classify` plus the repository's own `.grain/triggers`.
Every exported file lives under `tables/`, and `Classify` returns no
category for any of them (reproduced for `tables/template_grant.json`,
`grain_config.json`, `schedule_grant.json`,
`standing_sandbox_secret.json`, `gcp_key.json`, `repo_config.json`,
`repo_workflow.json`). The state-repo check (`postcheck.go`) checks
format, not privilege. So a task with `writes:` on the state
repository and auto-merge -- which a director session's own proposals
inherit -- merges, and the daemon applies, a change that grants
templates any capability, turns network isolation off, or binds a
deployment secret into a sandbox it then reads, with no person
involved.

Fix: classify state-repository paths that raise privilege (grants,
`grain_config`, `repo_workflow`, `standing_sandbox*`,
`persistent_sandbox`, `gcp_key`, `repo_config`) as a `RequiresHuman`
category when the PR's repository is the deployment's settings repo,
built in rather than left to that repository's own triggers; tests that
a PR touching `tables/template_grant.json` on the state repo is held
and one touching `tables/board.json` is not.

### 2. A guest can open unbounded controller-side streams, and reuse stream ids — **Medium**, reproduced

`third_party/granule/pkg/forward` `readLoop` on the controller (the
`granule forward -kube` process on the grain host, `RunClient`) starts
`go handleIncomingOpen` for every `frameOpen` the guest writes. Since
task 907's fix the target is allowlisted, but nothing bounds how many
streams a guest may open, and the stream id is taken from the frame
unchecked: it is not required to be of the guest's parity (even) or
unused, and `registerStream` overwrites a live entry, orphaning the
earlier stream's connection and goroutines until the target closes it.
Reproduced: 500 Opens all naming stream id 1 made the controller dial
500 TCP connections to the forwarded daemon port, all left open. Each
can also hold up to 64 queued 1 MiB `frameData` payloads when the
target is slow to read. The result is fd and memory exhaustion of the
forward process and of the daemon listener it targets, from a sandbox
whose own network may be `CHV_NET_MODE=none`.

Fix: refuse (with `frameOpenErr`) an Open whose id has the receiving
side's own parity or is already registered; cap concurrent streams per
session (a few hundred) and refuse beyond it; optionally bound queued
bytes per stream. Tests in `forward_test.go` for each.

### 3. hostd `paths_under` checks a path the child then re-resolves — **Medium**, reproduced

`Allowlist.Check` runs every path-like argument through
`checkPathUnder` (`safepath.Within`) and then passes the *original*
argument to the child, not the resolved path; `safepath.Within` also
accepts components that do not exist yet. The daemon -- hostd's
untrusted side -- writes under the usual root (`/var/lib/grain`), so it
can name `/var/lib/grain/x` that does not exist, have it accepted, and
create `x` as a symlink to anything the installing user can read before
the child opens it. Reproduced: an argument accepted by `Check` read a
file outside `paths_under` through a link planted after the check.
Environment values (`checkEnvValue`) have the same shape. `-dir` is not
affected (`Check` substitutes the resolved working directory).

Fix: substitute the resolved path into the argument, require it to
exist, and refuse a final component that is a symlink at check time;
document in `design/design/cli-run-location.design.md` that `paths_under`
contains what the CLI is *told*, not what a daemon able to write under
the root can swap in afterwards (a race remains for any path the
daemon can rewrite, which only an `openat`-style open in the child
could close). Test: plant the link after `Check` and assert the spec
names the resolved path or is refused.

### 4. localexec approvals do not show stdin — **Low**

`pkg/localexec` `approve` sends the approver the argv and only
`len(stdin)` (`Request.Stdin`, "and N bytes on stdin"). A script that
takes its instructions on stdin is approved by a person who cannot see
them. Fix: carry a bounded, redacted preview (or a hash plus the first
few KiB) of stdin on the request and show it wherever `Summary` is
shown; test that it appears.

## Looked at and found sound

- `execwire`: response/request lines are capped at `MaxLine` (4 MiB) on
  both sides, frames at `MaxPayload` (64 KiB), requests decode strictly,
  signal 0 and out-of-range signals are refused.
- `cmd/granule-agent` serves only connections from `VMADDR_CID_HOST`, so
  a guest process cannot reach its own agent over vsock loopback.
- `GRANULE_EXEC_USER=debian` is not a boundary inside the guest: every
  run already has passwordless sudo (task 907's audit says the same).
  The boundary is the controller, and what a guest can forge -- its own
  exit status, `Features`, and readiness via the ready file -- only
  describes the guest's own run.
- `guestexec`'s hybrid-vsock handshake reads at most 64 bytes under a
  deadline; the session after it has no deadline by design.
- `CHV_NET_MODE=inline` gives the guest the pod's own IP and MAC and
  nothing more: `netshim` installs no NAT and no forwarding, ARP is
  pinned to the given identity, and the pod's own loopback stays in the
  pod's namespace. What the pod identity reaches is task 907's pod
  manifest review (`automountServiceAccountToken: false`).
- State repo: `withheld.go` keeps `google_oauth_client_secret` out of
  the export and ignores it on import; secret *values* are never in the
  tables (finding 1 is about the bindings).
- `config_refusal.go`: a rehearsed write runs in a transaction that is
  always rolled back (`rehearseOnce`); a write that changes only
  `*_sequence.json` is let through but still rolled back; the
  unrehearsed routes (those that set a secret first) are refused before
  they run.
- hostd: socket created under umask 0177 and chmod 0600; the peer uid is
  checked with `SO_PEERCRED`; the allowlist must be a regular file not
  group/world writable and owned by an allowed uid; `LD_*` and identity
  variables need an exact `env` entry; `*` alone is not a valid pattern;
  the daemon cannot override `set_env`/`pass_env`.
- localexec: scripts must be regular, executable, owned by grain's user
  and in a folder no group/world can write; content is re-hashed before
  each call and again after an approval wait; argv is passed with no
  shell; a timeout kills the whole process group.
