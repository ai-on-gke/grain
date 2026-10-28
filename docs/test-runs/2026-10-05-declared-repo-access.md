# Declared repository access for CI and templates; pinned-commit capability withholding (2026-10-05)

Walkthrough of `docs/test-plans/2026-10-05.md` §5, run by grain task 1996.

## Environment and tested commit

- Base: `candidate` at `795d33a1a` (`git merge-base HEAD origin/candidate`).
  `git branch -r --contains` lists `origin/candidate` for all three commits under test:
  `8abd23ef2`, `a93928fc2` and `9cb1831af`.
- Debian sandbox, Linux 6.1, go 1.26.6, git 2.39.5.
- Backend: `orchestrator.HostSandboxes`, the same host backend `ci_hostile_test.go` uses. There
  was no granule VM.

## What was run

The walkthrough is a single Go test,
`cmd/grain/ci_declared_repo_access_walkthrough_test.go`
(`TestLiveDeclaredRepoAccessAndPinnedCommitWalkthrough`). It runs in the ordinary `go` CI job and
needs only `git`. All of the grain side is the daemon's own code:

- `ciWorkflowSource` reads `.grain/workflows/access.yml` out of the repository.
- `ci.Service` plans the run and vouches for the commit.
- `ci.Engine` schedules the jobs.
- `ciSandboxes` (with `ciReadScopes`, `ciWriteScopes`, `ciSandboxTeams` and
  `Store.TeamGitCredential`) gives each job a host sandbox. It checks the job out through the real
  git proxy, `startGitProxy`, which reads the team credential from the sqlite store.
- Runs a person asks for go through the real `ui.Server` (`POST /api/ci/runs`).
- The agent's retrigger goes through the real MCP `retrigger_ci` tool, then `daemonGrainCI` and
  `ui.HTTPClient`, to the same server.
- Template declarations are read by the real `declarations` reconciler
  (`orchestrator.Reconcilers()`) through `repoDeclarationSource`. Each is read back from
  `GET /api/repos/acme/widgets/declarations`, the payload `RepoPage` renders as the declarations
  status.

Commands:

```
go test ./cmd/grain/ -run 'TestLiveDeclaredRepoAccessAndPinnedCommitWalkthrough|TestLiveAHostileWorkflow' -count=3   # ok, 45.7s
go test ./pkg/github/githubsim/ -count=1                                                                          # ok
gofmt -l cmd/grain pkg/github                                                                                     # clean
make security                                                                                                     # go vet ./... + tests/security: ok (119.7s)
```

`.grain/baselines/` is untouched.

## Stubs

| Boundary | Stub |
|---|---|
| GitHub git endpoints | `githubsim.GitUpstream` over a real `git http-backend` (`gitHTTPBackendLive`), wrapped in a request log that records the token on each request. It knows two tokens. The deployment token reads `acme/widgets`. The team token (`infra-cred`) reads and writes `acme/widgets`, `acme/lib` and `acme/other`, so every refusal below comes from grain, not GitHub. |
| `gcp-key` | `ciTestCapability{id: "test-key"}`, which places `/home/debian/.ci-key` = `job-only-key`. It is granted to `acme/widgets` in `ci_capabilities`. |
| Release secret `SIGN_KEY` | A map resolver returning `stub-sign-key` at `ci.ReleaseSecretAddress`. |
| Caller identity | A wrapper sets `ui.WithPrincipal` from a test header, as `pkg/ui`'s `doAs` does. There is no auth mocking beyond naming the human. |

Nothing in grain's own APIs was mocked.

## Outcomes

| Plan step | Outcome |
|---|---|
| 1a. Job `cross` declares `repo-read:acme/lib` (push event). | **Pass.** It cloned `acme/lib` and printed its file. Every `/acme/lib` request in githubsim's log carried `token=ghp_team_infra_walkthrough`, the team's token. |
| 1b. Team credential removed. | **Pass.** The job errored before checkout: `team "infra" has no GitHub credential for acme/widgets, so this CI job cannot reach it -- set one in Team settings, Git identity`. The step was `skipped`. The job made no request to githubsim; the only requests were the workflow source's, with the deployment token and git's anonymous first probe. |
| 1c. Job `writer` declares `repo-write:acme/lib` (`workflow_dispatch`). | **Pass.** The push to `acme/lib` landed (`refs/heads/from-ci` exists). The push to `acme/other` was refused (`remote: acme/other is not in scope for this sandbox`, 403). The push back to `acme/widgets`, the repository under test, was refused (HTTP 403). Neither undeclared repository gained the ref. **Not applicable:** "refs/heads/main where WriteScopes names a branch". `WriteScopes` is per repository by design: `ReadOnlyScopeAuthorizer.AuthorizeRefUpdates` lets a CI job holding `repo-write:X` update any ref of X, as `docs/ci-capabilities.md` says. |
| 2. Template declarations, real sync from the githubsim upstream. | **Pass.** `repo-read:acme/lib` was accepted. A task filed from it with `POST /api/tasks {"templateId": …}` had Reads `[acme/lib]` and no Writes. Its sandbox token cloned `acme/lib` through the proxy with the team token and could not clone `acme/other`. The refusals that followed each left the last good commit in place and set `declarationsStatus.error`. For `gcp-key`: `capabilities: "gcp-key": a template declares only repo-read:<owner>/<repo> and repo-write:<owner>/<repo> here, …`. For `repo-write:grain/config`: `grain/config is grain's own configuration, served from grain's disk with no team credential in front of it, …`. For `repo-read:acme/outside`: `team "infra" may not work in acme/outside (its target repos are acme/widgets, acme/lib, acme/other), so this template's capabilities cannot reach it`. **Not done:** reading these in a browser in `grain demo` (see F3). |
| 3. Control, branch tip. | **Pass.** `keyed` found `.ci-key = job-only-key`. |
| 3a. Commit pushed only to `refs/pull/1/head`, `POST /api/ci/runs` pinned to it. | **Pass.** `keyed` and `cross` both errored with `this job requires the capability "…", and this run withholds every capability and release secret: its commit <sha> is not on acme/widgets's branch main, …`. No step ran, so no capability file was placed and nothing was cloned. |
| 3b. The same commit through MCP `retrigger_ci` on task 7 (branch `grain/task-7`). | **Pass.** All four jobs errored with the same sentence (`… is not on acme/widgets's branch grain/task-7 …`), `signer` included for its release secret. |
| 4. `workflow_dispatch` by hand. | **Pass.** For the member, `signer` errored with `this job requires the release secret "SIGN_KEY", and a workflow_dispatch run started by somebody who is not one of acme/widgets's admins places no release secrets`, and `stub-sign-key` was absent. For the team admin, `signer` succeeded and found `.grain-release-secrets/SIGN_KEY = stub-sign-key`. That sentence is the job error the CI page renders. It was read through the run API, not in a browser. |

