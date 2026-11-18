# Issue-tracker agents: grain tasks as issues, MCP over comments

**Status:** draft for review, nothing implemented (task 5799, from task 5606).
Normative. The decisions come from the operator's chat with the director and
are not reopened here.

**Specifies:** [`issue-tracker-agents.design.md`](../../design/issue-tracker-agents.design.md)

Grain runs its own agents today: dispatch picks a model connection and starts
the agent CLI on a host or in a run host beside `grain mcpserver`, and the
agent reaches the sandbox only through that server. This doc lets agents grain
does **not** run, such as an IDE agent session or an agent on someone's
workstation, work grain tasks. Each task is projected as an issue in an issue
tracker. The agent does all of its work by posting MCP calls as comments, and
grain posts the results back as comments. Grain still owns the sandbox,
credentials, git, CI and the merge queue, and it executes every call itself
with the sandbox agent's own tool surface.

Requirement headings carry `ITA-n` IDs
([`contributions.spec.md`](../contributions/contributions.spec.md) §3.5,
[`docs/design-docs.md`](../../../docs/design-docs.md)). Headings without one
are background, rationale or open questions.

**Non-goals:** replacing host or run-host dispatch now (§9 says what it would
take); moving task ownership into the tracker now (§2); adapters beyond the
GitHub Issues reference and a Jira sketch; new agent tools.

## 1. Architecture

Three new pieces, all in core:

- `pkg/issuetracker`: the `IssueTracker` (ITA-1) and `Notifier` (ITA-14)
  interfaces, an in-memory fake for tests, and the GitHub Issues adapter.
  Adapters register the way capabilities do (`capability.Register`), so a
  downstream layer adds its tracker without core naming it.
- `pkg/trackerbridge`: the daemon side. It projects tasks to issues, holds
  leases, parses `/grain` comments, runs them against the run's `grain
  mcpserver`, redacts and (in sidecar mode) encrypts results, and posts them.
- `cmd/grain-tracker-sidecar`: the optional agent-side process (ITA-18).

### The `IssueTracker` interface {#ITA-1}

```go
type IssueTracker interface {
    Kind() string; Limits() Limits // comment size, rate budget, can delete/restrict
    CreateIssue(ctx, IssueSpec) (IssueRef, error)
    UpdateIssue(ctx, IssueRef, IssuePatch) error // title, body, state, priority, labels
    Assign(ctx, IssueRef, Identity) error        // zero Identity = grain's own account
    GetIssue(ctx, IssueRef) (Issue, error)
    ListComments(ctx, IssueRef, after CommentCursor) ([]Comment, CommentCursor, error)
    PostComment(ctx, IssueRef, body string) (CommentRef, error)
    EditComment(ctx, CommentRef, body string) error
    DeleteComment(ctx, CommentRef) error             // may return ErrUnsupported
    RestrictComment(ctx, CommentRef, Audience) error // may return ErrUnsupported
    SearchModifiedSince(ctx, Scope, since time.Time) ([]IssueChange, error)
    Self(ctx) (Identity, error); ResolveIdentity(ctx, raw string) (Identity, error)
    CanView(ctx, IssueRef, Identity) (bool, error)
}
```

A `Comment` carries the tracker's comment ID, the author as the tracker reports
it, created and last-edited times, and the body. A `Scope` is the container
grain files into: a repository, project or component.

**GitHub Issues (reference, in core).** One repository per scope, a
`grain-task` label on every projection, 65,536-character comments, webhooks
(`issues`, `issue_comment`) through the existing `pkg/ghwebhook` receiver,
search `repo:<r> label:grain-task updated:>=<T>`. Authors can delete their own
comments. `CanView` means "collaborator, or the repository is public". The
request budget is shared with the rest of grain's GitHub use
(`docs/github-api-rate-limits.md`).

**Jira (sketch, not built in phase one).** One project or component per scope;
assignee and priority map directly; close is a configured transition; webhooks
`comment_created` and `jira:issue_updated`; JQL `labels = grain-task AND
updated >= "<T>"`; 32,767-character comments. `RestrictComment` sets a
comment's `visibility` (role or group). Deleting one's own comment needs the
"Delete Own Comments" permission.

### What the downstream-layer doc must cover

The first production tracker belongs to a downstream layer, and its adapter and
doc live there. That doc must cover:

1. Reusing the layer's existing host-run tracker client (search, comments,
   update, assignee, rate limits), not a second one, with its rate limits
   feeding `Limits` and the poll budget.
2. Whether ordinary accounts can delete or restrict their own comments (Q1,
   ITA-17); the comment size limit; whether a fenced JSON block survives
   verbatim; whether edit history is visible.
3. Push or webhooks, if any; otherwise measured poll intervals and the email
   nudge (ITA-14).
4. The ACL model behind `CanView`, setting up a notifications-off component
   with nobody CC'd, and whether lists or saved searches leak titles.
5. Mapping tracker accounts to grain principals (ITA-13), robots and people.
6. Comment retention, export and indexing, which shredding defends against
   (ITA-16), and the test component for rollout stage 4.

## 2. Source of truth and projection

### The issue is a projection of the task {#ITA-2}

Grain's database stays the source of truth. A task dispatched to a tracker
connection (ITA-21) gets an issue on its first lease, recorded in
`task_tracker_issue` (task, tracker, scope, issue, card comment, and a comment
high-water mark). Grain writes title, state, priority and assignee. A person's
edit of one of those on the issue is a **request** made as that person (ITA-13)
and checked as the matching API call would be:

| Edit on the issue  | Request                                  | If refused             |
| ------------------ | ---------------------------------------- | ---------------------- |
| close / reopen     | close / reopen the task                  | revert, comment why    |
| priority           | set the task's priority                  | revert, comment why    |
| assignee → another agent identity | end lease, lease to that identity | revert, comment why |
| assignee → anyone else | end lease, hold the task for a person | revert, comment why    |
| title, body        | none: grain restores its own text        |                        |

The bridge remembers the last value it wrote to each field, so its own writes
echoing back are never read as requests.

### Minimal body and the card comment {#ITA-3}

The issue body is fixed text:

> This is a grain task and includes MCP capabilities to implement it.
> For information, comment `/grain describe`.

Grain's first comment is the **card**: the task's prompt (built as a sandbox
agent's is, minus in-sandbox path advice) and a metadata block.

```
grain-card v1
task: 4321   attempt: 3   lease: L-9f2c   mode: sidecar   session: none
repo: acme/widgets   branch: grain/ab12/task-4321-fix-parser
grants: repo#write, api:ci-read@acme/widgets
idle-timeout: 30m   total-timeout: 2h
```

Grain edits the card at the start of each attempt (attempt, lease, `session:
none` in `sidecar` mode, and the conversation since the last attempt), edits
`session: none` to `session: open` once the lease's first `grain/session.open`
succeeds (ITA-16, ITA-18), and never posts a second card, so the prompt is
always the first comment. A prompt too long for one comment is cut there and
served whole as the MCP resource `grain://task/prompt` (`resources/read`, paged
as in ITA-6).

