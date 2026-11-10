# Changing configuration managed in an external config repository

A deployment whose configuration repository has a remote (Settings →
Config Repo → "Use external config repository") treats that repository as
the source of truth: its settings, templates, suites, schedules, boards
and per-repo configuration are edited by pull request there, and grain
applies what merges on its next sync. Grain's own API does not take
configuration writes in this mode.

## Who this makes an administrator

**Anybody who can push to the external repository's branch is a full
administrator of the deployment.** Grain pulls the branch and imports
whatever is there -- capability servers in `mcp_servers` (host scripts
among them), scope sets, capability policy, default capabilities,
secret bindings, templates and schedules with their grants. A forge-side
merge, or a direct push, never passes through anybody's Submit in grain,
so none of grain's own approval gates apply to it. Treat write access to
that branch exactly as you treat an unlimited role in grain:

- protect the branch on the forge (required reviews, no direct pushes,
  no force pushes), and keep its writers to the people you would give an
  unlimited role;
- or turn on `require_verified` mode (below), so grain itself refuses
  to apply a commit it cannot vouch for.

Naming or repointing the repository (`POST /api/state-repo/remote`,
Settings → Config Repo) needs an unlimited role for the same reason
(task 3965). That is a person's role: an agent's run reaches the same
route with an `api:settings:write` grant, so treat that grant as an
administrator's too.

## Require verified commits (`require_verified`)

In this mode grain applies a commit from the external branch only if it
can verify it, in one of these ways:

- **an approved pull request**: the commit is the merge (or squash) commit
  of a pull request into the branch, and at least *required approvals*
  reviewers other than its author approved the pull request's final head
  commit (their latest review, on that commit). Only reviewers GitHub
  names as the repository's owner, a member of its organization or a
  collaborator on it count, as in branch protection -- anybody may approve
  a pull request on a public repository. Read from GitHub, so this
  needs a `https://github.com/...` remote and a credential for it;
- **merged by grain**: that pull request was merged by one of *grain's
  forge accounts* -- the accounts grain's merge queue merges as, which a
  person's Submit in grain releases;
- **a signature**: the commit carries a good SSH signature by a key in
  the *allowed signing keys* (an ssh `allowed_signers` file,
  `gpg.ssh.allowedSignersFile`). OpenPGP and X.509 signatures never
  verify, whatever keyring this host holds;
- **accepted by an operator**: an unlimited role accepted the commit by
  its full SHA (`acceptCommit`).

Grain walks the branch's first-parent history from the last commit it
applied and moves forward across commits that verify. The first one that
does not is **held**: neither it nor anything after it is applied, the
journal logs it once, and Settings → Config Repo shows a notice naming
the commit, its subject and why it did not verify, with an **Accept**
button. A branch whose history was rewritten (the last applied commit is
no longer on it) is held the same way, whatever its tip is, until its
tip is accepted by name. The first
commit grain applies from a newly named repository is checked on its own;
if it does not verify, nothing is imported until it is accepted. A
rebase-merged pull request does not verify through its review (only its
last commit is the pull request's merge commit); sign such commits, or
use merge or squash merges.

The mode's settings live in `<data-dir>/state-repo.json` on this host,
never in the repository they guard -- a writer there must not be able to
turn them off -- and are set through the same route as the remote, so
they need an unlimited role (or, for an agent, `api:settings:write`, as
above):

```json
POST /api/state-repo/remote
{
  "remote": "https://github.com/acme/grain-config.git",
  "branch": "main",
  "requireVerified": true,
  "requiredApprovals": 2,
  "verifiedMergers": "acme-grain[bot]",
  "allowedSigners": "alice@example.com ssh-ed25519 AAAA...",
  "acceptCommit": "<a held commit's full SHA>"
}
```

Any field left out keeps its value. `GET /api/state-repo` reports the
mode and the held commit under `verification`.

## What a refused write says

Every configuration-writing route answers `409 Conflict` with a body an
agent or the UI can act on:

```json
{
  "code": "config_managed_externally",
  "repo": "acme/grain-config",
  "branch": "main",
  "url": "https://github.com/acme/grain-config",
  "path": ["settings/schedules.yml"],
  "change": [
    {
      "path": "settings/schedules.yml",
      "op": "modify",
      "patch": "diff --git a/settings/schedules.yml b/settings/schedules.yml\n...",
      "fragment": "    title: weekly\n"
    }
  ],
  "access": {"canWrite": false, "need": "a writes: grant on acme/grain-config (base main)"},
  "docs": "https://github.com/ai-on-gke/grain/blob/main/docs/external-config-repo.md",
  "error": "settings are read-only because ... Commit this change to settings/schedules.yml instead: ..."
}
```

- `path` is the file or files in the config repository holding the
  setting.
- `change` is the edit, one unified-diff patch per file, applicable with
  `git apply` to the repository as grain exported it. It is computed by
  running the write inside a transaction, exporting the configuration on
  both sides of it with the same code that writes the repository, and
  rolling the transaction back -- so it is exactly what the write would
  have done, in exactly the format the sync reads. `fragment` is the text
  the patch writes in.
- `access` is present for an agent: whether its task may land work in
  the config repository (its own repository, or a `writes:` grant), and
  if not, which grant it would need.
- `error` is the same thing as one instruction, for anything that only
  relays the message (an MCP tool result).

A request that changes nothing, or that would have failed anyway -- a
field that does not validate, a repository or schedule that does not
exist -- is still answered `409` with the same `code`, `path` and
`description`, but with an empty `change`: there is no edit to commit
for it, and external mode refuses every configuration write with `409`
whatever else is wrong with it. Fix the request and send it again, or
write the edit by hand from `description`. (A route that looks up what
it was asked to change before it reaches the guard -- deleting a console
session that does not exist, say -- still answers `404`.)

Internal mode answers the same way for every setting outside Models and
Sandboxes: the refusal names `grain/config` (`"internal": true`), and
`POST /api/state-repo/pull-requests` with `{"changes": <its change>}`
opens a validated pull request there, held for a person to submit
(`design/design/config-repo-settings.design.md`, section 4.1.1).

## Trying it

`grain demo -external-config-repo https://github.com/acme/grain-config.git`
serves the demo in this mode (`-external-config-branch` names a branch
other than `main`): Settings shows the read-only banner, and a write the
UI still offers -- adding a schedule, say -- is answered with the
refusal and its patch. The demo's seeded data is written before the
mode takes effect.

## Routes with no patch

These routes set a secret before their configuration write, so they are
refused before anything runs and say which file holds the setting
without a patch: GCP keys (`tables/gcp_key.json`), a capability server's
secret binding, a repository's release secrets
(`tables/repo_config.json`), and team git connections
(`tables/grain_config.json`). Secrets themselves are never kept in the
config repository; set them in the deployment's secret store.

Deleting a console session is refused the same way, before anything
runs (`tables/grain_config.json`, no patch): the route stops and closes
the session's task before its configuration write, and a refusal must
not leave the session's task cancelled behind it. Remove the session's
entry from `console_sessions` by hand.
