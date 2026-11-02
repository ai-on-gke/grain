# terraform/gcp -- a grain deployment on GCP

Deploys one small VM running `grain daemon` (`README.md`,
"Deploying it"), exposed at a fixed DNS name through an IAP-protected
HTTPS load balancer, with a separate persistent disk for its state so the
VM itself is disposable (bwsalmon/agents#394) and another for everything
its sandboxes write, so a task that fills a disk does not take the host
down with it ("Sandboxes get a disk of their own").

```
  terraform apply ──▶ the VM, its data and sandbox disks, its network,
        │              its load balancer, its service accounts and IAM
        │              grants
        │
        ▼
  push-secrets.sh ──▶ Secret Manager (a GitHub PAT, a Gemini API key,
        │              and -- if configured -- a minted GCP minter key),
        │              readable by the host's service account alone
        │
        ▼
  grain-config-sync.service on the host notices grain-deploy-generation
  changed
        │
        ▼
  deploy.sh: read the non-secret config out of this instance's metadata
  and the secrets out of Secret Manager, run scripts/setup.sh
```

Unlike v1's module (a controller VM running a pool of nested libvirt
sandbox guests, meant to be forked by many different organizations from
a template), this module is narrower on purpose:
grain dispatches each run into a sandbox on the one host it runs on, so
there is no fleet to generalize over here -- just the one VM
`scripts/setup.sh` already knows how to install and update, with the
staging-specific pieces (the disk split, the load balancer, IAP, the
IAM grants) wrapped around it.

## Use it

**1. Bootstrap the GCP side**, once, from a project-owner account:

```sh
./deploy/terraform/gcp/bootstrap-gcp.sh --project my-staging-project
```

This creates the Terraform state bucket and a deployer service account
with exactly the roles this module needs (see that script's own
comments). Pass `--repo owner/name` too if CI, not a human, will run
Terraform -- it also wires up workload identity federation, the same
mechanism v1's own bootstrap script used, so CI needs no long-lived
key.

The state bucket is the one piece `deploy/terraform-apply.sh` will also
create for itself, when a deploy finds it missing -- with the same
uniform bucket-level access, versioning and public-access prevention
this script applies. It only ever creates the name this script would
have chosen, `<project_id>-<name_prefix>-tfstate`; a `backend.hcl`
naming anything else is treated as a deliberate choice (or a typo) and
fails rather than being created, since a mistyped bucket would otherwise
give a deployment an empty state and have it build a second copy of
itself. Everything else here still has to be run once by hand: the
deployer account and the workload identity pool cannot bootstrap
themselves from a job that authenticates as them.

**2. Fill in the config.** Copy `example.tfvars` and `backend.hcl.example`
and fill in every `CHANGE-ME`. At minimum: `project_id`,
`deployer_member`, and `iap_members` -- an empty `iap_members` is a valid
plan, but nobody can reach the UI until it names someone.

`iap_members` is who IAP lets reach the daemon; `allowed_principals` is
who grain then admits as themselves, without the access code. They are
separate lists. Left unset, `allowed_principals` is `[deployer_member]`
when that is a `user:` -- so the person installing the deployment, signed
in through IAP, is the owner -- and otherwise empty, which leaves the
daemon's own default (its OS account and grain's service accounts) that
no IAP email matches. Set it explicitly when `deployer_member` is a
service account, as it is for CI deploys. Anyone in `iap_members` who
is not in `allowed_principals` still reaches the daemon, but is refused
unless they present the access code.

**3. No OAuth client needed.** IAP uses a Google-managed OAuth client
when none is configured, which is why the IAP OAuth Admin API was
deprecated in January 2025 -- there is normally no client to create any
more. Leave `create_iap_brand`, `iap_client_id` and `iap_client_secret`
alone and go straight to the apply.

Set `iap_client_id`/`iap_client_secret` only for a client of your own,
created by hand once (the GCP Console's "Google Auth Platform" page --
check current GCP guidance, this area of the product has moved). Do not
set `create_iap_brand` even then: a project may have at most one brand,
ever, and the provider warns `google_iap_brand` no longer functions as
intended for a new one.

**4. Apply.**

```sh
terraform init -backend-config=backend.hcl
terraform apply -var-file=staging.tfvars
```

A first apply takes a few minutes for the load balancer and managed SSL
certificate to finish provisioning (Google has to validate the
certificate against `dns_name` before it's usable, which can take up to
around an hour the very first time). The VM itself comes up much faster.

**5. Push the secrets.** Terraform never sees the GitHub PAT, the Gemini
API key, or the minter's own key -- see "Secrets never touch Terraform"
below.

```sh
export PROJECT="$(terraform output -raw project_id)"
export INSTANCE="$(terraform output -raw instance_name)"
export MINTER_SERVICE_ACCOUNT="$(terraform output -raw minter_service_account)"
export GRAIN_GITHUB_TOKEN="github_pat_..."   # a fine-grained PAT scoped to test_repos
./deploy/push-secrets.sh
```

Each value becomes the new version of a Secret Manager secret named
`<name_prefix>-<key>` (`secrets.tf`), and the version it replaces is
destroyed. The host reads them on its next deploy -- push one between
deploys and bump `deploy_generation` if it has to land now.

`GRAIN_GEMINI_API_KEY` is optional: with `enable_gemini_key` on (the
default) the host mints the daemon's own key for itself -- see "The
daemon's own Gemini key" below. Export one anyway to use a key of your
own instead.

`GRAIN_SECRETS_KEY` is optional too, and for the opposite reason: a first
deploy has no secrets yet, so the host minting its own key is the right
answer. Export it when standing a deployment back up on a rebuilt VM, so it can
read a secrets file restored beside it -- see "The secrets key is the one
file a redeploy must carry" below, and copy that file off the host before
you are the one who needs it.

`GRAIN_GITHUB_APP_ID`, `GRAIN_GITHUB_APP_INSTALLATION_ID` and
`GRAIN_GITHUB_APP_PRIVATE_KEY` are an alternative to `GRAIN_GITHUB_TOKEN`,
stored under the same `credential_name`: set all three (never a subset)
instead of the PAT to back this deployment with a GitHub App installation
token rather than a bare PAT -- see "There is no Checks permission to
grant" below for when that is needed and how to obtain them. Registering
the App itself is still a manual, browser-based step on GitHub's own
side; only pushing the resulting three values here is automated.

