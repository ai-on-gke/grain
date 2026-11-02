# Driving grain from an external CLI

`contributions.design.md` says designs are written "in interactive sessions between PMs, engineers and agents", outside grain, "with any interactive coding agent or by hand". After that, grain takes over: it generates specs and tasks, implements, reviews and merges. In practice the engineer's interactive agent keeps working after the design merges. That agent is Jetski / Antigravity, Claude Code, Codex CLI or Gemini CLI, running on a workstation or a laptop. The engineer uses it to hand work to grain, check on that work, answer grain's questions, and sometimes write the code themselves and ask grain to review and land it.

Today all of that is possible, but it takes effort. This design makes it effortless. An engineer should be able to tell their agent "have grain do these three things and tell me when they've merged", and the agent should know how to do it without being taught each time.

## What exists today

Grain already has nearly all the pieces. They just don't fit together for someone working from a workstation.

- **The task CLI** (`pkg/cli/main.go`, `taskcmd.go`, `cicmd.go`) runs on any machine. Its verbs are `list`, `get`, `create` (with `-depends-on`, `-position front|end`, `-repo`, `-base`, `-field`), `comment` (which also answers a parked question), `ci list|show|watch|run`, `ci faults`, `retry`, `retry-merge`, `settings` and more. Global flags are `-server`, `-token`, `-audience`, `-as` and `-json`.
- **Finding the server** (`cliconn.go`). The CLI takes `-server`, then `$GRAIN_SERVER`, then `/etc/grain/cli.env`, then `http://127.0.0.1:4242`. That fits a CLI on the daemon's own host. It does not fit an engineer with two deployments and neither of them local.
- **Credentials** (`pkg/ui/httpclient.go`). The CLI tries `-token` / `$GRAIN_TOKEN` (and its aliases), then the local API token for a loopback server, then a Google ID token from ADC or `gcloud auth print-identity-token --audiences=<url>` for an `https://` or `*.run.app` server. Nothing is saved between commands, so a remote user either exports the access code in every shell or passes `-token` on every call.
- **External agents** (`docs/agent-api.md` § External agents, `pkg/ui/external_agents.go`). An operator can enable `POST /mcp` and issue per-agent `gma_…` tokens. Each token carries a scope set, a repository limit, an expiry, a rate limit and a concurrency cap. Calls run as `{agent, external:<name>}`, are journaled, and leave an audit comment on any task they change. The same escalation rails apply as for in-grain agents: an external agent cannot grant itself capabilities, enable auto-merge, or merge without review.
- **The namespace** (`design/design/grain-namespace.design.md`). `ci://`, `src://`, `task://`, `config://`, `memory://` and `deploy://` are served at `/fs/<scheme>/<path>`, with `?list`, `?glob`, `?grep`, `?tail` and `?follow`. They are only mounted on the in-sandbox git proxy (`127.0.0.1:9728`), not on the daemon's main API.
- **The in-sandbox CLI** (`design/design/sandbox-first-agent-surface.design.md`). It covers the same verbs an external agent wants: `grain ls|read|grep`, `grain ci wait` with resumable long-polls, `grain task wait`, `grain merge-queue`, `grain pr status`. It is scoped to one task's branch and authenticated by the sandbox's proxy credential.
- **Adopting someone else's PR** (`docs/github-bot.md`, `pkg/orchestrator/takeover.go`). Commenting `@<slug> take over` on a PR, or adding the `grain:take-over` label, files a shepherd task. Grain files a code review (and a security review when needed) for every head, holds the merge queue until those reviews pass, and carries the PR to merge. `@<slug> review` asks for a review only.
- **Reaching a deployment.** Behind IAP, the CLI's ID-token path works. A single-tenant deployment takes the access code as a bearer token from any address (`docs/access-code.md`). Deployments behind a corporate identity-aware proxy are reached through a wrapper distribution, which imports this module. Core has no doc for that path and must not name corp tooling (`tests/hygiene/boundary_test.go`), so this design leaves it to a credential-source hook that a wrapper distribution registers (below).

