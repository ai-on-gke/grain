# Hand run: does codex resume a thread, and on what argv?

Task 284, the codex half of the idea `pkg/agent/antigravity/resume.go`
carries. Task 220 gave `pkg/agent/codex` an in-run retry that keeps the
sandbox but restarts the task from its prompt, because nothing in this
repository had ever measured codex's resume; this is that measurement.

The questions, all of them about a binary rather than about this code:
does a resume subcommand exist in the spelling this repository would
guess at, does it take a fresh prompt on stdin, does it compose with
`--json --skip-git-repo-check --model` and a per-run `CODEX_HOME`, does
it resolve a thread out of the directory an *earlier attempt* wrote, and
-- the one that decides whether any of it is worth doing -- does the
model actually get the earlier conversation back.

## What was run, and against what

**A real binary, and no OpenAI credential.** `codex-cli 0.153.0`, the
release `ARG CODEX_CLI_VERSION` pins in the Dockerfile, downloaded from
the same GitHub release URL the image uses and run in a grain agent
sandbox. Every probe below is a real exec of that binary.

Where a model turn had to complete, codex was pointed at a stub of
OpenAI's responses API running on loopback -- a `model_providers` stanza
in the run's `config.toml` -- which records request bodies and answers
each one with a one-message turn. That stub is not a fallback for the
missing key: it is the only way to read *what codex sent*, which is
where the answer to "was the conversation replayed?" is written, and a
real key would have proved less.

**Not measured:** anything about the resume that lives on OpenAI's side.
codex 0.153 replays the conversation from its own on-disk rollout, so
for this build there is nothing server-side to survive; a future codex
that stored threads remotely would need a credentialed probe.

## 1. The subcommand exists, and takes the argv this package builds

```console
$ codex exec --help
Usage: codex exec [OPTIONS] [PROMPT]
       codex exec [OPTIONS] <COMMAND> [ARGS]

Commands:
  resume  Resume a previous session by id or pick the most recent with --last
  fork    Fork a previous session by id into a new session
  ...

$ codex exec resume --help
Usage: codex exec resume [OPTIONS] [SESSION_ID] [PROMPT]

Arguments:
  [SESSION_ID]  Conversation/session id (UUID) or thread name...
  [PROMPT]      Prompt to send after resuming the session. If `-` is used, read from stdin

Options:
  --last, --all, -c, -m/--model, --json, --skip-git-repo-check,
  --ephemeral, --ignore-user-config, -o/--output-last-message, ...
```

Every flag `Framework.Run` passes is in that list: `--json`,
`--skip-git-repo-check`, `--model`, and the bare `-`. Flags may precede
the positional session id, so `codex exec resume --json
--skip-git-repo-check <id> -` parses -- which is exactly what
`codexArgs` now builds.

Two flags the ordinary `codex exec` has and `resume` does **not**:
`-C/--cd` and `-s/--sandbox`. Neither is passed by this package (the
working directory is set on the subprocess, and the sandbox policy is in
the config file), so nothing had to change. A future change that reached
for either would have to check `resume` first.

## 2. `thread.started` names the thread, before authentication

The first line of every `codex exec --json` stream, including one with
no credential at all:

```console
$ CODEX_HOME=$H codex exec --json --skip-git-repo-check - <<< 'say hi'
{"type":"thread.started","thread_id":"01a07dbc-0e5e-73f2-8edd-2cef47dd52d2"}
{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Code Mode is unavailable..."}}
{"type":"turn.started"}
{"type":"error","message":"Reconnecting... 2/5 (unexpected status 401 Unauthorized ...)"}
...
{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized: Missing bearer or basic authentication in header ..."}}
exit=1
```

So the id a resume needs is one this package already reads off a stream
it already parses, and it is announced before codex so much as
authenticates -- which is what makes "no `thread.started`" a sound test
for "codex would not perform this invocation" rather than "this run went
wrong". `transcript.go` now records it.

## 3. The thread is resolved out of `CODEX_HOME`, and cwd does not enter into it

Resuming that same thread, from the same `CODEX_HOME`, still with no
credential:

```console
$ CODEX_HOME=$H codex exec resume --json --skip-git-repo-check 01a07dbc-... - <<< 'carry on'
{"type":"thread.started","thread_id":"01a07dbc-0e5e-73f2-8edd-2cef47dd52d2"}
...
```

