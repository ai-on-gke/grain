# Sandbox image mirror

`-sandbox-image-mirror` (`grain settings -sandbox-image-mirror`, Settings →
Sandboxes, `grain_config.sandbox_image_mirror`) names an OCI repository,
without a tag, such as `us-docker.pkg.dev/<cluster-project>/grain/guest`.
An off-cluster grain copies the guest image its build is stamped for into
that repository, so that a Kubernetes cluster in another project or
organization pulls the guest from a registry of its own. The copy keeps the
stamp's tag (`sha-<commit>`), and new sandboxes are pinned to the digest the
mirror answers for it.

The copy is made in three places:

- **At startup**, from `EffectiveK8sGranuleImage`, when the mirror does not
  already hold this build's tag at the digest docker last pushed.
- **On a live settings change**, when the mirror is set or changed without
  a restart.
- **During an `-upgrade-image` upgrade**, by the upgrader (`stageImage`)
  before the new daemon starts.

## Cleaning up old mirrored images on the host

Every copy is a `docker pull`, `docker tag` and `docker push` on the daemon's
host, which leaves a local tag per build and per mirror repository behind.
Grain removes the old ones, keeping the image in force and the one before it,
so going back one build does not have to pull again.

- **An upgrade** cleans up in `cleanOldImages` (`pkg/upgrade/image.go`). It
  keeps the new and previous mirror targets.
- **A startup or live mirror** cleans up after the mirror succeeds
  (`cleanOldMirroredSandboxImages`, `pkg/daemon/sandbox_image_cleanup.go`).
  The daemon records the image in force and the previous one in
  `<data-dir>/sandbox-image-mirrored.json`. It then removes the other local
  tags in the repositories of those two. When the record moves on, it also
  cleans the repository of the image that dropped out of it, so changing the
  mirror twice does not strand the first repository's tag. The first mirror
  on a data directory only writes the record, because nothing is known yet
  about what came before.

Either way, only tags are removed (`docker image rm <repo>:<tag>`, never `-f`).
The source image is never removed, and neither is an image that
`-sandbox-image`, `-k8s-granule-image` or the custom sandbox image names.
Docker refuses to remove a tag a container still uses. That refusal is logged
(`leaving old mirrored sandbox image … in place`) and the tag stays.

## Cleaning up the mirror registry

**Grain does not delete anything from the mirror registry.** A mirror gains
one guest image per build it runs, and the registry's own retention rules are
what remove old ones. Grain leaves this to the registry for three reasons:

- **It would need write access that grain does not have.** Deleting needs
  permission to delete artifacts (`artifactregistry.versions.delete`, in
  `roles/artifactregistry.repoAdmin`). Mirroring only pushes
  (`roles/artifactregistry.writer`). `registryauth` hands its Google token
  only to Google registry hosts, so grain has no credential for any other
  registry.
- **The mirror is not grain's alone.** Several deployments, such as staging
  and production, or one host after a reinstall, can mirror into one
  repository. A single daemon only knows its own current and previous images,
  so it could delete a guest another deployment's pods still pull.
- **A delete is by digest, not by tag.** `DELETE /v2/<name>/manifests/<digest>`
  removes every tag that points at that digest. If an older tag shares the
  current build's digest (an unchanged guest), deleting the old tag deletes
  the current one too.

### Artifact Registry: a cleanup policy

Give the mirror's repository a
[cleanup policy](https://cloud.google.com/artifact-registry/docs/repositories/cleanup-policy)
that keeps the most recent guest versions and deletes older ones. For a mirror
at `us-docker.pkg.dev/<cluster-project>/grain/guest`, the repository is
`grain` and the package is `guest`:

```json
[
  {
    "name": "keep-recent-guests",
    "action": {"type": "Keep"},
    "mostRecentVersions": {"packageNamePrefixes": ["guest"], "keepCount": 5}
  },
  {
    "name": "delete-old-guests",
    "action": {"type": "Delete"},
    "condition": {"packageNamePrefixes": ["guest"], "tagState": "any", "olderThan": "14d"}
  }
]
```

```
gcloud artifacts repositories set-cleanup-policies grain \
  --project=<cluster-project> --location=us --policy=policy.json --dry-run
```

Check the dry run's audit log, then run it again with `--no-dry-run`. A `Keep`
policy always wins over a `Delete` one, so the most recent `keepCount` guests
survive however old they are. Set `keepCount` to at least two (current and
previous) for each deployment that mirrors into the repository.

### Other registries

A registry other than Artifact Registry needs its own retention rules. The
`registry:2` distribution registry has none. There, with
`REGISTRY_STORAGE_DELETE_ENABLED=true`, an old tag is deleted by its digest
and the space is reclaimed by a garbage collection:

```
accept='application/vnd.oci.image.index.v1+json,application/vnd.oci.image.manifest.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.docker.distribution.manifest.v2+json'
digest=$(curl -sfI -H "Accept: $accept" https://<registry>/v2/<name>/manifests/sha-<old> \
  | tr -d '\r' | awk -F': ' 'tolower($1)=="docker-content-digest"{print $2}')
curl -X DELETE https://<registry>/v2/<name>/manifests/$digest
registry garbage-collect /etc/docker/registry/config.yml   # inside the registry's container
```

Before deleting, check that the digest is not also the one a tag you keep
answers.