Watch it converge with:

```sh
gcloud compute ssh "$INSTANCE" --zone "$ZONE" --project "$PROJECT" \
  --tunnel-through-iap --command 'sudo journalctl -u grain-config-sync -f'
```

**6. Open it.** `terraform output url` -- sign in as one of
`iap_members`. With `expose_ui_publicly = false` there is no URL: run
`terraform output tunnel_command`, which prints the
`gcloud compute start-iap-tunnel` line to forward the UI to
`http://localhost:8080`.

## Bootstrapping the rest of a hand-created VM

Everything above assumes Terraform creates the VM itself, applied from
outside it, state in the GCS bucket `bootstrap-gcp.sh` creates. That's
not the only starting point: bwsalmon/agents#620 added a second one, for
a VM created by hand (the Console, a one-off `gcloud compute instances
create`, ...) that still wants this module's service accounts and IAM
grants set up afterward -- run from the VM
itself, against `-target`ed subsets of this same module, with state kept
on the VM's own data disk (`backend-local.hcl.example`) instead of GCS.

That path is written up as a playbook an agent holding the
`bootstrap-playbooks` grant can read and act on --
`pkg/capability/bootstrap/playbooks/gcp-capabilities.md` -- rather than
repeated here; file an interactive task carrying that grant (plus
`self-repair`, for the commands the playbook tells it to run) and ask it
to bootstrap GCP capabilities, or read that file directly.
The GitHub-side setup (`github-connections.md`, `github-test-repos.md`
in the same directory) has no Terraform involved at all -- see those
playbooks for why.

## What actually runs on the host

