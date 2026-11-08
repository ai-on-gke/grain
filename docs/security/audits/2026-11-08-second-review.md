# Security review: merges to `main`, 2026-11-08 (second review, 03:03 → 11:08 UTC)

- **Date:** 2026-11-08 (UTC), second review of the day. The first,
  `2026-11-08.md` (task 3329), audited up to `d7dde0c2` (03:03 UTC) and
  was itself merged in this range (#905). This file picks up where it
  stopped; the next review should start from the base below, not from
  `2026-11-08.md`'s.
- **Auditor:** grain task 3584 (agent), the nightly review of the day's
  merges, in the shape of task 907's 2.4 audit
  (`2026-10-25-2.4-audit.md`)
- **Base audited:** `origin/main` at `1ab19467` (merge of #941,
  2026-11-08 11:08 UTC)
- **Range:** `d7dde0c2..1ab19467` on `main`'s first-parent line: the
  **57** pull requests merged since the previous report's base
  `d7dde0c2` (merge of #877, 03:03 UTC); the oldest in the range is #883
  (04:17 UTC). Merge times below are UTC.
- **Method:** every merged pull request's file list was run through
  `trustboundary.Classify` (v23 at the head) with a throwaway program
  (not committed) and mapped to the surfaces below; the in-scope code of
  every change on the brief's named surfaces was read together with what
  it calls across the boundary; `go test ./pkg/trustboundary/...
  ./tests/security/...` and the suites of every touched boundary package
  were run (all green, see "Tests run"); each finding marked
  *reproduced* was confirmed with a throwaway test against the real code
  (described under the finding, deleted before commit). Fix tasks are
  filed one per finding with `propose_task` (`repo: ai-on-gke/grain`,
  `branch: main`).

> **Dedup note.** This task holds neither `tasks:read` nor
> `api:self-debug`, so `grain task list --search …` (`canonical_grant`,
> `trust-boundary`, `classify`, `metadata`, `password`, `sslmode`,
> `hosted`, `fix(security)`) shows only task 3584 itself, as the
> deployment notes warn; the searches cannot rule out an open duplicate.
> What can be ruled out: the 11-08 first review's one finding (v22/v23
> residue) landed in this range as #908 (`60dbf46d`, v23), and none of
> the three findings below names a file or path that report, or any
> earlier one under `docs/security/audits/`, lists -- all three are
> introduced by pull requests in this range (#927, #883, #917).

## Commits reviewed

First-parent merges, newest first. The last column is what
`trustboundary.Classify` reports for the pull request's full file list
(with the number of files per category); "—" means the list touched
nothing on it: `ui/`, `docs/`, `design/`, tests, or code outside every
category. 27 of the 57 are "—" (walkthrough docs, UI fixes, design
notes); the other 30 touch at least one classified path.

| Merge | Commit | PR / task | Change | Trust-boundary categories (files) |
| --- | --- | --- | --- | --- |
| 11:08 | `1ab19467` | #941 / 3609 | fix(release): use exponential backoff for release and image CLI downloads | release-machinery(3) github-workflows(1) sandbox-images(2) |
| 10:52 | `beefc4da` | #943 / 3460 | docs: record the 2026-11-08 repository, merge queue and release walkthrough | — |
| 10:20 | `ad6d8b91` | #933 / 3458 | fix(ui): keep a template's priority when the New task form restores a draft | — |
| 10:17 | `eb22c406` | #938 / 3358 | fix(ui): honor browser time zone and inline RoleChip across Settings | — |
| 10:06 | `297401e2` | #935 / 3355 | fix(ui): name kanban and list move slots by task id, not title alone | — |
| 09:40 | `82a986f8` | #936 / 3357 | fix(ui): preview a template-backed schedule's own hosting | — |
| 09:35 | `c30f93b6` | #930 / 3359 | fix(ui): take down a git connection's refusal banner once it saves | — |
| 09:33 | `1f0a0d79` | #925 / 3563 | fix(model): rewrite gcp-key grants naming an unconfigured set instead of keeping account | state-repo(2) |
| 09:29 | `c963bbcb` | #928 / 3564 | fix(ui): stop offering reorder and Grant to roles that cannot use them | — |
| 09:26 | `3745076e` | #932 / 3360 | fix(ui): date overcommit adjustments in the deployment's zone | — |
| 09:24 | `7cffe2f7` | #931 / 3459 | fix(ui): say when the boards could not be loaded instead of "No boards yet" | — |
| 09:21 | `7dbfb025` | #934 / 3361 | fix(ui): show New session's first message as sending while it starts | — |
| 08:59 | `a85135b8` | #927 / 3304 | feat(capabilities): read api and repo grants in both spellings (unified grants slice 2) | auth-routes(1) capabilities-secrets(8) exec-sandbox(2) |
| 08:58 | `b9f715ee` | #926 / 3565 | fix(access): refuse unlisted owners' gma_ tokens and code sign-in behind IAP | auth-routes(2) |
| 08:57 | `c8797d34` | #924 / 3567 | fix: queue proposals with no base from a parent filed on the default branch | auth-routes(1) exec-sandbox(2) |
| 08:57 | `c7e26b78` | #923 / 3568 | fix(external-agents): only a verified minter becomes a token's owner by default | auth-routes(2) |
| 08:56 | `546fdcb2` | #918 / 3569 | fix(ui): stop stale console-session tabs undoing grant changes | auth-routes(1) |
| 08:56 | `b6fc5cce` | #917 / 3570 | fix(agent-api): hold external agent credentials to the G8 approval rail | auth-routes(1) |
| 08:55 | `1fa77bfb` | #921 / 3574 | fix(orchestrator): don't fire a freshly seeded system schedule within its first hour | exec-sandbox(2) |
| 08:55 | `9410e7b6` | #911 / 3580 | feat(release): build version-stamped CLI archives and publish them to a release | release-promotion(1) release-machinery(3) |
| 08:54 | `6fe606b9` | #920 / 3590 | docs(design): cite current host command names and drop a retired flag | — |
| 08:54 | `d46cf6d6` | #919 / 3591 | docs(design): record CI hidden from the sidebar by default | — |
| 08:52 | `fb0129e6` | #916 / 3592 | docs(design): record the sidebar-footer sign-in after PR #726 | — |
| 08:52 | `5861d1fb` | #922 / 3585 | fix(release): let RELEASE_QUALIFICATION_APP=none qualify on Stage 1 alone | release-promotion(3) release-machinery(1) |
| 08:08 | `6d15dc3a` | #912 / 3577 | fix(secrets): skip Secret Manager keys gone upstream instead of failing redaction | capabilities-secrets(3) |
| 07:42 | `10df6284` | #913 / 3586 | fix(demo): attach the built-in config-map server so session grants can be edited | — |
| 07:22 | `6f072684` | #903 / 3158 | fix(ui): hold Create task while Base branch is a malformed fork head | — |
| 07:15 | `43208397` | #896 / 3411 | fix(model): say a person-granted proposal is held for a person, not unheld | — |
| 07:06 | `cd952ada` | #907 / 3579 | fix(gcp): point gcloud at token-mode credentials instead of logging in | capabilities-secrets(3) |
| 06:26 | `d5106506` | #914 / 3576 | fix(registry): send grain-config metadata grain-instance-init.sh can boot from | credential-proxy(2) |
| 06:19 | `b4e653c5` | #902 / 3221 | docs: catch the docs up with main's merges since be8390ba | — |
| 06:13 | `c1100d9a` | #890 / 3491 | docs(test-runs): start the §7 committed-context walkthrough driver | exec-sandbox(2) |
| 05:44 | `39a34b30` | #915 / 3572 | fix(hostd): say a placed server needs a grain host on an agents-only host, and "the host age... | capabilities-secrets(3) exec-sandbox(5) |
| 05:43 | `60dbf46d` | #908 / 3583 | fix(security): classify the human-review hold, director floor and on-behalf-of attribution (... | hold-policy(2) |
| 05:15 | `233ad69a` | #906 / 3573 | fix(daemon): serve a known hostd failure while a background probe refreshes it | auth-routes(1) |
| 04:57 | `dc234b44` | #904 / 3163 | fix(ui): reset a blank sandbox disk floor minimum to its default | — |
| 04:49 | `3a44b891` | #910 / 3165 | docs(test-runs): responsive navigation and live-session walkthrough, 2026-11-08 | — |
| 04:46 | `71f538a8` | #909 / 3354 | docs(test-runs): task intake, Inbox and lifecycle walkthrough for 2026-11-08 | — |
| 04:39 | `7a96dbbb` | #894 / 3487 | fix(access): cap an owned gma_ token at its owner's current role | auth-routes(2) |
| 04:36 | `937aedee` | #901 / 2805 | feat(ui): serve GET /cli/install.sh to install the grain CLI | auth-routes(4) security-tests(2) |
| 04:34 | `ca2b39bb` | #905 / 3329 | docs(security): add the 2026-11-08 security review of main (667b82c7..d7dde0c2) | — |
| 04:28 | `0641a77f` | #898 / 3319 | feat(ui): show the Hosting setting on the template and schedule editors | — |
| 04:27 | `c69bc4e5` | #897 / 3414 | test(gcp): walk gcp token mode live on a daemon over a fake GCP | capabilities-secrets(9) exec-sandbox(1) |
| 04:27 | `5e5e6777` | #893 / 3457 | fix(ui): count a removed repo's remaining tasks on its page | — |
| 04:26 | `698cce9e` | #895 / 3488 | fix(api): honour hold_for_approval in the agent approval rail whoever proposed it | auth-routes(2) exec-sandbox(2) |
| 04:25 | `db6a465c` | #892 / 3489 | fix(hostd): keep a host's renewal schedule across reconnects, and say host in grain-agentd | exec-sandbox(5) |
| 04:25 | `2c9c5ada` | #891 / 3490 | fix(cli): walk 2026-11-07 §6 capabilities and admission; show admission in declarations check | — |
| 04:24 | `30721c0e` | #887 / 3493 | wip(test-runs): §9 walkthrough items 1-3 and bug fixes F1-F5 | merge-gate(2) release-machinery(1) state-repo(8) upgrade-images(2) |
| 04:24 | `0345d79b` | #885 / 3479 | feat(trustboundary): classify grain-registry's front door (v22) | hold-policy(2) |
| 04:23 | `665dd100` | #886 / 3538 | fix(capabilities): let github#sandbox-repo mint through MintPass and release its repository | auth-routes(1) capabilities-secrets(10) merge-gate(1) |
| 04:22 | `608eda10` | #878 / 3539 | feat(registry): add grain registry stop and start | credential-proxy(2) |
| 04:22 | `44303daa` | #880 / 3547 | feat(ui,cli): narrow and sort the task list by admission | — |
| 04:21 | `51dab543` | #882 / 3548 | fix(ui): ask the server for owner and externalRef conditions over closed work | — |
| 04:20 | `b0ba37fa` | #881 / 3549 | docs(capabilities): add keyless GCP token access design and spec | — |
| 04:19 | `6f6c39d7` | #889 / 3550 | fix(git-login): sign delegated PRs and comments as the credential owner | credential-proxy(2) |
| 04:18 | `b612596a` | #888 / 3551 | fix(ui): don't wake grain wait --until awaiting-reply before the park settles | auth-routes(1) |
| 04:17 | `eb1c8ac8` | #883 / 3544 | docs(registry): add instructions for updating staging deployment in subsequent PRs | credential-proxy(13) deploy(16) |

## Trust-boundary surfaces touched

| Surface (brief §1) | Pull requests | What changed |
| --- | --- | --- |
| Agent API scopes and route authorization (`pkg/ui/agent_api*.go`, `route_scopes.go`, `access_roles.go`, `accesscode.go`, `external_agents.go`, `mcp_endpoint.go`, `pkg/model/agent_api.go`, `pkg/model/external_agent_token.go`) | #917, #926, #894, #923, #901, #927, #895, #918, #888, #906, #924 | #917 adds `externalApprovalRail`/`externalApprovalRefusal`: an external credential (`external:<name>`) is refused approving its own lineage or any person-held task, over REST, `/mcp` and `Client.Approve`/`RunNow`; run-now's implicit approval now asks for `tasks:admin`. #894 + #926 add `externalTokenOwnerCap` in `authenticateExternalAgent`: a `gma_` token wider than its owner's *current* role, or whose owner is no longer on the allowlist while a verifier is active, is refused (403). #926 also refuses `POST /api/auth/access-code` for a caller a verifier already identified (the code cannot widen a verified role). #923 records `OwnerSource` on a token: an access-code, loopback or in-process issuer never becomes a verified on-behalf-of owner. #901 adds `GET /cli/install.sh` (`internal` scope, `scopeSignedIn`, a single-tenant open route) serving a version-stamped install script -- read: it embeds only a release string and a URL, both regex-checked. #927 teaches `ParseAPIGrant` (so `APIGrantsOf`, the whole scope set) to read `https://grain.dev/capabilities/api#<scope>[={"repo"}]` as `api:<scope>[@repo]`. #895 labels carve-out routes with the scope that actually reaches them in `grain_explore_scope`. #918 adds docs for `PUT /api/console-sessions/{id}`'s `expectCapabilities`. #888 narrows `until=awaiting-reply`. #906 caches a hostd *failure* only. #924 wires `GitHubDefaultBranches`. |
| Capability grants and placements (`pkg/capability/...`, `pkg/model/capability_*.go`, `pkg/model/ci_grant_rule.go`, `pkg/daemon/capability_servers*.go`, `pkg/daemon/ci_capability_pass.go`) | #927, #886, #897, #907, #915, #925 | #927 (unified grants slice 2, 9b75fe30): `model.CanonicalGrant` / `StoredGrantSpelling` / `LegacyGrantString` make an api reference and `api:<scope>` one grant for `IsDangerousGrant`, `GrantsSubsetOf`, `EscalatedCapabilities`, `CapabilityPolicy.Permits`, the catalogue and `ParseAPIGrant`; `NormalizeCapabilityGrants` records the `api:` spelling; the repo server gains `#target {repo, base}` and `#write {repo, base}`; a request's `grants:` list is sorted into `repo`/`reads`/`writes`/`capabilities` (`SplitRequestGrants`, `CreateTaskRequest.UnmarshalJSON`); the method grammar admits `:`; `retiredInProcessNames` binds methodless shorthand to its retired names so a catalogue rule on `in_process.github-sandbox` is not bypassed by dropping the method. #886: `CIGrantRuleFor` now matches case-varied server IDs and methodless shorthand (closing a spelling bypass of `CIGrantRuleRefusal`/`Permits`); `IsCIOnly` is fail-closed (`!TaskGrantable || IsEventGated`); `/home/debian/.github-sandbox-token` leaves `reservedPlacementPaths` (github#sandbox-repo now places it through `MintPass`, a second placer is refused by `checkConflicts`); the sandbox repository is named for the attempt, not the run. #897: `protocol.Covers` fills a method's advertised argument defaults on both sides (`NoteArgDefaults`/`withArgDefaults`) so `{permission_set: logs}` is covered by `{permission_set: logs, primary: true}`; stdio capability servers' stderr is now logged by the daemon, line by line, scrubbed of every secret value the attach resolved (`scrubbedLines`); the gcp server gains a `-fake-cloud` test flag. #907: token-mode primary grants point gcloud at the config with `auth/credential_file_override` instead of `gcloud auth login`. #915: `Hello.AgentsOnly`, and Settings refuses placing a `run: host` server on an agents-only host with better words. #925: `checkCapabilities` applies the gcp-key `account`→`permission_set` rewrite even when the set is unconfigured (migration 127). |
| Sandbox backends (`pkg/k8s`, `k8s_granule_sandboxes.go`, `pkg/granule`, `third_party/granule`) | — | Not touched in this range. `pkg/hostd` (`exec-sandbox`) was: #915 wording and `AgentsOnly`; #892 the silent credential renewal now runs on the credential's own schedule across reconnects (`renewDue`, `RenewedAt` from the token file's mtime) -- an availability fix for a 30-day credential; no change to what a host runs or admits. |
| Release and secrets gating (`pkg/ci`, `pkg/upgrade`, `scripts/release-*.sh`, `scripts/candidate-gate.sh`, `scripts/release.conf`, `.github/workflows/*`, `pkg/secrets`) | #922, #911, #941, #887, #886, #912 | #922 (itself a security review's outcome, task 3585): `RELEASE_QUALIFICATION_APP=none` is an *explicit* opt-in only -- from the environment or `scripts/release.conf` **as `main` has it** (`git show "$main_ref:scripts/release.conf"`); empty or unset still exits 64 / refuses `grain/rc-hold`. #911: CLI archives are built in a `contents: read` job and uploaded by a separate `contents: write` job in the same run, every action pinned by SHA, inputs passed through `env:` not inline. #941: every download fallback (cosign, shellcheck, actionlint, codex) is a digest-pinned container image or npm tarball with the extracted binary `sha256sum -c`'d before use. #887: `requiredReleaseAsset` (the configured single `Asset` or `images.json`) decides "published"; `retag_image --prefer-index=false` keeps the signed digest. #886: `pkg/ci/workflows.go` canonicalises with `CanonicalCIGrant`. #912: a catalogued Secret Manager key gone upstream (404 / destroyed / disabled, **only** when the body is a Google API error) no longer fails `KnownValues` (redaction) as a whole; the last value this process read is retained for redaction; any other failure still fails closed. |
| State-repo sync (`pkg/staterepo`, `pkg/repodecl`, `statecheck`) | #887, #925 | #887 adds `gitref_forbidden.go`: a merged settings change (the third way in, after `POST/PATCH /api/schedules` and `grain state check`) that enables a `git_ref` schedule watching grain's own config repository, a retired `grain/config-<team>` alias, or the encrypted secrets backup repository is refused by `applySettingsFromDirs` (`forbiddenGitRefImport`) and by `statecheck.GitRefSchedules`; `decodeRecurrence` now runs `Recurrence.Validate`. #925 as above. |
| `pkg/trustboundary` and the security hold | #885 (v22), #908 (v23) | Additive: `pkg/webauth/` → `auth-routes`; `pkg/registry/`, `cmd/grain-registry/` → `credential-proxy`; the human-review hold, director floor and `on_behalf_of.go` → `auth-routes`. `securityhold.go` unchanged. |
| Path containment (`pkg/safepath` and callers) | — | Not touched. |
| Also read (classified `credential-proxy` since v22): the registry (`pkg/registry/...`) | #883, #878, #914 | #883 brings GCE-hosted instances up live: per-instance service account, Cloud SQL user/database, Secret Manager prefix, GCS folder, `startup-script` metadata, `BindProjectIAM`/`CloudSQLAdmin` config switches, `BootDiskGB` (range-validated 20–4000). #914 builds the `grain-config` metadata `grain-instance-init.sh` boots from (`InstanceGrainConfig`). #878 adds `grain registry stop/start` (demo). See finding 2. |

## Tests run

| Suite | Result |
| --- | --- |
| `go test ./pkg/trustboundary/... ./tests/security/...` | `ok` (trustboundary 2.0s, tests/security 26.9s) |
| `go test ./pkg/ui/ ./pkg/model/ ./pkg/capability/... ./pkg/hostd/ ./pkg/secrets/ ./pkg/staterepo/... ./pkg/gitproxy/ ./pkg/registry/... ./pkg/upgrade/ ./pkg/repodecl/ ./pkg/ci/` | `ok`, all 41 packages (pkg/ui 120s, pkg/gitproxy 66s, pkg/staterepo 28s, pkg/ci 24s) |

No failures, security or environmental. `pkg/orchestrator` and `pkg/daemon`
were not run as whole packages (they take longer than this run's budget
allows beside the reading); the orchestrator changes in range
(`finish.go`, `consolesessions.go`, `systemschedule.go`) were read and
their own test files (`finish_test.go`, `hold_for_approval_test.go`,
`person_granted_proposal_internal_test.go`,
`consolesessions_busy_internal_test.go`) are in the merged PRs.

## Findings

Three, none High. One is the recurring "a file that now carries an
authorization decision is not on the trust-boundary list" shape the
last four reviews each found an instance of; one is a credential placed
where a credential should not be; one is a one-condition inconsistency
in a rail that landed today.

### 1. `pkg/model/canonical_grant.go` -- the lens every grant predicate now reads through -- is not on the trust-boundary list, so an auto-merge pull request can change what counts as a dangerous grant with no security hold -- **Medium**, reproduced

**Who can do what.** Any task with an auto-merge pull request against
`ai-on-gke/grain` can edit `pkg/model/canonical_grant.go` (new in #927,
`a85135b8`, 08:59 UTC) and merge with no security review, no hold and
no person's Submit, because `trustboundary.Classify` returns nothing
for it and `holdForTrustBoundary` (`pkg/orchestrator/securityhold.go`)
clears a pull request whose file list matches nothing.

What that file decides, as of #927:

- `CanonicalGrant` (`:74`) and `LegacyGrantString` (`:156`) /
  `StoredGrantSpelling` (`:190`): the mapping between
  `https://grain.dev/capabilities/api#<scope>[={"repo"}]` and
  `api:<scope>[@repo]`. **`ParseAPIGrant`** (`pkg/model/agent_api.go:1068`)
  now calls them first, so `APIGrantsOf` -- the scope set behind every
  agent-API authorization, `IsDangerousAPIGrant`, and
  `agentGrantRefusal`'s dangerous-scope check
  (`pkg/ui/agent_api_gate.go:1224`) -- reads a reference through this
  file. So do `IsDangerousGrant` (`template_grants.go:110`, and through it
  `IsPersonOnlyGrant`), `GrantsSubsetOf` (`task.go:629`),
  `EscalatedCapabilities` (`auto_submit.go:260`),
  `CapabilityCatalogue.judge` (`capability_catalogue.go:290`) and
  `CapabilityPolicy.Permits` (`capability_ref.go:647`), each via
  `storedGrantSpellings`/`StoredGrantSpelling`/`CanonicalGrantKey`.
- `SplitRequestGrants` (`:325`): how a request's `grants:` list becomes a
  task's `Repo`/`Base`, `Reads` and `Writes` (`CreateTaskRequest.UnmarshalJSON`,
  `pkg/ui/client.go:341`; `UpdateTaskRequest` at `:4805`).

A one-line change -- `LegacyGrantString` returning `APIGrantPrefix +
"tasks:read"` for an `api#merge:force` reference, say, or
`StoredGrantSpelling` returning `s` unchanged -- makes a dangerous scope
read as a safe one to the gate, the proposal escalation check and the
catalogue at once, and nothing holds the pull request that does it.
`pkg/model/capability_ref.go`, `capability_catalogue.go`,
`template_grants.go` and `agent_api.go`, which this file now sits in
front of, are all classified (`capabilities-secrets`, `auth-routes`).

**Reproduced.** A throwaway `pkg/trustboundary` test (not committed)
asserting `Classify([]string{"pkg/model/canonical_grant.go"})` is nil
passes at `1ab19467`, while `pkg/model/capability_ref.go` on the same
run returns `capabilities-secrets`. The same probe shows
`pkg/model/auto_submit.go` (`EscalatedCapabilities`, `AutoSubmits`) and
`pkg/model/task.go` (`GrantsSubsetOf`, `UngrantedWrites`,
`LandsWhereGranted`, `AlignDefaultBases`) unclassified too; those are
not new to this range (and `task.go` carries far more than
authorization), so they are left to the fix task's judgement rather
than counted as findings here.

Also probed, and found sound, because it is the obvious follow-on
question: can the new `grants:` input path smuggle a write target past
the gate's `writes`/`reads` body check (`filingEscalation`,
`pkg/ui/agent_api_gate.go:1796`), which switches on the JSON key and
never sees a `repo#write` entry under `grants`? A throwaway `pkg/ui`
test had task 1 (holding `api:tasks:write` on `acme/widgets`) `POST
/api/tasks` with `grants: [{"capability": ".../repo#write", "with":
{"repo": "acme/gadgets", "base": "main"}}]`, the same with `repo#read`
on `acme/secret`, `repo#target` on `acme/secret`, and `api#merge:force`
under both `grants` and `capabilities`. Every one was refused 403:
the repo references because `agentGrantRefusal` finds them uncovered by
the task's held grants (`UncoveredCapabilityRefs`), the api reference as
"a dangerous api grant". That is fail-closed, by a different rule than
the one `writes:` is held to (held-grant coverage rather than
`allowedRepo("tasks:write", repo)`), and today it refuses even a target
the parent could legitimately pass on -- a usability gap, not a security
one, noted for the fix task's regression test so the two paths are
asserted to agree.

