# Hand run: how long does codex retry a refused API call, and should grain bound it?

Task 418, the codex half of the question
`docs/hand-runs/2026-09-08-claude-resume.md` §5 raised and task 410
settled for claude. claude retries a refused call ten times over 2m51s
inside the exec before grain hears about the refusal at all, and grain
decided to leave that alone (`pkg/agent/claude/retry.go`'s header).
Nothing equivalent had been measured for codex, so `pkg/agent/retry.go`
carried a sentence saying so.

The questions, all of them about somebody else's binary: how many
attempts does codex make against a provider that refuses everything, over
what wall clock, with what backoff, and what does it finally emit; is
there a `CLAUDE_CODE_MAX_RETRIES` equivalent, and does it take 0; and
given the answers, should a grain run bound it.

## What was run, and against what

**A real binary, and no OpenAI credential.** `codex-cli 0.153.0`, the
release `ARG CODEX_CLI_VERSION` pins in the Dockerfile, fetched from the
same GitHub release URL the image uses. The provider is a stub of
OpenAI's responses API running on loopback -- the `model_providers`
stanza `docs/hand-runs/2026-09-07-codex-resume.md` established -- which
counts and timestamps every request and answers each one with a status of
the probe's choosing. A real key would prove nothing here: nobody can ask
OpenAI to be overloaded on demand, and the count is a count of requests
arriving, which only a stub can take.

Every exec below used the argv `Framework.Run` builds and the
`config.toml` `writeConfig` writes -- MCP server, `sandbox_mode`,
`approval_policy` and `features.code_mode_host` included:

```
codex exec --json --skip-git-repo-check --model gpt-5.1-codex -
```

with the prompt on stdin. That is deliberate: the claude measurement
found its count was argv-dependent (ten attempts on the real argv, two on
a bare one), so a probe on a simplified command line measures nothing a
dispatch will see. §5 checks whether that holds for codex.

The refusal is OpenAI's own: HTTP 503 with
`{"error":{"message":"The server is overloaded or not ready yet.", …}}`,
which is the sentence `pkg/agent/codex/capacity.go` matches on.

## 1. Thirty requests over 25 seconds, and then a `turn.failed`

```
exec finished in 25.47s, exit status 1
requests: 30
  attempt  1  t= 3.697s            attempt 16  t=14.515s  gap=0.784s
  attempt  2  t= 3.897s  gap=0.201 attempt 17  t=14.734s  gap=0.220s
  attempt  3  t= 4.329s  gap=0.432 attempt 18  t=15.113s  gap=0.379s
  attempt  4  t= 5.175s  gap=0.846 attempt 19  t=15.921s  gap=0.808s
  attempt  5  t= 6.793s  gap=1.617 attempt 20  t=17.452s  gap=1.531s
  attempt  6  t= 7.041s  gap=0.249 attempt 21  t=19.037s  gap=1.585s
  …                                …
  attempt 15  t=13.731s  gap=1.665 attempt 30  t=28.018s  gap=1.637s
```

The stream, in full apart from the repeated lines:

```json
{"type":"thread.started","thread_id":"01a07f5e-fc3f-…"}
{"type":"turn.started"}
{"type":"error","message":"Reconnecting... 1/5 (unexpected status 503 Service Unavailable: The server is overloaded or not ready yet., url: …)"}
… 2/5, 3/5, 4/5, 5/5 …
{"type":"error","message":"unexpected status 503 Service Unavailable: The server is overloaded or not ready yet., url: …"}
{"type":"turn.failed","error":{"message":"unexpected status 503 Service Unavailable: …"}}
```

Two facts in that output are load-bearing for `pkg/agent/codex` and are
now measured rather than assumed: the terminal text carries both `503
Service Unavailable` (a shared marker in `agent/capacity.go`) and "server
is overloaded" (one of codex's own in `capacity.go`), and the process
exits non-zero -- which is what `retryable` requires before it reads any
marker at all. Run through this package's own classifier, the stream
above yields `codex: the provider had no capacity for this run` and
`retryable == true`.

**25 seconds, against claude's 2m51s.** That is the number the decision
below turns on.

## 2. Thirty is two ladders multiplied

`request_max_retries` (default 4) is the inner one: five requests per
stream attempt, backing off 0.2s, 0.4s, 0.8s, 1.6s. `stream_max_retries`
(default 5) is the outer one, the `Reconnecting... n/5` events above: six
attempts, starting 0.25s, 0.46s, 0.78s, 1.59s and 2.91s apart. Five times
six is thirty.

Bounding only the inner one confirms the split:

```
[model_providers.stub] request_max_retries = 0   →  6 requests, 7.97s
                                                    (gaps 0.24, 0.44, 0.75, 1.64, 3.54)
```

## 3. There is an equivalent of `CLAUDE_CODE_MAX_RETRIES`, and it takes 0

Not an environment variable -- `codex --help` has none and the binary's
own string table carries no `CODEX_*RETR*` name -- but two config keys,
both of which take 0 and both of which bound exactly the ladder they
name:

```
[model_providers.stub] request_max_retries = 0, stream_max_retries = 0
  → 1 request, exec finished in 1.04s, straight to turn.failed

codex exec … -c model_providers.stub.request_max_retries=0 \
             -c model_providers.stub.stream_max_retries=0
  → 1 request, exec finished in 1.28s
```

So the knob exists, on the config file and on the command line alike.

## 4. But not for the provider a grain run actually uses

`configTOML` sets no `model_provider`, so a grain run talks to codex's
built-in `openai` provider -- and codex 0.153 will not let a single key
be set on a built-in one, from the config file or from `-c`:

```console
$ codex exec … -c model_providers.openai.base_url=http://127.0.0.1:9/v1 -
Error loading config.toml: model_providers contains reserved built-in provider IDs:
  `openai`. Built-in providers cannot be overridden. Rename your custom provider
  (for example, `openai-custom`).
exit=1                                            # stdout: empty
```

This is the fact that makes codex's version of the question different
from claude's. For claude the escape hatch costs nothing -- grain sets no
`CLAUDE_CODE_MAX_RETRIES`, and an operator who sets one on the controller
has it reach the binary untouched. For codex there is nothing to leave
alone: bounding the retry means `pkg/agent/codex` declaring a whole
`[model_providers.*]` stanza of its own, with a base URL, a wire API and
an auth key, and then owning them -- freezing an endpoint the vendor
moves and breaking every deployment whose codex authenticates as anything
other than an `OPENAI_API_KEY` (ChatGPT sign-in, Azure, Bedrock; the
binary supports all three). An operator cannot supply one either: every
run gets a private `CODEX_HOME` that `writeConfig` fills and deletes.

## 5. The count is not argv-dependent, unlike claude's

The same stub, a bare `codex exec --json --skip-git-repo-check -` -- no
`--model`, and a config without grain's MCP server or sandbox policy:

```
30 requests, exec finished in 25.15s
```

Identical, which stands to reason: codex's ladder comes from provider
configuration rather than from how the CLI was invoked. Worth having
measured rather than assumed, since claude's did depend on the argv.

## 6. codex ignores `Retry-After`

A 503 answered with `Retry-After: 7` (and `x-should-retry: true`)
produced the same thirty requests on the same gaps, 25.87s. So codex's
backoff, unlike claude's, is a fixed exponential rather than the delay
the provider asked for. At 25 seconds that costs nothing; it would matter
if the ladder were three minutes long.

## 7. What this found that *is* worth acting on: 429 is not retried, or classified

Not the question asked, and the most operationally interesting thing
here. Every 429 -- with a `Retry-After`, without one, with a body worded
exactly the way OpenAI words a rate limit
(`"type":"tokens","code":"rate_limit_exceeded"`, "Please try again in
20s") -- produced:

```
1 request, exec finished in 1.23s
{"type":"error","message":"exceeded retry limit, last status: 429 Too Many Requests"}
{"type":"turn.failed","error":{"message":"exceeded retry limit, last status: 429 Too Many Requests"}}
```

codex retries a 429 not at all, and collapses it to that one sentence
with the provider's body dropped. Run through this package's classifiers
that text matches nothing: `usagelimit.go` deliberately does not match a
bare "429" and never sees `rate_limit_exceeded` because it is gone, and
`capacity.go`'s markers are 503/502/504 and the overload sentences. So
`retryable == false` and `usageLimitFailure == nil`: a rate limit that
arrives as a 429 reaches the orchestrator as an ordinary failed run,
counts against `task_streak`, and five of them in a row retire a task
that was never wrong. That is a classification gap rather than a retry
policy, so it is filed as its own task rather than fixed here.

*(Taken as task 432, which added the sentence above to
`pkg/agent/codex/capacity.go`'s markers — as **capacity**, not as a usage
limit. codex's wording does not say which kind of 429 it was, and the two
ways of being wrong do not cost the same: read as capacity, a plan's
ceiling costs a few retries and a dispatch backoff and no failure streak;
read as a usage limit, a per-minute limit stops dispatch deployment-wide
for at least a minute and cancels every run in flight. A 429 from the
responses API is also much more often an RPM/TPM limit than an exhausted
plan, which is `insufficient_quota`. Where codex does pass a body
through, `usagelimit.go` still matches first and still pauses; that
ordering is unchanged. The same change makes grain's own retry cover it —
30 seconds and a resume in the same sandbox, which is very nearly the
right answer to a per-minute limit and was previously being given by
nobody, since codex retries this status zero times — and adds
`TestLiveCodexReportsA429GrainCanClassify`, §7 as a nightly probe: the
stub answers 429 instead of 503, and the assertion is that codex's
sentence still names the status, since that is the whole of what grain
has left to classify on.)*

## The decision

**grain does not bound codex's internal retry**, and
`pkg/agent/codex/retry.go`'s header carries the reasoning:

- 25 seconds is the cheapest backoff in this stack. It holds the sandbox
  and spends nothing else -- no dispatch, no kontur VM boot, no clone,
  and no model turn, since a refused request produces no assistant turn
  -- and it is shorter than `agent.BaseTransientBackoff`'s own first
  wait. Every outage it absorbs is one that never reaches grain's budget
  of two.
- Bounding it is not free the way it is for claude (§4): it would cost
  this package a model provider of its own, permanently, to save those 25
  seconds.
- And the complaint that motivated asking -- that a refused run sits
  silent for minutes -- is claude's rather than codex's. A codex run
  refused twice spends about 2m45s inside `Framework.Run`, of which only
  75 seconds is codex's own; the other 90 are `agent.RetryPolicy`'s 30
  and 60, which do write notes to the transcript.

Which also settles a related sentence: `agent/retry.go` said the
frameworks' internal retries were unmeasured apart from claude's, and
now says what codex's is. **agy's remains unmeasured**, and is the
obvious next one -- the same probe shape should transplant, since
`agent/antigravity` already stubs a provider in its own live tests.

*(Since taken, on the day after: task 419, and the probe shape did
transplant, though the stub had to be built from scratch — agy's live
tests point at the real Google. agy turns out not to retry an HTTP
refusal at all, and the measurement found something larger while it was
there. `docs/hand-runs/2026-09-08-agy-retry.md`.)*

## What was written down as a result

- `pkg/agent/codex/live_retry_stub_test.go`:
  `TestLiveCodexRetriesARefusedTurnItself`, §1 as a probe that needs the
  binary and no credential. It asserts the shape the decision depends on
  rather than the numbers, which are a vendor's: that codex retries at
  all (more than one request), that it gives up inside 100 seconds --
  four times the measured 25, so a loaded runner does not fail it but a
  ladder that has grown into claude's minutes does -- and that what it
  emits still reads as a capacity refusal to `retryable`.
  `.github/workflows/live-frameworks.yml` runs it nightly, ahead of the
  credential gate, so a `CODEX_CLI_VERSION` bump that changes any of this
  says so the first night.
- `pkg/agent/codex/retry.go`'s header: the decision and §§2-4 behind it.
- `pkg/agent/codex/codex_test.go`:
  `TestRunLeavesCodexsOwnAPIRetryAlone`, which fails if this package ever
  starts writing a provider stanza or a retry key into `config.toml` --
  the only way to bound the retry, and therefore the change that has left
  the decision above behind.
- `pkg/agent/retry.go` and `pkg/agent/claude/retry.go`: the "codex's own
  internal retries have not been measured" sentences, corrected.

Repeat it on any machine with a codex on `$PATH`:

```
GRAIN_LIVE_AGENT_TEST=1 go test ./pkg/agent/codex/ \
  -run TestLiveCodexRetriesARefusedTurnItself -count=1 -v
```
