# Grain core complexity and error-risk review (2026-11)

Task 4352. This review covers `ai-on-gke/grain` `main` at `a46d68975`
(2026-11-10). The history window is 2026-09-30..2026-11-11, which is
2,601 non-merge commits. It is a report only: no product code changed,
and no follow-up tasks are filed. The list at the end is for the operator
to pick from.

## How the areas were scored

Each area has three scores:

- **E, error risk (1–5):** how many shipped bugs the structure caused or
  hid in the window, and how bad they were (security bypasses and stuck
  or corrupted state count most).
- **P, payoff (1–5):** code removed, review cost saved, and how many
  future changes stop needing the same rule written twice.
- **Effort:** S = 1, M = 2, L = 3.

**Score = (3·E + P) / Effort.** Error risk is weighted three times
because the ask weights it most heavily. Where scores tie, the area with
more attributable fix commits in the window ranks first.

"Fix commits" means non-merge commits whose subject starts with `fix`
(1,145 of 2,601, or 44%) or with `revert` (2).

## Ranked table

| # | Area | Where | E | P | Effort | Score |
|---|------|-------|---|---|--------|-------|
| 1 | One task-filing path: end-of-run proposals re-implement `CreateTask` | `orchestrator/finish.go` `relayProposedTasks`; `ui/client.go` `CreateTask` | 5 | 4 | M | 9.5 |
| 2 | One grant/capability canonicaliser at ingress | `model/canonical_grant*.go`, `capability_ref.go`, `agent_api.go`, `ci_grant_rule.go`; normalisers in `ui`, `orchestrator`, `agent/antigravity/hook` | 4 | 4 | M | 8.0 |
| 3 | Repository identity: `RepoRef` everywhere, host included | `model/task.go` `RepoRef`; 129 `EqualFold` and 98 `ToLower` compares across `hub`, `gitproxy`, `mcp`, `ci`, `ui` | 4 | 3 | M | 7.5 |
| 4 | One CI-verdict provider behind grain CI and GitHub Actions | `orchestrator/wake.go`, `model/resume_on.go`, `gitproxy/cli_actions.go`, `ui/ci.go`; 68 `CIMode*` branches | 4 | 3 | M | 7.5 |
| 5 | `ui.Config`: replace nil-means-unavailable hooks with required interfaces | `ui/labels.go` `Config` (126 fields, about 80 hooks); 106 test files build it | 4 | 3 | M | 7.5 |
| 6 | One path classifier feeding one review filer | `trustboundary/trustboundary.go` (v17→v28 in 6 days), `.grain/triggers`, `orchestrator/pathtriggerreview.go`, `securityhold*.go` | 4 | 3 | M | 7.5 |
| 7 | Authorise typed actions once, not raw JSON bodies in a gate | `ui/agent_api_gate.go` (3,915 lines), `access_roles.go`, `route_scopes.go`, `Client.agentMayApprove` | 5 | 5 | L | 6.7 |
| 8 | Task transitions as named model operations, not scattered `Observation` writes | `model/state.go` `StateOf`, `model/task.go` `Observation` (50 fields); 68 `ObserveField` call sites | 5 | 4 | L | 6.3 |
| 9 | One agent-tool registry for every MCP and CLI surface | `pkg/mcp`, `ui/mcp_endpoint.go`, `ui/agent_api_tools.go`, `cli/mcp_external.go`, `agent/stoprule`, `agent/toolroster.go`, `orchestrator/context_handover.go` | 3 | 3 | M | 6.0 |
| 10 | Split the god files that cross-merge into broken builds | `ui/client.go` (8,247 lines), `model/store.go` (7,978), `orchestrator/run.go` (5,447) | 2 | 3 | L | 3.0 |

---

## 1. One task-filing path: end-of-run proposals re-implement `CreateTask`

**What and where.** Grain files a task in two ways.

- A live call goes through `ui.Client.CreateTask` (`pkg/ui/client.go:2699`,
  **1,068 lines**). That covers the UI, the agent API, `grain_submit_task`
  and `grain task`.
- An end-of-run `propose_task` goes through
  `orchestrator.relayProposedTasks` (`pkg/orchestrator/finish.go:1628`,
  **925 lines, 143 `if`s**), with its helpers `expandProposalGrant`,
  `proposedAutoMerge`, `proposedTarget`, `proposalTargetsSourceRepo` and
  `proposalSeeding`.

The relay never calls `CreateTask`. Instead it carries at least ten
comments of the form "ui.CreateTask's rule for the identical call filed
live" (`finish.go`, relay body lines 60, 355, 465, 544, 579, 604, 633,
643, 690 and 704). In other words, every admission rule is written twice
and kept in sync by hand. `UpdateTask` (879 lines) is a third partial
copy for edits.

**Evidence.** `finish.go` had 57 commits and 30 fixes in the window. The
fixes below each brought one copy level with the other:

- `29c4e35df` honour `hold_for_approval` in the approval rail "whoever
  proposed it";
- `0cd9b9d9f` hold proposals with `keep_running` or a priority above the
  proposer's;
- `bd7de6d38` and then `669641b73` judge end-of-run proposals against the
  capability catalogue, then file them for a person when it refuses;
- `e8fa6a2d7`, then `d76d27918`, then `c4f3d5954` preserve bare
  capability labels, then expand retired aliases, then write canonical
  `api:` refs. That is three fixes in one day (2026-11-09) on one rule;
