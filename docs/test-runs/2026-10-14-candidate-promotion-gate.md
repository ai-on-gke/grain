# Walkthrough: candidate promotion gate in a scratch release cut (2026-10-14)

Plan: `docs/test-plans/2026-10-13.md` §4, run by task 3550 (filed by task 3492).
Under test: `scripts/candidate-gate.sh`, and how `scripts/release-candidate-cut.sh` uses it
(`517e22d8e`, `c452efde2`).

## Environment

- **Base:** `candidate` at `e1bbddcc7`. `git branch -r --contains` shows both `517e22d8e` and
  `c452efde2` on `origin/candidate` and `origin/main`. The run started on `a4674d57b`, and the
  scripts and tests under test did not change between the two.
- **Tools:** Debian sandbox, git 2.39.5, bash 5.2.15. No `gh` binary, and no `GITHUB_TOKEN`.
- **Scratch repositories:** one per case, under a temp directory, each built fresh:
  - `origin.git` is a bare repository with these refs:
    - `main`: `chore: initial commit` → `feat: eight` (annotated `v1.8.0` and `latest`) →
      `feat: nine-a…` (annotated `v1.9.0-rc.1`) → `feat: ten-a lands on main after the cut`.
    - `candidate`: `v1.9.0-rc.1` → `fix: nine-fix stabilises the v1.9 candidate`. This is a
      candidate-only fix that the back-merge must carry.
  - Commits and tags are dated three days back, so the cadence check passes on its own.
  - `clone/` is a plain `git clone` of `origin.git`. The real
    `/home/debian/work/scripts/release-candidate-cut.sh` runs in it, run with `env -u GITHUB_TOKEN`
    and `CANDIDATE_GATE_FILE` set for each case.
  - Nothing was pushed anywhere except the scratch `origin.git`.
- **Verdicts files:** the step 1b format. Placeholders are filled in with the scratch shas:
  - `HEAD`: the candidate head.
  - `MAIN`: the main-only `ten-a`.
  - `OLD`: `v1.8.0`, which is before this candidate's rc.1.
  - In every case except the live-agent ones, the four advisory suites are `pass` at `HEAD`.

## Stubs

None were needed. Nothing in the cut reaches outside git in this sandbox:

- **GitHub release drafting** (`gh release create`) and the CI check-run query (`gh api …`) only
  run when `command -v gh` succeeds and `GITHUB_TOKEN` is set. Neither holds here, so the
  script's own fallback ran: "GH CLI or GITHUB_TOKEN not available" and "Recorded draft release
  metadata".
- **Image builds** are not part of the cut. `.grain/workflows/promote.yml` retags images in
  grain CI later, and this run did not exercise it.

## Step 1–2: one cut per case

Every cut exited 0. Each row was run twice, once on the original scripts and once with the fixes
below. The results were the same both times, except that the not-in-candidate message now names
the range it checked.

| Case | "Candidate gate:" line | `v1.9.0` | `latest` | Back-merge | Next rc |
|------|------------------------|----------|----------|------------|---------|
| All pass | `promote <head>` | at head | at head | merged, pushed. `main` carries `nine-fix` | `v1.10.0-rc.1` = `main` (after the `docs(release): add release notes for v1.9.0` commit) |
| nightly `known` + reason | `promote <head>`. The nightly line quotes the reason. | at head | at head | merged | `v1.10.0-rc.1`, notes committed |
| nightly `known`, no reason | `hold <head> (nightly)`. Line: `untriaged at <head>, with no reason given; holds promotion.` | none | still `v1.8.0` | merged | `v1.10.0-rc.1` = back-merge, no notes |
| nightly `missing` | `hold <head> (nightly)`. Line: `missing at - -- still running` | none | `v1.8.0` | merged | `v1.10.0-rc.1` |
| nightly at a main-only commit | `hold <head> (nightly)`. Line: `missing at <ten-a>, which is not a commit of this candidate` | none | `v1.8.0` | merged | `v1.10.0-rc.1` |
| nightly at `v1.8.0` (an ancestor, but before rc.1) | `hold <head> (nightly)`, same message | none | `v1.8.0` | merged | `v1.10.0-rc.1` |
| live-agent `infra` + reason | `promote <head>`. live-agent is reported as `infra … -- no gemini-key on scheduled runs` | at head | at head | merged | `v1.10.0-rc.1`, notes committed |
| live-agent `regression` | `hold <head> (live-agent)` | none | `v1.8.0` | merged | `v1.10.0-rc.1` |
| No file | `Candidate gate: hold <head> -- no CANDIDATE_GATE_FILE says how …` | none | `v1.8.0` | merged | `v1.10.0-rc.1` |
| `CANDIDATE_GATE=off` | no gate line. Prints `CANDIDATE_GATE=off: promoting <head> without reading …` | at head | at head | merged | `v1.10.0-rc.1`, notes committed |

