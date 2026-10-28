# 2026-10-15 walkthrough: install and first sign-in (port 4242, admin user, channels, CLI timeouts)

Plan: `docs/test-plans/2026-10-15.md` §5. Task 3948 (filed by the nightly test-plan task 3749).

## Commits under test

Every one is on both `origin/candidate` and `origin/main` (`git branch -r --contains <sha>`):
`cbda0719d`, `9f7b35185`, `ee19e6964`, `e2e05e675`, `705d77285`, `7dd44b4c7`, `ac735ab44`,
`8c2ab162c`, `4511f14be`, `0a82c4cda`, `7912eda5c`, `7a71d2656`, `da0a34cac`, `26169101c`.
The tree walked was `origin/candidate` at `591f0f35f`, plus the fixes below as they landed.

## Environment

- Sandbox VM: Debian GNU/Linux 12 (bookworm), kernel 6.1.0-53-amd64, 4 vCPU, 8 GB RAM, `/dev/kvm` present.
- **systemd 252 was running as PID 1** (`systemctl is-system-running` = `running`), so `grain init` ran
  for real against the host's own systemd. The tests/installer harness and its container were not needed
  and were not used.
- Docker 29.9.0 (docker-ce), Go 1.26.6/1.26.9, Node 20.19.0, Playwright Chromium headless shell 151
  (downloaded with `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`, because the
  package-cache proxy re-signs the CDN).
- `grain` built from the tree (`go build ./cmd/grain`; `grain init` then ran `make build` itself, so the
  binary it installed embeds the built frontend).
- `grain init` ran with `-platform linux-container`. linux-kvm would have built the granule CLI and three
  guest images, which this walkthrough does not exercise. Every step this plan asks about (address, unit,
  cli.env, admin, password file, re-run) runs the same way on both host platforms. The claude CLI install
  was skipped (`-install-claude-cli=false`), which is what exposed finding 1.
- No real deployment was touched. Everything ran on the sandbox: `/etc/systemd/system/grain-daemon.service`,
  data dir `/var/lib/grain-walk`, and throwaway `grain daemon` and `grain demo` processes.

## Stubs

| Boundary | Stub |
| --- | --- |
| OCI registry for `-upgrade-image` | `localhost:5000`, a 40-line Python `http.server`. It answers `/v2/` and `/v2/x/grain/tags/list` with `latest candidate main v0.9.0 v0.9.1 v0.10.0-rc.1 v0.10.0-rc.2 sha-0a82c4c`, and gives 404 `MANIFEST_UNKNOWN` for every manifest, so no image is ever served. `default_request_version = "HTTP/1.0"` makes a TLS hello get an `HTTP/1.0 400` status line, as a real (Go) registry answers it. That lets `ListRegistryTags`' plain-HTTP fallback for a loopback registry engage. Requests it saw: `GET tags/list` ×2, `HEAD manifests/candidate`, `HEAD manifests/v9.9.9` ×2. |
| The running image (demo) | `grain demo` keeps its ref file at `<tmp>/grain-image.env`. Each pin was written there as `GRAIN_IMAGE=localhost:5000/x/grain:<tag>`, which is what `Upgrader.Running` reads on every request. |

Nothing else was stubbed. grain's own APIs, the daemon, systemd, docker and the browser were real.

## Steps and outcomes

### 1. `grain init`: dry run, real run, re-run, older unit

- `sudo grain init -dry-run -admin-user walker -platform linux-container -data-dir /var/lib/grain-walk -repo-root .`
  The plan listed `make -admin-user a system admin: would register "walker" with a password written to
  /var/lib/grain-walk/admin-password.walker`. It also said it would poll `http://127.0.0.1:4242`.
  On a fresh host the dry run has no "choose the UI/API address" line, because the daemon-account steps
  come first and are still "would". Over an older unit the dry run *does* print the 8420 pin (see below).
- **First real run: the daemon crash-looped.** See finding 1. Fixed, then re-run from a clean unit:
  - the unit has `-ui-addr 0.0.0.0:4242 -ui-allowed-hosts debuerreotype`, and `ss -ltn` shows `*:4242`;
  - `/var/lib/grain-walk/admin-password` and `admin-password.walker` are `0600 grain:grain`, the daemon
    account, inside a `0700 grain:grain` data dir, so root reads them with `sudo`;
  - `GET /api/settings` `adminAccounts` = `["grain","walker"]`, and `walker` signs in
    (`POST /api/auth/login` 200) with the generated password;
  - `grain user list` lists `grain` and `walker`, both with passwords. The listing has no admin column,
    so admin status was read from `adminAccounts`.
  - **`/etc/grain/cli.env` was not written.** See finding 2. Fixed; it is now written.