### Tracker as owner: what would have to change

The long-term goal is for the tracker to own tasks. That needs: the state
machine reading state, priority and assignee from the tracker, with the
database as a cache and conflicts resolved in the tracker's favour; every field
the tracker cannot hold (grants, base, model, dependencies, template fields)
stored on the issue as custom fields or a signed, authoritative card; approval
decided by tracker permissions, leaving grain's allowlist only the identity
mapping; task IDs that can be issue refs, and `pkg/staterepo` exporting
pointers instead of tasks; queue order from a tracker search fast enough to run
every dispatch cycle; and a migration that adopts existing issues. None of it
is in scope, and the bridge never assumes a projected field still holds what it
wrote, so nothing here blocks it.

## 3. Protocol: JSON-RPC 2.0 MCP in comments

### Call comments {#ITA-4}

A comment is a call if its first non-blank line starts with `/grain`. The rest
of the comment is one JSON-RPC 2.0 message or a batch array, optionally inside
one fenced code block, which grain strips so trackers that render markup keep
the JSON verbatim. Any MCP method is accepted (`initialize`, `tools/list`,
`tools/call`, `resources/list`, `resources/read`, `ping`,
`notifications/cancelled`), alongside grain's transport methods (`grain/page`
in ITA-6, `grain/pause` in ITA-12, `grain/session.open` and
`grain/session.rekey` in ITA-16, `grain/replay` in ITA-18) and two shorthands:
`/grain describe` and `/grain page <cursor>` (ITA-6).

```
/grain {"jsonrpc":"2.0","id":"a7","method":"tools/call",
  "params":{"name":"read_file","arguments":{"file_path":"work/go.mod"}}}
```

Grain answers each call comment with one reply comment:

````
grain-reply v1 to=<call comment id> ids=a7
```json
{"jsonrpc":"2.0","id":"a7","result":{"content":[{"type":"text","text":"module ..."}]}}
```
````

In sidecar mode the JSON is replaced by `grain-enc v1 <key id> <base64>`
(ITA-16). The header line stays plaintext, so routing works.

`/grain describe` returns `tools/list` for this lease plus these rules, which
should be nearly all the explanation an agent needs:

```
1. One call per comment: /grain {JSON-RPC 2.0}. A batch [...] runs in order.
2. Every request needs an "id" unique in this lease. Replies carry it.
3. The first comment (the card) is your task.
4. A reply whose result has _meta "grain/status":"accepted" comes again later.
5. A reply with _meta "grain/cursor" is cut: post /grain page <cursor>.
6. End your turn with comment_on_issue, nothing_needed, ask_question,
   request_access, request_secret or resume_later, then stop posting.
7. Only the assignee's calls run. Edited comments are not run again.
```

### Batches {#ITA-5}

A batch is a JSON array of requests, as JSON-RPC 2.0 defines it, of at most 32
elements. By default (including hand-written batches), grain runs the elements
**one after another, in array order**, and each element waits for the previous
element's **final result** before starting (never merely for its `accepted`,
ITA-10, so `[build, test]` in one batch never races). When a batch opts into
parallel execution — every request element carries `params._meta:
{"grain/parallel": true}`, as set by the sidecar on coalesced concurrent calls
(ITA-19; mixing parallel and non-parallel request elements in one batch gets
`-32600`) — its elements instead share the lease's 4-in-flight slots (ITA-10)
and start concurrently without waiting on earlier elements, queueing excess
elements in array order as slots open rather than failing with `-32004`.

