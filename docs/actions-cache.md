# The Actions cache

Grain CI builds every job in a sandbox it destroys when the job ends, so
until now nothing a job built survived it. A repository whose workflow
already says

```yaml
cache-from: type=gha
cache-to: type=gha,mode=max
```

got nothing for it: `act` runs an Actions cache server of its own, and
does hand the job an `ACTIONS_CACHE_URL` pointing at it — but that
server lives inside the sandbox, so every run exported its layers into a
cache that was deleted minutes later. Measured on `gke-labs/granule`,
`docker info` reported `Images: 0` and `docker buildx du` reported `0B`
at the start of every single run, and `~/.cache/actcache` was empty each
time.

The cache therefore moves off the machine that is thrown away and onto
the machine that is not: **the controller keeps one Actions cache, and
serves it to every CI job over HTTP**.

## How a job reaches it

The same way a sandbox reaches the proxy cache, and for the same
reasons — one listener on the address every sandbox backend can already
get to (`-granule-git-proxy-host` and its siblings), with no mount, no
route, no privileged port and no change to any sandbox image:

1. The daemon opens the cache under `<sandbox-dir>/cache/actions` (or
   `<data-dir>/cache/actions` if no sandbox directory is configured) and binds
   a listener in front of it (`cmd/grain/actionscache.go`).
2. When a CI job's sandbox is built, Grain mints a token, registers it
   against the repository and branch that job is testing (and whether
   it is a release run — see below), checks from inside
   the sandbox that the cache answers, and writes the URL —
   `http://<host>:<port>/<token>/` — to `/etc/grain/actions-cache-url`.
3. The `act` invocation Grain generates for an Actions job
   (`pkg/grit/actions`' `ActScript`) reads that file and, where it is there,
   runs `act --no-cache-server --env ACTIONS_CACHE_URL=<url>`. The job's
   own `actions/cache` steps and its `type=gha` builds then talk to the
   controller instead of to a server that is about to be deleted.
4. When the sandbox is released the token is revoked, so the URL written
   into it is worth nothing afterwards.

Nothing in a workflow file changes. A repository that already writes
`type=gha` because it also runs on GitHub gets warm builds here for
free; one that writes nothing gets what it always got.

## What it keeps, and for how long

The store speaks the Actions cache API as `actions/cache` and buildkit's
`gha` backend use it: an entry is reserved, uploaded in ranges,
committed, and afterwards found by key and version — the first key
exactly, then each restore key as a prefix, newest first, matched
case-insensitively. An entry is only ever handed to a lookup naming the
same *version*, which is what makes sharing one cache between jobs safe
rather than merely likely to work.

**Actions cache max size (GiB)**, on Settings → Caches, is how much
disk the whole cache may use (20 GiB by default). Over that, the entries
nothing has read for longest are thrown away — a global bound rather
than one per repository, because the failure it exists to prevent is a
disk-shaped one: CI admission here already holds jobs on a free-space
floor, so a cache that grew without a bound would start refusing the
jobs it was meant to make fast, and "N GiB per repository" is only a
bound if somebody counts the repositories. A busy repository can evict a
quiet one's entries; the cost of that is a cold build.

Changing it takes effect without a restart: the daemon resizes the
running cache on its next reconcile tick and evicts down to the new
bound there and then, which is as safe with jobs in flight as a flush
is.

`-actions-cache-size-gb` is still a flag, but only as a seed for a
deployment that has never stored a configuration; the stored setting
wins from then on. Its old `0` — which used to mean "keep no cache at
all" — seeds the **Actions cache** switch off rather than a cache of
zero bytes, so an operator who later turns it back on gets a sensible
size rather than one nobody chose.

## Who can read what

**One repository cannot read another's entries.** Entries are filed
under a repository, and the token in the URL is what says which
repository the job asking is asking for. It is minted per job, revoked
when the job's sandbox is released, and a request carrying an unknown or
revoked one is refused outright.

**Within a repository, entries belong to the branch that wrote them**,
after GitHub's own rule for `actions/cache` that a branch reads its own
entries and its base's, and a base never reads a branch's (issue #2392).
Here "its base's" means what release runs wrote, because a release is
the only run that nobody but Grain can start. Each entry records
the branch whose job wrote it and whether that job was a release run:

* An ordinary job — a push, a pull request, a manual or scheduled run,
  on any branch — writes entries under its own branch. It restores its
  own branch's entries first and, failing those, entries release runs
  wrote. It never restores another branch's entries, so a task branch
  cannot plant an entry under a key `main` or a release will restore.
* A release run — the one event Grain starts itself, after a merge or
  from the repository's **Run release** action, and that no caller can
  ask for — writes trusted entries and restores trusted entries only.
  Not even a push to the branch being released reaches it.
* An upload in progress can only be added to and committed by a job of
  the branch and trust that reserved it, and an archive can only be
  downloaded by a job whose lookup could have found it. Entry ids are
  sequential, so neither rests on an id being hard to guess.

What this costs is the first run of a new branch: it builds cold unless
a release run has already cached the same key, and warm from its second
run on. A deployment whose repositories have no release workflow
therefore shares nothing between branches at all — which is the price of
`main` never restoring what an unreviewed branch uploaded.

Entries written before entries recorded their branch say nothing about
where they came from, so no job reads them and the daemon removes them
the next time it starts.

The **Actions cache** switch on Settings → Caches (or
`grain settings -actions-cache=false`) still turns the whole cache off.
That takes effect for the next job dispatched: a job already running
keeps the URL its sandbox was built with, and the entries already stored
stay where they are until somebody flushes them.

The cache is not a credential store and holds nothing a CI job could not
rebuild for itself.

## What it is holding, and emptying it

System → Caches is what the Actions cache holds right
now: how many entries and how much disk, against the bound it is held
under, and a breakdown by repository, largest first. The breakdown is
the interesting figure — the cap is one global bound, so the question an
operator has when the cache is full is whose entries are under it.

Each repository has a **Flush**, and there is one for the whole cache.
Neither asks for confirmation and neither needs one: nothing is lost
that a rebuild does not put back, and a job reading an entry as it is
removed goes on reading it (`Cache.Flush` is explicitly safe to call
while jobs are using the cache). What a flush costs is the next job that
wanted that entry, which builds cold.

The page draws two states worth telling apart. A deployment that keeps
no cache at all says so as a reading rather than as an error — that is
ordinary, and CI is merely cold. A cache that is *kept* while the switch
is off is a warning, because it is the one state where "there is a cache
here" and "my CI is warm" come apart, and it is exactly when somebody
wants the space back.

The rail carries the entry count beside the page, abbreviated
("1.8k") — and nothing at all on a deployment that keeps no cache, since
a `0` would claim an empty one.

## Why not a persistent image store

The bigger version of this idea is to keep a Docker image store or a
buildx state directory per repository and give it back to each job,
which would skip the export and import entirely. It was not done:

* it is mutable state shared between branches with no key, no version
  and no eviction story — an image tag one job pushed is simply there
  for the next job, whatever built it;
* it has to be built once per sandbox backend (a volume for containers,
  a disk for granule guests, something else for Kubernetes), where an
  HTTP service works identically on all of them;
* a corrupted entry in a content-addressed, versioned cache is one miss
  away from being rebuilt. A corrupted image store is an operator's
  afternoon.

## What is not here yet

* Only the v1 cache API is served. A client that insists on the newer
  `ACTIONS_RESULTS_URL` protocol keeps talking to `act`'s own artifact
  server, as it does today; buildkit's `gha` backend uses v1, which is
  what was measured.
