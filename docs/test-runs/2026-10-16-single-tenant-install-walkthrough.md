# 2026-10-16: fresh single-tenant install, end to end

Task 4344. A new user's walk through a wrapper distribution's own installer on a
machine with no Grain: install, find the access code, log in remotely, add a
model and a git connection, file a first task, rotate the code, re-run the
installer, uninstall.

## Setup

| | |
|---|---|
| Host | The task sandbox itself: Debian 12 (bookworm), kernel 6.1, 4 vCPU, 7.8 GiB, systemd as PID 1, Docker 29.9, `/dev/kvm` with nested virtualisation. It runs the installer faithfully, so no GCP VM was created (none to delete) |
| User | `debian`, a normal account with sudo. The installer was run as it, with no `sudo` in front, as the README says |
| Build | `main` at 1cf49bd (2026-10-16 21:34Z), built with `make build` and installed with `GRAIN_CHANNEL=main GRAIN_HOST_BINARY=$PWD/bin/grain GRAIN_HOST_SOURCE_DIR=$PWD a wrapper distribution's own installer |
| Why not the image | `ghcr.io/bwsalmon/grain/grain:main` is **stale**: it is f09c277 (built 15:04Z), from before the access code merged (393cc69, 15:26Z). Every `release images` run on `main` since then has failed in `tests/container`, so nothing newer was published. `GRAIN_CHANNEL=main` alone would have tested the build *without* access codes. The binary had to be built from the checkout |
| "Remote" access | Chromium (Playwright 1.62, headless) with `--host-resolver-rules=MAP wt4344.proxy.corp.example 172.17.0.5`. The browser sent `Host: wt4344.proxy.corp.example` (matched by the installer's `*.proxy.corp.example`) from the host's non-loopback address, so the daemon saw a non-loopback source and a proxy-style Host. There was no TLS, and no real corp-proxy in front |
| Model | Antigravity (`agy` 1.2.2, copied out of the `main` image to `/usr/local/bin/agy`) with the Gemini API key this task was given |
| Git | No `github-sandbox` capability, so a local smart-HTTP git server (`git http-backend` behind Go's `net/http/cgi`, basic auth `walk`) holding the bare repo `walk/demo` with a one-step `.grain/workflows/ci.yml` |
| Stubs | None of grain's own APIs. The only stand-ins are the git server above and the host-resolver mapping |

## Results

| # | Step | Result |
|---|---|---|
| 1 | Run the installer as a normal user | **PASS with bugs**. Installs and starts in about 3 minutes, exit 0. But the model seed fails with a 401 (`grain sync` sends no access code, B2), and the closing text contradicts itself about single mode (B4) |
| 2 | Confirm single mode | **PASS**. Log line `tenancy: single (set by -tenancy)`. Unit flags: `-tenancy single -ui-addr 0.0.0.0:4242 -ui-allowed-hosts debuerreotype,*.proxy.corp.example` |
| 3 | Find the access code; mode, owner, another user | **PASS**. The installer's summary says `grain access-code`, which prints it. `/var/lib/grain/local-api-token` is `-rw------- debian debian`, and `/var/lib/grain` is `drwxr-x--- debian debian`. A new user `mallory` gets `Permission denied` from `cat`, from `grain access-code` and from `grain list`. The hints mallory sees name a `grain` account that doesn't exist on this install (B5) |
| 4 | Loopback without the code | **PASS**. `GET /` returns 401 with the "enter access code" page, and `GET /api/tasks` returns a JSON 401, from `127.0.0.1` and with `Host: localhost` |
| 5 | Remote via a non-loopback Host | **PASS** for the access code itself, **FAIL** for the app behind it, fixed in this PR (B1). Login page (401), wrong code refused, right code in (typed lower-case with spaces), and reload keeps you in. The cookie is `grain_access`, `HttpOnly`, `SameSite=Strict`, 90 days. Nine wrong codes in a row, then `429` for that source, even with the right code, as `docs/access-code.md` says. Loopback with the right code is still let in. An unknown Host gets `421`. A Google identity header with no code still gets `401`. **But once you're in, the UI covers every page with an un-dismissable "create account" dialog**, which single mode can't satisfy |
| 6 | CLI with no extra setup | **PASS** for ordinary commands. `grain list`, `grain access-code` and `grain team …` work in a fresh shell as `debian`. **FAIL** for `grain sync`, which skips `/etc/grain/cli.env` and so sends no code (B2) |
| 7 | Model connection, models in the picker | **PASS after workarounds** (B6). The installer's advice (`GRAIN_GEMINI_API_KEY`, shown as "Gemini key: present") gives no connection a key. I had to set `agent-backend-<id>` with `grain secrets` by hand. Discovery did not fill in the model list, so I listed models by hand. Then the picker shows "Team default (gemini-3.1-pro)", gemini-3.1-pro and gemini-3-flash. `grain settings -check-agent-key antigravity`: "Google accepted the key … listed 50 model(s)" |
| 8 | Git connection, starter policy for that one repo | **PASS after workarounds** (B7, B8). `grain team git-ambient --kind git` accepted `http://172.17.0.5:8088`, but grain's git proxy always refuses plain HTTP to a non-loopback host. The run said only "502", and the reason was in `audit.log`. Moved to `127.0.0.1:8088`. The setup.sh installer has no way to pass `-policy`, so I edited it into the unit. The policy denied the other repo ("local policy does not allow repository 172.17.0.5:8088/walk/demo"), but it **also held the owner's own task** until `local_requesters: [debian]` was added. Each policy edit took a daemon restart |
| 9 | First task: run, push, CI, merge queue | **PARTIAL / blocked by the environment**. Filed in the UI (after the B1 fix). Dispatched on `agy --model gemini-3.1-pro`, cloned through the git proxy, and ran about 1m40s. Then every call failed `API_KEY_INVALID`: this task's Gemini key stopped working mid-run, and a direct `generateContent` call with it returns the same 400. So no push and no CI. The merge queue was never reachable here anyway, because a plain git host has no forge, so grain refuses PRs and the merge queue for it (`docs/git-hosts.md`). Also, the branch check on that host called the GitHub API for `walk/demo` and got a 404 (B8). Needs the human checklist |
| 10 | Rotate the access code | **PASS**. `grain access-code rotate` prints a new code of the right shape, which matches the file. The open browser session gets the 401 login page on reload. The old code is refused both as a bearer (401) and in the form. The new code signs in. `grain list` works at once with no setup |
| 11 | Re-run the installer | **PASS for tenancy, address, hosts, code and data; FAIL for the policy** (B3). Three re-runs (adding the key, a new binary, a plain re-run after rotation). Each kept `single`, `0.0.0.0:4242`, the allowed hosts, the access code (sha256 unchanged, the rotated one included), the models, the git connections and the tasks. **Each re-run silently dropped `-policy`**: after the last one, the task the policy had been holding (on the disallowed repo) **went straight to running**. The summary also names a model `workstation-antigravity` that doesn't exist |
| 12 | Uninstall as documented | **PASS with gaps** (B9). `sudo GRAIN_USER=debian ./scripts/setup.sh --uninstall` removed the units, binaries, profile script, `cli.env`, sudoers drop-in, sandbox directory, granule VMs and the grain and k8s-sandbox images, and kept `/var/lib/grain` (23 MB) as documented. It left `ghcr.io/bwsalmon/grain/guest:latest` (**8.1 GB**). `--purge` was *not* run: with `GRAIN_USER=<you>`, as the README tells a workstation user, it runs `userdel` on **your own account** (`setup.sh:1587`, no guard). The workstation section has no uninstall instructions at all |