The gaps come down to four things:

1. The CLI has no memory of which deployment it talks to or how it authenticates.
2. Its output is not consistently machine-readable.
3. It has no blocking or streaming primitives outside the sandbox.
4. An external agent has nothing that tells it grain exists or how to use it.

## The model

The engineer's agent drives grain the same way the engineer would: by running the `grain` CLI in a shell. The CLI is the contract. The skill file and the optional MCP bridge are thin layers over it, so every journey here also works by hand.

```
 Jetski / Claude Code / Codex / Gemini CLI  (workstation)
        |  shell: grain ...            |  (optional) stdio MCP: grain mcp
        v                              v
   grain CLI  -- context: server URL + credential source + identity
        |  HTTPS: Authorization: Bearer <gma_… | access code | ID token>
        |         X-Grain-Client: jetski/<session>
        v
   grain daemon  /api/...  /fs/...  (agentAPIGate: scope, repo, rails, journal)
```

### Connection and auth: `grain login` and `grain context`

**Contexts.** `~/.config/grain/config.json` (mode `0600`, honouring `$XDG_CONFIG_HOME`) holds named contexts:

```json
{
  "current": "team-prod",
  "contexts": {
    "team-prod": {"server": "https://grain.team.example.com", "auth": "external-token", "credential": "keyring:grain/team-prod"},
    "personal":  {"server": "https://grain-abc123.run.app",   "auth": "google-id-token", "audience": "https://grain-abc123.run.app"},
    "workstation":  {"server": "http://127.0.0.1:4242",           "auth": "local"}
  }
}
```

- `grain context list|use <name>|show|delete <name>` manage them. `grain --context <name> …` and `$GRAIN_CONTEXT` pick one for a single call.
- The CLI resolves the server in this order: `-server`, then `$GRAIN_SERVER`, then the selected context, then `/etc/grain/cli.env`, then the built-in default. This extends `serverSource` in `cliconn.go`, so a daemon host with no config file behaves exactly as it does today. `explainDaemonError` already names where the server came from; it gains `context <name>` as one more source.

**`grain login <url> [--name <ctx>]`.** This command probes the server and stores what it learns, so later calls never prompt.

1. `GET /api/auth/whoami` (new, unauthenticated-safe) reports which credential kinds the daemon accepts: access code, `gma_` external-agent tokens, IAP assertions, or proxy identity. It also reports whether `externalAgentApi` is enabled.
2. Login then picks a credential source:
   - **IAP or Cloud Run.** If the server answers through IAP or is `*.run.app`, login checks that the existing ADC / `gcloud auth print-identity-token --audiences=<url>` path mints a token the server accepts. It records `auth: google-id-token` and the audience, and stores no secret. Each call mints a fresh token through the code in `httpclient.go` today.
   - **External-agent token (the default for agents).** If the server accepts `gma_` tokens, login offers to mint one: `grain login --issue-token --scope-set grain/external-dev --repos ai-on-gke/grain,gke-labs/granule --expires 30d`. A credential for a non-dangerous scope set can be issued by whoever the login session authenticated as, through `POST /api/external-agents/tokens`. A dangerous one still needs a person and is refused here. An operator can also mint the token in the UI and paste it into `grain login --token-stdin`.
   - **Access code.** On a single-tenant deployment the access code is the only credential, and login reads it from stdin. The CLI warns that this grants everything, and suggests minting a scoped `gma_` token instead once logged in.
   - **Corp proxy.** A credential-source hook (`cli.RegisterCredentialSource`, matching the registries core already uses for a wrapper distribution) lets a wrapper distribution add `auth: corp-proxy`. That source mints the corp proxy's credential from the user's existing corp sign-in session. When the session has expired it **fails fast**, naming the command that renews it. It never follows a login redirect: a `302` to a sign-in page, or an HTML body where JSON was expected, is reported as `not signed in to <proxy>: run <fix>` instead of hanging or being parsed as data.