In both modes, any request element still running or waiting to start 10 seconds
after the batch begins (or begins waiting on the in-flight cap) becomes
`accepted` (`"result":{"_meta":{"grain/status":"accepted"}}`, ITA-10). The
batch reply comment posts once every element is done or `accepted`, holding the
responses (final results and `accepted` placeholders) in array order; each
`accepted` element's final result posts later as its own individual reply
(`to=<batch comment id> ids=<id>`) once it runs to completion. Queued elements
waiting their turn in a sequential batch consume no extra slot beyond the
batch's running element. Notifications in a batch get no response, per the
spec. In a sequential batch, elements after a turn-ending call are answered
`-32002` and not run (in the batch reply when it finishes synchronously, or as
individual replies when an `accepted` element ends the lease); if an element of
a parallel batch ends the lease, running and queued sibling elements are
cancelled and answered `-32002`.

### Size limits and paging {#ITA-6}

A reply must fit the tracker's comment limit, less room for its header. Grain
keeps the JSON valid by cutting inside text content, never the serialized JSON:
the longest text blocks are cut first, each ending `[... truncated, N bytes
more]`, and the result's `_meta` carries `"grain/cursor": "<opaque>"`.
`grain/page {cursor}` returns the next chunk of that same result, with a
further cursor until the last. In a batch the budget is shared between elements
in proportion to size, and each cut element has its own cursor. A cursor lasts
until lease end or a grain restart (which clears the in-memory reply cache,
after which `grain/page` returns `-32005`, ITA-18). Grain's own run transcript
keeps every result whole.

### Errors, malformed, duplicate and edited comments {#ITA-7}

| Code   | Meaning                                                         |
| ------ | --------------------------------------------------------------- |
| -32700 | Parse error: the JSON after `/grain` does not parse              |
| -32600 | Invalid request: no `jsonrpc`, a request with no `id`, batch too large, mixed `grain/parallel` batch, or an edited comment |
| -32601 | Method not found                                                |
| -32602 | Invalid params: unknown tool, bad arguments                     |
| -32603 | Internal error: sandbox gone, bridge fault                      |
| -32001 | Not the assignee (ITA-13)                                       |
| -32002 | No active lease, or the lease has ended                         |
| -32003 | Refused over this transport, or session key missing (ITA-15, ITA-16) |
| -32004 | Busy or rate limited; `data.retry_after` says when to retry      |
| -32005 | Cursor unknown or expired                                       |
| -32006 | Interrupted by a grain restart, or cached reply lost across one; `data.may_have_run` |
| -32007 | Duplicate `id` in this lease                                    |
| -32008 | Session conflict on `grain/session.open` or `grain/session.rekey` (ITA-16) |

A tool that ran and failed is not a protocol error: it is a result with
`isError: true`, as in MCP. Parse and invalid-request errors carry `id: null`,
and the header's `to=` ties them to their comment. A comment that says `/grain`
anywhere but its first line is not a call.

- **Duplicates.** Each tracker comment ID runs at most once, recorded in the
  store alongside each accepted request `id` (with its call comment ID and,
  once posted, reply comment ID, never the response body) before any call runs
  (ITA-18, ITA-24), so redelivered webhooks and overlapping polls are harmless
  across restarts. A new comment reusing an `id` already in the lease's
  persisted seen-`id` set gets `-32007` and does not run.
- **Edits.** A comment runs as first seen. Edited after that, it is not run
  again, and grain posts one `-32600` reply with `data.reason: "edited"`. A
  comment edited before grain first saw it runs as seen.
- **Deletes** of call comments change nothing; the audit keeps them.

### Ordering {#ITA-8}

Within an issue, call comments start in tracker order (created time, then
comment ID), and a call comment starts only once every earlier call comment on
the issue has posted its reply, with each element either finished or accepted
as async (ITA-5, ITA-10). Within a batch comment, ordering is stricter by
default: each element waits for the previous element's final result before
starting, even after the batch reply has posted `accepted` placeholders and let
later call comments proceed, unless the batch opts into parallel execution with
`"grain/parallel": true` (ITA-5). Issues are independent of each other. Replies
(and individual async follow-ups from an earlier batch) may land out of order
relative to later calls; `to=` and `id` correlate them, never position.

## 4. Tool surface

### The sandbox agent's tools, unchanged {#ITA-9}

`tools/list` here is the run's `grain mcpserver` tool list, filtered by the
task's grants exactly as for a sandbox agent: `read_file`, `edit_file`,
`write_file`, `run_command` (and through it the `grain` CLI: `grain ci wait`,
`grain pr open`, ...), `propose_task`, `ask_question`, `comment_on_issue`,
`nothing_needed`, `request_access`, `request_secret`, `resume_later`,
`wait_for_task`, and whatever controller tools the task's api grants reach. The
agent needs no other credential. The only addition is `grain/pause` (ITA-12).
`comment_on_issue` keeps its meaning, a note on the grain task, which grain
also mirrors onto the issue as a plain comment.

### Long-running calls reply asynchronously {#ITA-10}