## Bugs found

Fixed in this PR:

- **B1. Single mode's UI is unusable behind an un-dismissable "create account" dialog.**
  With the access code, `/api/config` answers `authenticated: false,
  actor: "debian", tenancy: "single"`. `App.jsx`'s `isFirstTimeUser`
  (`!authenticated && !username`) then opens `UsernameDialog`. Its
  "create account" form talks to account routes that single mode answers
  with 404, and it can't be closed (Escape does nothing), so it blocks every
  page ([06](2026-10-16-single-tenant-install-walkthrough/06-tasks-page-with-account-panel.png),
  [06b](2026-10-16-single-tenant-install-walkthrough/06b-account-dialog.png)).
  Its text also tells you to use `grain user bootstrap-admin` or to sign in from
  localhost, neither of which applies. `e2e/access-code-walkthrough.spec.js`
  only checks that `#root` renders, so it didn't catch this. The fix is to skip
  the first-time-user dialog when `config.tenancy === "single"`. After that,
  [09](2026-10-16-single-tenant-install-walkthrough/09-tasks-page-fixed.png) and
  every later step worked in the browser.

Filed as tasks (numbers in the closing comment):

- **B2. `grain sync` ignores `/etc/grain/cli.env`, so the installer's model seed fails with a 401 on every single-tenant install.**
  `cmd/grain/main.go` sets `GRAIN_DATA_DIR` from `cli.env` for the ordinary
  commands. `sync` is dispatched before that (`main.go:166`), so its
  `ui.NewHTTPClient` finds no token. `grain -server … settings` worked and
  `grain sync -server … -config …` got a 401 from the same shell. Exporting
  `GRAIN_DATA_DIR=/var/lib/grain` makes it work. The installer's warning then
  sends you to add the model in the UI, which B1 blocked. The 401's own hint
  says `sudo -u grain env GRAIN_DATA_DIR=…`, naming the wrong account (B5).
