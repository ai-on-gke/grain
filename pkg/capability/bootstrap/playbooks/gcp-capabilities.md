# Bootstrap: GCP service account capabilities

Goal: create the GCP service accounts and IAM grants the `gcp-key` and
`gemini-key` capabilities need (`pkg/capability/gcpkey`,
`pkg/capability/geminikey`) -- an agent account those capabilities
mint short-lived keys for, and a minter account that mints/revokes them.
This playbook assumes the deployment's VM and data disk already exist,
hand-created (bwsalmon/agents#620) -- it only ever runs Terraform
`-target`ed at the identity/IAM resources in
`terraform/gcp/iam.tf`, never at the instance, disk, or network
resources in the same module.

Read this whole playbook before running anything. Every shell command
below is something you (the agent) run through the `run_host_command`
tool, one at a time -- each one is posted into this chat and waits for a
human "approve" before it actually runs.

## 1. Ask before touching anything

Confirm with whoever you're talking to:

- the GCP project ID this deployment should use (`project_id`)
- the zone and `name_prefix` the VM was actually created with (these
  must match `terraform/gcp/example.tfvars`' equivalents exactly, or
  a later `terraform plan` will see the hand-created instance as
  "missing" and try to create a second one)
- whether they want the `gemini-key` capability too (`enable_gemini_key`)

## 2. Get a master credential onto the host -- out of band, not through chat

You need to run Terraform (and `gcloud`) as an identity with
`roles/owner` or `roles/resourcemanager.projectIamAdmin` +
`roles/iam.serviceAccountAdmin` (and `roles/orgpolicy.policyAdmin` if
enforcing organization policies) on the project -- broader than anything
this deployment holds standing. Ask the human to place a service-account
JSON key for that identity directly onto the host themselves (`gcloud
compute scp`, the Cloud Console's SSH-in-browser, or equivalent) at a
path they tell you, e.g. `/tmp/grain-bootstrap-gcp.json` -- **never**
ask them to paste the key's contents into this chat. Anything you pass
to `run_host_command` is posted into this task's own conversation
verbatim for the approval step, which is fine for a command but wrong
for a secret: a key pasted into chat lives on in the task's stored
comments long after this playbook is done. Only ever reference the
*path* to the key file in the commands you run, never its contents.

Once the human confirms the file is in place:

```
gcloud auth activate-service-account --key-file=/tmp/grain-bootstrap-gcp.json
```

## 3. Point Terraform at local state on the data volume

`terraform/gcp` ships a GCS backend by default
(`backend.hcl.example`), meant for an operator applying the whole module
from outside the VM before it exists. That doesn't fit here: the VM
already exists, and state should live on `$GRAIN_DATA_DIR` (the mounted
data disk, `/var/lib/grain/data` unless this deployment set it elsewhere) so
it survives a VM redeploy without depending on a GCS bucket. Use
`backend-local.hcl.example` instead:

```
mkdir -p $GRAIN_DATA_DIR/terraform-state
cd <path to a checkout of this repo>/terraform/gcp
cp backend-local.hcl.example backend-local.hcl
# edit backend-local.hcl: path = "<GRAIN_DATA_DIR>/terraform-state/gcp.tfstate"
terraform init -backend-config=backend-local.hcl
```

If a previous bootstrap playbook already ran `terraform
init` against this same state file in this same working directory, skip
straight to `terraform plan` below -- one working directory and one
state file cover every flow that touches `terraform/gcp`, not one
each.

## 4. Fill in tfvars, targeting only the identity resources

Copy `example.tfvars` to `terraform.tfvars` and fill in every
`CHANGE-ME`, matching the hand-created VM's real `project_id`, `zone`,
and `name_prefix` from step 1. Variables that only govern resources this
playbook never targets (`ui_domain`, `iap_members`, `test_repos`, ...)
still need *some* valid value to pass validation -- placeholders are
fine there, but the identity-related ones must be real.

Then plan, restricted to exactly the resources `iam.tf` declares:

```
terraform plan -backend-config=backend-local.hcl \
  -target=google_service_account.host \
  -target=google_service_account.agent \
  -target=google_service_account.minter \
  -target=google_project_iam_member.host \
  -target=google_project_iam_member.agent_compute \
  -target=google_project_iam_member.agent_iap_tunnel \
  -target=google_project_iam_member.agent_gke \
  -target=google_project_iam_member.agent_cloud_sql \
  -target=google_service_account_iam_member.agent_acts_as_self \
  -target=google_service_account_iam_member.minter_manages_agent_keys \
  -target=google_service_account_iam_member.deployer_manages_minter_keys \
  -target=google_project_iam_member.minter_gemini_keys \
  -target=google_project_service.compute \
  -target=google_project_service.container \
  -target=google_project_service.artifactregistry \
  -target=google_project_service.generativelanguage \
  -target=google_project_service.apikeys \
  -target=google_project_service.sqladmin \
  -target=google_project_organization_policy.restrict_public_endpoints \
  -target=google_project_organization_policy.no_external_ip \
  -target=google_project_organization_policy.restrict_public_ip
```

(Check `iam.tf` and `network.tf`/`variables.tf` for the exact resource
addresses this version of the module declares -- names drift; the point
is every `-target` names a service account, an IAM member, an API
enablement, or an organization policy constraint, never
`google_compute_instance.host`, its disks, or anything network-shaped.)

Watch the plan's own count rather than trusting the list above:
`-target` with an address the module does not declare is a *warning*,
not an error, so a stale name here plans nothing and applies nothing
while still exiting 0 -- which looks exactly like "already up to date."
If the plan says "No changes" on a project that has never been
bootstrapped, suspect the addresses before believing it. A `-target`
naming a `for_each` resource (`agent_compute`, `agent_gke`) correctly
covers every instance of it; the `count = 0` ones (whichever of
`enable_gemini_key`/`agent_can_manage_*` are off) are simply absent, and
warn the same harmless way.

`google_service_account_iam_member.agent_acts_as_self` is easy to leave
out and does not fail visibly until much later: without it the agent
account holds instance and cluster admin but cannot attach any service
account to what it creates, so a task's first `gcloud compute instances
create` fails on a permission this plan looked like it had granted. See
`terraform/gcp/README.md`, "Creating a VM as the agent."

`google_project_organization_policy.restrict_public_endpoints`,
`google_project_organization_policy.no_external_ip`, and
`google_project_organization_policy.restrict_public_ip` (gated by
`enforce_private_infrastructure_policies`, on by default) enforce
private infrastructure on the project -- restricting GKE cluster public
endpoints, denying external IP allocation on GCE VM instances and
GKE node pools, and restricting public IP on Cloud SQL instances.

**Stop and ask the human before applying** if the plan proposes
creating, destroying, or modifying anything outside that set -- most
likely `google_compute_instance.host` or `google_compute_disk.data`,
because they exist for real but not yet in this fresh state file. That
means either `name_prefix`/`zone` don't match the real VM (fix tfvars
and re-plan) or the plan needs narrower `-target` flags.

Once the plan only touches identity/IAM resources, apply it with the
same `-target` flags:

```
terraform apply -backend-config=backend-local.hcl <same -target flags as above>
```

Every step here is get-or-create on GCP's side (the underlying resources
are the same ones `terraform/gcp/iam.tf` builds for v1), so re-running
this after fixing a permission error is safe.

## 5. If the master credential can't do everything

Some grants (project-level IAM bindings, in particular) need
`roles/owner`, not just `roles/editor` -- if `terraform apply` fails
with a permission error partway through, that's expected for a narrower
master credential. Rather than hunting for a broader one, point the
human at `grain setup gcp` (`cmd/grain/setup.go`,
`pkg/gcpsetup`): it does the same identity/IAM work through the GCP
SDK directly (no Terraform, no state file) and is built to run
partially -- it records whatever it can't do as a manual `gcloud`
command and continues past it. Running it after this Terraform apply is
safe (`get-or-create` in both cases); it's a legitimate fallback for the
specific resource this credential can't reach, not a replacement for
this playbook.

## 6. Wire the result into this deployment

Read the resulting emails back out of state:

```
terraform output agent_service_account
terraform output minter_service_account
```

Both emails are settings this deployment stores, so **where they go
depends on whether it has ever run**. The `grain_config` row is the
record (bwsalmon/agents#320): `-gcp-project` /
`-gcp-agent-service-account`, and the `GRAIN_GCP_PROJECT` /
`GRAIN_GCP_SERVICE_ACCOUNT_EMAIL` environment variables
`scripts/setup.sh` turns into them, seed that row on a database with no
row yet and are **discarded on every start after the first** -- so
telling the human to set an environment variable and restart is, on the
deployment they are almost certainly standing in front of, telling them
to do nothing at all.

On a deployment that is already running -- this one, if it is serving
the chat you are having -- write them where they are read from instead:

```
grain settings -gcp-project <project> -gcp-agent-service-account <agent email>
```

or Settings -> Capabilities in the UI, which has a field for each. Either
way the running daemon adopts them on its next reconcile tick, without a
restart, and the capability's badge on that tab is what says it has: it
reads **Ready** only once this deployment is *running* the configuration,
not merely storing it, so a badge that stays "saved but not yet in
effect" past a poll interval means the reconcile loop has stopped and the
daemon needs restarting.

The minter account still needs a key seeded into this deployment's own
secret store before `gcp-key` can mint anything --
`GRAIN_GCP_SERVICE_ACCOUNT_KEY_FILE`, `grain secrets set gcp-key-minter
key.json`, or the `gcp-key-minter` field under either capability on that
same Settings tab. That key is this deployment's own standing
credential, not the master credential from step 2, and is expected to
persist (unlike the master credential). Have the human press "Test
credential" beside the capability once it is in: it authenticates as
that key and lists the agent account's keys, which is the difference
between a secret that is set and one GCP still accepts.

## 7. Drop the master credential

Whether or not every step above succeeded:

```
gcloud auth revoke <the service account email you activated in step 2>
shred -u /tmp/grain-bootstrap-gcp.json 2>/dev/null || rm -f /tmp/grain-bootstrap-gcp.json
```

Confirm with the human that the key file no longer exists on the host
before you consider this playbook finished. Only `backend-local.hcl`'s
state file (on the data volume) and the narrow service-account emails
should outlive this conversation -- never the master credential itself.
