# Post-Merge Security Review of Six Skipped Trust-Boundary Reviews (2026-11-10)

- **Date:** 2026-11-10 (UTC)
- **Auditor:** grain [Task #3905](/tasks/3905) (agent)
- **Bases audited:**
  - `ai-on-gke/grain` `origin/main` at `73a9d5b9` (merge of #1053, 2026-11-10 01:01 UTC)
  - `experimental/grain` `origin/main` at `101381a` (merge of #144, 2026-11-10 01:11 UTC)
- **Why:** Between 2026-11-08 and 2026-11-10, pressing Submit merged trust-boundary changes before their automated `system:security-review` tasks finished, causing `closeSecurityReviewOnSettle` (`pkg/orchestrator/securityhold.go:755`) to close each review as *"Closing this review unfinished"*. (For `#3827`, as documented in §5 below, a multi-PR `LinkFixes` state bug in `OpenPullRequestLinks` triggered the same unfinished-close path on a reopened task's second PR.)
- **Method:** Because this run holds no `api:tasks:read` capability (`task://<id>/record.md` returns `forbidden` for other tasks), each skipped review was matched from git history, `refs/pull/<n>/head`, commit trailers (`Origin:` / `Filed-by:`), and remote task branches on both `ai-on-gke/grain` and `experimental/grain`. Every change was reviewed both **as it merged against its base** (`<merge>^1..<merge>`) and **as `main` stands now**, against the built-in security review brief (`pkg/model/security_review_template.go`), the category checklists in `pkg/trustboundary/trustboundary.go` (`trust-boundary/v27`), and `.grain/triggers/*.yml`. Note: per `TestCoreSaysNoGoogleTokens` (`tests/hygiene/boundary_test.go`), internal host and project names from the edition repository are written with neutral placeholders (`<edition-forge>/experimental/grain`, `<edition-project>`, `<edition-ar-writer>`).

---

## Summary of the Six Reviews

| Skipped Review | Reviewed Task & PR | Repository | Merged SHA (Base) | Status on `main` | Verdict |
|---|---|---|---|---|---|
| **[Task #3894](/tasks/3894)** *(High)* | [Task #3849](/tasks/3849) / PR #144 (`2d4dd16c`) | `experimental/grain` | `101381a` (`ff2f3f5`) | Current `HEAD` on `main` | **4 findings** (2 Medium, 2 Low) |
| **[Task #3735](/tasks/3735)** *(High)* | [Task #3685](/tasks/3685) / PR #987 (`129a3436`) | `ai-on-gke/grain` | `0851a007` (`5de91b1a`) | Extended by #1017 (`acbbf35e`) & #944 (`4de4df7f`) | **3 findings** on `main` (3 Medium) |
| **[Task #3737](/tasks/3737)** | [Task #3625](/tasks/3625) / PR #980 (`6edb2848`) | `ai-on-gke/grain` | `0d6a5329` (`a59d1e4c`) | Extended to `trust-boundary/v27` by #1007 (`69958670`) | **1 finding** on `main` (Low) |
| **[Task #3757](/tasks/3757)** | [Task #3716](/tasks/3716) / PR #1008 (`4385342e`) | `ai-on-gke/grain` | `d25389a2` (`9ef58028`) | Active on `main` | **Clean** (0 findings) |
| **[Task #3827](/tasks/3827)** | [Task #3546](/tasks/3546) / PR #954 (`5b7319ce`) & unmerged PR #1023 (`0a9473c7`) | `ai-on-gke/grain` | `8893b413` (`b3149bb1`); PR #1023 unmerged | `release.conf` updated by #1045 (`4dc9b5d8`); PR #1023 still open | **PR diffs clean** (accepted policy in #954); **1 Medium finding** in orchestrator/store (`OpenPullRequestLinks`) |
| **[Task #3823](/tasks/3823)** | [Task #3820](/tasks/3820) / PR #138 (`81728cd`) | `experimental/grain` | `4303576` (`630dfa9`) | Superseded by #140 (`523c9ac`) & #141 (`644fb8c`, core `v3.4.0`) | **1 finding** on `main` (Medium: `grain-plc/go.mod` unsynced, missing `c820cd52`) |

One focused follow-up task is proposed via `propose_task` for each of the **10 confirmed findings** (5 against `<edition-forge>/experimental/grain`, 5 against `ai-on-gke/grain`).

---

## 1. [Task #3894](/tasks/3894) → `experimental/grain` [Task #3849](/tasks/3849) / PR #144 (**High Priority**)

### What merged
- **Title:** *"Ship Google-edition images via Artifact Registry, pushed by a grain task (#144)"*
- **Commits:** Branch `grain/098073e4/task-3849-remove-release-asset` at `2d4dd16c` (`7f693e5` + merge `2d4dd16c`), squash-merged to `experimental/grain` `main` as `101381a` against base `ff2f3f5` on `2026-11-10 01:11:39 UTC` (`git diff 2d4dd16c 101381a` is empty). `101381a` is still `HEAD` of `experimental/grain` `main` (32 files changed, +1,269 / -5,372).
- **Summary of change:** Removes contract-v2 release-asset tarball/manifest publishing (`docs/release-assets.md`, `scripts/upgrade_release_assets_test.go`, and the release-asset paths in `scripts/release-images.sh`, `scripts/setup.sh`, and `installtrack/installtrack.go`). Returns Google-edition daemon, guest, and k8s-sandbox images to Artifact Registry (`us-docker.pkg.dev/<edition-project>/grain/{grain,guest,k8s-sandbox}`). Because the edition forge's Actions runners hold no GCP credential, `.github/workflows/release.yml` builds and qualifies only the daemon image and moves `sha-<12hex>`, `channel-<branch>`, `vX.Y.Z`, and `latest` git tags; two new `git_ref` schedules (`.grain/schedules/publish-google-images.yml` on `refs/tags/channel-main` and `.grain/schedules/publish-google-images-latest.yml` on `refs/tags/latest`, both `overlap: skip`) file `.grain/templates/publish-google-images.yml` (`priority: 9`, `network: open`, `auto-merge: false`, `nothing-needed: close`, granting `gcp#gcp-key={"permission_set":"<edition-ar-writer>"}`) to build in a detached worktree and push images to Artifact Registry. `scripts/cloudtop-install.sh` mints an OAuth2 access token from the installing user's `gcloud` login and passes `GRAIN_IMAGE_PULL_TOKEN` to `scripts/setup.sh` so root can pull from Artifact Registry. `installtrack.UpgradeDefaults` switches to `upgrade.ImageSourceRegistry` with `Fallback: "none"` and adds `grain-google/grain` to `MovesFrom`.

### Trust-boundary surfaces touched
Classified against `trustboundary.Classify` (`trust-boundary/v27`):
- **`release-machinery`** (3 files): `scripts/release-images.sh`, `.github/workflows/release.yml`, `.github/workflows/promote.yml`
- **`github-workflows`** (2 files): `.github/workflows/edition-qualification.yml`, `.github/workflows/plc-release.yml`
- **`grain-config`** (5 files): `.grain/templates/publish-google-images.yml`, `.grain/schedules/publish-google-images.yml`, `.grain/schedules/publish-google-images-latest.yml`, `.grain/templates/sync-oss-candidate.yml`, `.grain/templates/sync-oss-release.yml`
- **`sandbox-images`** (1 file): `Dockerfile`
- **`setup-scripts`** (1 file): `scripts/setup.sh`
- **Edition trust-boundary surfaces not matched by core `trustboundary.Classify`:** `installtrack/installtrack.go`, `scripts/cloudtop-install.sh`, `boundary_test.go`, `scripts/sync-oss-release.sh`, `scripts/edition-qualification-upgrade.sh`, `scripts/edition-qualification-cloudtop.sh` (see Finding 1.2).

### Findings

#### Finding 1.1 (Medium) — `scripts/release-images.sh` pulls unpinned seed images from a retired external GHCR namespace into Artifact Registry and stamps sandbox images by mutable tag instead of `@sha256` digest
- **Where:** `experimental/grain` `scripts/release-images.sh:384-396, 405-456, 473-476`
- **Details:**
  1. `seed_root` defaults to `ghcr.io/<retired-owner>/grain` (`RELEASE_IMAGE_SEED_ROOT`, line 394 — the retired personal GitHub Container Registry namespace predating `ai-on-gke/grain`). Because `publish-google-images` runs in a fresh ephemeral task sandbox VM on every firing (`network: open`, holding `gcp#gcp-key` with `roles/artifactregistry.writer`), local Docker cache (`$ref_root/guest:inputs-$guest_key`) is always cold. `resolve_guest` (`lines 410-427`) immediately executes `docker pull -q ghcr.io/<retired-owner>/grain/guest:inputs-$guest_key` by mutable tag with no digest pin and no signature verification; on a hit, it **skips `Granulefile` build entirely**, tags that external image directly as `us-docker.pkg.dev/<edition-project>/grain/guest:sha-<12hex>`, signs it when `RELEASE_IMAGE_SIGNING_KEY` is set, and pushes it to Artifact Registry as the official Google-edition VM guest image. `resolve_k8s_sandbox` (`lines 441-455`) similarly pulls `k8s-sandbox-base:$key` from `$seed_root`.
  2. Unlike core `ai-on-gke/grain/scripts/release-images.sh` (which pushes `guest` and `k8s-sandbox` first and stamps `grain` with their immutable `guest:sha-<short>@sha256:<digest>` refs), `experimental/grain/scripts/release-images.sh` builds `grain` before pushing the sandbox images (`lines 384-388, 473-476, 866-885`), stamping `SANDBOX_IMAGE` and `K8S_SANDBOX_IMAGE` only by mutable `:sha-<12hex>` tag. Any subsequent overwrite of a mutable `:sha-<12hex>` tag in Artifact Registry alters what already-published daemon images execute without changing the daemon image's digest.
- **Fix proposed:** Default `seed_root` to the edition's own Artifact Registry (`$registry_root`) rather than the retired external GHCR namespace, and push `guest` and `k8s-sandbox` (or resolve their registry digests) before `build_daemon_image` so `grain` is stamped with immutable `@sha256:<digest>` sandbox image references.

#### Finding 1.2 (Medium) — `experimental/grain` lacks `.grain/triggers/`, leaving `.grain/templates/publish-google-images.yml` (`gcp-key` + `network: open`) without `human: required` and edition security surfaces unclassified
- **Where:** `experimental/grain` `.grain/` (no `.grain/triggers/` directory) vs `ai-on-gke/grain/pkg/trustboundary/trustboundary.go:539-543`
- **Details:**
  1. In core's `pkg/trustboundary/trustboundary.go`, `grain-config` (`.grain/`) has `RequiresHuman: false`; in `ai-on-gke/grain`, `.grain/**` requires a human's Submit only because `ai-on-gke/grain` carries `.grain/triggers/grain-declarations.yml` (`paths: [".grain/**"]`, `gate: true`, `human: required`, `sensitive: true`). `experimental/grain` has **no `.grain/triggers/` directory at all** (and `publish-google-images.yml` declares no `push:` set, so `TemplatePush` does not apply). As a result, a pull request on `experimental/grain` that alters `.grain/templates/publish-google-images.yml` (modifying the prompt instructions executed alongside the `roles/artifactregistry.writer` `gcp-key` and `network: open`) or `.grain/schedules/publish-google-images*.yml` is classified solely under `grain-config` (`RequiresHuman: false`) and **re-enables auto-merge automatically after an agent security review without a human's Submit**, syncing directly into the live daemon once `declarations: true` imports `main`.
  2. Furthermore, because core's `trustboundary.Categories` names core paths (`pkg/access/`, `pkg/ui/`, `pkg/upgrade/`), edition-specific security-critical paths in `experimental/grain` — including `installtrack/` (where PR #144 changed `DefaultGoogleGrainRepository` and `UpgradeDefaults`), `scripts/cloudtop-install.sh` (mints user `gcloud` tokens and invokes `sudo setup.sh`), `boundary_test.go`, `scripts/sync-oss-*.sh`, `scripts/edition-qualification*.sh`, `uitrust/` (workstation/IAP auth verifiers), `corpwebauth/`, `corpauth/`, `corpresearch/`, `registrygoogle/`, and `grain-plc/googleplc/` — match **zero** trust-boundary categories when edited without a core-named path, allowing auto-merge with neither a security review nor a human gate.
- **Fix proposed:** Add `.grain/triggers/grain-declarations.yml` (`paths: [".grain/**"]`, `human: required`, `sensitive: true`), `.grain/triggers/release-promotion.yml` (`human: required` on `scripts/sync-oss-release.sh`, `scripts/edition-qualification.sh`, `.github/workflows/promote.yml`), and `.grain/triggers/edition-trust-boundary.yml` covering `uitrust/**`, `corpwebauth/**`, `corpauth/**`, `corpresearch/**`, `registrygoogle/**`, `installtrack/**`, `grain-plc/googleplc/**`, `cmd/**`, `boundary_test.go`, and `scripts/**` in `experimental/grain`, pinned by `boundary_test.go`.

#### Finding 1.3 (Low) — `cloudtop-install.sh` + `setup.sh` persist the installing user's personal `cloud-platform` OAuth2 access token in `/root/.docker/config.json`
- **Where:** `experimental/grain` `scripts/cloudtop-install.sh:1069-1087` and `scripts/setup.sh:4370-4395`
- **Details:** PR #144 added `resolve_image_pull_credentials()` to `scripts/cloudtop-install.sh`, which runs `gcloud auth print-access-token` as the installing developer (minting a full `cloud-platform`-scoped OAuth2 bearer token valid across every GCP project that user can access) and passes it across `sudo` via `GRAIN_IMAGE_PULL_TOKEN`. While `CLOUDTOP_TMP_ENV` is `0600` and immediately unlinked before `exec`, `setup.sh`'s `registry_login()` executes `docker login -u oauth2accesstoken --password-stdin https://us-docker.pkg.dev` as `root` with default `HOME=/root` and never runs `docker logout` or scopes `DOCKER_CONFIG` to a temporary directory. On a clean host where `root` has no Docker credential helper configured, Docker writes `base64("oauth2accesstoken:<user-token>")` unencrypted to `/root/.docker/config.json` and leaves it on disk after `setup.sh` exits (contrast core's `scripts/configure-dockerhub-auth.sh:74-88`, which scopes `docker login` to a `0700` `mktemp -d` `DOCKER_CONFIG` removed by an `EXIT INT TERM` trap).
- **Fix proposed:** Scope `setup.sh`'s `registry_login` (and image pulls using `GRAIN_IMAGE_PULL_TOKEN` or minted `gcloud` tokens) to an ephemeral `0700` `DOCKER_CONFIG` directory cleaned up by `setup.sh`'s `EXIT` trap so the installing user's `cloud-platform` token is never persisted in `/root/.docker/config.json`.

#### Finding 1.4 (Low) — `publish_commit` skips rebuilding with `RELEASE=vX.Y.Z` when `:sha-<12hex>` was already published via `channel-main`, re-tagging a `-dev`-stamped binary onto `:vX.Y.Z` and `:latest`
- **Where:** `experimental/grain` `scripts/release-images.sh:866-895`
- **Details:** PR #144's commit message and template contract state: *"A commit a `vX.Y.Z` names is stamped `RELEASE=vX.Y.Z`."* However, on `main`, `release.yml` moves `channel-main` immediately (`tag` phase), firing `publish-google-images` before upstream promotes `vX.Y.Z` (the fast path from PR #139) or while `sync-tags` awaits qualification. That first `publish` run builds and pushes `grain:sha-<12hex>` stamped with `vX.Y.0-dev.N+<sha7>`. When `sync-tags` later tags that same commit `vX.Y.Z` and `latest`, `publish_commit` checks only `if published "${ref_of[$role]}"` (`grain:sha-<12hex>`), sees it already exists, skips `build_images` and `check_release_stamp "$image" "$RELEASE"`, and re-tags the `-dev` image onto `grain:vX.Y.Z` and `grain:latest` (unlike core `scripts/release-images.sh` `promote`, which checks `org.opencontainers.image.version` on `grain:vX.Y.Z` and rebuilds the daemon binary once with `RELEASE=vX.Y.Z`).
- **Fix proposed:** When `publish_commit` runs with a non-empty `RELEASE=vX.Y.Z` (a commit tagged `vX.Y.Z`), verify that the image being tagged `:vX.Y.Z` / `:latest` actually carries `org.opencontainers.image.version == $RELEASE` (rebuilding the daemon image stamped with `RELEASE=$RELEASE` for `:vX.Y.Z` and `:latest` when `:sha-<12hex>` carries a `-dev` stamp, matching core `release-images.sh`).

### Examined and found sound in PR #144
- **Workflow permissions & secrets:** `plc-release.yml` narrowed `permissions: contents: write` to `contents: read` and dropped `GHOP_TOKEN`/`GH_TOKEN`; `release.yml` and `promote.yml` keep minimal `contents: write`, `statuses: write/read`, `checks: read` and touch no container registry.
- **Forward-only git tag movement & qualification gate:** `tag_may_move` fails closed on fetch/API errors or non-fast-forward history; `sync-tags` restricts release tagging to `main` and `release/vX.Y.Z`, verifies core's upstream `vX.Y.Z` and `latest` tags, and enforces `edition_qualified`.
- **Docker Hub confusion guard preserved:** Both `scripts/setup.sh` (`is_local_only_ref`) and `installtrack/installtrack.go` (`ReleaseAssetGoogleGrainRepository = "grain-google/grain"` in `MovesFrom`) continue refusing to `docker pull` or `docker login` against registry-less `grain-google/*` names.
- **`Dockerfile`:** Only comment and `ARG RELEASE` documentation updates; no new third-party downloads or elevated privileges.

---

## 2. [Task #3735](/tasks/3735) → `ai-on-gke/grain` [Task #3685](/tasks/3685) / PR #987 (**High Priority**)

### What merged & status on `main`
- **Title:** *"fix(security): hold limited roles' approve and run-now to tasks:admin and the role's ceiling"*
- **Merged:** `0851a007` (`129a3436` / `6875630e` against base `5de91b1a`, `2026-11-09 03:29:19 UTC`).
- **Subsequent changes on `main`:** Extended (not reverted) by PR #1017 (`acbbf35e` / `48cb44f2`, [Task #3759](/tasks/3759), *"fix(security): hold limited roles' approve and run-now to the task's review template"*, closing Finding 1 of `docs/security/audits/2026-11-09.md`), updated for canonical grant references in PR #944 (`4de4df7f`), and kept intact across PR #1029 (`04f064bc`). Both `0851a007` and `main` (`73a9d5b9`) were reviewed.

### Trust-boundary surfaces touched
- **`auth-routes`** (2 files + tests): `pkg/ui/access_roles.go`, `pkg/ui/agent_api_gate.go` (`pkg/ui/access_roles_test.go`).

### Findings (on `main` now)

#### Finding 2.1 (Medium) — Limited roles (and external `gma_` tokens on filing) bypass `TemplatePush` (`declaredPushOf`), allowing filing and approval of tasks with declared tag/branch push privileges
- **Where:** `ai-on-gke/grain` `pkg/ui/access_roles.go:814-919, 947-1007` and `pkg/ui/agent_api_gate.go:2159-2210`
- **Details:** A repository-declared template's `push:` list (`TemplatePush`, `pkg/trustboundary/trustboundary.go:699-703`, `RequiresHuman: true`) grants every run of a task carrying `task.TemplateID` direct git-proxy push rights to refs beyond its own task branch (`pkg/gitproxy/refupdates.go:474-527`), such as `refs/tags/v*` and `refs/tags/latest` in `.grain/templates/release-train.yml` or `refs/heads/release/v*` in `sync-oss-release.yml`. For that reason, `proposalPersonHolds` (`pkg/ui/agent_api_gate.go:1083-1090`) explicitly holds any task whose `TemplateID` has `declaredPushOf(decl) != nil` as requiring an unlimited person's approval, right alongside `AutoMerge`.
  However, **neither `roleApprovalWithin` (`pkg/ui/access_roles.go:947-1007`), nor `checkFilingTemplate` (`pkg/ui/agent_api_gate.go:2159-2210`), nor `roleStandingTargetEscalation` on `PATCH /api/schedules/{id}` and `POST /api/schedules/{id}/run` (`pkg/ui/access_roles.go:857-919`) checks `declaredPushOf`**. Worse, `POST /api/tasks` allows passing `templateId` together with an caller-chosen `description`/`body` while still recording `task.TemplateID`: a limited verified-identity role holding `tasks:write` + `tasks:admin` on a repository (which is explicitly forbidden `autoMerge` and `Submit` so it cannot land unreviewed changes on protected refs) — or an external `gma_` token holding `tasks:write` + `tasks:admin` filing an approved task via `POST /api/tasks` — can file or approve a task with `templateId` set to `release-train` and a custom prompt body, acquiring direct `git push` access to `refs/tags/v*` and `refs/tags/latest` without opening a pull request or passing a human gate.
- **Fix proposed:** Refuse `declaredPushOf(decl) != nil` for limited roles (requiring an unlimited `grain/operator` role, just like `autoMerge`) and for external/in-sandbox agents across `roleApprovalWithin`, `checkFilingTemplate`, and `roleStandingTargetEscalation` (`PATCH /api/schedules/{id}`, `POST /api/schedules/{id}/run`, `PATCH /api/templates/{id}`), and refuse overriding `body`/`description` when filing from a template that carries a `declaredPush` set.

#### Finding 2.2 (Medium) — `externalApprovalRail` does not enforce the external `gma_` credential's own grant, repository, `networkAccess`, or `reviewTemplateId` ceilings on `approve` and unapproved `run-now`
- **Where:** `ai-on-gke/grain` `pkg/ui/agent_api_gate.go:862-935, 1080-1135, 2732-2744`
- **Details:**
  1. PR #987 added `roleApprovalWithin` so a limited verified-identity role cannot approve a stored task it could not have filed approved on `POST /api/tasks`. No equivalent ceiling check exists in `externalApprovalRail` / `externalApprovalRefusal` (`pkg/ui/agent_api_gate.go:862-935`) for an external `gma_` bearer credential (`Authorization: Bearer gma_...`) calling `POST /api/tasks/{id}/approve` or `POST /api/tasks/{id}/run-now` on an unapproved task. When `proposer == nil` (any unapproved task filed by a user, UI form, or another external credential), `proposalPersonHolds` (`lines 1100-1109`) checks only `HoldRequested`, `ConfigRepoProposalHold`, `ChangeTargetNeedsPersonApproval`, `requires_person` / `IsPersonOnlyGrant`, `declaredPushOf`, `AutoMerge`, `KeepRunning`, and admission above standard — and then returns `nil`. It **never compares the stored task against the approving `gma_` token's own `held` grants and `set` repository limits**:
     - A `gma_` credential scoped strictly to `acme/widgets` (`tasks:read, tasks:write, tasks:admin, dispatch:write` on `acme/widgets` only) is refused 403 on `POST /api/tasks` if it tries to set `writes: ["acme/gadgets"]`, `reads: ["acme/gadgets"]`, `networkAccess: "open"`, non-person-only capabilities it does not hold (e.g. `api:tasks:write` across all repos, `github#read-issues`), or `reviewTemplateId: "heavy-review"`.
     - Yet that **same `gma_` credential** succeeds (`200 OK`) when calling `POST /api/tasks/{id}/approve` or `POST /api/tasks/{id}/run-now` on a stored unapproved `acme/widgets` task carrying every one of those out-of-scope writes, reads, capabilities, `networkAccess: "open"`, and `reviewTemplateId`.
  2. In `authorizeExternalAgentCall` (`pkg/ui/agent_api_gate.go:2732-2744`), PR #987 narrowed `set = set.Intersect(role.set).Without(role.deny)` when a `gma_` token is presented inside a limited verified session, but left `held` (the raw capability slice passed to `checkExternalTokenRotationEscalation` at line 2826 and `externalAgentFilingEscalation` at line 2829) un-narrowed by `role`.
- **Fix proposed:** Enforce the approving `gma_` credential's own ceiling in `externalApprovalRail` / `Client.Approve` / `Client.RunNow` (mirroring `externalAgentFilingEscalation` over `task.Grants`, `task.Writes`, `task.Reads`, `task.NetworkAccess`, `task.Admission`, and `task.ReviewTemplateID`), and narrow `held` alongside `set` when an outer limited `accessRole` is present in `authorizeExternalAgentCall`.

#### Finding 2.3 (Medium) — `POST /api/pull-requests/adopt` lets limited roles and external `gma_` tokens file approved `AutoMerge: true` tasks and approve existing unapproved tasks without `roleApprovalWithin` or `externalApprovalRail`
- **Where:** `ai-on-gke/grain` `pkg/ui/pull_requests_api.go:128-190` and `pkg/orchestrator/takeover_adopt.go:300-320, 389-394`
- **Details:**
  1. In `Client.pullRequestFiling` (`pkg/ui/pull_requests_api.go:128-140`), both a limited role holding `merge:write`, `tasks:write`, and `tasks:admin` on `repo` and an external `gma_` credential holding `merge:write`, `tasks:write`, and `tasks:admin` on `repo` (or `OwnerTrustedForTask`) receive `approved = true`. When they call `POST /api/pull-requests/adopt` on a collaborator's PR (`authorTrusted == true`), `AdoptPullRequest` (`pkg/orchestrator/takeover_adopt.go:315`) files the task already approved with **`AutoMerge: in.Approved && authorTrusted` (`true`)** — violating both the limited-role invariant (`"auto-merge requires an unlimited role (grain/operator)"`) and the hard agent rail (`errAgentAutoMerge`: agents and `gma_` tokens never create `AutoMerge: true` tasks).
  2. When a pull request already has an unapproved shepherd task in the store, `updateAdoptedShepherd` (`pkg/orchestrator/takeover_adopt.go:390-393`) executes `deps.Store.Approve(ctx, task.ID, in.Requester, now)` directly whenever `in.Approved` is true — completely bypassing `roleApprovalWithin` (approving a stored task carrying `AutoMerge`, ungranted capabilities, out-of-scope `Writes`/`Reads`, widened `NetworkAccess`, or `ReviewTemplateID`) and completely bypassing `externalApprovalRail` (allowing an external `gma_` token to self-approve a task it filed itself or approve a task held for a person, without even requiring `X-Grain-Acting-Reason`).
- **Fix proposed:** Set `AutoMerge` on adopted shepherd tasks only when the requester is an unlimited human principal (`accessRole == nil || accessRole.Full`, never an agent or limited role), and gate `updateAdoptedShepherd`'s approval of an existing unapproved task through `roleApprovalWithin` and `externalApprovalRail` (matching `Client.Approve`).

### Examined and found sound in PR #987 (and #1017)
- `POST /api/tasks/{id}/approve` and `POST /api/tasks/{id}/run-now` on already-approved tasks vs unapproved tasks: `dispatch:write` without `tasks:admin` still runs an already-approved task immediately without approving unapproved work.
- `PATCH /api/tasks/{id}` (`UpdateTaskRequest`) still carries no `approved` field and cannot approve a task.
- REST, `POST /api/mcp` (`grain_invoke`), and `POST /mcp` all run `authorizeRoleCall` / `authorizeMCPRoleCall` against the outer session's `accessRole`.

---

## 3. [Task #3737](/tasks/3737) → `ai-on-gke/grain` [Task #3625](/tasks/3625) / PR #980

### What merged & status on `main`
- **Title:** *"fix(security): classify the CLI installer and release helper scripts (trust-boundary/v26)"* (filed while `main` was at `v24`, bumped to `v26` on rebase over #966's `v25`).
- **Merged:** `0d6a5329` (`6edb2848` against base `a59d1e4c`, `2026-11-09 03:38:44 UTC`, 3 files changed, +75 / -1).
- **Subsequent changes on `main`:** Extended (not reverted) by PR #1007 (`69958670`, `trust-boundary/v27`, adding `pkg/hubenv/` to `auth-routes` and keeping `pkg/registry/` + `cmd/grain-registry/` alongside `pkg/hub/` + `cmd/grain-hub/` in `credential-proxy`). All five paths added by `0d6a5329` remain classified on `main`.

### Trust-boundary surfaces touched
- **`hold-policy`** (2 files): `pkg/trustboundary/trustboundary.go`, `pkg/trustboundary/trustboundary_test.go`
- **`path-triggers`** (1 file): `.grain/triggers/release-machinery.yml`

### Findings (on `main` now)

#### Finding 3.1 (Low) — Sibling guest-image build helpers (`scripts/granule/*`), patch-release script (`scripts/release-patch.sh`), GCE instance boot/bake scripts, `pkg/hostinit/`, and `pkg/installtrack/` remain unclassified
- **Where:** `ai-on-gke/grain` `pkg/trustboundary/trustboundary.go:460-585` and `.grain/triggers/release-machinery.yml`
- **Details:** PR #980 strictly widened the boundary (no paths removed or weakened, `.grain/triggers/release-machinery.yml` kept in lockstep with `release-machinery`), classifying `pkg/ui/cli_install.{sh,go}` under `setup-scripts` and `scripts/{buildvcs,configure-dockerhub-auth,release-image-repos}.sh` under `release-machinery`. Auditing the rest of `scripts/` and `pkg/` on `main` (`v27`) for the same invariant ("helpers the release builds and workflows run or source, setup scripts run as root, and code that picks upgrade images") shows six sibling surfaces still unclassified, so PRs modifying them alone still auto-merge with no security review:
  1. `scripts/granule/` (`scripts/granule/build-granule-image.sh` and `scripts/granule/guest-inputs.sh`), executed directly by `scripts/release-images.sh` to compute the guest cache key and build the runtime baked into every Granule VM guest image;
  2. `scripts/release-patch.sh`, which cuts `release/vX.Y.Z` branches and tags patch releases;
  3. `scripts/grain-instance-init.sh` and `scripts/bake-gce-release-image.sh`, which build and boot as `root` on every hosted Grain Hub GCE VM instance (`pkg/hub/reconcile`);
  4. `pkg/hostinit/`, which runs host initialization steps including `build-granule-image.sh`;
  5. `pkg/installtrack/`, which defines track image repositories and `MovesFrom` migration targets read by `pkg/upgrade`.
- **Fix proposed:** Classify `scripts/granule/` (`prefixes`) and `scripts/release-patch.sh` under `release-machinery` (and `.grain/triggers/release-machinery.yml`), `scripts/grain-instance-init.sh`, `scripts/bake-gce-release-image.sh`, and `pkg/hostinit/` under `setup-scripts` / `exec-sandbox`, and `pkg/installtrack/` under `upgrade-images` (bumping `trustboundary.Version` to `v28`).

---

## 4. [Task #3757](/tasks/3757) → `ai-on-gke/grain` [Task #3716](/tasks/3716) / PR #1008

### What merged & status on `main`
- **Title:** *"Walk through repository, merge queue, and release UI"* (commits: `docs: record the 2026-11-08 repository, merge queue and release walkthrough`, `fix(ui): escape # and ? in compare refs, report failed issue listings, and offer CI release grants`)
- **Merged:** `d25389a2` (`4385342e` against base `9ef58028`, `2026-11-09 07:01:14 UTC`, 15 files changed, +760 / -13). Active on `main`.

### Trust-boundary surfaces touched
- **`capabilities-secrets`** (2 files + tests): `pkg/model/capability_ref.go`, `pkg/model/ci_grant_rule.go` (`pkg/model/ci_grant_rule_test.go`)
- **UI/API adjacent files** (not classified, read in full): `pkg/ui/capability_grants.go`, `ui/src/components/RepoPage.jsx`, `ui/src/paths.js`, `ui/src/state.js`, `ui/e2e/*.spec.js`, `docs/test-runs/2026-11-09-repository-release-memory-ui.md`.

### Findings
**None (Clean).**

### Examined and found sound
- **`CapabilitySubject.AnyEvent` (`pkg/model/capability_ref.go:651`, `pkg/model/ci_grant_rule.go:248`, `pkg/ui/capability_grants.go:401`):** Every reference to `AnyEvent` across the repository was audited. `AnyEvent` is set solely in `handleListCapabilityMethods` (`GET /api/capability-methods?kind=ci&repo=...`), the read-only picker endpoint, and only when `kind == model.CapabilityRunCI`. In `CIGrantRuleRefusal`, the `s.Kind != CapabilityRunCI` guard (`line 235`) executes *before* `if !s.AnyEvent && !rule.Admits(...)` (`line 248`), so task and process runs can never pass `CIGrantRuleRefusal` even if `AnyEvent` were set, and `CapabilityPolicy.Permits` continues evaluating explicit policy `deny` rules regardless of `AnyEvent`. None of the three runtime CI capability enforcers (`pkg/daemon/ci_policy.go`, `pkg/daemon/ci_capability_pass.go`, `pkg/daemon/capabilities.go`) sets `AnyEvent`, so `pull_request`, `push`, and event-less CI runs remain strictly refused release-only capabilities at execution time.
- **Compare URL escaping (`ui/src/state.js`, `ui/src/paths.js`) and `RepoPage.jsx`:** `comparisonRefSegment` applies `encodeURIComponent(ref).replace(/%2F/gi, "/")`, preventing `#`, `?`, and `%` in branch names from truncating routes or injecting query/hash parameters. `RepoPage.jsx` renders error strings through React text interpolation with no HTML sink.

---

## 5. [Task #3827](/tasks/3827) → `ai-on-gke/grain` [Task #3546](/tasks/3546) (PR #954 & PR #1023)

### What merged, what is unmerged, and why `#3827` closed unfinished
- **Reviewed task:** [Task #3546](/tasks/3546), *"[RELEASE-TRAIN] Drive Grain v3.2.0 release train through to Docker Hub"*, working on branch `grain/098073e4/task-3546-release-train-drive-grain-v3-2`.
- **Two pull requests on the same task and branch:**
  1. **PR #954 (`5b7319ce` / `a53e4045`, merged to `main` as `8893b413` on `2026-11-08 22:04:55 UTC` against base `b3149bb1`):** *"fix(release): qualify rcs on Stage 1 alone while grain connects with a PAT"*, modifying `scripts/release.conf` (+7 / -1) to set `RELEASE_QUALIFICATION_APP=none`. Subsequently updated on `main` by PR #1045 (`4dc9b5d8` / `0b8c4644`, adding `RELEASE_MAX_PER_DAY=1` and `RELEASE_DAY_TZ=America/Los_Angeles` while keeping `RELEASE_QUALIFICATION_APP=none`).
  2. **PR #1023 (`0a9473c7` / `4c2f9388`, pushed `2026-11-09 07:44:37 UTC`, still open / NOT merged into `main`):** *"fix(release): write the GitHub Release when a person approves a pending release"*, modifying `.github/workflows/promote-release.yml` (+50 / -0).
- **Why `#3827` was closed as *"Closing this review unfinished"*:** Task `#3546` stayed open (or reopened) after PR #954 merged and later opened PR #1023 in the same repository, leaving **two** `LinkFixes` links (`ai-on-gke/grain#954` and `ai-on-gke/grain#1023`) on task `#3546`. When `holdForTrustBoundary` classified PR #1023 (`0a9473c7`) and filed security review `#3827`, the next `SyncPullRequests` cycle read both `LinkFixes` entries via `Store.OpenPullRequestLinks`, saw PR #954's `PrMerged` state, and executed `closeSecurityReviewOnSettle` — killing PR #1023's live security review `#3827` as *"Closing this review unfinished"* and closing task `#3546` while PR #1023 remained open on GitHub (Finding 5.1 below). Both PR #954 (`8893b413` + current `main`) and PR #1023 (`0a9473c7`) were reviewed.

### Trust-boundary surfaces touched
- **PR #954 (`8893b413`):** `release-promotion` (`RequiresHuman: true`, `scripts/release.conf`, also gated by `.grain/triggers/release-promotion.yml`).
- **PR #1023 (`0a9473c7`, unmerged):** `release-promotion` (`RequiresHuman: true`, `.github/workflows/promote-release.yml`, also gated by `.grain/triggers/release-promotion.yml`).

### Findings

#### Finding 5.1 (Medium) — `OpenPullRequestLinks` returns already-merged `LinkFixes` entries when a task opens a second PR in the same repository, killing the new PR's security review (`closeSecurityReviewOnSettle`) and dropping the new PR from the queue
- **Where:** `ai-on-gke/grain` `pkg/model/store.go:5613-5680` (`OpenPullRequestLinks`) and `pkg/orchestrator/sync.go:2022-2025` (`closeSecurityReviewOnSettle`, `recordPullRequestEvents`)
- **Details:** As documented in `pkg/model/store.go:5603-5612`, every reader of `OpenPullRequestLinks` reconciles each returned `LinkFixes` link against the task's **single** `task_observation` row, **single** `LinkSecurityReviewTask`, and **single** merge-queue entry. `PruneOffTargetPullRequestLinks` filters out stale `LinkFixes` links when a task moves to *another* repository, but nothing filters or replaces an already-merged/closed `LinkFixes` link (`#954`) when a standing, goal-driven, or reopened task opens a second pull request (`#1023`) in the **same** repository. Once the second run finishes:
  1. `holdForTrustBoundary` classifies the new PR (`#1023`) and files security review `#3827` (`LinkSecurityReviewTask = 3827`).
  2. On the next `SyncPullRequests` cycle, `OpenPullRequestLinks` returns both `#954` (`PrMerged`) and `#1023` (`PrOpen`).
  3. Processing `#954` sees `health == model.PrMerged`, calls `closeSecurityReviewOnSettle` — closing `#1023`'s unfinished security review `#3827` — and calls `recordPullRequestEvents` (`ClosedAt` + `PrMergedAt`), closing the task so `OpenPullRequestLinks` stops watching `#1023` altogether (leaving `#1023` open on GitHub without a completed security review and out of grain's merge/hold tracking).
- **Fix proposed:** When a task records a new open `LinkFixes` pull request in the same repository (or in `OpenPullRequestLinks` / `SyncPullRequests`), supersede or scope settled historical `LinkFixes` links so an earlier merged/closed PR on the same task can never settle the task's current observation row or close the current PR's `LinkSecurityReviewTask`.

### Examined and found sound in PR #954, current `scripts/release.conf`, and PR #1023
- **PR #954 (`8893b413`) & current `scripts/release.conf` (`0b8c4644`):** `RELEASE_QUALIFICATION_APP=none` does not raise `RELEASE_MAJOR` (`3`), widen workflow permissions, or expose secrets; as noted in `docs/security/audits/2026-11-09.md`, it is an explicit, documented operator policy choice (`human: required`, merged by the operator) while the deployment's forge credential is a PAT rather than a GitHub App (skipping Stage 2 `grain/release-qualification` and `grain/rc-hold` checks so Stage 1 `nightly.yml` alone qualifies release candidates). Subsequent `#1045` (`0b8c4644`) tightens release cadence to at most one release per Pacific calendar day (`RELEASE_MAX_PER_DAY=1`).
- **PR #1023 (`0a9473c7` / `4c2f9388`, unmerged `.github/workflows/promote-release.yml`):** Scoped inside the `assets` job (`needs: [approve]`, `if: inputs.decision == 'approve' || inputs.decision == ''`, after `verify-release-approval.sh` in `approve` has already verified the signed human approval record). Passes `RELEASE_VERSION` and `DRY_RUN` via `env:` (no `${{ ... }}` shell interpolation), re-validates `RELEASE_VERSION` against `^v[0-9]+\.[0-9]+\.[0-9]+$`, verifies `refs/tags/${RELEASE_VERSION}` exists, and passes `--verify-tag` to `gh release create`. (Note: PR #1037 / `f03c0d99` has since modified adjacent lines of `.github/workflows/promote-release.yml` on `main`, so PR #1023 should be rebased and re-classified once Finding 5.1 is fixed, or closed if superseded.)

---

## 6. [Task #3823](/tasks/3823) → `experimental/grain` [Task #3820](/tasks/3820) / PR #138

### What merged & status on `main`
- **Title:** *"chore(deps): sync github.com/ai-on-gke/grain to v3.3.0 (#138)"*
- **Merged:** Branch `grain/098073e4/task-3820-chore-deps-sync-github-com-ai` at `81728cd`, squash-merged to `experimental/grain` `main` as `4303576` against base `630dfa9` on `2026-11-09 19:04:29 UTC` (2 files changed: `go.mod`, `go.sum`, bumping `github.com/ai-on-gke/grain` from `468b48f7e283` to `545aa3f78f02`, core `v3.3.0`).
- **Superseded on `main`:** Superseded three hours later on `experimental/grain` `main` by PR #140 (`523c9ac`, [Task #3843](/tasks/3843)) and PR #141 (`644fb8c`, [Task #3844](/tasks/3844), an identical no-op merge over #140), which moved `go.mod` and `go.sum` to `v0.0.0-20261109190329-5ff76cf06248` (core `v3.4.0`). Both `#138` (`468b48f7e283..545aa3f78f02`) and the current `main` sync state (`545aa3f78f02..5ff76cf06248` + submodule `grain-plc/go.mod`) were reviewed.

### Trust-boundary surfaces touched
- `go.mod` and `go.sum` in `experimental/grain` (likely held under `!known` when `ListPullRequestFiles` failed transiently, or via review template; pulls in core changes across `auth-routes`, `capabilities-secrets`, `credential-proxy`, `exec-sandbox`, `hold-policy`, and `release-machinery` from `468b48f7e283..5ff76cf06248`).

### Findings (on `main` now)

#### Finding 6.1 (Medium) — `sync-oss-release.sh` and `sync-oss-candidate.sh` never update `grain-plc/go.mod`, leaving Google-edition `plc-studio` and `plc-chat-gateway` vulnerable to the `-grain-token` cross-host credential leak fixed in core `c820cd52`
- **Where:** `experimental/grain` `grain-plc/go.mod` and `scripts/sync-oss-release.sh` / `scripts/sync-oss-candidate.sh`
- **Details:** `experimental/grain` contains a separate Go module `grain-plc/go.mod` (`./grain-plc/cmd/plc-studio` and `./grain-plc/cmd/plc-chat-gateway`), which `boundary_test.go` (`TestGrainPLCMainsBuild`) builds and ships as Google-edition binaries. Neither `scripts/sync-oss-release.sh` (used by `#138` / `4303576` and `#140` / `523c9ac`) nor `scripts/sync-oss-candidate.sh` updates `grain-plc/go.mod`. On `experimental/grain` `main` today (`101381a`), while root `go.mod` is at core `v3.4.0` (`5ff76cf06248`), `grain-plc/go.mod` is still pinned to `github.com/ai-on-gke/grain/grain-plc v0.0.0-20261106104252-e31e7ad7a12a` and `github.com/ai-on-gke/grain v0.0.0-20261107013153-97fe0c3ca104` — **predating core commit `c820cd52` (`2026-11-08`, *"fix(grain-plc): send -grain-token only to the -grain-url daemon"*)**, where `-grain-token` (`$GRAIN_TOKEN`) was wired into `Config.BearerToken` and transmitted in `Authorization: Bearer` headers to the registry and to **every arbitrary host a registry entry resolved to**, rather than only `-grain-url`'s own `scheme://host`.
- **Fix proposed:** Update `grain-plc/go.mod` and `grain-plc/go.sum` in `experimental/grain` alongside root `go.mod` in `scripts/sync-oss-release.sh` and `scripts/sync-oss-candidate.sh` (syncing `grain-plc` to `v3.4.0` / `5ff76cf06248` now), and add an assertion in `boundary_test.go` that `grain-plc/go.mod`'s `github.com/ai-on-gke/grain` and `github.com/ai-on-gke/grain/grain-plc` pseudo-versions match root `go.mod`'s core commit so the two modules can never silently drift across security releases again.

### Examined and found sound in `#138` (`4303576`) and `#140`/`#141` (`644fb8c`)
- Root `go.mod` / `go.sum` changes in `4303576` and `644fb8c` touch no other dependencies (`third_party/granule` unchanged) and bring in core security fixes `#965`, `#966`, `#969`, `#980`, `#985`, `#986`, `#987`, `#1017`, `#1018`, `#1020`, and `#1022`. `go test -short .` in `experimental/grain` (`TestGoogleImportsOnlyExportedCore`, `TestRepoDeclarationsLoad`, `TestGrainPLCBoundaryRules`) passes cleanly against `5ff76cf06248`.

---

## Verification & Tests Run

| Repository & Command | Result |
|---|---|
| `ai-on-gke/grain`: `go test ./pkg/trustboundary/... ./tests/hygiene/...` | Pass |
| `experimental/grain`: `go test -short .` (`boundary_test.go`) | Pass |