**Severity.** Medium, as the previous four reviews rated this shape:
the hold is the one control between an agent's auto-merge and the
authorization path, and #927 moved the reading of every grant behind a
file the hold does not watch.

**Fix task filed:** `fix(security): classify pkg/model/canonical_grant.go
as trust-boundary (capabilities-secrets, v24)` -- `auto_merge: false`,
`hold_for_approval: true`, as every list change is. Add the file to
`capabilities-secrets`' `files` beside `capability_ref.go`; bump
`Version` to `trust-boundary/v24` with the usual comment; append it to
`knownBoundaryFiles`; add `TestCanonicalGrantIsClassified` with the
path, plus a `pkg/ui` regression test that a `grants:`-spelled
`repo#write`/`repo#read`/`repo#target` an agent's own task does not
hold is refused exactly as the `writes:`/`reads:`/`repo:` spelling is.

### 2. A hosted instance's Postgres password is written into GCE instance metadata (`grain-config`) and its DSN disables TLS -- **Medium**, reproduced

**Who can do what.** On a registry deployment with
`instance_sql.cloud_sql_admin: false` -- which is what the reference
test deployment `deploy/test/registry-sandbox/files/startup.sh:261`
sets, so it is the exercised path, not a corner -- `GCPInstanceResources.Provision`
(`pkg/registry/providers/gcp/resources.go:394-406`, #883) generates a
24-byte random password for the per-instance database role and returns
it in `InstanceResourceBundle.DatabasePassword`. `InstanceGrainConfig`
(`pkg/registry/reconcile/reconciler.go:1271-1301`, #914) then puts it
into the VM's startup environment **twice**: as `GRAIN_DB_PASSWORD`, and
inside `db_dsn` as
`postgres://<user>:<password>@<sql-private-ip>:5432/<db>?sslmode=disable`.
`GCECompute.CreateInstance` (`pkg/registry/providers/gcp/compute.go:493-505`)
serialises that map as the instance metadata item `grain-config`, which
`scripts/grain-instance-init.sh:132` and
`deploy/test/registry-sandbox/files/hosted-vm-startup.sh:60-71` read back
from the metadata server.

GCE instance metadata is not a secret store: anyone holding
`compute.instances.get` on the hosting project (`roles/compute.viewer`,
`roles/viewer`, every project-wide read role) reads it with `gcloud
compute instances describe`, and the `instances.insert` request that set
it is recorded in the project's Admin Activity audit log, readable by
`roles/logging.viewer`. Either reader, with network reach to the Cloud
SQL private IP (any workload in the VPC, or anyone who can start one),
has the hosted grain's whole database -- its tasks, comments, settings,
external agent token hashes, state. `sslmode=disable` means the password
and every query also cross the VPC in clear. The IAM-auth path
(`cloud_sql_admin: true`, the default) has neither problem: no password
exists and the VM authenticates as its service account through the Cloud
SQL connector. The same VM's service account already reads
`projects/<project>/secrets/<prefix>*` from Secret Manager (the init
script fetches `config_repo` credentials that way,
`grain-instance-init.sh:279`), so a secret store for this value is
already wired.

**Reproduced.** A throwaway `pkg/registry/reconcile` test (not
committed) called `InstanceGrainConfig` with a bundle whose
`DatabasePassword` was `SECRET-PW-0123` and found the string twice in
the JSON that becomes `grain-config` (as `GRAIN_DB_PASSWORD` and inside
`db_dsn`), with `db_dsn` carrying `sslmode=disable`. The merged test
`TestGrainInstanceInitBootsFromReconcilerMetadata/built-in_password_user`
(`pkg/registry/reconcile/instance_init_test.go:73-85`) asserts the same
shape as intended behaviour, so this is by design, not a slip -- which
is why it is a finding rather than a bug report.

**Severity.** Medium: it needs a GCP read role or VPC reach in the
registry's own project (an operator-controlled boundary), it is off on
the default configuration, and the tenant it exposes is one hosted
grain; but the credential is long-lived, never rotated, visible to every
project viewer, and guards a tenant's entire state.

**Fix task filed:** `fix(security): keep a hosted instance's database
password out of GCE metadata and require TLS on its DSN` --
`auto_merge: true` (`pkg/registry/` is `credential-proxy`, so the
security hold reviews it; nothing it touches is `hold-policy`). Write
the generated password as a Secret Manager version under the instance's
`SecretPrefix` (the bundle already carries it and the VM's service
account already has `secretAccessor` on that prefix via
`ensureProjectIAMBindings`), put only the secret's resource name in
`grain-config` (`db_password_secret`), have `grain-instance-init.sh` and
`hosted-vm-startup.sh` fetch it as they fetch `config_repo`'s token, and
emit the DSN with `sslmode=require` (Cloud SQL private IP accepts TLS by
default). Regression test: extend
`TestGrainInstanceInitBootsFromReconcilerMetadata/built-in_password_user`
to assert the password string does **not** appear in the
`InstanceGrainConfig` JSON and that the DSN the init script derives
carries `sslmode=require`, and add a `pkg/registry/providers/gcp` test
that `CreateInstance`'s metadata items never contain
`bundle.DatabasePassword`.

### 3. An external credential's run-now approves an unapproved no-repository task that the approve route refuses it -- **Low**, reproduced

**Who can do what.** #917 (`b6fc5cce`) made run-now's implicit approval
ask for `tasks:admin` "so the two routes agree on every unapproved task".
They agree except for a task with no repository. `externalApprovalRail`
(`pkg/ui/agent_api_gate.go:850-864`) checks

```go
if onlyIfUnapproved && !set.Allows("tasks:admin", repo) && !(repo == "" && set.Has("tasks:admin")) {
```

and `APIGrantSet.Has` is true when the scope is held on *any*
repository, while `Allows(scope, "")` is true only for a grant on every
repository. So a credential holding `dispatch:write` unlimited (which
is what lets the call reach the rail at all, since the route's own scope
check is `Allows("dispatch:write", "")`) and `tasks:admin@acme/widgets`
can `POST /api/tasks/{id}/run-now` on an unapproved task with no
repository, and the task is approved by `external:<name>` -- while `POST
/api/tasks/{id}/approve` on the same task answers 403 ("holds
tasks:admin only for acme/widgets, and … does not name a repository it
reaches"). The lineage and person-hold halves of the rail still apply,
so the task must be one nobody is holding for a person; what is approved
is work the credential's `tasks:admin` was deliberately not granted on.

**Reproduced.** A throwaway `pkg/ui` test (not committed) minted a
credential with `tasks:read, dispatch:write, tasks:admin@acme/widgets`,
stored an unapproved task `940` with `Target: nil`, and got `approve` →
403 and `run-now` → 200 with `approvedBy: external:mixed`,
`approvedBySource: external-token`, state `queued`.

**Severity.** Low: unlimited `dispatch:write` is already a strong grant,
no-repository tasks are the minority, and a person-held one is still
refused. It is a one-condition fix in a file that landed today, filed so
the two routes agree the way #917's commit message says they do.

**Fix task filed:** `fix(security): make run-now's implicit approval
need tasks:admin on the task's repository exactly as approve does` --
`auto_merge: false`, `hold_for_approval: true` (`pkg/ui/agent_api_gate.go`
is `auth-routes`). Drop the `!(repo == "" && set.Has("tasks:admin"))`
clause so the rail is `!set.Allows("tasks:admin", repo)` alone, matching
`authorizeExternalAgentCall`'s own check for the approve route.
Regression test: a case in
`TestExternalAgentRunNowApprovesOnlyUnderTasksAdmin`
(`pkg/ui/external_approval_rail_test.go`) with a repository-limited
`tasks:admin` and a `Target: nil` task, asserting `run-now` is 403 and
the task stays proposed.

## Observations (not findings)

Concrete, but with no failure mode an attacker reaches today; recorded
so the next review does not re-derive them.

- **`design/spec/capabilities/capabilities.spec.md:558` still lists
  `/home/debian/.github-sandbox-token` among the reserved placement
  paths** that #886 removed from `reservedPlacementPaths`
  (`pkg/capability/protocol/response.go:202`). Nothing in grain trusts
  that path (`grep` finds only the githubsandbox provider writing it and
  `cmd/ghcr-prune` reading its own copy), a second placer of the same
  path is still refused by `Pass.checkConflicts`, and a capability
  server may place files on any non-reserved path anyway -- so there is
  no new reach, only a spec that no longer matches the code. A docs
  follow-up, not a security task.
- **`KnownValues` after a daemon restart.** #912's retention of the
  last-read value of a secret gone upstream is per-process; a sandbox
  handed the value before a restart, still running after it, prints it
  unredacted once the key is gone from Secret Manager. The commit's own
  doc comment (`pkg/secrets/secrets.go:641-649`) says exactly this and
  accepts it; it needs a Secret Manager delete in the operator's project
  first. Noted as an accepted residual.
- **`protocol.Covers` now fills argument defaults from the method's
  advertised schema** (`NoteArgDefaults`, #897). A server that changes a
  default after a parent grant was recorded changes what that parent
  covers -- but it changes what the parent's own mint does too, and the
  server is the trust anchor for its own methods; no boundary grain
  controls moves. Fine as designed.
- **`gcp -fake-cloud <fixture>`** (#897) is a production-binary flag that
  points every admin call at an in-memory GCP. It is set only by whoever
  edits the capability server's `args` (Settings → Capability servers,
  `capabilities:admin`, or a reviewed grain/config change), which is
  operator-level already; its effect is tokens that work against
  nothing, not a wider grant.

## Examined and found sound

Read in full, with what it calls across the boundary; nothing to report.

- **#917 external approval rail** (`externalApprovalRail`,
  `externalApprovalRefusal`, `externalProposalLineage`,
  `filedByExternal`): lineage walk is bounded by
  `maxProposalLineageDepth` with a `seen` set; `filedByExternal` matches
  `Origin.Attribution.Actor` on `PrincipalAgent` + exact `external:<name>`
  (the actor, not the on-behalf-of person, so an owned credential never
  reads as its owner); the Client asks the same questions again in
  `Approve` (`client.go:6102`) and `agentMayApprove` (`:7528`) so `/mcp`
  and REST agree; `proposalPersonHolds` now reads `HoldRequested`
  (#895's `HoldRequestedTag`, set in `relayProposedTasks` whenever the
  run asked), so a `hold_for_approval` a permission would have cleared
  anyway is still a person hold for the agent rail. Finding 3 is the
  one residue.
- **#894/#926 owner cap** (`externalTokenOwnerCap`): applied in the
  single external authentication path (`authenticateExternalAgent`,
  called from `external_agents.go:791` and `server.go:258`); a failed
  group lookup that could narrow the owner refuses (403) rather than
  admits; an owner not on the allowlist is refused only while a verifier
  is active (so single-user and access-code deployments, whose owners
  were never listed, keep working); the local account (no `@`) is
  exempt. Mint-time widening by a limited role is still refused by
  `roleGrantEscalation` on `POST /api/external-agents/tokens`
  (`grantBodies`), so the cap is a second door, not the first. An owner
  named by a limited minter who is wider than the minter: the token's
  scopes were already capped at mint by the minter's role, and the
  owner principal authorizes nothing (it attributes, and finding-3's
  rail says a credential never stands in for its owner), so naming a
  Full owner escapes the cap without gaining anything.
- **#923 `OwnerSource`**: `OwnerPrincipal` downgrades the owner's
  `Source` to the unverified issuer's when `OwnerSource` is not a
  verifier's, so `Verified()` is false for an access-code-named owner;
  `CreateExternalAgentToken` clears `OwnerSource` when `Owner` is empty;
  an agent re-minting passes on its own credential's `OwnerSource`; a
  self-service mint with no verified caller is nobody's and refuses a
  named owner.
- **#926 access code behind a verifier**: `handleAccessCodeLogin`
  refuses a verified caller before reading the code (so a wrong/right
  code is not distinguishable), unless the verifier declares
  `AccessCodePrecedence`.
- **#901 `GET /cli/install.sh`**: `internal` in `routeScopes` (so no
  agent grant reaches it), `scopeSignedIn` in `humanRouteScopes`, open
  on a single-tenant daemon beside the hostd install script;
  `renderCLIInstallScript` substitutes only a release matching
  `hostdInstallReleaseRE` and a base URL matching `cliInstallURLRE`,
  with `Content-Type: text/x-shellscript`, `nosniff`, `no-store`.
  `tests/security/routes_test.go` and `single_tenant_test.go` were
  updated for it and pass.
- **#927 unified grants, the readers**: `GrantsSubsetOf`,
  `EscalatedCapabilities`, `IsDangerousGrant`, `IsPersonOnlyGrant`,
  `CapabilityCatalogue.judge`, `CapabilityPolicy.Permits`,
  `ParseAPIGrant`/`APIGrantsOf`/`IsDangerousAPIGrant`, and
  `grantAlreadyStored` all canonicalise before comparing; an api
  reference with arguments other than `{repo}` is refused by
  `ParseAPIGrant` (`ok, err`), skipped by `APIGrantsOf`, refused by
  `NormalizeCapabilityGrants`, and reads as "not held" to
  `agentGrantRefusal`. `apiGrantsOf` on the sandbox channel still drops
  `RequiresPerson` sets and `IsSandboxDeniedAPIScope` scopes whichever
  spelling arrived. The method-grammar change (`:` admitted in
  `methodPattern`) keeps `serverNamePattern` for MCP server names, so a
  reported server name cannot carry `:`. The `grants:` input path is
  fail-closed today (finding 1's probe). `model.Grant.Equal` replaces
  `==` in `consolesessions.go`/`session_sandboxes.go`, so a session
  whose grants changed still restarts.
- **#886 CI grant rules**: `ciGrantRuleLookup` matches recorded, bare,
  case-varied server IDs and methodless shorthand (the last only when
  the server declares exactly one rule-bearing method), and
  `CanonicalCIGrant` rewrites all of those to the recorded `<ref>={}`;
  `IsCIOnly` is `!TaskGrantable || IsEventGated()` -- a rule written
  before `TaskGrantable` existed stays CI-only; `ciCapabilityRefs` and
  `pkg/ci/workflows.go` use the same canonicaliser. The github sandbox
  repository is now `grain-sandbox-<attempt>` so two deployments on one
  App installation cannot collide; `release_` revokes under the mint's
  audit run id.
- **#897 capability server stderr → daemon log**: `scrubbedLines`
  replaces every value the attach's secret lookup resolved, buffered per
  line, with a 16 KiB cap on an unterminated line; `resolvedSecrets` is
  mutex-guarded because the server writes while the lookup may still be
  resolving. What it cannot scrub is a value the server minted itself
  (not a configured secret); the gcp server's `gcp: audit:` lines were
  read and name the attempt, set, account and deadline, never the token.
- **#907 token-mode activate script**: `gcloud config set
  auth/credential_file_override <path>` with the project; stderr kept so
  a failure is quoted to the task; `shellQuote` on both values.
- **#915/#892 hostd**: `AgentsOnly` is advisory (the daemon's Settings
  refuses placing a server there with better words; the host's own
  allowlist is what enforces). `renewDue` only changes *when* renewal is
  asked for; the token file's mtime is read, never its content, and a
  future mtime is clamped to now. `RefusedError` wording only.
- **#924/#895 proposal relay**: `AlignDefaultBases` respells a base only
  when exactly one side is `""` and the other equals the forge's answer
  for that repository's default branch (asked once per repository per
  batch, `""` on error leaving bases as written → withhold); the
  proposal is still filed as written, only the permission checks see the
  aligned copy; `otherBranchQueueNote` is text. `HoldRequestedTag` is
  appended whenever the run asked for `hold_for_approval`.
- **#921 system schedules**: a freshly seeded system schedule first
  fires ≥ 1h out; no change to what runs or as whom.
- **#906 hostd offers cache**: caches only *failures*, keyed by socket /
  remote session / name; a stale failure is served while one background
  probe (with `context.WithoutCancel`) refreshes; a success is never
  cached, so an allowlist edit still shows at once.
- **#889 `CredentialOwnerIdentity`**: the forge's answer for a
  credential without the host git config laid over it, used for the
  "Signed by <owner> (via Grain)" line; cache keyed by host + token as
  before; the flight map is keyed separately for owner and commit
  lookups.
- **#912 Secret Manager**: `accessGoneUpstream` accepts only a Google
  API error body whose `code` equals the HTTP status (a bare 404 from a
  proxy is still fatal); `retainGone` moves a cached value into
  `retained` (never resolved again, only redacted); `MissingUpstream`
  bypasses the cache and records no audit entry; `noteGone` logs the
  error text, which is the API message, not a value.
- **#887 state repo**: `ForbiddenGitRefWatch` and `RemoteMatchesRepo`
  compare the watched repository against the local config repo, the
  external state repo remote and the secrets backup remote read from
  `dataDir`; `applySettingsFromDirs` refuses before `planMerge` runs;
  `decodeRecurrence` validates both `git_ref` and `github_workflow_run`
  triggers; `repodecl.parseRecurrence` also refuses a retired
  `grain/config-<team>` alias. `pkg/upgrade` "published" now follows the
  configured single asset when set -- operator config, not input.
- **#925 migration 127**: `account: X` → `permission_set: X, primary:
  true`; `checkCapabilities` judges the rewritten grant even when the
  set is unconfigured, so `grain state check` and the migration agree;
  `primary: true` is the method's own default (and what
  `withArgDefaults` fills), not a widening.
- **#922/#911/#941 release machinery**: covered in the surfaces table.
  `release-from-trunk.sh` reads `release.conf` from `$main_ref`, not
  from the rc's tree; `candidate-gate.sh` with `CHECK_QUALIFICATION_APP`
  empty still exits 64; the CLI-asset build/upload split keeps
  `contents: write` out of the job that runs `go build`; every fallback
  download is digest-pinned and `sha256sum -c`'d.
- **#883 registry (beyond finding 2)**: hosted VMs get no external IP
  (`AccessConfigs: nil`); `BindProjectIAM` and `CloudSQLAdmin` default
  to true and only *narrow* when set false; `BootDiskGB` is validated in
  `RequestEntryReplaceWithDisk` (and `ValidateBootDiskGB` in `Store.CreateEntry`, `store.go:1199`);
  `startup_script` comes from the registry's own config file; the
  `deploy/test/registry-sandbox/` Terraform and scripts are a test
  deployment's, read for what they feed the registry (finding 2's
  `cloud_sql_admin: false`).
- **Unclassified pull requests** (#880 admission narrowing, #898
  hosting field, #882 closed-work conditions, #913 demo, #893, #896,
  #903, the walkthrough docs): read their file lists; all `ui/`, `pkg/cli`
  display, `pkg/demo`, or docs. #896 touches
  `person_granted_proposal_internal_test.go` only.

## Not reviewed

- `pkg/orchestrator` and `pkg/daemon` as whole test packages (see
  "Tests run"); the changed files in them were read.
- `grain-plc/pkg/grainclient/wire.go` (new, #917's branch): the PLC
  client's wire types; an isolated module outside the brief's surfaces.
- The `ui-registry` React app and Playwright specs added by #883: UI.
- Whether GCE Admin Activity audit logs record `metadata.items` values
  for `instances.insert` was not verified against a live project in
  this sandbox (no GCP access); finding 2 stands on
  `compute.instances.get` alone, which is documented behaviour.

## Package suites

`go test ./pkg/trustboundary/... ./tests/security/...` and the touched
packages listed under "Tests run": all `ok` at `1ab19467`.
