# Release machinery walkthrough — 2026-11-13 (test plan §9)

Task 5361 walked [§9 of `docs/test-plans/2026-11-13.md`](../test-plans/2026-11-13.md#9-release-machinery)
("What is NOT proved", items 1–6) against `main` at `51844bb2c`. It used real
`grain daemon -sandbox-port-base=0` processes built from that commit, plus one
daemon built from `24d0eba1` (schema 139) for the store upgrade.

Everything outside the daemon was a stand-in, and all of it was made up:

| Stubbed | With |
|---|---|
| github.com (REST, GraphQL, git) | `pkg/github/githubsim` over HTTP behind `-github-host` ([`relsim.go`](2026-11-13-release-machinery/relsim.go); the 2026-11-11 durable-tasks `ghsim.go` for item 2) |
| A GitHub Enterprise Server `ghe.example.com` | `githubsim` over HTTPS under `/api/v3` ([`ghesim.go`](2026-11-13-release-machinery/ghesim.go)). It used a throwaway self-signed certificate trusted through `SSL_CERT_FILE`, with `ghe.example.com` set to 127.0.0.1 in `/etc/hosts`. |
| Cloud KMS | `pkg/secrets/kmsfake` as a server ([`kmsfakesrv.go`](2026-11-13-release-machinery/kmsfakesrv.go)) through `GRAIN_KMS_ENDPOINT` |
| The agent | `fake-agy.py` from `2026-11-11-durable-tasks/`, which makes real MCP calls to the daemon's in-sandbox server (item 2), and a stub `claude` on `PATH` (item 6) |
| The release signing key | A throwaway Ed25519 key made with `grain-agentd-sign keygen` (item 6) |

No real registry was involved, and nothing was pushed or tagged anywhere. The
fixtures are made up: `acme/widgets`, `ghe.example.com`, `alice`/`bob`, and
`projects/demo-project/locations/us/keyRings/kr/cryptoKeys/k/cryptoKeyVersions/1`.

## Environment

| | |
|---|---|
| Sandbox | Debian 12, Go 1.26.10 |
| Added packages | `postgresql` 15, `postgresql-client`, `sqlite3`, `jq`, `socat` |
| Stores | SQLite for every daemon; PostgreSQL 15 (local cluster) as well for item 3 |
| Daemon flags | `-sandbox-port-base=0 -disable-native-sandboxes -disable-self-upgrade -github-host <sim> -github-insecure-http`; item 5 adds `-release-approval-kms-key`; item 2 uses `-host-sandboxes` with the fake agent |
| Browser | none (no UI change) |

## Helpers and logs

Everything is in [`2026-11-13-release-machinery/`](2026-11-13-release-machinery/). The logs are under `out/`.

| File | Item |
|---|---|
| `relsim.go`, `kmsfakesrv.go`, `start-daemon.sh` | Shared by 1, 2 (Stage 1 half) and 5: the simulator, KMS and a real daemon with acme/widgets' declarations on |
| `item1-release-manager.sh` → `out/item1.log` | 1 |
| `item2-found-in.sh` → `out/item2.log` | 2 |
| `migrate-142-145.sh` → `out/migrate-sqlite.log`, `out/migrate-postgres.log` | 3 |
| `ghesim.go`, `item4-ghe-reviewed-merge.sh` → `out/item4.log` | 4 |
| `item5-kms.sh` → `out/item5.log` | 5 |
| `item6-agentd.sh` → `out/item6.log` | 6 |

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| F1 | medium | **The release manager schedule turns `release-train-assets.yml` into a Stage 1 workflow.** `stage1Watched` (`pkg/orchestrator/stage1repair.go`) treats a workflow as Stage 1 when any enabled `github_workflow_run` schedule watches it on an rc tag. `.grain/schedules/release-manager.yml` (PR #1290) watches `release-train-assets.yml` on `v*-rc.*`. So a failed assets build on an rc now files a priority-9 "Fix the red Stage 1 (release-train-assets) at …" repair with `found-in: <rc>` and a `nightly-green` goal on that workflow. It shares the one per-repository `stage1-repair:` tag with the real `nightly.yml` repair, so while that repair is open, a red assets run is appended to it. Reproduced on the real daemon (`out/item1.log`: repair #3, closed by rc.3's green assets run). | **Not fixed.** Deciding what opts a workflow into Stage 1 is a design call: an explicit marker on the schedule, or keying on the train's own schedule. It is also not this walkthrough's to make. Filed as a follow-up. |
| F2 | low (operator clarity) | **A KMS release-approval key the daemon cannot read was reported as an opaque 500.** `-release-approval-kms-key` only has its shape checked at start. A well-formed version that KMS refuses (IAM denied here, but also a missing or disabled version) starts the daemon with no warning. Then `GET /api/release-approval-key`, and a person's Approve or Reject, answered `internal server error (ref …)`. | **Fixed** in `pkg/ui` (`releaseApprovalKMSError`): the answer is now 502 `upstream_unavailable`, naming `-release-approval-kms-key` and what to check. KMS's own answer stays in the log under the answer's ref. `TestAnUnreadableKMSReleaseApprovalKeyIsNamedInA502` fails without the fix. Start-up still makes no KMS call, which is deliberate: a KMS outage should not stop the daemon from starting. |
| F3 | medium (design gap) | **An enrolled host never checks a release signature itself.** `docs/agentd-release-signing.md` says every binary `grain-agentd` accepts is "verified against the public key pinned on the host". In the code, only the controller calls `release.VerifyBinary`, before it serves `/api/hostd/agentd/linux/<arch>` (`pkg/ui/hostd_install.go`). The install script checks only the binary's SHA-256 against the manifest that same controller serves. `grain-agentd install` then pins whatever key the downloaded binary embeds. Nothing on the host updates `grain-agentd` later, so no host-side check ever runs. A compromised controller can therefore hand a new host any binary. | **Not fixed** (it needs a design: where a host-side verifier runs before `grain-agentd` exists). Filed as a follow-up. In this walkthrough the host's check was run by hand with `grain-agentd-sign verify` against the pinned key. |
| F4 | note | The reviewed-merge forge error names only the path (`GET /repos/acme/widgets/commits/…/pulls: 502 Bad Gateway`), not the host. On GHE this means the operator cannot tell from the message which forge failed. | Reported only. |
| F5 | note | Host enrollment in item 6 stopped at the daemon's GitHub device sign-in (`/api/hostd/join` answered `github_device`), because this daemon had no simulated GitHub for it. The release-key pinning, which runs before enrollment, completed. | Walkthrough limitation, not a defect claim. |

Nothing else misbehaved.

## 1. Release manager on a real daemon (`out/item1.log`)

`relsim.go` seeded acme/widgets with this checkout's
`.grain/schedules/release-manager{,-patch}.yml` and
`.grain/templates/release-manager.yml`. The daemon read them through
declarations. Each "train tick" tagged an rc and concluded a
`release-train-assets.yml` run on it:

| Step | Result |
|---|---|
| `v1.1.0-rc.1` assets run (success) | **one** task: "Release manager for the newest rc", approved, priority 8, no hold |
| Its grants | `ci-read`, `releases:read`, `repos:read`, `schedules:read`, `task-admin`, `templates:read`, `self-debug`: **no `dispatch-control` or `dispatch:*`** (`task-admin` is tasks, merge:write, ci, notifications:write) |
| `v1.1.0-rc.2` assets run (failure), manager queued | no second manager; skip "#2 from the previous firing is still open (queued)", outcome `dropped` |
| Manager deferred (`POST /api/tasks/2/defer`), then `v1.1.0-rc.3` | no second manager; skip "#2 … still open (deferred)" |
| The manager's own dedupe query (`state=proposed,…,deferred,failed`, `q=Release manager for`) | finds the deferred #2 |

**Verdict: pass** for what the daemon decides. "Files exactly one fix per unowned
red check, and none when a deferred repair exists" is in the template body, so
it is agent judgement. With a fake agent it can only be read, not proved. The
body does keep `deferred` and `failed` in its search (lines 87 and 143). The rc.2
failure also produced F1.

## 2. `found-in` (`out/item2.log`, `out/item1.log`)

- **Steward-style proposals relayed at run end:** a real daemon dispatched a
  task to the fake agent, which made five real `propose_task` MCP calls:

  | Proposal | `foundIn` (`GET /api/tasks/{id}`) | `task_link` |
  |---|---|---|
  | `[rc-blocker v1.1.0-rc.2] Revert …`, no `found_in` | `v1.1.0-rc.2` (read from the marker) | `found-in v1.1.0-rc.2` |
  | `[rc-blocker v1.1.0-rc.2] Fix …`, `found_in: v1.1.0-rc.1` | `v1.1.0-rc.1` (stated beats title) | `found-in v1.1.0-rc.1` |
  | `found_in: 1.0.0` | `v1.0.0` (normalised) | `found-in v1.0.0` |
  | `found_in: "the latest one"` | none, with the "could not record" note | none |
  | no release | none | none |
- **Stage 1 repair on an rc** (grain's own rc-blocker filing): repair #3 carries
  `found-in: v1.1.0-rc.2` in the API and in `task_link`.
- **The weekly quality report counting it under that version:** not walked. That
  report is an agent's run reading these links, so there is no daemon-side
  count to check.

**Verdict: pass** (the quality report is not covered).

## 3. Host-qualified owners and migrations 142/145 (`out/migrate-*.log`)

`migrate-142-145.sh` let a schema-139 daemon (`24d0eba1`) create the store and
stopped it. It then set `target_repos` to `ghe.example.com/acme/widgets`
(GHE only), `acme/gadgets` + `ghe.example.com/acme/gadgets` (both hosts) and
`acme/tools` (github.com only). Next it inserted the bare-owner rows an older
build wrote into `qualification_config`, `qualification_item`,
`qualification_item_depends_on`, `scheduled_workflow_run`, `release`,
`release_candidate` and `qualification_run`, and opened the store with HEAD.

| Store | Schema | widgets | gadgets | tools |
|---|---|---|---|---|
| SQLite | 139 → 146 | `ghe.example.com/acme` in all seven tables | `acme`, with three "target repos name it on more than one host" log lines (142 plan, 142 firing, 145 releases) | `acme` |
| PostgreSQL 15 | 139 → 146 | same | same | same |

After the upgrade, the HEAD daemon's forge reads for the migrated repositories
went to `https://ghe.example.com/api/v3/repos/acme/widgets{,/git/matching-refs/tags/v}`
for widgets. They went to the `-github-host` stand-in for the github.com rows,
and **never to `api.github.com`**. The first run had no `-github-host`, and its
anonymous reads of the made-up `acme/*` repositories did reach `api.github.com`
(404). The script now points `-github-host` at a dead local port.

**Verdict: pass.** The migrated rows were checked, not new writes made on a GHE
target, which need a GHE git connection this setup did not have.

## 4. Reviewed-merge on GHE (`out/item4.log`)

`cmd/reviewed-merge -repo ghe.example.com/acme/widgets` ran with no
`GITHUB_API_URL`. That is the `reviewedmerge.REST` the daemon's
`-upgrade-reviewed-merge-repo` uses, so it derives `https://ghe.example.com/api/v3` itself.

| Case | Result |
|---|---|
| alice's PR, approved by bob, merged by alice | **pass**, record `via: review`. Calls: `GET ghe.example.com/api/v3/repos/acme/widgets/commits/<sha>/pulls`, `…/pulls/9000`, `…/reviews`, `…/git/commits/<sha>` |
| alice's PR self-merged on top | **refused** (exit 1): "pull request #9001 was merged by alice, with no approval … and not by grain" |
| first request answered 502 | retried once, then **pass** |
| every request answered 502 | exit 3, "forge error, not a policy refusal … after 5 attempts" |
| requests to `api.github.com` | 0 |

**Verdict: pass.** The daemon's own upgrade gate was not driven end to end,
because that needs `-upgrade-image` and `-upgrade-verify-cmd`. The check it runs
is the same code.

## 5. KMS release-approval signer (`out/item5.log`)

| Step | Result |
|---|---|
| `GET /api/release-approval-key` | KMS's `getPublicKey` for the version, byte for byte; no local key file made |
| Person's Approve of `v1.1.0` (`v1.1.0-pending` tag) | 202; one `asymmetricSign`; `promote-release.yml` dispatched on `main` with `approval` and `approval_signature` |
| `scripts/verify-release-approval.sh` with the served key | accepted, prints the approver |
| …with the record tampered (`approve` → `reject`) | refused: signature does not verify |
| …with another P-256 key | refused |
| Daemon with `…/cryptoKeys/k` (no version) or `not-a-key-version` | **exit 2 at start**: "is not a Cloud KMS key version name" |
| Daemon with a well-formed version KMS denies | starts; the key route answered an opaque 500 → **F2**, now a 502 naming the flag |

**Verdict: pass** after F2.

## 6. Signed `grain-agentd` release assets (`out/item6.log`)

`scripts/build-agentd.sh` (what `make dist-agentd` runs) was run with
`AGENTD_RELEASE_PUBLIC_KEY` set to the throwaway key and
`AGENTD_RELEASE_SIGNING_KEY` signing each manifest. That gave
`linux/amd64` (23.4 MB) and `linux/arm64` (21.9 MB), both `signed=true`.
A real daemon served them from `GRAIN_AGENTD_DIST_DIR`, with that key pinned
through `GRAIN_AGENTD_RELEASE_PUBLIC_KEY`:

| Step | Result |
|---|---|
| `GET /api/hostd/agentd/linux/{amd64,arm64}` and `/manifest.json` | 200 each |
| Verify with the throwaway key | each verifies (signer = the throwaway key) |
| Verify with the repository's own `agentd-release.pub` | refused: "signed by key …, not pinned key …" |
| arm64's binary against amd64's manifest | refused (size) |
| arm64's binary placed as amd64's | controller **404**s both routes |
| amd64 binary with one byte changed | controller **404**s; verification refuses (sha256) |
| original restored | 200 again |
| `grain-agentd install` of the downloaded linux/amd64 binary | pins `~/.config/grain/agentd-release.pub` = the throwaway key. That key verifies its own platform's manifest and binary and refuses the other platform's binary. Enrollment then stopped at GitHub device sign-in (F5) |

**Verdict: pass** for the controller and the pinned key. The host-side check is
F3: the plan item assumes an enrolled host verifies signatures itself, and
it never does.

## Not covered

- The weekly quality report's per-version count (item 2), which is an agent's report.
- New writes of plans, releases and candidates on a GHE target (item 3). Only migrated rows were checked.
- The daemon's `-upgrade-reviewed-merge-repo` gate driven by a real upgrade (item 4). The shared check was walked through `cmd/reviewed-merge`.
- Cosign v3.1.4 legacy bundles (PR #1253) and an edition build settling a signal-less install track (PR #1275). These need real registries and the edition.