`grain-daemon.service` runs a container image, not a binary this host
built (bwsalmon/agents#645): grain plus every binary it shells out to,
published to GHCR by `.github/workflows/build-artifacts.yml` on every
commit to grain (`Dockerfile`). Three variables decide which one:

- **`grain_ref`** names the branch. It is what the on-host checkout
  tracks -- `files/deploy.sh` clones and updates it, and that checkout
  is where this deploy finds `scripts/setup.sh` -- and, by default, also
  which image tag runs: that branch's name with `/` replaced by `-`,
  which is how CI tags it. Nothing else on the host reads that checkout:
  `setup.sh` keeps none of its own, and `scripts/granule`'s guest build
  runs out of the source unpacked from the deployment image.
- **`grain_image_tag`** overrides that. Set it to `sha-<short sha>` --
  published for every commit -- to pin this deployment to one immutable
  build. A rollback is that variable plus a `terraform apply`: no
  rebuild anywhere, and the old image is still in the registry.
- **`grain_image`** is the repository, for a mirror or a private copy.
  A registry needing credentials takes `grain_image_pull_user` here and
  a `GRAIN_IMAGE_PULL_TOKEN` through `push-secrets.sh`;
  `ghcr.io/bwsalmon/grain`'s own package is public and pulls
  anonymously.

Two consequences worth knowing before a first deploy. A deploy no longer
compiles anything on this host, so `grain_ref` pointing at a tag or a
commit SHA -- fine for the checkout -- has no image published under that
name, and needs `grain_image_tag` set alongside it. And the host now
needs to reach the registry: an egress policy that allowed GitHub and a
Debian mirror also needs `ghcr.io`.

For the container's own shape -- what it is given, what it deliberately
is not, and the two systemd path units that let it reboot the host and
restart itself from inside a container -- see grain's own
`README.md`, "The deployment is a container".

## Deploying it from CI

Everything above is the by-hand path. To have a config repo's GitHub
Actions apply it instead, pass `--repo owner/name` to the bootstrap
script and wire a workflow to the step scripts in this module's
`deploy/`:

```
deploy/terraform-apply.sh    init, validate, apply (with stock-out retries)
deploy/read-outputs.sh       Terraform outputs -> Actions step outputs
deploy/push-secrets.sh       the same script the by-hand path runs, called with env
deploy/wait-for-host.sh      block until the host reports it converged
deploy/write-summary.sh      the job summary
```

The step bodies live in grain, and the config repo's workflow only wires
them up -- supplying which secrets, which config directory, which
generation. That is the same split v1's own config-repo template
argues for: a workflow file is forked and then owned by the config repo,
so anything written *there* is something nobody re-syncs, while a fix
here reaches every deployment on its next `grain_ref` bump. See
`bwsalmon/agents`'s `.github/workflows/deploy-v2-staging.yml` for a
worked example.

These five paths are the interface a config repo pins, and all five
moved into it: four from grain's old top-level `ci/`, and
`push-secrets.sh` from the module root beside this README, so one
deploy's steps are no longer split across two directories for no
reason. A workflow still naming the old paths breaks on the
`grain_ref` bump that first picks this layout up, so repoint it in the
same change. Two names also shortened, now that the directory says the
rest: `read-terraform-outputs.sh` is `read-outputs.sh`, and
`write-deploy-summary.sh` is `write-summary.sh`.

Two things differ from a by-hand deploy:

- **`deployer_member` must be the CI service account**, not a human --
  `push-secrets.sh` mints the minter's key, which needs
  `deployer_manages_minter_keys` (iam.tf) on whoever runs it, and writes
  the secrets `secrets.tf` grants it `secretVersionManager` on. That
  also means `allowed_principals` seeds nobody by default, so name the
  owner there explicitly.
- **The workflow supplies `deploy_generation`**, so a manual re-run
  redeploys rather than no-oping. `wait-for-host` then blocks on the host
  reporting *that* generation, and CI goes green only once the rollout
  actually landed.

### Sharing a project with another grain deployment

Supported -- and now the normal case, since `bwsalmon/agents` runs both a
main and a staging deployment of this module in one project. It is the
reason `bootstrap-gcp.sh` derives its workload identity pool and provider
from `name_prefix`. A pool is a project-level resource, and the script
once hardcoded "github" for it: on that name, bootstrapping a second
deployment into a project that already had one took the *update* branch
on the first's provider and rewrote its attribute condition to name
whatever `--repo` was passed the second time -- a no-op while both name
the same repo, and a deploy that can no longer authenticate at all the
moment they differ. The script prefixes now, so two deployments never
touch. `--pool`/`--provider` override it for a pool shared deliberately.

Everything else that collides is a *project-level* name. Per-host names
do not: the guest attribute namespace (`grain`), the
`grain-config-sync.service` unit, and the `grain-data`/`grain-sandbox`
disk devices all live on one instance, and `gcloud compute instances
get-guest-attributes` reads the instance you name, so two deployments
never see each other's status through them.

What has to differ, per deployment:

| Setting | Why |
|---|---|
| the state bucket/prefix (`backend.hcl`) | two deployments on one prefix each see the other's resources as drift to destroy |
| `name_prefix` | the instance, network, deployer account, and WIF pool/provider are all derived from it |
| `agent_account_id` | a project-level service account; see below |
| `minter_account_id` | likewise, when both deployments enable `enable_gemini_key` |

**`agent_account_id` has to be set on at least one of them.** It defaults
to `grain-agent`, matching `pkg/gcpsetup.DefaultAgentAccountID`, so a
second deployment left at the default fails with

```
Error 409: Service account grain-agent already exists within project ...
  with google_service_account.agent[0], on iam.tf line 69
```

Set something like `agent_account_id = "grain-staging-agent"`. Do not
point it at the other deployment's existing account to make the error go
away: that account carries the other deployment's grants, so sharing it
hands a staging agent whatever the main deployment was trusted with,
which is the whole thing sharing a project is already uncomfortably close
to.

Nothing on the host assumes the default name -- the account's email
reaches the daemon as `gcp_agent_service_account` in instance metadata,
straight from this module's own output -- so changing it costs nothing.

What is *not* separated by any of this is the agent account's reach.
`agent_can_manage_gke` grants `roles/container.admin` project-wide with
no exclusion, and `agent_can_manage_compute_instances`'s exclusion names
only this deployment's own host -- so in a shared project a staging
agent can reach the *other* deployment's host VM and any cluster in the
project. Set both to `false` while sharing, or give each deployment its
own project.

## Secrets never touch Terraform

Five kinds of value never appear in a `.tf` file, a `tfvars` file, or any
plan this module is applied from:

- **The GitHub PAT** -- a fine-grained token scoped, on GitHub's own side,
  to `test_repos`, which is also wired into the daemon's own
  `-target-repos` allow-list -- see that variable's own doc comment and
  this module's README, "Repo enforcement."
- **The Gemini API key** -- the daemon's own operating key
  (`pkg/agent/gemini`). Distinct from the gemini-key *capability*
  (`pkg/capability/geminikey`), which mints its own short-lived keys per
  task. Optional to supply: see "The daemon's own Gemini key" below.
- **The Claude Code OAuth token** and **the OpenAI API key** -- the same
  thing for the other two agent frameworks (`pkg/agent/claude`,
  `pkg/agent/codex`), pushed as `GRAIN_CLAUDE_CODE_OAUTH_TOKEN` and
  `GRAIN_OPENAI_API_KEY`. Also optional, and for a reason that now
  applies to the Gemini key too: any of them can be pasted into the UI
  instead (Settings -> Models), which stores it in the host's
  own secrets database and takes precedence over whatever Secret
  Manager carries. The daemon starts and serves the UI with none set; only a
  *dispatch* needs one, and a run whose framework has none fails saying
  exactly that. Push one to have a deployment come up already able to
  dispatch, without anyone opening the UI first. Which framework a run
  is driven by is a store-backed setting, overridable per task, so a
  deployment that might use more than one wants each of their
  credentials.
- **The minter's own key** -- what lets `pkg/capability/gcpkey` mint and
  revoke the agent account's per-task keys.
- **The secrets key** -- grain's own key rather than a credential to
  anything else: what decrypts the encrypted secrets file under the data
  directory, pushed as `GRAIN_SECRETS_KEY`. Unset on a first deploy, where
  the host mints its own. See "The secrets key is the one file a redeploy
  must carry" below, which is the only one of these five that a rebuilt
  host cannot be handed a fresh replacement for.

All of them go into Secret Manager via `push-secrets.sh`: one secret
each, named `<name_prefix>-<key>`, created by `secrets.tf` with no
version, so Terraform holds their names and never a value. The host's
own service account is granted `roles/secretmanager.secretAccessor` on
each of them -- per secret, never project-wide, so a project shared with
another deployment shares none of its secrets -- and `deployer_member`
`roles/secretmanager.secretVersionManager`, which writes and destroys
versions but cannot read one. `files/deploy.sh` reads each latest
version with the access token the metadata server hands the host's
account; containers and sandbox guests cannot reach the metadata server
(`grain-block-metadata.service`), so they cannot get that token either.
Every read is a Data Access audit log entry, and revoking the grant
revokes the host's access without touching the instance.

They used to be custom instance metadata instead (#2376), which is
worth knowing for what it cost: anything that reached the metadata
server could read every one of them with no credential at all, anyone
with `compute.instances.get` on the host could read them through the
Compute API, and `terraform refresh` copied every one into the state
file, so a plan that replaced the host printed them in full -- which is
how the minter account's private key reached a deploy log in
bwsalmon/agents#653. `deploy/terraform-apply.sh` still filters the
apply's output for those attribute names, for the one apply on an old
deployment that removes them.

### Moving an existing deployment off instance metadata

`instance.tf` no longer ignores the old `grain-*` secret attributes, so
the first apply after this change removes whatever is still on the
instance. Copy them into Secret Manager before that happens:

- **From CI** there is nothing to do but the role below:
  `deploy/terraform-apply.sh` notices the instance still carries them,
  applies just the secrets and their grants, runs
  `deploy/migrate-secrets.sh`, and only then runs the full apply.
- **By hand**, the same three steps:

  ```sh
  terraform apply -var-file=staging.tfvars \
    -target=google_secret_manager_secret_iam_member.host_reads \
    -target=google_secret_manager_secret_iam_member.deployer_writes
  PROJECT=... INSTANCE=... ZONE=... ./deploy/migrate-secrets.sh
  terraform apply -var-file=staging.tfvars
  ```

`migrate-secrets.sh` copies each attribute into its secret -- never over
a version a newer `push-secrets.sh` already wrote -- and removes them all
from the instance once every copy has succeeded. Either way, the
identity applying Terraform needs `roles/secretmanager.admin` to create
the secrets and their IAM: `bootstrap-gcp.sh` grants it now, so re-run
it (it is idempotent) or grant that one role to an existing deployer
before the first apply with this change. The state file keeps whatever
values an earlier refresh wrote into it until the next apply rewrites
it, so the bucket's older object versions still hold them; rotate
anything you would not want read from there.

None of this routes a value through Terraform: keeping them out of
configuration is what keeps them out of diffs, pull requests, this
repository and now the state as well.

## The daemon's own Gemini key

With `enable_gemini_key` on (the default here), the minter account
already holds `roles/serviceusage.apiKeysAdmin` project-wide -- that is
what lets the gemini-key *capability* mint a short-lived key per task.
The same grant is all it takes to mint the daemon's own long-lived
operating key, and `push-secrets.sh` has already put the minter's
credential on the host. So the host mints that key for itself:
`scripts/setup.sh`'s `mint_gemini_operating_key` runs
`grain secrets mint-gemini-key` when no key is in place yet, right after
seeding the minter credential it authenticates with.

Nothing about this widens a grant. It is the same credential, the same
API, and the same `generativelanguage.googleapis.com` restriction every
per-task key is minted under -- which also makes a minted operating key
narrower than a hand-made one, since a key created by hand in the
console is unrestricted unless someone remembers to restrict it.

What it removes is a first-deploy footgun: without it, applying and
pushing secrets leaves you with a deployment whose every dispatched run
fails at setup, because the credential its agent framework runs as is
the one nobody has pasted in yet. (The daemon itself comes up either
way now, and its Settings pane is the other way to fix that -- see
README.md, "Two agent frameworks, either per task".)

Three things worth knowing:

- **It is seed-once.** An existing non-empty key is never overwritten,
  so `config-sync` re-running `setup.sh` on every convergence pass does
  not issue a fresh key each time, and a key you placed by hand always
  wins.
- **Rotating is manual**, the same as the minter key above: delete
  `<data-dir>/secrets/gemini-api-key` on the host, delete the old key in
  GCP, and bump `deploy_generation` so the next pass mints a new one.
- **The reaper never touches it.** `geminikey.DeleteExpired` deletes
  `grain-`prefixed keys older than 24 hours; the operating key carries
  that prefix too and is exempted by exact name
  (`geminikey.OperatingKeyDisplayName`), because it is the credential
  the daemon runs as rather than a per-task lease that leaked. That
  exemption is what keeps the reap from stopping the daemon a day after
  every deploy -- so the constant must keep its prefix, and the
  exemption must keep matching it.

Set `enable_gemini_key = false`, or supply `GRAIN_GEMINI_API_KEY`
yourself, and none of this runs.
## Where this deployment's configuration lives

Every deployment has a controller-local git repository named
`grain/config`. grain exports its database there as text
(`pkg/staterepo`) and applies merged configuration changes from native
Grain pull requests. The repository has no external remote and the deploy
does not accept a repository URL or branch for one.

The data disk, mounted at `/var/lib/grain/data`, holds both this
repository and the SQLite database. Preserve that disk during rebuilds,
and back it up (a disk snapshot schedule is the simple way): back up
`/var/lib/grain/data`; never back up `/var/lib/grain/scratch`, which is
the sandbox disk's run space and caches and is rebuilt by the next
deploy. A future backup facility will back up both stores together;
until it exists, neither is mirrored off the host by Grain.

The secrets key cannot be reissued by the next deploy, and nothing can
stand in for it. grain's secrets live encrypted under the data directory
(`<data-dir>/secrets/secrets.enc`), outside the config repository, sealed
to a public key whose private half is one file beside it
(`<data-dir>/secrets/secrets.key`) that grain copies nowhere. A host
restored onto a fresh data directory therefore needs both halves handed
back -- the secrets file from a backup of that directory or from
`secrets_repo_url` (below), and the key from here -- and one that mints
itself a fresh key instead cannot read a line of what was put beside it. `pkg/secrets` reports that as the
unrecoverable state it is rather than starting over silently, and it is
right to: there is no escrow here and no recovery path.

So keep a copy, and push it back when you rebuild:

```sh
# on the host, once, after the first deploy -- prints the file to copy
# and the public key that identifies it
gcloud compute ssh "$INSTANCE" --zone "$ZONE" --project "$PROJECT" \
  --tunnel-through-iap --command 'sudo grain state -data-dir /var/lib/grain/data status'

# rebuilding: hand the new host the key its predecessor had
export GRAIN_SECRETS_KEY="$(cat grain-secrets.key)"
./deploy/push-secrets.sh
```

Three things worth knowing:

- **It is seed-once**, like the agent credentials: `setup.sh` writes it
  only when the host has no key, so a key already on the host always wins
  -- it is the key that host's own secrets were encrypted to -- and
  `config-sync` re-running the deploy on every generation never replaces
  it. Pushing it on every deploy is therefore harmless; never pushing it
  is what costs a redeploy every secret it had.
- **Leave it unset on a first deploy.** A host with no key mints its own,
  which is the right answer for a deployment that has no secrets yet.
- **It is reported where you are already looking.** `setup.sh`'s
  readiness summary names the key (or says none exists yet) on every run,
  and `grain state status` prints the file, its public key, and what
  happens if nobody backs it up. That is deliberate: the deploy that
  needs this file is the *next* one, and a key nobody copied is only
  discovered missing once it is gone.

## And the encrypted file itself: `secrets_repo_url`

The key is the half that cannot be regenerated; the file it opens is the
half that, with nothing set here, exists on the data disk and nowhere
else. `secrets_repo_url` (and `secrets_repo_branch`, `main` by default)
gives it a git repository of its own -- written into
`<data-dir>/secrets-repo.json` by `setup.sh` -- and grain pushes the
ciphertext there on the same timer that exports its local config state.

The git proxy refuses the secrets repository to every sandbox, whatever
a task's scope says.

The file defaults to `grain-local.secrets.enc`. Set the file name through
`grain secrets repo -file` when several controller-local deployments share
one repository. The private key is pushed to neither repository, so a
clone of this one is every secret grain has in a form only the key above
can open. With both halves, a rebuilt host restores itself: the file
arrives from the repository before the daemon opens its store, and the
key arrives from `GRAIN_SECRETS_KEY`.

## Granule sandboxing

`enable_granule_sandboxes` (on by default, `variables.tf`) dispatches onto
real `bwsalmon/granule`-managed VMs, one per slot, over SSH
(`orchestrator.GranuleSandboxes`) instead of local Docker containers
(`orchestrator.ContainerSandboxes`) -- the same nested cloud-hypervisor guest
`scripts/granule/README.md` documents building, now actually wired through
this deployment shape (bwsalmon/agents#504) rather than only configurable
by hand-editing the systemd unit afterward.

Nothing has to be built or published by hand first, and nothing is built
on the host. `scripts/setup.sh`'s own `ensure_granule_images`/
`ensure_granule_kvm_access` pull the sandbox image and grant `$GRAIN_USER`
`/dev/kvm` and `docker` group access, before `write_systemd_units` wires
up `grain daemon`'s own `-granule-*` flags.

Which image is not a decision this deployment makes: CI publishes one per
commit, and the grain image a host runs carries the reference of the one
built from its own commit (`grain sandbox-image`), so the two are always
one commit's worth of each other -- including after a rollback, which asks
for its own older sandbox rather than whatever is newest
(bwsalmon/agents#645). The guest a task actually runs in travels inside
that same image, which is why there is one artifact here and not two.

That is a change worth knowing if you deployed an earlier version. A host
used to *build* its guest disk on first use -- debootstrap plus the whole
package set the guest build installs, several minutes against a real
Debian mirror, on every host and every deploy generation, with a
content-hash cache to avoid repeating it. It did that because
the guest build baked that deployment's own SSH public key into the
image, so no generic published disk could exist. granule generates that
keypair per VM boot now, so the guest is built once in CI from the
repository's `Granulefile` (`scripts/release-images.sh`) and pulled here like anything else.
`granule_image_bucket`, which existed for an operator who wanted to build
it once centrally and share it across a fleet, is gone with the build it
was an alternative to.

`granule_oci_image` overrides the pulled reference with one of your own --
a mirror, a private copy, or a sandbox pinned apart from grain's. It is
pulled either way, and built on the host in no case. (Pointing it at an
Artifact Registry repository in this project works; create the repository
yourself first.)

A granule prerequisite that is not ready this run (no `/dev/kvm`, a sandbox
image that will not pull) makes `setup.sh` dispatch into local Docker
containers instead, and say which prerequisite was missing. Re-running
`setup.sh` (or waiting for the next `config-sync` pass) picks granule back
up once it is there.

Set `enable_granule_sandboxes = false` to run on local Docker containers
indefinitely: `files/deploy.sh` passes the matching
`GRAIN_CONTAINER_SANDBOXES=1` through to `setup.sh` on your behalf. Nothing
above is required then, and `enable_nested_virtualization` can come off
with it if nothing else on this host needs `/dev/kvm`. (This used to mean
plain host directories, with no isolation at all; that backend has been
removed from grain.)

Each VM's root filesystem is writable, not read-only:
`write_systemd_units` passes `-disk-mode=overlay`, so the guest writes
into a thin qcow2 created inside its own container and backed by the
disk the sandbox image carries, which is only ever read
(bwsalmon/granule#37). Creating it costs a fixed few hundred KiB whatever
the disk's size, so booting a VM never copies a multi-gigabyte image and
several VMs on a host share one copy of it. `granule`'s own default is
read-only, which a dispatched task cannot use. The host directories this
used to need -- one for the shared image, one for the overlays -- are
gone with it.

Which is also why the sandbox volume is docker's data root rather than a
directory of its own: with the overlay inside the VM's container, "how
much disk can a task use" is a question about docker's storage, and
`sandbox_disk_gb` is the answer to it (see "Sandboxes get a disk of their
own").

A freshly created VM's container/pod getting an IP is not the same moment
as its nested guest actually accepting SSH connections -- confirmed by
hand, a docker-backed VM's container is reachable well before the guest
has finished booting to sshd. `orchestrator.GranuleSandboxes.resolveEndpoint`
now waits out that gap too (bwsalmon/agents#504: a plain TCP dial against
the resolved host:port, polled the same way it already polled for the
container IP itself), bounded by the same `ReadyTimeout` (2 minutes by
default) as the IP wait -- so the first dispatched tool call against a
brand new slot no longer races a guest that is still booting. A guest
that genuinely takes longer than that to boot (a cold local disk, a busy
host) still fails clearly, naming the address it could not reach, rather
than the ambiguous "connection refused" a bare SSH client would have
given no hint about.

## What the deployment is allowed to do

`iam.tf` creates three service accounts and grants each exactly what
its own capability needs -- see that file's own comments for the full
reasoning, and `variables.tf` for the on/off switches:

- **host** -- `roles/logging.logWriter`, `roles/monitoring.metricWriter`.
  `roles/secretmanager.secretAccessor` on this deployment's own secrets
  and nothing wider (`secrets.tf`).
- **agent** (`grain-agent` by default, matching
  `pkg/gcpsetup.DefaultAgentAccountID`) -- what a task's short-lived GCP
  credentials belong to. `enable_gemini_key`, `agent_can_manage_compute_instances`,
  `agent_can_manage_gke`, `agent_can_manage_cloud_sql`, and
  `enforce_private_infrastructure_policies` (all on by default for this module,
  per bwsalmon/agents#394's own ask) grant it, respectively: minting a Gemini
  API key per task; creating/managing Compute Engine instances everywhere in the
  project except this deployment's own host VM; creating/managing GKE clusters
  and Artifact Registry repositories, project-wide; creating/managing Cloud SQL
  database instances, databases, and users, project-wide; and enforcing project
  organization policies to ensure clusters, VMs, and databases are private.
- **minter** (`grain-gcp-key-minter` by default) -- mints and revokes the
  agent account's keys, and (with `enable_gemini_key`) administers API
  keys project-wide. Its own key never touches Terraform; see
  "Secrets never touch Terraform" above.

## Enforcing private infrastructure policies

`enforce_private_infrastructure_policies` (on by default) declares project-level
GCP organization policy constraints so agent tasks can only provision private
infrastructure:

- **`constraints/gke.restrictPublicEndpoints`** -- enforces restriction on
  public endpoints to guarantee GKE clusters are created as private clusters
  (with private control plane endpoints).
- **`constraints/compute.vmExternalIpAccess`** -- denies external IP allocation
  for GCE VM instances and GKE node pools, preventing public IP assignment.
- **`constraints/sql.restrictPublicIp`** -- restricts public IP configuration
  on Cloud SQL instances to ensure database instances are private and not
  exposed to the general internet.

## Creating a VM as the agent

`agent_can_manage_compute_instances` grants the roles, but a bare
`gcloud compute instances create my-vm --zone=...` run with a
`gcp-key` credential still fails, twice over, for reasons that name no
role this module sets:

- **The default service account.** `create` with no
  `--service-account` attaches the project's default Compute Engine
  account, and GCP refuses unless the caller holds
  `iam.serviceAccounts.actAs` on *that* account. Nothing here grants
  that, deliberately -- the default account usually carries broad legacy
  roles, so an instance running as it is a wider identity than the agent
  itself. `iam.tf`'s `agent_acts_as_self` grants `actAs` on the agent
  account only, so pass `--service-account=<agent email>` (or
  `--no-service-account --no-scopes` for an instance that needs no GCP
  identity).
- **Port 22.** This VPC opens SSH to Google's IAP range only, and only
  for tagged instances. `network.tf`'s `agent_iap_ssh` covers the
  `<name_prefix>-agent-vm` tag, so an instance created without
  `--tags=<name_prefix>-agent-vm` cannot be reached even though the agent
  holds `roles/iap.tunnelResourceAccessor` -- and it cannot fix that
  itself, since `compute.instanceAdmin.v1` can read firewall rules but
  not create them.

The agent's SSH grant is `roles/compute.osLogin`, so create the instance
with `--metadata=enable-oslogin=TRUE` (or set that project-wide) to keep
OS Login the path in. Left off, `gcloud compute ssh` falls back to
pushing a key into project-wide metadata instead, which is both a wider
write than this needs and a different permission from the one granted
here.

The `agent_vm_create_flags` output prints the whole set for this
deployment. The full lifecycle is then:

```
gcloud auth activate-service-account --key-file=$GOOGLE_APPLICATION_CREDENTIALS
gcloud config set project <project_id>

gcloud compute instances create my-vm $(terraform output -raw agent_vm_create_flags) \
  --machine-type=e2-medium --image-family=debian-12 --image-project=debian-cloud \
  --metadata=enable-oslogin=TRUE
gcloud compute ssh my-vm --zone <zone> --tunnel-through-iap --command 'hostname'
gcloud compute instances delete my-vm --zone <zone> --quiet
```

`--tunnel-through-iap` is not optional: `--no-address` above leaves the
instance with no external IP, which is what lets `enable_cloud_nat` give
it egress without giving the internet a path in.

Two things this deployment does *not* let a task do to itself:
`agent_compute`'s IAM condition excludes this deployment's own host VM
from both instance management and SSH, and `agent_iap_ssh` is scoped to
its own tag rather than the network, so a task cannot tag its way onto
the host's rules either.

## Repo enforcement

`test_repos` is now wired into the daemon's own `-target-repos`
allow-list (`pkg/model.Config.TargetRepos`, bwsalmon/agents#399): a
task naming a repo outside it is filed as asked but parked awaiting
reply rather than dispatched, the same "the allowlist the git proxy
enforces, so a task naming anything else is parked with a comment rather
than dispatched" v1's own `target_repos` documented. `default_target_repo`'s own validation, above, already
requires it be a member of `test_repos` (or empty), so the two variables
stay consistent with each other by construction.

### What the PAT needs

A **fine-grained** token, with repository access limited to exactly
`test_repos`, and four repository permissions:

| Permission | Level | What needs it |
|---|---|---|
| Metadata | Read | mandatory for every fine-grained token; also `GET /repos/{owner}/{repo}`, behind `DefaultBranch` |
| Contents | Read and write | the git proxy's fetch and push, plus creating and updating refs (`CreateBranch`, `UpdateBranch`, `BranchExists`, `GetBranchHead`) |
| Pull requests | Read and write | `CreatePullRequest`, `FindOpenPullRequestForBranch`, `GetPullRequest`, `MergePullRequest` |
| Actions | Read | `ListWorkflowRuns` -- the CI read auto-merge gates on, since a fine-grained token cannot reach the Checks API. Omit it and auto-merge never fires; see below |

**Do not grant `Workflows`.** `pkg/gitproxy` authorizes by repository and
by push-versus-fetch, never by what a commit touches -- nothing in v2
stops an agent editing `.github/workflows/**`, so the token not holding
the permission is the whole of the enforcement, exactly as v1 withholds
the classic `workflow` scope. GitHub rejects such a push server-side.

**`Issues` is not needed either**, unlike v1, where the GitHub issue list
*was* the task queue. v2 reads no issue, writes no comment and moves no
label: tasks arrive by being written to the store (`pkg/ui`), and the
agent's own escape-hatch tools become effects on the task, not GitHub
API calls. `pkg/github` still implements that surface -- it is built, not
wired -- so grant `Issues` only if something later routes those effects
back to GitHub.

**There is no `Checks` permission to grant, which is why `Actions` is on
the table above.** GitHub offered a `Checks` permission for fine-grained
tokens initially and withdrew it "due to some edge cases", and has said
only GitHub Apps may use that API in the meantime -- it is still listed
in GitHub's own permissions reference, so it looks grantable when it is
not. `ListCheckRuns` therefore returns 403 to this deployment
permanently.

`pkg/orchestrator`'s `checkRunsFor` answers that by falling back to
`ListWorkflowRuns`, which reads GitHub Actions workflow runs from the
Actions API and sits behind the `Actions` read permission a fine-grained
token *can* hold. Auto-merge works on this PAT with that permission
granted. What the fallback cannot see is CI reported through the Checks
API by anything other than Actions -- a third-party provider like
Buildkite or CircleCI, or a review bot -- so a deployment whose
`test_repos` use one of those needs a credential that can read checks
properly, below.

Grant neither and there is no CI signal at all: a task with `AutoMerge`
never merges, because a PR whose checks cannot be read is never
`PrClean` (reading it as clean is how you merge a PR with CI red).
`checkRunsFor` treats that as "health unknown" rather than an error, and
`/api/config` reports `autoMergeDegraded` so the UI can say so. Dispatch,
the push, and opening the pull request are a separate reconciler and are
unaffected either way.

Note that a **classic** PAT reads the Checks API fine with the `repo`
scope -- the limitation is specific to fine-grained tokens. `repo` is far
coarser than the four permissions above, though, so prefer the
fine-grained token plus `Actions`, or the App below.

**A deployment needing checks from a non-Actions provider needs a GitHub
App installation token instead of this PAT** (bwsalmon/agents#491) --
`pkg/gitproxy` now has that credential kind, so this is configuration
after all rather than the code change it used to be. Create the App on
`test_repos`' own org with Contents, Pull requests and Checks read
(-and-write for the first two, the same levels the PAT table above
grants), and install it on `test_repos`. That is a manual, browser-based
step on GitHub's own side with no API this module or `push-secrets.sh`
could drive even in principle (see `docs/design.md`, "Auth model") --
there is no way around a human clicking through it once.

Getting the resulting App ID, installation ID and downloaded private key
onto the host is configuration, though, and `push-secrets.sh` now does
it the same way it already does for `GRAIN_GITHUB_TOKEN`: export
`GRAIN_GITHUB_APP_ID`, `GRAIN_GITHUB_APP_INSTALLATION_ID` and
`GRAIN_GITHUB_APP_PRIVATE_KEY` (all three, never a subset) instead of
`GRAIN_GITHUB_TOKEN` before running it -- see "Push the secrets" above.
`scripts/setup.sh` seeds them, once, as a `<credential_name>.app.json`
file next to this deployment's `*.token` files under `secrets/github/`
-- `{"app_id": "...", "installation_id": "...", "private_key":
"-----BEGIN RSA PRIVATE KEY-----\n...\n-----END RSA PRIVATE KEY-----\n"}`
-- under the same `credentials.json` entry a bare PAT would otherwise
occupy, so switching a deployment from one to the other needs no
`credentials.json` edit, just which of the two env vars this run of
`push-secrets.sh` was given. `pkg/gitproxy.CredentialSet` mints and
refreshes an installation token from that file itself (`apptoken.go`);
nothing else about the ladder or the proxy changes. Placing the file by
hand (or editing `credentials.json` to name a different credential
entirely) still works exactly as before -- `push-secrets.sh` is a
convenience over that, not a replacement for it -- and either path needs
`deploy_generation` bumped (this module runs the git proxy and the REST
client in the same `grain-daemon.service`, unlike a deployment shape with
a standalone git-proxy process), or `grain-daemon.service` restarted by
hand, the same way rotating any other credential here already requires.

"A scoped PAT to a few test repos" -- the PAT itself being a GitHub
fine-grained token limited, on GitHub's own side, to `test_repos` -- is
still a second, independent enforcement boundary underneath this one:
even a `grain settings` change widening `TargetRepos` past what the PAT
can reach would still fail at the git proxy. Nothing here keeps the two
lists in sync automatically; an operator who changes one by hand (via
`grain settings -target-repos`, `grain repo add`, or the repos page --
all three write the same field, bypassing Terraform) should update the
other too.

## Notes and limits

- **The host runs nested guests by default.**
  `enable_nested_virtualization` is on, so `machine_type` must be a
  family that supports it -- N1, N2, N2D, C2, C3 or M-series, never E2 --
  and `on_host_maintenance` is `TERMINATE` in consequence. It has to stay
  on for `enable_granule_sandboxes` (also on by default -- "Granule
  sandboxing" above) to work at all; a deployment dispatching only into
  host directories can turn both off together and get MIGRATE, E2, and a
  daemon that survives host maintenance.
- **The load balancer and managed SSL certificate cost more to run than
  the VM does**, and are most of what this module spends. Set
  `expose_ui_publicly = false` and none of it is created: no load
  balancer, no reserved address, no managed certificate, no DNS name.
  The UI is then reached by forwarding `ui_port` over IAP's TCP tunnel
  (`terraform output tunnel_command`), which is the same IAP, checking
  `roles/iap.tunnelResourceAccessor` instead of
  `roles/iap.httpsResourceAccessor`. It also drops the sslip.io
  dependency and the certificate-provisioning wait entirely.
- **`create_iap_brand` is not needed and may not work.** IAP falls back
  to a Google-managed OAuth client when none is configured, which is the
  normal path -- see "No OAuth client needed" above.
- **The DNS name defaults to sslip.io**, a third-party public service
  neither this module nor Google controls, resolving purely by encoding
  the reserved IP in the hostname. Set `dns_managed_zone` to use a domain
  you actually control instead.
- **The minter key rotates with the deploy, and the host follows it.**
  `push-secrets.sh` mints a fresh minter key on every run, pushes it into
  Secret Manager and deletes every key on the minter account beyond
  the newest two; `scripts/setup.sh`'s `seed_gcp_minter_key` writes
  whatever it is handed into the host's own secrets database on every
  run, so the daemon ends each deploy holding the key that was pushed for
  it. That second half is new. It used to seed once and never overwrite,
  which meant a host went on authenticating with the key from its first
  deploy until the third `push-secrets.sh` run deleted it in GCP --
  after which every `gcp-key` mint failed with Google's `invalid_grant`
  ("Invalid JWT Signature") while the Capabilities tab still read
  **Ready**, since the secret is set and only GCP knows the key inside it
  is dead. What has not changed is *when* it takes effect: the host picks
  up a pushed key on its next deploy, so bump `deploy_generation` if you
  need a rotation to land right now.
- **The host's journal reaches Cloud Logging.** `files/deploy.sh`
  installs `google-cloud-ops-agent` early, before anything that can
  fail, which is what the host account's `roles/logging.logWriter`
  grant is for. So a failed rollout is readable without a shell on the
  box -- filter to this deployment in the Logs Explorer with
  `jsonPayload._SYSTEMD_UNIT="grain-config-sync.service"`, or:

  ```sh
  gcloud logging read \
    'resource.type="gce_instance" jsonPayload._SYSTEMD_UNIT="grain-config-sync.service"' \
    --project YOUR_PROJECT --freshness=1h
  ```

  `config-sync` also publishes the tail of a failed deploy as a guest
  attribute, which is what CI prints when a rollout fails.
- **Convergence is polling, not instant**, but close to it:
  `grain-config-sync.service` hangs on the metadata server's
  `wait_for_change` endpoint (`files/config-sync.sh`, ported from v1's
  own mechanism unchanged) rather than truly polling, so a
  `deploy_generation` change or a `push-secrets.sh` run is picked up
  within moments, not minutes.
- **One deployment per state prefix.** Running more than one of these in
  the same project needs a different `name_prefix`, a different
  `backend.hcl` prefix, and (since `agent_account_id`/`minter_account_id`
  default to fixed names) distinct values for those two as well. The
  state bucket, the deployer account, and the workload identity pool and
  provider all derive from `name_prefix` already.
- **Destroying it is deliberately awkward.** The data disk carries
  `prevent_destroy` (`instance.tf`) -- it holds the SQLite store and the
  secrets database, the state a wipe or redeploy must not lose. Remove
  that block first if you really mean to lose it. The sandbox disk
  carries no such block and needs none: everything on it is a re-pullable
  image or a finished run's leftovers (see "Sandboxes get a disk of their
  own"), so it goes with the rest of the host on a wipe.
- **Sandboxes get a disk of their own.** grain keeps everything under
  `/var/lib/grain`, split in two: `data/` (the data disk) and `scratch/`
  (this one). `sandbox_disk_gb` (100 GB by default) is attached as
  `grain-sandbox` and mounted by `files/startup.sh` at
  `/mnt/grain-sandbox`, which then holds both of the things that grow
  with the work this host does:

  - **docker's data root**, moved there with an `/etc/docker/daemon.json`
    the startup script writes. That is where the sandbox image lives and,
    less obviously, where every granule VM's writable root lives too: a VM
    gets its disk as a qcow2 overlay created *inside* its own container
    (bwsalmon/granule#37), so a guest's writes are docker's storage, not a
    host directory anything else could have mounted.
  - **the daemon's scratch directory** -- `/mnt/grain-sandbox/sandboxes`,
    bind-mounted onto `scripts/setup.sh`'s own `$GRAIN_SANDBOX_DIR`
    (`/var/lib/grain/scratch`), where host-sandbox runs and the package
    and Actions caches live by default. Docker's data root stays beside
    it on the same disk rather than inside it, because the daemon empties
    its run directories at startup.

  The boot disk keeps the OS, the journal and the grain checkout, and
  the data disk keeps the store and secrets. So a task that fills its
  disk -- a runaway build, a clone of something enormous -- costs the
  runs in flight and nothing else: the host stays up, `config-sync` still
  deploys, and the UI is still there to say what happened. A
  `docker.service` drop-in carries `RequiresMountsFor=/mnt/grain-sandbox`,
  so `dockerd` waits for the volume rather than racing it and quietly
  filling the boot disk under a mount point.

  The UI's own host status (System → Sandbox health) reports this
  volume as well as the data disk, one row per filesystem: the daemon
  reads `-data-dir`, `-sandbox-dir` and docker's data root and folds
  together whichever turn out to be the same disk, which here means the
  sandbox row covers both of the things listed above. It showed only the
  data disk's figure until then, so the 20 GB volume read as healthy
  however full the 100 GB one beside it got. Docker's data root
  (`/mnt/grain-sandbox/docker`) is not itself mounted into the daemon's
  container, so the daemon logs one line about it at startup and reports
  the volume through `$GRAIN_SANDBOX_DIR`'s bind mount instead -- the
  same filesystem, and the same number.

  On an existing deployment the volume arrives at the host's next boot,
  since the startup script is what mounts it (or run it by hand:
  `sudo google_metadata_script_runner startup`). That boot moves docker's
  data root, so the host re-pulls its images -- the startup script clears
  `config-sync`'s deploy state to force exactly that -- and leaves the
  old `/var/lib/docker` behind, unused, for you to delete once satisfied.
  `sandbox_disk_gb = 0` puts everything back on the boot disk and undoes
  the docker configuration that went with the volume; size `boot_disk_gb`
  accordingly if you do that.
- **The Cloud Run proxy and rendezvous relay are gone.** `cloudrun-proxy.tf`,
  `use_cloudrun_iap_proxy`, the `cloudrun_proxy_*` variables, and
  `relay.tf` / `relay-daemon.tf` (`docs/relay-removed.md`) were removed.
  To reach a daemon from outside its host, either forward `ui_port` over
  IAP's TCP tunnel (`terraform output tunnel_command`, the default in
  `staging.tfvars` with `expose_ui_publicly = false`) or turn on the
  HTTPS load balancer + IAP in front of the VM (`expose_ui_publicly = true`,
  `iap.tf`). Plan refuses a tfvars that still sets
  `use_cloudrun_iap_proxy = true`.

## Layout

```
versions.tf     provider/backend requirements
variables.tf    every knob, with why it exists
network.tf      VPC, firewall rules (IAP-tunnel SSH, load-balancer-to-UI), optional Cloud NAT
iam.tf          the host/agent/minter service accounts, their roles, and organization policies
secrets.tf      the Secret Manager secrets the host's credentials live in, readable by the host account alone
instance.tf     the VM, the data and sandbox disks, non-secret config as instance metadata
dns.tf          the reserved static IP and the fixed DNS name
iap.tf          the load balancer chain and IAP itself
outputs.tf      instance name, zone, service accounts, URL, ssh command
files/
  startup.sh      boot: mount the data and sandbox disks, put docker's data root on the latter, start config-sync
  config-sync.sh  watch metadata, run a deploy when it changes
  deploy.sh       translate this deployment's config into a scripts/setup.sh call
bootstrap-gcp.sh   one-time: state bucket, deployer service account, optional WIF
deploy/
  push-secrets.sh     post-apply: push the GitHub PAT, the agent credentials, the secrets key a rebuild has to carry, and a minted minter key into Secret Manager
  migrate-secrets.sh  once per older deployment: copy secrets out of instance metadata into Secret Manager, then remove them (terraform-apply.sh runs it)
  terraform-apply.sh  init, validate, apply -- creates the state bucket if it is missing
  read-outputs.sh     Terraform outputs -> Actions step outputs
  wait-for-host.sh    block until the host reports it converged on this generation
  write-summary.sh    the deploy's job summary
example.tfvars, backend.hcl.example
```
