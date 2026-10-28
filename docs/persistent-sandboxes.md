# Persistent sandboxes

A persistent sandbox (`model.PersistentSandbox`) is a staging environment
or long-lived service grain keeps running. It is a repository checked out
at a ref inside a sandbox from the deployment's own backend, with one
command started in that checkout. It can be reached on a port of the grain
host (`host_port`) or under the grain UI at `/staging/<name>/`
(`ui_subpath`). Definitions live in grain/config as
`tables/persistent_sandbox.json`. The orchestrator's
`PersistentSandboxSupervisor` runs them
(`pkg/orchestrator/persistent_sandboxes.go`), and System → Sandboxes lists
them with Restart, Stop/Start and Rebuild controls.

An app served under `/staging/<name>/` shares the grain UI's origin, so
the proxy (`handleStagingProxy`, `pkg/ui/persistent_sandboxes.go`) keeps
the two apart. It does not forward grain's `grain_*` cookies or the
`Authorization` header to the app. It serves every response with a
`Content-Security-Policy: sandbox` that gives the page an opaque origin.
It also drops the app's `Clear-Site-Data` and any `Set-Cookie` for a
`grain_*` name. Other cookies the app sets lose their `Domain` and are
confined to `/staging/<name>`. When the prefix is stripped, a cookie's
`Path` is read relative to the prefix, so `Path=/app` becomes
`/staging/<name>/app`.

The supervisor restarts the command whenever it exits or its sandbox dies,
backing off from 1s up to 5m on a crash loop. With `auto_update: true`,
each restart fetches `ref` again first.

## Refresh: following the branch

`auto_update` alone only fetches when something else restarts the
command. `refresh` makes the supervisor restart it when the ref moves:

| `refresh` | What it does |
|---|---|
| `manual` (or unset) | Never restarts by itself. This is the default. |
| `on_push` | Once the ref's head has moved and then stayed put for 2 minutes, restarts onto it. A burst of pushes is one restart. |
| `interval:<duration>` | Reads the head every `<duration>` (at least `1m`, e.g. `interval:1h`) and restarts only if it moved since the last check. |
| `after_green` | Restarts onto a new head only once grain CI has a passing verdict on that exact commit. A red, errored or cancelled head is skipped, and the sandbox stays where it is. |

Every policy except `manual` needs `auto_update: true`. Validation
rejects a definition without it rather than turning it on quietly.
`after_green` also needs `ref` to name a branch, because that branch's CI
verdicts are what it follows.

### How the head is read

Each refreshing sandbox reads its ref's head at most once a minute. The
read goes to the forge first: the same branch read the base-branch CI
judge uses to notice that a branch has moved. For a ref the forge can't
answer for, such as a tag or a deployment with no forge client, the
supervisor falls back to `git ls-remote` against the sandbox's own remote.
A ref that is a full commit sha never moves.

The supervisor never restarts a sandbox for a refresh unless it is
running. While a build or the checkout from a previous refresh is still
in progress, it waits for the next look.

### What "green" means for `after_green`

The verdict is grain CI's push run of the repository's own
`.grain/workflows` on that branch, at that commit. This is the run the
base-branch CI judge records in `model.BaseBranchCI`. The supervisor
reads the judge's row when the row is about that commit. Otherwise it
uses the most recent finished push run on that branch at that commit. A
run that was interrupted is no verdict, and the sandbox keeps waiting.
The judge only watches branches grain merges into, or those with
`watch_default_branch` set, so an `after_green` sandbox on any other
branch waits forever. Its events say so.

A refresh checks out the green commit by its sha, not `FETCH_HEAD`. That
way, a push that lands between the verdict and the fetch can't bring an
unverified commit along. The sandbox then stays pinned to that commit, so
a crash restart or an operator's Restart also stays on the last green
build instead of fetching past it. Rebuild does the same.

When an `after_green` sandbox first starts after the daemon does, it
starts on the ref's head if that head is already green. Otherwise it
takes the tip as a manual sandbox would, and the next green head becomes
its first refresh.

### Crash loops and `rollback_on_crash`

Sometimes a refreshed commit's command exits 3 times in a row without
staying up. The supervisor reports this in the sandbox's last error on
the panel, as a `refresh-crash` event, and in the daemon log.

With `rollback_on_crash: true` the supervisor also puts the sandbox back
on the commit it ran before the refresh, and never refreshes onto the
broken commit again. Rollback is only accepted with `after_green`.
That's the one policy whose previous commit is known to have passed CI.
Under `on_push`, the previous commit is just older, and rolling back to
it is no safer than staying.

### What the panel shows

Each sandbox's row shows the commit it is running and its refresh policy.
When the sandbox has refreshed itself, the row also says when and why,
e.g. "refreshed 2h ago: candidate moved; CI green at 1a2b3c4d5e6f".
Every refresh is recorded as a `refresh` event with the from-sha, the
to-sha and the reason. Skipped heads (`refresh-skip`), heads still
waiting for a verdict (`refresh-wait`), crash loops and rollbacks are
recorded as events too.

## Example: `grain-staging`

```json
{
  "name": "grain-staging",
  "repo": "bwsalmon/grain",
  "ref": "candidate",
  "auto_update": true,
  "refresh": "after_green",
  "rollback_on_crash": true,
  "command": "go run ./cmd/grain demo -addr 127.0.0.1:4280",
  "sandbox_port": 4280,
  "host_port": 4280,
  "enabled": true
}
```

This sandbox moves to each new `candidate` commit once grain CI passes on
it, and stays on the last green build otherwise.
