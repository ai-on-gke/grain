# 2026-10-18: qualifying single-tenant mode on the release candidate

Task 4623. This repeats the
[10-16 single-tenant walkthrough](2026-10-16-single-tenant-install-walkthrough.md)
against `candidate`, to decide whether its head can be promoted as the first
release since v1.9.0 with single-tenant mode working end to end. The owner
chose to release without per-model pauses (#4607) and the repo picker (#4582):
neither is on `candidate`.

## Setup

| | |
|---|---|
| Build | `candidate` at **8193acd92** (2026-10-17 23:05Z), built with `make build`. `ghcr.io/bwsalmon/grain/grain:candidate`, pulled by the installer, says it was built from the same commit |
| Not yet on candidate | #4132 (CI job checkout with the team's PAT; its port, task 4649, was in its last merge-queue repair). Tested separately on top of candidate: see step 7 |
| Host | The task sandbox: Debian 12, systemd as PID 1, Docker, `/dev/kvm` (nested), 4 vCPU, 7.8 GiB. User `debian`, installer run without `sudo` |
| Install | `GRAIN_CHANNEL=candidate GRAIN_HOST_BINARY=$PWD/bin/grain GRAIN_HOST_SOURCE_DIR=$PWD ./scripts/cloudtop-install.sh` |
| "Remote" browser | Debian Chromium 155 driven by Playwright, `--host-resolver-rules=MAP wt4623.proxy.corp.example 172.17.0.6`, so the daemon saw a non-loopback source and a `*.proxy.corp.example` Host over plain http. Steps 6 and 8 also used `http://localhost:4242` (the installer's ssh-tunnel path) |
| Stubs | **forgecli**: a shell script at `/usr/local/bin/forgecli` that logs its calls, prints a fake token for `auth token` and answers `api` with canned GitHub JSON (`walker`; `walk-org/service-a`, `walk-org/service-b`). **github.com PAT**: a fake token, so GitHub answers 401. **agy** 1.2.2: copied out of the candidate image (`/opt/antigravity/antigravity`). Nothing of grain's own was stubbed |
| Model key | The Gemini API key this task was given |

## Results

| # | Step | Result |
|---|---|---|
| 1 | Installer, fresh | **PASS**. Exit 0 in about 3 minutes. `Tenancy: single`, `0.0.0.0:4242`, allowed hosts `debuerreotype,*.proxy.corp.example`. The closing text and readiness now describe the access code correctly (10-16's B4 fixed), and the model seed no longer gets a 401 (B2 fixed) |
| 2 | Single tenancy | **PASS**. Unit `-tenancy single`, log `tenancy: single (set by -tenancy; kept in /var/lib/grain/tenancy)`, `/api/config` `"tenancy":"single"` |
| 3 | Access-code sign-in, remote | **PASS**. `GET /` returns 401 with a page holding one masked `code` field and no username field ([01](2026-10-18-single-tenant-candidate-qualification/01-access-code-page.png)). A wrong code is refused ([02](2026-10-18-single-tenant-candidate-qualification/02-wrong-code.png)). The right code, typed lower-case with spaces, lands on `/t/default/tasks` with no dialog ([03](2026-10-18-single-tenant-candidate-qualification/03-signed-in.png)), and still does after a reload ([04](2026-10-18-single-tenant-candidate-qualification/04-after-reload.png)). Loopback without the code returns 401 too. **Minor (filed)**: the sidebar still offers "Set username", which opens a username field and a change-password form ([05](2026-10-18-single-tenant-candidate-qualification/05-set-username-click.png)) |
| 4 | Tenancy across an installer re-run | **PASS**. Two re-runs (adding `GRAIN_GEMINI_API_KEY`, then `GRAIN_AGY_PATH`): `Tenancy: keeping single from the installed …grain-daemon.service`, unit still `single`, access-code sha256 unchanged |
| 5 | Tenancy across an upgrade | **PASS**. Uninstalled, removed `/var/lib/grain`, installed **v1.13.0-rc.1** (built from the tag), added a repo, then re-ran the installer with the candidate binary. `keeping single`, the daemon now pins it in `/var/lib/grain/tenancy` (rc.13 did not write that file), the access code is unchanged, and the repo is still there |
| 6 | Git connections: github.com PAT and forgecli | **PASS**. Team settings → Git identity → Add connection. github.com / GitHub / PAT saved ([11 form](2026-10-18-single-tenant-candidate-qualification/11-github-pat-form.png), [11 added](2026-10-18-single-tenant-candidate-qualification/11-github-pat-added.png)). `depot.code.corp.example` / GitHub / ambient `forgecli` saved, and shows "pushes and API calls act as walker" ([12](2026-10-18-single-tenant-candidate-qualification/12-forgecli-added.png)): the daemon ran `GH_HOST=depot.code.corp.example forgecli api … user` itself (server-side forgecli, #4596/#4609) |
| 7 | Repos, full names | **PASS**. The Repos page picker lists `depot.code.corp.example/walk-org/service-a` and `…/service-b`, discovered through `forgecli api user/repos`, and says "2 repos discovered from 1 git connection". The github.com connection's 401 is shown with the credential's name ([20](2026-10-18-single-tenant-candidate-qualification/20-repo-picker-open.png)). Added the host-qualified repo and `bwsalmon/grain-walkthrough`; both show their full names ([21](2026-10-18-single-tenant-candidate-qualification/21-added-host-qualified.png), [22](2026-10-18-single-tenant-candidate-qualification/22-added-github-repo.png)) |
| 8 | Task: clone → push → PR → grain CI → merge queue | **PASS (e2e harness)**. A live github.com or depot repo was not available to this sandbox (no `github-sandbox` capability, no corp network). `TestStandaloneTaskImplementsTestsAndLandsAChange` covers this path with a real single-tenant daemon set up by `grain init workstation`, a custom agent, and githubsim as the forge. The task is filed and approved, the agent pushes through the git proxy, a PR opens, grain CI runs the repo's own workflow, and the merge queue lands it on `main`. It passed on candidate (33s) and on candidate plus the #4132 port (33s). `TestGitHostsPhase5EndToEnd` (multiple hosts, per-host credentials, host-qualified repos, cross-host refusal) passed. The port's own CI-credential tests (`pkg/gitproxy` CI/forgecli/depot, the five `TestCi*`/`TestBuildCIService*` in `pkg/daemon`) passed. **A live run against github.com and depot remains on the human checklist below** |
| 9 | Model connection discovery | **PASS, with bugs (filed)**. With `agy` present, a new Antigravity connection with the Gemini API key discovered and advertised 11 models (gemini-3.8/3.7/3.6-flash, gemini-3.1-pro, at each effort) ([32](2026-10-18-single-tenant-candidate-qualification/32-gemini-connection-discovered.png), [33](2026-10-18-single-tenant-candidate-qualification/33-models-after-gemini.png)). Bugs: **(a)** the fresh install's team default is `gemini-3.1-pro`, which the seeded connection (`claude-opus-5-high` only) does not serve, so tasks on the default model wait ([30](2026-10-18-single-tenant-candidate-qualification/30-models-tab.png)); **(b)** over the installer's plain-http URL, **New connection** throws `crypto.randomUUID is not a function` and does nothing ([31](2026-10-18-single-tenant-candidate-qualification/31-new-connection-over-plain-http.png)); **(c)** abandoned drafts are saved as active, keyless connections |
| 10 | Per-model pause | **WAIVED by the owner**. #4607 is on `main` only |

## Bugs found (filed as tasks from task 4623)

1. **Fresh install's default model isn't served by the seeded connection**, so first tasks wait. A re-run given `GRAIN_GEMINI_API_KEY` adds no connection either. *Release-relevant.*
2. **"New connection" (models) does nothing over plain http** (`crypto.randomUUID` needs a secure context). Workaround: the https proxy URL or an ssh tunnel to localhost. *Release-relevant.*
3. Single mode still shows "Set username" and a change-password form, and abandoned model-connection drafts persist as active connections. *Minor.*

## Human checklist (Cloudtop only)

What this sandbox couldn't reach:

- [ ] On a real Cloudtop, `https://<id>.proxy.corp.example/`: sign in with the access code.
- [ ] Add a real fine-grained github.com PAT and real `forgecli` for depot; both repos appear in the picker with full names.
- [ ] File a task against a github.com repo and one against a `depot.code.corp.example/...` repo: clone, push, PR opened, grain CI jobs check out with the team's connection (#4132), merge queue merges.

## Outcome (2026-10-18 05:30Z)

- The #4132 port (task 4649) merged to `candidate` as **82945c6**, the commit
  qualified above plus that port and one test-only commit. Its
  `sha-82945c6` images were published (grain CI run a164ce3d3b88a094). It is
  the commit filed for promotion as **v1.13.0**.
- The owner waived the remaining bugs ("cut a release at the easiest spot. we
  do not need all of the bug fixes") and the `nightly` gate input. No
  scheduled workflow has fired on this host since 10-14, so the cut runs with
  `CANDIDATE_GATE=off`.
- Fixed on `main` since this walkthrough, so they ship in the next release
  rather than v1.13.0: New connection over plain http (3a33d7fb2), and
  single-mode account controls and abandoned connection drafts (041d52a17).
  The fresh-install default model is still open.
