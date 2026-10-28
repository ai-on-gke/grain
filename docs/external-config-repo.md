# Changing configuration managed in an external config repository

A deployment whose configuration repository has a remote (Settings →
Config Repo → "Use external config repository") treats that repository as
the source of truth: its settings, templates, suites, schedules, boards
and per-repo configuration are edited by pull request there, and grain
applies what merges on its next sync. Grain's own API does not take
configuration writes in this mode.

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
  "docs": "https://github.com/bwsalmon/grain/blob/main/docs/external-config-repo.md",
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
whatever else is wrong with it. Fix the request in internal mode, or
write the edit by hand from `description`. (A route that looks up what
it was asked to change before it reaches the guard -- deleting a console
session that does not exist, say -- still answers `404`.)

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
