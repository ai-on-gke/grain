# Image signing

grain/task-1773, following #2386 and grain/task-1706.

## The gap this closes

An upgrade is pinned to the digest it pulled: `pinImage` on the image
path, the check pod's `imageID` on the Kubernetes path (`kubePin`). And
`TagForBranch` gives every branch its own tag. Together those answer
*which bytes* an upgrade runs, and they make sure the checks and the
restart all run the same bytes. They do not answer *whose bytes* they
are. Anyone who can push to the GHCR package, or who controls the
registry or a proxy in front of it, can still have their image
health-checked, and a health check runs the image. Then it gets booted.

A signature answers that question. The release signs each image's
digest with a key the registry never holds, and a deployment checks the
signature before it runs anything.

## Decisions

### Signed with a key, not keyless

Releases are signed with a **cosign key pair** (`cosign sign --key`).
They are not signed keylessly through Fulcio, for these reasons:

- **The publisher has no identity Fulcio would certify.** A Grain CI
  runner has no OIDC issuer of its own. The only token it could mint is
  from the deployment's GCP service account
  (`/home/debian/.gcp-service-account.json`). That is the deployment's
  identity, not the release job's: every sandbox granted that file
  shares it, so a certificate for it would say nothing about which job
  signed.
- **Keyless writes every signature to the public Rekor log.** That
  publishes each digest, and the repository path, whether or not the
  package is public. The images are signed with `--tlog-upload=false`,
  and verifiers pass `--insecure-ignore-tlog=true`. A key-based
  signature does not need the log to be meaningful; the log only adds
  evidence of *when* something was signed.
- **One scheme for every publisher keeps verification to one key.** A
  deployment needs a single public key, not a choice of identities per
  publisher.

### The key must be held more narrowly than the push credential

This is the decision the scheme depends on. If whoever can push to the
package can also sign, a signature proves nothing beyond the push. The
private key is therefore **not** placed beside `package-publish`.
`package-publish` is also given to `workflow_dispatch` runs, and
`.grain/workflows/image.yml`'s `guest-cache` runs that from branches.
The key belongs in a CI capability of its own, `image-signing`, which:

- places `/home/debian/.image-signing-key` (cosign's encrypted private
  key) and `/home/debian/.image-signing-password`;
- is honoured only on the trusted `release` event, which grain emits
  against a branch's new head after a merge. It is never honoured on
  `workflow_dispatch`, a push, or a pull request;
- is granted by an operator for bwsalmon/grain alone. The key itself is
  stored in grain's secret store, not in any repository.

The capability is `pkg/capability/imagesigning` (grain/task-1802). It
reads the secrets `image-signing/key` and `image-signing/password`, and
`docs/ci-capabilities.md` has the operator's setup.
`scripts/release-images.sh` reads the paths it places
(`IMAGE_SIGNING_KEY` and `IMAGE_SIGNING_PASSWORD_FILE` override them).
`.grain/workflows/release.yml`'s `images` job declares the capability as
`optional-capabilities: [image-signing]` (`docs/ci-capabilities.md`,
"Optional capabilities"): a granted key signs every release, and an ungranted
one leaves it unsigned rather than unpublished. It is deliberately not a
requirement: as one it stopped every release on a deployment older than the
provider (grain/task-2016). A run of the
script that finds no key -- that job before the grant, by hand, or from another
workflow -- publishes unsigned and warns about it. Setting `REQUIRE_IMAGE_SIGNING=true`
(or `1` or `yes`) turns that warning into a failure, before the build starts.

### Where images are signed

