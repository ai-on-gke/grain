# How to give tasks GCP access with `gcp-key` permission sets

This is the operator's walk-through. What the capability does, and why, is in [capabilities.md → GCP (`gcp-key`)](capabilities.md#gcp-gcp-key); the keyless token architecture, host/admin identity resolution and security boundaries are in [`design/design/gcp-access.design.md`](../design/design/gcp-access.design.md), and the normative wire, placement and error contract is in [`design/spec/capabilities/capabilities.spec.md`](../design/spec/capabilities/capabilities.spec.md#61-leased-cloud-credential-gcp-key--mint_gcp-key--release_gcp-key) (§6.1).

## 1. Pick an admin account and where its credential comes from

An admin is the identity grain uses to create service accounts, bind their roles and mint their keys. Its credential has one of two sources:

- **configured** — a secret you give grain: a service-account key JSON, or a workload-identity external-account config. Use this when the `gcp` server runs in grain's container and the container's own identity should not be the admin.
- **ambient** — whatever identity the server already has where it runs. With `"run": "host"` (through `grain host`) that is your own `gcloud auth application-default login` identity on the host; in a container it is the container's ADC (`GOOGLE_APPLICATION_CREDENTIALS`) or the metadata server's service account.

Give the admin the custom role in [capabilities.md](capabilities.md#gcp-gcp-key) on every project it acts in:

```sh
gcloud iam roles create grainGcpKeyAdmin --project=PROJECT --file=grain-gcp-key-admin.yaml
gcloud projects add-iam-policy-binding PROJECT \
  --member=serviceAccount:ADMIN_EMAIL --role=projects/PROJECT/roles/grainGcpKeyAdmin
# a user admin (ambient on a host): --member=user:you@example.com
```

For a set scoped to a bucket, also grant the admin `roles/storage.admin` on that bucket, or a custom role holding `storage.buckets.getIamPolicy` and `storage.buckets.setIamPolicy`.

## 2. Add the admin and a permission set in Settings

Open Settings → Capabilities → Capability servers. The built-in **gcp** tab (`https://grain.dev/capabilities/gcp`) is already there, showing *needs configuring* until it has a permission set. Select it and press **Configure** on its card:

1. Under **Admin accounts**, add `{name, credential_source, projects, role_ceiling}`. `role_ceiling` is every role this admin may hand out — keep it short.
2. For a configured admin, paste into **Configured admin credentials** a JSON object from the admin's name to its credential: `{"minter": { …the key JSON… }}`. The field is write-only: grain stores it in its secrets store and never shows it again.
3. Under **Permission sets**, add `{name, admin, project, roles}`, and optionally `resource_scope` (`buckets/<name>`), `max_lease` (default `24h`), `pool_max` (default 3) and `adopt_legacy` (see [Accounts from before deployment ids](#accounts-from-before-deployment-ids)) — or, to use an account you already have, `{name, admin, service_account}` (see [Using an existing service account](#using-an-existing-service-account)). **Service account name prefix** (`account_prefix`, default `grain`) starts every account the sets create.
4. Press **Test**. The `gcp-key` method's description then says which deployment id the accounts are named after (`deployment 049f1e09 (instance …): new service accounts are named grain-049f1e09-<set>-<n>`), which identity each admin resolved to, and every permission it is missing on each project. Fix those before saving — an admin that cannot create service accounts will fail the first mint.
5. Save.

## 3. Grant a set to a task

In a task's or template's capability grants, choose **Grants → Add grant… → gcp server › gcp-key** (the grant is `https://grain.dev/capabilities/gcp#gcp-key`, the same on every deployment) and pick the **Permission set** from the dropdown. A template's grant, set included, is copied onto every task it files ([docs/schedules.md](schedules.md#grant-presets-capability-arguments-and-network-access) has a `settings/templates.yml` example). The first time a set is minted, grain creates `<prefix>-<deployment id>-<set>-1@<project>` — on one deployment, set `ci` in project P is `grain-049f1e09-ci-1@P` — and binds the set's roles; that can take a minute to take effect in GCP. The task's agent finds its key at `~/.gcp-service-account.json`, gcloud already logged in with the project selected.

A task that needs two sets holds two grants; set **primary** off on the second, whose key goes to `~/.gcp-keys/<set>.json`.

## 4. Watch usage and capacity

Test reports, for each set, how many accounts it has against `pool_max` and how many keys are held now. Each account takes ten keys at once (GCP's limit), so a set serves `10 × pool_max` concurrent attempts; past that a mint waits (`resource_exhausted`) until a key is released.

## 5. Remove a set

Delete the set and save. Its service accounts are **not** deleted: the next Test lists them as `removed set <name> still has <email>`. To delete them, add the set's name to **Delete removed sets' service accounts** (`delete_orphans`) and save; grain removes their project bindings and deletes them. Only accounts carrying this deployment's marker (below) are ever listed or deleted.

## Using an existing service account

A set can act as a service account you already have — one with organization-level grants, say — instead of accounts grain creates. Give the set **`service_account: <email>`** in place of `pool_max` (the two are exclusive, as are `adopt_legacy`, `accounts` and `resource_scope`):

```json
{"name": "org-deploy", "admin": "default", "service_account": "deployer@my-project.iam.gserviceaccount.com",
 "roles": ["roles/run.admin"], "max_lease": "8h"}
```

Then grain **never creates, changes or deletes that account, and never binds or removes a role** on it or on the project for it. `roles` is optional and only informational: Test reports any listed role the account lacks on the set's project, and changes nothing. The admin's `role_ceiling` does not apply, since grain grants nothing. **A task granted the set gets whatever the account may already do** — everything its own grants allow, wherever they were made — so you, configuring the set, are responsible for those permissions. The account must be in one of the admin's `projects`; the set's `project` (what gcloud selects) defaults to the account's own.

**`credential: token` (the default — use it).** Grain impersonates the account: no key ever exists. The admin needs `roles/iam.serviceAccountTokenCreator` on that one account, and nothing on the project:

```sh
gcloud iam service-accounts add-iam-policy-binding deployer@my-project.iam.gserviceaccount.com \
  --project=my-project \
  --member=serviceAccount:ADMIN_EMAIL --role=roles/iam.serviceAccountTokenCreator
# a user admin (ambient on a host): --member=user:you@example.com
```

The task's agent finds an `external_account` credential config at `~/.gcp-service-account.json` — not a key — with gcloud signed in with it and the project selected; ADC and the client libraries read it like any credential file. Whenever one of them needs an access token, it presents a per-attempt token (in `~/.gcp-token/<attempt_id>/<set>.token`) to the gcp server's data-plane listener, forwarded into the sandbox at `127.0.0.1:4252` (the server's `-listen` flag moves it), and gets a fresh access token the server minted with `generateAccessToken`, good until the grant's deadline and at most an hour. `~/.gcp-token/<attempt_id>/<set>-id-token AUDIENCE` prints an ID token for the account (the same role covers it). The per-attempt token works only on that listener, and only until the attempt is released or its deadline, which `max_lease` caps — nothing long-lived ever reaches the sandbox. An access token or ID token the server already handed out cannot be revoked, though: one minted shortly before an early release stays good until it expires, at most an hour later and never past the grant's deadline for access tokens (an ID token always lives its hour). There is no concurrency limit. See [`gcp-access.design.md`](../design/design/gcp-access.design.md) for the end-to-end architecture and [`capabilities.spec.md`](../design/spec/capabilities/capabilities.spec.md#61-leased-cloud-credential-gcp-key--mint_gcp-key--release_gcp-key) (§6.1.2) for the formal placement and loopback broker contract.

**`credential: key`.** Grain mints a key for the account per attempt and deletes it at release, as for its own accounts. The admin needs `iam.serviceAccountKeys.create` and `iam.serviceAccountKeys.delete` on the account (`roles/iam.serviceAccountKeyAdmin` on it). GCP allows ten keys per account, so at most ten attempts hold the set at once; an eleventh waits (`resource_exhausted`). The reaper never deletes keys on an existing account, since it cannot tell which are grain's: a key whose release never comes (the gcp server lost between mint and release) stays on the account until you delete it, which is one more reason to prefer `token`. Prefer `token` unless something in the task truly needs a key file.

**Who may grant it.** A set over an existing account is **person-granted** by default (`person_granted`, default on for these sets; off for every other): a person must give the grant, and an agent holding it cannot hand it on to a task or proposal it files — that waits for a person. We recommend leaving it on; turn it off only for an account whose permissions are narrow. The capability policy (Settings → Capabilities → Policy) applies as to any grant — use it to confine the set to the repositories that need it. Every grant and every token minted is logged with the attempt and the account (`gcp: audit: …` in the server's log).

**Test** reports, for each such set, the account, whether the admin can impersonate it (or, for `key`, mint keys for it), which listed `roles` it lacks — grants inherited from a folder or the organization are not seen there, so a role held that way shows as lacking — and how many attempts hold it now (no cap for tokens, ten for keys). An admin whose sets all use existing accounts is not asked for the `grainGcpKeyAdmin` role on their projects.

## Account names and ownership

A set's accounts are named `<prefix>-<deployment id>-<set>-<n>`. The deployment id is the one grain already puts in its branch names (`grain/<deployment>/task-<n>`): **Deployment ID** on System → Agents & Task Branches when a system admin has set one, otherwise the default drawn from the installation id. Grain sends it to the server itself, with the installation id, in the reserved configure fields `grainDeploymentId` and `grainInstanceId` ([`design/spec/capabilities/capabilities.spec.md`](../design/spec/capabilities/capabilities.spec.md) §4.5.7); they are not in the form and cannot be typed. A set that creates accounts is refused at configure if they are missing — the server must be attached through grain.

GCP allows 6–30 characters. When the full name is longer, the set's name is cut first, keeping its first characters and four hex digits of a hash of `<project>/<set>`; if even three characters of the set and its hash do not fit beside the deployment id, the deployment id is cut too, keeping four hex digits of its own hash: `grain-049f1e09-a-very-l-3f2a-1`. Names are deterministic. The set's hash covers the project as well as the name, so the same long set name in two projects looks different. A name some other account already has in the project is skipped for the next `n`, never shared.

Ownership does not rest on the name. Every account grain creates is described with the marker `[grain-gcp-key deployment=<deployment id> instance=<installation id prefix> set=<set>]`, and listing, minting, the reaper, Test and `delete_orphans` consider only accounts whose marker carries **this installation's** id. That has these effects:

- **Several deployments in one project** each create and use their own accounts, even with the same set names, and even if two admins chose the same deployment id. Another deployment's accounts are never leased, reaped, rebound or offered as orphans; Test reports only how many there are (`project P: N service account(s) of other grain deployments, left alone`).
- **Renaming the deployment id** in Settings renames the branches of new tasks, and re-attaches the `gcp` server with the new id. The set's existing accounts keep working — their marker carries the same installation id — and accounts created afterwards are named under the new id, so a set can hold `grain-old-ci-1` and `grain-new-ci-2`. Nothing is renamed or deleted. To drop old-named accounts, wait until Test shows no keys held, then delete them by hand (`gcloud iam service-accounts delete <email>`); the set creates new ones as it needs them.
- A **new installation** (a fresh database) is a different deployment even under the same deployment id: it does not inherit the old installation's accounts. They show up in Test as another deployment's; delete them by hand.

## Accounts from before deployment ids

Before this naming, a set's accounts were `grain-<set>-<n>` with the marker `[grain-gcp-key set=<set>]`, which says nothing about the deployment. They are used only by a set with **`adopt_legacy: true`**, and only when their name is exactly that old name; turn it on in the one deployment that created them. Such a set mints from its old accounts and its new `<prefix>-<deployment id>-<set>-<n>` ones alike, binds both, and reaps both; it creates only new-style ones. Without `adopt_legacy`, old accounts are left alone and Test counts them (`N service account(s) with the old marker … which no set here adopts`). Grain never renames or deletes an old account, and they are never orphans, even after their set is removed.

To retire them: with `adopt_legacy` on, lower nothing and let the set create new accounts as it fills; once Test shows no keys held on an old account (or after `max_lease`), delete it with `gcloud iam service-accounts delete grain-<set>-<n>@<project>.iam.gserviceaccount.com`, and turn `adopt_legacy` off when none are left.

## Migrating from the fixed pool

A deployment that configured `gcp` with `-project`, `-account grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com*10` and `GCP_MINTER_KEY` (`gcp_service_account_count: 10`) keeps working unchanged: on start the server migrates it to a configured admin `minter` holding the old minter key and a set `sandbox-agent` over exactly `grain-sb-agent-0` … `grain-sb-agent-9`. Grants with no arguments get `sandbox-agent`. Nothing about those accounts is modified, bound or reaped, and their names do not change with the deployment id: a migrated set's `accounts` are used as they are (migrated sets have `adopt_legacy` on).

To retire the fixed pool:

1. Look up the roles the pool accounts hold: `gcloud projects get-iam-policy grain-sandbox-dev --flatten=bindings --filter='bindings.members~grain-sb-agent' --format='value(bindings.role)' | sort -u`.
2. In Settings, add a permission set with those roles (call it `sandbox-agent`, first in the list, so argument-less grants keep getting it) and no `accounts`, and give its admin a `role_ceiling` covering them and the `grainGcpKeyAdmin` role (step 1). Test, then save. New mints now go to `grain-<deployment id>-sandbox-agent-<n>` (shortened with a hash when the deployment id is long).
3. Once no attempt holds an old key (Test shows 0 keys held for the old set, or wait out its `max_lease`), remove the old pool's bindings and accounts by hand — grain will not, since it did not create them:

   ```sh
   for n in $(seq 0 9); do
     gcloud iam service-accounts delete grain-sb-agent-$n@grain-sandbox-dev.iam.gserviceaccount.com
   done
   ```

   and drop `gcp_service_account_count`, `-account` and `GCP_MINTER_KEY` from the entry in grain/config.

## Retirement of the in-process `gcp-key` capability (migration 97)

Schema migration 97 retires the in-process `gcp-key` and `gcp-key:<name>` providers onto `https://grain.dev/capabilities/gcp#gcp-key` in a single release so the two never mint against the same service accounts or compete for GCP's 10-key-per-account quota:

- **Permission sets seeded automatically:** if `grain_config.gcp_project` and `grain_config.gcp_service_account_email` are configured and no `sandbox-agent` set exists yet on `https://grain.dev/capabilities/gcp`, migration 97 seeds a `sandbox-agent` set over that exact account pool (`gcp_service_account_count`, minting through the deployment's `gcp-key-minter` secret, `adopt_legacy: true`, `max_lease: "24h"`). Each `gcp_key` row behind `gcp-key:<name>` seeds a set `<name>` over its account pool with `key_path: "/home/debian/.gcp-service-account-<name>.json"` and its own admin and `max_lease`.
- **Stored grants rewritten:** bare `gcp-key` becomes `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"sandbox-agent","primary":true}` and `gcp-key:<name>` becomes `https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"<name>","primary":true}` across tasks, templates, schedules, suites, repository `default_capabilities` and `ci_capabilities`, console sessions, the capability catalogue, and local policy. Older state-repository exports carrying `gcp-key` are rewritten on import.
- **One-release alias:** `gcp-key` and `gcp-key:<name>` remain readable as aliases for one release (with a deprecation notice in Settings → Capabilities) and normalize to the corresponding `gcp#gcp-key` grants; the in-process provider itself is no longer registered.

## Removal of deprecated `account` and `activate` grant arguments (migrations 109 and 127)

Schema migrations 109 and 127 remove the deprecated `account` and `activate` arguments from `https://grain.dev/capabilities/gcp#gcp-key`, leaving only `permission_set` and `primary`:

- **Stored grants rewritten:** `account: <set>` becomes `permission_set: <set>` (`account: default` resolves to the first configured permission set at migration time, or `"default"` if no sets are configured), and `activate` is dropped across tasks, templates, schedules, repository `default_capabilities` and `ci_capabilities`, console sessions, persistent sandboxes, deployment `default_capabilities`, and the capability catalogue. When the named permission set does not exist on the configured `gcp` server (or `account: default` is encountered with no sets configured), the grant is still rewritten to `permission_set` (with `primary: true` and `activate` dropped; migration 127 rewrites any grants an earlier migration 109 left with `account`), logged in the migration log, and noted on its task, template, or schedule so admission fails clearly until the set is configured or replaced.
- **New grants and state-repo import:** after migration 109, new grants carrying `account` or `activate` are refused with a message directing callers to `permission_set`. State-repository (`grain/config`) imports accept the old forms for one release and rewrite them to `permission_set` on export.

## Running the live test

`go test ./pkg/capability/gcpsets -run TestLiveMintUseRelease -v` with `GRAIN_GCP_LIVE_PROJECT=grain-sandbox-dev`, `GRAIN_GCP_LIVE_BUCKET=<a test bucket>` and either `GRAIN_GCP_LIVE_ADMIN_KEY=<admin key file>` or ambient ADC. It runs as deployment `livetest` (account `grain-livetest-live-test-1`), mints a key for a set holding `roles/storage.objectViewer` on the bucket, lists the bucket with gcloud as that key, releases it and checks the key is gone. Without the variables it is skipped.
