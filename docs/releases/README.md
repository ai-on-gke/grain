# Releases

One file per release of grain, named for its tag: `v1.5.0.md` is what
`v1.5.0` brought that `v1.4.0` did not. Read the newest one to see what is
in `latest`.

Each file has:

- **Highlights**: a short account of the features and fixes worth a
  reader's attention, written by the agent of the release's release-notes
  task.
- **Features**, **Fixes** and **Other changes**: every pull request merged
  into the release, by title and number, sorted by its Conventional Commits
  type (`feat`, `fix`, everything else).

## How they are written

Releases are made from `main` by the release train
(`.grain/workflows/release-train.yml`, running
`scripts/release-from-trunk.sh`; `docs/release-candidate-management.md`).
When a release candidate `vX.Y.0-rc.N` passes its staging soak, the train
stages it as `vX.Y.0-pending` and files a task from
`.grain/templates/release-notes.yml`. That task:

- drafts the Highlights from what landed since the previous release;
- runs `scripts/release-notes.sh vX.Y.0 <previous> 'vX.Y.0-pending^{commit}' <highlights>`;
- opens a pull request for `docs/releases/vX.Y.0.md` against `main`, which
  merges on its own.

This happens before a person approves the release. The operator
notification for `vX.Y.0` links to the task, so whoever approves can read
the notes first.

- The previous release is the newest `vX.Y.0` that `RELEASE_NEVER_PROMOTE`
  in `scripts/release.conf` does not name, so notes never run from a
  placeholder tag.
- The lists cover `main`'s first-parent history between the two releases.
  There are no back-merges and no ports to account for, because every
  release is a commit on `main`.
- If a file for the release is already on `main`, the task leaves it as it
  is. Notes written or edited by hand are never overwritten.
- If the pending release is rejected before the task runs, the task writes
  nothing. Notes for a rejected release that already merged stay until a
  later rc of the same minor is approved, and that release's task leaves
  them as they are. Edit them in a normal pull request.
- To correct a release's notes, edit its file in a normal pull request.
- A patch release (`vX.Y.Z`, `release/vX.Y.Z`) has notes written by hand,
  listing only the picked fixes.

Releases up to `v2.4.0` were promoted by the legacy evening cut, which wrote
their notes itself and back-merged `candidate` into `main`. Its owed-ports
list (`owed-ports.json`) and back-merges were retired with it. A file from
that time may still have a "Fixes owed to main" section.

`v1.2.0` to `v1.5.0` were generated after the fact from the same script and
have no highlights. There is no `v1.1.0` tag, so `v1.2.0` is measured from
`v1.1.0-rc.1`.

There is no `v1.14.0`. Its candidate, `v1.14.0-rc.1`, held only the
single-tenant reset's first stage and was never promoted
(`RELEASE_NEVER_PROMOTE` in `scripts/release.conf`). The release after
`v1.13.0` is `v2.0.0`, the first single-tenant release. Its notes,
`v2.0.0.md`, were written by hand before the cut, so the cut left them as
they were.

There is no `v2.2.0` or `v2.3.0` either. Their candidates, `v2.2.0-rc.1` and
`v2.3.0-rc.1`, were held at the candidate gate because the required nightly
kept being cancelled for lack of sandbox capacity, so neither was promoted.
The release after `v2.1.1` is `v2.4.0`, and `v2.4.0.md` covers everything
since `v2.1.1`. It was written by hand before the cut, like `v2.0.0.md`, and
it folds in the breaking changes first drafted for v2.3.0. `v2.3.0.md` is
kept only as a pointer to it.

There is no `v2.5.0` release. Version numbers never repeat or go backwards,
and `main` had already been building `v2.5.0-dev` when release from trunk
took over. So v2.3, v2.4 and v2.5 are all consumed, and `v2.5.0` is only a
placeholder tag on `main`: a version number, not a release. It has no notes,
images or GitHub release. It makes `main` build `v2.6.0-dev`, and **the first
release from trunk is `v2.6.0`**. Its notes, `v2.6.0.md`, are written by the
release-notes task the train files when it stages `v2.6.0` as pending, and they cover
everything since `v2.4.0`, not since the placeholder (`RELEASE_NEVER_PROMOTE`
in `scripts/release.conf` names `v2.5.0`, so the train skips it as the
previous release).
