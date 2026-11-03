# The access code: signing in to a single-tenant grain

A grain is single-tenant, and has no accounts. It has one
credential instead: an **access code**, generated when the daemon first
starts. You need the code to use the UI from anywhere -- a web proxy's URL
in front of the machine, an SSH tunnel, or
`http://localhost:4242/` on the machine itself. The grain CLI on that
machine sends the same code for you.

**The access code, together with a URL that reaches the daemon, is full
owner access.** Whoever has both can file and approve work, read every
task and change every setting. Treat it like a password. If it leaks,
rotate it.

## Three modes

Behind Google Cloud Identity-Aware Proxy, grain can sign a person in
from IAP's signed assertion instead
(`design/design/authentication.design.md`). Which credentials the
network listener takes depends on two settings:

| Mode | Set | The access code | An IAP assertion |
| --- | --- | --- | --- |
| **No IAP** | neither | accepted from anywhere, as below | not checked (logged and ignored) |
| **IAP, code still accepted** | `-ui-iap-audience` | accepted from anywhere | verified, and its person is signed in |
| **IAP required** | `-ui-iap-audience` and `-ui-require-iap` | only over loopback with no forwarding headers ([break-glass](#break-glass-over-loopback)) | verified, and its person is signed in |

- **No IAP.** Local and standalone deployments. Everything on this page
  applies as written.
- **IAP, code still accepted.** `-ui-iap-audience` (or
  `GRAIN_IAP_AUDIENCE`) names the IAP backend's audience, with
  `-ui-iap-issuer` and `-ui-iap-keys-url` for a signing proxy other than
  IAP. Grain checks `X-Goog-IAP-JWT-Assertion` against IAP's keys,
  issuer, audience and expiry. A caller whose assertion verifies is
  signed in as the email it names, if the deployment's allowed users and
  groups list ([Who is let in](#who-is-let-in)) admits
  it, with no access code. An assertion that is present but does not
  verify is refused with a `401` and never falls back to the code (a
  browser loading the UI is shown a "Sign-in could not be verified" page
  with the reason, an API caller the JSON error). The
  code keeps working beside it, so this is the state to migrate in.
- **IAP required.** `-ui-require-iap` (or `GRAIN_REQUIRE_IAP=true`) as
  well. The network listener no longer takes the code at all, neither
  the bearer token nor the `grain_access` cookie, and there is no
  "Enter access code" page: a browser without an assertion gets a page
  telling it to sign in through IAP, an API caller a `401`, and
  `POST /api/auth/access-code` a `403` with no cookie. The daemon
  refuses to start with `-ui-require-iap` and no IAP audience.
  `grain access-code` still prints the code, and says the server takes
  it over loopback only.

`-ui-require-iap` governs IAP only. A wrapper build's own
`IdentityVerifier` that declares the access code wins over it
(`AccessCodeWins()`, as a verifier behind an identity-aware proxy may) keeps
taking the code over its proxy, so break-glass through that proxy still
works with the flag set.

In every mode the executor routes stay open
([Remote executors](#remote-executors)), and calls from inside a
sandbox (the git and API proxy, `grain mcpserver`) work as before.

### Break-glass over loopback

With `-ui-require-iap` the code is still accepted from `127.0.0.1` or
`::1` when the request carries none of `X-Forwarded-For`, `Forwarded`,
`X-Real-Ip`, IAP's headers or any registered verifier's headers. A proxy
on the same host therefore cannot relay somebody's code in. That leaves
a way in which does not depend on IAP, from the daemon's own host:

```bash
kubectl -n grain exec deploy/grain -- env GRAIN_SERVER=http://127.0.0.1:8080 GRAIN_DATA_DIR=/data grain list
```

## Who is let in

When a request carries a verified identity (`X-Goog-IAP-JWT-Assertion`,
or a registered `IdentityVerifier`'s signed header), grain checks that
identity against the deployment's **Allowed users and groups** list in
the database (`allowed_principal`).

- **Entries:** `user:<email>` for an individual person and
  `group:<email>` for a group. Group membership is resolved transitively
  through the Cloud Identity groups API (cached briefly) only when at
  least one `group:` entry is present and no `user:` entry already
  matched.
- **Equal privileges:** A verified user admitted through the list has
  full operator access, the same as an access-code holder, including
  viewing, adding and removing entries on the list. Anyone not on the
  list gets a `403` naming **Settings → Access** and `grain access allow`.
- **Live changes (no restart):** Adding or removing an entry takes
  effect immediately. Manage the list in **Settings → Access → Allowed
  users and groups** (`/settings/access#allowed-principals`), over the
  REST API (`GET /api/access/principals`, `POST /api/access/principals`,
  `DELETE /api/access/principals/{entry}`), or from the CLI:

  ```bash
  grain access list
  grain access allow user:alice@example.com
  grain access allow group:team@example.com
  grain access deny user:alice@example.com
  ```

  `grain access allow` is a no-op when the entry is already present, so
  installers can re-run it safely. Removing yourself or the last entry
  is allowed (with a warning in the UI), because the access code still
  works on a connection that does not go through the identity-aware
  proxy -- though behind IAP a verified identity takes precedence over
  the code, and with `-ui-require-iap` the code is taken over loopback
  only ([Break-glass over loopback](#break-glass-over-loopback)). Every
  addition and removal is recorded in `config_audit` (`surface =
  "access"`).
- **No implicit owner entry:** the daemon's own `-as` actor is no
  longer admitted by default. A deployment that relied on the old
  default allowlist (no `-ui-allowed-principals`) admits no verified
  person after upgrading until someone runs `grain access allow
  user:<owner-email>` -- with the access code, or from the daemon's own
  host when `-ui-require-iap` is set.
- **Agent API scopes:** `api:access:read` allows listing entries.
  `api:access:write` is a **dangerous** scope: only a person may grant
  it, every call requires an acting reason (`X-Grain-Acting-Reason`)
  recorded in `config_audit`, and it is refused on the `sandbox-cli`
  channel. Without `api:access:write`, sandboxes and agents cannot
  modify the list.
- **State repository:** `allowed_principal` lives in the database (and
  cold backups) and is never exported to `grain/config` pull requests,
  so a config PR can neither lock operators out nor grant sign-in
  access.
- **One-time migration off `-ui-allowed-principals`:** On start, if
  `-ui-allowed-principals` or `$GRAIN_ALLOWED_PRINCIPALS` is set and the
  database list has never been populated or migrated, grain imports its
  `user:` and `group:` entries once and logs the import. Entries of
  other kinds (`serviceAccount:`, `domain:`) are not imported; each
  dropped entry is logged and shown as a warning in **Settings →
  Access**. On subsequent starts, `-ui-allowed-principals` and
  `$GRAIN_ALLOWED_PRINCIPALS` are deprecated: grain logs a warning when
  either is set and ignores its value.

## Where it lives and how to see it

The code is the daemon's local API token: `<data-dir>/local-api-token`
(`/var/lib/grain/data/local-api-token` on a default install), mode `0600`,
owned by the account grain runs as. Only that account and root can read
it. `scripts/setup.sh` can grant the account's own group read (`0640`)
so an operator it adds to that group can run the CLI without `sudo`.
Nothing ever widens it beyond that, and the daemon takes away any other
bit it finds at start-up.

```bash
grain access-code          # prints it
grain access-code rotate   # replaces it, prints the new one
```

Run these as the account grain runs as. On a workstation install
that's you. The
installer tells you how to get the code, but never prints the code
itself, so it doesn't end up in a terminal log or scrollback that others
can read.

A new code is 160 random bits in base32, in eight groups of four
(`ABCD-EFGH-…`). Case, dashes and spaces are ignored when it's typed in.
An installation from before access codes keeps the token it already had
(`grain-local-…`) as its code until you rotate it. Restarting the daemon
never changes the code.

## Signing in from a browser

Behind IAP, the browser signs in through IAP's Google sign-in, and
grain shows no code page to a caller whose assertion verifies. With
`-ui-require-iap` there is no code page at all. The rest of this
section is how the code works where it is accepted.

A request to a single-tenant daemon that carries no valid code gets a
minimal **Enter access code** page (`401`) and never the app. An API
request gets a JSON `401`. Every caller is treated this way: loopback and
a network address grant nothing (see [What the daemon does not trust](#what-the-daemon-does-not-trust)).

Entering the right code sets the `grain_access` cookie:

- **What it holds:** an HMAC-signed session token whose key is derived
  from the code. It is never the code itself.
- **Flags:** `HttpOnly`, `SameSite=Strict`, host-only (no `Domain`), and
  `Secure` when the request came over https. A web proxy's
  `X-Forwarded-Proto: https` counts as https.
- **Expiry:** it lasts `-ui-access-session-lifetime` (default 90 days) and
  is renewed as you use it. A session older than an hour is re-issued on
  your next request, so a browser you use doesn't get signed out.
- **Signing out:** `POST /api/auth/logout` clears the cookie.

`SameSite=Strict` means the cookie isn't sent on a navigation another
site started, such as a web proxy's sign-in redirect back to the UI.
When that happens, the login page asks `GET /api/auth/access-code`
(same-site, so the cookie is sent). If you're already signed in, the page
reloads itself, so you don't type the code again.

### Rate limits

Wrong codes are rate-limited, but nobody else's wrong codes can keep you
out:

- **Per source:** 10 wrong codes, then one more every 6 seconds. A
  source is one IPv4 address or one IPv6 `/64`.
- **Globally:** 100 wrong codes across all sources, then one more every
  600 ms. The global limit only comes into play once a code has failed.
  It never refuses a right code. A source that sends a wrong code while
  the global limit is spent is put over its own limit for a minute, so
  many sources guessing together get about one guess a minute each. A
  source that hasn't sent a wrong code is never affected.
- **Which attempts count:** codes typed into the login form, and bearer
  tokens that could be the code. A bearer of a different length, or
  containing characters no code has (a JWT, a session, a GitHub
  token), isn't a guess. It is refused with a `401` and not
  counted. The length and alphabet aren't secret, since every generated
  code shares them.

A remote source over its own limit gets the same `429` before its code is
looked at, whether the code is right or wrong, so the limit never
confirms a guess. A wrong code that puts a source over its limit is
answered `429` too.

**Loopback is the exception.** A request straight from `127.0.0.1` or
`::1` with the right code is always let in, whatever has been sent from
loopback before. Every process on the host shares that one source,
including the CLI and every run's `grain mcpserver`. A per-source limit
there would let any local account stall every run just by sending wrong
codes. Wrong codes from loopback are still counted and answered `429`
once over the limit. The trade-off is that a local process can guess as
fast as it can connect, which the code's 160 random bits make hopeless,
and which no limit could stop for an account able to open loopback
connections anyway. A request that a proxy on loopback forwarded (it
carries `X-Forwarded-For` or a proxy identity header) is limited like
any remote source.

Codes are compared in constant time (hashed first). A wrong code gets the
same answer as any other wrong code.

### A grain hostd dialing in

The daemon serves `GET /api/hostd/connect` without the access code. A
`grain hostd -server` on another machine upgrades it and presents its
hostd token, which says which hostd it is (the hostd registry,
`/api/hostds`, or the deprecated shared `-hostd-token-file`). A bearer
on that route (the hostd token, or an IAP token) is never counted as an
access-code guess.

What else the hostd needs depends on how it reaches the daemon:

- **Remote**: it dials in through the IAP Gateway or another registered
  identity verifier's proxy, so the request carries a verified identity.
  The daemon records that identity as the connection's principal and
  checks that it is admitted (either on the database allowlist in
  **Settings → Access**, a daemon-provisioned non-pool service account,
  or the non-pool service account registered as an active hostd's owner
  in **Settings → Hostds**). The
  identity must also equal the hostd row's owner, compared as full,
  lowercased emails. A stolen hostd token is no use without its
  owner's identity. Service accounts may be owners, so a hostd running
  under Workload Identity or a key needs its service account as the
  owner. The daemon refuses, and logs, a remote hostd that connects as
  somebody else, a remote hostd whose row has no owner, and a request
  that came through a proxy, or from another host to a daemon with a
  verifier configured, with no verified identity. It never falls back to
  the token alone.
- **Local**: over loopback with no forwarding headers and no assertion,
  such as through `kubectl port-forward`. The token alone names it, as
  it always has, and it needs no owner. The same goes for every hostd
  of a daemon that has no identity verifier configured. The co-located
  hostd at `/run/grain-hostd/hostd.sock` does not dial in at all.

Set the owner when you register the hostd (Settings → Hostds,
`grain hostds register -owner <email> <name>`, or `POST /api/hostds`),
or later (`grain hostds set-owner <name> <email>`, or `PATCH
/api/hostds/{name}`). For the shared token, use `-hostd-token-owner`.
Start the hostd with `-iap-audience <IAP client ID>` so that it sends an
ID token from its service account or its user's login. At startup it
logs the email that token names, which is the owner the daemon expects.

`GET /api/hostd/install.sh`, the script that sets such a hostd up
(Settings → Hostds shows the command when it issues a token), is served
without the access code too: the machine fetching it holds no credential
yet, and the script carries none. It holds the daemon's URL as the
request reached it, its release and its image, and reads the hostd token
on the machine itself, from stdin or `$GRAIN_HOSTD_TOKEN`
(`--iap-audience` for a hostd that dials in through IAP).

The remote executor routes that were open the same way, including the
executor bootstrap routes (`executorBootstrapRoute`), were removed with
remote executors ([executors-removed.md](executors-removed.md)).

## The CLI and the processes grain starts

The grain CLI reads the code from the file (`$GRAIN_LOCAL_API_TOKEN_FILE`,
else `$GRAIN_DATA_DIR/local-api-token`, else `/etc/grain/cli.env`'s data
directory) and sends it as `Authorization: Bearer <code>` to a daemon on
loopback, as it always has with the local API token. The daemon sets
`GRAIN_LOCAL_API_TOKEN_FILE` in its own environment, so processes it
starts on the host can find the file. That includes a run's
`grain mcpserver`, which re-reads the file on every request and so keeps
working across a rotation. A bearer code is accepted from any address,
because the code is the credential, unless the daemon requires IAP
(`-ui-require-iap`), which takes it over unforwarded loopback only.

## Rotating

`grain access-code rotate` writes a new code into the file and prints it.
The running daemon re-reads the file within a second (no restart). From
then on:

- the old code is refused as a bearer token;
- every UI session signed with the old code is refused, because its MAC
  no longer verifies, and the browser is shown the login page again;
- the CLI and `grain mcpserver` pick up the new code from the file.

Deleting the file leaves the daemon with no code, so it lets nobody in.
It writes a new code at its next start.

## What the daemon does not trust

Before access codes, a daemon ran an unauthenticated request from
loopback as its own operator, and a multi-tenant one also believed
trusted networks and (with a flag) identity headers. All of
that is gone: every caller, local or remote, shows the code or a
verified IAP assertion. A caller with the code is the deployment's own
actor, and a caller with an assertion is the person it names. In
particular:

- **Loopback:** an SSH port-forward to localhost is a caller like any
  other.
- **Network addresses:** a network address isn't a credential.
- **Plaintext identity headers:** `X-Goog-Authenticated-User-Email`
  and the like name nobody. A request that carries only such a header
  gets a `401` (task 4338). Only a signed assertion that verifies
  (`X-Goog-IAP-JWT-Assertion`, or a registered verifier's header) signs
  a person in. When a request carries both an IAP assertion and the
  code, the IAP person is the caller.

Still open without the code:

- the login page and its script (`/api/auth/access-code`,
  `/api/auth/access-code/login.js`). With `-ui-require-iap`,
  `GET /api/auth/access-code` only says the server requires IAP, and
  posting a code to it over the network is refused;
- `/api/auth/logout`;
- the account routes' `404` (there are no accounts to make or sign in
  to).

A request handed to the server in-process, with no network address, is
still the daemon's own.

## Keeping the code away from agents

Agents must not be able to read the code. What holds today:

- **Container sandboxes** (`-container-sandboxes`) get no environment
  from the daemon and no mounts: `docker run` is given neither `--env`
  nor `--volume`, and `docker exec` forwards no variable. Neither the
  file, the data directory nor `GRAIN_LOCAL_API_TOKEN_FILE` exists inside
  one. `TestContainerSandboxesGetNoAccessCode` and
  `TestSandboxCommandsDoNotInheritTheAccessCodeFile` hold this.
- **Granule sandboxes** are microVMs with their own filesystem. The
  daemon creates one from an image and a shape only: no host directory
  is shared into it, and nothing the daemon sets in its own environment
  reaches it.
- **Run hosts** (`-run-host-image`) are given their spool, staging and
  credential directories, never the data directory.
- **Transcripts and logs:** the code isn't in any of them.
  The daemon logs the file's path, never its contents. The code travels
  only as an `Authorization` header from the CLI or `grain mcpserver` to
  the daemon, and as the login form's body. Neither is logged or recorded
  in a transcript.
- **Host sandboxes** (`-host-sandboxes`) run a command as the same
  account grain runs as, and that account can read a `0600` file it
  owns. So on a single-tenant daemon every command a run's `run_command`
  starts runs under [bubblewrap](https://github.com/containers/bubblewrap)
  (`bwrap`) with the data directory hidden:
  - The data directory is an empty tmpfs inside the command's mount
    namespace. That hides the code and also the database, secrets and
    keys beside it. A run's own directory is bound back on top if it
    sits under the data directory. Everything else on the host is as
    before.
  - The command gets its own PID namespace and `/proc`, so it can't reach
    the file through `/proc/<pid>/root` or read the environment or memory
    of the daemon or of the `grain mcpserver` that started it.
  - bwrap drops all capabilities and the kernel locks inherited mounts,
    so neither `umount` nor a nested `unshare` uncovers the directory.
    bwrap also sets `no_new_privs`, so `sudo` doesn't work in a confined
    command (root could read the file anyway).

  This covers the daemon's own commands in the sandbox (checkout, the
  setup script) and the agent's `run_command`, which gets the list as
  `grain mcpserver -sandbox-hide`. `read_file`, `edit_file` and
  `write_file` already refuse any path outside the run's directory.
  `TestHostSandboxRunCommandCannotReadTheAccessCode` and
  `TestHostSandboxRunCommandCannotReadTheDataDirectory` hold this.

  A single-tenant daemon checks at start-up that bwrap can confine a
  command on this host, and **refuses to start host sandboxes if it
  can't**. That happens when bubblewrap isn't installed, or when the
  kernel or an LSM refuses unprivileged user namespaces (for example
  Ubuntu's `kernel.apparmor_restrict_unprivileged_userns`, or a daemon
  running in a Docker container under the default seccomp profile).
  `scripts/setup.sh` installs `bubblewrap` for `GRAIN_HOST_SANDBOXES=1`.
  A run's command is also refused, never run unconfined, if bwrap goes
  missing later.

  What this doesn't cover: an agent CLI's *own* native tools. `claude`
  runs with its native tool list empty, and `codex` with no execution
  environment, so their only shell is grain's `run_command`. `agy`
  (antigravity) has no switch to turn its native tools off, and those
  run on the host unconfined. Don't pair `agy` with host sandboxes on a
  deployment whose access code matters. If you have, rotate the code
  after any run you don't trust.

## Work from other people

The access code is how the deployment's owner reaches their own grain.
A task somebody else asked for is checked against the local policy
([local-policy.md](local-policy.md)), not against the code. The access
code and, behind IAP, a verified assertion from an allowed principal are
the only ways into a single-tenant daemon's UI.
