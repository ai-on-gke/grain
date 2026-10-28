# GCP keys (`gcp-key:<name>`)

Plain `gcp-key` mints a short-lived key for one of the deployment's own
agent service accounts (Settings -> Capabilities). Work that needs GCP
access somewhere else -- another project, other accounts, a minter of its
own -- is given a **named GCP key** instead: a row that adds capability
`gcp-key:<name>` and mints for its own project and service accounts,
through its own minter secret.

Named keys were a team's until single-tenant reset stage 4e
(docs/single-tenant-reset.md). They are now the deployment's: there is one
key of each name, every task and CI job that is granted `gcp-key:<name>`
mints with it whatever team the task carries, and only a system admin
configures them. The key mechanism -- pools, minters, leases, the reaper
-- is unchanged.

This page covers what a row holds, how the capability it adds is
resolved, the GCP and secret setup it needs, and the ways to configure
one. The code is `pkg/model/gcp_key.go`, `pkg/model/gcp_key_store.go`,
`pkg/capability/gcpkey/key_provider.go` and
`pkg/capability/gcpkey/gcpkey.go`.

## The `gcp_key` table

The keys are stored in `gcp_key`, one row per key. Until schema 23 they
were `team_gcp_key`'s rows under the `default` team's id; a state
repository dump from before then is carried across by the dump fixup,
and one holding another team's key is refused
(docs/single-tenant-reset.md, 6-ii-b3).

| Column | Meaning |
|---|---|
| `name` | The key's name, and the primary key. A name may not contain whitespace, `/`, `\` or `:`, and may not be `default` (below). |
| `project_id` | The GCP project the agent service accounts live in. Required. |
| `service_account_email` | The agent account keys are minted for, or the first account of a pool (see below). Required. |
| `service_account_count` | How many agent accounts are in the pool. `1` (the default) means `service_account_email` alone. |
| `minter_credential` | The name of the secret holding the minter's key file (see below). |
| `max_key_age_seconds` | How long a minted key's lease lasts. `0` means the default, 24 hours. |
| `description` | Free text. |
| `created_at`, `updated_at` | Timestamps. |

The table is one of the state repository's settings tables, so a
deployment's grain/config exports it as `tables/gcp_key.json`.

## What a row adds

A row named `<name>` adds the capability `gcp-key:<name>`. It is looked up
by name, for the deployment, at every step -- resolve, mint, the prompt
section, revoke, the hourly reap and the credential check
(`gcpkey.KeyProvider`) -- and never by the task's team, which is inert
metadata since the reset. A task asking for a name the deployment has no
key for is refused when it is filed (`model.MissingGCPKeyRefusal`), or,
when the grant was only a deployment or repository default, filed
without it. A `.grain` declaration naming one is dropped and explained on
the task thread.

A row does **not** change plain `gcp-key`. That capability still mints
from the deployment pool configured under Settings -> Capabilities
(`grain settings -gcp-project … -gcp-agent-service-account …`), through
the deployment's `gcp-key-minter` secret. A task or CI job has to ask for
`gcp-key:<name>` by name.

You grant `gcp-key:<name>` the same way as any other capability: on the
task, in a template or schedule's `capabilities:` list
([declarations](declarations.md)), or in a repository's CI capabilities
([CI capabilities](ci-capabilities.md)), for example
`grain repo ci-capabilities -set gcp-key:deploy acme/app`.

### Where the key lands

The minted key is written to `/home/debian/.gcp-service-account-<name>.json`
(`gcpkey.KeyPath`), and plain `gcp-key`'s to
`/home/debian/.gcp-service-account.json` (`gcpkey.SandboxKeyPath`), so a
task or job granted more than one GCP key gets every one of them in a file
of its own. The sandbox prompt names each path and its key's `project_id`.

Do not name a key `default`. `model.GCPKeyCapability("default")` is plain
`gcp-key`, so a key named `default` would never get a capability of its
own; saving one is refused.

## Service account pools

When `service_account_count` is greater than 1, the email is a template
for a numbered pool rather than one account. The local part loses a
trailing `-0` and any trailing `-`, and the pool is that prefix plus
`-0` to `-<N-1>`:

```
service_account_email: grain-agent-0@my-proj.iam.gserviceaccount.com
service_account_count: 3
  -> grain-agent-0@my-proj.iam.gserviceaccount.com
     grain-agent-1@my-proj.iam.gserviceaccount.com
     grain-agent-2@my-proj.iam.gserviceaccount.com
```

`grain-agent@…` and `grain-agent-0@…` name the same pool. If the email
has no `@`, the domain is `<project_id>.iam.gserviceaccount.com`. Each
mint tries the accounts round-robin. An account that already holds GCP's
maximum of 10 user-managed keys is skipped for the next one, and the mint
fails only when every account in the pool is full. Two runs of the same
task can therefore get different accounts. Every account in the pool
needs the same IAM roles for the work, and the minter needs its role on
each of them (below).

