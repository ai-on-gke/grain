# 2026-10-17: connect a single-tenant Grain to a local Silo hub

Task 4346. A team admin and a Grain owner connect a single-tenant Grain to
a Silo hub, then take work from it. A team admin sets up a team and mints a
node token in the hub UI, and the Grain signs in under a local policy. A
team member files a task in the hub, and the walkthrough follows it into the
Grain. It then checks a refusal, restarts both sides and revokes the token.
Task 4243 repeats this against the deployed hub.

## Setup

| | |
|---|---|
| Host | The task sandbox: Debian 12, Docker 29, `/dev/kvm`, Node 20.19, Go 1.26.6 |
| Hub | bwsalmon/silo `main` at 0399bba, UI bundle built (`cd ui && npm ci && npm run build`, so U1, U2, U3 and U7 are all in). `silo hub serve -addr 127.0.0.1:8080 -database sqlite:hub.db -dev-user-header X-Silo-User -admins user:admin@acme.test`, as in README "To run a hub locally" (with the header name ui.md §7.1 uses). A second hub on :8081 signs in with OIDC against silo's `internal/fakeoidc`, to check `grain hub login` itself |
| Grain | bwsalmon/grain `main` at 7fa4fa1d (v1.12.0-rc.1+), UI built. Installed with `grain init workstation` (single tenancy, starter policy) |
| People | `admin@acme.test` (hub admin), `alice@acme.test` (team admin of `widgets`, Grain owner), `bob@acme.test` (team member) |
| Browser | Playwright 1.62 Chromium, headless. Hub pages send `X-Silo-User` (the dev sign-on). Grain pages send the access code as a bearer token |
| Stubs | No hub or grain API is mocked. Stand-ins: silo's `scripts/fake-github.py` as the Grain's GitHub (`-github-host 127.0.0.1:18999 -github-insecure-http`), and **no sandbox backend** (`-sandbox-backend none`, `-hub-slots 2`), as silo's `scripts/grain-interop.sh` runs it. See step 2 for why |
| Not run | A real run: no sandbox image, no model and no real GitHub here, so no diff, PR or merge (step 4) |

## Results