A call not finished 10 seconds after it starts gets an immediate reply,
`"result":{"_meta":{"grain/status":"accepted"}}`, and its result later in a
second reply with the same `id`. No tool list decides this: `grain ci wait`, a
long build and a blocking `wait_for_task` are async because they are slow.
Progress notifications are not posted (they would flood the issue); the sidecar
makes its own. `notifications/cancelled` with the request's id cancels it
(`run_command` kills its process group). At most 4 calls per lease run in
flight at once; a new call comment past the cap gets `-32004`, while elements
within a batch queue for a free slot rather than failing (ITA-5). Later call
comments proceed while an async call runs, since ITA-8 waits only for the
earlier comment's reply (`accepted` included); remaining elements of a
sequential batch still wait for each previous element's final result, whereas a
parallel batch (`"grain/parallel": true`) shares free in-flight slots
concurrently (ITA-5).

## 5. Leases, identity and permissions

### Assignment is the lease {#ITA-11}

A lease is one attempt. Dispatch picks an identity from the connection's pool,
checks `CanView`, creates the sandbox, edits the card and assigns the issue.
The lease (`task_tracker_lease`: lease ID, task, attempt, identity, start, last
call, key ID) starts when grain reads the assignment back as its own choice.

A turn-ending call (`comment_on_issue`, `nothing_needed`, `ask_question`,
`request_access`, `request_secret`, `resume_later`, a `wait_for_task` park,
`grain/pause`) ends the lease: grain answers it, assigns the issue back to
itself, ends the run with the outcome a sandbox run would have, and destroys
the session key (ITA-16). When the task is ready again (a reply, a finished
park, a met `resume_later` condition) dispatch leases it again, perhaps to
another identity, as the next attempt.

Timeouts, set per connection:

- **Idle**, 30 minutes with no call by default. Grain warns on the issue at two
  thirds of it. At expiry the lease ends as a timed-out run does: the attempt
  counts, the task is retried with backoff, and after `MaxConsecutiveFailures`
  it is held for a person.
- **Total**, 2 hours by default, like a sandbox run, with time blocked in CI
  waits credited back under the same cap.
- **Never answered**: no call within 10 minutes of assignment ends the lease as
  "not picked up". It is not charged to the task. It is charged to the
  identity, and three in a row take the identity out of the pool until a person
  re-enables it.
- Async calls still running at expiry are cancelled.

### Usage limits pause, not fail {#ITA-12}

