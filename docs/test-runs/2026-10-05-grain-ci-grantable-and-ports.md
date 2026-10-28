# 2026-10-05: grain-ci grantable, the k8s-granule manifest fix, and why neither was ported

Plan: `docs/test-plans/2026-10-05.md` §10 (read from `origin/candidate`, where task 1981 landed it).
Task: grain/task-2001. Base: `main`.

## Environment

- Tested commit: `main` at `d7b9d3d9b` (Merge PR #2903). `git branch -r --contains` for each
  commit under test (`6172e1205`, `5bd2827bb`, `e6b9787d4`, `8f692703e`) lists `origin/main` and
  never `origin/candidate`. All four are on `main` only.
- Sandbox: Debian 12, kernel 6.1.0-53-amd64, 4 vCPU, 7 GiB. Go 1.26.6, Node 20.19.0,
  Playwright 1.62.1 (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), Docker 29.8.2,
  kind v0.32.0, kubectl 1.35. `/dev/kvm` is present.
- UI: `cd ui && npm ci && npm run build`, so the production bundle in `pkg/ui/static` is embedded
  in the `grain` binary.
- Server: `grain demo -addr 127.0.0.1:0 -open=false -fake-ci` (real `pkg/ui.Server` on its
  embedded SQLite store), driven by the CLI with `GRAIN_SERVER=http://127.0.0.1:<port>` and by
  Playwright through `ui/e2e/fixtures.js`.

## Stubs

- **grain CI runner:** `-fake-ci` (`cmd/grain/demo_fakeci.go`). It sits at `ui.Config.CI`,
  records runs, and never executes one. The routes, the RBAC and the `ciEnabledFor` gate in front
  of it are the real server.
- **GitHub:** the demo's own fake repository `acme/widgets`. No GitHub API was reached.
- **Nothing else was stubbed.** grain's APIs and React are not mocked.

## 1. grain-ci in demo, as a system admin

| Step | Command / action | Outcome |
| --- | --- | --- |
| 1a | Repo page `acme/widgets` → Default capabilities → pick "grain CI" (Playwright, signed in as `sysadmin`, one of demo's `AdminAccounts`) | Accepted. No "unknown capability" text and no error alert. `GET /api/repos/acme/widgets/capabilities` shows `defaultCapabilities: ["grain-ci"]`. **PASS** |
| 1b | `grain repo capabilities acme/widgets -set grain-ci` | exit 0, `own defaults: grain-ci`. **PASS** |
| 1c | `grain settings -default-capabilities self-debug,gcp-key,gemini-key,grain-ci` | exit 0. **PASS** |
| 1d | `grain create -repo acme/widgets -title … -capability grain-ci` | exit 0, task 13 filed, **but task 13 holds `capabilities: []`**. See finding A. |
| 1e | `POST /api/ci/runs {"repo":"acme/widgets","ref":"main"}` with grain-ci on the repo and on the deployment | **403** `grain CI is not enabled for acme/widgets: attach the "grain-ci" capability to this repo (Settings, Repos) or to this deployment's default capabilities`. The refusal tells the operator to do what they have already done. **Finding A.** |
| 1f | Repo page → CI Mode → "Use Grain CI" (`PUT /api/repos/acme/widgets/ci-mode {"ciMode":"grain"}`) | `ciMode: grain`, `ignoreGitHubCI: true` |
| 1g | `POST /api/ci/runs` again | **202**, run `fake-ci-1` for `acme/widgets` `main`, listed by `GET /api/ci/runs` (`enabled: true`). **PASS** |
| 1h | `grain create … -capability grain-ci`, now with CI mode grain | task 14 holds `["grain-ci"]` |

So `6172e1205` does what it says: all four entry points accept the name. But accepting it turns
nothing on. Since `57939261f` ("make grain ci capability implicit based on repository CI mode",
09-24), the switch is the repo's CI mode:

- `ui.Client.ciEnabledFor` reads `model.RepoUsesGrainCI` (config repo, CI mode, legacy
  `IgnoreGitHubCI`) and recorded workflows, never the capability;
- `model.adjustGrainCIGrant` grants grain-ci to tasks on a grain-CI repo and strips it from tasks
  on any other repo, which is why task 13 lost the grant it asked for.

`6172e1205`'s message says "Verified on a real deployment: … a run of the repository's own
.grain/workflows starts". That is consistent with a deployment whose repository already had
`ignore_github_ci` / CI mode grain set, as `bwsalmon/grain` does. There the run would have
started with or without the capability.

### Finding A: the CI refusal, the daemon's `-ci` help and its startup log name the wrong switch

**Reproduction (deterministic):**

```sh
go build -o /tmp/grain ./cmd/grain
/tmp/grain demo -addr 127.0.0.1:0 -open=false -fake-ci &   # note the port
export GRAIN_SERVER=http://127.0.0.1:<port>
/tmp/grain repo capabilities acme/widgets -set grain-ci
/tmp/grain settings -default-capabilities self-debug,gcp-key,gemini-key,grain-ci
curl -s -X POST $GRAIN_SERVER/api/ci/runs -H 'content-type: application/json' \
  -d '{"repo":"acme/widgets","ref":"main"}'
# => 403 ... attach the "grain-ci" capability to this repo ... (it is attached)
```

The same wrong advice appeared in four more places:

- the release-run refusal (`pkg/ui/repo_release_workflows.go`);
- the daemon's `-ci` flag help;
- the daemon's startup log line `grain CI is on for repos carrying the "grain-ci" capability (Settings -> Repos)`,
  which is the line `6172e1205` itself cites as its motivation;
- the comment `6172e1205` added beside the `OfferedCapabilities` entry.

**Fixed here (scoped, message and comment only, no behaviour change):**

- `pkg/ui/ci.go`: there is now one refusal, `ciNotEnabledMessage`, used by both the CI API and
  release runs. It names the repo page's CI Mode, "Use Grain CI", and says the grain-ci capability
  does not turn grain CI on by itself. `ciEnabledFor`'s doc comment now describes what it actually
  reads.
- `cmd/grain/daemon.go`: the `-ci` flag help and the startup log say the same.
- `pkg/ui/labels.go`: the grain-ci entry's comment says it is accepted everywhere but is not the
  switch.
- Regression tests:
  - `pkg/ui/ci_test.go` `TestStartCIRunRefusalDoesNotSendAnOperatorBackToTheCapability` attaches
    grain-ci to the repo and to the deployment, asserts the 403 no longer says "attach the" and does
    name the CI Mode, then sets the mode and asserts 202. It fails on the old code with the old
    sentence.
  - `ui/e2e/grain_ci_grantable_walkthrough.spec.js` is the Playwright walkthrough of steps 1a, 1e,
    1f and 1g, as `sysadmin` against `grain demo -fake-ci`. It passed 3 of 3 runs, and fails against
    the pre-fix server with
    `Expected substring: "Use Grain CI" / Received string: "grain CI is not enabled …attach the \"grain-ci\" capability…"`.
  - The `TestStartCIRunRefusesARepoGrainCIIsNotEnabledFor` assertion that the refusal names
    "grain-ci" (which encoded the bug) now asserts it names "Use Grain CI". The `CIPage.test.jsx`
    fixture string follows the new wording.

**Left for a human (proposed as a follow-up):** the design question underneath. Either grain-ci
goes back out of `OfferedCapabilities`, as `57939261f` deliberately did, or the capability becomes
an enabling switch again (`ciEnabledFor` / `adjustGrainCIGrant` reading it). Until one of those
lands, `grain create -capability grain-ci` on a github-mode repo silently drops the grant, and the
repo page's CI Mode caption ("if {repo}'s tasks do not hold the grain-ci capability, turning this
on means {repo} merges nothing") describes the pre-`57939261f` model. The
`pkg/capability/grainci` package doc's "three granularities" are also stale.

## 2. Port check: why neither fix reached `candidate`

| PR | Branch | Commit | Merged (UTC) | Merged by |
| --- | --- | --- | --- | --- |
| #2899 | `grain/grainci-capability` | `6172e1205` | 2026-10-05 00:48:10 | by hand on GitHub (merge author Eric Brewer) |
| #2908 | `grain/k8s-granule-upgrade-image` | `5bd2827bb` | 2026-10-05 00:48:22 | by hand on GitHub (Eric Brewer) |
| #2888 | `grain/granule-resync-58971c6` | `e6b9787d4`, `8f692703e` | 2026-10-05 00:48:56 | by hand on GitHub (Eric Brewer) |
| #2914 | `grain/platform-verification-record` | `cf0a784c4` | 2026-10-05 00:48:26 | by hand on GitHub (Eric Brewer) |

Grain's merge queue merges as `bwsalmon` from `grain/task-N` branches (for example #2903,
`d7b9d3d9b`). These four were pushed and merged outside grain, within 46 seconds of each other.
No grain task tracked these PRs, so there are no merge-queue notes to read. The per-merge port path
(`pkg/orchestrator/sync.go` → `filePorts`, `port.go`) runs from `syncEntry` only for a task's own
pull request when the queue sees it merge. `docs/release-candidate-management.md` §5 scopes it the
same way: "When the merge queue lands a task's pull request". A PR no task owns is never seen by
it. That is by design, not a bug in the port path, so no code fix is made.

The designed safety net worked. The nightly `backport-triage` run (grain CI run
`1a53b40d82fa30cf`, `main` at `ad55be4`, after the merges) printed all five commits:

```
PORT e6b9787d4… main -> candidate: chore(granule): resync third_party/granule to upstream f80f4cf
PORT 8f692703e… main -> candidate: docs(granule): record the host verification of the resync, …
PORT 6172e1205… main -> candidate: fix(ui): make grain-ci grantable, …
PORT 5bd2827bb… main -> candidate: fix(k8s): stop grain-granule.yaml passing a flag its own daemon refuses
PORT cf0a784c4… main -> candidate: docs(init): record which platforms have actually been run, and when
main -> candidate: 5 missing, 0 skipped
```

Its review task is the one that files ports from those lines. No branch on `origin` carries a
`Backported-from:` or `cherry picked from` footer for any of these commits. They also landed after
the 10-04 18:00 cut, so the 10-05 18:00 cut fast-forwards `candidate` from `main` and carries them
regardless.

As the plan directs, ports were filed with `propose_task(branch: "candidate")` for `6172e1205`
and `5bd2827bb`, one per PR, each citing its commit. Each one says to close it as a duplicate if
the triage review task's port or the 18:00 cut gets there first. This sandbox has no task-list
tool, so open tasks could not be checked for an existing port. Nothing was cherry-picked by hand,
and `main` was not merged into `candidate`. This task's own fix lands through the merge queue on
a task branch, so the port path files its port to `candidate` automatically.

## 3. `go test ./tests/deploy/ -run Granule`, and the daemon pod on kind

`go test ./tests/deploy/ -run Granule -count=1 -v`: **PASS**, all 14 tests, including
`TestTheKubernetesGranuleManifestPassesNoFlagTheDaemonDoesNotDefine` and
`TestTheKubernetesGranuleManifestSelectsTheGranuleBackend`.

kind and `/dev/kvm` were available, so the daemon pod was started for real:

```sh
kind create cluster --name granule-check
# image under test: ghcr.io/bwsalmon/grain/grain:main (built from 18c7c6d2b, older than the
# tested commit) with /usr/local/bin/grain replaced by `CGO_ENABLED=0 go build ./cmd/grain` from
# the tested tree, so the daemon in the pod is the one under test
kind load docker-image grain-under-test:031e447 --name granule-check
# CHANGEME values filled in (-k8s-granule-image=ghcr.io/bwsalmon/grain/guest:main,
# -default-target-repo=acme/widgets), image swapped, imagePullPolicy Never
kubectl create namespace grain
kubectl apply -f fixed.yaml            # deploy/kubernetes/grain-granule.yaml at the tested commit
kubectl -n grain rollout status deploy/grain   # => successfully rolled out, 1/1 Running, 0 restarts
kubectl apply -f broken.yaml           # the same file at 5bd2827bb^ (passes -upgrade-image)
# => CrashLoopBackOff, 3 restarts in 45s, log:
#    grain daemon: -upgrade-image is not supported yet with -k8s-granule-sandboxes: …
kubectl apply -f fixed.yaml            # => rolls out again, 1/1 Running
kind delete cluster --name granule-check
```

The fixed manifest's daemon pod starts and passes its `/api/config` readiness probe. Its log
says `upgrading from the UI is off -- neither -upgrade-image nor -upgrade-src-dir is set`. The
pre-fix manifest reproduces the CrashLoop exactly. This covers the daemon pod only. No sandbox pod
(privileged, `/dev/kvm`) was started, because that needs a packed guest image and a model
connection, which are outside §10.

The granule resync (`e6b9787d4`, `8f692703e`) is covered by the Granule tests above and the
`go` CI job. No separate run was needed.

## Findings

| # | Finding | Reproduction | Disposition |
| --- | --- | --- | --- |
| A | The CI API and release-run refusals, the daemon's `-ci` help and its startup log tell operators to attach `grain-ci`. Since `57939261f` that turns nothing on. The switch is the repo's CI mode. | §1 above; `TestStartCIRunRefusalDoesNotSendAnOperatorBackToTheCapability`; `ui/e2e/grain_ci_grantable_walkthrough.spec.js` | **Fixed here** |
| B | `grain create -capability grain-ci` on a github-mode repo is accepted and silently drops the grant (task 13: `capabilities: []`). The picker offers a capability that does nothing on its own. | §1 step 1d | **Follow-up proposed** (design: drop it from the picker again, or make it a switch again) |
| C | `6172e1205` and `5bd2827bb` have no port on `candidate`. They were merged by hand outside grain, so the task-scoped port path never saw them. Working as designed. Nightly triage listed them. | §2; `git log -1 --format=%an 7974c9ff1 c488b0a44` | **Ports proposed** to `candidate` |

No recurrence of the known flakes was seen locally.

## CI verdict

Grain CI full suite (`wait_for_checks` without `jobs`), run `d65d0fb50372928b` on
`grain/task-2001` at `e5554f8`: **success**.

| Job | Result |
| --- | --- |
| frontend | success, 5m25s |
| go | success, 17m27s |
| installer | success, 3m59s |
| lint | success, 24s |
| package cache | success, 2m0s |
| postgres | success, 1m37s |
| terraform | success, 47s |
| ui e2e | success, 11m40s, including `grain_ci_grantable_walkthrough.spec.js` |

No known flake recurred. The commit that adds this table only changes this report.
