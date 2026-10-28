# Walkthrough: per-version release notes and the upgrade preview

- **Date:** 2026-10-13
- **Plan:** `docs/test-plans/2026-10-12.md` §10 (read from `origin/candidate`)
- **Commit under test:** `f3dfcaa5c` (`main`). It contains `be2088baa`, `94823af51` and `4276f0e35`.
- **Task:** 3293 (filed by task 3209)

The task brief said the feature was not on `candidate` yet. When this ran, all three commits were already on
`origin/candidate` too. The walkthrough still targets `main`, as the plan says.

## Environment

- Debian 12 sandbox: Go 1.26.6, Node v20.19.0, Docker 29.9.0, git 2.39.5, and Playwright Chromium headless shell
  151.0.7922.34. Chromium was installed with `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`,
  because the package cache re-signs the CDN's TLS.
- `cd ui && npm ci && npm run build`, then `go build -o /tmp/grain ./cmd/grain`. `grain version` printed
  `v1.10.0-rc.1.0.20261013063229-f3dfcaa5c6f5 · f3dfcaa · 2026-10-13 06:32Z`.
- **Demo:** `grain demo -addr 127.0.0.1:18421 -open=false`. For step 4 it was restarted with
  `-upgrade-image localhost:5000/x/grain -upgrade-allowed-branches '**'`. Users were picked with `X-Grain-User`.
  - `alice` is a system admin, but only with `X-Grain-Admin-Mode: on`.
  - Two users with no system-admin role were added to the `default` team through `PUT /api/teams/default`:
    `tadmin` as team admin and `mem` as member.
  - `mallory` is not on any team.
- **Source path:** `grain demo` wires only the container upgrader (`pkg/demo/demo_upgrade.go`), so step 3 ran on a
  real daemon:
  `grain daemon -data-dir /tmp/dd -disable-native-sandboxes -ui-addr 127.0.0.1:18420 -upgrade-src-dir /tmp/srcclone
  -upgrade-install-path /tmp/dd/grain-installed -upgrade-allowed-branches '**'`.
  - It trusts loopback as its own actor, which is a system admin.
  - `/tmp/srcclone` is a clone of the local bare repo `/tmp/origin.git`, which has these branches:
    - `main`: `origin/main`.
    - `no-notes`: `origin/main` again, so it has no added file.
    - `notes-test`: `main` plus a commit adding `docs/releases/v9.9.0.md`, which has a table, a fenced code
      block, a link and inline code.
    - `pre-notes`: `f71fd5363`, the parent of `be2088baa`.
- Both servers' readiness was polled on `GET /api/sandboxes`.
- The browser was Playwright, driven by a scratch script against the built UI. It is not committed.

### Stubs

| Boundary | Stub | Why |
|---|---|---|
| Image registry (ghcr) | `registry:2` on `localhost:5000`, holding `x/grain:{main,notes-test,pre-notes}` | No registry credentials in a sandbox |
| `x/grain:main` | Built from a clone of `main` with `scripts/ci-docker-build.sh --build-arg INSTALL_CODEX_CLI=0`, after one edit to that clone's `Dockerfile`: the kubectl download was replaced with a `/out/kubectl` script that exits 1. The image stamps itself `v0.0.0-20261013063229-f3dfcaa5c6f5+dirty`, because the clone has no tags and the Dockerfile was edited | `dl.k8s.io` resolves only to IPv6 here and timed out after 180 s. The codex CLI download from GitHub was reset 4 times. The preview needs neither |
| `x/grain:notes-test`, `x/grain:pre-notes` | `FROM x/grain:main` plus a `CGO_ENABLED=0` grain binary built from the `notes-test` branch (with tags, so `v1.10.0-rc.1.0.20261013082904-5deea7a8ef1b`) or from `f71fd5363` (`dev (unstamped)`, no `release-notes` subcommand) | Two more full image builds would cost about 20 minutes each. Only the binary differs |
| Signature verification | None: no `-upgrade-verify-cmd` | No cosign key in a sandbox |
| Git remote (source path) | A local bare repo | No GitHub access from a sandbox |
| Kubernetes path | Not run | No cluster here. It is covered in CI by fakes only (`pkg/upgrade/releasenotes_test.go`) |