3. Secrets go in the OS keyring (Secret Service, macOS Keychain, Windows Credential Manager) under `grain/<context>`. On a headless Workstation with no keyring they go in `~/.config/grain/credentials/<context>` with mode `0600`, and the CLI refuses to read a file that is group- or world-readable. `config.json` never holds the secret itself.

`grain logout [<ctx>]` deletes the stored secret and, for a token login minted, revokes it (`POST /api/external-agents/tokens/{id}/revoke`). `grain whoami` prints the context, server, principal (`external:jane-jetski`, or a human identity behind IAP), scopes and repository limits. It is the first thing the skill tells an agent to run.

### An agent-friendly CLI surface

**Output.** Every verb supports `--json`, and its JSON is stable: field names match the `/api` response types, and new fields are only ever added. Without `--json`, output is compact text with one line per item, ids first and no box drawing, so it is cheap for a model to read and still pleasant for a person. `--json` fixes several things that are wrong today:

- `--json` is accepted after the verb as well as before it.
- An error under `--json` prints `{"error": {"code": …, "message": …, "fix": …}}` on stderr.
- Exit codes are fixed: `0` success, `1` the thing asked about failed (CI red, task failed), `2` usage, `3` auth, `4` unreachable, `5` timed out while waiting.

The verbs an external agent needs, existing and new:

| Verb | Status | Notes |
|---|---|---|
| `grain list`, `grain get <id>` | exists | `get` gains `--comments-since <ts>` so a poller reads only what is new |
| `grain create` | exists | `--depends-on`, `--position front\|end`, `--repo`, `--base`, `--field`; gains `--body-file -` and `--design <path>` (below) |
| `grain comment <id>`, `grain reply <id>` | `comment` exists; `reply` is new | `reply` answers the task's parked question and refuses if nothing is parked, so an agent cannot mistake a comment for an answer |
| `grain inbox` | new | the same rows as the UI Inbox: parked questions, warning/critical notifications, held merges, unapproved proposals |
| `grain wait` | new | see below |
| `grain logs <id> [--follow]` | new | the live transcript or status line of a task's current run |
| `grain ci …` | exists | `list`, `show`, `watch`, `run`, `faults`, `cache`; gains `ci wait`, `ci retrigger` and `ci test-history`, sharing code with the in-sandbox versions |
| `grain merge-queue` | new outside the sandbox | batches, queued PRs, held gates (`GET /api/merge-batches`, `/api/pipeline/changes`) |
| `grain pr adopt\|review\|status <pr-url\|owner/name#n>` | new | see CUJ 3 |
| `grain fs ls\|cat\|grep <uri>` | new outside the sandbox | see below |

**`grain wait <id…> --until=<cond> [--timeout=30m]`.** This blocks until every named task reaches the condition, then prints each task's state, outcome, PR URL and closing note, and exits `0`. If the timeout runs out first, it exits `5` and prints a resumable command line.

