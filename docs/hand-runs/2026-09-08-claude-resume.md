# Hand run: does claude resume a session, and on what argv?

Task 339, the claude half of the question
`docs/hand-runs/2026-09-07-codex-resume.md` settled for codex. Task 284
had already given `pkg/agent/claude` a resume -- `--resume <session-id>`
on the retry's exec -- and written the probe that would measure it
(`TestLiveClaudeResumesASessionAcrossExecs`), but that probe needs
`CLAUDE_CODE_OAUTH_TOKEN` and no such secret has ever reached
`.github/workflows/live-frameworks.yml`'s `live-claude` job. So the claim
was carried by its fallback rather than by a measurement. This is the
measurement, taken the way codex's was: against the real binary, with the
model replaced by a stub, and therefore with no credential at all.

The questions, all of them about somebody else's binary rather than about
this code: does `--resume` compose with the exact argv a grain dispatch
builds, does the resumed exec pick up a fresh prompt from stdin, does the
model on the other end actually get the earlier conversation, is a
session whose turn the *provider refused* still resumable -- that being
the only case grain ever resumes after -- and what does claude do when
asked for a session it does not have, which is what the fallback is keyed
on.

## What was run, and against what

**A real binary, and no Anthropic credential.** `claude-code 2.1.197`,
installed from npm (`@anthropic-ai/claude-code`) in a grain agent
sandbox. The Dockerfile installs claude from the vendor's unpinned
installer, so what a freshly built image carries is whatever the vendor
shipped that day; 2.1.197 is what that was on 2026-09-08.

Where a turn had to complete, claude was pointed at a stub of Anthropic's
`/v1/messages` running on loopback (`ANTHROPIC_BASE_URL`, with
`ANTHROPIC_API_KEY` set to a string that is not a credential and that the
stub never reads). The stub records request bodies and answers each one
with the shortest complete SSE turn. It is not a workaround for a missing
key: reading *what claude sent* is the only place the answer to "was the
conversation replayed?" is written, and a real key would have proved
less.

Every exec below used the argv `Framework.Run` builds, MCP config
included:

```
claude -p --tools '' --mcp-config <file> --strict-mcp-config \
  --allowedTools mcp__grain-sandbox__… --output-format stream-json \
  --verbose --model claude-sonnet-5 [--resume <session-id>]
```

with the prompt on stdin, and with `HOME` pointed at a directory of the
run's own so that "the second exec found the first exec's session" is a
fact about these two execs and not about a store somebody else had been
writing to.

**Not measured:** anything about a resumed session that lives on
Anthropic's side. claude 2.1 replays the conversation out of its own
on-disk store (§2), so on this build there is nothing server-side to
survive. `TestLiveClaudeResumesASessionAcrossExecs` is still the probe
that would notice that changing, and it still wants a credential.

## 1. `--resume` composes with the argv, and stdin still carries the turn

Two execs, the second with `--resume` and a new prompt on stdin. Both
exit 0, both stream a `system`/`init` first line, both end on a
`result`:

```
first exec:  session e4b95c4c-472e-47ff-afd1-c081aeeb4581, answer "ok"
second exec: session e4b95c4c-472e-47ff-afd1-c081aeeb4581, answer "ok"
```

The resumed exec re-announces **the same session id** rather than forking
under a new one. `retry.go` does not depend on that either way -- it
resumes whichever id the last attempt's stream named -- but it is worth
having written down, since the opposite behaviour would look identical
from the outside.

## 2. The model is sent the earlier conversation

This is the whole point of resuming rather than restarting, and it is
invisible from claude's own event stream: a session that came back empty
looks exactly like one that came back whole. The stub's record of the
second exec's request body:

```
user:      [{"type":"text","text":"…GRAIN-RESUME-PROBE-FIRST-TURN"}]
assistant: [{"text":"ok","type":"text"}]
user:      [{"type":"text","text":"GRAIN-RESUME-PROBE-SECOND-TURN",
             "cache_control":{"type":"ephemeral"}}]
```

The first exec's prompt, the assistant turn it produced, and then the new
turn from stdin. Replayed from disk, at:

```
$HOME/.claude/projects/<slugified cwd>/<session-id>.jsonl
```