- **B3. An installer re-run silently drops `-policy`, and setup.sh has no way to set it.**
  `docs/local-policy.md` says to start the daemon with `-policy`, but neither
  setup.sh nor wrapper-installer can pass it, so you edit the unit. Every
  re-run rewrites the unit without it, with no warning. Work the policy was
  holding is then dispatched (seen here: task 2, on a repo the policy refuses,
  went to `running`). It needs a `GRAIN_POLICY` knob, and a re-run should keep
  the installed unit's `-policy` the way it keeps `-tenancy`.
- **B4. The installer's closing text gets single mode and the sandbox backend wrong.**
  setup.sh's "Done" block says "on the network: sign in with a password
  account; only localhost is trusted without one". The readiness report says
  "loopback: trusted", "accounts: everyone else signs in, so every account
  needs a password", and "single-tenant: nobody signs in; loopback and this
  host's grain CLI are the operator". All three contradict the access code that
  wrapper-installer's own summary then describes correctly. It also says
  "Installing as debian: host-mode daemon, host-directory sandboxes" while the
  readiness says "sandboxing: granule VMs". The workstation NOTE says "every run …
  executes directly on this machine, as you … under /var/lib/grain-sandbox",
  and the README says "Each run's tools execute in a Docker container". With
  `/dev/kvm` they run in granule VMs. "max workers: 1" is reported from a
  deprecated, ignored flag, the fresh `default` team has `maxWorkers: 0`, and
  the daemon logs "0 ordinary agent run(s) at once" yet dispatches. The
  `default` team is shown as "admin" in the UI. The daemon warns "started 6
  times in the last 30 minutes" after deliberate restarts and re-runs.
- **B5. CLI and server hints name a `grain` account on installs that run as the user.**
  `grain access-code` and `grain list` as another user, and the 401 hint, all
  say `sudo -u grain grain …` and "(grain owns it)". On a workstation install the
  account is the user (`debian` here) and there is no `grain` user.
- **B6. The installer's agent-key advice configures no connection.**
  "Set GRAIN_GEMINI_API_KEY … and re-run" puts it in `-gemini-api-key-file`,
  and the readiness then says "Gemini key: present". But connections use only
  their own key (task 1656), so a new connection, whether `authMode` is
  `api_key` or empty, fails with "this connection has no Gemini API key of
  its own". There is no CLI to set a connection's key: the way in was
  `grain secrets -data-dir /var/lib/grain set agent-backend-<id> value`.
  A connection created through `grain sync` with an empty model list got no
  discovered models. The daemon also logs "the default agent framework is not
  ready", and the reason is only re-read at restart.
