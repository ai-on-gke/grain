# Reading the nightly suite: the first week, 2026-09-07

The reading `docs/test-plans/2026-09-06.md` section 14 asks for -- the
first week of `.github/workflows/nightly.yml` (`#848`) read against
`docs/nightly-test-review.md`, which is the standing prompt for it and
which asks for a judgement nobody had made.

**There is no first week.** One run of the nightly has ever happened.
That is the finding, and it is the one the task filing this reading
anticipated: the task was approved a week early, and the reading it asks
for is re-filed with a date on it rather than manufactured out of a
single night.

What follows is therefore not the verdict the prompt asks for. It is the
count that says why not, the one run recorded as raw material for the
reading that does happen, the two things about that run that *are*
decidable without a series to compare against, and two amendments to the
prompt that the first night already earned.

## Why there is only one run

| | |
| --- | --- |
| `#848` merged (`d66f04b0`) | 2026-09-06 12:16 UTC |
| First possible firing of `cron: "11 2 * * *"` | 2026-09-07 02:11 UTC |
| Runs of `nightly.yml` to date | **1** |
| This reading | 2026-09-07 |

The workflow is a day and a half old and has had exactly one scheduled
firing. Nothing stopped, nothing is misconfigured, and the nightly is
not silently dead -- the schedule fired, on its first opportunity, and
produced a run. There is simply no history yet, and a "week" of it
cannot be read on 2026-09-07 by anyone.

Everything the prompt's most valuable steps ask for needs a series and
has nothing to work with:

* step 2, "compare against the last several nights", has no previous
  night to compare against;
* the **regression** class is undecidable -- it is defined as "it passed
  on previous nights, it fails now", and there are no previous nights;
* the **flake** class is undecidable for the same reason, since one
  observation cannot distinguish non-determinism from a deterministic
  failure;
* "is a check producing noise rather than findings" is a question about
  a rate, and a rate needs a denominator bigger than one.

That is not a reason to write nothing, but it is a reason not to write
the verdict, and the difference matters here more than usual: a
confident-sounding classification off one sample is exactly the kind of
thing that gets quoted later as though a week had been read.

## The run that exists

Run 1, `34094712239`, event `schedule`, head `30f133d7` on `main`,
started 2026-09-07 07:17:00 UTC, finished 07:30:17 UTC (13m17s wall).
Conclusion: **failure**, 3 of 5 jobs green.

| Job | Conclusion | Duration |
| --- | --- | --- |
| `go, shuffled and repeated` | success | 13m14s |
| `state repository soak` | success | 8m43s |
| `sustained load` | success | 4m10s |
| `ui e2e, repeated` | **failure** | 4m49s |
| `govulncheck` | **failure** | 39s |