- The conditions are `closed`, `merged`, `awaiting-reply` (`StateAwaitingReply`: a question is parked), `ci-done` (the task's latest CI run concluded), and `any` (the first change of state). `--until=awaiting-reply,merged` wakes on whichever happens first. That is how an orchestrating agent finds out about a question without polling.
- On the server this is a long-poll on a new `GET /api/tasks/wait?ids=…&until=…&timeout=…`, under `tasks:read`, which holds for up to 60s. The CLI re-issues it until its own timeout. It reuses the change feed the UI's live updates already read, and does not poll the database per caller.
- An external caller earns no wait credit. Wait credit only applies to a grain run.
- `grain ci wait --pr <url>` and `grain ci wait <run-id>` do the same for one CI run.

**`grain fs` over the main API.** The namespace server is mounted a second time on the daemon's own listener, at `GET /api/fs/<scheme>/<path>`. It supports the same `?list`, `?glob`, `?grep`, `?tail`, `?follow` and `Range` as the sandbox proxy, so `grain fs cat ci://ai-on-gke/grain/ci-1942/go/gotest.log?grep=FAIL` works from a laptop.

- Grants are mapped to scopes: `ci://` needs `ci:read`, `task://` needs `tasks:read`, `deploy://` needs `deployment:read`, and `config://` needs `settings:read`.
- `src://` (grain's own source) needs `deployment:read` and is off for external tokens unless the scope set names it explicitly.
- Secret redaction and size limits are the namespace server's own, unchanged.
- `grain fs` and the in-sandbox `grain ls|read|grep` are one implementation with two transports.

### Teaching the external agent: `grain skill` and the optional `grain mcp`

**`grain skill install [--jetski|--claude|--codex|--gemini|--dir <path>] [--project]`** writes a `SKILL.md` for the current context into the agent's skill directory:

| Target | User-level | `--project` |
|---|---|---|
| Jetski / Antigravity | `~/.gemini/jetski/skills/grain/SKILL.md` | `.agents/skills/grain/SKILL.md` |
| Claude Code | `~/.claude/skills/grain/SKILL.md` | `.claude/skills/grain/SKILL.md` |
| Codex CLI, Gemini CLI | the tool's skills or instructions directory, when it has one; otherwise `--dir` | |

A skill's description is a couple of lines that the agent keeps loaded; the body is read only when the skill is invoked. So the skill costs almost nothing until the engineer mentions grain. The file has two parts:

- A **static part** shipped in the binary (`pkg/cli/skill/SKILL.md.tmpl`): when to reach for grain, the verbs, the `--json` and exit-code contract, the `wait` patterns, and rules like "answer a parked question with `grain reply`, never with `grain comment`" and "never `force-merge`".
- A **deployment part** filled in from `GET /api/config` and `grain whoami` at install time. It lists the context name, the repos this credential can reach, the templates and custom fields `grain create -h` would list, whether the repo uses the `design/` workflow, and the scope set held, so the agent doesn't suggest something it will be refused.

The file records the grain version and the hash of the deployment part. `grain skill install` is safe to run again, and `grain skill status` says whether the installed skill is stale. Any `grain` call whose context's skill is stale prints a one-line note on stderr.

**`grain mcp`** is a local stdio MCP server for agents that prefer native tools to a shell. It is optional. It serves a small, hand-picked set of tools named after the in-grain ones so prompts carry over: `propose_task`, `read_grain_task`, `list_grain_tasks`, `wait_for_task`, `reply_to_task`, `comment_on_task`, `grain_inbox`, `ci_runs`, `ci_log`, `merge_queue`, `fs_read`, `fs_grep`, `adopt_pull_request`. Each one calls the same code path as its CLI verb, with the user's `grain context` credentials.

- It is not a proxy for remote `POST /mcp`. That endpoint stays the way to hand an agent the full generated `api_*` surface, which is useful for an unattended orchestrator but too large for an interactive agent's context window.
- `wait_for_task` slices its long-poll to stay under the client's tool-call timeout and returns a `resume` hint, as `grain ci wait` does in the sandbox.
- `grain skill install --mcp` adds the server entry to the agent's MCP config alongside the skill.

### Attribution and scoped tokens

The **operator** is the person the credential belongs to. Every `gma_` token gains an `owner` field: the human principal who issued it (set by `grain login --issue-token`, or picked in the UI). Today a call runs as `{agent, external:<name>}`. It keeps that principal, and the journal line and the audit comment it leaves now also carry `on behalf of <owner>`.

A task filed by an external agent records `Author` as the owner rather than the daemon's own actor. That is the identity `attribution.design.md` credits as a design-folder contributor, and the one `grain list -author <owner>` finds. The `-as` / `X-Forwarded-User` header is still ignored as an identity claim (`pkg/ui/principal.go`), because anyone can send it. The owner comes from the credential.

The **tool and session** come from a new `X-Grain-Client: <tool>/<version> session=<id>` header. The CLI fills it from `$GRAIN_CLIENT` and `$GRAIN_CLIENT_SESSION`, which the skill tells the agent to export (Jetski: `jetski/<ver>`, and the conversation id when the agent knows it). The daemon stores it on the task as `filed_via`, and on each comment, and shows it as "filed from Jetski by Jane". This header is a label, not a credential: it never widens or narrows what a call may do. It lets a person find every task one agent session produced (`grain list --filter filed_via:has:session=…`).

**Scopes.** A new built-in scope set, `grain/external-dev`, is what `grain login --issue-token` uses by default:

- `tasks:read`, `tasks:write`, `ci:read`, `ci:write`, `merge:write`, `repos:read`, `templates:read`, `deployment:read` (for `deploy://merge-queue.md` and health).
- Explicitly not: `tasks:admin` (approving proposals), `merge:force`, `settings:write`, `secrets:*`, `connections:write`, `capabilities:admin`, or the dangerous `deployment:*` scopes.
- It needs no person to grant and is normally limited to the repos named at login.

Tasks filed through it land **proposed** unless the deployment's proposal trust gate (`externalAgentFilingEscalation`) auto-approves that owner's filings. A team that wants its engineers' agents to queue work directly configures that once, for those owners and repos. An agent cannot grant it to itself. A `read-only` variant (`grain/external-read`: the `:read` scopes only) suits an agent that should watch but not act.

## CUJs

Each journey below follows the same format: what the engineer wants, what Jetski runs, what grain does, and how the result gets back to Jetski. Commands are as Jetski would run them; the `--json` flags an agent would add are left out where they don't change the point.

### CUJ 1: One-time setup

The engineer, on a fresh Workstation, says: "Set me up to use our team's grain."

1. **Install.** Jetski runs the install line the deployment shows on its Settings → CLI page, which downloads the release binary that matches the server's version:
   ```sh
   curl -fsSL https://grain.team.example.com/cli/install.sh | sh   # -> ~/.local/bin/grain
   grain version
   ```
2. **Log in.**
   ```sh
   grain login https://grain.team.example.com --name team --issue-token \
       --scope-set grain/external-dev --repos ai-on-gke/grain,gke-labs/granule --expires 30d
   ```
   Grain answers `whoami` and reports that it is behind IAP and accepts `gma_` tokens. The CLI authenticates the issuing call with the engineer's own IAP identity, which it gets from `gcloud`. The daemon mints `gma_…` named `jane-jetski-workstation` with `owner: jane@…`, and the CLI stores it in the keyring. If the engineer's `gcloud` login is missing, login stops with `run: gcloud auth login`; it never opens a browser redirect it can't finish.
3. **Verify.**
   ```sh
   grain whoami
   # context team  https://grain.team.example.com  as external:jane-jetski-workstation (owner jane@…)
   # scopes grain/external-dev  repos ai-on-gke/grain gke-labs/granule  expires 2026-11-28
   ```
4. **Teach Jetski.**
   ```sh
   grain skill install --jetski          # ~/.gemini/jetski/skills/grain/SKILL.md
   ```
   From now on, when the engineer mentions grain, Jetski loads the skill and knows the verbs, the repos it can reach, and that it must export `GRAIN_CLIENT=jetski/<ver>`.

When the token expires, every call fails with exit `3` and the message `token for context team expired on …: run grain login --refresh`.

### CUJ 2: Design and spec flow

The engineer says: "Let's write a design for per-repo CI quotas, then let grain spec and build it."

1. **Draft locally.** Jetski and the engineer write `design/design/ci-quotas.design.md` in a local checkout, in the same `#` / `##` format as the other docs there. Jetski pushes a branch and opens the PR with the engineer's own `gh` or `git` login, so the engineer is the PR's author. The PR is a human-authored design PR in the sense of `contributions.design.md`, and its contributors are the engineer and the reviewers whose comments change it.
2. **Bring grain in.**
   ```sh
   grain pr review https://github.com/ai-on-gke/grain/pull/150 --design
   ```
   This files a review task against the design PR, the same as commenting `@grain review` on the PR. `--design` tells the reviewer to check the doc against the design-folder conventions and against existing designs it overlaps. From then on, grain listens on the PR as `attribution.design.md` requires: a reviewer's comment addressed to grain becomes a commit on the PR with a `Co-authored-by:` for the commenter.
3. **The design merges, and grain files the spec task.** Jetski watches for it:
   ```sh
   grain list --repo ai-on-gke/grain --filter derived_from:eq:ai-on-gke/grain#150 --json
   grain wait 1812 --until=awaiting-reply,closed      # the spec task
   ```
   The `derived_from` filter reads the `Derived from:` link that `attribution.design.md` adds; until that lands, Jetski finds the spec task by the design PR's URL in its body.
   When the spec task opens its PR (`design/spec/ci-quotas.spec.md`, with `Derived from: …#150`), `grain get 1812 --json` shows the PR URL. Jetski fetches the branch and reviews the spec with the engineer locally. Edits go back one of two ways. The engineer leaves review comments on the PR, and grain's spec agent addresses them and replies. Or Jetski pushes a commit to the spec branch directly, which a design-folder PR allows; the human who made that edit is credited as `editor`.
4. **Tasks.** After the spec merges, grain opens the tasks PR (`design/tasks/ci-quotas/*.task.grain.md`). Jetski reads the proposed grants with `grain fs cat` on the PR's files, or just from the checkout, and the engineer audits them. Narrowing a grant is a one-line edit pushed to the PR.
5. **Implementation.** Once the tasks PR merges, grain imports the tasks. Jetski can watch them:
   ```sh
   grain list --filter derived_from:eq:ai-on-gke/grain#163 --json   # the imported tasks
   grain wait 1830 1831 1832 --until=merged,awaiting-reply --timeout=4h
   ```
   If a task raises a clarification PR against the design folder, `wait` returns with that task `awaiting-reply` and the clarification PR's URL. The engineer reviews it like any design PR. When everything has merged, `wait` prints the merged PRs, and Jetski runs `git pull`.

### CUJ 3: Authoring a code PR in Jetski and having grain adopt it

The engineer says: "I've fixed the quota off-by-one myself. Get grain to check it against the spec and land it."

1. **Write and push.** Jetski writes the fix and its test in the local checkout and pushes a branch: on the repo itself if the engineer has write access, or on a fork. It opens the PR with the engineer's own login. The PR description says `Implements: design/spec/ci-quotas.spec.md` (or `Design: design/design/ci-quotas.design.md`).
2. **Adopt.**
   ```sh
   grain pr adopt https://github.com/ai-on-gke/grain/pull/171 \
       --spec design/spec/ci-quotas.spec.md --mode review-only
   ```
   `grain pr adopt` is the CLI form of `@grain take over`, through a new `POST /api/pull-requests/adopt` under `merge:write` (plus `tasks:write`). It files the same shepherd task `takeover.go` files, with these additions:
   - `--spec` / `--design` (or the PR description's `Implements:` line) links the shepherd and its reviews to those documents. The code review is asked to check conformance to them as well as correctness: "does this do what §Quota accounting says, and does the test cover the case the spec names?" The PR's chain for `attribution.design.md` purposes becomes that design/spec/tasks chain.
   - `--mode review-only` keeps the engineer as the person who addresses findings. The shepherd runs CI and the reviews and holds the merge, but does not push fixes. `--mode finish` lets the shepherd fix findings and red CI itself, building on the engineer's commits and never force-pushing, as takeover already requires.
   - Because an external agent filed it, the shepherd starts **proposed** unless the filing gate trusts the owner. Auto-merge follows takeover's existing rule: on for a trusted author, off otherwise.
3. **Grain reviews and tests.** The daemon files the code review and, for a trust-boundary path or a non-collaborator author, the security review. It runs grain CI on the head and holds the queue (`takeover-cleared`) until every review for the current head has passed. Findings are posted as a GitHub review with a `grain/review` check.
4. **Results come back to Jetski.**
   ```sh
   grain wait 1840 --until=ci-done,awaiting-reply
   grain pr status https://github.com/ai-on-gke/grain/pull/171 --json
   # {"head": "ab12…", "ci": {"run": "ci-2210", "conclusion": "failure", "failing": ["go"]},
   #  "reviews": [{"kind": "code", "head": "ab12…", "state": "findings", "threads": 2}], "mergeQueue": "held: review"}
   ```
   Jetski reads the review threads (`grain pr status … --threads`), fixes them locally, and pushes. The push moves the head, so grain files fresh reviews and re-runs CI. Jetski replies on the threads with the engineer's own `gh` login, so the reply is the person's. To hand the rest over instead: `grain pr adopt …/171 --mode finish`, which updates the existing shepherd rather than filing a second one.
5. **Merge.** Once CI is green and every review for the head has passed, the shepherd submits the PR to the merge queue. If auto-merge is off, it waits in the Inbox for a person to approve the merge; the external agent cannot do that step, by design. `grain wait 1840 --until=merged` returns the merge commit, whose trailers credit the design chain's contributors.

### CUJ 4: Fanning out parallel tasks from a local session

The engineer is partway through a change and says: "Hand off the granule API bump, the CLI flag, and the docs, but the docs have to wait for the other two."

1. **File.**
   ```sh
   export GRAIN_CLIENT="jetski/1.4 session=$JETSKI_SESSION_ID"
   a=$(grain create --repo gke-labs/granule --title "Expose sandbox quota in granule API" \
         --body-file - --position front --json < a.md | jq -r .id)
   b=$(grain create --repo ai-on-gke/grain --title "Add -ci-quota flag to grain repo" \
         --body-file - --depends-on "$a" --json < b.md | jq -r .id)
   c=$(grain create --repo ai-on-gke/grain --title "Document per-repo CI quotas" \
         --body-file - --depends-on "$a" --depends-on "$b" --json < c.md | jq -r .id)
   ```
   Alongside `--position front|end` (which places `order_key` within a priority level), `grain create -priority <N>` sets numeric task priority (`1` or more, default `5`; higher numbers run first). Grain records each task with `Author` set to the engineer and `filed_via` set to the Jetski session. Under the filing gate they land proposed or queued. `b` and `c` wait on their dependencies the way any `depends_on` does.
2. **Keep working, check in asynchronously.** Jetski starts a background shell, or just checks between turns:
   ```sh
   grain wait $a $b $c --until=merged,awaiting-reply --timeout=2h --json
   ```
3. **Answer a question.** `wait` returns early:
   ```json
   {"id": "1851", "state": "awaiting-reply", "question": {"summary": "Quota per repo or per repo+branch?", "body": "…"}}
   ```
   Jetski shows the engineer the question, and the engineer answers:
   ```sh
   grain reply 1851 "Per repo. Branches share the repo's quota."
   ```
   That puts the task back in the queue with the answer in its conversation, signed as the engineer via Jetski. Jetski calls `wait` again on the tasks still open.
4. **Pull the results.** When all three have merged, `wait` prints their merge commits. Jetski runs `git pull` in both checkouts and rebases the engineer's in-progress branch onto the result.

### CUJ 5: Debugging CI, merge-queue stalls and inbox alerts

The engineer asks: "Why did my PR fail CI?", or "What's in the grain inbox?"

1. **Triage.**
   ```sh
   grain inbox --json
   # [{"kind": "question", "task": "1851", …},
   #  {"kind": "notification", "severity": "warning", "subject": "flaky-e2e", …},
   #  {"kind": "merge-held", "task": "1840", "reason": "review"}]
   grain ci list --ref grain/task-1840 --conclusion failure --limit 1 --json
   grain ci show ci-2210 --json            # jobs, steps, failing attempt, shared faults
   ```
2. **Get the failing test's own log.**
   ```sh
   grain fs grep 'FAIL|panic' ci://ai-on-gke/grain/ci-2210/go/gotest.log -n
   grain fs cat  ci://ai-on-gke/grain/ci-2210/go/attempts/1/tests/pkg/ci/TestQuotaOffByOne.log
   grain ci test-history TestQuotaOffByOne --limit 30
   ```
   `ci show` already reports when the same job is failing the same way on other branches, as `ci faults` does. The skill tells Jetski to check that, and the test history, before blaming the change.
3. **Act.** What Jetski does depends on what it found:
   - **It's the engineer's bug.** Fix it locally, push, then `grain ci wait --pr …/171`.
   - **It's a known flake or an infrastructure fault.** `grain ci retrigger ci-2210` (new outside the sandbox, the same as the in-sandbox `grain ci retrigger`) reruns the failed jobs once, uncached, which `ci:write` allows.
   - **It's a merge-queue stall.** `grain merge-queue --repo ai-on-gke/grain` shows the held gate and why. If the PR was given up on, `grain retry-merge 1840` puts it back. `force-merge` stays a person's action, and the skill says so.
   - **It needs real repair work.**
     ```sh
     grain create --repo ai-on-gke/grain --title "Fix flaky TestQuotaOffByOne under -race" \
         --body "Seen in ci-2210 attempt 1 and 4 of the last 30 runs: …" --json
     ```
     The new task carries the CI run id in its body and is attributed to the engineer.
   - **It's a question only a person can answer** (an inbox notification asking for a secret, say). Jetski tells the engineer and links the root-relative UI route, `/tasks/<id>` or `/settings/...`. It doesn't try to answer.

## Security notes

- The external surface adds no new power. Every new route sits under an existing scope and goes through `agentAPIGate`, so the totality test (`TestRouteScopesAreTotal`) covers it. `POST /api/pull-requests/adopt` is `merge:write`, `GET /api/tasks/wait` is `tasks:read`, and `/api/fs/*` is per-scheme as above.
- The access code is never written to the keyring by default; `grain login` steers single-tenant users to a scoped `gma_` token. The skill file never contains a credential, only the context name.
- `X-Grain-Client` and `-as` are labels, not identity. The owner comes from the token's own record.
- The external-agent rails (no self-grant, no auto-merge, no unreviewed merge, rate limits, `revoke-all`) apply to everything the CLI, skill and `grain mcp` do, because all three are the same HTTP calls.

## Open questions

1. Should `grain login --issue-token` be allowed for a human who is not an admin? This design says yes for non-dangerous scope sets limited to repos that person can already reach, so engineers don't need an operator for every new laptop. The alternative is an operator-only issuing step with a self-service request.
2. Should external-agent filings default to **proposed**, as here, or to **queued** for owners who are already collaborators on the target repo? Proposed is safer. Queued is closer to "effortless".
3. Should `grain pr adopt --mode review-only` be the default? Here it is, because it keeps the engineer's PR the engineer's. Takeover today means finish.

## Follow-up work

1. Contexts, `grain login|logout|whoami|context`, keyring and `0600` storage, `GET /api/auth/whoami`, the credential-source hook, and the fail-fast handling of login redirects.
2. Consistent `--json` and exit codes on every verb, and `--json` accepted after the verb.
3. `GET /api/tasks/wait` and `grain wait` (`closed|merged|awaiting-reply|ci-done|any`); `grain reply`, `grain inbox`, `grain logs`.
4. Mount the namespace server at `/api/fs/*` with per-scheme scopes, and share `grain fs` code with the in-sandbox `ls|read|grep`.
5. `grain ci wait`, `ci retrigger`, `ci test-history` and `merge-queue` outside the sandbox.
6. `grain pr adopt|review|status`, `POST /api/pull-requests/adopt`, spec/design-linked conformance reviews, and the `review-only` / `finish` modes on takeover.
7. Token `owner`, task `filed_via` from `X-Grain-Client`, and the `grain/external-dev` and `grain/external-read` scope sets.
8. `grain skill install|status` for Jetski, Claude Code, Codex CLI and Gemini CLI, and `grain mcp`.
9. A Settings → CLI page with the install line and context snippet, and `/cli/install.sh`.
