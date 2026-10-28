# Hand run: may agy update its own binary, and what actually holds the pin?

Task 185, out of an aside in task 178's run
(`docs/hand-runs/2026-09-17-agy-resume.md` §5). Measuring agy's
cross-dispatch resume turned up something that was not being asked
about: the pinned 1.1.27 binary the probes were run against **replaced
itself with 1.2.5 on the first exec**, when it was unpacked somewhere the
probe's own user could write it.

That makes three things in this repository claims about a binary a
deployment may not be running:

- the `Dockerfile`'s pin, whose whole argument is written next to it --
  "Google publishes release hashes; keeping them beside the version makes
  a changed artifact fail the build instead of silently changing the
  agent runtime";
- `docs/agy-surface.md`, which is agy's own surface read off *that*
  release, with its size and sha256 at the top;
- every measured fact in `pkg/agent/antigravity` -- the retry ladder, the
  turn deadline, the resume behaviour, the native tool roster -- each of
  which records which agy it was taken on.

Three questions, then. Does a dispatch really rewrite the binary, and on
what? Is there a switch? And, given the answers, may agy update itself at
all -- because a pin a release silently walks through is worse than no
pin, and saying so would have been a legitimate outcome.

## What was run, and against what

`agy 1.1.27`, the release `ARG AGY_CLI_VERSION` pins, fetched from the
same immutable `storage.googleapis.com` URL the image build uses and
checked against `ARG AGY_CLI_SHA256_AMD64`
(`f874d4f6…`). The unpacked binary is 210,551,040 bytes, `sha256
93eb2118…` -- the same digest `docs/agy-surface.md` records, and the one
task 178 measured against.

Every exec below is the shape `Framework.Run` builds: the production
argv (`agyArgs`), the private HOME `writeAgyHome` writes, the prompt on
stdin as a `stream-json` user event, and a stub of Gemini's
`streamGenerateContent` endpoint on loopback answering each turn
(`GOOGLE_GEMINI_BASE_URL`) -- task 178's fixture, reused. No Gemini
credential, and none needed: what is being measured is what the process
does to its own file.

Each exec runs against a **copy** of that binary in a directory the test
process owns, because the two images both unpack agy as root and the
question here is precisely the case permissions do not cover.

## 1. A dispatch replaces the binary, seven seconds after it returns

One exec, with nothing in the environment about updating:

```
before  210,551,040 bytes  sha256 93eb2118…   agy --version -> 1.1.27
after   217,284,864 bytes  sha256 84808e10…   agy --version -> 1.2.5
```

`84808e10…` is byte for byte what `https://antigravity.google/cli/install.sh`
serves, which is how task 178 identified it. The replacement lands
**after the exec has returned** -- 7 seconds later in this run, and
between 8 and 30 seconds in task 178's -- because it is a background
download that outlives the process that started it. Reading the file
immediately after an exec is how this went unnoticed for as long as it
did.

agy's own installer says so, when it finds an agy already installed:

> Notice: 'agy' is already installed at $BINARY_PATH.
> The Antigravity CLI automatically self-updates in the background during regular runs.

That installer also names the service the update comes from --
`https://antigravity-cli-auto-updater-…run.app/manifests/linux_amd64.json`,
which on 2026-09-17 answered `{"version": "1.2.5", "url": "…/1.2.5-4931130160447488/linux-x64/cli_linux_x64.tar.gz", "sha512": "1f899737…"}`.
So the updater follows a mutable manifest, which is the same reason the
Dockerfile fetches the artifact directly instead of running that
installer.

**Not every exec does it.** Measured one at a time, each on a fresh copy:

| exec | binary afterwards |
| --- | --- |
| `agy --version` | unchanged through 20s |
| `agy --help` | unchanged through 60s |
| `agy models` (refused: "Please sign in to view available models") | replaced within 15s |
| a dispatch-shaped `--input-format stream-json` exec | replaced within 7s |

`agy models` matters on its own: that is the exec
`pkg/agent/antigravity`'s `Catalog` makes, which is what the model list
in grain's Settings runs. Before this change, an operator opening that
list was enough to change which agy a deployment runs -- and it happens
even when the listing itself is refused for want of a credential, so no
key and no successful call is needed.

## 2. There is a switch, and it takes exactly one value

1.1.27's string table holds `AGY_CLI_DISABLE_AUTO_UPDATE`, once, and
nothing else about it. It is documented nowhere: `agy --help`'s flag
list (`docs/agy-surface.md`) does not mention the updater, and the binary
is stripped.

Measured, each on a fresh copy, with the same dispatch-shaped exec:

| environment | binary afterwards |
| --- | --- |
| nothing | replaced, 7s after the exec |
| `AGY_CLI_DISABLE_AUTO_UPDATE=1` | **replaced**, within 30s |
| `AGY_CLI_DISABLE_AUTO_UPDATE=true` | unchanged through 90s, and again through 60s in the committed probe |

So the value is compared as a string rather than parsed as a boolean.
`=1` looks like it works and does nothing, which is the kind of detail
that gets "tidied" into place later, so
`pkg/agent/antigravity/selfupdate_test.go` asserts the literal entry
`AGY_CLI_DISABLE_AUTO_UPDATE=true` rather than the variable's presence.

Nothing else about the exec changed: the resume probes pass with the
switch set, and the session, the conversation and the request bodies are
the same. Turning the updater off does not make agy refuse to run.

