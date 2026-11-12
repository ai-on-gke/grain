# Walkthrough — secrets store consolidation and the KMS-wrapped key (2026-11-11 plan §3)

**Plan:** [`docs/test-plans/2026-11-11.md` §3](../test-plans/2026-11-11.md#3-secrets-store-consolidation-and-the-kms-wrapped-secrets-key)
(PRs #1080, #1105, #1108, #1138, #1132). **Task:** 4516 (filed by 4297).
**Helpers:** [`2026-11-11-secrets-store/`](2026-11-11-secrets-store/).

## Outcome

| # | Item | Result |
|---|---|---|
| 1 | Upgrade a 61668e58 data dir with real credential files | **Pass.** Every file imported, read back and renamed `*.migrated`; the git proxy clones and pushes; App tokens are minted from the stored key; a restart changes nothing; a re-created file is taken as the newer word. |
| 2 | Downgrade via `GRAIN_GITHUB_CREDENTIALS=files` | **Pass for credentials.** Every file is written back, and the CAs and approval key are byte-identical. 61668e58 clones, mints, pushes and signs with the same key. **But** 61668e58 refuses the schema-142 database: there is no schema downgrade (see finding F2). |
| 3 | KMS wrap/unwrap with kmsfake | **Pass, after a fix.** Wrap/unwrap work with the daemon up (no restart) and down (in-process fallback). Permission denied and unreachable both recover on backoff in about 11 s without the daemon closing. **Bug F1, fixed:** every config/secrets-repo sync was a fresh KMS unwrap. |
| 4 | Retired keys | **Pass.** |
| 5 | Export refusal and bundle import | **Pass.** |
| 6 | Redaction through the daemon | **Pass.** Values are redacted through `POST /api/secrets/redact`; with the daemon not answering, lines are withheld; the route returns 401 with no credential. The run on `-host-sandboxes` holds the owner's token by design (F3). |
| 7 | `request_secret` refusals from a real run | **Pass.** |
| 8 | Typed-in credentials through a `grain/config` PR | **Pass.** |
| UI | Settings → State (the "config repo" tab) at 1440×900 and 390×844, light and dark | **Pass.** No `ui/` change. |

**Fix in this PR:** `fix(secrets): unwrap a KMS-wrapped key once per process for repo tokens`. It adds `secrets.OpenShared`, used by `pkg/daemon`'s repo-token store and `pkg/releaseapproval`. The regression tests are `pkg/secrets/shared_test.go` and `pkg/daemon/repo_token_kms_test.go`. The PR touches `pkg/secrets`, so it files the automated security review, which is a separate merge gate from CI.

## Environment

- Sandbox: Debian, 7 CPUs, 30 GB. Go 1.26. systemd is PID 1.
- **HEAD** is `c5611085` on this branch, plus the fix. It is built to `/home/debian/bin/grain`, and the React UI is built into `pkg/ui/static` (`npm run build`) for the Settings checks. **61668e58** is built from a worktree to `/home/debian/bin/grain-old`.
- Real `grain daemon`s run on SQLite with `-host-sandboxes` and `-sandbox-port-base=0`. Runs use a scripted agent, [`fake-agy.sh`](2026-11-11-secrets-store/fake-agy.sh), and [`hook.py`](2026-11-11-secrets-store/hook.py). The hook talks to each run's own `grain mcpserver` over stdio.
- **GitHub:** [`ghsim.go`](2026-11-11-secrets-store/ghsim.go), which is `pkg/github/githubsim` plus git smart-HTTP with receive-pack. It adds the App installation-token exchange (`POST /app/installations/{id}/access_tokens`) with a mint counter, because githubsim does not serve it.
- **Stubbed:** only **Cloud KMS**, via [`kmsfakesrv.go`](2026-11-11-secrets-store/kmsfakesrv.go). That is `pkg/secrets/kmsfake` run as a server, with `/deny` (403) and `/down` (503) switches and decrypt counters. `GCE_METADATA_HOST` points at a dead port, so nothing reaches Google.
- **Item 6 journal:** the daemon runs as the transient system unit `grain-walk.service` (env.sh `start_daemon_unit`), in group `systemd-journal`, so `read_grain_log`'s "daemon" source reads a real journal. A `tail` started beside the daemon echoes `journal-inject.txt` onto the unit's own stdout. That stands in for "the daemon logged a credential". `GRAIN_MCP_SURFACE=full` makes the run's MCP server serve grant tools, because the scripted agent calls them over MCP rather than through the in-sandbox CLI.
- **Secret values:** every value is random, generated into `$WORK/values` (mode 0600) and never printed. [`storedigest.go`](2026-11-11-secrets-store/storedigest.go) prints sha256 prefixes, and the scripts compare digests or grep for the value.

Re-run: `./seed_old.sh && ./items_1_2.sh && ./item_3.sh && ./items_4_5.sh && ./item_6.sh && ./item_8.sh` (sources `env.sh`). Item 7 is the one-liner in §7 below.

## Findings

### F1 — every repo-token read was a Cloud KMS unwrap (fixed)

With the secrets key wrapped, an idle daemon kept calling KMS `decrypt`: 4 at start, then about 6 a minute. The cause: `RepoToken` asks `storedRepoToken` on every state- and secrets-repo sync, and that opened a fresh `secrets.Store` through `repoTokenSecrets` (`pkg/daemon/loose_credentials.go`). A fresh store loads its key, and with a KMS-wrapped key that is a network unwrap. `releaseapproval.storeIn` did the same on every signature. `pkg/gitproxy/credstore.go` already kept one store per key file per process for exactly this reason.

- **Repro (deterministic):** `go test ./pkg/daemon -run TestRepoTokenUnwrapsAKMSWrappedKeyOnce`. Before the fix, 5 `RepoToken` reads made 5 KMS decrypts.
- **Live, against kmsfakesrv:** 5 secret writes, each followed by `POST /api/state-repo/sync`, made 5 secrets-repo pushes. Unfixed build: **+10 decrypts**. Fixed build: **+0**.
- **Fix:** `secrets.OpenShared(cfg)` caches one `Store` per key file and secrets file. A store that cannot be read is not cached, so a store opened while KMS refuses recovers on the next call (`TestOpenSharedDoesNotCacheAStoreKMSRefused`). `repoTokenSecrets`, `fileSecretsBeside` and `releaseapproval.storeIn` use it. `go test ./pkg/secrets ./pkg/daemon ./pkg/releaseapproval ./pkg/gitproxy ./pkg/cli` passes.
- **Left as is:** a daemon start still makes 3 decrypts, one per distinct store: the daemon's own `OpenSecrets`, gitproxy's store, and the shared repo-token store. That is "once per store per process" and bounded. Folding `OpenSecrets` and gitproxy's cache onto `OpenShared` would make it 1. That is filed as a follow-up.

### F2 — the plan's downgrade (item 2) cannot start 61668e58 on the same database

`GRAIN_GITHUB_CREDENTIALS=files` on HEAD restores every file correctly, but 61668e58 then exits with `database schema is newer than this build: found 142, this build knows 131`. HEAD migrates 131→142 in place and keeps no pre-migration copy. That is the schema rule rather than a secrets bug. The plan's wording ("start 61668e58 on the same dir: everything works") is not reachable without restoring a pre-upgrade database. Neither `design/design/secrets-key-protection.design.md` nor the CLI help says that a downgrade needs a database from before the upgrade. Filed as a docs follow-up. The credential half was verified by giving 61668e58 a fresh store beside the restored files (item 2b).

### F3 — on `-host-sandboxes` a run holds the owner's token (by design, noted)

The run's MCP server talks to the daemon with `GRAIN_LOCAL_API_TOKEN_FILE`, the deployment's access code, and the agent process can read that file. So from a host-sandbox run, `POST /api/secrets/redact` answers 200. The daemon says so at start ("host sandboxing … with NO isolation: every run's commands execute as this daemon's own user"), so this is the documented property of host sandboxes, not a defect in the route. The route itself returns 401 with no credential, and its roles are pinned by `tests/security` (`route_scopes.go`: `internal`). The plan's "agent token or limited role gets 403/404" needs a deployment that mints per-run tokens (Kubernetes/granule), which this sandbox cannot run. **Not walked here.**

### Smaller observations (no change)

- **Re-created credential files keep the operator's mode.** A `bot.token` an operator re-creates with mode 0644 is imported and renamed `bot.token.migrated` still at 0644. `secrets/github` is 0700, so nothing is exposed.
- **`key retired delete -all` is all-or-nothing.** While one archive is still sealed to, it refuses outright and deletes no other archive either.
- **A held literal header is not moved when its derived secret name already holds a different value.** The daemon logs `… not moved into the secrets store: capability-server-…/header-Authorization already holds a different value`, and the literal stays in the database. Seen only because item 8 was run twice on one store, with different random values.
- **`request_secret` matching is case-sensitive.** `GitHub-Credential-Bot` and `Gemini-API-Key` are accepted. The store is case-sensitive and grain reads only the lower-case names, so a run cannot overwrite grain's own secret this way.
- **The sandbox-access CA moves on first use, and only on Kubernetes deployments.** A host-sandbox daemon never opens it, so its file stayed in place (and byte-identical). "Sandboxes still trust the sandbox-access CA" needs a Kubernetes deployment. **Not walked here.**

## Steps and evidence

Paths are under `/var/tmp/ss/walk`. Output is trimmed; `<pub>` stands for a public key.

### 1. Upgrade with real files ([`seed_old.sh`](2026-11-11-secrets-store/seed_old.sh), [`items_1_2.sh`](2026-11-11-secrets-store/items_1_2.sh))

The seed uses 61668e58 throughout:

- a PAT via `grain connections import`;
- `ci-app.app.json` (RSA App key) and `deploy-ssh.ssh.json` (ed25519), the files 61668e58 reads;
- the secrets-repo token, written by the old daemon's `POST /api/state-repo/secrets-repo`;
- `state-repo-token`, in `writeRepoToken`'s format;
- `package-cache-ca.pem`, from the old daemon's first start;
- `sandbox-access-ca.pem` and `release-approval-key.pem`, from the old build's own `sandboxaccess.LoadOrCreateCA` and `releaseapproval.LoadSigner` ([`seedca.go`](2026-11-11-secrets-store/seedca.go)).

HEAD's first start:

```
grain: migrated the store in place from schema 131 to 142
gitproxy: moved git credential "bot" … (secret github-credential-bot, key token); the file is kept as …/bot.token.migrated
gitproxy: moved git credential "ci-app" … (secret github-credential-ci-app, key app.json) …
gitproxy: moved git credential "deploy-ssh" … (secret github-credential-deploy-ssh, key ssh.json) …
grain: moved …/secrets/secrets-repo-token into secret secrets-repo-token (kept as ….migrated for a rollback)
grain: moved …/secrets/state-repo-token into secret state-repo-token (kept as ….migrated for a rollback)
grain: moved the CA bundle …/package-cache-ca.pem into secret package-cache-ca (kept as ….migrated …)
600    858 ./package-cache-ca.pem.migrated
600    227 ./release-approval-key.pem          (moves on first use: see 1f)
600    834 ./sandbox-access-ca.pem             (moves on first use, Kubernetes only)
600     36 ./secrets/github/bot.token.migrated
600   1804 ./secrets/github/ci-app.app.json.migrated
600    474 ./secrets/github/deploy-ssh.ssh.json.migrated
600     36 ./secrets/secrets-repo-token.migrated
600     35 ./secrets/state-repo-token.migrated
github-credential-bot/token fd791ac35ddc…   pat file digest: fd791ac35ddc
```

- **1b** A real run commits and pushes from its own `run_command`. Its branch `grain/…/task-2-push-after-upgrade-pat` reaches ghsim, and ghsim saw `Basic` git auth. The daemon also pushed the encrypted secrets to the secrets repo with the migrated repo token.
- **1c** Restart: `file tree unchanged across restart`, and no `moved` lines.
- **1d** `bot.token` re-created with a new value: the store's digest becomes the new file's digest (`1e086051f562`), and the file is renamed `.migrated` again.
- **1e** The connection switched to `ci-app`: the run pushed `…task-3-push-after-upgrade-app`, and App token mints went 0 → 3, signed with the key from `github-credential-ci-app/app.json`.
- **1f** `grain release approval-key` against the daemon: `release approval: moved the signing key …/release-approval-key.pem into secret release-approval-key (kept as ….migrated …)`.

### 2. Downgrade

- **2a** HEAD with `GRAIN_GITHUB_CREDENTIALS=files` logs `wrote … back out of the secrets store` for bot.token, ci-app.app.json, deploy-ssh.ssh.json, package-cache-ca.pem, state-repo-token and secrets-repo-token, and restores release-approval-key.pem. Afterwards `grain secrets list` holds none of them, and `CA bundles and approval key byte-identical to the 61668e58 originals`.
- **2b** 61668e58 on the same dir: `database schema is newer than this build: found 142, this build knows 131` (F2). On a fresh store beside the restored files, 61668e58 pushed `…task-2-push-on-61668e58-after-files` with the App credential (mints 4 → 7). `61668e58 signs with the same approval key`, and `CA bundles unchanged after 61668e58 start`.

### 3. KMS round trip ([`item_3.sh`](2026-11-11-secrets-store/item_3.sh))

The KMS key is `projects/demo-project/locations/us/keyRings/grain-walkthrough-keyring/cryptoKeys/grain-secrets-key-with-a-deliberately-long-name`.

- **3a Wrap with the daemon up.** It went through `POST /api/state-repo/secrets-key/wrap` (daemon log: `grain: wrapped the secrets key …`), with KMS counters encrypt 1 / decrypt 1. The file now starts `grain-secret-key-kms-v1:`. The daemon was the same process, and the next run cloned. `GET /api/state-repo` reports `secretsKeyProtection: kms`, `secretsKmsKey` and `secretsKeyLastUnwrap`.
- The plaintext-copy list `wrap` prints was accurate: no `secrets.key.replaced-*` and no `/run/grain-secrets` existed, and none were listed. Its escrow note is printed unconditionally.
- **3b Restart.** See F1 for the per-process count before and after the fix. After the fix, an idle minute made 0 decrypts.
- **3c KMS refuses at start.**
  - With `deny` (403), the daemon comes up and keeps answering its API. The status drops `secretsKeyLastUnwrap`, and the daemon logs `secrets: KMS permission denied for projects/…: decrypt: HTTP 403: Permission 'cloudkms…' denied`.
  - With `down` (503), it logs `secrets: KMS unreachable for …: decrypt: HTTP 503`.
  - In both cases it recovered on backoff about 11 s after KMS answered, and the next run cloned (App mints moved) without a restart.
- **3d Unwrap with the daemon up:** `unwrapped …: it is a plaintext key file again`, the file starts `grain-secret-key-v1:`, and `secretsKeyProtection: file`.
- **3e Wrap and unwrap with no daemon:** `(no daemon answered, so this command changed the key file itself)` both ways, and the store resolves in between.

### 4. Retired keys ([`items_4_5.sh`](2026-11-11-secrets-store/items_4_5.sh), on a copy of the data dir)

- `key import` of a key that does not open `secrets.enc` is refused: `this file is encrypted to a different key`.
- Two restores from other hosts, each a `secrets.enc` sealed to a fresh key (generated by [`newkey.go`](2026-11-11-secrets-store/newkey.go)) followed by `key import`, left `secrets.key.replaced-20261112-102702` and `…-102703`.
- `key retired`, `key retired list` and `state status` all list both, by name, public half and time.
- `delete secrets.key`, `delete secrets.enc` and `delete ../secrets.key` are each refused with `no retired key "…" beside …`. A bare `delete` prints usage.
- After restoring the file sealed to k1: `refusing to delete secrets.key.replaced-…-102703: …/secrets.enc is encrypted to it and not to the live key -- install it again with grain state key import first`. `delete -all` is refused the same way.
- Back on a file the live key opens, `delete -all` deletes both, and the store still resolves.

### 5. Export refusal and bundle import

- `grain export` with no encryption: `refusing to write an unencrypted bundle: it would carry …/secrets/secrets.key, the private key that opens every secret in it. Encrypt it with -passphrase-file or -key-file, or pass -plaintext …`. Exit 1, and no file is written.
- `-plaintext`, `-passphrase-file` and `-key-file` each write a 0600 bundle (17 files).
- Each bundle imports into an empty data dir: `state repo: verified`, `credentials: loaded 3 credentials`. `github-credential-bot/token` and `package-cache-ca/bundle.pem` resolve to the source's digests.
- A wrong passphrase gives `authentication failed (incorrect passphrase/key or corrupted bundle)`.

### 6. Redaction through the daemon ([`item_6.sh`](2026-11-11-secrets-store/item_6.sh))

- **6a** One line was written into the unit's journal holding a stored secret's value (`walk-redact/value`) and the migrated PAT. A run granted `grain#journal` by the owner (`POST /api/tasks/{id}/capabilities`) calls `read_grain_log {"source":"daemon","contains":"walk-marker-1"}` and gets `walk-marker-1 stored=[redacted: secret walk-redact/value] pat=[redacted: secret github-credential-bot/token]`. The planted values appear **0** times in the answer.
- **6b** The run reads once with the daemon up, pauses, the daemon is frozen (SIGSTOP), and the run reads again. Up: the lines come back redacted. Frozen: `[withheld: this line could not be redacted]` × 5, values visible 0, and not an error. A real `systemctl stop` cancels the daemon's runs with it, so freezing is how "the daemon stops answering mid-run" was produced.
- **6c** With no credential, `POST /api/secrets/redact` answers 401. From the run, see F3.

### 7. `request_secret` refusals from a real run

A run called `request_secret` for each of these names. Every one was refused with `"<name>" is where grain keeps its own credentials or signing material (a git credential, a CA, a signing key, a model or minter key, a release secret or a repository token), which a run may not ask for …`:

- `github-credential-bot`
- `github-credential-ci-app/app.json`
- `gemini-api-key`
- `claude-oauth-token`
- `openai-api-key`
- `gcp-key-minter`
- `gcp-key-minter-prod`
- `release-approval-key`
- `github-app`
- `github-app/private-key`
- `image-signing`
- `agent-backend-ag-walk`
- `state-repo-token`
- `sandbox-access-ca`

An ordinary `walk-ordinary-token` was accepted (`Recorded. When this run finishes, grain asks for walk-ordinary-token …`). `" github-credential-bot"` (leading space) is refused as not a valid name. The case variants are noted under "Smaller observations".

### 8. Typed-in credentials ([`item_8.sh`](2026-11-11-secrets-store/item_8.sh))

This uses a throwaway clone of the walkthrough deployment's own `grain/config` (`data/state-repo`). `main` gains a capability server with a literal `Authorization` header, as a store from before the rule holds.

- `grain state check -base main` on `main` gives `warning: … carries a credential typed in at header Authorization, as the branch this is against already does …` and `ok`. Exit 0.
- A PR branch adding a second literal: `grain state: this change types 1 credential(s) into capability servers …` names only `https://caps.example.com/typed-in: header Authorization` and still warns about the held one. **Exit 1**, and no value is printed.
- The same PR with `$secret:walk-cap-token` instead: exit 0, with the held warning only.
- API: `PUT /api/capability-servers` with a literal gives **400** `header Authorization holds a credential typed in; store it as a secret and write $secret:<name> instead …`, and nothing reaches the database. A `$secret:` reference gives 200.
- A literal planted in the live database the way a pre-rule build left it: on the next start the daemon moves it (`moved a typed-in credential into the secrets store: … is now $secret:capability-server-…/header-Authorization`). Editing that held server (a new `serverUrl`, header unchanged) answers 200.

### Settings → State ([`state-ui.mjs`](2026-11-11-secrets-store/state-ui.mjs), [`shots/`](2026-11-11-secrets-store/shots/))

The tab is "config repo" (`/settings/config-repo`, `StateRepoPanel.jsx`), rendered with the key KMS-wrapped. At each size and theme the script asserts:

- the long key name is shown;
- it sits inside the viewport and inside its own container;
- the page does not scroll sideways;
- the page mentions KMS and the last unwrap.

| Size | Theme | Key name | Lines | Side scroll |
|---|---|---|---|---|
| 1440×900 | light | inside | 2 | none |
| 1440×900 | dark | inside | 2 | none |
| 390×844 | light | inside | 6 | none |
| 390×844 | dark | inside | 6 | none |

The screenshots are `shots/state-{1440,390}-{light,dark}.png`, the key-name crops are `shots/state-key-*.png`, and `shots/contact-sheet.png` holds all four.

## Not walked

- Sandboxes trusting a migrated sandbox-access CA, and an agent token or limited role against `POST /api/secrets/redact`. Both need a Kubernetes/granule deployment, which this sandbox cannot run (F3).
- PostgreSQL. Every daemon here ran on SQLite.

## Follow-ups filed

- Fold `daemon.OpenSecrets` and `pkg/gitproxy`'s per-key-file cache onto `secrets.OpenShared`, so a KMS-wrapped key costs one unwrap per process rather than one per store (F1).
- Document the downgrade path for the credentials move: it needs a pre-upgrade database, and `GRAIN_GITHUB_CREDENTIALS=files` only covers the files (F2).
