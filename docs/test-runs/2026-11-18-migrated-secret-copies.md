# Walkthrough: deleting the `.migrated` plaintext secret copies (2026-11-18 plan §5)

**Plan:** [`docs/test-plans/2026-11-18.md` §5](../test-plans/2026-11-18.md#5-secrets-deleting-the-migrated-plaintext-copies) (PR #1512, `87f78fa57`).
**Task:** 7056 (filed by [Task #7014](/tasks/7014)).
**Helpers:** [`2026-11-18-migrated-secret-copies/`](2026-11-18-migrated-secret-copies/). They reuse the [2026-11-11 secrets-store harness](2026-11-11-secrets-store/): `env.sh`, `ghsim.go`, `fake-agy.sh`, `hook.py`, `seedca.go`, `storedigest.go`.

## Outcome

| # | Item | SQLite | PostgreSQL |
|---|---|---|---|
| 1 | v3.4.0 → v3.13.0 → HEAD → HEAD. Copies deleted and logged once, then silence. Pushes and the release-approval key keep working. | **Pass** | **Pass** |
| 2 | Superseded copy deleted; orphaned copy kept with a warning on every start; the re-import instruction works | **Pass** | **Pass** |
| 3 | `GRAIN_GITHUB_CREDENTIALS=files`: nothing retired, the plaintext files are read; back to the store, two starts delete the copies | **Pass** | **Pass** |
| 4 | Downgrade to v3.13.0 per `docs/host-setup.md` | **Fails as documented.** Passes once `state-repo/` is restored too (F2) | **Fails as documented.** Passes with an emptied-schema restore plus `state-repo/` (F2) |
| 5 | Hostile copies: symlink out of the data dir, `0644`, FIFO, undeletable | **Bug F1, fixed.** HEAD read through the symlink, then blocked at start on the FIFO | same |
| 6 | Secret Manager backend (**stubbed**: no emulator) | **Pass**, plus observation F4 | not run (the backend is a secrets axis, not a database one) |

**Fixes in this PR:**
- `fix(gitproxy): never follow or block on a hostile .migrated rollback copy` (F1). Regression tests: `pkg/gitproxy/migrated_copy_files_test.go` and `pkg/daemon/migrated_copy_files_test.go`. Touches `pkg/gitproxy`, a trust-boundary path, so it files the automated security review.
- `docs(host-setup): restore state-repo and an emptied schema on a downgrade` (F2).

**Follow-up proposed:** F3. A HEAD `grain secrets` command on the Secret Manager backend migrates the store in place without the state repository, and the daemon then refuses to start.

## Environment

- Sandbox: Debian 12, 7 CPUs, 30 GB, Go 1.26.10. systemd is PID 1.
- **Builds:**
  - **v3.4.0** (`5ff76cf06`, schema 130): newest `v3.4.*` tag, pre-move.
  - **v3.13.0** (`46fdc1104`, schema 151).
  - **HEAD**: this branch, i.e. `main` at `281e37a9a` (schema 152) plus the fix.
  - **HEAD-unfixed**: `281e37a9a` itself, used for item 5's before.

  Each was built with `go build ./cmd/grain` from its own `git worktree`.
- **Databases:**
  - SQLite: the daemon's embedded `modernc.org/sqlite` v1.57.0.
  - PostgreSQL 15.20 (Debian `postgresql` package): database `mc_walk`, given to the daemon with `-postgres-url`, and to `grain connections` / `grain secrets` with `GRAIN_DB_DRIVER` / `GRAIN_DB_DSN`.
- **Daemon:** real `grain daemon` runs with `-host-sandboxes -sandbox-port-base=0`, using the GitHub sim (`ghsim.go`: acme/widgets over smart-HTTP with receive-pack) and a scripted agent (`fake-agy.sh` + `hook.py`). A run commits and pushes through the daemon's git proxy. "Push X: task N pushed …" in the transcripts is the branch landing in the sim's bare repository.
- **Stubbed:**
  - **Secret Manager**, item 6 only. [`smfakesrv.go`](2026-11-18-migrated-secret-copies/smfakesrv.go) is `pkg/secrets/secretmanager_test.go`'s fake, copied verbatim and run as a server. The daemon reaches it through `GRAIN_SECRET_MANAGER_ENDPOINT` / `GRAIN_SECRET_MANAGER_TOKEN`. No Secret Manager emulator exists to run in a sandbox.
  - Nothing else. `GCE_METADATA_HOST` points at a dead port.
- **Secret values** are random, kept in `$WORK/values` (mode 0600), and never printed. The transcripts compare sha256 prefixes (`storedigest.go` trims, as `RetireMigratedCopy`'s repository-token comparison does). The only API response in the transcripts is the token list after a rotation, which carries names, not values.
- **Not walked here:** **sandbox trust of the sandbox-access CA.** That CA moves on first use and only on Kubernetes deployments (2026-11-11 report). A host-sandbox daemon never opens it, so its file was never moved and had no copy. The package-cache CA did move, and its copy was walked.

**Re-run:** `./walk.sh sqlite`, `./walk.sh postgres` and `./item_6.sh`. Transcripts: [`walk-sqlite.txt`](2026-11-18-migrated-secret-copies/walk-sqlite.txt), [`walk-postgres.txt`](2026-11-18-migrated-secret-copies/walk-postgres.txt), [`item_6.txt`](2026-11-18-migrated-secret-copies/item_6.txt). Each step lists `find <data-dir> -name '*.migrated'` (mode, owner, size, path) and that start's log lines.

## Steps and outcomes

Paths below are relative to the data directory. The SQLite and PostgreSQL runs produced the same files and the same log lines, apart from sizes, which vary with the random values.

### 0. Seed (v3.4.0)

`grain connections import` writes the GitHub sim PAT as `secrets/github/bot.token`. The first v3.4.0 start writes `package-cache-ca.pem`. `seedca.go`, run in the v3.4.0 worktree, writes `sandbox-access-ca.pem` and `release-approval-key.pem` with that build's own code. Then come a `deploy-ssh.ssh.json` SSH credential, the secrets-repo token (`POST /api/state-repo/secrets-repo`), `secrets/state-repo-token`, and a leftover `secrets/relay/token`. No `.migrated` files exist yet.

### Item 1: the upgrade path

- **1a, v3.13.0's first start (the move).** It moved bot, deploy-ssh, both repository tokens, the relay token and the package-cache CA, and kept each as `<file>.migrated` (6 files, all `0600`). The release-approval key moved on first use (`grain release approval-key`), making 7 copies. A run pushed through the git proxy.
- **1b, v3.13.0 again.** No log lines. The same 7 copies stay, because v3.13.0 never deletes them.
- **1c, HEAD's first start.** Every copy the store held went, each logged exactly once:
  ```
  grain: deleted the plaintext rollback copy package-cache-ca.pem.migrated: the secrets store holds the same value as package-cache-ca/bundle.pem
  grain: deleted the plaintext relay token copy secrets/relay/token.migrated: the relay has been removed, so nothing reads it or restores it
  grain: deleted the plaintext rollback copy secrets/state-repo-token.migrated: the secrets store holds a newer value as state-repo-token/token, which is what a rollback restores
  grain: deleted the plaintext rollback copy secrets/secrets-repo-token.migrated: the secrets store holds the same value as secrets-repo-token/token
  grain: deleted the plaintext rollback copy release-approval-key.pem.migrated: the secrets store holds the same value as release-approval-key/key.pem
  grain: WARNING: keeping the plaintext rollback copy secrets/github/deploy-ssh.ssh.json.migrated: the secrets store holds no github-credential-deploy-ssh/ssh.json, … Rename it back to secrets/github/deploy-ssh.ssh.json …
  ```
  After the start, only the orphan (item 2) is left. The things that use these credentials still work:
  - A run pushed through the git proxy with the rotated token.
  - `grain release approval-key` gives the same public-key digest as on v3.13.0.
  - The store's digests of the bot and repository tokens match the values written. The package-cache CA and the approval key match the v3.4.0 files.
- **1d, HEAD again.** There is no deletion line, only the orphan's warning.

### Item 2: superseded and orphaned copies

- **Rotation in Settings.** On v3.13.0 the bot token was rotated with `PUT /api/github-tokens/bot`, the route `/next/settings` uses. v3.13.0's `SetToken` drops that credential's copy at once (`bot.token.migrated` disappears in the 2a listing). So a rotation in Settings leaves HEAD nothing to supersede.
- **Superseded copy.** To get one, `state-repo-token` was rotated offline with `grain secrets set` while the daemon was stopped. HEAD deleted its copy as superseded ("holds a newer value", above).
- **Orphaned copy.** `github-credential-deploy-ssh` was deleted from the store before HEAD. Its copy was kept, with the warning above on the first, second and third HEAD starts (1c, 1d, 2b).
- **Re-import.** Following the warning (`mv …ssh.json.migrated …ssh.json`), the next start moved it into the store again (`gitproxy: moved git credential "deploy-ssh" …`), and `grain secrets list` shows `github-credential-deploy-ssh: ssh.json`. The start after that deleted the new copy as "the same value", leaving no `.migrated` files. A run pushed afterwards.

### Item 3: `GRAIN_GITHUB_CREDENTIALS=files`

This ran on a copy of the data directory as v3.13.0 left it, with all 7 copies present.

- **The files start.** HEAD with `=files` wrote back each git credential, both repository tokens and the package-cache CA ("`=files: wrote … back out of the secrets store`"), and restored `release-approval-key.pem` (with no log line, as the docs say). Each restore replaces the copy it stands for, so that copy goes with it. `deleteMigratedCopies` did not run: the relay token's copy, which nothing restores, was left in place. A run pushed using the plaintext files.
- **Back to the store.**
  - The first start without the variable moved every file into the store again, and kept their copies "until the next start". It deleted only the relay copy and the secrets-repo copy. The secrets-repo copy was itself made earlier in that same start: a `RepoToken` read for the secrets-repo sync moved the file before `deleteMigratedCopies` ran. So it did not survive until the next start, which the design promises every copy moved during a start (see Smaller observations).
  - The second start deleted bot, deploy-ssh, the package-cache CA and the state-repo token copies, leaving no `.migrated` files.

### Item 4: the downgrade to v3.13.0

This followed `docs/host-setup.md` "Downgrading to an earlier build", after HEAD had deleted every copy:

1. The key is not KMS-wrapped, so nothing was unwrapped.
2. HEAD started once with `GRAIN_GITHUB_CREDENTIALS=files`. Every credential was written back. The two CA bundles and the approval key were byte-identical to the v3.4.0 originals.
3. The pre-upgrade backup taken on v3.13.0 was restored: a cold copy of `store/` on SQLite, a `pg_dump -Fc` on PostgreSQL.
4. v3.13.0 was started with `GRAIN_ACCEPT_STORE_BEHIND=1`. **It exited** (F2).

Once `state-repo/` was also put back from the pre-upgrade copy, v3.13.0 started on both databases:
- It moved the files back into its store, as a post-move build does.
- A run pushed.
- The approval-key digest was the one HEAD and v3.13.0 had before.

On PostgreSQL, the documented `pg_restore --clean --if-exists --no-owner` also failed (F2), and the restore that worked was into an emptied schema.

### Item 5: hostile copies

These were planted with the store holding every value:
- `secrets/github/bot.token.migrated` as a symlink to a file outside the data directory;
- `secrets/state-repo-token.migrated` at mode `0644`, holding the stored value;
- `package-cache-ca.pem.migrated` as a FIFO;
- `secrets/relay/token.migrated` in a `0500` directory.

- **HEAD-unfixed** read the symlink's target and deleted the symlink ("deleted the plaintext rollback copy …bot.token.migrated: the secrets store holds a newer value …"). It then **blocked for good on the FIFO**: the API never came up, and the 0644 and relay copies were never reached. The target file itself was left intact, because `os.Remove` removes the link.
- **HEAD (fixed)** came up. The symlink and the FIFO were kept, and so was the target:
  ```
  grain: keeping the rollback copy secrets/github/bot.token.migrated: reading it: not a regular file (L---------), so it is neither read nor deleted
  grain: keeping the rollback copy package-cache-ca.pem.migrated: reading it: not a regular file (p---------), so it is neither read nor deleted
  grain: deleting the plaintext relay token copy secrets/relay/token.migrated: remove secrets/relay/token.migrated: permission denied
  grain: deleted the plaintext rollback copy secrets/state-repo-token.migrated: the secrets store holds the same value as state-repo-token/token
  ```
  The `0644` copy was deleted like any other. The undeletable copy was logged, and the start was not affected.

### Item 6: Secret Manager (stubbed)

This used `-secrets-backend secretmanager` on SQLite. On that backend, Secret Manager is the store the CA bundles are checked against; git credentials, repository tokens and the approval key stay in the file store.

- **a.** v3.13.0 moved `package-cache-ca.pem` into Secret Manager and kept the copy. HEAD deleted it ("holds the same value as package-cache-ca/bundle.pem"). The next HEAD start was silent.
- **b.** The secret was deleted from Secret Manager before HEAD, using v3.13.0's CLI (see F3). HEAD kept the copy and warned. See F4 for the second start.

## Findings

### F1: a hostile `.migrated` copy was read through, or blocked the start (fixed)

`gitproxy.RetireMigratedCopy`, which every kind's retire path uses (git credentials, CA bundles, repository tokens, approval key), read the copy with `os.ReadFile`. That follows symlinks and opens anything. The consequences:
- A symlink to an outside file that holds the stored value is read and "verified", and the symlink is deleted. It is also an oracle: the log says whether an arbitrary file's bytes equal a stored secret.
- A FIFO blocks the daemon's start for good.
- A link to `/dev/zero` blocks too, and grows memory without bound.

Planting one needs write access to the data directory, so the impact is bounded. But the plan requires such files never to be followed or fatal.

- **Reproductions (deterministic):**
  - `go test ./pkg/gitproxy -run RollbackCopy`: on `281e37a9a` the symlink, FIFO and `/dev/zero` cases fail (two by 5 s timeouts).
  - `go test ./pkg/daemon -run TestAStartNeitherFollowsNorBlocksOnHostileRollbackCopies`: `MoveLooseCredentialsIntoStore` has not returned after 5 s.
  - Live: item 5 with HEAD-unfixed, on both databases.
- **Fix:** `readMigratedCopy` reads the copy only when it is a regular file. It does an `Lstat`, then an `O_NOFOLLOW|O_NONBLOCK` open, then checks that the opened descriptor is the same regular file, and caps the read at 1 MiB. Anything else is kept and logged as "not a regular file … neither read nor deleted". `go test ./pkg/gitproxy ./pkg/daemon` passes.

### F2: the documented downgrade left out the state repository and failed on PostgreSQL (docs fixed)

- **State repository.** `<data-dir>/state-repo` is migrated in place along with the store (`Migrate grain state to schema 152`). With only the store restored, v3.13.0 exits on both databases:
  ```
  grain daemon: loading the config repository: state repository was written by a newer build of grain: repository is at schema 152, this build knows 151
  ```
  `docs/host-setup.md` said "Put back only the store", and none of its restore options mentioned `state-repo/`. Restoring `state-repo/` from the same pre-upgrade copy made the downgrade work. A `grain backup` archive carries it as `config/config-repo.bundle` (`pkg/cli/backup.go` archives `daemon.StateRepoDir`), but restoring from that bundle was not walked.
- **PostgreSQL restore.** `pg_restore --clean --if-exists --no-owner` of the 151 dump over the 152 database failed:
  ```
  pg_restore: error: could not execute query: ERROR:  cannot drop table public.task_suspension because other objects depend on it
  pg_restore: error: could not execute query: ERROR:  relation "task_suspension" already exists
  pg_restore: error: could not execute query: ERROR:  could not create unique index "task_suspension_pkey"
  pg_restore: warning: errors ignored on restore: 3
  ```
  The `--clean` drops follow the older dump's dependency order, and at 152 the `task_ready` view depends on `task_suspension`. So the 152 table survived, the dump's rows were appended to it, and its primary key could not be rebuilt. Restoring into an emptied schema (`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`, then `pg_restore --no-owner`) reported no errors, and v3.13.0 started, pushed and signed on it.
- **Reproduction:** `./walk.sh sqlite` and `./walk.sh postgres`, step 4. Each tries the documented restore first.
- **Fix:** `docs/host-setup.md` now says to restore `state-repo/` and to restore PostgreSQL dumps into an emptied schema, and `docs/database.md`'s dump-restore paragraph says the same.

### F3: HEAD's `grain secrets` on Secret Manager migrates the store without its state repository (follow-up)

On a data directory v3.13.0 left (store and `state-repo` at 151), a **read-only** `grain secrets -data-dir D -secrets-backend secretmanager -secrets-sm-prefix … list` from HEAD logs `grain: migrated the store in place from schema 151 to 152`. It opens the database for the Secret Manager catalogue. From then on every HEAD daemon start exits:
```
grain daemon: loading the config repository: state repository was written by an older build of grain: repository is at schema 151, this build knows 152
```
The file backend's `grain secrets list` does not open the database and is not affected.

- **Reproduction (deterministic):**
  1. `cp -a $WORK/data-after-v313 D` from `item_6.sh`.
  2. Run HEAD's `grain secrets … secretmanager … list` on it.
  3. Run `grain daemon -data-dir D …`: it exits.

This lies outside §5's code (`pkg/staterepo` and the CLI's store opening), and the right fix (a CLI that does not migrate, or a daemon that brings the state repository forward to its store's schema) needs a design call. It is proposed as a follow-up. Item 6 used v3.13.0's CLI for its offline delete to stay clear of it.

### F4: an orphaned CA bundle copy is warned about once, not on every start (observation)

In item 6b the store held no package-cache CA when HEAD started. The retire pass kept the copy and warned, as designed. The same start then minted a new package-cache CA into the store, so the next start deleted the old copy as superseded ("holds a newer value … which is what a rollback restores"). So for a CA the "warning on every start" lasts one start. The warning's "rename it back" advice also comes too late: the start that prints it has already replaced the CA. The deletion itself is consistent with the design, because a rollback restores the store's CA. Only the warning's wording is off for CA bundles. No change made.

### Smaller observations

- A rotation in Settings (`PUT /api/github-tokens/{name}`) on v3.13.0 already drops that credential's copy, so the "superseded" path is only reached by changes outside `SetToken` (`grain secrets set`, a secrets-repo sync, a hand edit).
- A copy kept under `secrets/github` is logged twice per start: once by `RetireMigratedCopy`, and once by `moveGitCredentialsIntoStore`'s summary ("some plaintext rollback copies of git credentials are still under …").
- **A repository token moved by an early `RepoToken` read loses its copy in the same start.** The design says a copy moved during a start survives until the next start. The secrets-repo token is moved by the first `RepoToken` read, which comes before `deleteMigratedCopies`, so its copy is checked and deleted at once. It was checked against the store first, so no value is lost, but the one-start rollback window does not hold for it. Seen in item 3. No change made.
