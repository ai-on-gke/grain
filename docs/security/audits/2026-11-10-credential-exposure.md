# Security audit: credential exposure to non-admin users

- **Date:** 2026-11-10 (UTC)
- **Auditor:** grain task 3983 (agent), filed by the director (task 2923)
  at the operator's request: "make sure non-admin users cannot … get
  access to credentials".
- **Base audited:** `origin/main` at `563ec374f` (merge of #1084).
- **Checklist:** the credential inventory of
  `design/design/secrets-key-protection.design.md` §1 (SKP-1).
- **Method:** each credential was traced from where grain keeps it to
  every surface a non-admin can read. The code was read, and every
  finding marked *reproduced* was confirmed by a test now committed
  (see [Regression tests](#regression-tests)), first against the
  unfixed code and then against the fix. Fixes small enough to land
  here did. Each remaining finding is one follow-up task, filed with
  `depends_on: [3983]`.

## Who is a "non-admin"

grain is single-tenant (`docs/single-tenant-reset.md`). Whoever holds the
access code is the operator. Everyone else reaches the API through a
**role** on the allowlist (`pkg/ui/access_roles.go`) or as an **agent**
holding `api:<scope>` grants (`pkg/ui/agent_api_gate.go`). The widest
non-admin role grain ships is `grain/viewer`
(`pkg/model/api_scope_set.go`, `builtinAccessRoles`): **every**
non-dangerous `:read` scope. That includes `tasks:read`,
`sessions:read`, `settings:read`, `connections:read`,
`capabilities:read`, `secrets:read` and `deployment:read`.
`grain/contributor` and `grain/delegate` add writes on top of it, and
`grain/delegate` holds `sessions:write`. Only the dangerous scopes
(`secrets:write`, `capabilities:admin`, `settings:write`,
`connections:write`, `access:write`) are admin.

So every surface below was judged against `grain/viewer`, and against an
agent granted the same `:read` scopes.

## Findings

| ID | Finding | Severity | Status |
| --- | --- | --- | --- |
| CE-1 | Console output (`GET /api/console/{id}/output`, `sessions:read`) served a run's raw terminal, unredacted | High | **Fixed here** |
| CE-2 | Capability server `env` / `headers` / `args` / `serverUrl` accepted typed-in credentials, and `GET /api/capability-servers` (`capabilities:read`) returned them verbatim | High | **Fixed here** for the API and new saves; state repo, grain/config pull requests and existing rows → follow-up task |
| CE-3 | Attempt transcripts (`tasks:read`): with no object store, the DB copy is redacted only of the sandbox env and credential shapes, and a live tail not at all | High | **Fixed here** (redacted on read) |
| CE-4 | Credentials kept in files beside the store (Codex logins, GitHub App webhook secrets, setup.sh agent key files, state/secrets repo tokens, relay token file, Postgres password) were in no redaction list | Medium | **Fixed here** |
| CE-5 | `sessions:write` (e.g. `grain/delegate`) can type into any task's console, whatever grants that run holds | High | Follow-up task |
| CE-6 | A bare access code / `LocalAPIToken`, the legacy `grain-local-<hex>` token and the secrets private key (`grain-secret-key-v1:`) had no redaction shape (only the `X-Grain-Access-Code:` header did) | Low | **Fixed here** |
| CE-7 | State browser (`GET /api/state-repo/file`, `settings:read`) served exported rows with no redaction | Low (CE-2's carrier) | **Fixed here** |
| CE-8 | Capability server errors (`Status.Error`) are scrubbed of the server's own resolved secrets, not of shapes or other secrets | Low | Noted; no confirmed leak |

### CE-1: console output (reproduced)

`handleConsoleOutput` (`pkg/ui/console.go`) returned `session.Output`
base64-encoded as-is. Any role holding `sessions:read` (`grain/viewer`)
could read the terminal of any task's interactive session, including a
run that `cat`s `~/.gcp-token`, its `.git-credentials` or a config-map
secret file. **Fix:** output now goes through `Config.LogRedactor`, the
list the Logs pane uses, before it is encoded. Offsets stay the session's
own. If that list cannot be built, the output is withheld rather than
served. **Residual:** redaction runs per polled chunk, so a value split
across two reads can survive. That is covered with CE-5's follow-up.

### CE-2: typed-in capability server credentials (reproduced)

`protocol.ServerConfig.Validate` only checks that `$secret:` references
are well-formed. A literal `X-Api-Key: abc123` or `API_TOKEN=abc123` was
saved into `grain_config.mcp_servers` and returned verbatim by
`GET /api/capability-servers`, a `capabilities:read` route. **Fix:**

- `ServerConfig.LiteralCredentials` (`pkg/capability/protocol/literals.go`)
  names any env var or header under a credential's name (`*TOKEN*`,
  `*SECRET*`, `*PASSWORD*`, `Authorization`, `*_KEY`, …) that carries
  literal text, and any value, arg or URL with a credential's shape.
  `Client.SetCapabilityServer` refuses such a save and points to
  `$secret:<name>` and *Bind a secret*. The refusal never echoes the
  value.
- `ServerConfig.WithheldLiterals` is what a caller without
  `capabilities:admin` is shown. Literal env and header text becomes
  `[withheld]`. References and auth scheme words (`Bearer
  $secret:cap-token`) are kept. Shapes are cut from args, URL and config
  strings.

**Still open (follow-up task):** the same row is exported to the state
repository and readable with `settings:read` through the State browser
(CE-7 redacts shapes and known values there, but not a shapeless
literal), `config://` and the settings history. A grain/config pull
request can still add one. The follow-up adds the rule to `statecheck`
and migrates existing literals into the store.

### CE-3: attempt transcripts (reproduced)

`Client.AttemptTranscript` serves a live tail (`liveTranscripts.Tail`),
then the DB copy (`Store.RunTranscript`), then the archive. The live tail
is redacted only when a transcript object store is configured
(`l.remote != nil`), which is not the default. The DB copy is written at
finish (`pkg/orchestrator/run.go`, `SetRunTranscript`) redacted only by
`mcp.SandboxEnvRedactor`: the sandbox's operator env and shapes. It is
not redacted of the secrets store's values a run resolved, nor of
`request_secret` answers. **Fix:** `AttemptTranscript` passes whatever it
returns through `Config.LogRedactor`, which also covers transcripts
already stored. The object-store path was already redacted at flush
(`transcriptObjectRedactor`).

### CE-4: loose credential files (reproduced)

The daemon's four redactors are the transcript object store's, the
journal / `read_grain_log` (`grainLogRedactor[For]`), the Logs pane
(`logRedactor`) and the namespace (`buildNamespaceRedactorFor`). They
knew the store's values and GitHub PATs, nothing else under `secrets/`.
Every Codex run's `CODEX_HOME` links `codex-auth*.json`
(`pkg/agent/codex/codex.go`), so a Codex run that printed it kept its
refresh token. **Fix:** `knownLooseCredentials` (`pkg/daemon/redact_files.go`)
adds, by name:

- the tokens of each Codex login;
- each GitHub App's `webhook_secret`;
- `gemini-api-key`, `claude-oauth-token`, `openai-api-key`;
- `state-repo-token`, `secrets-repo-token`, `relay/token` and
  `postgres-password`.

It skips values under 8 characters. GitHub App *private keys* are cut
by the PEM block rule, and their installation tokens by the `ghs_` shape.

### CE-5: console input across grants (read, not reproduced)

`POST /api/console/{id}/input` needs only `sessions:write`.
`agentAPIGate` refuses agents, but a person whose role holds the scope
(`grain/delegate` does) can type into a console whose task holds grants
they don't. That reaches every credential the run holds. Follow-up task:
hold console input to the caller's own grants, the way
`roleApprovalWithin` holds approvals.

### CE-6: grain's own credentials had no shape (reproduced)

`pkg/redact` cut `X-Grain-Access-Code: <code>` but not the bare code, so
`cat <data-dir>/local-api-token` in a host-side run survived into its
transcript. **Fix:** three new shapes:

- the access code's own form, eight dash-separated groups of four
  base32 characters;
- `grain-local-<hex>`;
- `grain-secret-key-v1:<base64>`.

`TestTextRedactsBareGrainCredentials` checks all three, and checks that
a git SHA is left alone.

## Credential × surface

**Result key:**

| Mark | Meaning |
| --- | --- |
| ✔ | Not exposed: the route or surface never carries the value |
| R | Exposed only redacted (by name or by shape) |
| ✔\* | Fixed by this change, previously exposed |
| ✗ | Still exposed (follow-up filed) |
| — | Not applicable |

**Surface key:**

| Column | Surface |
| --- | --- |
| API | `GET` routes as `grain/viewer` |
| Cap-srv | `GET /api/capability-servers` |
| State | State browser, `config://`, state repo |
| Transcript | Attempt transcripts |
| Console | Console output |
| Logs | Logs pane / `read_grain_log` |
| Export | `grain export` |

| Credential | API | Cap-srv | State | Transcript | Console | Logs | Export |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Git PAT (`secrets/github/*.token`) | ✔ names only (`/api/github-tokens`) | — | ✔ | R | R | R | operator only |
| GitHub App key / webhook secret | ✔ | — | ✔ | R (PEM rule) / ✔\* | R / ✔\* | R / ✔\* | operator only |
| GitHub App installation token (`ghs_`) | ✔ | — | ✔ | R (shape) | R (shape) | R | — |
| gcp-key minter key (store) | ✔ (`/api/gcp-keys` names the secret, never its value) | — | ✔ (`gcp_key.minter_credential` is a name) | R | R | R | operator only |
| gcp access / ID token minted mid-run | ✔ | — | — | R (`ya29.`, JWT) | R | R | — |
| Agent API keys / OAuth tokens (store) | ✔ presence (`/api/agent-keys`) | — | ✔ | R | ✔\* | R | operator only |
| Agent key files (setup.sh) | ✔ | — | ✔ | R (shape) / ✔\* | ✔\* | ✔\* | operator only |
| `codex-auth.json` | ✔ presence (`/api/agent-backends/{id}/codex-auth`) | — | ✔ | ✔\* | ✔\* | ✔\* | operator only |
| Access code / `LocalAPIToken` | ✔ | — | ✔ (not in export) | ✔\* (shape) | ✔\* | ✔\* | not exported |
| Session signing key | not stored (derived from the code) | — | — | — | — | — | — |
| Release-approval key | ✔ (public half only) | — | ✔ | R (PEM rule) | R | R | not exported |
| Registry / hub token (store, `grc_`) | ✔ (`hub.go` redacts hub text) | — | ✔ | R | R | R | operator only |
| External agent token (`gma_`) | ✔ prefix only, shown once on create | — | ✔ (only its digest and display prefix are stored) | R (shape) | R | R | digests only |
| Host credential / enrollment code (`ghd_`, `ghe_`) | ✔ | — | ✔ (`LocalTables`) | R | R | R | not exported |
| Standing sandbox secrets | ✔ names (`standing_sandbox_secret` holds names) | — | ✔ names | R | ✔\* | R | operator only |
| Capability server `$secret:` values | ✔ names + presence | ✔ | ✔ references | R | ✔\* | R | operator only |
| Capability server **typed-in** values | — | ✔\* withheld / refused | ✗ CE-2 follow-up (R where shaped) | R | ✔\* | R | in `grain_config` |
| `request_secret` answers (store) | ✔ (`PUT /api/tasks/{id}/secret` is write-only) | — | ✔ (`pending_secret` is a name) | ✔\* | ✔\* | R | operator only |
| State / secrets repo tokens | ✔ write-only (`POST /api/state-repo/secrets-repo`) | — | ✔ | ✔\* | ✔\* | ✔\* | operator only |
| Secrets private key / `secrets.enc` | ✔ (no route returns it; `secrets-key` is write-only) | — | ✔ (`secrets.enc` is outside `browsableDirs`, and moved out of the repo) | ✔\* (shape) | ✔\* | ✔\* | operator only |

**Notes:**

- **Export** is `grain export`, a host CLI that only the operator (with
  the data dir) can run. No API route serves an export bundle or a data
  dir backup.
- **State repo history:** a legacy state repository's history can still
  hold `secrets.enc` (SKP-1.1, T5). It is reachable only through the
  repository's git remote, where GitHub's own permissions govern access,
  and through no grain route. The State browser reads the working tree
  through `staterepo.BrowseFile`'s allowlist with symlinks resolved.
- **Other people's runs:** in a single-tenant daemon `tasks:read` reads
  every task's transcript, by design. That is why CE-3's read-path
  redaction matters. Sandbox files have no API route. Placed credential
  files (`~/.gcp-token`, `.git-credentials`) live only in the sandbox,
  so they are reachable only through its console (CE-1, CE-5) or what
  the run prints (CE-3, CE-4).
- **Error messages:** capability server attach errors are scrubbed of
  that server's resolved values (`servers.Scrub`). The git proxy injects
  upstream credentials into outbound headers and never echoes them. No
  leak was found (CE-8).

## Regression tests

The nightly `go-long` job (`.github/workflows/nightly.yml`) runs all of
these, since it runs `./tests/security/...` and every package. They need
no live sandbox: the console test starts a real PTY with `/bin/sh`.

| Test | Package | Holds |
| --- | --- | --- |
| `TestNonAdminReadsNoPlantedCredential` | `tests/security` | Entry point for the nightly `security-attacks` job. It plants a fake credential of every kind above where grain keeps it, signs in a `grain/viewer` person and reads every `GET` route (112 answer 200). It also runs a console that prints every credential a run could hold, plus a minted gcp access and ID token, and stores a transcript of the same. No response may carry any planted value, plainly or base64'd. It redacts with `daemon.LogRedactor`, the daemon's real wiring |
| `TestLooseCredentialFilesAreRedactedByName` | `pkg/daemon` | The transcript, log and namespace redactors cut every loose credential file (CE-4) |
| `TestCapabilityServerSaveRefusesATypedInCredential` | `pkg/ui` | CE-2 save refusal |
| `TestStateRepoFileIsRedacted` | `pkg/ui` | CE-7 |
| `TestLiteralCredentialsNamesTypedInCredentialsOnly`, `TestWithheldLiteralsKeepsReferencesAndCutsValues` | `pkg/capability/protocol` | CE-2 rules |
| `TestTextRedactsBareGrainCredentials` | `pkg/redact` | CE-6 |

Each of the sweep's capability-server and transcript checks was seen to
fail with its fix disabled.

## Follow-up tasks

Both are filed with `depends_on: [3983]`:

1. **Refuse and migrate typed-in credentials in capability server
   config.** This covers grain/config pull requests and existing rows
   (CE-2's remainder).
2. **Hold console input to the caller's own grants** (CE-5, plus CE-1's
   chunk-boundary residual).