An external agent can reach its own usage limit. It calls `grain/pause
{"until": "<time or duration>", "reason": "..."}`, which is turn-ending: the
lease ends as a usage-limit suspension (today's `SuspensionKind`), the attempt
is not charged, and a pause keyed by connection and identity holds further
leases to that identity until `until`. The sidecar sends it itself when its
agent CLI exits with a configured usage-limit message. Idle expiry is never
read as a limit.

### Identity and permissions come from the tracker {#ITA-13}

Whoever the tracker says wrote a comment made the call. Grain does not tell a
person from their agent: a person posting `/grain` by hand while assigned is
fine, and an agent acting as its person is that person.

- Only the **current assignee's** comments, during an active lease, are run.
  Anyone else's call gets `-32001` and does not run. `/grain describe` is
  answered for anyone, since it shows only what the issue already does.
- Which tasks an agent can see is the tracker's ACL on the scope. Grain never
  assigns an issue to an identity `CanView` refuses.
- `tracker_identity` maps (tracker, account) to a principal in the allowlist
  (`docs/design/access-rbac.md`). The principal's role must hold a new
  `tasks:lease` scope for the task's repository. The role decides *who* may
  work a task; the task's grants still decide *what* the work may do, as for an
  internal run.
- Every call run is audited as that principal, with lease, comment, method,
  tool, duration and outcome, in the agent API's audit stream.

## 6. Notification and latency

### `Notifier` {#ITA-14}

Behind the interface, a `Notifier` delivers "these issues in scope changed";
the bridge then reads their comments after its high-water mark. It also takes
`Nudge()` (poll now) and the number of open leases.

- **Webhooks** where the tracker has them (GitHub, Jira), with a 5-minute
  safety poll beside them for missed deliveries.
- **Adaptive polling** otherwise: one `SearchModifiedSince(scope, T - skew)`
  per tick for every grain issue, never a request per issue. The tick is 60 s
  with no lease open and 3 s while any is, easing to 10 s after a quiet minute.
  Each request spends from the adapter's rate budget. When the budget runs low
  the tick lengthens and queued replies to one issue are merged into one
  comment.
- **Email nudge** (optional): a watcher on grain's mailbox calls `Nudge()` on
  any tracker notification mail. It never parses or trusts the mail beyond
  "something changed".

### Latency and comment volume

Round trip of one call, from the agent posting it to reading the reply:

| Path                                   | Typical | Worst |
| -------------------------------------- | ------- | ----- |
| Webhook, sidecar reading every 1 s     | 2–3 s   | 6 s   |
| 3 s poll, sidecar reading every 1 s    | 3–5 s   | 12 s  |
| Loop mode, agent checking every 5 s    | 5–15 s  | 30 s  |

That adds to each tool's own run time. A typical run makes about 60 tool calls:
unbatched, about 125 comments (counting async follow-ups) and 2.5–3 minutes of
transport on webhooks, 3.5–5 minutes on a 3 s poll. With the sidecar's
coalescing (`coalesce_window: 150ms`, ITA-19), only concurrent calls emitted in
the same agent turn fold into a parallel batch (`"grain/parallel": true`,
ITA-5), while sequential turns (edits, builds, `grain ci wait`) still take one
call comment each: if roughly a third of the 60 calls fan out in parallel
exploration bursts, a run posts about 40–45 call comments (~85–95 comments in
all) and spends about 1.5–2.5 minutes in transport on webhooks (2.5–3.5 minutes
on a 3 s poll), plus up to the 10 s `accepted` cutoff whenever a fast call
shares a coalesced batch with a >10 s sibling. Coalescing pays off in parallel
exploration (concurrent reads, greps, `git log`), not in chains of dependent
edits and builds, and trims tracker rate-limit spend on busy deployments.

## 7. Secrets and confidentiality

Comments are permanent and readable by everyone who can see the issue, and
copies go to notification mail, exports and search indexes.

### Redaction and refused tools {#ITA-15}

Every result goes through grain's existing redactor (`pkg/redact`) before it is
encrypted or posted, in both modes. Refused with `-32003`: reading
capability-placed credential files or the access code (by `read_file`, or
`run_command` arguments naming them, checked against the path list
`-sandbox-hide` already uses), and controller routes that return secret
material. Everything else, including command output and environment dumps, is
redacted. `request_secret` is allowed, since the value goes into grain's secret
store through the UI, never the issue. Redaction knows only secrets grain
knows; something minted in the sandbox can slip past, which is why sidecar mode
also encrypts.

### Sidecar mode: results crypto-shredded {#ITA-16}

**Key delivery.** At lease start (card showing `session: none`, ITA-3) the
sidecar makes an ephemeral X25519 key pair in memory and posts `/grain
{"method":"grain/session.open", "params":{"pub":"<base64>"}}` (with no
`replaces`). The tracker authenticates the author as the assignee: that is the
bootstrap from tracker identity. Grain makes its own ephemeral pair, records
the key ID on `task_tracker_lease` in the store, edits the card to `session:
open`, and replies with its public key and key ID. Both sides derive `K =
HKDF-SHA256(X25519(...), salt = lease ID, info = "grain-tracker v1")`. Only
public keys are ever posted, never `K` or a wrapping of it. An identity may
also register a long-term signing key in grain (Settings, signed in as that
principal); the sidecar then signs its ephemeral key, so not even a tracker
admin rewriting the comment can sit between them. On a sidecar connection grain
posts no plaintext result: before the initial handshake, and after a grain
restart clears `K` from memory until a new `grain/session.open` re-keys the
lease, every call except `/grain describe` and `grain/session.open` gets
`-32003` (with `data.reason: "rekey_required"` once a key ID exists on the
lease).

**Results** are sealed with AES-256-GCM under `K`, a random 96-bit nonce, and
the reply's header line as associated data, so a ciphertext cannot be moved to
another call.

**Calls stay plaintext**, by choice: (a) the issue stays auditable after
shredding, since anyone who can see it reads every command and edit the agent
made; (b) grain parses calls before running them, and refusal and routing work
on plaintext; (c) a call holds only what the agent chose to write, after seeing
redacted results, while results carry raw sandbox output, which is where
secrets and logs come from. The cost is that calls can hold repository code (an
`edit_file` body), so a scope's viewers must be no wider than the repository's
readers. Encrypted calls are Q4.

**Rotation.** Every lease, and so every attempt, starts with a fresh handshake.
A lease older than an hour re-keys (`grain/session.rekey`, carrying
`"replaces": "<current key_id>"` and returning `-32008` on mismatch), updating
`task_tracker_lease.key_id`; grain drops the old key after the sidecar's next
call, which it sends only once it has read the replies sealed under it. Any
subsequent `grain/session.open` on a lease that already has a `key_id` (from a
restarted sidecar or after a grain restart, ITA-18, signed with the identity's
registered signing key when one is configured) MUST carry `"replaces":
"<current key_id>"`. If `replaces` is omitted or does not match
`task_tracker_lease.key_id`, grain refuses the call with `-32008` and leaves
any active `K`, `key_id` and in-flight state untouched, so a second sidecar on
the same identity cannot take the key. When `replaces` matches, grain derives a
new `K` under a new `key_id`, updates `task_tracker_lease.key_id`, and zeroes
any previous `K` immediately, while keeping the lease's persisted seen-`id` set
(ITA-7) and any current-incarnation paging cursors (ITA-6), in-memory reply
cache for `grain/replay` (ITA-18), and in-flight async calls (ITA-10, ITA-24),
whose later replies are sealed under the new `K`.

**Destruction.** At lease end grain zeroes its private key, `K`, paging cursors
and the `grain/replay` reply cache, all of which live only ever in memory (so
redacted plaintext results never sit in the database or its backups), and the
sidecar does the same when it closes the session. Every copy of every result
(the comment, mail, exports, indexes) is then unreadable, with nothing deleted.
That holds only if both ends keep the key in memory: a sidecar that leaks it
before closing leaks that session.

**What an auditor can still read.** On the issue: every call in plaintext, each
reply's header (which call, ids, time, key ID) and the ciphertext's length. In
grain: the audit stream (ITA-13), the run transcript with its results under
grain's normal run retention (Q5), and the commits, pull request and CI.

### Loop mode: plaintext, restricted scopes only {#ITA-17}

With no sidecar there is nothing to hold a key, so loop mode is plaintext. It
relies on redaction, and when the lease is released grain deletes its reply
comments where the tracker lets an author do that, restricts them where it can,
and otherwise leaves them. Deleting does not reach mail already sent.

Loop mode is **only for scopes whose every viewer may see the content**, which
is everything the sandbox prints, after redaction. A loop-mode connection
refuses a scope an operator has not marked `loop_ok`. We recommend a dedicated
scope with notifications off and nobody CC'd. Whether ordinary accounts can
delete or restrict comments differs by tracker: GitHub can delete; Jira can
delete with a permission and restrict by visibility; the first downstream
tracker is Q1.

## 8. Agent side

### Sidecar {#ITA-18}

`grain-tracker-sidecar` runs where the agent runs, using the agent identity's
own tracker credential (which it already has; grain issues it nothing).

```yaml
tracker: {kind: github, endpoint: https://api.github.com,
          credential: env:GITHUB_TOKEN, scope: acme/grain-tasks}
identity: alice-agent              # the assignee grain uses
max_sessions: 2
workdir: ~/.cache/grain-sidecar
agent:
  # {prompt}: the card; {mcp_config}: file naming this session's server
  command: ["claude", "-p", "{prompt}", "--mcp-config", "{mcp_config}"]
  transport: stdio                 # or http: 127.0.0.1, per-session bearer
  usage_limit_patterns: ["usage limit reached.*resets at (?P<until>.+)"]
poll: {assignments: 30s, replies: 1s}
coalesce_window: 150ms             # ITA-19
signing_key: file:~/.config/grain-sidecar/sign.key   # optional, ITA-16
```

Lifecycle: watch the scope for issues assigned to `identity`; read the card;
acquire an exclusive per-lease lockfile in `workdir`
(`<workdir>/leases/<lease-id>.lock`, held until session close so two local
processes never attach to the same lease); open or resume the session only if
the card shows `session: none` for that lease or `workdir` already holds that
lease's recovery record (otherwise another sidecar owns the lease and this
instance ignores it); do the handshake (ITA-16); fetch `tools/list`; start the
agent with an MCP server serving exactly that list; relay. On a fresh claim
(`session: none`), the sidecar writes the lease's recovery record (`lease_id`
and initial `session.open` request `id`) before posting `grain/session.open`
with no `replaces`, then records the returned `key_id` (updated on
`grain/session.rekey`); if `-32008` comes back because another sidecar won an
initial-open race, it removes that tentative record and starts no agent. While
open, `workdir` persists only non-secret recovery metadata (`lease_id`, current
`key_id`, last processed reply comment ID and pending outbound request `id`s;
never `K` or its private key, ITA-16). On a turn-ending reply, the issue being
assigned away, `-32002`, or `-32008`, it stops the agent if started, zeroes the
key, removes the session's files and releases the lockfile.

