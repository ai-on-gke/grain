# How to give tasks GCP access with `gcp-key` permission sets

This is the operator's walk-through. What the capability does, and why, is in [capabilities.md → GCP (`gcp-key`)](capabilities.md#gcp-gcp-key).

## 1. Pick an admin account and where its credential comes from

An admin is the identity grain uses to create service accounts, bind their roles and mint their keys. Its credential has one of two sources:

- **configured** — a secret you give grain: a service-account key JSON, or a workload-identity external-account config. Use this when the `gcp` server runs in grain's container and the container's own identity should not be the admin.
- **ambient** — whatever identity the server already has where it runs. With `"run": "host"` (through `grain hostd`) that is your own `gcloud auth application-default login` identity on the host; in a container it is the container's ADC (`GOOGLE_APPLICATION_CREDENTIALS`) or the metadata server's service account.

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
3. Under **Permission sets**, add `{name, admin, project, roles}`, and optionally `resource_scope` (`buckets/<name>`), `max_lease` (default `24h`), `pool_max` (default 3) and `adopt_legacy` (see [Accounts from before deployment ids](#accounts-from-before-deployment-ids)). **Service account name prefix** (`account_prefix`, default `grain`) starts every account the sets create.
4. Press **Test**. The `gcp-key` method's description then says which deployment id the accounts are named after (`deployment 049f1e09 (instance …): new service accounts are named grain-049f1e09-<set>-<n>`), which identity each admin resolved to, and every permission it is missing on each project. Fix those before saving — an admin that cannot create service accounts will fail the first mint.
5. Save.

## 3. Grant a set to a task

In a task's or template's capability grants, choose **Grants → Add grant… → gcp server › gcp-key** (the grant is `https://grain.dev/capabilities/gcp#gcp-key`, the same on every deployment) and pick the **Permission set** from the dropdown. A template's grant, set included, is copied onto every task it files ([docs/schedules.md](schedules.md#grant-presets-capability-arguments-and-network-access) has a `settings/templates.yml` example). The first time a set is minted, grain creates `<prefix>-<deployment id>-<set>-1@<project>` — on one deployment, set `ci` in project P is `grain-049f1e09-ci-1@P` — and binds the set's roles; that can take a minute to take effect in GCP. The task's agent finds its key at `~/.gcp-service-account.json`, gcloud already logged in with the project selected.

A task that needs two sets holds two grants; set **primary** off on the second, whose key goes to `~/.gcp-keys/<set>.json`.

## 4. Watch usage and capacity

Test reports, for each set, how many accounts it has against `pool_max` and how many keys are held now. Each account takes ten keys at once (GCP's limit), so a set serves `10 × pool_max` concurrent attempts; past that a mint waits (`resource_exhausted`) until a key is released.

## 5. Remove a set

Delete the set and save. Its service accounts are **not** deleted: the next Test lists them as `removed set <name> still has <email>`. To delete them, add the set's name to **Delete removed sets' service accounts** (`delete_orphans`) and save; grain removes their project bindings and deletes them. Only accounts carrying this deployment's marker (below) are ever listed or deleted.

## Account names and ownership

A set's accounts are named `<prefix>-<deployment id>-<set>-<n>`. The deployment id is the one grain already puts in its branch names (`grain/<deployment>/task-<n>`): **Deployment ID** on System → Agents & Task Branches when a system admin has set one, otherwise the default drawn from the installation id. Grain sends it to the server itself, with the installation id, in the reserved configure fields `grainDeploymentId` and `grainInstanceId` ([`docs/capabilities-spec.md`](capabilities-spec.md) §4.5.7); they are not in the form and cannot be typed. A set that creates accounts is refused at configure if they are missing — the server must be attached through grain.

GCP allows 6–30 characters. When the full name is longer, the set's name is cut first, keeping its first characters and four hex digits of a hash of `<project>/<set>`; if even three characters of the set and its hash do not fit beside the deployment id, the deployment id is cut too, keeping four hex digits of its own hash: `grain-049f1e09-a-very-l-3f2a-1`. Names are deterministic. The set's hash covers the project as well as the name, so the same long set name in two projects looks different. A name some other account already has in the project is skipped for the next `n`, never shared.

Ownership does not rest on the name. Every account grain creates is described with the marker `[grain-gcp-key deployment=<deployment id> instance=<installation id prefix> set=<set>]`, and listing, minting, the reaper, Test and `delete_orphans` consider only accounts whose marker carries **this installation's** id. That has these effects:

- **Several deployments in one project** — or the in-tree `gcp` server running beside the old in-process `gcp-key` — each create and use their own accounts, even with the same set names, and even if two admins chose the same deployment id. Another deployment's accounts are never leased, reaped, rebound or offered as orphans; Test reports only how many there are (`project P: N service account(s) of other grain deployments, left alone`).
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

## Running alongside the built-in gcp-key

The in-process `gcp-key` capability (the bare grant name `gcp-key`, configured by `gcp_project`, `gcp_service_account_email` and `gcp_service_account_count`) keeps working unchanged next to the gcp server. A task can hold either: `gcp-key` stays the in-process provider, and `https://grain.dev/capabilities/gcp#gcp-key` is the server. Nothing rewrites one into the other.

**Do not point both at the same service accounts.** On this deployment the in-process provider mints from the fixed pool `grain-sb-agent-0` … `grain-sb-agent-9@grain-sandbox-dev`. If the server is configured from the same flags or `GCP_MINTER_KEY`, its migration (`gcpsets.Migrate`) produces a `sandbox-agent` set over exactly those accounts. GCP allows ten keys per service account, and the two leasing systems cannot see each other's leases, so each would find accounts full that the other filled, on top of the IAM write-quota 429s already seen on 2026-10-23. **Test** on the gcp server warns, naming the set and the shared accounts, whenever one of its permission sets covers an account the in-process provider mints from.

The recommended trial setup is a **separate permission set with its own service accounts**: a set with no `accounts`, so the server creates `<prefix>-<deployment id>-<set>-<n>` itself, under an admin of its own. Grant it to the trial tasks as `https://grain.dev/capabilities/gcp#gcp-key` with that set, and leave everything else on `gcp-key`. Once the trial is done, retire the fixed pool as described above.

## Running the live test

`go test ./pkg/capability/gcpsets -run TestLiveMintUseRelease -v` with `GRAIN_GCP_LIVE_PROJECT=grain-sandbox-dev`, `GRAIN_GCP_LIVE_BUCKET=<a test bucket>` and either `GRAIN_GCP_LIVE_ADMIN_KEY=<admin key file>` or ambient ADC. It runs as deployment `livetest` (account `grain-livetest-live-test-1`), mints a key for a set holding `roles/storage.objectViewer` on the bucket, lists the bucket with gcloud as that key, releases it and checks the key is gone. Without the variables it is skipped.