| # | Step | Result |
|---|---|---|
| 1 | Hub up; team, members and repos in the UI; mint a node token | **PASS**. The hub admin creates `widgets` on /teams. On the team page they add alice as admin and assign `acme/widgets` and `acme/outside`. alice adds bob as a member, then mints a token for node `walk-grain` on /nodes, shown once with a copy button ([01](2026-10-17-grain-hub-connect-walkthrough/01-hub-team-created.png), [02](2026-10-17-grain-hub-connect-walkthrough/02-hub-team-members-repos.png), [03](2026-10-17-grain-hub-connect-walkthrough/03-hub-node-token-minted.png)) |
| 2a | `grain init workstation` with a policy for the test repo only | **FAIL** (B1). On a docker machine init picks `container` and writes a `daemon.sh` that exits at once: `-container-git-proxy-host is required with -container-sandboxes`. The starter policy itself is right (`repos: ["acme/widgets"]`, fails closed). Continued with `-sandbox-backend none` |
| 2b | `grain hub login`, following the docs | **PASS against an OIDC hub; FAIL against the README's dev hub** (B6). With OIDC, `grain hub login -id-token-file` mints and writes `hub-node-token` (0600), and a second login revokes the first. The README's dev-header hub answers `401 Unauthorized: unauthenticated` with no hint. A token minted in the hub UI has no documented way into grain (B5): it works written to `<data-dir>/hub-node-token`, plus `-hub-node-id walk-grain`, which init doesn't write |
| 3 | Registration: `online` on /nodes | **PASS** once the node ID matched: online within 1 s, 2 slots, `acme/widgets`, owner alice ([04](2026-10-17-grain-hub-connect-walkthrough/04-hub-nodes-online.png)). With init's default node ID (the host name) the hub refuses it: `this credential is for node "walk-grain", not "debuerreotype"`. The daemon then stops trying, and nothing says to pass `-hub-node-id` (B4) |
| 4a | File in the hub as a team member; placed on the Grain | **PASS after undocumented setup** (B5). bob's first two tasks came back `queued`, "refused by node walk-grain: refused by this node's policy" ([05](2026-10-17-grain-hub-connect-walkthrough/05-hub-task-filed.png)). The Grain's log gives the real reasons: no GitHub credential on the default team, then `user "bob@acme.test" does not have permission to submit tasks for team "default"`. After adding a git connection and making bob a Grain user, a requeue placed it: `running` on `walk-grain` as Grain task 3 ([08](2026-10-17-grain-hub-connect-walkthrough/08-hub-task-running.png)) |
| 4b | It appears in the Grain's UI (with the access code) | **PASS with a bug** (B3). The access-code page, then task 3 tagged "from hub" with its hub task ID and a link to the hub page ([07a](2026-10-17-grain-hub-connect-walkthrough/07a-grain-access-code.png), [07](2026-10-17-grain-hub-connect-walkthrough/07-grain-task-from-hub.png)). It is `proposed`, and the page says "Approve it on the hub, not here", but the hub has no way to approve it. Meanwhile the hub says `running`. `grain approve 3` on the CLI works |
| 4c | Live events in the hub UI | **PASS** as far as a run got. The task page streams an `artifact` (Grain's comment), then `running · attempt 1 · started` and `failed: setup-failed` (no sandbox backend here) ([09](2026-10-17-grain-hub-connect-walkthrough/09-hub-task-events.png)) |
| 4d | The view-through shows the diff | **PARTIAL**. The view-through works for the comment artifact: `GET /v1/tasks/{id}/artifacts?ref=comment/2` returns the Grain's text. There was no diff to view without a real run |
| 4e | Lands through the Grain's merge queue | **NOT RUN** (environment: no sandbox, model or real GitHub) |
| 4f | Final state shows in the hub | **NOT RUN** for a merge. A cancel and a close reaching the hub are covered by silo's `grain-interop` CI job |
| 5 | Refusal: a repo outside the Grain's policy | **PASS in the hub, FAIL in the Grain's UI** (B4). Not advertised by the Grain, the `acme/outside` task sits `queued` with no reason at all (B6, [10](2026-10-17-grain-hub-connect-walkthrough/10-hub-refusal-unadvertised.png)). Advertised (`-hub-repos acme/widgets,acme/outside`), it is refused: the hub shows "refused by node walk-grain: refused by this node's policy" and excludes the node ([11](2026-10-17-grain-hub-connect-walkthrough/11-hub-refused-by-policy.png)). The Grain logs `local policy does not allow repository acme/outside on this machine`, but its UI shows nothing ([12](2026-10-17-grain-hub-connect-walkthrough/12-grain-tasks-after-refusal.png)) |
| 6a | Stop the Grain mid-task and restart it | **FAIL** (B2). On start the Grain reaches the hub before its task API is up: `resuming hub task … (task 3): no free slot: this grain's task API is not running`. The hub marks the task `lost` and refuses the waiting offer too. A manual requeue on the hub put it back on the **same** Grain task 3: no duplicate |
| 6b | Restart the hub | **PASS**. `the hub closed the connection: the hub is shutting down; reconnecting`, then reconnected and `resumed hub task … as task 3` within 2 s. The task stayed `running` |
| 7 | Revoke the node token | **PASS at the hub, FAIL for clarity at the Grain** (B4). Revoking on /nodes takes the node `offline` at once, and the hub refuses each reconnect ("unknown or revoked node token") ([13](2026-10-17-grain-hub-connect-walkthrough/13-hub-token-revoked.png)). The Grain logs only `websocket.Dial …: bad status; retrying in 32s` and keeps retrying, and its UI says nothing. A token newly minted and written to `hub-node-token` was picked up on the next retry (30 s), with no restart, and the hub task resumed |
| 8 | Error messages say what to do next | **FAIL**. Good: the Grain's local refusal reasons (git credential, team permission, policy), `grain hub login`'s output, and the hub's 401 on the access-code page ("Run grain access-code…"). Not good: B1's flag error, B2, B3's note, B4's "bad status" and node-ID refusal, the hub's "refused by this node's policy" for what are configuration gaps, and B6's silent `queued` |

## Bugs filed

Grain (bwsalmon/grain):

- **B1.** `grain init workstation`: the container `daemon.sh` it writes refuses to start (`-container-git-proxy-host is required`).
- **B2.** On restart the daemon connects to the hub before its task API is up, and its in-flight hub tasks go `lost`.
- **B3.** Grain's UI tells the owner to approve a hub task "on the hub", where nobody can, while the hub shows it `running`.
- **B4.** The hub connection is invisible in Grain's UI, and its log errors don't say what to do (revoked token, wrong node ID, refused offers).
- **B5.** Quickstart step 6 leaves out what a Grain needs before it accepts a hub task: team users, a git credential, the node ID, and how to use a UI-minted token.

Silo (bwsalmon/silo):

- **B6.** The hub doesn't say why a task isn't placed (no node serves its repo), and the README's dev-mode hub can't be used with `grain hub login`, with a bare 401.

## Commands

The hub, its team, a member and the token (the hub UI steps were driven
with Playwright; the API equivalents are below):

```sh
silo hub serve -addr 127.0.0.1:8080 -database sqlite:$PWD/hub.db \
  -dev-user-header X-Silo-User -admins user:admin@acme.test
# UI: /teams -> create "widgets"; /teams/widgets/members -> add user:alice@acme.test (admin),
#     assign acme/widgets and acme/outside; as alice add user:bob@acme.test;
#     /nodes -> mint token for node walk-grain.
```

`grain hub login` against the README hub:

```
$ grain hub login -hub-url http://127.0.0.1:8080 -id-token-file idt -hub-node-id walk-grain
grain hub: the hub refused to mint a token for node "walk-grain": 401 Unauthorized: unauthenticated
```

and against an OIDC hub (fakeoidc):

```
$ grain hub login -hub-url http://127.0.0.1:8081 -id-token-file id-token -data-dir g -hub-node-id walk-grain-2
Node token 04fc02de24931f1f for node "walk-grain-2", owned by alice@acme.test, written to …/g/hub-node-token.
A running daemon uses it when it next reconnects; one that stopped after the hub refused its old token needs a restart.
```

The Grain:

```
$ grain init workstation -non-interactive -data-dir $GD -repo acme/widgets -owner alice@acme.test \
    -sandbox-backend container -hub-url http://127.0.0.1:8080 -hub-login=false \
    -install-service=false -start-service=false
Wrote the starter policy …/grain_policy.yml.
Wrote …/daemon.sh, the daemon's command line.
$ $GD/daemon.sh
grain daemon: -container-git-proxy-host is required with -container-sandboxes      # B1
$ # daemon.sh edited to -sandbox-backend none; token from the UI written to $GD/hub-node-token
$ $GD/daemon.sh -ci=false -github-host 127.0.0.1:18999 -github-insecure-http
grain hub: no longer registered with http://127.0.0.1:8080: rendezvous: hub refused the node:
  rendezvous: this credential is for node "walk-grain", not "debuerreotype"         # B4
$ $GD/daemon.sh -ci=false -github-host 127.0.0.1:18999 -github-insecure-http -hub-node-id walk-grain -hub-slots 2
grain hub: registering node "walk-grain" with http://127.0.0.1:8080: 2 slot(s), repos acme/widgets
```

The Grain's side of the first offers, and the setup they needed (B5):

```
grain hub: refusing hub task t-ffcd357ba4ebe640: team "default" has no GitHub credential, so it cannot reach acme/widgets -- …
$ curl -H "Authorization: Bearer $(cat $GD/local-api-token)" -X POST :4242/api/teams/default/git-connections \
    -d '{"host":"127.0.0.1:18999","baseUrl":"http://127.0.0.1:18999","kind":"github","value":"walk-token"}'
grain hub: refusing hub task t-304b16ac8459afff: user "bob@acme.test" does not have permission to submit tasks for team "default"
$ curl … -X PUT :4242/api/teams/default -d '{"users":["debian","alice@acme.test","bob@acme.test"]}'
$ curl -H 'X-Silo-User: bob@acme.test' -X POST :8080/v1/tasks/t-304b16ac8459afff/requeue -d '{}'
grain hub: accepted hub task t-304b16ac8459afff as task 3
$ grain approve 3          # the UI says to approve it on the hub (B3)
state:      queued
```

Refusal, restart and revocation:

```
grain hub: refusing hub task t-3e81edb2ee118872: local policy does not allow repository acme/outside on this machine
hub:       tasks: node walk-grain refused t-3e81edb2ee118872: "refused by this node's policy"

# Grain restarted (B2)
grain hub: resuming hub task t-304b16ac8459afff (task 3): no free slot: this grain's task API is not running
hub:       {"state":"lost","reason":"lost the node while the task ran","node_task":"3"}

# hub restarted
grain hub: the hub closed the connection: the hub is shutting down; reconnecting
grain hub: resumed hub task t-304b16ac8459afff as task 3

# token revoked on /nodes (B4)
hub:       rendezvous: refused a node from 127.0.0.1:48374: directory: unknown or revoked node token
grain hub: connecting to the hub: rendezvous: dial ws://127.0.0.1:8080/v1/nodes/connect: websocket.Dial …: bad status; retrying in 32s
# a new token written to hub-node-token
grain hub: resumed hub task t-304b16ac8459afff as task 3
```

## Screenshots

| | |
|---|---|
| [01](2026-10-17-grain-hub-connect-walkthrough/01-hub-team-created.png) | Hub /teams after the admin creates `widgets` |
| [02](2026-10-17-grain-hub-connect-walkthrough/02-hub-team-members-repos.png) | The team page: alice admin, both repositories |
| [03](2026-10-17-grain-hub-connect-walkthrough/03-hub-node-token-minted.png) | alice's token for `walk-grain`, shown once |
| [04](2026-10-17-grain-hub-connect-walkthrough/04-hub-nodes-online.png) | /nodes: `walk-grain` online, 2 of 2 free |
| [05](2026-10-17-grain-hub-connect-walkthrough/05-hub-task-filed.png), [05-form](2026-10-17-grain-hub-connect-walkthrough/05-hub-task-filed-form.png) | bob files a task: refused, "refused by this node's policy" |
| [06](2026-10-17-grain-hub-connect-walkthrough/06-hub-task-placed.png) | The second task, refused for bob's Grain membership |
| [07a](2026-10-17-grain-hub-connect-walkthrough/07a-grain-access-code.png) | The Grain's access-code page |
| [07](2026-10-17-grain-hub-connect-walkthrough/07-grain-task-from-hub.png) | Grain task 3, "from hub", "Approve it on the hub, not here" |
| [08](2026-10-17-grain-hub-connect-walkthrough/08-hub-task-running.png) | The hub's view: `running` on walk-grain as 3 |
| [09](2026-10-17-grain-hub-connect-walkthrough/09-hub-task-events.png) | Live events: the comment artifact, run started, setup-failed |
| [10](2026-10-17-grain-hub-connect-walkthrough/10-hub-refusal-unadvertised.png) | `acme/outside` with no node serving it: `queued`, no reason |
| [11](2026-10-17-grain-hub-connect-walkthrough/11-hub-refused-by-policy.png) | `acme/outside` refused by the Grain's policy |
| [12](2026-10-17-grain-hub-connect-walkthrough/12-grain-tasks-after-refusal.png) | The Grain's task list: nothing about the refusal |
| [13](2026-10-17-grain-hub-connect-walkthrough/13-hub-token-revoked.png) | /nodes after revoking: offline, token revoked |