## 3. Permissions are not the thing holding the pin

Task 178 read `Dockerfile.k8s-sandbox` as safe and left the main image as
an open question. Reading both:

- `Dockerfile.k8s-sandbox` installs agy as root under `/opt/antigravity`,
  `chmod -R a+rX`, and declares `USER agent` (uid 1000). A task's agy
  there cannot rewrite the file. (It is also installed *unpinned*, by
  `install.sh`, so what it carries is whatever the manifest served on
  build day -- 1.2.5 as of 2026-09-17. That is a separate problem from
  this one.)
- The main `Dockerfile` unpacks it the same way and declares **no
  `USER`**, on purpose: the systemd unit `scripts/setup.sh` writes passes
  `--user` with the host's own grain uid, so a single-node deployment
  runs the daemon unprivileged and root-owned `a+rX` files do stop it.
  `deploy/kubernetes/grain.yaml` sets no `securityContext` and no
  `runAsUser` at all, so a Kubernetes deployment's daemon **is root** --
  and root ignores the mode bits. That deployment's pin was being walked
  through on the first dispatch of every fresh pod, silently. (As
  measured, on 2026-09-17. That manifest runs as uid 1000 since
  grain/task-209 -- see "Still owed" at the end of this file.)

Which is the answer to "confirm the binary is not writable in every image
grain ships and say so beside the pin": it is not writable in one of the
two ways a deployment is started, and permissions were never going to
cover the third case -- `GRAIN_AGY_PATH` pointing at a binary in an
operator's own home directory, which is the documented escape hatch for a
deployment pinning a version by hand.

## The decision

**agy may not update its own binary.** A deployment runs the agy its
image was built with until an image is built with another one. The pin
stays, and what holds it is `AGY_CLI_DISABLE_AUTO_UPDATE=true` in the
environment of every agy this repository starts, rather than file
permissions:

- `pkg/agent/antigravity/selfupdate.go` has the reasoning and one
  `agyEnv` helper, used by all three exec sites -- a dispatch, an
  interactive session, and the model listing above.
- both images set it as an `ENV`, for execs that do not come through that
  package: the build's own `agy --version` check, an operator's shell in
  the container, and a task running agy inside the sandbox image.
- `scripts/agy-surface.sh` exports it, so that a capture is one binary.
  The committed `docs/agy-surface.md` was consistent by luck --
  `.grain/workflows/agy-surface.yml` unpacks the release as root and
  runs the script as the runner's user -- but that document records a
  digest and then execs agy a dozen more times, `agy models` and a
  stream-json session included, and a local run against a writable
  `~/.local/bin/agy` would have produced a document describing two
  different binaries.

What this gives up is stated rather than hidden: an agy release that
fixes something a deployment is hitting now waits for a `Dockerfile`
bump. That is what the pin was always asking for, and it is what makes
every measurement in `pkg/agent/antigravity` a statement about the binary
a deployment is actually running.

## What this changed

- `pkg/agent/antigravity/selfupdate.go`: the decision, the measurement,
  and `agyEnv` -- the private HOME, the cleared `GOOGLE_API_KEY` and the
  switch, in one place instead of three copies.
- `pkg/agent/antigravity/selfupdate_test.go`: all three exec sites carry
  the literal entry.
- `pkg/agent/antigravity/live_selfupdate_stub_test.go`: §1 and §2 as a
  probe. Two writable copies of the installed agy, the same dispatch
  against each, the files watched afterwards; the control drops the
  switch on purpose, since a probe that cannot reach Google's updater
  proves nothing by finding a binary unchanged, and a control that stays
  put skips rather than passes.
  `.grain/workflows/live-agent.yml` runs it beside the other two
  credential-free agy probes.
- `Dockerfile`, `Dockerfile.k8s-sandbox`, `scripts/agy-surface.sh`: the
  `ENV`/`export` above, each with the reason beside it.

## Still owed

- **The Kubernetes manifest runs the daemon as root.** The pin no longer
  depends on that, but nothing else about it was examined here, and a
  `runAsUser`/`fsGroup` on `deploy/kubernetes/grain.yaml` is a change
  with a PVC-ownership question attached. Worth a task of its own rather
  than a line in this one. *(Settled by grain/task-209: that manifest now
  runs the daemon as uid 1000 with `fsGroup: 1000` and an explicit
  `HOME`, the reasoning is beside its `securityContext`, and
  `deploy/kubernetes/README.md` carries the chown an existing volume
  needs.)*
- **The sandbox image's agy is unpinned.** `Dockerfile.k8s-sandbox` still
  installs whatever `install.sh` serves on build day, so two builds of
  the same commit can carry two different agys for a task to run. This
  change stops one from becoming the other mid-run; it does not settle
  which one the image gets. *(Settled since, in grain/task-208: that
  image now fetches the same pinned artifact the `Dockerfile` does, and
  claude with it, with `TestBothImagesPinTheSameAgentCLIs` holding the
  two files' `ARG` defaults equal.)*
- **The next `docs/agy-surface.md` regeneration.** The capture is
  unchanged by this in substance -- the committed one was taken against a
  root-owned 1.1.27 -- but its header now says the probes run with the
  updater disabled, and that sentence only appears once
  `.grain/workflows/agy-surface.yml` is dispatched again.