- `663e99ee4` refuse undeclared `propose_task` arguments;
- `6ecb24971` and `571aaea8a` base-branch handling for proposals;
- `fa45477f7` and `69d0da3f2` `proposalTargetsSourceRepo` tightened, then
  loosened again;
- `bb313d282` admission above standard for delegated callers. This fix
  touched both `finish.go` and `agent_api_gate.go`.

#4225 (`grain_submit_task` missing `items`, `dbc39450b`) is the same split
seen from the schema side: the live tool and `propose_task` describe the
same request in two schemas.

**Simplification.** Introduce one `FileTask(ctx, FilingRequest, Filer)`
with a typed request and a filer that is either a principal or a
proposing run. Make it the only writer of new tasks. The relay turns its
`map[string]any` into a `FilingRequest` and calls it. The "held for a
person" result becomes a return value rather than a second code path.
Delete the per-field `proposed*` helpers.

**Payoff.** This removes about 900 lines and a whole class of "the relay
forgot rule X" bugs: about 12 of the 30 `finish.go` fixes in the window.
After it, a new admission rule is one review instead of two.

**Risk and effort: M.** What could break:

- the relay's extra outcomes ("file for a person", "refused, noted on the
  task") need to fit the shared return type;
- `finish.go` is trust-boundary-classified, so the change will be held
  for a security review.

**Tests first.** Write a table-driven parity test. It takes each
`CreateTaskRequest` shape (grants, `keep_running`, priority, base, writes,
`auto_merge`, `hold_for_approval`, retired alias, catalogue refusal), files
it once live through `CreateTask` and once as a `propose_task` call
through `relayProposedTasks`, and asserts the same stored task and the
same hold or refusal. That test should fail today on at least one row.

## 2. One grant/capability canonicaliser at ingress

**What and where.** At least 20 functions turn a written grant into
"the" grant:

- in `pkg/model`: `CanonicalGrant`, `CanonicalGrantKey`,
  `CanonicalStoredGrant(s)`, `CanonicalLegacyGrant(s)`,
  `CanonicalCIGrant`, `canonicalWrittenGrant`, `normalizeCapabilityRef`,
  `NormalizeCapabilityNames`, `NormalizeCapabilityGrants`,
  `canonicalizeDeclaredGrants`, `canonicalGrantString`;
- elsewhere: `ui.Client.normalizeCapabilityID(s)`,
  `ui.Server.normalizeCapFn`, `orchestrator.expandProposalGrant(Names)`
  and `antigravity/hook.canonicalGrant`.

The retirements each added a new spelling and usually a stored-data
migration as well: `priority-dispatch`/`durable` became `admission`,
`local-scripts` became `host-script`, `repo-read:` became `repo#read`,
`github-credential:` became `repo#credential`, `github-sandbox` became
`github#sandbox-repo`, and `grain-ci` became `api:ci-read`. The migrations
are `admission_migration.go`, `hosting_migration.go`,
`legacy_grant_migration.go`, `canonical_grant_migration.go` and
`runtime_grant_retirement.go`, plus migrations 88 and 120, about 5.9k
lines of `*migration*.go` in total.

**Evidence.**

- 74 commit subjects in the window mention retire, alias or spelling.
- `e2143611d` "bind every spelling of CI grant rules" is the clearest
  case: the rule existed, but one spelling bypassed it.
- `f398bea9a` hold `repo#read`/`repo#write {repo}` "to the old rules
  everywhere".
- `d42a2715a` convert migrated local scripts "again at every start".
- `62d354351` migration 120 rewrites `grain-ci` in the capability policy.
- `6fb3ef598` and `ce4a70948` close the unified-grants slice 2 and slice 3
  gaps.
- The proposal-label chain from area 1 (`e8fa6a2d7`, `d76d27918`,
  `c4f3d5954`).

**Simplification.**

- Have one `capref.Parse(written) (Grant, error)` in `pkg/model`. It
  returns a typed, canonical `Grant` and reads one alias table (a single
  file listing `retired → canonical`).
- Call it at every ingress (HTTP decode, `.grain/` declaration parse,
  `propose_task` relay, settings import), store only canonical grants,
  and make every rule compare `Grant` values, never strings.
- Delete the other normalisers.
- Future retirements then add an alias-table row and need no migration,
  because stored data is already canonical and old spellings are read
  through the table.

**Payoff.** This prevents the "one spelling slips past the rule" class of
bypass. It removes perhaps 1–2k lines over time, and a retirement becomes
a one-line review.

**Risk and effort: M.**

- It touches `pkg/capability` and `pkg/model`, both trust-boundary paths.
- Old stored strings that only one normaliser understood could change
  meaning, so it needs a one-time audit migration that refuses unknown
  spellings loudly.

**Tests first.**

- A property test: for every alias-table row and every canonical grant,
  each normaliser that exists today returns the same canonical answer.
  This finds the divergences before they are deleted.
- A test that no exported rule function takes a grant as a `string`.

## 3. Repository identity: `RepoRef` everywhere, host included

**What and where.** `model.RepoRef.Same` (`pkg/model/task.go:316`) is the
right rule: owner and name compared case-insensitively, plus
`OffDefaultHost()`. But the code mostly compares strings:

- 129 `strings.EqualFold` calls on repo, owner or name strings and 98
  `strings.ToLower(...repo...)`, against 167 `.Same(` calls. The heaviest
  are `hub/server.go` (9), `hub/demo.go` (8), `githubsim` (7) and four
  each in `namespace`, `mcp/ci_repo_scope.go`, `hub/store`,
  `gitproxy/cli_actions.go` and `capability/ciapi/serve.go`;