Notes on the table:

- **Every held cut** prints `Promotion held by the candidate gate: v1.9.0 is not tagged and latest
  stays where it is.` It leaves `latest` on the `v1.8.0` commit and pushes no `v1.9.0`. It still
  back-merges `candidate` into `main` and cuts `v1.10.0-rc.1`, with `candidate == main`.
- **`CANDIDATE_GATE=off` with a file set** also promotes without reading the file, as documented.

## Step 3: held cut, then promoting cut (bug 1, fixed)

1. Run the "known, no reason" case. `v1.9.0` is held and `v1.10.0-rc.1` is cut.
2. Run a second cut with `nightly <new candidate head> pass` and
   `RELEASE_CUT_NOW=<now + 1 day>`, so that it falls on the next release day.

Result: `Writing release notes for v1.10.0 (v1.8.0..v1.10.0)`. `v1.10.0` and `latest` are on the
promoted head, and `v1.11.0-rc.1` is cut. The notes do run from the newest existing release tag.

**Bug:** on the original scripts, `docs/releases/v1.10.0.md` listed `feat: nine-a` and
`feat: ten-a` under Features and `None.` under Fixes. **`fix: nine-fix`**, the held candidate's own
fix, was missing.

Cause: `release-notes.sh` drops any non-PR merge on main's first-parent line, assuming "what it
brings was released already". That holds when the previous candidate was promoted. When it was
held, the back-merge is the only way its candidate-only commits reach `main`, and they were never
released.

Reproduction: `TestReleaseCandidateCutScript_NotesAfterAHeldPromotion` in
`tests/deploy/candidate_gate_test.go`. It runs four cuts: gate off, gate off, held, gate off. On
the original scripts it fails with
`v1.2.0's notes list "fix: a fix made on the held candidate" 0 times, want once`.

Fix (`scripts/release-notes.sh`):

- A non-PR merge is still not listed itself.
- What it brings that `<from-ref>` does not carry is walked on the merged branch's own
  first-parent line (`<second parent> ^<first parent> ^<from-ref>`), with the same
  PR-title / commit / back-merge rules.
- A title is listed once, so a change that was ported between the branches does not appear twice.

After the fix, the held-then-promote notes list `fix: nine-fix` under Fixes. In the normal case
(`v1.9.0` promoted, then `v1.10.0`), `v1.10.0`'s notes are unchanged. They do not repeat
`nine-fix`, because `v1.9.0` carries it. The existing `TestReleaseCandidateCutScript_CommitsReleaseNotes`
still passes.

## Step 4: the release watchdog's invocation (bug 2, fixed)

`.grain/templates/release-watchdog.yml` §2b runs
`bash scripts/candidate-gate.sh /tmp/candidate-gate origin/candidate`. It passes no rc base.

- **Reports without tagging:** confirmed. Before and after every invocation, `git ls-remote` of
  the scratch origin and the local `git tag -l` were identical (`latest v1.8.0 v1.9.0-rc.1`). The
  script only reads git.
- **Bug:** without the base, the gate does not check the lower bound. A nightly verdict at
  `v1.8.0`, a run of the *previous* candidate, gave `Candidate gate: promote` (exit 0) under the
  watchdog's invocation. The cut's invocation of the same file, with
  `refs/tags/v1.9.0-rc.1` as the base, gave `hold … (nightly)`. So the watchdog predicted the
  opposite of what the evening cut would decide.
- **Fix (`scripts/candidate-gate.sh`):** with no base argument, the base is the newest
  `v*.*.0-rc.1` tag the candidate commit carries (`git tag --merged <commit> --sort=-v:refname`).
  This is the same tag the cut passes. The template did not need to change.
