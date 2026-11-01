# Git hosts: per-credential hosts and per-team connections

Task 2365. Status: **phases 1 and 2 landed** (phase 2: task 2370).
Phases 3–5 are filed as follow-up tasks.

## Deployment git connections (single-tenant reset, stage 1)

Task 4680 ([single-tenant-reset.md](single-tenant-reset.md), stage 1)
changes whose connections are read. Where this document says "the team's
connection" below, read "the deployment's":

- **One set of connections.** Every git lookup resolves through the
  deployment's git connections (`Store.GitConnectionOn(host, owner,
  repo)`), with the same scope rules (§3): `owner/repo`, then `owner`,
  then the host default, on that host only. A task's team, a CI job's
  team and the team that owns a repository choose nothing.
  `TeamGitCredentialOn` and `TaskGitCredentialOn` still take a team, and
  ignore it.
- **Where they are stored.** Since schema 22 the deployment's
  connections are `grain_config.git_connections` and
  `grain_config.git_credential` (`Config.GitConnections`,
  `Config.GitCredential`); they were the default team's row before that
  ([single-tenant-reset.md](single-tenant-reset.md), 6-ii-b2). No team's
  row is read for them. `Store.TeamRowGitConnectionOn` consults one
  team's per-repository override before them, for the team editor and
  its tests only.
- **No ladder.** Nothing resolves a credential through
  `secrets/github/credentials.json`: the file is not read at all (stage
  2, task 4681). Its pattern editor in Settings → GitHub
  (`/api/github-credential-patterns`) and the start-time import of it
  are gone too. A deployment that still has one has to record its
  entries as connections, with `grain connections import` (below) or in
  the Settings pane.
- **The state and secrets repositories** use their own token file, or
  the deployment's connection for them once the daemon has opened its
  store (`daemon.SetRepoTokenStore`). The startup restore of the
  secrets repository runs before the store opens, so it needs a token
  file.
- **Refusals** name the deployment's connections ("this deployment has
  no git connection covering ..."), not the credentials.json ladder.

### Seeding connections: `grain connections import` (stage 2)

`secrets/github/` holds only credential *material* now: one
`<name>.token`, `<name>.app.json`, `<name>.ssh.json` or
`<name>.ambient.json` per credential. Which credential a repository
uses is always a deployment git connection. A headless or scripted
install records them with

    grain connections -data-dir DIR import seed.yaml

where `seed.yaml` is

```yaml
git:
  - credential: bot             # a credential name under secrets/github
    host: github.com            # optional; the deployment's default host
    scope: ""                   # "" (host default), "owner" or "owner/repo"
    token_file: /run/bot.token  # optional; stored as secrets/github/bot.token
```

(the same file can carry model connections, see
[connections-and-models.md](connections-and-models.md#seeding-connections-grain-connections-import)).
An entry replaces the connection already recorded for its host and
scope, so importing the same file twice changes nothing. The file is an
import format only: grain never reads it again, and it can be deleted
once the command has run. `scripts/setup.sh` (the `GRAIN_GITHUB_TOKEN`
credential becomes the host default, once), `grain init`
(`-github-token` narrowed to each `-github-repo`) and `grain init
workstation` (`-github-token-file` narrowed to each `-repo`) all seed
through it.

`CredentialSet.Select` is now a lookup through the deployment's git
connections (`CredentialSet.SetConnections`, wired to
`Store.GitConnectionOn` by the daemon). Its two callers name packages
rather than repositories: package publishing
(`CredentialSet.PackageCredential`) and the package cache's registry
pulls (`pkgcache`). `SelectForRepo` and `SelectForRepoOn` are gone.

Every stored credential is also a capability a task may name
(`ExtraNames`), the host default's included: with no ladder, there is no
"deployment default" that is served without being asked for.

## Problem

grain assumed one git forge per deployment and one credential per team:

- the git proxy forwarded to github.com, or to one deployment-wide
  `ForwardHost`;
- GitHub App installation tokens were minted only against github.com. A
  proxy forwarding anywhere else refused App credentials outright
  (`appsUnavailable`);
- `model.RepoRef` is `owner/name`, with no host;
- a team had one `Team.GitCredential`, plus per-repo overrides in
  `TeamRepoConfig.GitCredential`.

The operator runs GitHub Enterprise instances. **The host is a property
of the credential.** One team can hold a github.com App, a PAT on
`ghe-a.example.com` and a credential for `git.example.com` at once, and
team admins manage all of it without a deployment admin.

## Design

### 1. A host on every credential

Each credential in the deployment's credential store
(`secrets/github/`, `gitproxy.CredentialSet`) has a host record,
`<name>.host.json`, beside its `<name>.token` or `<name>.app.json`:

```json
{"host": "ghe.example.com", "kind": "github", "base_url": "https://ghe.example.com:8443"}
```

or, for a host reached only through a proxy (section 12):

```json
{"host": "depot.code.forge.test", "kind": "github", "proxy": "http://127.0.0.1:3128"}
```

- `host` is the git host, normalized (`model.NormalizeGitHost`): lowercase
  `host[:port]` with `:443` dropped.
- `kind` is one of:
  - `github`: github.com, or a GitHub Enterprise Server. The REST API is
    at `https://api.github.com` or `https://<host>/api/v3`, and App tokens
    are minted there.
  - `git`: a plain git server with no forge API.