- 153 `ParseRepo` call sites;
- only 12 `OffDefaultHost()` uses.

`ui.RepoNames.RepoFullName(owner, name string)` cannot take a host at all.

**Evidence.**

- #4257 / grain/task-4249 (`e58892031`): adding
  `git.example.org/team/app` stored the default host's `team/app`, a
  different repository, because `githubSpelling` passed owner and name
  only.
- `f37385297` reach host-qualified repos through the agent API.
- `c8dce0a06` apply bare `scope_owners` rules to host-qualified claims.
- `6506674f6` link PRs and issues on the repo's own git host.
- `5d2741ce7` list GitHub Actions runs of repos off the default host.
- `c02bd0db1` support 3-segment host-qualified paths in `namespace`.
- `84d12c754` keep the repo prefix on commit-tagged test-history rows.

**Simplification.**

- Make `RepoRef` (with a `Key()` that includes the canonical host) the
  only type that crosses package boundaries for "a repository". Parse at
  ingress.
- Change `RepoNames.RepoFullName` to take and return a `RepoRef`.
- Add a vet-style hygiene test (like `tests/hygiene/boundary_test.go`)
  that rejects `strings.EqualFold` and `strings.ToLower` on identifiers
  named `*repo*`, `owner` or `name` outside `pkg/model`.

**Payoff.** This removes the whole "host silently dropped" class of bug,
which corrupts the allowlist and can misroute credentials. The 7 fixes
above are one bug written seven times.

**Risk and effort: M.**

- It is mechanical but wide: the Hub API and its stored JSON use strings,
  so they need a compatible wire form.
- The lint will have false positives at first.

**Tests first.**

- Make every package's repo-matching tests carry one off-default-host
  case (`git.example.org/o/n` against `o/n`), the way
  `repo_connection_lifecycle_test.go` now does for `ui`.
- A fuzz test asserting that `SameRepoText(a, b)` agrees with
  `ParseRepo(a).Same(ParseRepo(b))`.

## 4. One CI-verdict provider behind grain CI and GitHub Actions

**What and where.** A CI verdict comes from one of two places: the
`ci_run` table (grain CI, `ci_mode: grain`), or `github_ci_job` plus
workflow runs (`ci_mode: github`). Every consumer branches on the mode
itself: 68 `CIModeGitHub`/`CIModeGrain` sites, with 47 in `model`, 35 in
`ui`, 23 in `orchestrator` and some in `sandboxcli`, `mcp`, `ci` and
`gitproxy`. The consumers are the wake and resume holds
(`orchestrator/wake.go`, `model/resume_on.go`), `grain ci wait`
(`gitproxy/cli_actions.go`, `mcp/grain_ci_wait_tool.go` at 1,454 lines,
`mcp/wait_for_checks_tool.go`), test history, release-train LKG and
pending-release watches.

**Evidence.**

- The task 4257 `resume_later` CI wait that never woke is `0d57ae344`.
  Waits read only `ci_run`, which a GitHub Actions repo never writes, so
  the task stayed deferred indefinitely.
- The follow-ups on the same mechanism are `4e47815f8` (an early wake did
  not release the named `at`; task 3557 sat for hours), `ba4e06aab` (Run
  now spent on a parking run), `b8d00b09d` (`resume_later` stoprule),
  `96094facb` and `fbf3a1ab2`.
- `ci wait` fixes: `389b54b37`, `582f2cd15`, `5039debd4`, `2d3beae0a`.
- Mode-specific fixes in other consumers: `b43acac2f` (test-history),
  `78cae2100` (pending releases), `77edde272` (LKG from Actions),
  `5027ff6c0`, `8def651f1`, `3ab3c2d87` (cancel grain CI runs when the
  mode is `github` or `none`), and `d69f5e6d3`, a security hold that did
  not hold on Actions repos.

**Simplification.**

- Define `ci.Verdicts` with three methods: `Head(repo, branch)`,
  `Run(id)` and `Since(repo, branch, t)`. Give it two implementations
  (grain and GHA) chosen once per repo from the effective `ci_mode`.
- Consumers stop reading tables or `ci_mode` directly. Wake holds become
  "re-evaluate `Verdicts` for these keys" with one stall bound.

**Payoff.** About ten consumer fixes in the window came from a consumer
that knew only one backend. With the interface, a new consumer gets both
backends for free.

**Risk and effort: M.**

- `pkg/ci` and `gitproxy` are trust-boundary paths.
- The GitHub side is polled (`recordPolledChecks`), so verdict freshness
  semantics must be explicit.

**Tests first.** A conformance suite run against both implementations,
covering pending → success, a failure, a superseded run, a cancelled run,
an unknown run id and an off-default-host repo. Wake, `ci wait` and
test-history should each be driven from both through the suite, not only
from grain CI as most existing tests are.

## 5. `ui.Config`: replace nil-means-unavailable hooks with required interfaces

**What and where.** `ui.Config` (`pkg/ui/labels.go:139–1192`) has
**126 fields**. About 80 of them are optional function or interface hooks
(`RepoNames`, `PullRequests`, `GitHubBranches`, `RepoComparer`,
`IssueImporter`, `Wake`, `TaskCanceller`, `SandboxProbe`, `WaitCredit`,
...). 36 doc comments in the struct define nil as meaning "unavailable",
and `pkg/ui` has 122 `== nil` guards on them. 106 `pkg/ui` test files
build a `Config` literal, and before `e58892031` none of them set
`RepoNames`.