A sidecar that restarts mid-lease has lost its key, so under the lease lock it
generates a new ephemeral X25519 key pair (giving its requests a new
`s<pub8>-<n>` prefix that cannot collide with pre-restart IDs, ITA-19), reads
`key_id` from `workdir` (or from grain's plaintext reply to its recorded
initial `session.open` request `id` if it crashed before saving `key_id`), and
posts `grain/session.open` with `"replaces": "<key_id>"` to re-key the lease
(ITA-16). A live sidecar that receives `-32003` (`data.reason:
"rekey_required"`) after a grain daemon restart does the same — re-keying with
`"replaces": "<key_id>"` and replaying unanswered calls without restarting the
agent CLI.

To recover replies sealed under a lost key or async results that landed while
down **without re-running any call**, the sidecar calls `grain/replay` with
either `{"ids": ["<id>", ...]}` (up to 32 request IDs from this lease) or
`{"after": "<comment id>"}` (up to 32 replies posted on this lease after
`<comment id>`, or from lease start when `""`, returning `{"responses": [...],
"last_comment_id": "<id>", "has_more": <bool>}`). The lease's reply cache lives
**in memory only** (cleared at lease end or on a grain restart, ITA-16):
`grain/replay` re-seals cached redacted responses under the current `K`
(issuing fresh ITA-6 cursors where cut), never re-executing a tool or MCP
method. Requests still in flight (including run-host calls reattached across a
grain restart, ITA-24) return `"result":{"_meta":{"grain/status":"accepted"}}`
and post their final replies under the current `K` when they finish. Request
IDs present in the lease's persisted seen-`id` set (ITA-7) from a previous
daemon incarnation whose cached responses were lost on a grain restart return
JSON-RPC error `-32006` with `data: {"may_have_run": true}` in `"responses"`
(both for `{"ids": [...]}` and, via the persisted `(request_id,
reply_comment_id)` metadata, for `{"after": "<comment id>"}`), never
`"unknown"`, so the agent does not repeat a non-idempotent call blindly. Only
IDs absent from the persisted seen-`id` set are listed in `"unknown": [...]`,
telling the sidecar grain never started them and they may be resent under new
`s<pub8>-<n>` IDs.

