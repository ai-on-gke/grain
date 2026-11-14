# Which pull requests should get security and targeted reviews? (2026-11-12)

Task 5053. Report only: this recommends a policy and changes none. The
operator decides, and a follow-up task makes the change.

This has two parts:

- **Part 1** measures, from git history alone, what the current policy
  fires on.
- **Part 2** measures what the reviews filed over 7 days found and cost,
  from grain's database and task records. It reuses tasks 4357 and 4358
  (the parts of the #4353 review-effectiveness study) and says where its
  numbers differ.

The recommendation and the concrete edits are at the end of part 2.

## Method (part 1)

- **Window:** pull requests merged to `main` from 2026-10-29 to
  2026-11-12 (14 days, 1099 merges), and the last 2 days on their own
  (2026-11-10 17:00 to 2026-11-12 17:00 UTC, 162 merges). The operator
  counted 164 merges in their window.
- **Files:** each pull request's files are `git diff --name-only M^1 M` of
  its merge commit `M`. They are classified with today's
  `trustboundary.Classify` (trust-boundary/v28) and today's
  `.grain/triggers/*`. Trigger paths are matched with
  `trustboundary.MatchStatePath` (`**`-aware), and `ignore:` is honoured.
  Pull requests merged before v28 or before a trigger existed are
  classified as if today's policy had applied. This is a simulation of the
  current policy, not a record of what was filed.
- **Heads:** a branch's heads are approximated by its first-parent commits
  `M^1..M^2`, split into its own commits and merges of the base. Pushes
  often carry several commits, so this is an upper bound on heads.
- **Test/doc files:** `*_test.go`, `*/testdata/*` and `*.md` files.
- **Script:** the classifier is in the appendix.

## What fires today

### Most merged pull requests get a security review

| window | merged PRs | security review (any TB category) | sensitive trigger | every-pr |
|---|---|---|---|---|
| 2 days | 162 | 121 (75%) | 30 | 162 |
| 14 days | 1099 | 729 (66%) | 138 | 1099 |

Every path a sensitive trigger watches is also on the trust-boundary list,
with one exception: `agentd-contract`'s design doc, `tests/agentdcompat/**`
and `scripts/agentd-compat.sh`. 5 pull requests in 2 days (5 in 14)
touched an agentd-contract path the list doesn't cover. So the security
review set *is* the trigger set, plus everything else.

### Trust-boundary categories (14 days; 2-day figures in brackets)

"Sole" means it was the only category the pull request touched, so
removing the category would remove the review. "Test/doc only" means every
path that matched the category was a test or doc file.

| category | PRs hit | sole | test/doc only |
|---|---|---|---|
| exec-sandbox | 382 (65) | 125 (16) | 50 (7) |
| auth-routes | 258 (46) | 54 (9) | 1 (0) |
| capabilities-secrets | 154 (24) | 34 (9) | 7 (1) |
| credential-proxy | 120 (20) | 35 (2) | 14 (3) |
| security-tests | 88 (21) | 1 (1) | 88 (21) |
| state-repo | 83 (10) | 8 (1) | 38 (3) |
| merge-gate | 67 (9) | 8 (0) | 9 (1) |
| grain-config | 53 (11) | 13 (1) | 0 |
| github-workflows | 51 (16) | 13 (3) | 0 |
| release-machinery | 51 (10) | 11 (2) | 1 (1) |
| release-promotion | 50 (9) | 7 (0) | 0 |
| hold-policy | 42 (3) | 7 (0) | 4 (1) |
| setup-scripts | 32 (6) | 3 (0) | 0 |
| upgrade-images | 29 (8) | 10 (4) | 2 |
| sandbox-images | 29 (5) | 8 (1) | 2 |
| deploy | 27 (3) | 2 (0) | 5 (1) |
| ci-suppressions | 16 (5) | 1 (0) | 0 |
| path-triggers | 11 (2) | 0 | 0 |

What drives the counts:

- **exec-sandbox through `pkg/orchestrator/`:** this path alone puts 296 of
  1099 merged PRs (27%) on the list. 81 PRs in 14 days (7 in 2 days) reach
  review *only* through it.
- **auth-routes through the route table:** `agent_api_tools.go` (77),
  `agent_api_gate.go` (76), `server.go` (68) and `route_scopes.go` (52).
  These are touched by nearly every feature that adds a route. 18 PRs in 14
  days reached review only through these four files.

### Narrowing categories saves little

- **Excluding test and doc files** (`*_test.go`, `testdata/`, `*.md`) from
  every category except `security-tests` and `hold-policy` takes 35 of 729
  PRs (5%) out of security review over 14 days, and 3 of 121 over 2 days.
  Dropping `security-tests` as well adds one more PR. Its 88 hits almost
  always come with another category.
- **Dropping `pkg/orchestrator/` from exec-sandbox** would take 81 PRs
  (11%) out. Without yield data I can't tell whether those reviews found
  anything. Part 2 finds it still does: 12 of 50 reviews with exec-sandbox
  as the only category found something, so I don't recommend it.
- **Pull requests that are entirely test or doc files:** 162 in 14 days, 18
  in 2 days. Most of them never touch the list.

### The multiplier: targeted reviews, then heads

**Security reviews.** In the 2-day window, grain filed 223 security
reviews against the 121 pull requests the list classifies: about 1.8 per
merged PR. Those PRs had 646 first-parent commits, of which 385 were their
own commits and 261 were merges of `main`. 219 of the 385 own commits
touched a trust-boundary path.

- Every review should be a full read at a head, or an incremental re-read
  of a push that touched a classified path.
- 223 reviews is about the number of commits (219) that touched one, so
  pushes and the debounce coalesce about as many heads as unchanged heads
  add.
- How many of the 223 read a head whose new commits touched no classified
  path can only be counted from the review records. Part 2 finds
  that re-reviews still find things 32–55% of the time.
- Rejected and unmerged PRs are in the 223 but not in this count.

**Targeted reviews.** 99 were filed against the **30** pull requests that
touched a sensitive trigger: **about 3.3 per PR**. Those pull requests had
260 heads between them. Two things should prevent nearly all of these:

- every one of those trigger paths, except agentd-contract's design doc,
  `tests/agentdcompat/**` and `scripts/agentd-compat.sh`, is also a
  trust-boundary path;
- `pathtriggerhold.go` already reuses the security review when it covers
  every path the trigger matched (`allPathsCovered`, since 183623b9f,
  2026-11-10).

So either that reuse isn't taking effect, or the filed targeted reviews
are not what this simulation predicts. Only 5 PRs in the window touched
an uncovered agentd-contract path. Part 2 finds the reuse misses, partly because of filing order (see
"Misses and overlap").

**Code reviews.** The `every-pr` code review has run since 2026-11-11
13:41. 139 were filed over the 85 merges since then: about 1.6 per merged
PR, plus reviews of PRs that never merged.

## Part 2: what the reviews found and cost

### Method (part 2)

- **Source:** grain's database (`grain db query`, read-only). Review tasks
  are on ai-on-gke/grain, created 2026-11-05 17:00 to 2026-11-12 17:00 UTC
  (7 days), unless marked "2d" (the operator's window, from 2026-11-10
  17:00).
- **Kind** comes from the title:
  - `Security review: …` is the security review;
  - `Review: <task>` is a targeted review, and its trigger is read from
    the `trigger:` line of the body;
  - `Review <owner>/<repo>#N` is the `every-pr` code review.
- **The reviewed change** is the review's `proposed-by` link. The head is
  the `head_sha:` line, which only targeted and code reviews carry.
- **Ran:** the review has a run that succeeded and its note isn't a
  superseded or merged-first close. Otherwise it is **not run**.
- **Outcome:** read from the review's longest closing note with keyword
  rules (`/tmp/cls2.py` in the run): **found** (found or fixed a problem,
  or asked the owner) or **clean**. Calls to `add_review_comment` and
  `request_human_review` count as questions.
- **Checking the rules:** I hand-read a seeded sample of 28 notes and
  every same-head pair below. About 1 in 10 "found" labels was really
  clean, because a reviewer had edited files locally without fixing
  anything.
- **Cost:** `task_run_usage`, recorded only for Claude runs from about
  2026-11-10, so the `$ / review that ran` figures are 2-day medians of
  the costed runs. Run hours are wall clock, CI waits included.
- **Real:** whether a finding was real is judged on the hand-read sample.
  I reuse task 4357's larger sample for the nitpick share.
- **experimental/grain** was readable and is reported separately at the
  end.

### Volume and cost per review kind (7 days)

| kind | filed | changes | per change | ran | found, % of ran | $ / review that ran | run hours | questions |
|---|---|---|---|---|---|---|---|---|
| security | 456 | 269 | 1.70 | 328 | 36% (119) | $0.69 | 206 h | 18 |
| targeted (all) | 139 | 58 | 2.40 | 37 | 54% (20) | $1.15 | 30 h | 6 |
| – agentd-contract | 18 | 7 | 2.6 | 5 | 0% | $0.78 | 2 h | |
| – grain-declarations | 46 | 27 | 1.7 | 13 | 54% | $1.32 | 13 h | |
| – release-machinery | 46 | 20 | 2.3 | 12 | 75% | $1.37 | 12 h | |
| – release-promotion | 29 | 19 | 1.5 | 7 | 57% | $0.96 | 3 h | |
| code (`every-pr`, since 11-11 13:41) | 140 | 57 | 2.46 | 97 | 54% (52) | $1.12 | 67 h | 3 |

**Volume.**
- **Operator's window:** across all repositories it had 222 security, 93
  targeted and 141 code reviews, which matches the 223/99/139 in the brief.
  Of those, ai-on-gke/grain had 210, 64 and 140, and experimental/grain had
  9 security and 29 targeted.
- **Reviews per head:** no head was reviewed twice by the same kind and
  trigger (0 duplicates among the 140 code and 139 targeted reviews that
  carry a head), so #4093 holds. Every review beyond the first on a change
  came from a head move.

**Most "filed" reviews never ran.**
- **Targeted:** 102 of 139 never ran, and 94 of those were never even
  dispatched. They were superseded by a newer head, or the change merged
  first. So the "99 targeted reviews" headline is mostly rows, not spend:
  21 ran in the 2-day window.
- **Security:** 128 of 456 never ran. 76 of those had started a run before
  being cancelled, wasting 36.5 run-hours.

**Cost per day** (2-day window):

| kind | runs/day | $/day | run-hours/day |
|---|---|---|---|
| security | ≈78 | ≈$54 | ≈44 |
| code | ≈86 | ≈$96 | ≈58 |
| targeted | ≈10 | ≈$13 | ≈6 |
| **total** | | **≈$163** | **≈108** |

Task 4358 set reversal lines of $150/day and 60 model-hours/day. We are
already over the spend line. Its run-hours were measured differently, so
the 108 isn't directly comparable to its 60.

### Were the findings real?

| sample | real | nitpick | false positive | of the real ones, security issues proper |
|---|---|---|---|---|
| security (10) | 8 | 1 | 0 | 3 (rollover closing any task under `dispatch:write`; skill install; signing-key provenance) |
| code (7) | 6 | 1 | 0 | 3 (GHCR private-blob cache readable without a grant; a real VM token sent to a test stub; socket dir looser than documented) |
| targeted (8) | 5 | 2 | 0 | 3 (signing key readable from any branch; materialized files trusted by the request; registry auth) |

Task 4357's larger sample (42 reviews) found 64% real, 36% nitpick and 0
false positives. My sample is higher on real findings but small. No sample
found a false positive.

- **Owners rarely have to decide anything.** Questions were only 27 of 456
  + 139 + 140 reviews. Security reviews push their fix to the change's
  branch, and code reviews open a stacked pull request, so a finding is
  normally fixed by the time anyone sees it.
- **Re-reviews still find things.** For security reviews, the 2nd review
  of a change found something in 25 of 79 runs, and the 3rd and later in
  20 of 49. For code reviews, 10 of 22 and 12 of 22. On long-running PRs
  each pass found a new real bug (#1325 landed 9 fix commits, most from
  reviews). Head-move re-reviews are therefore not waste, and I don't
  recommend cutting them beyond #4808's diff-unchanged heads.

**Size is the strongest yield signal.** The table shows the share of runs
that found something, by the changed lines of the reviewed PR:

| kind | ≤50 | 51–200 | 201–800 | >800 |
|---|---|---|---|---|
| security | 0/7 | 1/23 | 22/84 | 86/175 |
| code | 1/4 | 2/8 | 14/29 | 27/43 |
| targeted | – | 3/6 | 0/2 | 14/23 |

### Misses and overlap

- **Later reviews catching a miss:**
  - 27 of 209 clean security reviews (13%) were followed by a review that
    found something on the same change, 19 of them by another kind.
  - For clean code reviews it was 8 of 45 (18%).
  - These are upper bounds, because a later head may have brought the
    problem in.
  - Task 4357 covers CI and later fixes (part D): reviewed PRs were later
    fixed at 37 per 100.
- **Same head, two kinds:** this compares a security review and a code
  review of one change filed within 20 minutes of each other (57 pairs).

  | security \ code | code clean | code found |
  |---|---|---|
  | security clean | 21 | **23** |
  | security found | **4** | 9 |

  - **Same issue twice: 0 of 9.** I checked every pair where both found
    something against the fix commits that landed. In 3 of them the two
    found *different* bugs in the same area: #1291 (fork runs closing a
    repair), #1325 (the Exit-grace spool) and #1288 (carrying priority
    to proposals).
  - **The code review catches security bugs the security review passes.**
    On #1322 at 09:25, security review 4899 was clean, while code review
    4900 found that a real VM token was sent to the loopback stub. On
    #1291, code review 4849 found a fork's pull request could close the
    repair, after the security review's own fork hardening.
  - **The reverse holds too.** The security review uniquely caught the
    release manager's privilege problem (4789) and pinning re-runs to the
    task's own head rather than a fork's branch (4864, #1302).
  - **So the two kinds are complementary second reads, not duplicates.**
    Dropping either one loses unique findings.
- **Targeted vs security:**
  - 42 of 139 targeted reviews were filed within 30 minutes of a security
    review of the same change.
  - **On the same head the targeted review re-read the same paths.** On
    #1291 at `b79b83c`, targeted review 5080 (`release-promotion`) and
    security review 5086 both read `scripts/release-from-trunk.sh` with the
    same brief.
  - The reuse at `pathtriggerhold.go:213` misses because of ordering. In
    17 of the 46 nearby pairs the targeted review was filed before the
    security review existed. In the other 29 it was filed after, which
    means the reuse conditions (`trust-boundary-cleared` matching the head,
    `securityReviewReusable`) also failed.
  - In the 6 same-head targeted/security pairs, each found something the
    other did not once (one pair each way). Targeted reviews run the same generic
    security brief, so they add a second read, not a different one.

### Trust-boundary categories and triggers with no findings in the window

The cleanest signal is a security review whose change touched only one
category. The counts below are runs that were clean / found something,
over 7 days.

- **Zero findings as the only category:**
  - security-tests (1/0), github-workflows (4/0), state-repo (1/0);
  - merge-gate (1/0), sandbox-images (2/0), ci-suppressions (1/0);
  - hold-policy (2/0), upgrade-images (4/0);
  - path-triggers and trigger-templates (no runs or no findings).
  - Except for upgrade-images and sandbox-images, all of these guard merge
    gates. They stay: low volume, high stakes.
- **Classified paths all test or doc files:** 6 runs, all clean.
- **Still finding things alone:**
  - exec-sandbox (alone 38/12; 115/75 counting shared runs);
  - auth-routes (10/6);
  - capabilities-secrets (12/5);
  - release-machinery (6/4);
  - deploy (3/8).
  - `pkg/orchestrator/` is not a good narrowing candidate.
- **Triggers:**
  - agentd-contract: 0 findings in 5 runs, all spent on the generic
    security brief, which doesn't check the contract (task 4359, 6b).
  - grain-declarations, release-machinery and release-promotion all found
    things. In the sample, though, the findings were in the PR's code
    rather than in the declaration or release file itself.

### experimental/grain (7 days)

- **Security reviews:** 40 filed, 35 ran, 46% found something.
- **Targeted reviews:** 29 filed, 26 of them its own `edition-trust-boundary`
  trigger. 19 never ran, 3 found something and 5 were clean.
- The same pattern holds: most targeted rows never run.

## Recommendation

Goals: keep everything guarding credentials, grants, merge gates and
release promotion, and cut the rows that never run or that re-read what
another review of the same head already reads.

1. **Fold targeted reviews into the security review** whenever both apply
   to a head, by fixing the ordering at the root. In `judgePullRequestTriggers`
   (`pkg/orchestrator/pathtriggerhold.go`), when a gating trigger's kind
   is `system:security-review` and every path it matched is classified,
   don't file a review of its own at all:
   - append the trigger's name, paths and any trigger-specific checklist
     to the security review's body (`securityReviewBody`);
   - gate on that review's ID;
   - don't wait for `securityReviewReusable` to see one.

   Keep each trigger's own `human:` and approvers.

   Expected effect:

   | | runs/day | $/day | rows filed/day |
   |---|---|---|---|
   | targeted, now | ≈10 | ≈$13 | ≈32 |
   | targeted, after | ≤1 | ≈$1 | — |

   The ≤1 run/day left are agentd-contract changes touching unclassified
   paths, which item 2 gives a contract review of their own. experimental/grain's `edition-trust-boundary`
   should get the same treatment, but its trigger file is in that
   repository.
2. **Per-trigger edits:**
   - **`release-machinery.yml`:** delete it. It is the trust-boundary
     category of the same name, with the same paths and `Category.Review`
     brief and no `human:`. Keep the category: it guards publishing and
     `DOCKER_PAT`.
   - **`release-promotion.yml`:** keep `human: required` and add
     `template: none`. The category already files the security review,
     and a person is the gate.
   - **`agentd-contract.yml`:** keep `human: required`. Give it its own
     template, `.grain/templates/agentd-contract-review.yml`, as task 4359
     and task 4456 also propose:
     - optional fields only;
     - new behaviour behind a feature the daemon checks and degrades
       without;
     - nothing removed inside its deprecation window;
     - the contract block regenerated;
     - run `scripts/agentd-compat.sh`.

     Or set `template: none` if a person's read is enough.
   - **`grain-declarations.yml`:** keep `human: required` on
     `.grain/workflows/**`, `.grain/schedules/**`, `.grain/settings.yml`
     and `.grain/templates/**`. Templates carry grants, and a template's
     push set is already its own human-only category. Add `ignore:`
     entries for `.grain/suites/**` and `.grain/README.md`, and let a clean
     security review release them: 4 PRs in 14 days, with no finding seen.
     This one is the operator's call: the saving is small and the
     `human: required` hold costs a person's Submit, not review spend.
3. **Stop classifying test and doc files** (`*_test.go`, `*/testdata/*`,
   `*.md`) under the prefix categories, except `security-tests` and
   `hold-policy`. This is `trustboundary.categoryOf` plus a Version bump to
   v29. It saves about 5% of security reviews; the 6 runs it would have
   skipped this week were all clean.
4. **Small-change threshold for the security review.** For PRs of 200
   changed lines or fewer whose only categories are exec-sandbox,
   state-repo, upgrade-images or sandbox-images, skip the separate
   security review and append the trust-boundary paths and checklist to
   the `every-pr` code review, which already runs on that head.
   - This week that is 15 reviews (12 ran, 1 found something). It saves
     about 2 runs/day.
   - Credential, grant, merge-gate, release and setup categories keep
     their own review at any size.
   - Size is read at classification time, not merge time. The data
     measured merge size, so expect a little more to fall under the line.
   - This is the riskiest item and saves the least. I'd take it only if
     spend stays over the line after items 1–3.
5. **Keep the code review on every PR.** It has the highest yield (54%
   found, 63% on PRs over 800 lines) and catches security bugs the
   security review passes (23 code-only finds vs 4 security-only at the
   same head).
   - Skip it only for PRs whose files are all docs (`docs/`, `design/`,
     `*.md`): task 4358 measured about 9% of PRs, with no review findings
     on docs-only PRs.
   - The size data doesn't support a size cut: the 4 reviews of PRs of 50
     lines or fewer found 1 real problem.
6. **Don't cut re-reviews on head moves** beyond #4808's diff-unchanged
   heads. Second and later reviews still found something 32–55% of the
   time. The cheaper lever is not filing the review that gets superseded:
   36.5 run-hours a week were spent on security runs cancelled for a newer
   head. Wait out the 3-minute debounce on security re-reviews too, which
   `pathtriggerreview.go`'s `reviewDebounce` applies only to gating-trigger
   reviews.

### Expected counts and cost under the policy (per day, today's volume)

| kind | runs/day now | after | $/day now | after |
|---|---|---|---|---|
| security | ≈78 | ≈72 (items 3, 4, 6) | ≈$54 | ≈$50 |
| targeted | ≈10 | ≤1 (items 1, 2) | ≈$13 | ≈$1 |
| code | ≈86 | ≈78 (item 5) | ≈$96 | ≈$87 |
| **total** | ≈174 | ≈151 | **≈$163** | **≈$138** |

That is about 15% less spend and run time, almost all of it from rows that
duplicated another review of the same head. Filed rows fall more than
runs do: targeted rows go from ≈32 a day to ≈1.

The dominant cost, security and code reviews of PRs over 200 lines, is
also where the findings are: 86 of 175 security and 27 of 43 code runs on
PRs over 800 lines found something. Cutting it would cut yield, so I don't
recommend going below about $135/day without a different trade-off, such
as one combined review per head for PRs under 200 lines.

### Concrete edits (for the follow-up task)

1. **`pkg/orchestrator/pathtriggerhold.go`, `securityhold.go`:**
   - fold a security-kind gating trigger into the head's security review:
     its body lists the trigger's paths and checklist, and the gate links
     that review;
   - debounce security re-reviews like the gating reviews (items 1, 6).
2. **`pkg/trustboundary/trustboundary.go`:** in `categoryOf`, skip
   `*_test.go`, `*/testdata/*` and `*.md` for prefix and bases categories
   other than `security-tests` and `hold-policy`. Bump Version to v29 and
   update its tests (item 3).
3. **`.grain/triggers/release-machinery.yml`:** delete it (item 2).
4. **`.grain/triggers/release-promotion.yml`:** add `template: none`
   (item 2).
5. **`.grain/triggers/agentd-contract.yml`:** add
   `template: agentd-contract-review`, plus the new template
   `.grain/templates/agentd-contract-review.yml` (item 2).
6. **`.grain/triggers/grain-declarations.yml`:** add `ignore:` entries
   for `.grain/suites/**` and `.grain/README.md` (item 2, operator's
   call).
7. **`.grain/triggers/every-pr.yml`:** skip docs-only PRs. Today `paths: ["**"]` matches every file, so this needs
   a trigger option such as `skip-if-only: [docs/**, design/**, "**/*.md"]`
   (item 5).
8. **Optional:** the small-change threshold, in `holdForTrustBoundary`
   (item 4).

Edits 3–6 touch `.grain/triggers/`, which is the `path-triggers`
category: owner approval and a human Submit. Edit 2 is `hold-policy`: a
human Submit. Nothing here lives in grain/config.

### Where these numbers differ from tasks 4357 and 4358

- **Reviews per change:** 4358 measured 1.84, or 1.95 in its last 7 days,
  before #4093. Security reviews are now 1.70 per change. Code and
  targeted reviews are 2.4–2.5 per change, but all of that is head moves:
  there are no same-head duplicates.
- **Spend:** 4358 put review spend at $0.71 a review and about $47/day,
  before every-pr. It is now about $163/day, past 4358's own $150/day
  reversal line. The code review alone is about $96/day.
- **Review that never ran:** 4357 put it at 21% of reviews. I measure 28%
  of security reviews, 31% of code reviews and 73% of targeted reviews,
  most of them never dispatched.
- **Real findings:** 4357 found 64% real and 36% nitpick. My samples are
  80–86% real, but small. Neither sample found a false positive.

## Appendix: classifier

Run from the repository root as `go run ./.studytool 2026-10-29T00:00:00Z`.
It writes one JSON line per merged PR, with its files, categories,
triggers, commit counts and lines changed.

<details>

```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ai-on-gke/grain/pkg/trustboundary"
	"gopkg.in/yaml.v3"
)

type trig struct {
	Name   string
	Paths  []string `yaml:"paths"`
	Ignore []string `yaml:"ignore"`
}

type pr struct {
	Merge, Date, Num, Branch string
	Files                    []string
	Commits, Pushes, Lines   int
	Cats, Trigs              map[string][]string
}

func git(args ...string) string {
	out, _ := exec.Command("git", args...).Output()
	return string(out)
}

func main() {
	var trigs []trig
	for _, n := range []string{"agentd-contract", "grain-declarations", "release-machinery", "release-promotion", "every-pr"} {
		b, _ := os.ReadFile(".grain/triggers/" + n + ".yml")
		t := trig{Name: n}
		if err := yaml.Unmarshal(b, &t); err != nil {
			panic(err)
		}
		trigs = append(trigs, t)
	}
	sc := bufio.NewScanner(strings.NewReader(git("log", "--first-parent", "--merges",
		"--since="+os.Args[1], "--format=%H|%ad|%s", "--date=iso-strict-local", "HEAD")))
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "|", 3)
		p := pr{Merge: parts[0], Date: parts[1], Cats: map[string][]string{}, Trigs: map[string][]string{}}
		if f := strings.Fields(parts[2]); len(f) >= 6 && f[0] == "Merge" {
			p.Num, p.Branch = f[3], f[5]
		}
		p.Files = strings.Fields(git("diff", "--name-only", p.Merge+"^1", p.Merge))
		ns := strings.Fields(git("diff", "--numstat", p.Merge+"^1", p.Merge))
		for i := 0; i+2 < len(ns); i += 3 {
			var a, d int
			fmt.Sscan(ns[i], &a)
			fmt.Sscan(ns[i+1], &d)
			p.Lines += a + d
		}
		fmt.Sscan(git("rev-list", "--count", "--no-merges", p.Merge+"^1.."+p.Merge+"^2"), &p.Commits)
		fmt.Sscan(git("rev-list", "--count", "--first-parent", p.Merge+"^1.."+p.Merge+"^2"), &p.Pushes)
		for _, m := range trustboundary.Classify(p.Files) {
			p.Cats[m.Category.ID] = m.Paths
		}
		for _, t := range trigs {
			for _, file := range p.Files {
				hit := false
				for _, pat := range t.Paths {
					hit = hit || pat == "**" || trustboundary.MatchStatePath(pat, file)
				}
				for _, ig := range t.Ignore {
					hit = hit && !trustboundary.MatchStatePath(ig, file)
				}
				if hit {
					p.Trigs[t.Name] = append(p.Trigs[t.Name], file)
				}
			}
		}
		enc.Encode(p)
	}
}
```

</details>
