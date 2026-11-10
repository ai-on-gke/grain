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

| Mode | Set | The access code | A signed proxy assertion |
| --- | --- | --- | --- |
| **No IAP** | neither | accepted from anywhere, as below | not checked (logged and ignored) |
| **IAP, code still accepted** | `-ui-iap-audience` | accepted from anywhere | verified, and its person is signed in |
| **IAP required** | `-ui-iap-audience` and `-ui-require-iap` | only over loopback with no forwarding headers ([break-glass](#break-glass-over-loopback)) | verified, and its person is signed in |
| **Hub required** | `-ui-hub-{issuer,audience,keys-url}` and `-ui-require-verifier=registry` | only over loopback with no forwarding headers ([break-glass SSH](#break-glass-ssh-on-registry-hosted-instances)) | `X-Grain-Registry-Assertion` verified, and its person is signed in |

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
- **Another verifier required.** `-ui-require-verifier=<name>` (or
  `GRAIN_REQUIRE_VERIFIER=<name>`) is the general form of the flag above:
  `-ui-require-iap` is an alias for `-ui-require-verifier=iap`, and the
  two together with a name other than `iap` are refused. It closes the
  network listener to the access code the same way, and the daemon
  refuses to start unless an `IdentityVerifier` of that name is active
  (for `iap`, an IAP audience; for `registry`, all three of
  `-ui-hub-issuer`, `-ui-hub-audience` and
  `-ui-hub-keys-url`; otherwise one a wrapper build registered).
  Any active verifier's assertion still signs its caller in.
- **Behind the Grain Hub (`-ui-require-verifier=registry`).**
  Core ships a built-in `registry` verifier ([`docs/hub.md`](hub.md))
  alongside `iap`, active when `-ui-hub-issuer`
  (`GRAIN_HUB_ISSUER`), `-ui-hub-audience`
  (`GRAIN_HUB_AUDIENCE`) and `-ui-hub-keys-url`
  (`GRAIN_HUB_KEYS_URL`) are all set. It verifies the short-lived
  ES256 `X-Grain-Registry-Assertion` JWT that `grain-registry` signs on
  every proxied request to `https://<name>.<instance-domain>/`, checking
  the signature against the hub's JWKS
  (`https://<registry-host>/.well-known/jwks.json`), exact `iss`
  equality, single-audience `aud` equality
  (`https://<name>.<instance-domain>`), and `exp`/`iat`/`nbf` bounds.
  Its `AccessCodeWins()` is `false`: a verified hub assertion always
  takes precedence over the access code, and an invalid assertion fails
  closed with `401`. Hosted VM instances created by the hub run with
  `-ui-require-verifier=registry`, closing the network listener to the
  access code while keeping unforwarded loopback open for break-glass SSH
  ([Break-glass SSH on registry-hosted instances](#break-glass-ssh-on-registry-hosted-instances)).
  On external `connected` grains running `-hub-connect`, streams
  arriving over the outbound `/api/entries/<name>/connect` tunnel are
  served in-process over a `MuxListener` (`RemoteAddr` is
  `stream-<id>-remote`, never `127.0.0.1`) behind a mandatory
  tunnel-ingress gate that unconditionally requires a valid
  `X-Grain-Registry-Assertion` and never accepts `grain_access` cookies,
  `X-Grain-Access-Code` or bearer access codes over the tunnel -- even if
  a workstation daemon omits `-ui-require-verifier=registry` on its local
  `127.0.0.1` TCP listener so local CLI commands can still use the access
  code directly.

Neither flag stops a wrapper build's own `IdentityVerifier` that declares
the access code wins over it (`AccessCodeWins()`, as a verifier behind an
identity-aware proxy may): it keeps taking the code over its proxy, so
break-glass through that proxy still works with the flag set.

In every mode the executor routes stay open
([Remote executors](#remote-executors)), and calls from inside a
sandbox (the git and API proxy, `grain mcpserver`) work as before.

### Break-glass over loopback

With `-ui-require-iap` or `-ui-require-verifier=<name>` (including
`-ui-require-verifier=registry`) the code is still accepted from
`127.0.0.1` or `::1` when the request carries none of `X-Forwarded-For`,
`Forwarded`, `X-Real-Ip`, IAP's headers (`X-Goog-IAP-JWT-Assertion`),
`X-Grain-Registry-Assertion` or any registered verifier's headers. A
proxy on the same host therefore cannot relay somebody's code in. That
leaves a way in which does not depend on IAP or the hub proxy, from
the daemon's own host:

```bash
kubectl -n grain exec deploy/grain -- env GRAIN_SERVER=http://127.0.0.1:8080 GRAIN_DATA_DIR=/data grain list
```

### Break-glass SSH on registry-hosted instances

Hosted VM instances provisioned by `grain-registry` (`kind = instance`,
[`docs/hub.md`](hub.md)) run with
`-ui-require-verifier=registry` and have no external IP address, so the
access code is never accepted over `https://<name>.<instance-domain>/` or
over the VPC listener when proxy headers are present.

When break-glass SSH is enabled (`compute.ssh_break_glass: true` in the
hub configuration, the default, and `-allow-ssh` on the entry), the
hub grants the entry's accountable human `owner` (`user:<owner>`)
conditional OS Login administrator and IAP TCP-forwarding bindings
(`roles/compute.osAdminLogin` and `roles/iap.tunnelResourceAccessor`) on
that instance VM (`<vm-prefix><name>`). The owner can open a shell on the
VM through IAP TCP forwarding without exposing a public SSH port:

```bash
gcloud compute ssh <vm-name> \
  --project <instances-project> \
  --zone <zone> \
  --tunnel-through-iap
```

From inside that SSH session (or over an unforwarded local SSH port
tunnel `-L 8080:127.0.0.1:8080`), requests hit `http://127.0.0.1:8080`
directly over loopback with no proxy or verifier headers, where the
daemon accepts the access code:

```bash
# On the instance VM:
grain access-code
grain access list
grain access allow user:alice@example.com --reason "restore owner sign-in"
```

Because hosted VMs are disposable and replaced on upgrades, shape
resizes and VM recoveries, `grain-instance-init` persists the instance's
break-glass access code (`local-api-token`) in Google Cloud Secret
Manager under the instance's isolated secret prefix
(`grain-inst-<name>-local-api-token`) and materializes it into
`/run/grain-secrets` (`0700` `tmpfs`) before `grain-daemon` starts.
Replacing the VM therefore preserves the break-glass access code until
you rotate it.

Break-glass SSH stays bound to the entry's individual accountable
`owner` (not pooled across `owner_group` by default) so root shell access
remains individually attributable; when ownership is transferred
(`grain hub transfer` / `grain hub accept-transfer`), the
hub revokes the previous owner's SSH bindings and grants them to the
new owner. Deployments that forbid shell access to hosted VMs can disable
break-glass SSH across the hub (`compute.ssh_break_glass: false`) or
per entry (`grain hub update -allow-ssh=false <name>`).

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
- **Service accounts:** `serviceAccount:<email>` admits a non-human
  caller (a `*.gserviceaccount.com` address), at full operator access
  unless its entry is given a narrower role (see **Roles** below).
  Each one is added explicitly, with a reason: `grain access allow
  serviceAccount:<email> --reason "<why>"`, or **Settings → Access**
  with the Reason field filled in (`POST /api/access/principals` takes
  it as `reason`, or as `X-Grain-Acting-Reason`). The reason is kept
  with the entry and recorded in `config_audit`, and Settings → Access
  lists service accounts in their own group with who added each, when
  and why. A service account is admitted only by its own
  `serviceAccount:` entry, never through a `group:`, and what it does is
  attributed to it as kind `service`, not `operator`. Accounts whose
  keys grain hands to tasks -- the `gcp-key` pool and the gcp
  capability's permission-set accounts -- are always refused, both when
  added and at sign-in, even if an entry for one is already in the
  table. `domain:` entries are not supported.
- **Roles:** every entry has a role, which is an API scope set
  ([`docs/agent-api.md`, "Scope sets"](agent-api.md#scope-sets)). A
  verified session can call only the routes its role's scopes cover, on
  the repositories they reach. It is checked against the same route
  table as an agent ([`docs/design/access-rbac.md`](design/access-rbac.md)).
  grain ships three roles:
  - `grain/operator`: every scope. This is the default, and every entry
    that existed before roles has it.
  - `grain/contributor`: `grain/viewer` plus `tasks:write` and `ci:write`.
  - `grain/viewer`: every `*:read` scope.

  A deployment can also declare its own sets, for example one with
  `repos: [acme/widgets]` to limit a role to one repository. A person
  who matches several entries (a `user:` entry plus `group:` entries)
  holds the union of their roles. A scope that any of those roles'
  sets `deny` is withheld from that person, even when another role
  grants it. A refusal is a `403` that names the missing scope and the
  caller's role. `GET /api/auth/user` and `GET /api/config` report the
  caller's role and scopes as `access`, and Settings → Access shows
  your role and hides controls your role does not cover. Anyone not on
  the list gets a `403` naming **Settings → Access** and
  `grain access allow`.
- **Who assigns roles:** the access code (and loopback break-glass) can
  assign any role. A change involving a role that holds a dangerous
  scope, including `grain/operator` itself, or one that denies scopes
  needs a reason (`--reason`). The reason is recorded in `config_audit`
  along with who made the change and the old and new role, and Settings
  → Access shows it under **Access history**. A verified person whose
  role holds `access:write` can add, re-role and remove entries,
  dangerous and deny roles included, as long as their own role holds
  every scope of the entry's current and new role. They cannot widen,
  or add or lift a deny on, an entry that matches themselves (their
  `user:` entry or a group they are in); narrowing one asks them to
  confirm (`--confirm-self`). Nobody signed in can remove or narrow the
  last entry holding a full admin role: the access code stays the way
  to recover. Agents and sandboxes cannot change the list by any route,
  whatever they hold.
- **Routes outside the scope table:** the SPA's static pages are served
  to every admitted caller, and so are sign-in, `GET /api/config` and
  `whoami`. Event streams, console terminals and file downloads are
  ordinary scoped routes. `/api/mcp` and `/mcp` admit any signed-in role
  and check each tool call against the caller's role using the scope and
  repository rules of the route that tool maps to (filtering tool
  discovery and `grain_filing_context` to the role's scopes and naming
  the missing scope on a refused call). Persistent sandboxes' `/staging/`
  services and a run's own plumbing routes are refused to every role that
  does not hold every scope.
- **Live changes (no restart):** Adding or removing an entry takes
  effect immediately. Manage the list in **Settings → Access → Allowed
  users and groups** (`/settings/access#allowed-principals`), over the
  REST API (`GET /api/access/principals`, `POST /api/access/principals`,
  `PATCH /api/access/principals/{entry}` with `{"role": ...}`,
  `DELETE /api/access/principals/{entry}`), or from the CLI:

  ```bash
  grain access list
  grain access allow user:alice@example.com --reason "on-call lead"
  grain access allow group:team@example.com --role grain/viewer
  grain access role group:team@example.com grain/contributor
  grain access allow serviceAccount:ci@my-project.iam.gserviceaccount.com --role acme/ci-bot --reason "release pipeline"
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
  "access"`), and so is every role change.
- **No implicit owner entry:** the daemon's own `-as` actor is no
  longer admitted by default. A deployment that relied on the old
  default allowlist (no `-ui-allowed-principals`) admits no verified
  person after upgrading until someone runs `grain access allow
  user:<owner-email>` -- with the access code, or from the daemon's own
  host when `-ui-require-iap` is set.
- **Agent API scopes:** `api:access:read` allows listing entries. Agents
  and sandboxes cannot add, re-role or remove entries by any route, even
  with a person-granted `api:access:write`. Only people decide who signs
  in. In a person's role, `access:write` is what lets a verified session
  manage the list, as described above.
- **State repository:** `allowed_principal` lives in the database (and
  cold backups), and so do its roles. It is never exported to
  `grain/config` pull requests, so a config PR can neither lock
  operators out nor grant sign-in access. What a role grants is its
  scope set, which is exported in `tables/api_scope_set.json`. The state
  check's scope-set diff therefore also reports how each role built on
  a set widens or narrows.
- **One-time migration off `-ui-allowed-principals`:** On start, if
  `-ui-allowed-principals` or `$GRAIN_ALLOWED_PRINCIPALS` is set and the
  database list has never been populated or migrated, grain imports its
  `user:` and `group:` entries once and logs the import.
  `serviceAccount:` entries are not imported -- an operator adds each
  one back deliberately, with a reason -- and neither are `domain:`
  entries; each dropped entry is logged and shown as a warning in
  **Settings → Access**. On subsequent starts, `-ui-allowed-principals` and
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
`-ui-require-iap` there is no code page at all. A browser whose request
carries a verified assertion that posts the code anyway gets a `403`
saying that its IAP sign-in, not the code, decides its role, and no
cookie. The code is not checked, so the refusal does not say whether it
was right. To use the code, run it over loopback ([Break-glass over
loopback](#break-glass-over-loopback)). The rest of this section is how
the code works where it is accepted.

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

### A host dialing in

The daemon serves `GET /api/hostd/connect` without the access code. A
`grain host -server` or `grain-agentd -server` on another machine upgrades
it and presents its host credential (`ghd_...`), which says which host it is
(the host registry in **Settings → Hosts** / `/api/hostds`, or the deprecated
shared `-hostd-token-file`). Hosts enrolled via `--join` or migrated with
`rekey` also sign every `/api/hostd/connect` request with their Ed25519
private key (`X-Grain-Hostd-Timestamp`, `X-Grain-Hostd-Nonce`,
`X-Grain-Hostd-Signature`, bounded to a 5-minute replay cache), so a copied
credential without the host's private key is refused; their 30-day credentials
renew silently while connected (`OpRenew` and `POST /api/hostd/renew`). A
bearer on that route (the host credential, or an IAP token) is never counted
as an access-code guess.

What else the host needs depends on how it reaches the daemon:

- **Remote**: it dials in through the IAP Gateway or another registered
  identity verifier's proxy, so the request carries a verified identity.
  The daemon records that identity as the connection's principal and
  checks that it is admitted (either on the database allowlist in
  **Settings → Access**, a daemon-provisioned non-pool service account,
  or the non-pool service account registered as an active host's owner
  in **Settings → Hosts**). The
  identity must also equal the host row's owner, compared as full,
  lowercased emails. A stolen host token is no use without its
  owner's identity. Service accounts may be owners, so a host running
  under Workload Identity or a key needs its service account as the
  owner. The daemon refuses, and logs, a remote host that connects as
  somebody else, a remote host whose row has no owner, and a request
  that came through a proxy, or from another host to a daemon with a
  verifier configured, with no verified identity. It never falls back to
  the token alone.
- **Local**: over loopback with no forwarding headers and no assertion,
  such as through `kubectl port-forward`. The token alone names it, as
  it always has, and it needs no owner. The same goes for every host
  of a daemon that has no identity verifier configured. The co-located
  host at `/run/grain-hostd/hostd.sock` does not dial in at all.

Set the owner automatically by enrolling with `--join <enrollment-code>` from
**Settings → Hosts → Invite hosts** (the verified enrolling identity becomes
the host's owner once the deployment owner accepts its join request), or set it on a legacy per-host token (**Settings → Hosts**,
`grain host register -owner <email> <name>`, `POST /api/hostds`,
`grain host set-owner <name> <email>`, or `PATCH /api/hostds/{name}`). For
the shared token, use `-hostd-token-owner`. Start the host with
`-iap-audience <IAP client ID>` so that it sends an ID token from its service
account or its user's login. At startup it logs the email that token names,
which is the owner the daemon expects.

`GET /hostd/install` (and `GET /api/hostd/install.sh`), `POST /api/hostd/join`,
`POST /api/hostd/join/{id}/poll`, and `POST /api/hostd/renew` are served
without the access code too: an enrolling host signs its join and poll
requests with its generated Ed25519 keypair, presents a shared `ghe_...`
enrollment code (which grants nothing except filing a pending join request)
plus a verified enrolling identity (from the proxy `IdentityVerifier`,
`ui.RegisterHostJoinVerifier`, or a GitHub device-flow token verified
server-side), and waits for the deployment owner to accept it. For
legacy per-host token installs, the script reads the token on the machine
itself from stdin or `$GRAIN_HOSTD_TOKEN`; `--release` and `--image` are
filled in only for an admitted caller or passed on the command line.

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

## Who the code is recorded as

The code carries the owner's powers, but every request it authenticates
also records who presented it, as the principal `access-code:<name>`
(a human, source `access-code`, shown as "`<name>` (access code)"):

- **The browser sign-in** asks for your email beside the code. When the
  allowlist (Settings -> Access) names anybody, the email must be one it
  admits; a verified identity that arrives with the code (a verifier
  declaring `AccessCodeWins`) is used instead of what was typed. The
  session cookie carries the name under its MAC
  (`v2.<issued>.<name>.<mac>`), so it cannot be edited. A session signed
  in before names existed (`v1`) keeps working until it expires, as
  "operator (access code)".
- **The CLI** sends `X-Grain-Access-Name` beside a bearer code:
  `$GRAIN_OWNER` when set, else the local account as `<user>@<host>`.
  The header counts only beside the code. The name is what the CLI
  says, not something grain verified: anybody who holds the code is the
  owner already, so the name attributes the request, it does not
  authorize it. For a verified identity, use `grain login` through IAP
  or an identity proxy.
- **The access history** (Settings -> Access, the config audit) records
  every mutating request the code authenticates under the surface
  `access-code`: who, the source address, the route and the time,
  beside role changes.

## Bootstrap and recovery only: `grain access-code mode`

```bash
grain access-code mode            # prints on, recovery or off
grain access-code mode recovery   # recommended once a verified sign-in works
grain access-code mode off
grain access-code mode on         # the default
```

The mode is kept in `<data-dir>/access-code-mode`, beside the code, and
the daemon re-reads it within a second. Only this command, on the host,
changes it, never Settings. That way someone holding the code on the
network cannot switch it back on, and an owner who has lost every
verified sign-in still has the host.

- **on** (the default, and how every existing install behaves): the code
  works everywhere, as described above.
- **recovery**: once a person on the allowlist with a full role (a
  `user:` entry, `grain/operator` or wider) can sign in through a
  verifier (an IAP audience, `-ui-require-verifier`, or a registered
  identity proxy), the code only manages access and settings:
  `/api/access/*`, `/api/api-scope-sets*`, `/api/settings*`,
  `/api/config`, `/api/deployment` and its config audit,
  `/api/whoami`, signing in and out, and the frontend. Every other
  route answers `403` naming recovery mode. Each use raises an Inbox
  notification (`access-code:recovery:<source>`, at most one a minute
  per name and source, with every raise counted) that names who used it
  and from where. Until such a person exists, recovery works like
  **on**, so it cannot lock out the bootstrap it is meant to protect.
- **off**: the code is refused as a bearer token, in
  `X-Grain-Access-Code`, as a session, and at sign-in. One exception
  remains: a run's own plumbing (`grain mcpserver` requests carrying
  `X-Grain-Acting-Task`, held to that task's grants and recorded as the
  task) still reaches the daemon with the code, until runs carry a
  credential of their own. Undo it with `grain access-code mode on` on
  the host.

A mode file holding anything else is read as **off**, so a typo never
opens the code wider than intended.

Settings → Access shows the mode read-only in its **Access code** card
(`GET /api/access/access-code`, `access:read`): the mode, whether
recovery is in force, and the command that changes it, run as the
account that owns the code (`sudo -u <account> grain access-code
-data-dir <data-dir> mode recovery`). Once you are signed in with a
verified identity and the mode is still **on**, the card recommends
recovery. The **Access history** card lists every request the code
changed state with (`config_audit` surface `access-code`) beside role
changes.

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
  credential directories, never the data directory. They are never
  given a host sandbox either, so the daemon refuses `-run-host-image`
  with `-host-sandboxes`: a host sandbox's commands would run in the run
  host, beside the credential directory and every other run's spool and
  staging, with no bubblewrap there to hide them.
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