- **B7. The local policy holds the owner's own tasks, and says so as "paused or busy".**
  A task filed in the UI with the access code has requester `debian`, the OS
  account. It is held ("approved only by its own requester, which local policy
  does not accept") unless `local_requesters` names it, and nothing tells a new
  user that name. The policy example uses `me@example.com`. The daemon log says
  `dispatch: candidate 2 skipped: paused or busy` instead of "held by local
  policy" (only the task's activity line has the reason). Policy edits need a
  daemon restart, which the doc doesn't say.
- **B8. A plain git host accepts a base URL its proxy will never use, and the branch check calls GitHub.**
  `grain team git-ambient --kind git --base-url http://172.17.0.5:8088` is
  accepted. The git proxy then refuses every request ("refusing to forward to
  … over plain HTTP: that is only allowed to a loopback address"), and the run
  reports only `The requested URL returned error: 502`. The reason is only in
  `state/git-proxy/audit.log`. After a run on `127.0.0.1:8088/walk/demo`, the
  orchestrator's branch check called the GitHub API (`cannot read walk/demo …
  GitHub API error 404`), dropping the host.
- **B9. Uninstall: `--purge` deletes the workstation user's own account, and `--uninstall` leaves 8 GB behind.**
  The README tells you to uninstall "with the same `GRAIN_USER`", which on a
  workstation is you. `--purge` then runs `userdel "$GRAIN_USER"` and `groupdel`
  on your primary group, with no check that it's a system account grain created.
  `--uninstall` left `ghcr.io/bwsalmon/grain/guest:latest` (8.1 GB), the
  container sandbox image the install pulled. The workstation section has no
  uninstall instructions.
- **B10 (noted, not filed). `ghcr.io/bwsalmon/grain/grain:main` is a day-old build without access codes.**
  `release images` on `main` has failed in `tests/container` on every commit
  since the access code merged (`TestTheCLIReachesTheDaemonFromASecondContainer`,
  `upgrade_test.go … never served`, `TestAPrivilegedPortBindsWithTheCapabilityAndNotWithout`).
  That is base-branch release triage's job, so it isn't filed again here. But
  `GRAIN_CHANNEL=main` doesn't install what `main` holds until it's fixed.

## Human checklist (Workstation only)

These need a real workstation, a the host user's own login and the access code. Run them on the
build that carries this PR's B1 fix, and after B2 and B3 land.

1. **Install as yourself:** `GRAIN_CHANNEL=main a wrapper distribution's own installer.
   Check the summary's Model line says `created` (it says "not set up" until B2
   is fixed) and that `Git` shows the depot connection.
2. **an identity-aware reverse proxy URL:** open `https://<id>.proxy.corp.example/`. Expect the
   "Enter access code" page, not 421. Enter `grain access-code`'s output, and
   check that the app loads with **no** "create account" dialog (B1). Reload,
   and check you stay signed in. Open it in a second browser profile and check
   it asks again.
3. **Wrong codes through the proxy:** about 10 wrong codes should give "too
   many wrong access codes". Check that the right code works again a minute
   later, and that the limit applies per real client (`X-Forwarded-For`), not
   to the proxy as a whole.
4. **Ambient `agy` login:** Settings, then Models: the seeded "Antigravity
   (ambient login)" connection should list models. `grain settings
   -check-agent-key antigravity` should say ok.
5. **`forgecli`/depot:** `GH_HOST=git.example.com forgecli auth status`. Then
   re-run the installer with
   `GRAIN_TARGET_REPOS=git.example.com/<you>/<scratch-repo>` and check
   it reports the git connection as created.
6. **First task through the merge queue:** file a small task on that depot
   repo in the UI. Watch it dispatch on agy, push a branch, open a PR on depot,
   pass grain CI and merge through the merge queue. This walkthrough could not
   do it, because a plain git host has no forge and this run's Gemini key was
   revoked mid-run.
7. **Policy:** write `/var/lib/grain/grain_policy.yml` with `repos:` naming
   only that repo and `local_requesters:` naming the requester your UI tasks
   get (see a task's `creator` field). Add `-policy` to the unit, and check that
   a task on another repo is held. Re-run the installer, and check `-policy` is
   still in `systemctl cat grain-daemon` (it isn't until B3 is fixed).
8. **Rotate:** `grain access-code rotate`. Check the proxy tab is signed out
   and the new code works.
9. **Uninstall:** `sudo GRAIN_USER=$USER ./scripts/setup.sh --uninstall` only.
   **Do not run `--purge` with your own user** until B9 is fixed. Check
   `docker images | grep grain`.

## Step details

### 1. Installer

a wrapper distribution's own installer (with the channel and binary variables above)
printed, in order:

```
==> Tenancy: single (the default for a fresh workstation install; export GRAIN_TENANCY=multi for a shared one)
==> UI/API address: 0.0.0.0:4242 (the default for a fresh install)
==> UI/API allowed hosts: debuerreotype,*.proxy.corp.example (this machine's own names, plus *.proxy.corp.example ...)
==> WARNING: /opt/corp/bin/releases/agy-devs/tools/cli is not an executable file on this host.
==> Installing as debian: host-mode daemon, host-directory sandboxes, UI on 0.0.0.0:4242     <- B4: it is granule
...
==> Done.
    UI:      http://debuerreotype:4242/ -- on the network: sign in with a password account;  <- B4
             only localhost is trusted without one.
...
    sandboxing:        granule VMs (one per run, over SSH as debian)
    loopback:          trusted -- only localhost ... is the operator without signing in        <- B4
    accounts:          everyone else signs in, so every account needs a password                <- B4
    sign in:           ... single-tenant: nobody signs in; loopback and this host's grain CLI are the operator  <- B4
...
==> Configuring an initial model for the default team: antigravity, ambient login, claude-opus-5-high
grain sync: applying settings: grain server: this daemon is single-tenant ... (401 Unauthorized)   <- B2
==> WARNING: configuring that model failed. ...
==> System admin: none needed -- single-tenant, so the access code is owner access
==> WARNING: forgecli is not installed for debian (go/gh-cli).
  Access    single-tenant: every visitor, localhost included, enters this
            deployment's access code. Show it with:
                grain access-code
  NOTE: every run dispatched here executes directly on this machine, as     <- B4
  you, ... in a directory under /var/lib/grain-sandbox.
```

The missing `agy`, `claude` and `codex` warnings, and the missing forgecli login,
are correct for a non-corp machine. The warning about the unverified image
publisher (`no GRAIN_IMAGE_VERIFY_KEY`) appears on every run and has no
pointer to how to set one.

### 3. The access code file

```
$ ls -l /var/lib/grain/local-api-token
-rw------- 1 debian debian 40 Oct 16 23:42 /var/lib/grain/local-api-token
$ sudo -u mallory cat /var/lib/grain/local-api-token
cat: /var/lib/grain/local-api-token: Permission denied
$ sudo -u mallory grain access-code
grain access-code: /var/lib/grain/local-api-token is readable only by the account grain runs as and root
  (grain owns it): run this as that account (sudo -u grain grain access-code) -- open ...: permission denied   <- B5
```

### 5. Remote login

| Screenshot | What it shows |
|---|---|
| [01](2026-10-16-single-tenant-install-walkthrough/01-access-code-page.png) | The 401 "Enter access code" page at `http://wt4344.proxy.corp.example:4242/` |
| [02](2026-10-16-single-tenant-install-walkthrough/02-wrong-code.png) | "That access code was not accepted." |
| [03](2026-10-16-single-tenant-install-walkthrough/03-signed-in.png), [04](2026-10-16-single-tenant-install-walkthrough/04-after-reload.png) | Signed in, and still signed in after a reload (before the B1 fix, so the dialog is up) |
| [05](2026-10-16-single-tenant-install-walkthrough/05-rate-limited.png) | "too many wrong access codes: wait a minute and try again" |
| [06](2026-10-16-single-tenant-install-walkthrough/06-tasks-page-with-account-panel.png), [06b](2026-10-16-single-tenant-install-walkthrough/06b-account-dialog.png) | B1: the "create account" dialog over the tasks page and team settings |

`curl` with a wrong bearer from 172.17.0.5:
`401 ×9, then 429 ×6`. The right code from the same source is `429`.
The right code from 127.0.0.1 is `200`.

### 7–9. Model, git, policy, first task

| Screenshot | What it shows |
|---|---|
| [08](2026-10-16-single-tenant-install-walkthrough/08-new-task-dialog.png) | The new task dialog, usable after the B1 fix |
| [09](2026-10-16-single-tenant-install-walkthrough/09-tasks-page-fixed.png) | The tasks page with no dialog |
| [10](2026-10-16-single-tenant-install-walkthrough/10-model-picker.png) | The model picker: Team default (gemini-3.1-pro), gemini-3.1-pro, gemini-3-flash |
| [11](2026-10-16-single-tenant-install-walkthrough/11-new-task-filled.png), [12](2026-10-16-single-tenant-install-walkthrough/12-task-created.png) | The first task filled in and created on `127.0.0.1:8088/walk/demo` |
| [13](2026-10-16-single-tenant-install-walkthrough/13-capacity-zero.png) | Team capacity on a fresh install: every limit 0 ("0 for the deployment limit") |
| [14](2026-10-16-single-tenant-install-walkthrough/14-task3-run-failed.png) | The run's live transcript on agy, before the key was rejected |

The connection that finally dispatched:

```json
{"id":"gemini-key","name":"Gemini (API key)","framework":"antigravity",
 "models":["gemini-3.1-pro","gemini-3-flash"],"cliPath":"/usr/local/bin/agy","team":"default"}
```

with `grain secrets -data-dir /var/lib/grain set agent-backend-gemini-key value`
fed the key on stdin. The git connection was
`grain team git-ambient --team default --host 127.0.0.1:8088 --kind git --base-url http://127.0.0.1:8088 --source git`,
with the login in `~/.git-credentials`. The policy:

```yaml
repos:
  - 127.0.0.1:8088/walk/demo
local_requesters: [debian]
```

The run's own log, in order: `task 3 commits as … the account its git
credential acts as`, `agent: running: /usr/local/bin/agy … --model
gemini-3.1-pro --effort high`, two more agy turns, then `antigravity: run
ended in status ERROR: … API key not valid` and `checking 3's branch: … cannot
read walk/demo … GitHub API error 404` (B8).

### 10. Rotation

| Screenshot | What it shows |
|---|---|
| [15](2026-10-16-single-tenant-install-walkthrough/15-after-rotate-logged-out.png) | The open session reloads to the login page after `grain access-code rotate` |
| [16](2026-10-16-single-tenant-install-walkthrough/16-new-code-signed-in.png) | Signed in again with the new code |

### 11. Re-runs

The `ExecStart` diff before and after the first re-run showed only the start
time and the PID. Across all three re-runs, `sha256sum -c` on the access code
file passed. `grep -c "policy /var/lib" /etc/systemd/system/grain-daemon.service`
was `0` after each one (B3).

### 12. Uninstall

```
==> Uninstalling grain from this host (keeping /var/lib/grain and the debian account; --purge removes both)
...
==>   kept /etc/grain: it holds files setup.sh did not write (package-cache.env)
==>   removed ghcr.io/bwsalmon/grain/grain:main
==>   removed ghcr.io/bwsalmon/grain/k8s-sandbox:sha-f09c277@sha256:…
$ docker images | grep grain
ghcr.io/bwsalmon/grain/guest:latest 8.13GB          <- B9
```

`/etc/grain/package-cache.env` belongs to the task sandbox, not to the install.
`/usr/local/bin/agy` and `/opt/antigravity` were added by hand for step 7.