The same pattern exists outside `ui`. The `WithGitHubAccess` option on the
three agent frameworks is passed by `daemon/agents.go`, and its absence
makes `pull_request_status` and `wait_for_checks` answer "this run has no
GitHub repository configured for it". `pkg/cli/daemon_github_access_test.go`
says in so many words that this message "is exactly what makes losing it
invisible". This deployment's own run notes report that runs placed on
remote hosts still get that error from those tools most of the time,
because the forked `mcpserver` there has no data directory to read. That
is the same nil-means-unavailable shape, one process boundary further
out.

**Evidence.**

- #4257: `githubSpelling`'s real path was never run by a test.
- `97978da45` guard a nil GitHub client on finish.
- `ReopenBecause` calls `c.Config.PullRequests.(PullRequestReattacher)`
  through a type assertion, so a fake without that method skips
  re-attachment without anyone noticing.

**Simplification.**

- Group the hooks into a few required interfaces (`Forge`, `Scheduler`,
  `Sandboxes`, `Host`).
- Have explicit `Unavailable{Reason}` implementations that return a typed
  error the UI can show. Make construction fail without them.
- Add `uitest.NewConfig(t)` to fill every interface with recording fakes,
  so a test has to opt *out* of a dependency, not in.
- Replace the optional-method type assertions (`PullRequestReattacher`)
  with methods on the interface.

**Payoff.**

- Tests exercise the real branch by default, which removes the class of
  bug that #4257 shows.
- The "deployment lost the wiring" class becomes a startup error instead
  of a misleading run-time message.
- It removes the 122 nil guards.

**Risk and effort: M.** The change is mechanical but touches 106 test
files. Use `grain demo` and the sandboxes' UI as a check that the
`Unavailable` cases read correctly.

**Tests first.**

- A reflection test that fails when any `ui.Config` field is a nil-able
  hook not listed in an explicit allowlist. Shrink the allowlist to zero
  over the refactor.
- An e2e check that a run placed on a remote host gets a working
  `pull_request_status` (or a clear "not supported on this host" refusal).

## 6. One path classifier feeding one review filer

**What and where.** Three mechanisms decide that touching a path needs a
review:

- the hand-kept list in `pkg/trustboundary/trustboundary.go` (1,274
  lines, 145 path entries, `Version = "trust-boundary/v28"`);
- `.grain/triggers/*` path triggers with `human: required`;
- the security hold (`orchestrator/securityhold*.go`).

`orchestrator/pathtriggerreview.go` (new in `183623b9f`) now merges the
results.

**Evidence.**

- `pkg/trustboundary` has the highest fix ratio of any package: 24 fixes
  in 38 commits (63%).
- The list moved from v17 to v28 between 2026-11-05 and 2026-11-10, each
  step a "classify X" fix after something sensitive merged unclassified:
  `91203a8ed` (role gate, scope sets, approval callbacks), `1b273897e`,
  `928c3e36d`, `7841dafe9`, `a6b47b92d`, `6f2279289`, `a601a9f05`
  (`canonical_grant.go`, `auto_submit.go`), `6edb28484`, `c9d98dd66` and
  `63a29270f`.
- Case handling had its own fix: `f462f12f4` (upper-case `.YML`
  templates).
