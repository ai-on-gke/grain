# The GitHub App bot

When a repository's GitHub credential is a GitHub App and the webhook gateway
is connected (see "Running grain as a standalone GitHub App bot on GKE" in `deploy/kubernetes/README.md`), grain
answers mentions of the App's slug (`@<slug>` or `@<slug>[bot]`) on issues and
pull requests. Mentions inside code spans or fenced blocks are ignored, and so
is anything the bot itself posted. The handling lives in
`pkg/orchestrator/githubevents.go` and `pkg/orchestrator/takeover.go`.

## Trust

Every command checks the sender's permission on the repository. A sender with
`admin`, `maintain` or `write` access is trusted: grain reacts with 👀 and the
task it files is approved and starts. Anyone else gets the same task filed
**proposed**, which waits for a person to approve it in grain. Each command is
deduplicated on the issue or pull request plus the comment, review or label
event that carried it, so a webhook redelivery never files twice.

## Commands

| Command | Where | What it does |
| --- | --- | --- |
| `@<slug> <anything>` | issue or PR | Files a task carrying the comment as its brief. |
| `@<slug> review` | PR | Files a review of the pull request. Its findings are posted as a GitHub review, with a `grain/review` check run. Requesting the App as a reviewer does the same. |
| `@<slug> take over` (or `takeover`, `shepherd`) | PR comment or review body | grain adopts the pull request and shepherds it to merge. See below. |
| label `grain:take-over` | PR | Same as `take over`. The daemon reads the label from `orchestrator.Deps.TakeoverLabel`; when that is empty it uses `grain:take-over`. |
| `@<slug> release` | PR | Hands a taken-over pull request back. A collaborator or the pull request's author may ask. |

On a pull request grain already owns, other mentions, `CHANGES_REQUESTED`
reviews and answers to parked questions are delivered to the owning task and
wake it.

## Taking a pull request over

`take over` files a **shepherd** task: an ordinary grain task whose own pull
request (`LinkFixes`) is the one taken over. This means the merge queue, Submit,
CI status, review threads, security holds and path triggers all treat it as
they treat a pull request grain opened itself. The task also carries
`takes-over` (`model.LinkTakeover`) naming the pull request, and a `pr-merged`
goal, so it keeps resting and waking until the pull request merges.

- **Ownership.** If a live grain task already owns the pull request (its own
  PR, or an earlier takeover), grain files nothing and replies naming that
  task.
- **Where it works.** For a branch in the repository itself, grain works on the
  pull request's own head branch. A fork's branch is out of reach of grain's
  git proxy, which pushes only to the task's own repository, even when the
  author allows maintainers to modify it. For a fork, grain copies the head to
  `grain/takeover-<number>` and opens a replacement pull request. The
  replacement links the original and credits its author, and the shepherd's
  commits carry a `Co-authored-by` trailer for that author. grain also comments
  on the original suggesting it be closed. It never closes the original itself.
  For an untrusted request the copy is made only once a person approves the
  task.
- **Untrusted input.** The pull request's description is quoted into the
  shepherd's prompt as external content. Nothing in the pull request or its
  comments can set the task's capabilities, grants, model or auto-merge.
  Auto-merge is on for a trusted request and off otherwise.

### Reviews before merge

The daemon enforces this order. The agent does not decide it.

1. Once the shepherd is approved, grain files a **code review** of the pull
   request as it stands, through the same path as `@<slug> review`. It also
   files a **security review** when the author is not a collaborator, when the
   change touches a trust-boundary path (`pkg/trustboundary`: `.grain/`,
   workflows, capabilities and so on), or when grain cannot read the file list.
   The shepherd's first run depends on these reviews, so it starts once they
   finish.
2. The merge queue refuses the pull request until every review filed for its
   **current head** has passed (`takeover-cleared`, read by the queue's review
   hold). A review passes when it finished with no findings, or when it left
   findings and the shepherd has run since, fixing them or answering them in
   their threads. If a review leaves findings, the shepherd is woken.
3. Any push moves the head: the shepherd's own push, or one by the author while
   grain holds the pull request. grain then files a fresh set of reviews for
   the new head and closes the gate until they pass. The shepherd is told never
   to force-push, and to build on top of the author's commits.
4. The `grain/review` check run on the head shows the gate: in progress while
   reviews are pending, and success once they have passed. The shepherd's
   conversation records each re-review.
5. Once the gate is clear, the normal rules apply. CI runs, then the merge
   queue merges. Security holds and repository path triggers still withdraw
   auto-merge where a person must Submit, and grain says so on the pull
   request.

grain stops filing reviews after 8 heads and raises an operator notification.
The pull request stays held until a person looks at it.

### Handing it back

`@<slug> release` from a collaborator or the pull request's author, or closing
the shepherd task in grain, hands the pull request back. The task is closed,
its goal cleared and its check run marked neutral, and grain stops pushing.
