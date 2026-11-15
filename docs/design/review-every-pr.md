# grain reviews every pull request, and that review is the reviewed merge

Status: proposed (task 6022). Design only: nothing here is built yet.
Supersedes deferred task 4270 ("PR review watcher").

Core publishes images only for commits that reached `main` (or `release/v*`)
through a **reviewed merge**: `scripts/release-images.sh`
`check_reviewed_merge` runs `cmd/reviewed-merge`, which asks
`pkg/reviewedmerge` about every first-parent commit since the last published
image. `pkg/upgrade/reviewed.go` asks the same question before a deployment
runs an image (`docs/security/audits/2026-11-10-insider-risk.md`, finding 3;
`docs/image-signing.md`, "A signature is not a review"). A merge passes in one
of two ways:

- **grain merged it.** `merged_by` is one of `GRAIN_MERGE_LOGINS`, which only
  grain's merge credential can be, and grain merges only after its own holds
  let the change go (`docs/github-actions-ci.md`, "A merge identity of grain's
  own").
- **The forge records a review.** An `APPROVED` review of the merged head by
  an `OWNER`, `MEMBER` or `COLLABORATOR` other than the author, with no change
  request outstanding (`Policy.approvedBy`).

On 2026-11-13/14 a human contributor's four pull requests (#1488, #1489,
#1498, #1501) were merged by hand with no review. The gate held
publishing for every commit after them until the operator approved all four
after the fact (task 5591). The gate did its job. The process around it did
not: grain reviews only the pull requests it opened, and nothing reviewed
these.

**Goal.** grain reviews every pull request on a repository it works in,
whoever opened it, before it merges. A passing grain review is, by itself,
what makes the merge count as reviewed.

**Non-goals.** This doc does not change what the review checks
(`.grain/templates/pr-review.yml`, the security review). It does not make
grain push to anybody's branch uninvited, and it does not weaken the gate:
anything grain cannot vouch for still fails closed.

## What exists today

Most of the pieces are already built. They only work on pull requests grain
owns, or on ones somebody explicitly hands to grain.

| Piece | Where | What it does now |
| :--- | :--- | :--- |
| `every-pr` gating trigger | `.grain/triggers/every-pr.yml`, `docs/declarations.md` "Gating reviews" | One `pr-review` code review per (pull request, head, template), findings-only. Applies only to tasks' pull requests (`pullRequestTriggersApply`). |
| Security review | `pkg/trustboundary`, trust-boundary hold | Reviews heads that touch classified paths. |
| Review-only shepherd | `POST /api/pull-requests/adopt`, `grain pr adopt`, `pkg/orchestrator/takeover_adopt.go` | Adopts a pull request grain did not open. Files code and security reviews per head, holds `takeover-cleared` until they pass and CI is green, never pushes. |
| `@<slug> review`, `review_requested` | `pkg/orchestrator/githubevents.go` | Files a review of an external pull request. It posts a `COMMENT` review when clean, and drives a `grain/review` check run (`success` when clean, `neutral` with findings). |
| Takeover limits | `docs/github-bot.md` | 8 heads per pull request, then an operator notification. Forks are copied to `grain/takeover-<n>`. |
| Review submission | `github.RESTClient.SubmitReview` | Can post `APPROVE`. Nothing calls it with `APPROVE` today. |
| Merge identity | `merge_credential` on a git connection | grain merges as an App (`grain-merge[bot]`), so `GRAIN_MERGE_LOGINS` names no person. |

Review tasks such as 5882 and 5832 show the per-head review working on grain's
own pull requests. What's missing is (1) intake of pull requests nobody handed
to grain, and (2) a forge record of a passing review that the gate accepts.

## 1. Intake

### Every open pull request gets a review-only shepherd {#RP-1}

For a repository with intake on (§5), grain adopts every open pull request
that no live grain task owns, as a **review-only shepherd**
(`model.TakeoverReviewOnly`), through the same code path as `POST
/api/pull-requests/adopt`. All the existing machinery then applies unchanged:
`every-pr` files a `pr-review` per head, the security review runs where it
would, `human: required` triggers still wait for a person, and
`takeover-cleared`, the `grain/review` check run and the 8-head cap work as
today.

The one difference from `grain pr adopt` is that an intake shepherd is filed
with **auto-merge off** and does not submit to the merge queue on its own. The
author still decides when to merge (§2). They can merge by hand once grain
approves, or ask grain to merge it (a new `@<slug> merge` command, or Submit
in grain).

Intake is idempotent on `(repo, number)`. A pull request with a live shepherd
or owning task gets nothing new. A shepherd closed by `@<slug> release` is not
re-adopted until the pull request's head moves.

Pull requests grain opened itself skip intake, because their task already owns
them. Their reviews and gates are unchanged, and grain's merge already counts
through `GRAIN_MERGE_LOGINS`.

**This supersedes task 4270.** That watcher would have filed one read-only
review per head with no gate, no check run and no record the release gate
reads. Reusing the shepherd gives all three. Task 4270 should be closed
pointing at this doc.

### Finding pull requests: webhooks first, polling as the backstop {#RP-2}

- **Webhooks.** Where the App webhook gateway is connected (`pkg/ghwebhook`),
  `handlePullRequestEvent` gains `opened`, `reopened` and `ready_for_review`,
  which adopt the pull request. A shepherd's head-move path already handles
  `synchronize`, and the App manifest already subscribes to `pull_request`.
- **Polling.** Every repository with intake on is also reconciled on a timer,
  every 5 minutes by default: `GET /repos/{o}/{r}/pulls?state=open` with
  `If-None-Match`, so an unchanged list answers 304 and costs no rate-limit
  budget (`docs/github-api-rate-limits.md`). Polling is the only source on a
  deployment with no webhook gateway. Elsewhere it catches dropped or late
  deliveries. One reconcile per repository per tick, not per pull request.
- **Merged without a review.** The same reconcile also lists pull requests
  closed in the last 7 days (`state=closed&sort=updated`) to catch merges that
  happened before grain's review finished, or with intake off (§4).

### Forks {#RP-3}

grain reads a fork's head through the base repository's `refs/pull/<n>/head`,
which grain's git proxy can fetch. It never pushes there, so a review-only
shepherd needs no access to the fork. "Fix it for me" on a fork uses the
existing replacement pull request (`grain/takeover-<n>`, `Co-authored-by` the
author).

The author's `author_association` decides how the shepherd is filed:

- `OWNER`, `MEMBER` or `COLLABORATOR`: approved, and it runs at once.
- Anyone else: by default, filed **proposed** for a person to approve before
  any agent runs (`pr_intake_external: proposed`). On a public repository,
  anybody can otherwise spend the deployment's quota by opening pull requests.
  `pr_intake_external: auto` approves them anyway. A non-collaborator's head
  always gets the security review as well.

The pull request's title, description and comments are quoted into every
prompt as external content, as the takeover path already does. Nothing in them
can set grants, model, mode or auto-merge.

### Drafts {#RP-4}

A draft cannot be merged, so grain does not review it by default. Intake files
the shepherd when the pull request is marked ready (`ready_for_review`, or the
poll seeing `draft: false`). Setting `pr_intake_drafts: true` reviews drafts
too, for authors who want early feedback. Converting a reviewed pull request
back to draft leaves its shepherd in place but stops filing reviews until it
is ready again.

### A new head means a new review {#RP-5}

Reviews are keyed by head (`path-trigger-head:<sha>`), and grain's approval is
only ever of one head (§2):

- **A push** that adds commits files a fresh review for the new head,
  debounced as gating reviews already are. Where the old head is an ancestor
  of the new one, the review re-reads only the new commits.
- **A force-push**, where the old head is not an ancestor of the new one, gets
  a full review of `base...head`. A rewritten history has no incremental diff
  to trust.
- **A base change** (retargeting, or a merge of `main` into the branch) is a
  new head like any other.
- grain **dismisses its own approval** of the old head as soon as it sees the
  head move, rather than waiting for the forge's "dismiss stale approvals"
  (which may be off). The gate does not need the dismissal, because it already
  refuses an approval whose `commit_id` is not the merged head. The dismissal
  keeps the pull request page honest.

### Cost and quota limits {#RP-6}

Every pull request now costs at least one agent run per head. Every limit
fails closed: hitting one means **no approval**, never a default approval.

- **Per head:** the existing 8-head cap, then a notification and a hold.
- **Per repository per day:** `pr_intake_daily_reviews` (default 50). Beyond
  it, heads queue and `grain/review` says "daily review budget reached".
  Usage-limit pauses retry like any task's.
- **Priority:** intake reviews run at priority 3 (`pr_intake_priority`), below
  grain's own work, so a burst of outside pull requests cannot starve the
  merge train.
- **Stale:** an unreviewed head older than `pr_intake_max_age` (30 days) is
  not adopted until it is pushed to again.
- **Rate limits:** reviews read diffs through git. Intake adds one conditional
  list call per repository per tick, plus one reviews call per merged pull
  request checked under §4.

## 2. Making the review count

There are three options for how grain's passing review becomes a reviewed
merge.

### (a) grain posts an approving review, and the gate trusts its login

When the gates for a head clear, grain submits an `APPROVE` review of that
head (`commit_id` = head) from a dedicated **review identity**. The gates that
must clear are: every review filed for it passed, every `human: required`
trigger was released by a person, and the security review is clean where it
applies. `reviewedmerge.Policy` gains `Reviewers []string`, the forge logins
grain reviews as. Their approvals count whatever their `author_association`
says. GitHub may report an App's association as `NONE`, which
`countsAsReviewer` would otherwise drop.

- **What it trusts.** That only grain holds the review identity's key, and
  that grain approves only when its gates clear. This is the same trust
  `GRAIN_MERGE_LOGINS` already places in the merge identity.
- **The identity must not be a person's token.** grain's push credential on
  this repository is the operator's token. An `APPROVE` posted with it reads
  as the operator's own review, with `author_association: OWNER`, and **the
  gate already counts it today**. It would be indistinguishable from the
  operator approving by hand, or from anyone else holding that token. So
  approvals go out only under an App credential (`review_credential` on the
  git connection, next to `merge_credential`; it may name the same App). With
  no `review_credential`, grain posts no approval at all and falls back to a
  `COMMENT`, as today. `grain health` warns when `GRAIN_REVIEW_LOGINS` names a
  `User` account, as it does for merge logins.
- **Self-approval via grain.** The review's template and triggers are read
  from the **base** branch, never the head, so a pull request cannot weaken
  its own review. `.grain/**` edits are `human: required`, the brief is quoted
  as untrusted content, and an author's acceptance of their own findings does
  not count (RP-10). What is left is the reviewing model's own judgement
  against an adversarial diff. The same is true of every option, and of
  grain's own merges today.
- **A compromised grain credential.** Whoever holds the App's key can approve
  anything, just as the merge App's key merges anything. The mitigations are
  the same: the key lives only in grain's credential store, is never granted
  to a task, and has only *Pull requests: write* and *Metadata: read*. For
  detection, each approval body names the task and head ("Approved by grain
  task 6100 at abc1234: code review clean"), grain records an `approved-as`
  event per approval, and a nightly audit compares every approval by a
  `GRAIN_REVIEW_LOGINS` login against those events. An approval with no record
  raises an operator notification.
- **A stale head.** The gate already refuses an approval whose `commit_id` is
  not the merged head. grain also dismisses its old approval when the head
  moves (RP-5).
- **`GRAIN_MERGE_LOGINS`.** Unchanged. grain's own merges still pass as `Via:
  "grain"`. An approved pull request that a person merges by hand passes as a
  new `Via: "grain-review"`, with `By` set to the review login. The
  attestation `Record` keeps `RecordType` v1: `Via` is already a string, and
  the new `Merge` fields (`Review`, the review id, and `PostMerge`, §4) are
  optional.
- **`pkg/upgrade/reviewed.go`.** `ReviewedMergeConfig.Policy.Reviewers` comes
  from a new daemon flag, `-upgrade-reviewed-merge-reviewers`, next to
  `-upgrade-reviewed-merge-logins`. The upgrade check reads only that flag,
  never the repository variable, because a commit that rewrote the workflow
  must not be able to change what the check trusts. `grain health` warns when
  the flag and the repository's `GRAIN_REVIEW_LOGINS` disagree.
- **`scripts/release-images.sh`.** It passes `-reviewers
  "${GRAIN_REVIEW_LOGINS:-}"` to `cmd/reviewed-merge`. If the variable is
  unset, nobody's approval counts unless they are a collaborator, as today.
- **Branch protection** on `main` and `release/v*`: *Require approvals: 1*,
  *Dismiss stale approvals*, *Require approval of the most recent reviewable
  push*, required checks as today, no bypass actors, and *Do not allow
  bypassing the above settings* (admins included). Whether GitHub counts an
  App's approval toward *Require approvals* is open question 1. The gate does
  not depend on it, because it reads the review record itself. Branch
  protection is what stops the unreviewed hand merge from happening at all,
  instead of being refused at publishing time.

### (b) every pull request merges through grain's merge queue

Intake shepherds submit to the merge queue once their gates clear, and branch
protection lets only the merge App merge. Nothing new is trusted: the gate
already accepts `GRAIN_MERGE_LOGINS`, and the merge pins the head it checked
(`MergePullRequest`'s `sha`). The only bypass left is a person's Submit in
grain with `--override-gates`, which merges as grain and counts as reviewed
today (task 3904). Under (b) it has to be recorded as §4's break-glass. The
cost is that every merge goes through grain: a grain outage or a merge-queue
repair stops all merging, and contributors without grain access depend on
`@<slug> merge`.

### (c) a required `grain/review` check plus branch protection

The existing `grain/review` check run becomes a required check, and
`reviewedmerge` reads check runs at the merged head (a new `Forge` method). A
*commit status* named `grain/review` can be posted by every Actions workflow's
`GITHUB_TOKEN`, and a *check run* by any App with `checks: write`,
`github-actions` included. So the required check must be pinned to grain's App
as its expected source, and the gate must check `app.slug`. Otherwise a
workflow edit forges the review. A check is also a CI artifact, not the review
record that `pkg/reviewedmerge` was written to read.

### Side by side

| | (a) grain approves | (b) grain merges | (c) required check |
| :--- | :--- | :--- | :--- |
| Trusts | review App's key + grain's gates | merge App's key + grain's gates | check source App + grain's gates |
| Self-approval via grain | base-branch templates, author cannot accept own findings | same, plus `--override-gates` recorded | same as (a) |
| Compromised credential | review key approves anything; nightly approval audit | merge key merges anything | App or any workflow posting checks, unless source is pinned |
| Stale head | approval `commit_id` must equal merged head (already checked); grain dismisses old approvals | merge pinned to `sha` | check is per commit |
| `GRAIN_MERGE_LOGINS` | unchanged; adds `GRAIN_REVIEW_LOGINS` | the only path | unchanged; adds a check-source setting |
| `pkg/upgrade/reviewed.go` | `Policy.Reviewers` from `-upgrade-reviewed-merge-reviewers` | no change | new check-run read, pinned slug flag |
| Branch protection | 1 approval, dismiss stale, last-push approval, no bypass | restrict merges to merge App, no bypass | required check with expected source, no bypass |
| People can press Merge | yes, after approval | no | yes, after check |

### Recommendation: (a) {#RP-7}

Choose (a): grain approves from an App review identity that only grain holds,
the gate trusts `GRAIN_REVIEW_LOGINS` the way it trusts `GRAIN_MERGE_LOGINS`,
and branch protection requires one approval with no bypass.

- (a) is the literal answer to the goal: grain's review is itself the forge
  record the gate reads, and the gate keeps reading only the forge record.
- People keep the forge's Merge button, so a grain outage does not stop
  merging. A collaborator's approval still works, as it does today.
- (b) needs no new trust, but every merge depends on grain. Merging through
  the queue remains the default for grain's own pull requests, and anyone may
  choose it with `@<slug> merge`.
- (c) adds a forger (workflows) and trades the review record for a check.
  `grain/review` stays as the *visible* state (§3), but neither the gate nor
  branch protection trusts it.

## 3. Findings on someone else's pull request

### Findings are review comments, and grain never pushes uninvited {#RP-8}

A review with findings posts one GitHub review, with event `COMMENT`, and its
findings as inline comments (`add_review_comment`), as gating reviews do now.
It does not post `REQUEST_CHANGES`. Under (a), a counted change request would
block a collaborator's approval of the same head (`approvedBy`), and a person
needs to be able to overrule grain. grain withholds its approval, and the
`grain/review` check goes `neutral` with "N findings".

grain never pushes to a branch it did not create unless asked (RP-9).

### "Fix it for me" {#RP-9}

The pull request's author, or a collaborator with write, can ask grain to fix
the findings with `@<slug> fix`, the label `grain:fix`, or **Fix it for me** in
grain's action area. This upgrades the review-only shepherd to `finish` in place (the existing
`TakeoverFinish` upgrade). The shepherd pushes fix commits on top of the
author's (never a force-push), or opens a replacement pull request for a fork.
Its push moves the head, so the new head gets a fresh review: grain does not
approve its own fixes unreviewed. A request from anybody else is filed
proposed for a person to approve.

### The owner accepts findings (with task 4752) {#RP-10}

Task 4752 ("Let the owner accept a review's findings as-is without
force-merging") adds **Accept findings** to a gate with findings. Accepting
records, for each finding, who accepted it, the head, and an optional note,
then clears the review gate for that head. This doc adds one rule to it. **On
an intake pull request, acceptance by the pull request's author does not clear
the gate.** The person accepting must be a grain user with `merge:write` on
the repository who is not the author, or the forge login linked to them must
differ from the author's. Otherwise an author could clear their own findings
and have grain approve the result.

grain's approval body then says "findings accepted by <person>", and the
`approved-as` event carries it. Task 4752 builds the acceptance; the
author-is-not-acceptor rule lands with intake.

### How the author and the operator see the state {#RP-11}

- **The author, on GitHub,** needs no grain account. The `grain/review` check
  is `in_progress` while reviews run, `neutral` with findings, and `success`
  with grain's approval. Its summary names the head, each gate and the next
  step ("fix the 2 findings, comment `@<slug> fix`, or ask a maintainer to
  accept them"). Comments and grain's approval (or its dismissal on a new
  head) appear in the pull request's Reviews.
- **The operator, in grain.** The shepherd's task page in `ui-ng` uses the
  single action area with merge gates (ui-ng task 6014). For an intake pull
  request it lists **Code review**, **Security review** and any **Targeted
  review** for the current head, and **Approved at `<sha>`** once grain has
  approved. It offers *Accept findings* (RP-10), *Fix it for me*, *Re-review*
  and *Merge*. Intake shepherds rest out of the inbox: findings on someone
  else's pull request are the author's to act on. A **Pull requests** filter
  on the task list ("intake, has findings", "intake, approved, unmerged")
  shows them all. An inbox item is raised only for §4's unreviewed merges and
  for the 8-head cap.

## 4. Bypass and emergencies

A person can still merge with no grain approval and no collaborator approval:
an admin past branch protection, a ruleset with a bypass, or a repository
whose protection was never set. Under (a), that merge does not count as
reviewed, and publishing stays blocked for the merge and every commit after
it. This doc keeps that blocking and adds two ways out.

### An after-the-fact grain review clears it automatically {#RP-12}

The intake reconcile (RP-2) sees every pull request merged into a release
branch. When `reviewedmerge` would refuse one, grain does the following:

1. It raises an operator notification ("#1488 merged by <login> with no
   review: image publishing is held from <sha>").
2. It files a **post-merge review** of the merged head: the same gates and
   templates as a pre-merge head, approved without waiting. This is intake's
   only non-proposed filing for a non-collaborator's pull request, because the
   change is already on `main`.
3. If the review is clean, grain posts its `APPROVE` on the merged pull
   request at the merged head. The forge accepts reviews on merged pull
   requests, which is how task 5591 cleared #1488–#1501. The gate then passes
   it on its next run (`Via: "grain-review"`, `PostMerge: true`, because the
   review's `submitted_at` is after `merged_at`).
4. If the review has findings, nothing is approved. The notification says so
   and offers *Revert* (`grain git revert`), *Fix forward* (a task) or *Accept
   findings*. Accepting, by someone other than the author, leads to the
   approval as in RP-10.

This automates what task 5591 did by hand. It does not loosen the gate: a
commit is still published only once a review vouched for it.

### Break-glass {#RP-13}

In an emergency where the change cannot wait for grain, or grain is down, the
break-glass is the gate's existing second path: a collaborator other than the
author approves the merged head on the forge. No new trust is added, and no
file a merger could edit marks a commit as exempt.

grain records each one so that it is visible rather than silent. When the
reconcile sees a merge that passed only on a person's approval, with no grain
approval of that head, it writes a `reviewed-merge-exception` event on the
shepherd (or on a new record task when none exists) holding:

- repository, pull request, base, merged head and merge commit SHAs,
  `merged_by` and merge time;
- the approver's login, `author_association`, review id, `submitted_at`, and
  whether it came before or after the merge;
- the approval's body, which should begin `break-glass: <reason>`. When it
  does not, grain asks the approver for the reason on the pull request and in
  its notification;
- whether grain's own review of that head later passed. grain still runs one,
  as in RP-12, so that every merge eventually has grain's review on record.

The release attestation's `Merge` entry gains `Review` (the review id) and
`PostMerge`, so the published image records which path vouched for each
commit. `grain health` lists open exceptions, meaning those whose grain review
has not passed.

A person's Submit with `--override-gates` on a grain task is the same kind of
exception. It is recorded the same way, with the override's reason, until task
3904 removes it.

## 5. Rollout

### Configuration {#RP-14}

**Operator settings** live in `grain/config`, per repository, through
`api_set_repo_settings`. They are not in `.grain/`, because a pull request
must not be able to turn off its own review.

| Setting | Default | Meaning |
| :--- | :--- | :--- |
| `pr_intake` | `off` | `off`; `shadow` (adopt and review, post `COMMENT` only, never approve); `approve` (§2a). |
| `pr_intake_external` | `proposed` | `proposed` or `auto` for non-collaborator authors (RP-3). |
| `pr_intake_drafts` | `false` | Review drafts (RP-4). |
| `pr_intake_daily_reviews` | `50` | Per-repository daily cap (RP-6). |
| `pr_intake_priority` | `3` | Dispatch priority of intake reviews. |
| `pr_intake_max_age` | `720h` | Do not adopt older untouched heads. |
| `pr_intake_poll` | `5m` | Reconcile interval. |

**The git connection** gains `review_credential`, next to `merge_credential`,
naming an App credential. Without it, `approve` behaves as `shadow`, and
`grain health` says why.

**Repository declarations (`.grain/`)** need no new kind:
`.grain/triggers/every-pr.yml` and `.grain/templates/pr-review.yml` already
define the review. Its comment changes from "every grain-opened pull request"
to "every pull request". The trigger's `human: on-findings` already means what
§3 needs. The declarations are read from the base branch (§2a).

**GitHub** needs the variable `GRAIN_REVIEW_LOGINS` and §2a's branch
protection and App permissions. **The daemon** needs
`-upgrade-reviewed-merge-reviewers` beside `-upgrade-reviewed-merge-repo`.

### Phases for ai-on-gke/grain {#RP-15}

1. **Shadow.** Set `pr_intake: shadow`. Every outside pull request gets
   reviews and a `grain/review` check, but no approval. Measure cost, latency
   and false-positive findings for two weeks. Close task 4270.
2. **Approve.** Create or reuse the App, set `review_credential`, and set
   `GRAIN_REVIEW_LOGINS=<app>[bot]` and the daemon flag. Land the `Reviewers`
   support in `pkg/reviewedmerge`, `cmd/reviewed-merge`, `release-images.sh`
   and `pkg/upgrade`. Then set `pr_intake: approve`. Post-merge reviews
   (RP-12) turn on with it.
3. **Protect.** Turn on the §2a branch protection with no bypass, once open
   question 1 is answered. If App approvals do not count toward *Require
   approvals*, keep protection at "require pull request" and rely on the gate
   plus RP-12.
4. **Default.** New repositories get `pr_intake: shadow` from `grain init`,
   and the operator promotes them.

### The Google edition {#RP-16}

The Google edition (`experimental/grain`) runs on a different forge, with its
own review record and its own issue-tracker capability. Core stays
forge-neutral. Intake and approval are written against `reviewedmerge.Forge`,
`pkg/change` and the review-submission seam, so nothing here names that forge.
A Google-layer follow-up doc must cover:

- how that forge's change feed provides intake (push or poll), and its draft
  and fork equivalents;
- what that forge's approval record is, whether a non-human identity can
  approve, and how the identity is kept distinct from any person's;
- its `reviewedmerge.Forge` implementation and what counts as a reviewer there
  (the `OWNER`/`MEMBER`/`COLLABORATOR` analogue);
- where findings go: review comments on the change, and whether a finding also
  files an issue in its tracker through its issue-tracker capability;
- its branch-protection equivalent, its bypass, and the break-glass record
  there;
- whether the same intake caps fit its change volume.

### Metrics {#RP-17}

Per repository, on `/metrics` and in `grain health`:

- `grain_pr_intake_adopted_total{author_kind}`, review latency (head to
  verdict), and runs and cost per head;
- `grain_pr_merges_total{via}`: `grain`, `grain-review`,
  `grain-review-post-merge`, `person-review`, or `unreviewed`. The goal is
  `unreviewed` = 0 and `person-review` falling;
- `grain_publish_held_seconds`: how long the gate held publishing because of
  an unreviewed merge;
- `grain_pr_intake_budget_refusals_total`, and
  `grain_review_approvals_unrecorded_total`: approvals by
  `GRAIN_REVIEW_LOGINS` with no `approved-as` event. It must stay 0; anything
  else is a possible credential compromise;
- findings accepted versus fixed versus "fix it for me".

### Open questions

1. Does GitHub count a GitHub App's `APPROVE` toward *Require approvals* in
   branch protection and rulesets? This decides phase 3. Verify it on a
   scratch repository.
2. Which `author_association` does the forge report for an App's review?
   `Reviewers` makes the answer irrelevant to the gate, but tests should pin
   it.
3. One App for merges and reviews, or two? One means one key to guard, while
   two means each key has narrower permissions. Either key alone already gets
   a commit past the gate, so two Apps buy permissions, not a smaller blast
   radius.
4. Should a post-merge grain approval (RP-12) count fully, or should
   `PostMerge: true` merges also need a person within N days?
5. Should intake ever post `REQUEST_CHANGES` (for example, for security
   findings), at the cost of blocking a collaborator's override?
6. Do pull requests into non-release branches (feature branches) get intake?
   The gate ignores them, but their eventual merge into `main` does not.