Nothing in grain's own API, store or UI was mocked.

## Step 1: `grain release-notes`

`ls docs/releases` lists `README.md`, `releases.go`, and `v1.2.0.md` through `v1.9.0.md`.
- `grain release-notes` printed the same eight versions, newest first (`v1.9.0` … `v1.2.0`), each from its `# vX.Y.0` heading.
- `--json | jq` parsed. The top-level keys were `notes` and `version`, and `.version` was
  `v1.10.0-rc.1.0.20261013063229-f3dfcaa5c6f5`, matching `grain version`. There were 8 notes, each with
  `version`, `title`, `date` and `commit`. The dates and commits match each file's "Released … from commit"
  line, for example v1.9.0 `2026-10-12` `adea62d8f`.

| Arguments | Printed | Exit |
|---|---|---|
| `--from v1.8.0` | v1.9.0 | 0 |
| `--from v1.9.0` (newest) | stderr "no release notes in this build for that range" | 0 |
| `--to v1.3.0` | v1.3.0 v1.2.0 | 0 |
| `--from v1.5.0 --to v1.7.0` | v1.7.0 v1.6.0 | 0 |
| `--from v1.7.0 --to v1.7.0` (equal) | stderr "no release notes in this build for that range" | 0 |
| `--from v1.7.0 --to v1.5.0` (reversed) | stderr "no release notes in this build for that range" | 0 |
| `--from v0.1.0`, `--to v9.9.9` (unknown, outside the range) | all eight | 0 |
| `--from v1.4.1` / `--to v1.4.1` (unknown, inside the range) | v1.9.0…v1.5.0 / v1.4.0…v1.2.0 | 0 |
| `--from v1.10.0` | stderr "no release notes in this build for that range" | 0 |
| `--from 1.5.0` (no `v`) | v1.9.0…v1.6.0 | 0 |
| `--from bogus`, `--to bogus` | all eight (see observation 1) | 0 |
| `--from v1.9.0 --json`, `--from bogus --json` | `{"version":…,"notes":[]}` / all eight | 0 |

**Result:** pass.

## Step 2: Metrics → Release notes

Playwright opened `/metrics` on the demo twice: as `mem` with no Admin-mode header, and as `alice` with
`X-Grain-Admin-Mode: on`. Both views were the same:
- Weekly and Versions sections. The Versions intro reads: "This deployment runs
  v1.10.0-rc.1.0.20261013063229-f3dfcaa5c6f5, which has no notes of its own in this build." That is the
  `grain version` string.
- No version carries the "running" chip. That is correct, because v1.10.0's notes do not exist yet.
- Eight versions, newest first, each with "Released <date>".
- Opening each version:
  - `aria-expanded=true`.
  - The region is labelled "vX.Y.0 release notes".
  - The headings render as h1/h2 (`vX.Y.0`, Highlights where the file has them, Features, Fixes, Other changes).
  - Lists render as `<li>`, code as `<code>` (3–15 per file), and the compare link as an `<a href>`.
  - No raw markdown (`**`, `# `, `](http`) leaked, and there were no page errors.
- The real notes files have no tables or fenced code. The `v9.9.0.md` used in steps 3 and 4 covers those:
  the table rendered as a `<table>` and the fence as a code block.

On 11 of the 16 clicks (5 of 8 as `mem`, 6 of 8 as `alice`), the region already labelled for the newly clicked version still held the previous
version's markdown when sampled straight after the click. For example, "v1.7.0 release notes / v1.8.0". This is
**finding 1**, fixed here.