The same id comes back, which is codex saying it found and joined the
thread. Repeated from a different working directory: identical. The
`--all` flag ("Show all sessions (disables cwd filtering)") governs
picking a session, not resuming a named one.

This is the fact the whole idea depended on. `writeConfig` builds a
fresh `CODEX_HOME` per run and deletes it as `Run` returns, so the only
home a retry can resume from is the one its own earlier attempt wrote --
and that is the home codex looks in.

## 4. A refused resume writes nothing to stdout

Two ways to refuse, and they agree on the part that matters:

```console
$ codex exec --json --skip-git-repo-check --resume <id> -     # a spelling codex does not have
error: unexpected argument '--resume' found
exit=2                                                         # stdout: empty

$ codex exec resume --json --skip-git-repo-check 00000000-0000-0000-0000-000000000000 -
Error: thread/resume: thread/resume failed: no rollout found for thread id 00000000-... (code -32600)
exit=1                                                         # stdout: empty
```

Neither emits `thread.started`. So `retry.go`'s fallback -- an attempt
that asked to resume, died, and opened no thread is the argv being
refused; drop it, restart in place, leave a note in the transcript --
has a signal it can actually read, and it covers both a codex that drops
the subcommand and a thread whose rollout is missing.

## 5. The resumed exec replays the whole conversation

Against the stub, so the request body could be read. First exec sends
one prompt; the resumed exec sends another:

```console
$ ... codex exec --json --skip-git-repo-check - <<< 'REMEMBER-THE-WORD-ZANZIBAR'
$ ... codex exec resume --json --skip-git-repo-check <thread> - <<< 'WHAT-WAS-THE-WORD'
```

The second request's `input` array, roles `user` and `assistant` only:

```
user      : "<environment_context>\n  <cwd>/tmp/probe-work ..."
user      : "REMEMBER-THE-WORD-ZANZIBAR\n"
assistant : "ack"
user      : "WHAT-WAS-THE-WORD\n"
```

The earlier turn and the model's own reply to it are both there, with
the new prompt appended -- which is the saving this is all for, and the
reason a resumed attempt sends `agent.ResumePrompt` (a nudge) rather
than the task's prompt again.

## 6. And it holds for a turn the provider *refused*, which is the case that matters

The retry loop only ever resumes after a capacity refusal, so the
question is not whether a completed turn is replayed but whether a
*failed* one is. Stub set to answer 503; first exec fails
(`turn.failed`, exit 1); stub set back to healthy; resume:

```
user : "<environment_context> ..."
user : "REMEMBER-THE-WORD-ZANZIBAR\n"      <- the refused turn's own prompt
user : "CARRY-ON-NUDGE\n"
```

The prompt of the turn the provider refused is still in the thread. A
resumed retry therefore does not need to resend the task, and anything
the first attempt did before the refusal -- its tool calls included --
comes back with it.

## What this changed

- `pkg/agent/codex/retry.go` resumes the thread on a retry (`codex exec
  resume <thread> -` with `agent.ResumePrompt` on stdin) instead of
  restarting the task, keeping the fallback for a codex that refuses the
  invocation.
- `pkg/agent/codex/codex.go` grew `codexArgs`, which builds both shapes
  from one place so a flag added to a run's first exec cannot be
  forgotten on its retry.
- `pkg/agent/codex/transcript.go` records `thread_id` and whether a
  thread was ever announced.
- `TestLiveCodexResumesAThreadAcrossExecs` is sections 1, 3 and 5 above
  written down as a test: it needs the binary and no credential, and
  `.github/workflows/live-frameworks.yml` runs it nightly ahead of the
  credential gate. It was checked to fail -- on both the thread id and
  the replayed prompt -- when the resume is removed from `codexArgs`.

## Still owed

- **`agent/claude`.** The identical question about `claude --resume` is
  still unmeasured, and that framework is still on the cheap variant.
  The stub-provider trick here does not transplant unchanged (claude has
  no `model_providers` stanza), but the shape of the probe does.
- **A credentialed end-to-end resume.** Everything above is codex's own
  behaviour with the model's answers stubbed. `TestLiveRunEndToEnd`
  covers the credentialed path for an ordinary run, and no test covers a
  credentialed *resumed* run -- which would only differ if a future
  codex stopped replaying from the local rollout.