- **Re-run:** after one more run to pick up a committed build, plus the restart that run asked for, a
  second re-run left nothing changed. The diff was empty across the unit, both password files (mode, owner,
  mtime, sha256), `/etc/grain`, the binary's sha256, `adminAccounts`, `grain user list` and the daemon's
  `MainPID`/`ActiveEnterTimestamp`. On a dirty checkout every run rebuilds and asks for a restart. That is
  by design (`grainBinaryCurrent` refuses a dirty tree).
  The re-run said "kept from the installed unit (pass -ui-addr 0.0.0.0:4242 to move to the new default)"
  about a unit already on 0.0.0.0:4242. Fixed (finding 3).
- **Older unit:** with `systemctl stop` run and `-ui-addr`/`-ui-allowed-hosts` stripped from `ExecStart`,
  both the dry run and the real run said `-ui-addr 127.0.0.1:8420, kept: the installed ... names no
  -ui-addr, and 127.0.0.1:8420 is the default it was written to serve on (pass -ui-addr 0.0.0.0:4242 to
  move to the new default)`. The unit was rewritten with `-ui-addr 127.0.0.1:8420`, and the daemon listens
  on `127.0.0.1:8420`. Before the fix, `grain user list` then failed with "no grain daemon answered at
  http://127.0.0.1:4242 (the default)" (finding 2). After it, cli.env names 8420 and the CLI works.

### 2. Sign-in page of a fresh daemon (Playwright)

A fresh `grain daemon -data-dir /tmp/wt/d2 -ui-addr 0.0.0.0:4343 -ui-allowed-hosts <host ip>
-container-sandboxes ...` with no passwords was browsed at `http://<host ip>:4343/`, a non-loopback and
therefore untrusted source, by a Playwright script with the real built UI.

- The dialog shows the hint once (`data-testid="no-password-accounts"`, `role="note"`), and the page body
  never names the daemon's actor (`debian`).
- **`/api/config` named it, though.** `adminAccounts: ["debian"]` and the default team's
  `users`/`admins`/`members` were all present (finding 4). Fixed.
- **The hint said `http://localhost/`** (port 80) on a 4343/4242 deployment (finding 5), and **told the
  reader to run `grain user password <user> <password>`**, which on a fresh daemon fails with
  `user "debian" not found`, because no account exists (finding 6). Both fixed.
- **A red "sign in to use this server's API" toast** sat under the dialog (finding 7). Fixed. After the
  fixes: one note, zero alerts.
- `grain user bootstrap-admin -password-file /tmp/wt/d2-walker.pw walker` created the account (file
  0600), made it a system admin and set its password. `/api/config` then omits `noPasswordAccounts`, and
  the reloaded dialog has no hint.
- With `-ui-trust-loopback=false` and a loopback browser, the toast carried the daemon's longer
  loopback-not-trusted wording. The hint's "sign in from this machine" advice does not apply in that mode.
  This is recorded only: it is a non-default, operator-chosen configuration.

### 3. CLI against a stopped daemon and a black-holed address

Timings are wall clock, and each run printed one line on stderr and nothing on stdout:

| Command | Target | Time | Line |
| --- | --- | --- | --- |
| `grain list` | stopped daemon (cli.env → 127.0.0.1:8420) | 4.16s | `no grain daemon answered at http://127.0.0.1:8420 (from /etc/grain/cli.env). Set GRAIN_SERVER or pass -server (connection refused)` |
| `grain user list` | same | 4.14s | same |
| `grain list` | no cli.env, built-in default | 4.47s | `... at http://127.0.0.1:4242 (the default) ...` |
| `grain list` | `GRAIN_SERVER=http://10.255.255.1:4242` | 5.02s | see finding 8 |
| `grain user list` | same | 5.03s | same |

Each one fails in about the 5s connect timeout, exit 1. The black-holed line was garbled (finding 8). Fixed.

### 4. `setup.sh` `resolve_image_tag`

The image-channel section of `scripts/setup.sh` (lines 1599-1736) was sourced into a harness, with `log`
and `GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain` set, and run against a temp `image.env`:

