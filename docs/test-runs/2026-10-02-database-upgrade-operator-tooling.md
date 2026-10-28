# Test run: database upgrade token, PostgreSQL store, upgrade allow-list, export/import secrets

**Plan:** `docs/test-plans/2026-10-02.md` §6 (filed by task 1528; the plan itself is on `candidate`, not yet on `main`)
**Task:** 1578
**Run on:** 2026-10-03, 01:02–01:35 UTC
**Tested commit:** `main` at `5a63bb2d1` (Merge PR #2688). Every §6 commit is an ancestor of it and none of them is on `candidate` yet (`git merge-base --is-ancestor <sha> HEAD`; `origin/candidate` lacks all of them, so tonight's cut is still pending): b44df8851, 8002ba301, e37a528d7, 4303cd102, 45db3ffc9, cfd8443d7, 78fac4d82, 5f49a0fbf, 75f1c8754, f2da257c7, f8941b240, 83c371c2d, a980d5c30, 304d424d4, eb1bc604e, a2aa0ea07, 0cc6dabbd, 0b3ca375c, 988817980.

**Outcome:** every §6 behaviour held. The walkthrough found **three bugs**, all fixed on this branch with regression tests:

1. **On PostgreSQL every `GET /api/tasks` returned 500.** This is what you get right after a database upgrade.
2. **The Settings UI could no longer finish a database upgrade.** It had never been taught the new confirmation token step.
3. **A deployment with no tasks rendered the "something went wrong" page everywhere.** `GET /api/tasks` returned `null`, not `[]`.

## Environment

| | |
|---|---|
| Host | grain sandbox, Debian, 4 vCPU, user `debian` |
| Go / Node | go from `/usr/local/bin/go`; Node 20.19.0 |
| grain | `go build -o /tmp/grain ./cmd/grain`, after `cd ui && npm ci && npm run build` (production bundle in `pkg/ui/static`) |
| PostgreSQL | `docker run postgres:16`, `POSTGRES_PASSWORD=S3cretPw-XYZZY`, published on `127.0.0.1:55432`; databases `grain`, `other`, `gtest` |
| Browser | Playwright 1.62.1 / Chromium from `ui/node_modules`, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Terraform / shellcheck | Terraform v1.16.4, shellcheck from Debian |

Daemons (all real `grain daemon`, not `grain demo`; demo has no `SourceDB` and no journal-backed log source):

- **d1:** `sudo systemd-run --unit grain-daemon --uid=debian … /tmp/grain daemon -data-dir /tmp/d1 -disable-native-sandboxes -ui-addr 127.0.0.1:18430 -ci=false`. It runs as the `grain-daemon.service` unit because `GET /api/logs/daemon` reads `journalctl -u grain-daemon.service` (`cmd/grain/daemon.go:2249`), so the token redaction was checked against the real journal. Upgraded from SQLite to `postgres://…/grain` in the token flow, then restarted onto it.
- **d2:** `-data-dir /tmp/d2` on SQLite, the source of the CLI export.
- **d3:** `-data-dir /tmp/d3 … -upgrade-src-dir /tmp/upsrc -upgrade-install-path /tmp/up-installed/grain`, first with the default allow-list, then with `-upgrade-allowed-branches 'main,feature/*'`. `/tmp/upsrc` is a clone of a scratch bare repo carrying `main` and `feature/x`. There is no `-upgrade-restart-cmd`, and the install path is a scratch file, so the running binary could not be replaced.

The caller on loopback is the OS user `debian`. `debian` is the deployment's sole system admin (`adminAccounts: ["debian"]`). For the wrong-principal case, `carol` was added to `adminAccounts` through `PUT /api/settings` and spoken as with `X-Grain-User: carol`.

### Stubs

- **GCP** (`migrate-secrets.sh`): a fake `gcloud` on `PATH` (`/tmp/gstub/gcloud`). It answers `compute instances describe` with canned metadata, answers `secrets versions list` (one secret already has a version) and records argv and stdin for `secrets versions add` and `remove-metadata`. Nothing ran against live GCP, so `terraform plan`/`apply` and Secret Manager itself were not exercised.
- **GitHub**: none needed. `release-candidate-cut.sh` ran with `GITHUB_TOKEN` unset, which takes its "no gh" path, against a local bare `origin`.
- None of grain's own APIs or React were mocked.

## 1. Database upgrade confirmation token (b44df8851, 8002ba301, e37a528d7)

`DSN=postgres://postgres:S3cretPw-XYZZY@127.0.0.1:55432/grain?sslmode=disable`; each request was `curl -X POST /api/database/upgrade -d '{"dsn":…}'`.

| Step | Expected | Observed |
|---|---|---|
| First request as admin `debian` | 202 `confirmationRequired`, nothing changed | 202 `{"confirmationRequired":true,"message":"Nothing has been changed yet…","expiresAt":"…+10m"}`; `/tmp/d1/db-config.json` absent |
| Token in the daemon log | present | `journalctl -u grain-daemon.service`: `ui: database upgrade requested by "debian": copy the store to postgres://postgres:%2A%2A%2A%2A%2A%2A@127.0.0.1:55432/grain?sslmode=disable … if you made this request, resend it with "confirmationToken": "qs2z3kotjs2gy26mauwxexxf" to proceed` |
| Token in `GET /api/logs/daemon?lines=5000` | absent | 0 hits; the same line reads `"confirmationToken": "[redacted: read it on the daemon host]"` |
| Wrong principal: `debian` issues, admin `carol` redeems | 403 | 403 `confirmation token is invalid, expired or does not match this request…` |
| Wrong principal reversed: `carol` issues, `debian` redeems | 403 | 403 (log says `requested by "carol"`) |
| Non-admin `carol` (before being made admin) | 403 | 403 `user "carol" does not have access to this server…` |
| Wrong DSN (`…/other`) | 403 | 403 |
| Same token again with the right DSN after that mismatch | 403 (spent by any attempt) | 403 |
| Same DSN, `migrateData:false` instead of `true` | 403 | 403 |
| Made-up token | 403 | 403 |
| Expired: issued 01:13:11, redeemed 01:26:53 (TTL 10 min, no restart in between: unit active since 01:08:20) | 403 | 403 |
| Correct token, same principal and request | 200, migrated | 200 `Successfully migrated SQLite database to PostgreSQL (88 tables migrated)`, `"dsn":"postgres://postgres:%2A%2A…@…"` |
| Replay of that used token | 403 | 403 |

Also observed: a token issued before a daemon restart is gone after it (403), because tokens are held in memory only. That is correct, and an operator should know about it. The first expiry attempt hit this case by accident, so it was redone without a restart (the row above).

**Cosmetic:** in the log line and in `GET /api/database`, the masked URL password is percent-encoded (`%2A%2A%2A%2A%2A%2A`, not `******`). The key/value form (below) shows `******`. It is masked in both forms, just less readable in the URL form. Not changed here.

## 2. PostgreSQL after migration (4303cd102 … f2da257c7)

`sudo systemctl restart grain-daemon`: `GET /api/database` → `{"driver":"postgres","dsn":"postgres://postgres:%2A…@127.0.0.1:55432/grain?sslmode=disable","isSqlite":false}`.

**Finding 1: `GET /api/tasks` answered 500 on PostgreSQL.** Log line: `api: 500 internal [ref c6c2c58d30c2]: ERROR: column "o.pr_merged_at" must appear in the GROUP BY clause or be used in an aggregate function (SQLSTATE 42803)`. The cause is `Store.TaskSpans` (`pkg/model/store.go`). It grouped `task_run` by task and read bare columns beside `MIN(started_at)`, which relies on SQLite's bare-column rule. PostgreSQL refuses that query, so the task list, and with it the main UI, was broken on every PostgreSQL deployment. The query has been there since `ae5435f14` (2026-09-06). The parity suite had no TaskSpans test, so the new `postgres` CI job could not catch it. Fix and test: see "Fixes".

After the fix, every parameterless `GET /api/...` route registered in `pkg/ui` (53 routes) was swept on the PostgreSQL daemon, and none answered 5xx.

Large ids and insert read-back (sequences pushed with `setval` in the container):

| Check | Observed |
|---|---|
| `task_sequence_number_seq` → 4294967296, then `POST /api/tasks` | `{"id":"4294967297"}`; `GET /api/tasks/4294967297` reads it back; row present in `task` |
| `task_comment_id_seq` → 3000000000, then `POST /api/tasks/4294967297/comments` | comment `id: 3000000001` in the task's comments and in `task_comment` |
| `template_sequence_number_seq` → 2147483650, then `POST /api/templates` | `template-2147483651`, listed by `GET /api/templates` |
| `\d task_sequence`, `\d task_comment` | `number bigint`, `id bigint` |

DSN password `S3cretPw-XYZZY`, searched for on every surface:

| Surface | Hits |
|---|---|
| `GET /api/settings`, `/api/database`, `/api/config` | 0, 0, 0 |
| all 53 parameterless GET routes | 0 |
| `GET /api/logs/{daemon,config-sync,git-proxy-audit}` | 0 |
| the daemon's own journal, including the upgrade and a failed `POST /api/database/test` | 0 |
| `sudo grep -r` over the data dir and `HOME` | 0 |
| `ps -eo args` | 0 |
| `db-config.json` (mode 0600) | `{"driver":"postgres","dsn":"postgres://postgres@127.0.0.1:55432/grain?sslmode=disable","passwordSecret":"database-password"}` |

Other DSN forms in the token log line, with 0 hits for the secrets:
- `host=… password=KVsecret99 …` → `password=******`
- `…?sslmode=disable&password=QSsecret77` → `password=******`
- `password='quoted secret88'` → `password=******`

a2aa0ea07: after `PUT /api/settings` with `googleOAuthClientSecret`, `GET /api/settings` shows `googleOAuthClientSecretSet: true` and no value. The value is not in `/tmp/d1/state-repo`.

## 3. `grain export` / `grain import` (a980d5c30, 304d424d4, eb1bc604e)

All in a temp `HOME` (`/tmp/h3`). The source is `/tmp/d2` with a task and the secret `demo-secret/value = hunter2-roundtrip` set by `grain secrets set`. Modes were loosened first: `store/grain.db` 666, `secrets/secrets.{enc,key}` 644, `state-repo/settings` 777.

| Command | Result |
|---|---|
| `cat pp \| grain export -data-dir /tmp/d2 -passphrase-file - -out b1.enc` | exit 0, bundle 0600. A 50 ms `ps` watcher saw only `./grain export -data-dir /tmp/d2 -passphrase-file - -out /tmp/b1.enc`, with 0 passphrase hits |
| `grain export … -key-file /tmp/key` (64 hex) | exit 0 |
| `cat key \| grain import -data-dir /tmp/i2 -in b2.enc -key-file -` | exit 0, `credentials: none` (deployment credentials, none set), `grain secrets list` → `demo-secret: value`. The `ps` watcher had 0 hits for the key |
| `grain import -data-dir /tmp/i1 -in b1.enc -passphrase-file pp` | exit 0 |
| passphrase bundle with `-key-file` / wrong passphrase on stdin | exit 1, `authentication failed (incorrect passphrase/key or corrupted bundle)` |
| `-passphrase 'argv secret'` (export), `-password` (import), `-key <hex>` (import) | work, each prints `warning: -passphrase/-password on the command line is deprecated and visible to other users through ps; use -passphrase-file …` (or the `-key` version) |
| 63-hex key on stdin; 64 × `z` key file | exit 1, `key must be 32 bytes written as 64 hex characters (…)` |
| key with surrounding spaces and newline on stdin | accepted |
| `-passphrase-file pp -key-file key` (export and import) | exit 1, `give either a passphrase or a key, not both` |
| `-passphrase-file - -key-file -` | exit 1, `-passphrase-file and -key-file cannot both read stdin` |
| `-passphrase x -passphrase-file pp` | exit 1, `give the passphrase once…` |
| empty stdin for `-passphrase-file -` | exit 1, `passphrase from - is empty` |
| `grain backup -h` | exit 0 |

Modes after import (`find /tmp/i2 -printf '%m %p'`):
- data dir, `secrets/`, `store/` and `state-repo/` are 700;
- `secrets.enc`, `secrets.key` and `store/grain.db` are 600, against source modes of 644, 644 and 666;
- the files `git` checks out inside `state-repo/` are 644 under umask 022, and directories there are 755. They sit inside the 700 `state-repo/`.

## 4. Upgrade allow-list (83c371c2d)

d3 with the default allow-list, through the API (`POST /api/upgrade`):

| Branch | Observed |
|---|---|
| `feature/x` (immediate) | 400 `branch is not one this deployment may upgrade to: "feature/x" matches none of main (set with -upgrade-allowed-branches)` |
| `feature/x` with `afterTasksFinish` (scheduled) | the same 400, at request time; nothing scheduled |
| `feature/../x` | 400 `… is not a valid git ref name` |

The same through **Settings → Upgrade** in Playwright (`/tmp/pw/upgrade.mjs`, `/admin/upgrade`): with "Wait until this deployment is idle" off and on, the form's POST got the same 400 and the panel showed the message as an alert. Nothing was scheduled or installed.

d3 restarted with `-upgrade-allowed-branches 'main,feature/*'`:

| Branch | Observed |
|---|---|
| `feature/y/z` | 400 `… matches none of main, feature/*` (`*` does not cross `/`) |
| `release/1` | 400 |
| `feature/x` scheduled (API) | 202, `scheduled: {branch: feature/x, byId: debian}`; then `DELETE /api/upgrade/scheduled` → `cancelled: true` |
| `feature/x` immediate (Settings UI, Playwright) | 202 `phase: running`. Steps went `checkout:done`, then `build:running`, then `build:failed` |

**How the binary was kept from being replaced:**
- `-upgrade-install-path` was a scratch file and there was no `-upgrade-restart-cmd`.
- The build failed on its own anyway: `make container-build`'s builder container could not reach npm through the sandbox's package-cache proxy (`SELF_SIGNED_CERT_IN_CHAIN`). That is a property of this sandbox, not of grain.
- `/tmp/up-installed/` stayed empty.

UI environment notes:
- A fresh real daemon with no accounts opens the first-user "create account" dialog over everything, including the admin overlay. The walkthrough created `debian` / password through it; the next browser context signed in through the "log in" tab.
- d3 showed "reconcile loop has stopped", because the git proxy's fixed port 127.0.0.1:9728 was already taken by d1 on the same host. That is a test-environment artifact, and it is also why d3 never filed its director task. That task-less state is what exposed finding 3.

## 5. `scripts/release-candidate-cut.sh` (0b3ca375c)

There is no `DRY_RUN` switch. The equivalent used here: for each case, a scratch repo whose `origin` is a local bare clone, so every `git push` in the script stays local. Each repo has an annotated `v1.5.0-rc.1` tag with its tagger date set via `GIT_COMMITTER_DATE`, a `candidate` branch with one fix (merged into `main`), and one newer `main` commit. `GITHUB_TOKEN` was unset. Harness: `/tmp/rc.sh <hours>`.

| rc.1 age | Observed |
|---|---|
| 20 h | `was cut 20 hours ago (required interval: 1 days, 6h slack)`. Promoted the candidate to `v1.5.0` and `latest`, found nothing to back-merge, cut `candidate` from `main` and tagged `v1.6.0-rc.1`. exit 0; bare origin gained `v1.5.0`, `latest`, `v1.6.0-rc.1`, and `candidate` = `main` |
| 2 h | `was cut 2 hours ago … Cadence interval of 1 days has not elapsed yet. Skipping release cut`. exit 0; origin unchanged |

The threshold is `24 − 6 = 18` whole hours (integer division), so a daily 18:00 cut that ran a few minutes late the day before still cuts.

## 6. Terraform and deploy scripts (0cc6dabbd)

- `terraform -chdir=deploy/terraform/gcp init -backend=false && terraform validate` → **Success**, with deprecation warnings for `google_iap_brand` (`iap.tf:134`) and the value used at `iap.tf:152`. Those are upstream deprecations, not this change. `.terraform/` was removed afterwards.
- `bash -n` and `shellcheck` are clean on `deploy/terraform/gcp/files/deploy.sh`, `deploy/terraform/gcp/deploy/push-secrets.sh` and `deploy/terraform/gcp/deploy/migrate-secrets.sh`. The task text gives these as bare names; these are their actual paths.
- `migrate-secrets.sh` against the stub `gcloud` (`PROJECT=p INSTANCE=grain-host ZONE=z`):
  - `--pending` → exit 0 (legacy keys present).
  - The full run copied `grain-github-token` into `grain-github-token`, with the value piped on stdin (`--data-file=-`, bytes `ghp_FAKE\n` preserved) and never on argv.
  - It skipped the empty `grain-gemini-api-key`, and kept the existing version of `grain-secrets-key`.
  - It then removed exactly those three keys from metadata and left `startup-script` alone.
- **Not done (no live GCP):** `terraform plan`/`apply`, Secret Manager IAM, and the instance actually reading secrets at boot.

## Fixes on this branch

1. **`fix(model)`, PostgreSQL task list.**
   - The bug: `Store.TaskSpans` needed SQLite's bare-column `GROUP BY`, so every `GET /api/tasks` answered 500 on PostgreSQL.
   - The fix: the query now returns one row per attempt (no `GROUP BY`) and keeps the earliest start in Go.
   - The test: `pkg/model/postgres/task_span_store_test.go` (`TestTaskSpansOnPostgres`), which the `postgres` CI job runs against a real server. It failed with SQLSTATE 42803 before the fix and passes after. The SQLite-side `pkg/ui` TaskSpan tests still pass.
2. **`fix(ui)`, empty task list.**
   - The bug: `GET /api/tasks` sent `null` for an admin on a deployment with no tasks, and `App.jsx` crashed with `Cannot read properties of null (reading 'filter')`. Seen on d3 as the "something went wrong" page on `/admin/upgrade`.
   - The fix: `taskListCache.response` always marshals an array.
   - The test: `TestTaskListOfAnEmptyStoreIsAnEmptyArray`, which failed with `"null"` before.
3. **`fix(ui)`, Settings database upgrade.**
   - The bug: since b44df8851, `PostgresDatabaseUpgrade` showed the 202 as a red error and offered no way to send the token back.
   - The fix: it now shows an info notice pointing at the daemon log, adds a "Confirmation token" field, and resends the identical request with `confirmationToken` ("Confirm Migration"). Editing the DSN or the migrate option, or any failed attempt, drops the pending token.
   - The test: a new case in `UpgradePanel.test.jsx`, which fails without the change.
   - Walked through in Playwright on d3: 202, the info notice and the token field appeared. The token was read from the daemon's log file and entered, the 200 came back, and the panel switched to "This deployment is running on PostgreSQL (postgres://postgres:%2A…@…/other…)". `db-config.json` was written with `passwordSecret`.

None of these touch a trust-boundary path or `tests/security`.

## CI

- Full run `71e11491aacf7d10` at `ebbe9ae`: every job passed except `go`, which failed only on the known `pkg/console` flake `TestSessionGivesItsProcessAControllingTerminal` ("nothing after 30s"; fix queued as task 1556). grain CI reported the same failure on 7 other branches at the time.
- The selective `go` run `41a1f5a66628625c` on the same commit passed.
- The earlier full run `8486c9cc54a4a995` showed `go` as failed while its Test step was still running with no output. It is recorded here as seen.
- `postgres` (which now includes `TestTaskSpansOnPostgres`), `frontend`, `ui e2e`, `terraform`, `lint`, `installer` and `package cache` passed.
- At `36bd55a` (a SQLite-side TaskSpans ordering test added), `ui e2e` failed twice, the second time in the uncached rerun `31129d0fe795acf9`. The failure was the plan's known `suites_workflows.spec.js:8` flake, now with the server's answer: `acme/innovate-app is not on this deployment's target repo list`.
  - The cause: the spec picked the Run overlay's repo by position, and `non-admin-team-setup-walkthrough.spec.js` adds `acme/innovate-app` to the same demo.
  - This branch now selects `acme/widgets` by name. The hunk is identical to the one on task 1565's open PR #2684 (and five other branches carry the same fix), so whichever lands first merges cleanly.
- Final full run `e2d71d0ca2a657f5` at `3258f70`: every job passed.

## Not covered / limitations

- No live GCP. Terraform `plan`/`apply` and Secret Manager were not run; `migrate-secrets.sh` was exercised against a stub `gcloud` only.
- Embedded PostgreSQL (`postgresAvailable: false` here: no PostgreSQL binaries on the host) was not used. A `postgres:16` container stood in for it. `45db3ffc9`/`cfd8443d7` (fsync/full_page_writes and the embedded cluster's env) were therefore not exercised beyond their unit tests.
- The `feature/x` upgrade could not get past `make container-build` in this sandbox (npm behind the package-cache proxy's self-signed CA inside the builder container), so install, health check and the schema steps were not reached.
- `988817980` (disk-floor sweep every 10 minutes) was not timed. The daemon logs the disk floor at start.