- **Grain CI** (`scripts/release-images.sh`): each of the three images
  is signed by digest right after its immutable `sha-<short>` push, and
  before any moving tag is pushed. That way no tag a deployment resolves
  ever names an unsigned image from this release. The two sandbox images
  (`guest`, `k8s-sandbox`) are pushed by the `build` phase, before the
  grain image is built: the grain image is stamped with them as
  `repo:sha-<short>@sha256:<digest>`, and the registry only assigns the
  digest on push (`push_pinned` reads it off the push's own output).
  The build then reads both stamps back out of the grain image
  (`grain sandbox-image`, `grain k8s-sandbox-image`) and fails if either
  is not the pinned reference. Nothing is signed until the release
  suites have passed: `publish_images` signs the two sandbox digests the
  build recorded, then pushes and signs the grain image. A release that
  fails its suites therefore leaves its sandbox `sha-` tags published but
  unsigned, and a verifying deployment refuses them. A signature covers a digest, not a tag, so the branch tag and
  the registry-side retags `promote` makes (`latest`, `vX.Y.Z`) carry the
  same signature, and nothing needs re-signing. cosign is installed from
  the Go module proxy at a pinned version (`COSIGN_VERSION`) when the
  sandbox does not already have it.
- **GitHub Actions** (`.github/workflows/build-artifacts.yml`,
  `publish-grain-image`): **does not sign.** That workflow runs on every
  branch push, so any secret in it can be read by anyone who can push a
  branch. `TestOnlyDefaultBranchLiveJobsHoldAModelCredential` keeps that
  invariant: build-artifacts.yml reads no secrets. The workflow has also
  been dormant since Actions began refusing this account's jobs on
  2026-09-17. Images it publishes are unsigned, so a verifying
  deployment refuses them. If the workflow is revived, there are two
  ways to sign. One is keyless signing with the workflow's own OIDC
  token (`id-token: write`), verified with
  `--certificate-identity=https://github.com/ai-on-gke/grain/.github/workflows/build-artifacts.yml@refs/heads/main`.
  The other is a job restricted to a protected `image-signing`
  environment. Either needs the secrets test above to be revisited
  deliberately.

### Where images are verified

`-upgrade-verify-cmd` (repeated, one argument each, like
`-upgrade-restart-cmd`) sets `ImageConfig.VerifyCmd` and
`KubeConfig.VerifyCmd`. On a real deployment it is:

```
cosign verify --key /etc/grain/cosign.pub --insecure-ignore-tlog=true
```

The upgrade plan gains a `signature` step:

- **Image path** (`stageImage`): after `pinImage` and before
  `healthCheckImage`. The verifier is handed `repo@sha256:...`, the
  pinned digest with the tag removed, and a non-zero exit refuses the
  upgrade with the ref file untouched. If the verifier prints cosign's
  JSON payloads, the digests in them must include the one it was asked
  about. The sandbox image the new build names is pulled, pinned with
  `DigestCmd`, and verified the same way, before the pull counts. A
  sandbox reference that is already pinned (what a release stamps) is
  pulled by its digest, and `pinImage` only confirms the host now holds
  exactly that digest; it never appends a second one.
- **Kubernetes path** (`stageKube`): nothing pulls the image before the
  check pod runs it, so the verifier is handed the *tag*. The digest
  comes from what it printed: cosign's JSON payloads, each carrying
  `critical.image.docker-manifest-digest`. The check pod and every pod
  after it are created from `tag@<that digest>`. A pod whose `imageID`
  reports a different digest is refused. A verifier that exits 0 without
  naming a digest is also refused, because the pod would otherwise pull
  whatever the tag named by then. The sandbox image is handled the same
  way before its own pod; a sandbox reference that is already pinned is
  verified by the digest it names, and its pod runs that reference
  unchanged.

`scripts/setup.sh` verifies the initial install when given
`GRAIN_IMAGE_VERIFY_KEY`, the path of a public key on the host, and it
needs `cosign` on the host's PATH. The grain image is verified by digest
after the pull, or against the local copy that the pull-failed fallback
uses. `GRAIN_IMAGE_REF` is then pinned to the verified digest, so the
ref file the unit boots from names those bytes rather than the tag. The
local Docker sandbox image is verified the same way when it is pulled or
found locally; for a pinned reference, the digest verified is the one
the reference names (`image_digest_ref`), not whichever digest of that
repository the host happens to list first.

The daemon's own upgrades are held to the same key (grain/task-1803).
setup.sh copies `GRAIN_IMAGE_VERIFY_KEY` to `/etc/grain/cosign.pub`,
which is root-owned with mode 0644, and passes
`-upgrade-verify-cmd=cosign -upgrade-verify-cmd=verify
-upgrade-verify-cmd=--key=/etc/grain/cosign.pub
-upgrade-verify-cmd=--insecure-ignore-tlog=true` in grain-daemon.service's
ExecStart whenever the Upgrade button is on. In container mode the key
is mounted read-only at the same path, and the verifier is the image's
own cosign: the `Dockerfile` copies `/ko-app/cosign` out of
`ghcr.io/sigstore/cosign/cosign`, pinned by digest (`COSIGN_IMAGE`). In
host mode the verifier is the host's cosign, which setup.sh has already
required. An image built before cosign was added cannot verify anything,
including the upgrade that would replace it. setup.sh warns when it is
about to run one of those with a key.

`deploy/kubernetes/grain.yaml` does the same with a ConfigMap,
`grain-image-verify-key`, mounted read-only at `/etc/grain`. The manifest
ships it holding a `CHANGEME` placeholder rather than a key. Until the
placeholder is replaced with the release's `cosign.pub`, every upgrade is
refused at its signature step, while the daemon itself runs normally. No
`--output` flag is passed, because the Kubernetes path reads the verified
digest out of cosign's default JSON.

### No verifier configured: warn, don't refuse

A deployment with no `-upgrade-verify-cmd` still upgrades. The
`signature` step is **skipped** and says why: "no signature verification
configured … running whatever the registry serves for this tag, whoever
published it". The daemon also logs a warning at startup. setup.sh with
no `GRAIN_IMAGE_VERIFY_KEY` does the same.

Refusing by default would strand every deployment that is installed
today. Nothing published before signing began carries a signature, and
nothing installed today has a key configured. The first build with this
change would refuse its own successor and every rollback. Configuring a
verifier is how a deployment opts into refusing, and once it is
configured a failure always refuses. Changing the default to refuse
belongs after the steps below, once every supported rollback target is
signed.

### Signatures in the registry

cosign stores a signature as a tag on the same package:
`sha256-<hex>.sig`. `cmd/ghcr-prune` classifies those tags as
`KindSignature` and keeps a signature exactly as long as it keeps the
version it signs. Without that rule, the signature on `latest` would
have been pruned after the 48-hour window, as an unrecognised tag, while
`latest` itself was kept forever.

## Known gaps

- **No published public key.** Nothing in this repository says which
  `cosign.pub` the releases are signed with. `docs/ci-capabilities.md`
  suggests `docs/cosign.pub`, but nothing has been committed there.
  Until a key is published, an operator has to obtain it from whoever
  holds the signing key before setting `GRAIN_IMAGE_VERIFY_KEY` or
  filling in grain.yaml's ConfigMap.
- **Sandbox images stamped by tag are verified when pulled, not when
  used.** Release builds no longer have this gap: `release-images.sh`
  stamps `repo:sha-<short>@sha256:<digest>` (grain/task-1892), so
  granule VMs, local Docker sandboxes and Kubernetes sandbox pods are
  created from the same digest an upgrade or setup.sh verified. A build
  stamped by tag still has it: `.github/workflows/build-artifacts.yml`
  (dormant) stamps bare `sha-` tags, a hand `make image` stamps whatever
  `SANDBOX_IMAGE`/`K8S_SANDBOX_IMAGE` it is given, and an unstamped build
  falls back to `guest:latest`/`k8s-sandbox:latest`. So do sandbox
  images named by deployment flags (`-sandbox-image`,
  `-k8s-sandbox-image`, `-container-sandbox-image`,
  `GRAIN_GRANULE_OCI_IMAGE`) unless the operator pins them by digest.
  For those, VMs and pods follow whatever the tag names when they are
  created.
- **Key rotation** means re-signing the digests that are still kept
  (`latest`, releases, live branch heads) with the new key, and shipping
  the new public key to deployments, before the old one is retired.