### Request/response mapping {#ITA-19}

Each JSON-RPC request the sidecar posts (including `grain/session.open` and
`grain/replay`) gets an `id` unique across sidecar sessions in the lease,
`s<pub8>-<n>`, where `<pub8>` is the first 8 hex characters of `SHA-256` of
that sidecar session's ephemeral X25519 public key and `<n>` is a monotonic
per-session counter, mapped back to the agent's MCP request `id` on the reply.
Because a restarted sidecar generates a fresh key pair on `grain/session.open`,
its IDs never collide with earlier sessions' IDs in the lease's seen-`id` set
(ITA-7). Concurrent `tools/call` and `resources/*` requests arriving within
`coalesce_window` go out as one parallel batch carrying `params._meta:
{"grain/parallel": true}` (ITA-5); turn-ending calls (ITA-11),
`notifications/cancelled` and `grain/*` transport calls flush any pending batch
and go out in their own comments. Coalescing folds a single turn's parallel
fan-out (such as concurrent reads and greps) into one round trip and two
comments instead of `2N` without serializing fast calls behind slow siblings,
though at most 4 elements run at once (ITA-10) and any fast element batched
with a >10 s sibling waits until the 10 s `accepted` cutoff for the batch reply
to post (ITA-5). The sidecar reads comments after the last it saw, matches
`ids=`, decrypts and answers. An `accepted` reply becomes MCP progress
notifications when the agent sent a `progressToken`. A cut reply is paged
automatically, up to the sidecar's own limit (1 MB). `initialize` and `ping`
are answered locally; `resources/*` go to grain.

### No sidecar: the loop prompt {#ITA-20}

Settings → the connection → **Copy agent prompt** gives this text, for agents
that can post and read issue comments but run no sidecar:

```text
You are working a grain task through issue <ISSUE URL>. You do all of
your work by posting comments on that issue and reading grain's
replies. You have no other access to the code, and need none.

1. Read the issue's first comment (the card): it is your task. Then post
   a comment saying exactly:  /grain describe
   Grain's reply lists your tools and the rules.
2. To call a tool, post a comment whose first line is /grain followed by
   one JSON-RPC 2.0 request, for example
   /grain {"jsonrpc":"2.0","id":"1","method":"tools/call","params":
     {"name":"read_file","arguments":{"file_path":"work/README.md"}}}
   Use a new id every time. To send several calls in one round trip,
   post a JSON array of requests in a single comment (they run in order).
3. Wait for a comment starting "grain-reply v1" whose ids= names your
   id. Check every 5 seconds. If its result says "grain/status":
   "accepted", keep waiting for the second reply with the same id.
4. If a reply's _meta has "grain/cursor", post /grain page <cursor> and
   read the rest before acting on it.
5. Never post a secret, or anything every reader of this issue may not
   see.
6. When the task is done or you need a person, call comment_on_issue,
   nothing_needed, ask_question, request_access, request_secret or
   resume_later, then stop. If you reach your own usage limit, call
   grain/pause with "until". Post nothing after that.
```

## 9. Fit with dispatch

### A tracker connection is a model connection {#ITA-21}

A tracker-backed agent is a new connection framework, `tracker`, beside
Antigravity, Claude and Codex (`docs/connections-and-models.md`). It holds the
tracker adapter and scope, the mode (`sidecar` or `loop`), the pool of agent
identities, a per-identity lease limit, the connection's concurrency limit,
ITA-11's timeouts and the enabled flag. It advertises one model,
`tracker/<connection name>`, which tasks reach the usual ways: the deployment's
`default_model` list, a template's model or a pin. Nothing picks it implicitly.

Admission stays dispatch's: free capacity is the smaller of the concurrency
limit and the enabled identities' free lease slots; priority and Run now apply;
a pipeline pause holds it; usage-limit pauses are keyed by connection and
identity (ITA-12). Interactive tasks are not offered to tracker connections,
whose latency is wrong for chat.

### Where the sandbox lives {#ITA-22}

Where it would for an internal run on the same deployment: the task's sandbox
backend (run host, docker, kube or granule), created at lease start. What runs
in it differs: no agent CLI, only `grain mcpserver`, which the bridge drives
over the link an agent CLI's MCP client would use. In a run host that server is
a sibling of the daemon, so a daemon restart does not kill an in-flight call
(ITA-24). The sandbox is destroyed when the lease ends. An idle lease holds a
sandbox, which is why the idle timeout is short. Host placement (`run: hostd`)
is not involved, since there is no CLI to place.

### Cost and quota accounting {#ITA-23}

Grain sees no tokens. It accounts what it sees: sandbox time per lease, charged
to the task and the identity as today (`docs/sandbox-budget.md`); calls, reply
bytes and tracker requests per connection, so an operator can watch the rate
budget; and optional self-reported model usage (`notifications/grain/usage`),
shown as reported and never used for limits. Quotas per identity: open leases,
calls per hour and sandbox hours per day. Reaching one answers `-32004` or
holds new leases, and never ends a lease mid-call.

