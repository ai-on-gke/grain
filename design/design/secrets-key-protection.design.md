# Protecting the secrets key, rotating it, and the credentials outside the store

**Owner:** operator (investigation filed 2026-11-10, task 3880)

**Status:** investigation and recommendation. Nothing here is built yet. It
describes `main` as of 2026-11-10 (merge `d79c6aff`). Every claim about current
behaviour cites the file and line it was read from. No real secret value was
read while writing it.

**The recommendation in one paragraph:** keep the sealed-box file store
(`pkg/secrets`). Put one *key source* seam in front of the private key
([Phase 0](#SKP-P0)). Ship `grain state key rotate`, a key rotation that can
crash at any step and still read ([Phase 1](#SKP-P1)). Then make a **Cloud KMS
wrap of `secrets.key`** the default on GCE and GKE ([Phase 2](#SKP-P2)): the
same X25519 key, sealed by a KMS key the host's service account (or Workload
Identity) may only use, never export. Wrapping changes nothing in
`secrets.enc`, so it is zero-downtime and reversible by construction. Move the
loose credentials that *can* move into the store
([Phase 3](#SKP-P3)), and give the Postgres password a rotation and one
fewer copy ([Phase 4](#SKP-P4)). Workstations, Cloudtops and OSS hosts with no
cloud keep the file key by default, with hardening. A TPM/`systemd-creds` key
source and an OS-keychain key source are optional later phases
([Phase 5](#SKP-P5)). The existing Secret Manager *backend* stays the answer
for hub-managed instances, and needs a migration path it does not have yet
([Phase 6](#SKP-P6)).

---

## 0. Two corrections to the brief {#SKP-0}

The brief says `secrets.enc` lives in the state repository and the hub
registration token is a loose file. On `main` neither is quite true any more.
Both corrections matter to the design.

1. **`secrets.enc` is no longer in the state repository.** It moved to
   `<data-dir>/secrets/secrets.enc`, beside the key
   (`pkg/daemon/staterepo.go:109-137`). The reason: once tasks could be
   dispatched at the state repository, any sandbox that could clone it got
   every object in it (`pkg/daemon/staterepo.go:113-121`). On upgrade,
   `moveSecretsOutOfStateRepo` renames an old copy out
   (`pkg/daemon/staterepo.go:222`). The repo's `.gitignore` now names
   `secrets.enc`, `secrets.key` and `*.key` (`pkg/staterepo/bind.go:1491-1507`).
   Ciphertext still leaves the host in two ways:
   - an **optional secrets repository** of its own (`pkg/secretsrepo`). It is
     a git repo holding only the ciphertext, pushed on a timer, which the git
     proxy refuses to every sandbox (`pkg/secretsrepo/secretsrepo.go:15-60`,
     `pkg/daemon/staterepo.go:249-258`);
   - the **history of state repositories written by older builds**, which
     `staterepo.HasSecrets` still detects (`pkg/staterepo/staterepo.go:918-947`).

   So the brief's point about history holds, for the secrets repository and
   for legacy state repos: old ciphertext stays readable to whoever holds the
   key it was sealed to.
2. **The hub registration token is already in the store,** as
   `registry-registration-token` (`pkg/secrets/secrets.go:89-93`). A file is
   only a flag-driven fallback (`HubTokenFile`,
   `pkg/daemon/config.go:896-903`).

The weak points the brief names still stand. The private key is a plaintext
0600 file on the same disk (`pkg/secrets/crypto.go:154-176`), and it is
re-read on every load and save (`pkg/secrets/secrets.go:490`, `:513`). Nothing
rotates it: `grain state key` offers `show|path|import` only
(`pkg/cli/state.go:80-82`, `:540`). And many credentials live outside it
([§1](#SKP-1)).

---

## 1. Inventory: credential material at rest {#SKP-1}

**Legend.**
- **Who reads it:** `grain` is the daemon's account, `GRAIN_USER`, default
  `grain` (`scripts/setup.sh:282`). The daemon container runs as that uid
  with the data dir bind-mounted (`scripts/setup.sh:7653-7658`).
- **Root-equivalent:** root, plus the `docker` group whenever the daemon
  container has the docker socket. It gets the socket when granule,
  container sandboxes or UI upgrade are on (`scripts/setup.sh:7688-7695`), and
  in that case the daemon's own uid is root-equivalent too.
- **On Cloudtop installs,** grain runs as the installing user
  (`pkg/ui/fileowner_unix.go:31`), so "grain" means every process of that
  person.
- **Backups:** `grain backup` uploads the database and a config bundle, and
  never credentials or `secrets/` (`pkg/cli/backup.go:37`). `grain export`
  copies the *whole* `<data-dir>/secrets/` tree, key included
  (`pkg/cli/export.go:314-341`), into a bundle whose AES-256-GCM encryption is
  optional (`pkg/cli/export.go:55-65`, `:462`). A disk or PVC snapshot copies
  everything.

### 1.1 The secrets store and its key

| Item | Where | Who reads it | Backed up / committed | Rotation today |
|---|---|---|---|---|
| **Secrets private key** (X25519, `grain-secret-key-v1:` prefix) | `<data-dir>/secrets/secrets.key`, 0600 (`pkg/daemon/staterepo.go:132-137`, `pkg/secrets/crypto.go:154-176`) | grain; root-equivalent. Also read directly by: the daemon store, each per-run `grain mcpserver` for journal redaction (`pkg/daemon/mcpserver.go:1657-1671`), transcript redaction (`pkg/daemon/transcripts.go:239`), `grain secrets` / `grain state` / `grain connections import` on the host (`pkg/cli/state.go:181`, `pkg/cli/connections.go:301`), `grain import` (`pkg/cli/import.go:233`), and the git proxy as a legacy fallback for its token file (`pkg/gitproxy/tokens.go:157-180`) | Not committed (`.gitignore`). In `grain export`. On GCE, optionally escrowed **in plaintext** in Secret Manager as `secrets-key` and seeded by setup.sh (`deploy/terraform/gcp/secrets.tf:42-53`, `deploy/terraform/gcp/files/deploy.sh:388`, `scripts/setup.sh:6138`). On GKE, on the PVC (`deploy/kubernetes/grain.yaml:315-329`) | **None.** `ImportKey` replaces it with a key that already opens the file, and moves the old one aside as `secrets.key.replaced-<ts>`, never deleted (`pkg/secrets/secrets.go:365-404`, `:392`). Those archives accumulate in the same directory |
| **Encrypted store** (`secrets.enc`, sealed box: X25519 + HKDF-SHA256 + AES-256-GCM, fresh ephemeral key per write) | `<data-dir>/secrets/secrets.enc`, 0600 (`pkg/secrets/secrets.go:512-547`, `pkg/secrets/crypto.go:183-203`) | grain | Optional secrets repository, **with history** (`pkg/secretsrepo/secretsrepo.go:15-60`). In legacy state-repo history (`pkg/staterepo/staterepo.go:918-947`). In `grain export` | Each value is replaced by `Set`. `Store.Rotate` regenerates values grain makes itself (`pkg/secrets/secrets.go:871`). Nothing re-keys the file |
| What the store holds today | Agent keys (`gemini-api-key`, `claude-oauth-token`, `openai-api-key`, `agent-backend-<id>`), `registry-registration-token` (`pkg/secrets/secrets.go:75-99`, `:202`), the GCP minter key `gcp-key-minter` (`scripts/setup.sh:6212-6242`), `database-password` for a saved DSN (`pkg/model/postgres/upgrade.go:34-42`), `relay/token` (`pkg/daemon/run.go:81-88`), capability-server data-plane tokens (`Ensure`, `pkg/secrets/secrets.go:852`), `request_secret` answers | — | — | — |
| **Secret Manager backend** (`-secrets-backend=secretmanager`) | GCP Secret Manager under `-secrets-sm-prefix`. Names and versions (never values) catalogued in the DB's `secret_entries` (`pkg/secrets/secretmanager.go:63-81`, `pkg/daemon/staterepo.go:139-177`) | The metadata-server identity (`pkg/secrets/secretmanager.go:496-547`); a 30 s in-memory cache (`:46-48`) | Secret Manager versioning | Each `Set` adds a version and destroys superseded ones (`design/design/hub.design.md`, "Secrets in Secret Manager"). There is no key |

### 1.2 Loose files under `<data-dir>/secrets/`

| Item | Where | Who reads it | Backed up / committed | Rotation today |
|---|---|---|---|---|
| **GitHub credentials:** PAT `<name>.token`, App `<name>.app.json` (holds the App **private key**), SSH `<name>.ssh.json`, plus `<name>.host.json` | `<data-dir>/secrets/github/` (`pkg/gitproxy/credentials.go:17-37`, `pkg/gitproxy/sshkey.go:17-31`, `pkg/gitproxy/hosts.go:17-23`). Seeded by setup.sh (`scripts/setup.sh:6131-6132`); written by Settings → GitHub (`pkg/ui/github_tokens.go:30-45`) | grain (the git proxy runs in the daemon) | `grain export`. GCE: `github-token` / `github-app-*` in Secret Manager (`deploy/terraform/gcp/secrets.tf:42-53`) | Manual: replace the file and restart. App installation tokens are short-lived and re-minted; the App key itself is not |
| Agent key files seeded by setup.sh: `gemini-api-key`, `claude-oauth-token`, `openai-api-key` | `<data-dir>/secrets/<name>` (`scripts/setup.sh:6157-6159`) | grain. Copied into the store as `agent-backend-<id>` by `grain connections import` (`pkg/cli/connections.go:292-305`), **but the file stays** and remains the `-gemini-api-key-file` fallback (`pkg/daemon/agents.go:2023-2036`) | `grain export`; GCE Secret Manager | Manual |
| **Local Postgres password** | `<data-dir>/secrets/postgres-password`, 0600, generated once and "never replaced" (`scripts/setup.sh:2834-2836`, `:3002-3016`) | grain; the Postgres container reads it at initdb only (`scripts/setup.sh:3069`) | `grain export` (it is under `secrets/`) | **None** |
| **Codex login** (OAuth, rotating refresh token) | `<data-dir>/secrets/codex-auth.json` (`pkg/daemon/agents.go:1663-1664`), linked into each run's `CODEX_HOME` and rewritten in place by the CLI (`pkg/agent/codex/codex.go:294-305`) | grain; the codex subprocess on the host | `grain export` | Self-rotating (`pkg/agent/codex/refresh.go`) |
| Git proxy sandbox tokens | `sandbox-tokens.json`, sealed to its own `sandbox-tokens.key` (`pkg/gitproxy/tokens.go:94-108`) | grain | `grain export` | Per run, 24 h TTL (`pkg/gitproxy/tokens.go:66-68`) |
| Model proxy tokens | `secrets/modelproxy-<vendor>-tokens.json` (`pkg/daemon/modelproxy.go:266-277`) | grain | `grain export` | Per run; revoked on restart for ended runs |
| State-repo / secrets-repo git tokens | `secrets/state-repo-token`, `secrets/secrets-repo-token`, 0600 (`pkg/daemon/statemanager.go:1195-1212`) | grain | `grain export` | Manual |
| Relay token (file fallback) | `secrets/relay/token` (`pkg/daemon/run.go:86`) | grain | `grain export` | Manual |
| Staged GCP minter key | `secrets/.minter-key.staged.json`, transient during setup.sh (`scripts/setup.sh:6237`) | grain | — | push-secrets.sh mints a fresh one and prunes all but the newest two (`scripts/setup.sh:6186-6191`) |

### 1.3 Loose files at the root of `<data-dir>`

| Item | Where | Who reads it | Backed up / committed | Rotation today |
|---|---|---|---|---|
| **Access code = `LocalAPIToken`** (160-bit) | `<data-dir>/local-api-token`, 0600, or 0640 for the operator group setup.sh grants (`pkg/ui/localtoken.go:44`, `:61-93`, `pkg/ui/accesscode.go:40-55`) | grain; the host CLI; every per-run `grain mcpserver` through `GRAIN_LOCAL_API_TOKEN_FILE` (`pkg/daemon/ui_server.go:250-264`) | Not in `grain export` or backup (outside `secrets/`); in snapshots | `grain access-code rotate` (`pkg/cli/accesscode.go:33-45`, `pkg/ui/accesscode.go:157`) |
| **Session-cookie signing key** | **Not stored.** Derived as `SHA-256("grain access session\0" ‖ code)` (`pkg/ui/accesscode.go:244-247`) | — | — | Rotates with the access code |
| **Release-approval signing key** (ECDSA P-256 PEM) | `<data-dir>/release-approval-key.pem`, or `Config.ReleaseApprovalDir` (`pkg/releaseapproval/releaseapproval.go:63-64`, `pkg/ui/release_approval_github.go:279-286`), created 0600 (`pkg/attest/dsse.go:193-204`) | grain | Not in export or backup | **None.** Changing it means updating the `GRAIN_RELEASE_APPROVAL_KEY` repo variable (`pkg/ui/release_approval_github.go:259-276`) |
| **Package-cache CA** (TLS interception root that sandboxes trust) | `<data-dir>/package-cache-ca.pem`, cert + EC private key, 0600 (`pkg/daemon/packagecache.go:305-310`, `pkg/pkgcache/ca.go:173-176`) | grain | Not in export | Regenerated 30 days before expiry (`pkg/pkgcache/ca.go:42`) |
| **Sandbox-access CA** | `<data-dir>/sandbox-access-ca.pem`, or Secret Manager when `SandboxAccessCASecret` is set (`pkg/daemon/sandbox_access.go:96-99`) | grain | Not in export | On expiry |
| `db-config.json` | DSN **without** password; the password is in the store as `database-password` (`pkg/model/postgres/upgrade.go:34-56`) | grain | `grain export` | — |
| Store DB, transcripts, journal | `store/`, `postgres/`, `state/transcripts` | grain | `grain backup` | Not credentials. Transcripts are redacted against `KnownValues` (`pkg/daemon/transcripts.go:228-245`) |

### 1.4 `/etc/grain/*`, `/run`, and other hosts

| Item | Where | Who reads it | Backed up / committed | Rotation today |
|---|---|---|---|---|
| **Postgres DSN** (holds the password) | `/etc/grain/postgres.env`, root 0600, read by systemd as an `EnvironmentFile` and passed in by name (`scripts/setup.sh:2842-2850`, `:3039-3048`). Deliberately outside the data dir, so the daemon's account cannot plant `LD_PRELOAD` | root | No | **None** |
| Operator env file | `/etc/grain/daemon.env`, `--env-file` into the container (`scripts/setup.sh:7526`, `:7712`) | root, and the daemon environment | No | Operator's own. Anything put there is plaintext |
| Public / non-secret | `/etc/grain/cosign.pub`, `image.env`, `cli.env`, `hostd.yaml`, `hostd.env` (proxy settings) | — | — | — |
| GCE boot-time secrets | `/run/grain-secrets` tmpfs, fetched from Secret Manager by `deploy.sh`, mounted read-only into the container. The minter key lands at `minter-key.json` there (`deploy/terraform/gcp/files/deploy.sh:340-401`, `scripts/setup.sh:7713`) | root; container ro | Source of truth is Secret Manager | push-secrets.sh |
| Host (`grain hostd` / agentd) identity | `-key-file` Ed25519 private key 0600 and `-token-file` credential on each host (`pkg/agentd/agentd.go:143-144`, `pkg/hostd/enroll.go:171-230`); `/var/lib/grain-hostd` | that host's account | No | `-rekey`; `grain host rotate-code` for enrollment codes (`pkg/cli/host_enroll.go:274`) |
| `grain login` context secrets (client side) | OS keyring (Secret Service via `secret-tool`, macOS Keychain), else `~/.config/grain/credentials/<ctx>` 0600 (`pkg/cli/credentials.go:28-35`) | the CLI user | No | Log in again |
| `grit serve` attestation key | `<grit-data-dir>/attest.key` (`cmd/grit/serve.go:41-60`) | grit | No | None |

### 1.5 Credentials that exist only per run

- **gcp-key leases:** a minted service-account JSON lands *in the sandbox* at
  `/home/debian/.gcp-service-account[-<name>].json`
  (`pkg/capability/gcpkey/gcpkey.go:76-91`), with a 24 h max age
  (`pkg/capability/gcpkey/gcpkey.go:100`), deleted on release and reaped. In
  `credential: token` mode no key exists at all
  (`design/design/gcp-access.design.md`). On the controller the only
  long-lived material is the minter key, which is in the store.
- **Agent CLI homes:** the Claude OAuth token reaches the subprocess as
  `CLAUDE_CODE_OAUTH_TOKEN`. A Vertex credential is a 0600 temp file removed
  after the run (`pkg/agent/claude/terminal.go:206-215`). Antigravity and
  Codex write 0600 config into a per-run home
  (`pkg/agent/antigravity/antigravity.go:1254`, `pkg/agent/codex/codex.go:1029`).
  Codex conversations persist under `state/agent-sessions`
  (`pkg/daemon/staterepo.go:102-107`), but the login itself is the one shared
  `codex-auth.json` above.

**What the inventory says, in short.** The store key protects about half of
grain's long-lived credentials. The most valuable single item outside it is
the GitHub App private key (`secrets/github/*.app.json`). Next come the
release-approval key, the package-cache CA key and the Postgres password,
which has no rotation at all. Everything under `secrets/`, the key included,
travels together into `grain export` and every snapshot.

---

## 2. Threat model {#SKP-2}

Protection options are scored against these threats:

| # | Threat | Status quo |
|---|---|---|
| T1 | **Data at rest off the running host:** a disk or PVC snapshot, a decommissioned disk, a copied data dir, an unencrypted `grain export` bundle | **Exposed:** the key and ciphertext sit side by side |
| T2 | **Code execution as grain or root on the running host** | Exposed. No local scheme fixes this: the process has to be able to decrypt to work. Achievable goals: an *audit trail*, *revocation* that stops the next unwrap, and *not leaving the key behind* after the attacker is evicted |
| T3 | Another unprivileged account on the same host | Protected by 0600 / 0700. **Not** on Cloudtop installs, where grain runs as the person (`pkg/ui/fileowner_unix.go:31`), so any process of theirs can read it |
| T4 | A sandbox / agent | Protected. The key never enters a sandbox, and the secrets repo is refused (`pkg/daemon/staterepo.go:249-258`) |
| T5 | **Old ciphertext in git history** (secrets repo, legacy state repo) once a key leaks | Exposed **forever** for every value that was in any pushed version. Only rotating those values helps ([§4.3](#SKP-4-3)) |

---

## 3. Key protection options {#SKP-3}

### 3a. Cloud KMS envelope: wrap `secrets.key` {#SKP-3a}

**Shape.** The X25519 private key is 32 bytes, far under KMS's 64 KiB
symmetric-encrypt limit, so grain wraps **the key itself**. A per-file DEK is
not needed: the sealed box already gives each write a fresh AES key
(`pkg/secrets/crypto.go:178-203`). `secrets.key` becomes:

```
grain-secret-key-kms-v1:<projects/P/locations/L/keyRings/R/cryptoKeys/K>:<base64 KMS ciphertext>
```

The AAD is `"grain secrets key v1\0" ‖ <public key>`, so a wrapped blob cannot
be swapped for another deployment's.

**Unwrap.** One `cryptoKeys.decrypt` call at the first load. The daemon then
keeps the key in memory for its lifetime. The token comes from the metadata
server, exactly as `smBackend.token` gets one
(`pkg/secrets/secretmanager.go:496-547`), which also covers GKE Workload
Identity through the GKE metadata server.

**IAM.** The host service account gets `roles/cloudkms.cryptoKeyEncrypterDecrypter`
on **its own key only**. Encrypt is needed only to wrap a newly rotated key; a
deployment that rotates only from an operator workstation can grant
`cryptoKeyDecrypter` alone.

**KEK rotation.** Turn on KMS automatic rotation (e.g. 90 days). The decrypt
response's `usedPrimary` tells grain the blob is under an old version, and it
rewraps at startup. KEK rotation is free and invisible. It does **not**
replace rotating the X25519 key ([§4](#SKP-4)).

| | |
|---|---|
| **Security gained** | **T1 closed:** a snapshot, an export bundle or a stray copy of the key file is useless without `decrypt` on that KMS key. **T2 improved:** every unwrap is a Cloud Audit Logs Data Access entry, and disabling the key version or revoking IAM stops every later unwrap (a running daemon keeps its in-memory copy until restart). It also kills a key that was copied off-host *wrapped*. T3 on Cloudtop is unchanged, since the person's processes share the identity. T5 unchanged |
| **KMS down** | The daemon must not refuse to start. Today an unreadable store is logged and the daemon keeps serving (`pkg/daemon/staterepo.go:197-207`); the same holds here. The UI, the API and runs already dispatched keep working. Anything that resolves a secret fails with a clear "KMS unreachable" error, and a background retry with backoff unlocks the store when KMS answers. A *running* daemon is unaffected by an outage, because it unwrapped once at start |
| **Caching** | In memory only, for the process lifetime. **No on-disk cache**, which would just be the plaintext key again. Child processes that read the key today (per-run `grain mcpserver`, transcript redaction, host CLI) should ask the daemon over the local API instead of each calling KMS ([Phase 0](#SKP-P0)). Offline CLI use with the daemon stopped unwraps directly |
| **Cost** | One software key: about $0.06 per active key version per month, plus about $0.03 per 10 000 operations at list price (check current pricing). With one unwrap per process start, the total is cents a month. An HSM key is about $1–2.50 per month and buys nothing material here |
| **Ops burden** | Create a key ring and key in the deployment's region (Terraform can do it, beside `deploy/terraform/gcp/secrets.tf`), and grant one role. A new failure mode to monitor: "key disabled / IAM removed" means **every secret is unreadable on the next restart**. That is the point, but it must be a loud, specific error. The GCE `secrets-key` escrow should then hold the *wrapped* key, which removes today's plaintext copy in Secret Manager |
| **Fit** | **GCE (terraform/gcp): excellent**, with the metadata SA already in place. **GKE: excellent** via Workload Identity on the `grain` KSA (`deploy/kubernetes/grain.yaml:403`), and it closes PD-snapshot exposure of the PVC. **Cloudtop: opt-in only.** It is a GCE VM, but its service account may not be one the operator can grant roles to, and user ADC credentials expire and need interactive re-auth, which is [3e](#SKP-3e)'s problem again. The decision belongs to the installer that lives outside this repo. **Workstation without GCP / OSS with no cloud: not applicable** |

### 3b. TPM / vTPM sealing, or the Linux kernel keyring {#SKP-3b}

**vTPM sealing (Shielded VMs).** Seal the X25519 key to the instance's vTPM:
storage-root-key–parented, optionally bound to a PCR policy. A pure-Go TPM
client exists, so no cgo is needed. The daemon container needs
`--device /dev/tpmrm0` and the `tss` group.

| | |
|---|---|
| **Security gained** | T1 closed for snapshots and copies: the blob opens only on *that* VM's vTPM. No network dependency. T2 unchanged, and no audit log |
| **Failure modes** | **VM recreation loses the key.** A Terraform replace, or a host rebuilt onto a fresh disk, gets a new vTPM, so an escrow is mandatory. That escrow is today's plaintext `secrets-key` in Secret Manager, which reintroduces the exposure unless it is itself KMS-wrapped, at which point 3a alone is simpler. PCR binding breaks on kernel or firmware updates unless the policy is limited to stable PCRs. A non-Shielded VM has no vTPM |
| **Cost / ops** | No cloud cost. Real engineering: device passthrough, TPM handle management, re-seal on policy change, and a recovery story |
| **Fit** | GCE: possible, but strictly worse than KMS there. **GKE: no.** A pod reschedules onto another node with another vTPM. Bare-metal / OSS hosts with a TPM 2.0: plausible, best reached through `systemd-creds` ([3f](#SKP-3f)) rather than grain speaking TPM itself |

**Kernel keyring.** Keys in the keyring disappear at reboot, so the key still
needs a persistent source. A keyring is a *cache*, not protection. Docker's
default seccomp profile blocks `add_key`, `keyctl` and `request_key`, so the
daemon container cannot use one without loosening seccomp. **Rejected.**

### 3c. OS keychain / Secret Service on workstations {#SKP-3c}

There is precedent: `grain login` already stores CLI secrets in the OS keyring
through `secret-tool` or `security`, and falls back to a 0600 file
(`pkg/cli/credentials.go:28-75`).

| | |
|---|---|
| **Security gained** | T1 closed for data-dir copies: the key is not in the data dir. T2/T3 unchanged. An unlocked login keyring answers any process of the same user over D-Bus |
| **Failure modes** | The keyring exists only in a logged-in desktop session. setup.sh's daemon is a **system** unit, inside a container, running as `grain`, with no session bus (`scripts/setup.sh:7515-7540`), so it cannot work there. A daemon started before login, or after the keyring locks, cannot open the store. Headless hosts have none |
| **Cost / ops** | None. A small `KeySource` implementation reusing `pkg/cli/credentials.go`'s keyring code |
| **Fit** | Only `grain daemon` run by hand in a user session, mainly macOS laptops. Not GCE, GKE or setup.sh installs. A Cloudtop's desktop session is possible but its daemon is not a session process. **Optional, low priority** |

### 3d. Secret Manager as the backend {#SKP-3d}

**This already exists** (`pkg/secrets/secretmanager.go`,
`-secrets-backend=secretmanager`, `pkg/daemon/staterepo.go:139-177`), designed
for hub-managed instances (`design/design/hub.design.md`, "Secrets in Secret
Manager"). There is no local key: each secret is its own Secret Manager
secret, IAM-conditioned on the instance prefix, and catalogued in the DB.

| | |
|---|---|
| **Security gained** | T1 closed entirely, with nothing on disk. T2: per-secret audit log, revocable. T5 does not arise: no git history of ciphertext, and superseded versions are destroyed |
| **Failure modes** | **Every resolve goes to Secret Manager,** behind a 30 s cache (`pkg/secrets/secretmanager.go:46-48`). An outage longer than the cache stops dispatch, and the daemon running is no protection. The catalogue lives in the database, so the backend is unusable for anything needed *before* the DB opens, such as a DSN password. Tokens come only from the metadata server or a test env var (`pkg/secrets/secretmanager.go:496-547`), so it needs GCE or GKE |
| **Cost** | About $0.06 per active secret version per month, plus about $0.03 per 10 000 accesses. Tens of secrets and a 30 s cache come to well under a dollar a month |
| **Ops burden** | A prefix-conditioned custom role per instance. Out-of-band deletions surface as `ErrGoneUpstream` (`pkg/secrets/secrets.go:114-121`). **No migration from the file backend exists** (no hits for one in `pkg/cli/secrets.go` or `pkg/secrets`) |
| **Fit** | **Hub-managed GCE instances: the right answer**, as already designed. GCE (terraform/gcp) and GKE: good for operators who want per-secret IAM, but heavier than 3a. Cloudtop, workstations, OSS: no |

### 3e. Passphrase-wrapped key entered at start-up {#SKP-3e}

**Rejected for the running daemon.** grain restarts unattended all the time:
`Restart=on-failure` (`scripts/setup.sh:7534`), UI-driven upgrades
(`GRAIN_ENABLE_UI_UPGRADE`, `scripts/setup.sh:7688`), the non-disruptive
upgrade path (`design/design/non-disruptive-upgrade.design.md`), host reboots
and GKE pod reschedules. Each would leave the deployment unable to resolve a
credential until a person typed the passphrase.

There is also nowhere good to type it:
- `systemd-ask-password` needs a console;
- the web UI is gated by an access code that is itself a file on the same
  disk ([§1.3](#SKP-1)), so a passphrase sent through it protects against
  nothing the code does not;
- to make restarts work, operators would put the passphrase in
  `/etc/grain/daemon.env`, which is T1-equivalent to today.

What a passphrase *is* good for, and already does: **offline** material. That
means `grain export` bundles (PBKDF2 + AES-256-GCM, `pkg/cli/export.go:462`)
and an operator's off-host escrow of the key.

### 3f. Status quo, hardened {#SKP-3f}

- **A separate unix user owning the key.** The daemon must decrypt to work,
  so a separate owner only helps with a broker process that holds the key and
  answers decrypt requests. That is a new privileged service for little gain.
  When the docker socket is mounted (`scripts/setup.sh:7688-7695`), the
  daemon's uid is root-equivalent anyway. **Not worth it.**
- **`systemd` `LoadCredentialEncrypted=` (`systemd-creds`).** This is the
  interesting one. systemd ≥ 250 decrypts a credential at unit start into a
  non-swappable ramfs `$CREDENTIALS_DIRECTORY` readable only by the unit. The
  credential is encrypted with the host key
  `/var/lib/systemd/credential.secret`, a TPM2, or both.
  - `--with-key=tpm2` (or `host+tpm2`) gives 3b's T1 protection with systemd
    doing the TPM work.
  - `--with-key=host` alone keeps the key off the *data dir*, so a data-dir
    copy, `grain export` or a PVC/data-disk snapshot no longer carries it. It
    does not help against a root-disk snapshot.
  - The daemon runs in a container, so the unit must bind-mount
    `$CREDENTIALS_DIRECTORY` read-only into it. setup.sh already mounts
    `/run/grain-secrets` the same way (`scripts/setup.sh:7713`).
  - **Fit:** OSS / bare-metal / workstation Linux hosts with a recent systemd.
    Not GKE.
- **`ProtectSystem=` and friends.** The hostd unit has them
  (`scripts/setup.sh:7921-7926`); the daemon unit does not
  (`scripts/setup.sh:7515-7540`). On the daemon unit they would constrain only
  the `docker` CLI, not the container, so they have **near-zero value**. The
  container boundary is what isolates the filesystem.
- **Cheap hygiene, worth doing regardless** ([Phase 0](#SKP-P0)):
  - stop accumulating `secrets.key.replaced-*` archives in the live directory
    (`pkg/secrets/secrets.go:392`): list them in `grain state status`, and
    offer to delete them;
  - make an unencrypted `grain export` that contains `secrets.key` require an
    explicit `-plaintext` flag, since it is T1 in a file;
  - delete setup.sh's seeded agent-key files once they are in the store
    ([§5](#SKP-5)).

### 3g. Summary {#SKP-3g}

| Option | T1 snapshot / copy | T2 host compromise | Survives VM replace | Needs network at start | GCE | GKE | Cloudtop | Workstation, no GCP | OSS, no cloud |
|---|---|---|---|---|---|---|---|---|---|
| Status quo | ✗ | ✗ | with escrow | no | ✓ | ✓ | ✓ | ✓ | ✓ |
| **a. KMS wrap** | ✓ | audit + revoke | ✓ (same SA) | **yes, once** | **default** | **default** | opt-in | — | — |
| b. vTPM | ✓ | ✗ | ✗ (needs escrow) | no | possible | ✗ | ? | if TPM | if TPM |
| c. Keychain | data-dir only | ✗ | n/a | no | ✗ | ✗ | ✗ | opt-in (session) | opt-in (session) |
| d. SM backend | ✓ | audit + revoke, per secret | ✓ | **every 30 s** | hub instances | opt-in | ✗ | ✗ | ✗ |
| e. Passphrase | ✓ | ✗ | ✓ | no, needs a person | ✗ | ✗ | ✗ | ✗ | ✗ |
| f. systemd-creds | data-dir only (host) / ✓ (tpm2) | ✗ | ✗ with tpm2 | no | — | ✗ | ? | opt-in | **opt-in** |

---

## 4. Rotation {#SKP-4}

### 4.1 Rotating the store key {#SKP-4-1}

A new command, **`grain state key rotate`**, sits beside the existing
`show|path|import` (`pkg/cli/state.go:80-82`). That is where operators already
look for key operations, so the brief's `grain secrets rotate-key` is better
as an alias than as the primary name. It also needs a **Settings → State →
"Rotate secrets key"** action and a `POST /api/state-repo/secrets-key/rotate`
route, next to the existing import route (`pkg/ui/server.go:708`).

**It must run in the daemon.** The CLI goes through the API when a daemon is
up, because the daemon is the only writer and holds `Store.mu`
(`pkg/secrets/secrets.go:240-246`). The CLI writes to disk directly only when
no daemon is running.

**Crash safety without a two-file atomic swap.** The armour header names the
recipient public key in clear (`pkg/secrets/crypto.go:205-219`). Make the
loader hold a small **key ring**: the current key, a staged
`secrets.key.next`, and any retained retired keys. On load it picks the key
whose public half matches the recipient, instead of returning `ErrWrongKey`
(`pkg/secrets/crypto.go:230-233`). Rotation then becomes:

1. Generate key K₂. Write `secrets.key.next` (0600, atomic, through
   `WriteKeyFile`; wrapped under KMS when [3a](#SKP-3a) is on). From here a
   crash leaves the file sealed to K₁ and readable.
2. Decrypt with K₁, re-encrypt to K₂, verify the new ciphertext decrypts,
   then rename it over `secrets.enc`. From here a crash leaves the file sealed
   to K₂, and K₂ is on disk as `.next`, so it is still readable.
3. Rename `secrets.key` to `secrets.key.retired-<ts>`, then `secrets.key.next`
   to `secrets.key`. A crash between the two renames still reads, because the
   ring includes `.next`.
4. **Keep K₁ (retired) until the off-host copies have caught up:**
   - the secrets repository has pushed a commit with K₂ ciphertext
     (`secretsrepo.Repo.Sync`, `pkg/secretsrepo/secretsrepo.go:290`);
   - the GCE `secrets-key` escrow has been updated. Rotate pushes it when the
     host SA may write that secret, and otherwise tells the operator to run
     push-secrets.sh;
   - the operator has confirmed any off-host copy they keep.

   Until then a restore could fetch K₁ ciphertext. Only then does the
   Settings action offer "Delete retired key". The retired key is never
   deleted automatically.

Other consumers are unaffected:
- `sandbox-tokens.json` already has its own key, for exactly this reason
  (`pkg/gitproxy/tokens.go:94-105`);
- `openKeys`' legacy fallback to `secrets.key` only matters for a token file
  from before that split.

**The ring also gives `ImportKey` a non-destructive mode.** Today it archives
the current key whenever the new one differs (`pkg/secrets/secrets.go:391-396`).
With a ring, "import" can mean "add to the ring", and the `.replaced-*` files
become retired keys with a lifecycle.

### 4.2 What rotating the key does *not* do {#SKP-4-2}

Old ciphertext stays in git history under the old key: in every commit of the
secrets repository, and in any legacy state repo
(`pkg/staterepo/staterepo.go:918-947`). Rotation therefore protects against a
**future** leak of the old key's copies (archives, escrow, export bundles,
backups) **only once those copies are destroyed**. It does nothing for a key
that has already leaked: whoever holds K₁ and a clone of the secrets repo can
read every value that was ever pushed.

So the doc and the UI must say plainly: **if the key leaked, rotate the
secrets, not just the key.** Two things soften this:
- with [3a](#SKP-3a) on, a leaked *wrapped* key is harmless once IAM is
  revoked, which is the strongest argument for doing 3a before relying on
  rotation;
- an operator may, after a leak, rewrite the secrets repository's history.
  That is an operator action grain should document, not perform.

### 4.3 Rotating individual secrets {#SKP-4-3}

- **Values grain generates:** capability data-plane tokens (`Store.Rotate`,
  `pkg/secrets/secrets.go:871`), the access code (`grain access-code rotate`),
  host enrollment codes (`grain host rotate-code`), the package-cache CA
  (renewed near expiry). Add the missing ones:
  - the **Postgres password** ([Phase 4](#SKP-P4));
  - the **release-approval key**: generate a new key, show the new public key
    for `GRAIN_RELEASE_APPROVAL_KEY`, and keep signing with the old one until
    the operator confirms the variable is updated;
  - the **sandbox-access and package-cache CAs** on demand (`grain ca rotate`),
    since running sandboxes trust the old one until their next attempt.
- **Values an outside party issues** (GitHub PAT / App key, Gemini / Claude /
  OpenAI keys, hub registration token): grain cannot rotate these itself.
  What it can do:
  1. record **when each key was last set**. File-store `contents` has no
     timestamps (`pkg/secrets/secrets.go:467-470`), so add `updatedAt` per key
     in a `version: 2`; the SM catalogue already has `UpdatedAt`
     (`pkg/secrets/secretmanager.go:63-71`);
  2. show **age, and what uses it,** in Settings → Secrets;
  3. offer **"Replace…"** inline;
  4. provide a **compromise runbook** listing every item in [§1](#SKP-1) with
     its upstream revocation URL or command.
- **Redaction after rotation:** `KnownValues` returns current values only
  (`pkg/secrets/secrets.go:650-683`). A just-replaced value can still appear
  in a running sandbox's output. Keep the previous value in the redaction set
  until every run that started before the rotation has finished, the way the
  SM backend already keeps a value that is gone upstream
  (`pkg/secrets/secrets.go:641-649`).

### 4.4 Surface {#SKP-4-4}

| Command / UI | What it does |
|---|---|
| `grain state key rotate [-yes]` | [§4.1](#SKP-4-1) steps 1–3 through the daemon. It prints the new public key, the retired key's path and what must catch up before deletion |
| `grain state key retired [list\|delete <ts>]` | Lists retired and `.replaced-*` keys, and deletes one after a confirmation that names the secrets-repo push state |
| `grain state key wrap -kms <key>` / `unwrap` | [Phase 2](#SKP-P2) migration and its reversal |
| `grain secrets list` (extended) | Adds the age of each key, never its value |
| Settings → State | "Rotate secrets key", "Retired keys", "Key protection: file / Cloud KMS (key, last unwrap, `usedPrimary`)" |
| Settings → Secrets | Age column, "Replace…", and a link to the compromise runbook |

---

## 5. Loose files: move into the store, or stay {#SKP-5}

The rule: **a credential moves into the store if the daemon is its only
reader and nothing needs it before the store can be opened.** Under 3a,
"opened" means after one KMS unwrap. The store does not need the DB on the
file backend, so a DB password *can* live in it, and already does
(`pkg/model/postgres/upgrade.go:34-42`).

| Item | Decision | Why |
|---|---|---|
| `secrets/github/*.token`, `*.app.json`, `*.ssh.json` | **Move** (Phase 3) | The highest-value loose item; the App key is a long-lived signing key. The reason recorded for keeping them as files is that the git proxy "reads one thing", whose availability is separate from the store's (`pkg/ui/github_tokens.go:39-45`, `pkg/gitproxy/credentials.go:31-37`). That stops holding once the store is a once-per-start unlock and the proxy runs in the same process. Keep `*.host.json` (not secret) as files or rows. Migrate on start by reading each file, `Set`, verifying, and then removing it, logged once like `moveSecretsOutOfStateRepo` |
| Seeded `gemini-api-key`, `claude-oauth-token`, `openai-api-key` | **Remove the loose copy** after `grain connections import` stores it (`pkg/cli/connections.go:292-305`) | The store copy is what dispatch reads first (`pkg/daemon/agents.go:2011-2036`). setup.sh should write these through `grain secrets set -value-file` from a tmpfs path, not into `secrets/`. Keep `-*-key-file` flags for operators who point them at their own tmpfs |
| `secrets/postgres-password` | **Stay a file, but stop needing the data-dir copy** (Phase 4) | Two non-Go readers, setup.sh (bash) and the Postgres container's `POSTGRES_PASSWORD_FILE`, cannot open the store, and the daemon needs the DSN before anything else. The server reads the file only at initdb (`scripts/setup.sh:2834-2836`), so after initdb the data-dir copy exists only for setup.sh re-runs. Those can read the root-owned `/etc/grain/postgres.env`. Move it to `/etc/grain/postgres-password` (root 0600), mount it only into the Postgres container, and it leaves `grain export` and data-disk snapshots |
| `/etc/grain/postgres.env` | **Stay** | systemd must read it before the daemon exists. Root-owned and outside the data dir on purpose (`scripts/setup.sh:2848-2850`). Optional: `LoadCredentialEncrypted=` under [3f](#SKP-3f) |
| `local-api-token` (access code) | **Stay** | It is the credential *for reaching* the daemon. The host CLI, per-run `grain mcpserver` and setup.sh's group-read grant read it (`pkg/ui/localtoken.go:61-93`, `pkg/daemon/ui_server.go:250-264`). Putting it in the store would make every CLI caller need the store key, which is strictly more privilege. Rotation exists |
| Session-cookie key | **Nothing to move** | It is derived from the access code (`pkg/ui/accesscode.go:244-247`) |
| `registry-registration-token` | **Already in the store.** The `HubTokenFile` fallback should import on first start and warn (`pkg/daemon/config.go:896-903`) | Part of [§0](#SKP-0) |
| `release-approval-key.pem` | **Move** (Phase 3). Cloud KMS asymmetric signing as an option on GCP | Only the daemon signs (`pkg/ui/release_approval_github.go:156`). KMS signing means the key never leaves KMS, as the hub's JWT key already does (`design/design/hub.design.md`) |
| `package-cache-ca.pem`, `sandbox-access-ca.pem` | **Move** (Phase 3) | Daemon-only readers. The sandbox-access CA already has a pluggable store (`pkg/daemon/sandbox_access.go:96-99`), so add a `secrets.Store` CAStore. Either CA key lets someone on-path impersonate TLS to sandboxes |
| `state-repo-token`, `secrets-repo-token` | **Move** (Phase 3), served to git by a credential helper that reads the store | Written as files only so git can read them (`pkg/daemon/statemanager.go:1195-1212`) |
| `relay/token` file | **Deprecate the file fallback** (`pkg/daemon/run.go:86`); import on start | The store already holds `relay/token` |
| `codex-auth.json` | **Stay** | The Codex CLI rewrites it in place with rotating refresh tokens, and every process must see the same file (`pkg/agent/codex/codex.go:294-305`). Encrypting it would need grain to front the CLI's own auth |
| `sandbox-tokens.json(.key)`, `modelproxy-*-tokens.json` | **Stay** | Short-lived per-run tokens (24 h TTL), re-minted, and already isolated from the operator key |
| Host Ed25519 key / token on remote hosts, `grit` `attest.key` | **Stay** | Each is a separate machine's or component's own identity, with its own rekey path |
| `/run/grain-secrets` (GCE) | **Stay** | tmpfs, sourced from Secret Manager |
| `/etc/grain/daemon.env` | **Stay, but document** that secrets belong in the store, not here | Operator-owned |

---

## 6. Recommendation {#SKP-6}

### 6.1 Default per install type {#SKP-6-1}

| Install | Default | Opt-in |
|---|---|---|
| **GCE via `deploy/terraform/gcp`** | **KMS-wrapped `secrets.key`** ([3a](#SKP-3a)). Terraform creates a key ring and key and grants the host SA encrypt/decrypt on that key. The `secrets-key` escrow holds the *wrapped* key | SM backend ([3d](#SKP-3d)) for per-secret IAM |
| **Hub-managed GCE instances** | SM backend, as `hub.design.md` already specifies | — |
| **GKE (`deploy/kubernetes`)** | **KMS-wrapped key** via Workload Identity on the `grain` KSA | SM backend |
| **Cloudtop** | File key plus Phase 0 hardening. The installer lives outside this repo and decides | KMS wrap when the VM's SA can be granted decrypt on a key the operator controls |
| **Workstation without GCP** | File key plus Phase 0 hardening | OS keychain ([3c](#SKP-3c)) for a session-run daemon |
| **OSS, no cloud** | File key plus Phase 0 hardening | `systemd-creds` host / TPM2 ([3f](#SKP-3f)) |

Why KMS wrap rather than the SM backend as the GCE/GKE default:
- it touches the network **once per process start**, not every 30 s, so a
  KMS or SM outage cannot stop a running deployment;
- it keeps the file store's semantics and tests, and the secrets repo;
- it is reversible by rewriting one file;
- it needs no migration of the secrets themselves.

### 6.2 Migrating existing deployments: zero-downtime and reversible {#SKP-6-2}

**KMS wrap ([Phase 2](#SKP-P2)):**
1. **Upgrade to a build that reads both key formats.** This changes no
   behaviour. Key-format detection is by prefix (`grain-secret-key-v1:` versus
   `grain-secret-key-kms-v1:`), the same tagging `pkg/secrets/crypto.go:64-69`
   uses today.
2. **`grain state key wrap -kms <key>`** runs inside the daemon, which keeps
   its in-memory key throughout, so nothing restarts. It:
   1. wraps the key;
   2. unwraps the result and checks it is the same key **before** writing;
   3. atomically replaces `secrets.key` with the wrapped form;
   4. updates the escrow;
   5. reports the old plaintext copies it can see (`.replaced-*`,
      `/run/grain-secrets`, the SM `secrets-key` version history) for the
      operator to destroy.

   `secrets.enc` is untouched, so the secrets repo and its history do not
   change.
3. The **next restart unwraps through KMS.** A failure is loud and the
   daemon stays up, per [3a](#SKP-3a)'s failure modes.
4. **Rollback:** `grain state key unwrap` (needs KMS), or
   `grain state key import` from the operator's escrowed plaintext. **Unwrap
   before downgrading:** an older build cannot parse the wrapped form. It
   starts, but cannot read secrets (`pkg/daemon/staterepo.go:197-207`). The
   release notes for the phase must say so.

**Key rotation, loose-file moves and the Postgres password** are each
reversible on their own:
- rotation keeps the retired key ([§4.1](#SKP-4-1));
- loose-file moves rename the file to `<file>.migrated` and delete it only one
  release later, and `grain secrets get-file`-style export is not needed
  because the source is kept;
- the Postgres change keeps the old password valid until the new one is
  verified ([Phase 4](#SKP-P4)).

---

## 7. Phased plan and task briefs {#SKP-7}

Each brief below is ready to file against `ai-on-gke/grain`. They are listed in
dependency order; the operator chooses which to file. P1 and P2 both depend on
P0. P3 and P4 are independent of P1/P2 but read better after P0. P5 and P6 are
optional.

### Phase 0: a key-source seam, and hygiene {#SKP-P0}

> **Title:** pkg/secrets: one KeySource for the store key, daemon-served redaction for children, and key hygiene
>
> **Body:** Per `design/design/secrets-key-protection.design.md` (Phase 0).
> Today every reader of the secrets key calls `secrets.ReadKeyFile` on the
> path itself:
> - `Store.load` / `Store.save` re-read it on every operation
>   (`pkg/secrets/secrets.go:490`, `:513`);
> - per-run `grain mcpserver` (`pkg/daemon/mcpserver.go:1657-1671`);
> - transcript redaction (`pkg/daemon/transcripts.go:239`);
> - `pkg/cli/import.go:233`;
> - the gitproxy legacy fallback (`pkg/gitproxy/tokens.go:157-180`).
>
> 1. Add `secrets.KeySource` (`Load(ctx) (Key, error)`, `Describe() string`)
>    with a `FileKeySource` that matches current behaviour. Have
>    `secrets.Config` carry it, and have `Store` load once and cache the key
>    in memory, re-reading only after `ImportKey`.
> 2. Make per-run `grain mcpserver` and transcript redaction get known
>    values from the daemon's local API (they already hold the local API
>    token) instead of opening the key, with today's path kept as a fallback
>    when no daemon answers.
> 3. Add to `grain state status` and Settings → State a list of
>    `secrets.key.replaced-*` archives, and add `grain state key retired delete`.
> 4. Make `grain export` refuse to write an unencrypted bundle that contains
>    `secrets/secrets.key` unless `-plaintext` is passed.
> 5. Make setup.sh stop leaving `secrets/{gemini-api-key,claude-oauth-token,openai-api-key}`
>    behind once `grain connections import` has stored them.
>
> No format change. Tests: the existing `pkg/secrets` suite, plus a test that
> the store reads the key file once across many `Resolve` calls. `scripts/setup.sh`
> and `pkg/staterepo` are trust-boundary paths, so expect the security review.

### Phase 1: store-key rotation {#SKP-P1}

> **Title:** grain state key rotate: crash-safe store key rotation with retired keys
>
> **Body:** Per `design/design/secrets-key-protection.design.md` §4.1–4.4.
> Depends on Phase 0's KeySource.
> 1. Give the file store a key ring: current key, `secrets.key.next`, and
>    retained retired keys. Choose the decrypting key by the armour's recipient
>    (`pkg/secrets/crypto.go:205-233`) instead of failing with `ErrWrongKey`.
> 2. Implement `Store.RotateKey`, steps 1–3 of §4.1, so a crash at any point
>    still reads. Cover it with fault-injection tests that kill the process
>    between each rename.
> 3. Add `POST /api/state-repo/secrets-key/rotate` (scope `secrets:write`,
>    beside `pkg/ui/server.go:708`), `grain state key rotate` (through the
>    daemon when one is running), `grain state key retired list|delete`, and
>    the Settings → State actions. Deletion of a retired key is offered only
>    once `secretsrepo` has pushed K₂ ciphertext, with an operator override.
> 4. Bump `contents` to version 2 with a per-key `updatedAt`. Read v1, write v2.
>    Surface each key's age in `grain secrets list` and Settings → Secrets.
> 5. Keep each replaced value in the redaction set until every run started
>    before the change has ended.
> 6. Add `docs/secrets-compromise-runbook.md`: for each item in the design's
>    §1, how to revoke and replace it upstream. State plainly that rotating
>    the key does not protect ciphertext already pushed to the secrets
>    repository.
>
> Regenerate the CLI spec (`GRAIN_UPDATE_CLI_SPEC=1 go test ./pkg/cli ./pkg/sandboxcli -run CLISpec`).

### Phase 2: Cloud KMS-wrapped key {#SKP-P2}

> **Title:** Cloud KMS-wrapped secrets key (grain state key wrap/unwrap), default on terraform/gcp and GKE
>
> **Body:** Per `design/design/secrets-key-protection.design.md` §3a and §6.2.
> Depends on Phase 0. Phase 1 is recommended first, so a wrapped deployment
> can also rotate.
> 1. Add `KMSKeySource`. The key file format is
>    `grain-secret-key-kms-v1:<cryptoKey name>:<base64 ciphertext>`, with AAD
>    `"grain secrets key v1\0" ‖ public key`. Unwrap once through
>    `cloudkms.googleapis.com/v1/<name>:decrypt` with a metadata-server token,
>    reusing the pattern in `pkg/secrets/secretmanager.go:496-547`; this
>    covers GKE Workload Identity. Rewrap at start when the response's
>    `usedPrimary` is false. Selection is by file prefix, with no flag needed.
> 2. On failure the daemon stays up and the store reports "KMS unreachable /
>    permission denied for <key>", retrying with backoff
>    (`pkg/daemon/staterepo.go:189-210`).
> 3. Add `grain state key wrap -kms <name>` and `unwrap`, through the daemon.
>    Each verifies a round trip before writing atomically, and reports
>    remaining plaintext copies. Settings → State shows the protection mode,
>    the key, and the last unwrap.
> 4. In `deploy/terraform/gcp`: a key ring and key in the deployment region,
>    rotation period 90 days, `roles/cloudkms.cryptoKeyEncrypterDecrypter` for
>    the host SA on that key only; `deploy.sh` / setup.sh wrap on first
>    deploy; the `secrets-key` escrow holds the wrapped key; README updated.
>    `deploy/kubernetes/README.md` documents the Workload Identity binding.
> 5. Release notes: run `unwrap` before downgrading.
>
> Tests use a fake KMS endpoint (`GRAIN_KMS_ENDPOINT`, like
> `GRAIN_SECRET_MANAGER_ENDPOINT`). No Google-internal names in fixtures
> (`TestCoreSaysNoGoogleTokens`).

### Phase 3: loose credentials into the store {#SKP-P3}

> **Title:** Move GitHub credentials, CA keys, the release-approval key and repo tokens into the secrets store
>
> **Body:** Per `design/design/secrets-key-protection.design.md` §5. Depends
> on Phase 0.
> 1. **GitHub credentials:** store `<name>.token` / `.app.json` / `.ssh.json`
>    under `github-credential-<name>` in the store, and have the git proxy
>    credential set (`pkg/gitproxy/credentials.go`) and Settings → GitHub
>    (`pkg/ui/github_tokens.go`) read and write there. Rewrite the
>    "files, not store rows" rationale in both doc comments. On start, import
>    each file, verify it, and rename it to `<file>.migrated`; delete one
>    release later. setup.sh seeds through `grain secrets set`.
> 2. **`release-approval-key.pem`** into the store, with optional Cloud KMS
>    asymmetric signing.
> 3. **`package-cache-ca.pem` and `sandbox-access-ca.pem`** through a
>    store-backed `CAStore`.
> 4. **`state-repo-token` / `secrets-repo-token`** into the store, served to git
>    by a credential helper.
> 5. **`HubTokenFile` and `secrets/relay/token`:** import into the store on
>    start, and warn.
>
> Each move is reversible within a release (the source is kept as
> `.migrated`). Touches `pkg/gitproxy/*` and `scripts/setup.sh`, which are
> trust-boundary paths.

### Phase 4: Postgres password, one fewer copy, and rotation {#SKP-P4}

> **Title:** Local Postgres: password out of the data dir, and grain db rotate-password
>
> **Body:** Per `design/design/secrets-key-protection.design.md` §5 and §4.3.
> Today the password is `<data-dir>/secrets/postgres-password`, never
> replaced, and copied into `/etc/grain/postgres.env`
> (`scripts/setup.sh:2834-2850`, `:3002-3048`).
> 1. Move the source of truth to `/etc/grain/postgres-password` (root 0600),
>    mounted read-only only into grain-postgres. Migrate the data-dir copy and
>    remove it, so it leaves `grain export` and data-disk snapshots.
> 2. Add `grain db rotate-password` (and a setup.sh path):
>    1. generate a new password;
>    2. `ALTER ROLE grain PASSWORD` over the existing connection;
>    3. write the new password file and `postgres.env` atomically;
>    4. restart only grain-daemon, whose `Requires=` keeps Postgres up;
>    5. on failure, restore the old env file. The old password stays valid
>       until the daemon reconnects with the new one.
> 3. Cover the `db-config.json` / `database-password` path
>    (`pkg/model/postgres/upgrade.go:34-56`) as well.

### Phase 5 (optional): local hardware and OS key sources {#SKP-P5}

> **Title:** Optional key sources: systemd-creds (host/TPM2) for setup.sh hosts, OS keychain for session daemons
>
> **Body:** Per `design/design/secrets-key-protection.design.md` §3b, §3c and
> §3f. Depends on Phase 0.
> 1. **`GRAIN_SECRETS_KEY_PROTECTION=systemd-creds`** in setup.sh: encrypt
>    the key with `systemd-creds encrypt --with-key=auto` (TPM2 when present,
>    otherwise host), install it as `LoadCredentialEncrypted=` on
>    grain-daemon.service, bind-mount `$CREDENTIALS_DIRECTORY` read-only into
>    the container, and add a `CredentialsDirKeySource`. Refuse on systemd
>    older than 250. Document that TPM2-bound keys need an escrow on VM
>    replacement.
> 2. **`KeychainKeySource`** for `grain daemon` run in a user session, reusing
>    `pkg/cli/credentials.go`'s keyring code. It is never the default.
>
> Both are reversible through `grain state key unwrap` to a plain file.

### Phase 6 (optional): file → Secret Manager backend migration {#SKP-P6}

> **Title:** grain secrets migrate: move a file-backed store to -secrets-backend=secretmanager and back
>
> **Body:** Per `design/design/secrets-key-protection.design.md` §3d. The
> Secret Manager backend exists (`pkg/secrets/secretmanager.go`), but a
> deployment on the file backend has no way onto it.
> 1. Add `grain secrets migrate -to secretmanager -sm-prefix ...`, which reads
>    every key with `KnownValues` and `Set`s it into an SM-backed store whose
>    catalogue is the deployment DB. It verifies each value by `Resolve`,
>    writes nothing to the file store, and prints the daemon flags to switch.
>    It does not switch them itself.
> 2. Add the reverse (`-to file`) for rollback.
> 3. Refuse to migrate `database-password` when the DSN it backs is the
>    database holding the catalogue.
> 4. After a successful migration, document that the operator should retire
>    the file store and key, and rotate the secrets if the old key's
>    ciphertext was pushed to a secrets repository.