Five jobs, because that is what the workflow had that morning. It has
six now: `4d376605` ("Build the sandbox guest uncached nightly, and diff
it against the cached one", 2026-09-07 17:51 UTC) added `sandbox guest,
cached vs. built from scratch` ten hours after this run. The next
reading will therefore find a job in every run it looks at that has no
counterpart here, and should read its absence from this table as the job
not existing yet rather than as a job that was skipped.

Two notes on the green three, since the prompt asks for durations even
on a green morning and since these are the baseline the next reading
will compare against:

* `go, shuffled and repeated` came in at 13m14s against the ~15m the
  workflow's own comment predicts, and it was green: one pass of
  `-shuffle=on -count=2 -race` over the whole tree found no order
  dependence and no leaked state. That is a real (if single) result --
  the flags were added on the theory that they would find something, and
  on their first outing they did not.
* `sustained load` was green in 4m10s. The workflow comment is explicit
  that a hosted runner is not a host sized for this harness and that a
  red run there should be read for deadlock and lost tasks rather than
  for a missed throughput number. One green run does not retire that
  warning; it is the first sample of the distribution the comment is
  worried about, and the next reading should look at whether it stays
  green rather than treat 4m10s as a baseline.

Job logs are not readable without admin rights on the repository, so the
detail below comes from the runs' check annotations, from the commit
history, and -- for `govulncheck` -- from reproducing the job locally.
Anyone re-reading this run should know that the annotations are all that
is left once the logs expire.

## `ui e2e, repeated`: harness, and already fixed on `main`

Not a regression, not a flake, and not a finding about the product. It
is the repeated-suite harness reporting its own fixture reuse, and the
fix landed five hours after the run.

Four failures, being two tests failing twice each out of three repeats:

    [chromium] › e2e/inbox.spec.js:21:1 › loads /inbox cold, grouped by what each task is waiting for
    [chromium] › e2e/inbox.spec.js:68:1 › answers a parked question from its inbox row
    (203 passed, 3 skipped, 4 failed)

At the time of the run, `make test-e2e-long` was `npm run test:e2e --
--repeat-each=3` against the single `grain demo` store Playwright's
`webServer` brings up. `inbox.spec.js` answers that store's one parked
question, which requeues the task for good, so both tests that need a
task waiting on an answer fail on every repeat after the first. Two
tests, one passing repeat and two failing repeats each: exactly four.

That diagnosis is not mine. Commit `1dc74067` ("Repeat the e2e suite
three times over three stores, not three times over one",
2026-09-07 12:36 UTC) reaches it independently, confirms it serially at
`--workers=1`, finds a second and separate race between repeat indices
in `phone.spec.js`, and replaces `--repeat-each=3` with three
invocations each bringing up its own store. It did the right thing by
the prompt's rule, too: three passes are still three passes, so the
suite was made correct rather than less rigorous.

**No task proposed.** The fault is fixed on `main`, the job that failed
no longer exists in the form that failed, and the confirmation is one
night's patience: if `ui e2e, repeated` is green on the next run, this
closes itself.

## `govulncheck`: advisories, and one toolchain bump clears most of them

The class the prompt calls an advisory: `govulncheck` asks today's
vulnerability database about a tree that has not necessarily changed, so
a red run here says the database moved.

Reproduced locally on this checkout (`go run
golang.org/x/vuln/cmd/govulncheck@latest ./...`, same command the
Makefile's `vulncheck` target runs, same Go version the job resolves out
of `go.mod`): **16 vulnerabilities on call paths this module actually
reaches**, from the standard library and two modules.

| Where | Found in | Fixed in | Called advisories |
| --- | --- | --- | --- |
| Go standard library | `go1.26.2` | `go1.26.6` | 14 |
| `google.golang.org/grpc` | `v1.79.3` | `v1.82.1` | 1 (GO-2026-6061) |
| `golang.org/x/text` | `v0.38.0` | `v0.39.0` | 1 (GO-2026-5970) |

Fourteen of the sixteen are the standard library at `go1.26.2`, which is
what `go.mod`'s `go 1.26.2` line pins and what `setup-go`'s
`go-version-file: go.mod` therefore installs. They are fixed across
`go1.26.3` through `go1.26.6`, so a single bump of that line to `1.26.6`
clears all fourteen. The remaining two are indirect module requirements
and are a `go get` each.

Is the call path one grain actually reaches? For some of it, plainly
yes, and it is worth separating that from the rest:

* **`html/template`, three advisories, two of them XSS**
  (GO-2026-4980 and GO-2026-4982 escaper/URL-escaping bypasses,
  GO-2026-6091 Javascript regexp context tracking), all reached through
  `pkg/ui/server.go:48` → `template.Template.Execute`. grain renders
  HTML templates to a browser on every page; this is the live path, not
  an over-approximation.
* **`net/http/httputil`** GO-2026-4976, reached through
  `pkg/claudeproxy/proxy.go:140` → `httputil.ReverseProxy.ServeHTTP`.
  That is the Claude proxy's entire job, so the path is real.
* **`net/url`** GO-2026-6218 (quadratic `resolvePath`) and
  **`net/http`/`crypto/tls`/`crypto/x509`**, reached through
  `pkg/github`'s transport and the MCP server. Real paths; the severity
  question is one for whoever takes the bump, not for this reading.
* Some traces are `govulncheck`'s call-graph over-approximating and
  should not be read literally -- `model.ValidTimeZone calls
  time.LoadLocation, which eventually calls tls.Conn.Handshake` is the
  clearest example. The bump makes them moot either way, which is why
  this is one small task rather than sixteen judgement calls.

**Class: advisory, and probably a standing one.** It is not a regression
in the day's commits: nothing in `30f133d7`'s history introduced these,
and `go.mod` has not been touched in the commits around the run. Whether
this job settles into a useful signal or into noise is precisely the
question one run cannot answer -- see the next section.

**Task proposed** rather than fixed here, on the prompt's instruction not
to fix an advisory in the same breath as reporting the night.

## Is any check producing noise rather than findings?

The prompt asks, and the task filing this reading asks specifically that
a noisy check be reported and a task filed rather than the suite quietly
turned down. On one run the honest answer is that only one candidate is
even visible, and it cannot be settled yet:

`govulncheck` is unpinned by design (the Makefile comment: "the point is
to ask today's database today"), it holds the tree's Go version rather
than the runner's latest, and it fails the job on any called
vulnerability. Those three together mean it goes red the day an advisory
lands against a stdlib package grain calls and *stays* red every night
until somebody bumps the toolchain -- which is a check that spends most
of its nights repeating a finding already made. That is the shape noise
takes here, and it is worth watching for. But it is a prediction, not an
observation: the job has run once, and once is a finding rather than a
repetition. If the proposed bump lands and the job is red again within
days, the next reading has grounds to say something about the check
itself; if it goes green and stays green, it does not.

Nothing in the other four jobs looks like noise on this evidence. The
one red among them was a genuine harness fault that a commit fixed the
same day, which is the suite working.

## What the next reading should know

Two things this one night turned up that the prompt does not currently
ask about. Both are amendments to `docs/nightly-test-review.md` in this
change rather than observations left here to be forgotten.

**The cron fires late, and by hours.** Scheduled for 02:11 UTC, this run
was created at 07:17 UTC -- 5h06m late. GitHub delays scheduled
workflows on shared runners and does not promise a firing time, so this
is expected behaviour rather than a fault. But the prompt tells whoever
sets up the review schedule to fire it "some hours after the workflow's
cron so the run has finished", and it tells the agent to take "the most
recent completed run" without ever asking what night that run is from.
Those two together have a bad failure mode: a review that fires before a
delayed nightly has run will pick up *yesterday's* run and report it as
last night's, indefinitely and without ever looking wrong. The prompt
already says a nightly that silently stopped running is the most
important thing an agent can report -- it now also says how to notice,
which is to check the run's own timestamp against the night being read.

**The workflow can change between the runs being compared.** The e2e job
that failed here does not exist any more: `1dc74067` rewrote what
`test-e2e-long` runs, five hours after the run. Nor is the job list the
same one: `4d376605` added a sixth job ten hours after it. Both edits
landed between this run and this write-up, which is the whole argument
in miniature -- the single night on record already cannot be compared
against the next one job-for-job. Step 2's comparison
against previous nights silently assumes the check was the same check on
each of them, and on a repository merging at this rate -- 171 commits
landed on `main` between this run's head and the time of writing, about
ten hours -- that assumption will not hold for long. Comparing durations
or conclusions across an edit to `nightly.yml` or to the Makefile
targets it drives is comparing two different checks, so the prompt now
asks for that to be checked before the comparison is made, and it is a
cheap `git log` on two paths.

## Tasks

Proposed:

* **Clear the 16 called vulnerabilities `govulncheck` reports** -- bump
  `go.mod`'s `go` line to `1.26.6`, `golang.org/x/text` to `v0.39.0` and
  `google.golang.org/grpc` to `v1.82.1`. Reproduction and full advisory
  list in the task body.
* **Re-file this reading with a date on it** -- the same job, run once
  the nightly has a week of runs to read, not before 2026-09-14.
* **Confirm the review schedule exists** -- `docs/nightly-test-review.md`
  asks for a daily schedule that fires the prompt, and a schedule is a
  store row in this deployment's own state
  (`bwsalmon/grain-home`), not a file in this repository, so it cannot
  be verified or created from here. Nothing accumulated to read in this
  case regardless -- the nightly is a day old -- so the absence of prior
  readings is not evidence either way, and it is worth an explicit
  check before the re-filed reading assumes there is a habit to join.

Not proposed, and why:

* **The `ui e2e, repeated` failure.** Diagnosed and fixed on `main` by
  `1dc74067` before this reading started. Filing it would be filing a
  closed bug.
* **Anything about the three green jobs.** One green run is not a
  finding. `sustained load` in particular is the one to watch rather
  than to trust, for the reason its own job comment gives.

## Reproducing this reading

    # the runs that exist, and the per-job results
    curl -s 'https://api.github.com/repos/bwsalmon/grain/actions/workflows/nightly.yml/runs'
    curl -s 'https://api.github.com/repos/bwsalmon/grain/actions/runs/34094712239/jobs'
    # failure detail without admin rights on the logs
    curl -s 'https://api.github.com/repos/bwsalmon/grain/check-runs/101655600017/annotations'
    curl -s 'https://api.github.com/repos/bwsalmon/grain/check-runs/101655600204/annotations'
    # the advisories, against this checkout
    make vulncheck