### Replacing internal dispatch later

Nothing in the protocol needs comments: a lease is a JSON-RPC channel to a
sandbox's `grain mcpserver`, with identity, ordering and shredding on top.
Replacing host dispatch would take a direct transport (the same messages over
an authenticated stream, millisecond latency); the sidecar becoming the host's
agent runner, starting the CLI as host dispatch does now; the usage-limit,
provider-capacity and OOM detection grain does from CLI output moving into the
sidecar, reported as `grain/pause` or run outcomes; transcript and token
streaming to the UI; interactive tasks; and the e2e suites passing unchanged on
it. That is a later doc.

## 10. Failure modes {#ITA-24}

- **Tracker outage.** No calls arrive and no replies post. Lease clocks stop
  while the adapter's health check fails, replies wait in a per-issue outbox in
  the store and post in order on recovery, and the connection leases nothing
  new. After an hour, open leases end as an infrastructure failure, not
  charged, and their tasks are retried.
- **Comment posted while grain restarts.** The high-water mark, the set of run
  comments and each lease's seen-`id` set (with call and reply comment IDs,
  never result bodies, ITA-7) are in the store, written before each call runs,
  while `K`, the private key, paging cursors (ITA-6) and the `grain/replay`
  reply cache (ITA-18) live only in memory. On start the bridge re-reads every
  leased issue after its mark, so nothing is lost and nothing runs twice. On a
  `sidecar` lease, grain has no `K` in memory after restart and answers every
  call other than `/grain describe` and `grain/session.open` with `-32003`
  (`data.reason: "rekey_required"`) until the sidecar re-keys with
  `"replaces": "<key_id>"` (ITA-16, ITA-18). For a call in flight at the
  restart, the bridge reattaches to its result if the run host survived
  (holding the reply in memory until `K` is re-established on a sealed lease),
  and otherwise answers `-32006` with `may_have_run: true`; `grain/replay`
  likewise answers `-32006` (`may_have_run: true`) for pre-restart seen IDs
  whose cached responses vanished with memory, keeping `"unknown"` for IDs
  never seen. Lease clocks are credited the downtime.
- **Second sidecar on the same identity.** Two sidecar processes configured
  with one `identity` (for example, workstation and laptop, or two local
  processes): an exclusive per-lease lockfile in `workdir` blocks a second
  local process, and a remote sidecar skips any issue whose card already shows
  `session: open` when its own `workdir` holds no recovery record for that
  lease (ITA-3, ITA-18). If two sidecars race while the card still shows
  `session: none`, both post an initial `grain/session.open` without
  `replaces`; tracker ordering (ITA-8) runs one first, sets `session: open` and
  records `key_id`, and the second gets `-32008` without rotating or zeroing
  `K`, removes its tentative `workdir` entry, and starts no agent (ITA-16).
- **Sandbox dies:** in-flight calls get `-32603`; the lease ends as an
  infrastructure failure, retried as today. **Agent silent:** ITA-11's
  timeouts. **Assignment race:** the lease exists only once grain reads back
  its own choice of assignee. **Rate limited:** back off, lengthen the tick,
  merge queued replies. **Lost webhooks:** the safety poll. **Issue deleted:**
  the task is held with a note; grain does not re-create an issue a person
  deleted.

## 11. UI {#ITA-25}

In `ui-ng/` only; the classic UI shows such a run as a run.

The task row and detail show an **External agent** badge, the identity, lease
age, time since the last call and a link to the issue. The run transcript shows
each call and result from grain's own redacted copy, linked to its comment;
after shredding it is where results are read. Actions: **End lease** (a
person's stop) and **Lease to…**. Settings → Connections gains the `tracker`
form, with **Copy agent prompt** (ITA-20) and per-identity enable/disable and
"not picked up" counts.

## 12. Rollout {#ITA-26}

Behind `tracker_agents_enabled` (a deployment setting, off by default) and each
connection's enabled flag.

1. **Fake tracker:** the in-memory adapter, the bridge, and an e2e test where a
   scripted agent finishes a real task over comments.
2. **GitHub, loop mode,** one `loop_ok` test repository, one identity,
   operator-filed test tasks.
3. **Sidecar and encryption** there; measure latency and volume (§6).
4. **Downstream tracker,** one notifications-off test component.
5. **Wider:** more identities and repositories, then a decision on replacing
   internal dispatch.

## 13. Open questions

- **Q1.** Can ordinary accounts delete or restrict their own comments in each
  tracker, and in the first downstream tracker in particular? That decides
  whether loop mode can clean up there at all.
- **Q2.** One lease per identity by default, or several sessions per sidecar?
  One keeps attribution simple.
- **Q3.** Is 10 s the right "accepted" threshold (ITA-10), or should it follow
  the measured round trip?
- **Q4.** Encrypted calls, opt-in per scope, for code that must not reach
  anyone beyond its readers? They cost the plaintext audit trail.
- **Q5.** Should grain's own transcript also shred results at lease end,
  keeping only hashes, or keep today's run retention?
- **Q6.** One identity-mapping table per tracker, or principals carrying
  several external accounts?
- **Q7.** Do three "not picked up" leases disable an identity, or only lower
  its weight in the pool?