- `base_url` is optional. It gives the scheme and port, for a server not
  reached on https:443.
- `proxy` is optional: an `http(s)://host:port` proxy that every connection
  the daemon makes to this host goes through, and that no other host's
  traffic uses (section 12). It never carries a login, because the record is
  not a secret.
- The **auth type** is the file beside the record: `.token` is a PAT,
  `.app.json` is a GitHub App, and `.ssh.json` is an SSH key (section 9).

A file rather than a database row, because the credential store is
already files, read by several processes. `credentials.go`'s header says
why.

**Migration in place.** A credential with no record is on the set's
default host: the proxy's `ForwardHost`, otherwise github.com. When
`BuildProxy` forwards anywhere other than github.com, it writes that
default down for every credential that has no record
(`MigrateCredentialHosts`). The credential then stays on its host even if
`ForwardHost` later changes. A github.com deployment needs no files
written.

**A credential never goes to another host.** A set that knows where its
tokens go (`ForwardHost`, set by `BuildProxy`) serves a credential
recorded for any other host with no token and a `WrongHost` sentence. The
proxy refuses the request with that sentence, whichever rung of the
ladder chose the credential (task grant, team override, team connection
or the deployment's patterns).

### 2. GitHub Apps on GHE

An App credential's installation token is minted against **its own host's
API**: `https://api.github.com/app/installations/<id>/access_tokens` for
github.com, and `https://<host>/api/v3/app/installations/<id>/access_tokens`
for GHE (`github.RealTransport.PathPrefix`). Per-repo narrowing is
unchanged.

The old refusal existed because the JWT and token could reach a host that
was not the App's. That cannot happen now:

- the JWT goes only to the host the credential is recorded on;
- the token goes only there too (the wrong-host guard above).

An App recorded on a `git`-kind host is refused, because there is no API
to mint against.

### 3. A team holds a set of credentials

(Since stage 1 of the single-tenant reset, only the default team's set is
read, as the deployment's: see
[Deployment git connections](#deployment-git-connections-single-tenant-reset-stage-1).)

`Config.GitConnections` replaces the single `Config.GitCredential`. It
is stored as JSON in `grain_config.git_connections` (on `team` before
schema 22), and the state repo exports it as one more column of
`grain_config`. Each connection has:

| field        | meaning                                                                  |
|--------------|--------------------------------------------------------------------------|
| `credential` | a credential name in the credential store                                |
| `host`       | the host it is used for; empty means the deployment's default host       |
| `scope`      | empty means the host default; `owner` (or `owner/*`); `owner/repo`       |

A credential for several specific repos is several connections.

**Resolution** (`Store.TeamGitCredentialOn(team, host, owner, repo)`):

1. the task's own `github-credential:` grant, unchanged;
2. the most specific connection **on that host**: `owner/repo`, then
   `owner`, then the host default. If two match equally, the earlier one
   in the list wins.

**Connections are the only place a team's git access is recorded**
(grain/task-3929). The single team token (`Team.GitCredential`) and the
per-repository tokens (`TeamRepoConfig.GitCredential`) are retired:

- On every start (`Client.MigrateTeamGitCredentials` →
  `Store.MigrateLegacyGitCredentials`) each per-repository credential
  becomes an `owner/repo`-scoped connection on that repository's host,
  placed first in the team's list so it still wins any tie, and a
  remaining team credential becomes the default host's default
  connection, placed last, where it was always read. The old fields are
  then cleared, so a second start finds nothing to do. Every repository
  resolves to the same credential before and after
  (`TestMigrateLegacyGitCredentialsResolvesTheSame`).
- Until a row is migrated -- a state repo export, or a repository
  declaration, still carrying `gitCredential` -- `TeamGitCredentialOn`
  reads it as before: the repository override first, then the team
  credential as the default host's default.
- `PUT`/`DELETE /api/teams/{id}/github-credential` and
  `/api/teams/{id}/repos/{owner}/{name}/github-credential` answer
  **410 Gone** pointing at the connections API (after refusing, with 403,
  anyone who could never have used them). Their `GET` and `/check`
  routes remain, for a credential not yet migrated.
- Which connection a repository uses is chosen in Team settings → Git →
  **Repositories**: `PUT /api/teams/{id}/repos/{owner}/{name}/git-connection`
  `{"credential": "<one of the team's connections>", "host": "..."}`
  records that credential as a connection scoped to the repository,
  replacing any earlier choice; `{"credential": ""}` ("Default") removes
  it, and the repository falls back to its owner's connection or the
  host default. Team admins (or system admins) only, like every other
  connection write.

If nothing matches, the answer is `NoTeamGitCredentialError` naming the
host. **A credential on another host is never used as a fallback.**

`Store.GitDefaultHost` is the daemon's `-github-host`. With it, a
connection written with an explicit `ghe.example.com` and one written
with no host both mean the same thing on a deployment that forwards to
`ghe.example.com`.

### 4. A host on the repo (phase 2)

- `RepoRef` gains an optional `Host`, where empty means the default host,
  so `owner/repo` keeps its meaning and every serialized form stays valid.
- `ParseRepo` accepts `host/owner/repo` and URLs. A first segment
  containing a dot or colon is a host, and GitHub owners can contain
  neither.
- `String()` writes the host only when it is not the default, so existing
  rows and state-repo files round-trip byte for byte.
- Team target repos, task targets, reads/writes, repo config and `.grain`
  declarations then accept any of those forms.

This is deliberately **not** in phase 1. A repo carrying a host that the
proxy cannot yet route would be cloned from the default host. The host
on the repo lands together with the routing.

### 5. The git proxy routes per host (phase 2)

- Sandbox URLs carry the host:
  `http://172.17.0.1:<port>/<host>/<owner>/<repo>.git`. The old
  `/<owner>/<repo>.git` form still means the default host.
- The proxy selects the credential by team + host + owner + repo
  (`TeamGitCredentialOn`), and forwards to the credential's own host
  (`CredentialHost.hostPort` and `base_url`). That replaces the one
  `RealForwarder`.
- Authorization, read scope, ref policy, audit and refusals apply as
  today, with the host added to the key and to the audit entry.

### 6. Forge API clients per host (phase 3)

- `pkg/github` and `pkg/forge` callers (PRs, checks, merges, branch
  protection, issues and origins) reach the repo's host through the
  daemon's routed client (section 12, "Server-side paths"): `CredentialHost.APIBase()` gives the API host and path prefix,
  and the credential comes from the same ladder.
- A repo on a `git`-kind host has no forge API. grain says so wherever a
  PR flow would start: merge queue, PR review, checks and origins. What
  works there is clone, fetch and push of a branch. A task on such a host
  ends with its branch pushed and no pull request.

### 7. UI (phase 4)

- Team settings get a **Git connections** list. Each entry shows host,
  kind, auth type and scope, and has a Test button. Admins can add any
  number of entries.
- The typed per-auth-type editor (PAT, SSH key, GitHub App, none) is in
  Team settings, Git identity, for the team's credential and each repo
  override (task 2366, section 9).
- Repo pickers accept repos on any host the team has a connection to.
- The deployment-wide GitHub URL becomes, at most, the default host for
  repos written without one.

### 8. End-to-end test (phase 5)

One team, two fake GitHub instances and a plain git server:

- tasks clone and push each repo through the right credential, and never
  through another host's;
- legacy single-credential teams and host-less repos behave exactly as
  before.

### 9. SSH keys (task 2366)

An SSH key is the third auth type, `<name>.ssh.json` (`pkg/gitproxy/sshkey.go`):
a private key with an optional passphrase, the host, port and login,
pinned `known_hosts`, an optional `path_prefix`, and an optional
`api_credential`.

- **Host keys are pinned, never learned.** On github.com, an empty
  `known_hosts` means GitHub's published Ed25519 and ECDSA keys. Any
  other host must give its key. The client offers only the algorithms of
  the keys it has pinned, and a server presenting any other key is
  refused before a command runs.
- **The proxy bridges HTTP to SSH** (`sshforward.go`). Sandboxes keep
  speaking smart HTTP. Every request gets its own SSH session:
  - `info/refs` runs the service and returns its advertisement;
  - a POST replays the advertisement, writes the body, and streams the
    answer back.

  This is exact for protocol v2, and for v0 fetch and push, whose
  stateless client resends its whole negotiation on every round.
  `GIT_PROTOCOL` comes from the sandbox's `Git-Protocol` header.
  Authorization, read scope, body limits, the ref policy and the audit
  all run before the transport is chosen, so they apply unchanged.
- **Git only, unless paired.** An SSH key has no forge API.
  `api_credential` names a PAT or App on the same host, and REST calls
  for repositories reached with the key use it
  (`CredentialSet.APICredential`). Without one, opening or merging a pull
  request fails before anything is sent, with `GitOnlyReason`'s sentence.
- **Limits.** Routing per host is phase 2, so today an SSH key serves
  repositories on the request's own host, which is the deployment's
  default. A key recorded for another host is refused by the same
  wrong-host guard as any other credential. Hashed and wildcard
  `known_hosts` entries are refused rather than matched.

### 10. Ambient: this host's own GitHub login (task 3719)

An ambient credential, `<name>.ambient.json` (`pkg/gitproxy/ambient.go`),
holds no secret. It records a host (default `github.com`; GHE hosts are
allowed) and a source:

- `gh` runs `gh auth token --hostname <host>`;
- `git` runs `git credential fill` with `protocol=https host=<host>` (the
  base URL's scheme and host[:port] when the host record has one) and
  reads `password`;
- `auto` (the default) tries `gh`, then `git`.

The commands run as the daemon's own account at the moment a token is
needed, with `HOME` set to that account's home directory from its passwd
entry. That is not the daemon's `HOME`: a host-mode unit points that into
the data directory, where no `gh` login lives. The token is cached in
memory for 5 minutes, and a 401 from the git proxy or the REST client
drops it, so a logout, re-login or rotation is followed. It is never
written to disk, the database, a log line or the state-repository export.

Every path that resolves a credential (the git proxy's clone, fetch and
push, the REST client's pull requests, merges and refs, porting, the
merge queue) goes through `CredentialSet.load`, so an ambient credential
works wherever a stored one does. An ambient token is a user token, so it
can't read the Checks API, the same as a PAT. When the host's login can't
produce a token, the request is refused with what to run, for example
"gh is not logged in to github.com: run `gh auth login --hostname
github.com` as alice on build-7".

Which credential the REST paths pick matters here, because an ambient
credential is only ever a team connection (below) and never a
`credentials.json` pattern. The daemon's REST client reads and posts for a
repository as the team that owns it: the first custom team targeting it,
otherwise the default team (`repoTeamCredential`, `pkg/daemon/
capabilities.go`), and the deployment ladder only when that team has no
connection for the host. A run's forked `grain mcpserver` has no store, so
the daemon tells it the credential its branch is pushed with by name
(`-pr-credential`, `agent.RunConfig.GitCredential`) and its
`pull_request_status`, `base_branch_status`, issue and merge-queue reads
are made as that credential. Before either, a host-mode install whose only
credential was the default team's ambient connection had an empty ladder,
every REST read went out unauthenticated, GitHub answered 404 for the
private repository, and a run that had just pushed could not open its pull
request.

**The trade-off.** Pushes and API calls act as whoever `gh` is logged in
as, with all of that account's access: every repository and organization
it can reach, not only the team's. So:

- It is only allowed where the operator's login can be reached:
  - **A daemon on the bare machine** (a development `grain demo` or
    `grain daemon`) runs `gh`, a registered source's tool or `git credential fill` itself.
  - **A daemon in the release container beside grain hostd**, which
    is every machine install with hostd (the default), reaches the login
    through hostd's `host-git` capability entry (`pkg/gitproxy/hostgit.go`).
    That entry starts `grain capability host-git` on the host, as the
    installing user, once per call. The server runs only a registered
    source's own commands: its token command, its package-token command,
    and `<tool> api` for a source whose forge traffic goes through its
    tool (`forgecli` in the tests). Each runs exactly as it would on the host. The daemon
    names the entry and hands hostd no argv. That matters because hostd's
    argv check would refuse every `<tool> api` endpoint as a path outside
    the data directory. The `git` source is refused here, because `git
    credential fill` would hand the container every login the account's
    helpers hold. `scripts/setup.sh` writes the entry into
    `/etc/grain/hostd.yaml` and adds it to an older allowlist on a re-run:

        capabilities:
          host-git:
            command: /usr/local/bin/grain
            args: [capability, host-git]
            pass_env: [HTTP_PROXY, HTTPS_PROXY, NO_PROXY, http_proxy, https_proxy, no_proxy]

    Saving or
    using an ambient connection is refused, saying which piece is
    missing, while hostd does not answer or its allowlist does not offer
    `host-git`. The daemon still holds the token it is handed, just as a
    daemon on the host does. design/design/cli-run-location.design.md ("Security")
    explains why that is no wider than before: grain is trusted as the
    installing user in both modes.
  - **A daemon in a container with no hostd** (`GRAIN_HOSTD=0`, or
    Kubernetes) has no login of the operator's to borrow. The daemon
    counts as containerised only on evidence of a container
    (`pkg/whererun`): `GRAIN_IN_CONTAINER=1`, which grain's image sets,
    `/.dockerenv`, `/run/.containerenv`, or a container runtime named in
    `/proc/1/cgroup`. Saving an ambient connection is refused with the
    signal it saw, and an `.ambient.json` dropped in by hand is never
    used.
- Only a system admin can create or test one (Team settings → Git
  connections, type "Ambient (this host's GitHub login)", or
  `grain git-ambient`). The pane shows the account it acts as, from
  GitHub's own `/user` lookup with the token, and warns when the login
  lacks the `repo` scope.
- Only the teams it is assigned to use it. It can't be a
  `credentials.json` pattern, so it is never a deployment-wide fallback,
  and it is never offered as a nameable capability (`ExtraNames`).
  Naming another team's credential already needs a system admin.
- Every git use is audited like any stored credential's, with an
  `(ambient: this host's gh login)` note on the outcome.

**The commit identity.** A sandbox commits as the account its git
credential acts as -- there is no commit author to type in. For a task
whose git credential for its target (`Store.TaskGitCredentialOn`)
resolves, `CredentialSet.CredentialGitIdentity`
(`pkg/gitproxy/credential_identity.go`; wired as
`orchestrator.Deps.CredentialGitIdentity` from `pkg/daemon/
git_identity.go`) derives it from the credential itself:

- **PAT** (classic or fine-grained): `GET /user` with the token -- which
  needs no scope or permission -- gives the display name (or login) and
  the login's noreply address `<id>+<login>@users.noreply.<host>`, which
  GitHub attributes to the account exactly as it does a verified one.
- **GitHub App**: `GET /app` with the App's JWT gives its slug, and
  `GET /users/<slug>[bot]` (asked with the installation token) the bot
  user's id, so runs commit as `<slug>[bot]
  <<id>+<slug>[bot]@users.noreply.<host>>` -- the shape GitHub's own Apps
  (`github-actions[bot]`) commit as, linked to the App.
- **SSH key**: whoever its paired API credential is.
- **Ambient**: what already exists on the host for that login
  (`CredentialSet.AmbientGitIdentity`, `pkg/gitproxy/ambient_identity.go`):
  1. the host account's own global `git config user.name` / `user.email`,
     read as that account the way the token is (`HostGitIdentity`, cached
     five minutes) -- what its owner commits as by hand; then
  2. the login itself as GitHub reports it: its display name (or login),
     and its noreply address. A plain git host has no `/user`, so only the
     helper's username is there.

Lookups are cached per credential and token (or App). A credential that
names nobody -- anonymous, an SSH key with no token paired, any other
credential on a plain git host -- or a lookup that fails leaves the
identity to a team's legacy `Team.AgentGitName`/`AgentGitEmail` (still
accepted by the API, no longer offered in the UI; it fills only a half the
credential leaves empty), then grain's own `grain agent
<grain-agent@localhost>` (`mcp.DefaultGitIdentity`). Team settings → Git
identity lists who each connection commits as (its status carries
`commitName` / `commitEmail`, or `commitPending` / `commitError`), and a
sandbox rebuilt mid-run (`recreate_sandbox`) is given the same identity
again.

**When to prefer something else.** Use a fine-grained PAT when the
deployment should reach only some repositories, and a GitHub App when it
is shared, needs the Checks API, or should act as a bot rather than a
person. Ambient suits one operator running grain as their own account on
their own machine (`google/scripts/cloudtop-install.sh` sets one up on the default
team with `GRAIN_CLOUDTOP_GIT_HOST=github.com`, when `gh` is logged in and no
`GRAIN_GITHUB_TOKEN` was given; its own default is depot, section 11). It
replaces seeding the store once with `gh auth token`, which stored a
snapshot that didn't follow logouts or rotation.

### 11. Ambient on a self-hosted forge (task 3722)

The measurements behind ambient credentials on one self-hosted,
proxy-fronted forge are an edition's own, and are documented with it
(`google/docs/depot-ambient.md` in the Google build). What they settled
for core is section 12: a forge reached through a registered command-line
tool, and a login redirect read as "renew the login".

### 12. A forge reached through a command-line tool (tasks 3905, 4529)

Some forges answer only a client that holds a credential grain cannot get
at: a corp login a host-side tool refreshes, say. A registered ambient
source can be **command-backed** for those (`AmbientCommandSource.ForgeViaCommand`,
`pkg/gitproxy/commandforge.go`): every forge API call goes through
`<tool> api`, git goes through the git proxy with the git auth header the
source names, a response that is the forge's login page reads as the
source's login hint rather than as a 404, and a 401 says what to run. The
daemon routes a host's forge calls through it whenever the team's
connection on that host names such a source (`pkg/daemon/forgeroute.go`),
and the connection editor offers its Source menu entry, presets and help
from the registration (`AmbientSourceUI`). Core's tests use the made-up
`forgecli` source on `*.code.forge.test` (`pkg/gitproxy/gitproxytest`).

Google's GitHub-on-Prem is the one such source today; its design and the
manual checks are in [google/docs/ghop.md](../google/docs/ghop.md).

## Phase 1: what this change does

- `model.NormalizeGitHost`, `GitHostKind`, `SameGitHost` and
  `ResolveGitHost` (`pkg/model/githost.go`).
- `Team.GitConnections`, the `git_connections` column with its migration,
  and `MatchTeamGitConnection`. `Store.TeamGitCredentialOn` resolves by
  host, with the legacy credential folded in. `NoTeamGitCredentialError`
  names a non-default host.
- Credential host records: `HostOf`, `SetHost`, `MigrateCredentialHosts`,
  and `Remove` deletes the record too.
- The wrong-host refusal in the proxy (`Credential.WrongHost`).
- App installation tokens minted against the credential's own host API,
  including GHE's `/api/v3`. `appsUnavailable` and its github.com-only
  refusal are gone.
- Tests:
  - an App credential on a fake GHE mints from that host's `/api/v3`, and
    the minted token is the one forwarded;
  - another host's credential is refused, with nothing sent;
  - an App on a plain git host is refused;
  - scope precedence across three hosts on one team;
  - legacy teams, including on a GHE-default deployment.

## Phase 2: what this change does

- `model.RepoRef.Host`, empty for the default host
  (`model.SetDefaultGitHost`, set from `-github-host` beside
  `Store.GitDefaultHost`). `ParseRepo` reads `host/owner/name`, URLs and
  scp remotes, dropping the default host; `String` writes the host only
  off the default host.
- **Storage without a schema change.** The host rides in the existing
  owner column as `host/owner` (`RepoRef.StoredOwner`,
  `RepoFromStored`). An owner can never contain a slash, so the split is
  unambiguous, and a default-host row is byte for byte what it was. Task
  targets, run pins, reads, writes, schedules, templates, suite runs,
  repo config and team repo config carry it. The state repo writes
  `host/owner/name` and splits on the last slash reading it back.
- **Proxy.** `/<host>/<owner>/<repo>.git` beside the old form.
  Authorizers see `host/owner` (`GitRequest.ScopeOwner`), so scope, CI
  read/write scopes and the ref policy keep hosts apart with unchanged
  interfaces. Credentials resolve per host (`GitCredentialOverrideOn`,
  `TaskGitCredentialOn`, `TeamGitCredentialOn`, `GetForRepoOn`,
  `SelectForRepoOn`); a lookup that only knows the default host is
  refused for any other. The wrong-host guard is now "the credential's
  recorded host is the request's host", checked before anything is
  read or minted. Requests go to the credential's own host:
  `GitProxy.Forwarder` for the default host, `ForwarderFor` or a
  `RealForwarder` to the host's `base_url` otherwise. The audit entry
  carries the host.
- Sandbox clone URLs (`orchestrator.CloneURL`) carry the host.
  `.git-credentials` needs nothing new: it names the proxy, which is one
  host whatever repository is behind it.
- Not in this phase: forge state keyed by repository (merge queue,
  releases, branches, base-branch CI, PR origins) still reads and writes
  plain owner/name. Those are phase 3's, along with the forge API
  clients that would act on them.

## Phase 3: what this change does

- **Forge API clients and transports per host.**
  - `pkg/github` and `pkg/daemon` callers (`PullRequestReader`, `issueReader`, `githubDefaultBranchReader`) construct their transport from the repository's host using `repoTransport` and `CredentialHost.APIBase()`, obtaining the API host and path prefix (`/api/v3` for GitHub Enterprise, `api.github.com` for github.com).
  - Credentials resolve per host via the git connections (`TeamGitCredentialOn`, now `Store.GitConnectionOn`).
- **Plain-git hosts with no forge API.**
  - Repositories on plain git hosts (`model.GitHostGit`, or hosts where `APIBase()` reports `ok == false`) have no forge API: PR flows (pull requests, PR review, checks, origins, and merge queue) are refused cleanly with clear diagnostic explanations.
  - `credentialTokenSource.GitOnly` returns descriptive refusal when a repository is hosted on a plain git host or reached with an SSH key lacking paired API credentials.
  - In `pkg/orchestrator`, `OpenPullRequestForTask` refuses PR creation for git-only repositories and plain git hosts.
  - In `pkg/orchestrator/finish.go` (`salvagePushedBranch`), tasks whose pushed branches target plain git hosts are completed cleanly without opening a pull request, leaving an explanatory note on the task.
  - In `pkg/daemon/mcpserver.go`, `QueryMergeQueue` reports that the merge queue is unavailable for repositories on plain git hosts.