## Findings

### F1. githubsim refused GitHub's `x-access-token` form. Fixed here.

`githubsim.GitUpstream` read the token only from the Basic-auth username. GitHub also accepts
`x-access-token:<token>`, and that is how `ciWorkflowSource`'s askpass presents the deployment
credential. Every workflow read through the double was therefore refused as a revoked token.

Reproduction, before the fix: run the walkthrough, or `GET /acme/widgets.git/info/refs` with
`SetBasicAuth("x-access-token", "<known token>")`. The answer is
`401 Invalid username or token`.

Fix: `basicAuthToken` takes the password when the username is `x-access-token`. Regression test:
`TestATokenSentAsTheXAccessTokenPasswordIsTheToken` in
`pkg/github/githubsim/gitupstream_test.go`.

### F2. A read-scoped push gets an opaque refusal. Follow-up proposed.

A CI job holding only read access to a repository, here the repository under test, pushes to it.
git prints `error: RPC failed; HTTP 403 … send-pack: unexpected disconnect` and no reason. A push
to an unscoped repository (`acme/other`) gets `remote: acme/other is not in scope for this
sandbox`.

Cause: the proxy authorizes `info/refs?service=git-receive-pack` as the `info/refs` action, which
a read scope allows. The refusal therefore lands on the `POST git-receive-pack`, whose body git
does not print. The push is still refused, so this is not a security issue.

Reproduction: the `writer` job's `push-self` line in the walkthrough's output.

This touches `pkg/gitproxy`, a trust boundary, so it is proposed as a follow-up and not fixed
here.

Fixed by grain/task-2076: the proxy now authorizes the push advertisement as `git-receive-pack`,
so a read-only sandbox is refused there with `remote: acme/widgets is read-only for this sandbox:
it may fetch it but not push to it`. The walkthrough's 1c asserts that line.

### F3. `grain demo` cannot show a real declarations sync. Not fixed.

The plan asks for the refusal reasons to be read in Playwright on `grain demo`'s declarations
status. The demo seeds declarations statically (`demo.go`'s `SyncRepoDeclarations` call, and
`demo_sensitive.go`'s hand-written `RecordDeclarationFailure`). It has no git-server-backed repo
for the reconciler to read. Seeding the three sentences by hand would only test that React
renders a string.

The reasons were therefore read from the real API payload that `RepoPage` renders. The browser
step is left for a demo scenario that serves a real repository. This walkthrough files no
follow-up for it; it is noted for whoever next touches the demo's scenarios.

### Observations (by design, not bugs)

- The `signer` job declares only `secrets:`, yet it also received `.ci-key`. A job that declares
  no capabilities inherits every capability the repository grants (`jobCapabilities`), as
  `9cb1831af`'s message says. The pinned-commit rule is what keeps this from reaching a fork's
  tree.
- With no team credential at all, the 1b sentence names the repository under test (the first
  target checked), not `acme/lib`. It still names the team and Team settings.

## Fixes in this change

- `pkg/github/githubsim/gitupstream.go`: the F1 fix, with its regression test.
- `cmd/grain/ci_declared_repo_access_walkthrough_test.go`: the walkthrough above, as a standing
  test.

Neither touches `pkg/ci` or `pkg/gitproxy`.

## CI verdict

Full-suite grain CI on `grain/task-1996` at `0d624696b` (run `f96869e7acc1157b`): **success**.

| Job | Result | Time |
|---|---|---|
| `go` | success | 16m23s |
| `ui e2e` | success | 11m43s |
| `frontend` | success | 5m14s |
| `installer` | success | 3m51s |
| `package cache` | success | 1m58s |
| `postgres` | success | 1m38s |
| `terraform` | success | 35s |
| `lint` | success | 24s |

None of the known flakes recurred. The commit that adds this table only changes this file, and it
was re-verified with a second full-suite run.