- Duplicate reviews per PR head (#4093): two triggers matched one head and
  filed two identical security reviews (#4070, #4071). This was fixed in
  `183623b9f`, then followed immediately by `ac5b25f95` "close gating
  review and incremental re-review bypasses".

**Simplification.**

- Classify by directory with deny-by-default. Every file under
  `pkg/{ui,capability,access,gitproxy,ci,runhost,staterepo,trustboundary}`
  and `scripts/`, `.github/` and `deploy/` is trust-boundary unless an
  explicit exemption names it.
- Make trust-boundary one *kind* of path trigger, so that `.grain/triggers`
  and the in-code list produce the same `(kind → paths)` value and one
  filer, keyed by `(PR, head, kind)`, consumes it.

**Payoff.** No more "classify X" catch-up commits, which were 10 in 6
days. Each of those was a window in which a sensitive file could merge
itself. It also gives one dedup rule for reviews instead of a merge step
on top of two producers.

**Risk and effort: M.**

- Deny-by-default will over-hold at first, which means more human
  reviews until the exemptions settle.
- The package itself is human-review-gated.

**Tests first.**

- A hygiene test that walks `git ls-files` and fails for any file under a
  sensitive directory that is neither classified nor exempt. This would
  have caught every v17→v28 step.
- A test that two triggers of the same kind on one head file exactly one
  review.

## 7. Authorise typed actions once, not raw JSON bodies in a gate

**What and where.** Access decisions are made in an HTTP-layer gate that
re-parses the raw request body:

- `ui/agent_api_gate.go` (3,915 lines) re-parses it with
  `orderedJSONFields`, `firstMatchingJSONKey`, `grantNamesFromField` and
  `bodyExplicitlyDisablesAutoMerge`;
- `ui/access_roles.go` and `route_scopes.go` add their own checks;
- the handlers then decode the same body again with `encoding/json`.

The same rule is also restated per entry point:

- `Server.agentApprovalRail`, `Server.externalApprovalRail`,
  `Server.roleApprovalWithin` and `Client.roleApprovalWithin`;
- `Client.agentMayApprove` and `Client.ownerTrustApproves`;
- and separately for approve, run-now, PR submit, PR adoption, session
  edits and access requests.

That is "two answers depending on which button".

**Evidence.** The four gate files had 69 fixes in the window, 20 of them
`fix(security)` or "bypass". There are 58 `fix(security)` commits
repo-wide. `agent_api_gate.go` alone had 52 fixes in 102 commits, and the
gate files appear in 4 of the 31 review-branch merges. Two kinds of fix
stand out.

Parser differential, where the gate and the handler read a different
body:

- `b858ed7f7` "fold agent-gate body keys the way encoding/json does";
- `9e91483cb` "close proposalKey and case-folded PATCH bypasses";
- `1116754ee` `priorityDispatch` on PATCH as a grant edit.

The same rule written once per button:

- `0fe934e8c` "make run-now's implicit approval need `tasks:admin` …
  exactly as approve does", then `6875630ea` and `65abff02d`;
- `cb754045b` "`grain pr submit` is refused … as task Submit is";
- `bba6370cc` autoMerge and approval rails on PR adoption;
- `94c1c7529` role ceilings "across task edits, access requests, merges
  and sessions";
- `f9f6e295b`, `6100fd659`, `22c9a236b`, `b14eb844d`, `f99c370c5`.

Merge crossings in the rails broke the build three times on 2026-11-10
(`578a81eab`, `77d35794d`, `a5da92bc2`).

**Simplification.**

- Decode once. Then call `authz.Decide(actor, Action)`, where `Action` is
  a closed sum of typed operations (`FileTask{req}`, `EditTask{id, diff}`,
  `Approve{id, implicit bool}`, `Submit{id}`, `AdoptPR{...}`, ...).
- Put every rail (role ceiling, agent, external credential, owner trust,
  hold-for-approval) in that function, in a fixed order.
- Run-now becomes `Approve{implicit: true}` and so cannot diverge.
- The gate shrinks to route-scope checks that read no request body.

**Payoff.** This is the largest single source of security fixes. It ends
the parser differentials by construction and turns "is X allowed?" into
one table to review. It should remove 2–3k lines across `ui`.

**Risk and effort: L.**

- It is the highest-stakes refactor on this list. Every route has to
  migrate, and a gap in the migration is an authorisation hole.
- The files are human-review-gated trust-boundary paths.
- Do it route by route behind the decision table, with the old gate still
  running in shadow mode and logging disagreements.

**Tests first.**

- A matrix test: actor kinds (person by role, agent with grants, external
  `gma_` credential, director child) × actions × entry points (HTTP,
  agent MCP, CLI). It asserts the same decision for the same `Action`
  whatever button produced it.
- A body-shape fuzz test showing the gate and the handler see the same
  fields (duplicate keys, case folding, unknown keys).

## 8. Task transitions as named model operations, not scattered `Observation` writes

**What and where.** A task's state is derived by `model.StateOf`
(`pkg/model/state.go:146`) from field precedence over a `Task` and an
`Observation`. `Observation` has **50 fields**, such as `CompletedAt`,
`ClosedAt`, `RetryRequestedAt`, `DeliveredCommentID`,
`PendingQuestionCommentID`, `PendingSecret` and `PendingAccess`. `Task`
adds `DeferredAt`, `DeferredUntil` and `UndeferredAt`, and suspensions
carry their own `resume_at`.

Transitions are hand-written field edits in 68 `ObserveField` call sites:

- 10 in `ui/client.go` (`reopenAcknowledgeable`, `retry`, `ReopenBecause`,
  `AddCommentReopening`, `RunNow`, ...);
- 10 in `demo`;
- the rest across `change/submit.go`, `ui/messages.go`, `ui/ci.go`,
  `orchestrator/sync.go` and `session_sandboxes.go`.

Booleans also encode states. `AutoMerge` doubles as "submitted"
(`AwaitsSubmit` says Submit "reuses it rather than adding a second
field").

**Evidence.**

- #4278 (`f15e32e60`): a comment left on a closed task was never
  delivered on reopen. `reopenAcknowledgeable` and `ReopenBecause` each
  wrote part of the transition, and the invariant "a person's undelivered
  comment means a queued run" lived in neither.
- `4e47815f8` (a wake cleared the deferral but not the suspension's
  `resume_at`).
- `531c1e066` "take Retry's stop check from RequestActor too".
- `ea6eb9bd0` (standing closure stamp when closed again).
- `552c615e9` (parked on a question over an unseen message).
- `5fa17498a` (dropped comments in interactive sessions).
- `c9ba53383`, `ba4e06aab`.
- Docs and code disagree as well: `AwaitsAck`'s comment and 9 others name
  a `StateAwaitingAck` that does not exist. `StateOf` returns
  `StateAwaitingReply`.

**Simplification.**

- Move transitions into `pkg/model` as named operations: `Reply`,
  `Reopen`, `Retry`, `RunNow`, `Wake`, `Defer`, `Undefer`, `Close`,
  `Park`. Each is the only writer of the fields it touches, and each
  checks invariants after it writes ("undelivered person comment ⇒
  queued, or explicitly not delivered").
- Collapse deferral, suspension `resume_at` and the wake flags into one
  `Hold{kind, until, wakeOn}` value.
- Give "submitted" its own field instead of reusing `AutoMerge`.

**Payoff.** This removes the class of bug where a task is stuck because
two writers each did half a transition, which is the most expensive kind
to diagnose (tasks sat for hours). The UI and orchestrator then call
operations rather than editing fields.

**Risk and effort: L.**

- State semantics are subtle, as `StateOf`'s comments show.
- A stored-data migration is needed for the hold merge.
- The `demo` seeding writes fields directly and has to move too.

**Tests first.**

- A model-level state-machine property test: random sequences of
  operations (comment, close, reopen, retry, run now, defer, wake, CI
  verdict, park) and an invariant checker after each step. The minimum
  invariant is "no task with an undelivered person comment is in a state
  nothing dispatches".
- Run it against the current code first. It should rediscover #4278 if
  that fix is reverted.

## 9. One agent-tool registry for every MCP and CLI surface

**What and where.** Agent tools are declared on four MCP surfaces:

- the sandbox `mcpserver` (`pkg/mcp`, about 23k lines of tool files);
- the controller (`ui/mcp_endpoint.go` and `ui/agent_api_tools.go`);
- capability toolsets;
- `grain mcp` (`cli/mcp_external.go`).

The in-sandbox `grain` CLI (`sandboxcli`) mirrors many of the same tools.
Tool *roles* are hand-kept lists in at least eight places. `"ask_question"`
is written as a literal 18 times, in `agent/stoprule`,
`agent/toolroster.go`, `agent/agent.go`, `antigravity/hook` (two lists),
`orchestrator/context_handover.go` `parkingTools`, `orchestrator/finish.go`,
`mcp/registry.go:337` and `mcp/toolnames.go`. These lists differ, and
nothing records whether that is on purpose: `isTurnEndingTool`
(`mcp/registry.go:335`) omits `wait_for_task`, which `parkingTools`
includes.

**Evidence.**

- #4225 (`dbc39450b`): `grain_submit_task` declared an array without
  `items`, Gemini rejected the whole tool list, and the fix had to audit
  all four surfaces by hand.
- `b8d00b09d` (`resume_later` stoprule).
- `b4b88860c` (read `resume_later`'s condition past grain's notices).
- `663e99ee4` (undeclared `propose_task` args).
- `b4744d6e3` (optional booleans and `minItems` in grant args).
- `sandboxcli` has a 59% fix ratio (30 of 51), and `help.go` had 17 fixes
  in 34 commits.
- `pkg/mcp` has 38 fixes.

**Simplification.**

- Keep one Go table of tools: name, JSON schema, the surfaces that serve
  it, and role flags (`ends_turn`, `parks`, `requires_success`,
  `deny_in_hooks`).
- Generate the schemas and every role list from it, the way
  `design/spec/cli/*.spec.md` is generated for the CLI.
- Have one schema linter (`items` on arrays, no undeclared properties)
  run over every surface's served list.

**Payoff.** It ends "added to three lists, forgot the fourth". A
schema-shaped outage like #4225 becomes a test failure instead of a
model-provider rejection in production.

**Risk and effort: M.**

- The agent-framework hooks (antigravity, claude, codex) have quirks, so
  per-framework name mangling must stay.
- The CLI spec generation has to keep working.

**Tests first.**

- Extend `pkg/cli/mcp_array_items_test.go` and
  `pkg/ui/mcp_endpoint_test.go` into one test that enumerates the
  *served* tool list of every surface and lints it.
- A test asserting that every hand-kept role list equals the registry's
  (failing today on `wait_for_task`, unless the difference is
  intentional, in which case it should be a flag).

## 10. Split the god files that cross-merge into broken builds

**What and where.**

| File | Lines | Commits in window | Fixes in window | Notes |
|------|-------|-------------------|-----------------|-------|
| `pkg/ui/client.go` | 8,247 | 135 | 81 | 107 `Client` methods; `CreateTask` 1,068 lines, `UpdateTask` 879 |
| `pkg/model/store.go` | 7,978 | 78 | 25 | 185 `Store` methods |
| `pkg/orchestrator/run.go` | 5,447 | 62 | 25 | `RunDispatch` 969 lines |

Other functions over 600 lines:

- `daemon/run.go` `Run` (1,051 lines) and `runDaemon` (770);
- `cli/daemon.go` `parseDaemonArgs` (1,154);
- `ui/settings.go` `plannedConfig` (855);
- `orchestrator/sync.go` `syncEntry` (758);
- `orchestrator/cycle.go` `runOneWithFramework` (672);
- `daemon/mcpserver.go` `ServeMCPServer` (623).

**Evidence.** According to this deployment's notes, its merge queue runs
with `merge_recheck` off, so it does not re-test a PR against a base that
has moved. Parallel PRs into the same large files therefore
merged cleanly as text and broke the build:

- `578a81eab`, `77d35794d` and `a5da92bc2` on 2026-11-10 in `pkg/ui`;
- three separate tasks writing the same `grain login` alias fix the same
  day (`85dc9cc8a`, `e609527e7`, `3e1037c38`, all 2026-11-04);
- a three-fix flip-flop on one signature (`7bde587b8`, `a8881d94a`,
  `3d0b17429`, `ParseListCursor`, 2026-11-10).

`client.go` also has the most fixes of any file (81).

**Simplification.**

- Split `ui.Client` by domain (task lifecycle, filing, settings and repos,
  approvals, PRs) into separate types sharing a small core. Areas 1, 7
  and 8 each take a slice of it naturally.
- Split `model.Store` per table group.
- Replace `parseDaemonArgs` with a declarative flag table.

**Payoff.** Fewer textual-overlap merges, smaller review diffs, and less
time spent on build-break repair. This scores lower on error risk because
these breaks are loud and fixed within hours.

**Risk and effort: L.** The work is mechanical, but it causes large
churn and merge pain while it is in flight. Do it one domain at a time,
and preferably after areas 1, 7 and 8 have carved out their pieces.

**Tests first.** None specific. The existing suites are the net. The
deployment-side fix for the crossings (turning `merge_recheck` on) is
listed under "considered".

---

## Considered but not ranked

- **Schema migration numbering.** `SchemaVersion` moved 81 times in the
  window (now 138). Renumbering on conflict is a known chore (the
  `staterepo/dump.go` threshold), and dual SQLite/Postgres paths needed
  fixes (`c0d13bc7b`, `adee93081`, `243340575`). Most of the churn is
  capability renames, which area 2 removes. It is worth revisiting
  (name-keyed migrations) once that lands.
- **`merge_recheck` off in this deployment's merge queue.** This is the
  root cause of area 10's broken builds. It is a `grain/config` setting,
  not core code, so it needs a PR against `grain/config`.
- **`githubsim.Request`** (1,193 lines, 13 fixes). It is a test simulator.
  Its size costs review time, but it ships no bugs to users.
- **`demo.seedDemo`** (1,050 lines, 20 fixes on `demo.go`). It writes
  `Observation` fields directly, so it is covered by area 8.
- **Upgrade and release train** (`pkg/upgrade`: 18 fixes in 39 commits,
  mostly edition, host and registry selection: `d05e0b8c1`, `2610e9f16`,
  `7e39ffbf9`, `771debe65`, `39f28fdba`). The fixes are diverse rather
  than one repeated rule. Revisit after the release-asset publishing work
  settles.
- **hostd/agentd** (24 fixes in 48 commits). These are mostly
  registry-reconnect and renewal lifecycle fixes (`cc83be491`,
  `91da4bcab`, `b47cada05`). A reconnect state machine might help, but
  the evidence is thinner than for the ten above. The host-placed
  `pull_request_status` gap is folded into area 5.
- **`pkg/hub`** (49.8k lines of churn under its old name `pkg/registry`,
  then renamed in `77fa2543a`). It is young code. Its repo-string compares
  are counted in area 3.

## Appendix A: churn by top-level area (2026-09-30..2026-11-11, non-merge)

Lines added plus deleted, and the number of file-touches.

| Area | Lines changed | File-touches |
|------|---------------|--------------|
| docs | 484,256 | 3,396 |
| third_party | 415,803 | 1,397 |
| ui (React) | 346,325 | 4,041 |
| pkg/orchestrator | 238,558 | 2,473 |
| pkg/ui | 236,368 | 3,444 |
| pkg/model | 180,174 | 2,355 |
| pkg/cli | 104,917 | 1,390 |
| tests | 96,085 | 967 |
| pkg/daemon | 91,158 | 1,460 |
| pkg/capability | 71,263 | 788 |
| pkg/mcp | 52,727 | 583 |
| pkg/agent | 52,212 | 629 |
| pkg/registry (now pkg/hub) | 49,785 | 289 |
| pkg/staterepo | 43,761 | 551 |
| pkg/gitproxy | 42,482 | 490 |
| pkg/upgrade | 34,183 | 229 |
| pkg/ci | 33,025 | 279 |
| pkg/github | 23,166 | 167 |

## Appendix B: fix commits by package (non-test Go files)

The columns are commits that are fixes, all commits, and the fix ratio.

| Package | Fixes | Commits | Ratio |
|---------|-------|---------|-------|
| pkg/ui | 326 | 563 | 58% |
| pkg/orchestrator | 189 | 325 | 58% |
| pkg/model | 172 | 344 | 50% |
| pkg/daemon | 142 | 318 | 45% |
| pkg/cli | 92 | 237 | 39% |
| pkg/capability | 46 | 109 | 42% |
| pkg/gitproxy | 44 | 71 | 62% |
| pkg/mcp | 38 | 88 | 43% |
| pkg/sandboxcli | 30 | 51 | 59% |
| pkg/github | 28 | 51 | 55% |
| pkg/registry | 28 | 53 | 53% |
| pkg/hostd | 24 | 48 | 50% |
| pkg/trustboundary | 24 | 38 | 63% |
| pkg/staterepo | 22 | 53 | 42% |
| pkg/ci | 19 | 37 | 51% |
| pkg/namespace | 19 | 34 | 56% |
| pkg/upgrade | 18 | 39 | 46% |

## Appendix C: files with the most fix commits

| Fixes | Commits | Lines changed | File |
|-------|---------|---------------|------|
| 81 | 141 | 10,255 | pkg/ui/client.go |
| 52 | 102 | 5,428 | pkg/ui/agent_api_gate.go |
| 30 | 81 | 4,242 | pkg/ui/tasks.go |
| 30 | 57 | 4,995 | pkg/orchestrator/finish.go |
| 25 | 79 | 9,061 | pkg/model/store.go |
| 25 | 62 | 6,466 | pkg/orchestrator/run.go |
| 24 | 38 | 1,625 | pkg/trustboundary/trustboundary.go |
| 23 | 37 | 1,737 | pkg/ui/access_roles.go |
| 22 | 63 | 4,793 | pkg/daemon/mcpserver.go |
| 21 | 68 | 2,451 | pkg/daemon/run.go |
| 21 | 58 | 1,234 | pkg/cli/main.go |
| 21 | 54 | 3,670 | pkg/orchestrator/cycle.go |
| 20 | 83 | 2,229 | pkg/ui/agent_api_tools.go |
| 17 | 34 | 316 | pkg/sandboxcli/help.go |
| 16 | 34 | 3,824 | pkg/daemon/ci.go |
| 14 | 32 | 1,331 | pkg/orchestrator/capability_pass.go |
| 14 | 26 | 1,756 | pkg/gitproxy/cli_actions.go |
| 14 | 26 | 1,679 | pkg/model/capability_catalogue.go |

## Appendix D: fix chains

- **Proposal grant labels (area 1/2), all on 2026-11-09:** `e8fa6a2d7`
  preserve bare labels → `d76d27918` expand retired aliases →
  `c4f3d5954` write canonical `api:` refs.
- **Proposal catalogue (area 1):** `bd7de6d38` judge against catalogue →
  `669641b73` file for a person when refused.
- **Source-repo targeting (area 1):** `69d0da3f2` allow infra-review
  proposals into source repos → `fa45477f7` tighten against state and
  unknown repos.
- **Run-now and approve parity (area 7):** `0fe934e8c` (2026-11-08) →
  `6875630ea` → `65abff02d` (2026-11-09).
- **Agent gate body parsing (area 7):** `b858ed7f7` → `9e91483cb` →
  `1116754ee` (all 2026-11-01).
- **Approval rails, three build breaks (areas 7/10):** `578a81eab`,
  `77d35794d`, `a5da92bc2` (2026-11-10).
- **`resume_later` wake (areas 4/8):** `fbf3a1ab2` (10-29) → `0d57ae344`
  (11-03, the never-woke wait) → `96094facb` (11-03) → `b8d00b09d` →
  `4e47815f8` (11-09) → `ba4e06aab` (11-09).
- **Duplicate reviews (area 6):** `183623b9f` one review per head →
  `ac5b25f95` close gating-review and incremental re-review bypasses
  (same PR, #1129).
- **Trust-boundary list (area 6):** v17 `91203a8ed` (11-05) → v19
  `1b273897e` → v20 `928c3e36d` → v21 `7841dafe9` → v23 `a6b47b92d` →
  v24 `6f2279289` → v25 `a601a9f05` → v26 `6edb28484` → `c9d98dd66` →
  v28 `63a29270f` (11-10).
- **Repo host dropped (area 3):** `c02bd0db1` → `f37385297` →
  `6506674f6` (10-30) → `5d2741ce7` (11-02) → `c8dce0a06` (11-08) →
  `e58892031` (11-11, #4257).
- **`grain login` alias, three parallel repairs (area 10):** `85dc9cc8a`,
  `e609527e7`, `3e1037c38` (2026-11-04).
- **`ParseListCursor` flip-flop (area 10):** `7bde587b8` → `a8881d94a` →
  `3d0b17429` (2026-11-10).

## Appendix E: method

- Churn: `git log origin/main --since=2026-09-30 --no-merges --numstat`,
  bucketed by `pkg/<name>` or by top-level directory. A file counts once
  per commit.
- Fix attribution: the commit subject starts with `fix` or `revert`.
  Counts are per package per commit, over non-test `.go` files.
- Review merges: first-parent merges of branches matching
  `task-N-(review|security)` (31 of them), with each branch's files taken
  from `git diff merge^1...merge^2`.
- Structure: `wc -l` for file size, function length from `^func` to the
  next `^}`, and `grep` counts for the patterns quoted in each area. The
  counts are approximate and meant to compare areas, not to be exact.

## If approved, file these

1. Parity test: a live `CreateTask` and a relayed `propose_task` produce
   the same task, hold and refusal.
2. Route end-of-run proposals through one `FileTask` admission path and
   delete the relay's copies.
3. Property test: every grant normaliser agrees with the alias table.
4. One canonical grant parser at ingress, an alias table for retirements,
   and the per-surface normalisers deleted.
5. Hygiene lint against string compares of repository names, plus a
   host-aware `RepoNames`.
6. A `ci.Verdicts` interface with grain-CI and GitHub Actions
   implementations and a shared conformance suite.
7. Move wake holds, `grain ci wait` and test-history onto `ci.Verdicts`.
8. A reflection test on `ui.Config` nil-able hooks, and a
   `uitest.NewConfig` with fakes filled in by default.
9. Required `ui.Config` interfaces with explicit `Unavailable`
   implementations.
10. Make `pull_request_status` and `wait_for_checks` work, or refuse
    clearly, on runs placed on remote hosts.
11. A hygiene test that every file under trust-boundary directories is
    classified or explicitly exempt.
12. Make trust-boundary one path-trigger kind behind one review filer
    keyed by (PR, head, kind).
13. A matrix test: same decision for the same action from every entry
    point and actor kind.
14. A typed `authz.Decide(actor, Action)`, adopted route by route with
    the old gate in shadow mode.
15. A task state-machine property test (invariant: no undelivered
    person comment on an undispatchable task).
16. Named model transitions (`Reply`, `Reopen`, `Retry`, `RunNow`,
    `Wake`, ...) and one `Hold` value replacing deferral plus suspension
    `resume_at`.
17. One agent-tool registry that generates every surface's schemas and
    role lists, plus a schema linter over every served tool list.
18. Split `ui.Client` and `model.Store` by domain.
19. (`grain/config`) Turn on `merge_recheck` for `ai-on-gke/grain`.
