# Security review: merges to `main`, 2026-11-11 14:02 → 16:27 UTC

- **Date:** 2026-11-11 (UTC); second review of the day, following
  `2026-11-11.md`
- **Auditor:** grain task 4506 (agent), the nightly review of the day's
  merges, in the shape of task 907's 2.4 audit
  (`2026-10-25-2.4-audit.md`)
- **Base audited:** `origin/main` at `39c2bfbed` (merge of #1248,
  2026-11-11 16:27 UTC)
- **Range:** `87a5e7638..39c2bfbed` on `main`'s first-parent line: the
  **6** pull requests merged since the most recent report's base
  `87a5e7638` (`2026-11-11.md`, merge of #1220, 2026-11-11 14:02 UTC).
  Merge times below are UTC.
- **Result:** **no trust-boundary surface touched; no findings; no fix
  task filed.** Every merge in the range changes files under `docs/`
  only.

## Commits reviewed

Every merge to `main` in the range, newest first, with the files it
changes. None of them touches Go source, a workflow, a script, a
`.grain/` declaration or a baseline.

| Merge | Merged (UTC) | PR | Title | Files |
|---|---|---|---|---|
| `39c2bfbed` | 16:27 | #1248 | docs: retired upgrade flags are ignored at start, not fatal | `docs/release-assets.md` (+5 −2) |
| `b3d05d92c` | 16:10 | #1247 | docs(releases): add the release notes for v3.5.0 | `docs/releases/v3.5.0.md` (+134) |
| `bff615b79` | 15:26 | #1242 | docs(security): nightly review of merges to main, 2026-11-10 07:26 to 2026-11-11 14:02 UTC | `docs/security/audits/2026-11-11.md` (+428) |
| `5bc8d60d7` | 15:24 | #1241 | docs(releases): add the release notes for v3.3.0 | `docs/releases/v3.3.0.md` (+240) |
| `773b7bc62` | 15:20 | #1240 | docs(releases): add the release notes for v3.4.0 | `docs/releases/v3.4.0.md` (+36) |
| `08de61a5e` | 15:17 | #1239 | docs: say repo-declared firings with person-only grants wait for acceptance | `docs/access-code.md`, `docs/declarations.md`, `docs/release-assets.md`, `docs/schedules.md` (+47 −10) |

## Trust-boundary surfaces touched

None. Checked against every surface in the brief:

| Surface | Paths | Touched in range |
|---|---|---|
| Agent API scopes and route authorization | `pkg/ui/agent_api*.go`, `pkg/ui/route_scopes.go`, `.grain/baselines/route-authorization.txt` | no |
| Capability grants and placements | `pkg/capability/...`, `pkg/orchestrator/run.go` (`applyPlacements`), `pkg/mcp/ssh_placements.go` | no |
| Sandbox backends | `pkg/k8s`, `pkg/orchestrator/k8s_granule_sandboxes.go`, `pkg/granule`, `third_party/granule` | no |
| Release and secrets gating | `pkg/ci`, `pkg/ui/ci.go`, `pkg/ui/repo_release_workflows.go`, `pkg/orchestrator/basebranch*.go` | no |
| State-repo sync | `pkg/staterepo`, `pkg/orchestrator/sync.go` | no |
| Trust-boundary classification and the security hold | `pkg/trustboundary`, `pkg/orchestrator/securityhold.go` | no |
| Path containment | `pkg/safepath` and callers | no |

`git diff --stat 87a5e7638..39c2bfbed` lists ten files, all under
`docs/`. Documentation does not sit on a trust boundary: nothing in
`docs/` is read by the daemon, a sandbox, a workflow or a capability
server at run time, so no change here can alter who may do what.

## Findings

None.

## Examined and found sound

Because the range is docs-only, the review's work was to confirm that
nothing in it is executable or configuration in disguise, and that the
one change describing authorization semantics does not misstate the
code it documents.

- **#1239 (`docs/declarations.md`, `docs/schedules.md`,
  `docs/access-code.md`).** Describes the hold on firings of
  repository-declared schedules and templates whose composition carries
  something only a person may grant, naming
  `model.DeclaredGrantNeedsPerson` and `model.DeclaredFiringHold`, and
  renames the hub assertion header in the access-code page. Both
  functions exist on `main` at the audited commit
  (`pkg/model/template_grants.go:192` and `:230`), and
  `X-Grain-Hub-Assertion` is the header `pkg/hub/signer.go` and
  `pkg/hub/api/connect.go` use, with the older
  `X-Grain-Registry-Assertion` name still accepted as the page says. The
  page does not loosen anything: it documents that a declaration can only
  *ask* for a person-only grant and that acceptance takes
  `capabilities:admin` (capability server grant) or the scope itself
  (`api:` grant), which matches the earlier reports' reading of that
  code (`2026-11-10-task-grant-escalation.md`, `2026-11-11.md`). The
  behaviour itself was reviewed when it merged, not in this range.
- **#1240, #1241, #1247 (`docs/releases/v3.{3,4,5}.0.md`).** Release
  notes; prose and PR references only. `docs/releases/` is exempt from
  the hygiene token scan, so each was read for anything that looks like
  a credential, an internal hostname or a deployment address rather than
  release prose. Nothing of the kind.
- **#1248 (`docs/release-assets.md`).** Corrects the page to say a
  daemon unit still passing a retired `-upgrade-*` flag starts with a
  warning rather than failing. That is what
  `pkg/cli/daemon_unknown_flags.go` does (the `WARNING: starting anyway,
  ignoring ... flag(s)` line at `:193`); the behaviour merged earlier and
  was not changed here. Ignoring an unknown flag is a startup-robustness
  choice, not an authorization one: no flag that gates access is among
  the retired ones, and the page does not say otherwise.
- **#1242 (`docs/security/audits/2026-11-11.md`).** The previous
  review's own report. Its finding (the brief / compact / rollover
  routes in `pkg/ui/agent_cross_task.go`) and the fix task it names are
  tracked there; this review did not re-open that code because nothing
  in this range touches it.
- No merge in the range adds or changes a file under `.github/`,
  `.grain/`, `scripts/`, `Makefile`, `go.mod`, or any Go package, so
  neither the CI gate, the release train, nor any trigger classification
  moved.

## Tests run

All in the sandbox (Go 1.26.10, linux/amd64) on the tree at `b3d05d92c`;
#1248 merged while they ran and adds only `docs/release-assets.md`,
which none of these suites reads except the hygiene scan, re-run on the
final tree with this report staged:

| Suite | Result | Note |
|---|---|---|
| `go test ./pkg/trustboundary/...` | ok (1.7s) | |
| `go test ./tests/security/...` | ok (78.0s) | includes `TestRouteAuthorizationMatrix` |
| `go test ./tests/hygiene/...` | ok (23.2s) | the token scan that covers `docs/` and this report; the only suite whose inputs the range's files are |

No failures, so none to classify as security or environmental.

## Fix tasks

None filed: there is no finding. No existing task was looked up for
deduplication for the same reason (and this task holds no `tasks:read`,
as the previous report also notes).

## For the next review

Start from `39c2bfbed` (merge of #1248). Anything merged after it is
unreviewed.