| Case | Tag | Channel report |
| --- | --- | --- |
| no channel, fresh host | `latest` | `latest (the default for a new deployment: the last promoted release)` |
| `GRAIN_CHANNEL=candidate`, fresh host | `candidate` | `candidate (GRAIN_CHANNEL=candidate, as given)` |
| existing `image.env` `GRAIN_IMAGE=...:sha-0a82c4cda1234` | `sha-0a82c4cda1234` | `pinned to sha-0a82c4cda1234, which follows no channel (kept: ...; set GRAIN_CHANNEL=latest\|candidate\|main to switch)` |
| existing `...:candidate` | `candidate` | `candidate (kept: ...)` |
| existing `...:v0.9.0-rc.3` | `v0.9.0-rc.3` | `pinned to v0.9.0-rc.3, upgrades offered from candidate (kept: ...)` |
| sha- pin plus `GRAIN_CHANNEL=latest` | `latest` | `latest (GRAIN_CHANNEL=latest, as given)` |
| `GRAIN_CHANNEL=nightly` | — (exit 1) | `GRAIN_CHANNEL=nightly is not a channel: use latest, candidate or main` |

All as designed.

### 5. Upgrade channel on `grain demo -upgrade-image localhost:5000/x/grain`

`-upgrade-allowed-branches main,candidate,latest,v[0-9]*.[0-9]*.[0-9]*,v[0-9]*.[0-9]*.[0-9]*-rc.[0-9]*`.

| Running | `GET /api/upgrade` `channel` | `/api/upgrade/targets` `default` | Upgrade pane's Branch box (Playwright) | `POST /api/upgrade {}` |
| --- | --- | --- | --- | --- |
| `v0.10.0-rc.1` | `candidate` | `candidate` | `candidate` | 202, `branch: candidate`. It then failed at the pull, since the stub serves no image: `docker pull localhost:5000/x/grain:candidate ... not found` |
| `latest` | `latest` | `latest` | `latest` | not posted (same path as above) |
| `sha-0a82c4c` | absent | absent | empty | 400 `branch is required: this deployment follows no channel an upgrade could default to` |

Targets list the three channels plus the released versions `v0.9.1` and `v0.9.0`. `releasedVersions`
lists release tags only, so the `-rc` tags are not offered as versions even when allowed. That is by design.

Previewing a target that does not exist (`GET /api/upgrade/release-notes?branch=...`):
- `no-such-branch` and `grain/task-0-nope` give 400 `branch is not one this deployment may upgrade to:
  "..." matches none of ...`. Correct, since the allowlist refuses them before any pull.
- `v9.9.9` (allowed, absent from the registry) gave 422 with docker's raw `pulling ...: docker pull ...:
  exit status 1: Error response from daemon: failed to resolve reference ...: not found` (finding 9).
  Fixed; it now says `the registry has no image localhost:5000/x/grain:v9.9.9`.

## Findings

Each reproduction is deterministic on a host like the one above.

1. **`grain init` on a fresh host leaves a daemon that cannot create its own `-data-dir`.**
   Repro: `sudo grain init -platform linux-container -data-dir /var/lib/grain-walk -install-claude-cli=false ...`
   on a host with no `/var/lib/grain-walk`. The output says `give the daemon account ownership of -data-dir:
   /var/lib/grain-walk does not exist yet` [skipped], then `confirm the daemon's API is reachable` [manual].
   The journal repeats `mkdir /var/lib/grain-walk: permission denied` on every restart. Only installing
   the claude CLI happened to create the directory first.
   **Fixed** (`fix(init): create a missing -data-dir ...`): a new `create -data-dir` step (0700, or a
   `sudo install -d -m 0700` manual step when not root) runs before the chown.
   Tests: `TestEnsureDataDirOwnershipCreatesAMissingDataDir` and `TestEnsureDataDirOwnershipNamesTheMkdirWhenNotRoot`
   (`pkg/hostinit`). Verified live: the next fresh run came up and provisioned both accounts.
2. **`grain init` writes no `/etc/grain/cli.env`.** The CLI therefore misses a daemon on any address but
   127.0.0.1:4242, including the 127.0.0.1:8420 that grain init itself pins an older unit to. Repro: step 1's
   older-unit run, then `grain user list` gives `no grain daemon answered at http://127.0.0.1:4242 (the default)`.
   **Fixed** (`fix(init): write /etc/grain/cli.env ...`): after settling the address, a `write the grain CLI's
   connection file` step get-or-writes `GRAIN_SERVER=http://<loopback dial addr>` and `GRAIN_DATA_DIR`, the
   same keys `setup.sh` writes. Tests: `TestEnsureCLIEnvNamesTheSettledAddress`,
   `TestEnsureCLIEnvDryRunAndNonRootWriteNothing`, and `withTempUnitPath` now redirects the file too.
   Verified live: written, skipped on re-run, and `grain user list` reaches 8420.