- **Message:** "not a commit of this candidate" now names the range it checked, for example
  `(refs/tags/v1.9.0-rc.1..731f93391036)`.
- **Regression test:** the "with no base given" check in `TestCandidateGate`.

## Step 5: malformed lines

Each line below was run with the cut's three-argument invocation.

| Line | Before | After |
|------|--------|-------|
| `nightly <sha> passed` | hold: `untriaged …, with verdict 'passed', which is none of pass, known, infra, regression, missing` | same |
| `nightly <sha>` (no verdict) | hold: `with verdict ''…` | same |
| `nightly` (workflow only) | hold: `untriaged at -, with verdict ''…` | same |
| `nightly pass <sha>` (columns swapped) | hold: `untriaged at pass, with verdict '<sha>'…` | same |
| `nightly <sha> pass` + CRLF | hold: `with verdict 'pass^M'`, which is unreadable because the CR is invisible in a terminal | **promote**. The CR is dropped (bug 3) |
| `nightly origin/candidate pass` | **promote**: `pass at origin/candidate` | hold: `missing at origin/candidate, which is not a commit of this candidate (…)` (bug 3) |
| `nightly HEAD pass` | hold (in the scratch clone, `HEAD` was not on the candidate) | hold |
| `live-agent <sha> regresion …` (typo) | reported `untriaged … verdict 'regresion'`, does **not** hold | same: policy question, see below |
| `live-agent <sha>` (no verdict) | reported, does not hold | same: policy question |
| `nightly <sha> pass` then `nightly - missing` | promote. `nightly: listed twice; using the first line.` | same: policy question |

Bug 3 had two parts:

- **Ref names:** the commit column took any ref. A verdict at `origin/candidate`, or at a branch
  name, names whatever the ref points at when the gate runs, not the commit a run tested, and it
  promoted.
- **CRLF:** a verdicts file with CRLF line endings held on every line, with an invisible
  `\r` in the verdict.

Fix: the commit must be a 7–40 digit hex sha, and a trailing CR is stripped from each line.
Regression tests are the "a ref named instead of the commit a run tested" and "a verdicts file
with CRLF line endings" cases in `TestCandidateGate`. Both fail on the original script.

Every malformed line for the required `nightly` is a readable refusal (a hold that says why).
For advisory suites, an unrecognised verdict is printed but does not hold. That includes a typo
of `regression`, the one verdict that is meant to hold for them. The existing test pins this as
intended ("live suites missing credentials"). Whether it should change, and whether a workflow
listed twice with conflicting verdicts should be refused, is a policy question. I filed it with
`propose_task` (depends on 3550, `branch: candidate`, not auto-merge).

## Findings summary

1. **Fixed:** release notes after a held promotion omitted the held candidate's back-merged
   fixes.
2. **Fixed:** the watchdog's gate invocation checked no rc.1 lower bound, so it could predict
   "promote" where the cut holds.
3. **Fixed:** the commit column accepted ref names, and CRLF files held with an unreadable
   verdict.
4. **Proposed (policy):** an unrecognised verdict on an advisory suite, and conflicting duplicate
   lines, are reported rather than refused.
5. **Not covered:** a verdicts file a real agent builds from `list_ci_runs` / `get_ci_log` for the
   `*-candidate.yml` runs. That needs a live cut task. The format cases it depends on (comments,
   `-` commit, multi-word reasons) parse, as shown by the "known + reason" and "missing" rows and
   the comment line in every file.

## Fixes and tests

- `scripts/release-notes.sh`: walks a non-PR merge's new commits, and lists each title once.
- `scripts/candidate-gate.sh`: defaults the base to the newest rc.1 tag, accepts sha-only
  commits, and drops CR.
- `docs/release-candidate-management.md` (promotion gate section): documents the sha-only
  commit, CRLF handling and the watchdog's default base.
- `tests/deploy/candidate_gate_test.go`:
  - two new `TestCandidateGate` cases;
  - the no-base (watchdog) check;
  - `TestReleaseCandidateCutScript_NotesAfterAHeldPromotion`.

  Each new test fails on the original scripts and passes on the fixed ones.
- `go test ./tests/deploy/ -count=1` passes locally (161 s).
- No `.grain/` file was edited.

## CI verdict

Grain CI full suite on this branch: see the pull request. The result is recorded in the task's
closing note.