which is keyed on `HOME` and the working directory and on nothing else.
That is what makes the resume work in production, where claude is exec'd
with the controller's own environment and directory on every attempt of a
run (`retry.go`'s header) -- and it is why these probes have to set
`HOME` themselves to mean anything.

## 3. A session the provider refused is still there, prompt and all

The case the retry loop actually exists for. The stub answered the first
exec with Anthropic's own refusal:

```
HTTP 529
{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}
```

claude gave up on the turn, and the session file was on disk anyway
(2094 bytes, the path in §2). Resuming it worked, and the replayed
request carried the refused turn's prompt. A build that only wrote a
session on a completed turn, or that kept the failure and not the prompt,
would have passed §1 and §2 and lost every retry's context in production.

## 4. What claude does with a session it does not have

The premise `retry.go`'s fallback is keyed on. Asked to resume a
well-formed session id it has never held:

```console
$ claude … --resume 00000000-0000-4000-8000-7af812dc30f1
exit status 1
stderr: No conversation found with session ID: 00000000-0000-4000-8000-7af812dc30f1
stdout: {"type":"result","subtype":"error_during_execution",…,
         "errors":["No conversation found with session ID: …"]}
```

No `system`/`init` event, and -- the stub saw no request at all -- no API
call. So "the exec failed and claude opened no session" really is the
signature of a resume that did not take, which is what `retry.go` reads
to drop the flag and restart the task in place. The two failure modes
that would have broken that (an init event first, or a *fresh* session
started quietly) are both absent on this build.

## 5. claude retries a 529 itself, for about three minutes

Not a question anyone asked, and the most operationally interesting thing
here. With the argv above, a stub that refuses every request produces:

```
api_retry attempt 1 … retry_delay_ms 534    ← 529 overloaded
api_retry attempt 2 … retry_delay_ms 1245
…
api_retry attempt 10 … retry_delay_ms 32595
result: is_error true, api_error_status 529, duration_ms 175103
        "API Error: 529 Overloaded. This is a server-side issue…"
```

Ten attempts, exponential backoff, **2m51s of wall clock before grain
hears about the refusal at all** -- and then `agent.RetryPolicy` waits
its own 30 seconds on top before the resumed exec. Nothing is broken
about that: the run keeps its sandbox and its turns either way, and a
provider that recovers during those three minutes never becomes grain's
problem. It is worth knowing when reading a run's timings, and worth
knowing that a claude run refused twice can spend six minutes inside
`Framework.Run` doing nothing visible.

(The count is not fixed at ten: the same stub answering a bare `claude -p
--tools '' --output-format stream-json --verbose` -- no MCP config, no
`--model` -- was given up on after two attempts and 2.4 seconds. What
bounds it deterministically is `CLAUDE_CODE_MAX_RETRIES`, which the tests
set to 0 so that a probe about sessions does not sit through somebody
else's backoff.)

Task 410 took that as a question rather than a fact and answered it:
grain does **not** set `CLAUDE_CODE_MAX_RETRIES` on a run. claude's
backoff is the cheaper of the two -- it holds only the sandbox, spends no
dispatch and no model turn, and waits the delay Anthropic itself asked
for -- and bounding it would push ordinary two-minute overloads onto
`agent.RetryPolicy`'s budget of two retries and 90 seconds of ladder,
paying a fresh sandbox and clone for outages claude currently absorbs
invisibly. Invisibly is the real cost, and it is a reporting problem: the
reasoning and the escape hatch for a deployment that wants otherwise are
in `pkg/agent/claude/retry.go`'s header, with the shorter version in
`pkg/agent/retry.go` beside the retry budget it sits under.

Two facts in that output are load-bearing for `pkg/agent/claude` and are
now measured rather than assumed: the terminal event's text is `API
Error: 529 Overloaded…`, which is exactly what `capacity.go`'s `api
error: 529` marker matches, and the process exits non-zero, which is what
`retryable` requires before it reads any marker at all.

## What was written down as a result

`pkg/agent/claude/live_resume_stub_test.go`, four probes that need the
binary and nothing else -- §1 and §2 as
`TestLiveClaudeResumeReplaysTheEarlierConversation`, §3 as
`TestLiveClaudeResumesASessionTheProviderRefused`, §4 as
`TestLiveClaudeRefusesASessionItDoesNotHave`, and one more that drives
`Framework.Run` itself against the stub
(`TestLiveClaudeRetryResumesTheRunAfterARefusal`): refuse the first
attempt's turn, and check that grain's own loop reads the refusal, execs
again with `--resume` and the session id off the first stream, and sends
`agent.ResumePrompt` into a conversation that still has the task in it.
That one is the end-to-end measurement; deleting the `--resume` from
`attemptArgs` fails it, which is what makes it one.

`.github/workflows/live-frameworks.yml` runs all four before its
credential gate, so the claude job measures this every night whether or
not anybody has minted a token.

Repeat any of it, on any machine with a claude on `$PATH`:

```
GRAIN_LIVE_AGENT_TEST=1 go test ./pkg/agent/claude/ \
  -run 'TestLiveClaudeResumeReplays|TestLiveClaudeResumesASessionTheProviderRefused|TestLiveClaudeRetryResumes|TestLiveClaudeRefuses' \
  -count=1 -v
```