API checks:
- `GET /api/release-notes/versions` gave 200 for `mem`, `tadmin` and `alice`, and 403 for `mallory`.
- `/versions/1.9.0` (no `v`) answered v1.9.0.
- `/versions/v9.9.9` and `/versions/bogus` gave 404 "this build carries no release notes for …".

**Result:** pass after the finding 1 fix.

## Step 3: upgrade pane, source path (real daemon)

`GET /api/upgrade` returned `{"enabled":true,"mode":"source"}`. On `/admin/upgrade`, the branch box was filled
with each branch in turn:

| Branch | API | Pane |
|---|---|---|
| `notes-test` | 200, notes `[v9.9.0]` with date `2026-10-13` and commit `abcdef012` | "What notes-test brings", "This deployment runs v1.10.0-rc.1…", then v9.9.0 rendered: headings, the link, inline code, the table and the code block (9 rich nodes) |
| `no-notes` | 200, `notInThisBuild`, "release notes are not in this build for anything newer than v1.10.0-rc.1…" | Info alert with that sentence |
| `pre-notes` | 200, same as `no-notes`. Its `docs/releases` stops at v1.8.0 | Same |
| `does-not-exist` | **422**, `fetching does-not-exist: git fetch … exit status 128` | Warning "Could not read the release notes does-not-exist carries: …", and nothing else. See **finding 2** |
| `notes-test` again, within 5 minutes | 200 in 1.0–1.9 ms, same body | Same as the first time |

- **Cache:** the daemon log shows one `upgrade: notes-test: release notes: fetching notes-test to read its release
  notes` line, for the first preview only. The repeat made no new fetch line and answered in about 1 ms. Failures
  are not cached: in an earlier curl pass on the same daemon binary, two `does-not-exist` previews 5 s apart
  both logged a fetch (08:29:26 and 08:29:31).
- **No disconnected banner:** `disconnected-banner` count was 0 right after the 422 and again 3 s later. The only
  sign of the 422 is the pane's own inline warning.

**Result:** pass. Finding 2 is fixed here.

## Step 4: container path (`grain demo -upgrade-image`)

Docker was available. `GET /api/upgrade` returned `mode: image`.

| Tag | API | Pane |
|---|---|---|
| `notes-test` | 200 in 1.16 s. Ref `localhost:5000/x/grain:notes-test@sha256:2c494a…`, notes `[v9.9.0]`, version `v1.10.0-rc.1.0.20261013082904-5deea7a8ef1b`, `missing: v1.10.0`, "release notes are not in this build for v1.10.0: an image is built before the release cut writes its version's notes" | The sentence, then v9.9.0 rendered. The target carrying notes newer than its own version is an artifact of the made-up v9.9.0 |
| `main` | 200 in 0.75 s. Version `v0.0.0-…+dirty`, "… is not newer than v1.10.0-rc.1…, which this deployment runs" | Before the fix: **"V0.0.0-…+dirty is not newer than …"** in a green success alert. This is **finding 3** |
| `pre-notes` | 200. `notInThisBuild`, "release notes are not in this build: it does not answer `grain release-notes`, which a build older than the subcommand does" | Info alert with that sentence |
| `does-not-exist` | **422** in 35 ms. "pulling localhost:5000/x/grain:does-not-exist: docker pull …: not found" | Inline warning. No disconnected banner |
| `notes-test` again | 200 in 1 ms. No second `docker pull` in the log | Same |

The daemon log shows the whole preview sequence for each tag: `docker pull`, `docker image inspect` (the pin),
then `docker run --rm <ref@sha256> release-notes --json --from v1.10.0-rc.1…`.

**Result:** pass. Finding 3 is fixed here.

## Step 5: `/api/upgrade/release-notes` is refused to anyone but a system admin

`GET /api/upgrade/release-notes?branch=main` on the demo:

| Principal | Admin mode off | Admin mode on |
|---|---|---|
| `mem` (member) | 403 "user \"mem\" does not have administrator privileges" | 403 |
| `tadmin` (team admin) | 403 | 403 |
| `mallory` (no team) | 403 | 403 |
| `alice` (system admin) | 403 | 200 with `-upgrade-image`. Without an upgrader it is 404, which `UpgradePanel` reads as "not configured" |

**Result:** pass.

## Findings

1. **Fixed: Versions showed the previous version's notes under the newly clicked one's name.**
   - `ReleaseVersions.jsx` kept the last `note` until the selection effect cleared it. The click's own render
     therefore showed the old markdown inside the region labelled for the new version.
   - Reproduction: in Metrics → Release notes, click v1.8.0, then v1.7.0, and read the
     `release-version-notes` region straight after the second click. It is labelled "v1.7.0 release notes" and
     holds `# v1.8.0`.
   - Fix: the loaded notes carry the version they were read for, and render only under that version.
   - Regression test: `ReleaseNotesPanel.test.jsx`, "never shows one version's notes under another's name while
     switching". It holds v1.7.0's answer pending and records what `Markdown` is handed during the click. It
     fails without the fix.
2. **Fixed: on the source path, a branch origin does not have answered 422 "fetching X: git fetch … exit status 128".**
   - The pane printed that sentence verbatim.
   - Fix: when git's stderr says "couldn't find remote ref", the 422 now says "origin has no branch X".
   - Nothing else is read from git's stderr, because it can carry the remote URL.
   - Regression test: in `TestReleaseNotesOnTheSourcePathReadsTheFetchedBranch`. It fails without the fix.
3. **Fixed: the upgrade preview turned a version into "V…", and showed "not newer" as a success.**
   - `UpgradeReleaseNotes.jsx` capitalised every server message, so "v1.9.0 is not newer than v1.10.0" read
     "V1.9.0 …".
   - That message is the only one sent without `notInThisBuild`, and it rendered as a green success alert, even
     though upgrading onto that target goes nowhere or goes back.
   - Fix: a message that starts with a version keeps its `v`, and that case is now a warning.
   - Regression test: `UpgradeReleaseNotes.test.jsx`, "warns of a target no newer than what runs, keeping its
     version's name". It fails without the fix.

### Observations (not changed)

1. `--from`/`--to` silently ignore a value that is not a version. `grain release-notes --from bogus` prints every
   version and exits 0. `releasenotes.Between` documents this, so that a branch name or an unstamped build leaves
   that side open. A typo on the command line therefore gives no hint.
2. A preview served from cache logs nothing. The only evidence of a hit is a missing fetch/pull line and a
   response of about 1 ms.
3. A target that refuses the question puts its whole usage text into the daemon log, one line per line. That was
   100 lines for `pre-notes`, on every preview that is not cached.
4. The plan's step 3 says "`grain demo` (source path)". `grain demo` can only wire the container upgrader, so the
   source path needs a real `grain daemon -disable-native-sandboxes -upgrade-src-dir …`, as used here. That
   daemon came up in about 1 s with no other configuration.
5. The image build from `scripts/ci-docker-build.sh` cannot finish in a sandbox without help:
   - `dl.k8s.io` only resolves to IPv6 and the build fetches it with `curl -4`, which timed out after 180 s.
   - The codex CLI download from GitHub was reset 4 times.
   - Building from a `git worktree` also fails, at `git config --global --add safe.directory /src`, because the
     worktree's `.git` file points outside the build context. Build from a full clone instead.

## Fixes in this change

- `ui/src/components/ReleaseVersions.jsx`, with its test in `ui/src/components/ReleaseNotesPanel.test.jsx` (finding 1)
- `pkg/upgrade/releasenotes.go`, with its test in `pkg/upgrade/releasenotes_test.go` (finding 2)
- `ui/src/components/UpgradeReleaseNotes.jsx`, with its test in `ui/src/components/UpgradeReleaseNotes.test.jsx` (finding 3)