## The minter secret

A key is minted by a separate **minter** service account, never by the
agent account itself. Otherwise a sandbox holding a leaked agent key could
mint itself new ones. The minter's JSON key file is kept in grain's
secret store, under the name in `minter_credential`.

- **Default name.** A key saved with no minter gets
  `gcp-key-minter-<name>` (`model.GCPKeyMinterCredential`), which is also
  what a row with an empty `minter_credential` mints through
  (`EffectiveMinterCredential`). Editing an existing key without naming a
  minter keeps the minter it already has. Keys saved while GCP keys were a
  team's kept the name they were given then,
  `gcp-key-minter-<team>-<name>`.
- **Only a system admin can store it**, through
  `PUT /api/secrets/{secret}/{key}`, Settings -> Secrets or
  `grain secrets set`.
- A system admin may point a key at any minter, including the
  deployment's own `gcp-key-minter`. That gives every holder of
  `gcp-key:<name>` whatever that minter can mint, so do it on purpose.

For example, to set up the `deploy` key's minter:

```sh
curl -X PUT "$GRAIN/api/secrets/gcp-key-minter-deploy/key.json" \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --rawfile v minter.json '{value: $v}')"
```

## GCP setup

In the key's project:

1. Create the agent service account, or the numbered pool of them, and
   grant them only what the work that holds the key needs.
2. Create the minter service account and download a JSON key for it.
   That file is the secret value above.
3. Grant the minter `roles/iam.serviceAccountKeyAdmin` **on each agent
   account**, not on the project:

   ```sh
   for i in 0 1 2; do
     gcloud iam service-accounts add-iam-policy-binding \
       "grain-agent-$i@my-proj.iam.gserviceaccount.com" \
       --member="serviceAccount:grain-minter@my-proj.iam.gserviceaccount.com" \
       --role=roles/iam.serviceAccountKeyAdmin
   done
   ```

   That role covers creating, listing and deleting keys, which covers
   mint, revoke, the hourly reap and the credential check.
4. Make sure organization policy does not block key creation on those
   accounts. `constraints/iam.disableServiceAccountKeyCreation` (or its
   managed form, `iam.managed.disableServiceAccountKeyCreation`) is on by
   default in many organizations. When it applies, GCP refuses the mint
   and grain's error names the constraint. Exempt the project, or the
   accounts, from it.

Grain enforces each key's lifetime itself, because user-managed keys have
no TTL in GCP: a lease expires after `max_key_age_seconds`, and the reaper
deletes keys older than the oldest live run.

## Configuring a key

There are three ways to configure a key, and all three write the same
row. There is no UI pane for GCP keys yet.

### CLI: `grain gcp-key`

```sh
grain gcp-key set deploy \
  --project my-proj \
  --account grain-agent-0@my-proj.iam.gserviceaccount.com \
  --count 3 --max-age 12h
# --minter <secret> to name a minter other than gcp-key-minter-deploy

grain gcp-key list
grain gcp-key get deploy
grain gcp-key delete deploy
```

`set` prints the capability the key adds (`gcp-key:deploy`). It talks to
`/api/gcp-keys` (below), so it needs a system admin's credentials.

### API: `/api/gcp-keys`

| Route | Who | |
|---|---|---|
| `GET /api/gcp-keys` | any member | list the keys |
| `GET /api/gcp-keys/{name}` | system admin | one key |
| `POST /api/gcp-keys` | system admin | create or update; the name is in the body |
| `PUT /api/gcp-keys/{name}` | system admin | the same |
| `DELETE /api/gcp-keys/{name}` | system admin | delete |

A row names a project, accounts and a minter secret's name, never key
material, so the list answers anyone signed in.

The body is `ui.SetGCPKeyRequest`:

```json
{
  "name": "deploy",
  "projectId": "my-proj",
  "serviceAccountEmail": "grain-agent-0@my-proj.iam.gserviceaccount.com",
  "serviceAccountCount": 3,
  "maxKeyAge": "12h",
  "minterCredential": "gcp-key-minter-deploy",
  "description": "Deploys to staging"
}
```

`maxKeyAgeHours` (an integer) is accepted instead of `maxKeyAge`. A save
that leaves `description` out keeps the key's description; an empty
string clears it. The old `/api/teams/{id}/gcp-keys` routes are gone.

### Config repositories

The deployment's grain/config, `tables/gcp_key.json`. Whoever can merge
to that repository is already the operator, so these rows are imported
as they are, with no minter check. Columns are as in the table above.

Until schema 23 the `default` team's config repository could also carry
the keys as `gcp_keys.yaml`. The daemon opens a configuration
repository only for a team besides the `default` one, and a team
configuration repository no longer exports or applies the file.
