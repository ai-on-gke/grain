# Releases

One file per release of grain, named for its tag: `v1.5.0.md` is what
`v1.5.0` brought that `v1.4.0` did not. Read the newest one to see what is
in `latest`.

Each file has:

- **Highlights**: a short account of the features and fixes worth a
  reader's attention, written by the agent of the release's release-notes
  task.
- **Breaking changes**, only when there are any: every change whose title
  or commits carry a Conventional Commits `!` marker (`feat!:`,
  `fix(api)!:`) or a `BREAKING CHANGE:` trailer, with the trailer's text.
  `scripts/release-notes.sh check-breaking` refuses a release that has
  them unless it is the major release `RELEASE_MAJOR` in
  `scripts/release.conf` asks for, or a person sets
  `RELEASE_ALLOW_BREAKING=<version>` for it: `grain release approve
  -allow-breaking vX.Y.Z` (`allow_breaking: true` on the approve route),
  or the `allow_breaking` input of the GitHub Actions `promote-release`
  workflow.
- **Features**, **Fixes** and **Other changes**: every pull request merged
  into the release, by title and number, sorted by its Conventional Commits
  type (`feat`, `fix`, everything else).

## How they are written

Releases go out continuously from `main` by the release train
(`.grain/schedules/release-train.yml` running `scripts/release-train-task.sh`
and `scripts/release-from-trunk.sh`; `design/playbook/releases.playbook.md`).
When a release candidate `vX.Y.0-rc.N` passes two-stage qualification
(`nightly.yml` and `grain/release-qualification`), the train promotes `vX.Y.0`
and moves `latest` automatically -- or stages `vX.Y.0-pending` when a person
must approve (a major bump, breaking changes, or `RELEASE_AUTO_PROMOTE=false`).

Grain's outbound `release-notes` reconciler (`pkg/orchestrator/releasenotes.go`)
watches the repository's release tags and, whenever a promoted `vX.Y.Z` tag
(or a staged `vX.Y.Z-pending` tag) has no `docs/releases/vX.Y.Z.md` on `main`
and no open task, files one task from `.grain/templates/release-notes.yml`.
That task:

- drafts the Highlights from what landed since the previous release;
- runs `scripts/release-notes.sh vX.Y.Z <previous> '<to-ref>^{commit}' <highlights>`;
- opens a pull request for `docs/releases/vX.Y.Z.md` against `main`, which
  merges automatically on green CI (`auto-merge: true`).

For a staged `-pending` release, the operator notification in the Inbox quotes
`docs/releases/vX.Y.Z.md` once merged so whoever approves can read the notes
first.

- The previous release is the newest earlier `vX.Y.Z` that is neither
  retracted (`-retracted`) nor named by `RELEASE_NEVER_PROMOTE` in
  `scripts/release.conf`, so notes never run from a placeholder or retracted
  tag.
- The lists cover first-parent history between the two releases.
- If a file for the release is already on `main`, both the reconciler and the
  task leave it as it is. Notes written or edited by hand are never
  overwritten.
- Historical minor series older than an already-documented release that have
  no `docs/releases/vX.Y.*.md` file (such as `v3.0.0`–`v3.0.2`, whose changes
  were rolled into `v3.1.0.md`) are not backfilled automatically, while a new
  patch tag (`vX.Y.Z`) on an already-documented minor series still files a
  release-notes task.
- If a pending release is rejected before the task runs, the task writes
  nothing. Notes for a rejected release that already merged stay until a
  later rc of the same version is promoted, and that release leaves them as
  they are. Edit them in a normal pull request.
- To correct a release's notes, edit its file in a normal pull request.
- A patch release (`vX.Y.Z`, `release/vX.Y.Z`) may also have notes written by
  hand before or after tagging, listing the picked fixes.

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