3. **A re-run told to "pass -ui-addr 0.0.0.0:4242 to move to the new default" when already on it.**
   **Fixed** in the same commit. Test: a new `TestSettleUIAddr` case.
4. **An unauthenticated `GET /api/config` names accounts.** Repro: a fresh daemon on 0.0.0.0, then
   `curl http://<host ip>:<port>/api/config` returns `"adminAccounts":["debian"]` and `teams[0].users/admins/members`.
   Next to `noPasswordAccounts: true`, that tells anyone who can reach the port which account has no
   password. The daemon already blanked `actor` for strangers (grain/task-1616). **Fixed**
   (`fix(ui): name no account to a caller of /api/config ...`): when the caller is not authenticated and
   not a trusted source, `adminAccounts`, `teams` and `targetRepos` are emptied. Loopback is unchanged.
   Test: `TestConfigNamesNoAccountToAStranger` (`pkg/ui`), which fails without the fix.
5. **The sign-in hint points at `http://localhost/`** (port 80). **Fixed**: `localSignInURL()` keeps the
   page's port. Vitest: `UsernameDialog.test.jsx` "points the no-password hint at this page's own port".
6. **The hint's `grain user password <user> <password>` cannot work on a fresh daemon** (`user "debian"
   not found`). **Fixed**: it names `grain user bootstrap-admin -password-file <file> <user>`, which
   creates the account, writes its password and makes it a system admin. Vitest updated.
7. **A red "sign in to use this server's API" toast under the sign-in dialog.** The page's background
   loads (`/api/tasks`, `/api/boards`, … ten of them) all return 401 before sign-in, some before
   `/api/config` has answered. **Fixed**: `showError` drops a 401 while the sign-in dialog is up or config
   has not loaded. Vitest: `App.test.jsx` "raises no error toast for the 401s behind the sign-in dialog",
   which fails without the fix.
8. **The CLI's timeout line named the wrong daemon as timing out.** It read: `the grain daemon at
   http://10.255.255.1:4242 (from $GRAIN_SERVER); this host's daemon listens on http://127.0.0.1:8420 per
   /etc/grain/cli.env did not answer in time (...)`. **Fixed**: the hint now follows the clause it
   qualifies. Test: `TestATimeoutNamesTheHostDaemonAfterTheServerThatTimedOut` (`cmd/grain`).
9. **An image-mode preview of a missing tag answered with docker's raw pull error.** **Fixed**: `the
   registry has no image <repo>:<tag>`. Test: `TestReleaseNotesSaysTheRegistryHasNoSuchTag`
   (`pkg/upgrade`). Verified live against the stub.

Observed and left alone:

- The `-admin-user` password file is `admin-password.<name>` (`grain init`), next to `admin-password`
  for `-admin-account`. The plan's wording assumes one file. Both are 0600 and owned by the daemon account.
- `grain user list` shows no admin column; admin status was read from `adminAccounts`.
- The fresh-host dry run does not print the UI address step, because the account steps before it are only
  "would". The real run and the dry run over an existing unit both do.
- The `check the sandbox budget` step was `[manual]` on this 8 GB host: an 8192 MiB sandbox shape cannot
  fit. That is the host's size, not a bug.
- `grain init` run with sudo leaves `ui/node_modules` and `bin/grain` in the checkout owned by root. That
  is expected for a root install and was chowned back by hand for Playwright.

## Not run

- `grain init -platform linux-kvm`: the granule build and guest images. This plan's steps do not depend on them.
- `grain init` on the two Kubernetes platforms.
- A real `setup.sh` install. `resolve_image_tag` was sourced into a harness rather than run in a full
  install (the plan allows either).
- An image upgrade past the pull. The stub serves no images, so `POST /api/upgrade {}` was only followed
  until it chose its target and failed at the pull.

## Tests run locally

- `go test ./pkg/hostinit/ ./pkg/ui/ ./pkg/upgrade/` and `go test ./cmd/grain/ -run 'CLIEnv|Refused|Unanswered|Timeout|Unauthorised'`: pass.
- `npx vitest run` (ui): 199 files, 3415 tests pass.

## grain CI

See the pull request: the final commit's full-suite `wait_for_checks` verdict is recorded in the task's
closing note.
