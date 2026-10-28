# Hand run: does agy resume a conversation across dispatches, and on what argv?

Task 178, the agy half of the idea `pkg/agent/antigravity/resume.go`
carries. `docs/hand-runs/2026-09-07-codex-resume.md` settled the
equivalent questions for codex; agy's had never been asked of a binary at
all, because the only probe this repository had
(`tests/e2e/live_resume_test.go`) needs a `GEMINI_API_KEY` that has never
reached `.grain/workflows/live-agent.yml`, and every scheduled run of
that workflow has died at its "Require a credential" step.

The three questions, all of them about a binary rather than about this
code, and all three load-bearing for the session store task 135 added:

1. does `--conversation <id>` compose with `--input-format stream-json`
   and the rest of the argv a grain dispatch builds?
2. does agy resolve a conversation out of the HOME it is given when the
   working directory is not the one that wrote it -- which is what a new
   sandbox per dispatch means?
3. does `conversation_summaries.db` have to travel with the conversation,
   or does it rebuild?

All three came back in this code's favour. A fourth thing, which was not
being asked about, came back against it: **a conversation agy cannot find
is not refused.**

## What was run, and against what

**A real binary, and no Gemini credential.** Two of them, in fact:

- `agy 1.1.27`, the release `ARG AGY_CLI_VERSION` pins in the Dockerfile,
  fetched from the same `storage.googleapis.com` URL the image build uses
  and checked against `ARG AGY_CLI_SHA256_AMD64`. The unpacked binary's
  own sha256 is `93eb2118…`, which is the hash `docs/agy-surface.md`
  recorded for 1.1.27.
- `agy 1.2.5`, what `https://antigravity.google/cli/install.sh` served on
  2026-09-17 -- and therefore what a `Dockerfile.k8s-sandbox` built that
  day carries, since that file installs agy unpinned.

Every probe below was run against both, and they did not differ.

The provider is a stub of Gemini's `streamGenerateContent` endpoint on
loopback, pointed at through `GOOGLE_GEMINI_BASE_URL` -- the same seam
`docs/hand-runs/2026-09-08-agy-retry.md` refuses every request through,
used here to *answer* every request with one complete SSE turn, and to
keep the request bodies. That is not a fallback for the missing key: it
is the only way to read **what agy sent on the resumed exec**, which is
where "was the conversation replayed?" is actually written. A session
that came back empty looks exactly like one that came back whole from
agy's own stream.

Every exec used the argv `Framework.Run` builds (`agyArgs`) and the
private HOME `writeAgyHome` writes -- the MCP config naming that run's
own sandbox, the permission rules, the hooks, and the `modelProvider:
"gemini"` that makes agy read `GEMINI_API_KEY` at all -- with the prompt
on stdin as a `stream-json` user event, and with this run's configuration
written back into the HOME before each exec (`refreshAgyHome`), exactly
as `runAttempts` does. Two temporary directories stand in for two
dispatches' sandboxes.

## 1. `--conversation` composes with `--input-format stream-json`

```
agy --input-format stream-json --output-format stream-json \
    --dangerously-skip-permissions --disable-slash-commands \
    --print-timeout 12m59s --model gemini-3.1-pro --effort high \
    --add-dir <sandbox-b> --conversation 5396f55a-… --log-file …
```

Accepted, exit 0, and the first line out of it is agy's own `init`:

```json
{"event":"init","conversation_id":"5396f55a-2173-4614-9822-6878e26dc400","init":{"model":"gemini-3.1-pro","cwd":"…/sandbox-b","tools":[…57 names…],"permission_mode":"always-proceed"}}
```

The id is the one the earlier exec ran in, which is agy saying it found
and joined that conversation rather than opening one. So the thing
`resume.go` had been unable to claim for a fortnight -- that the flag
composes with the one input format grain uses -- holds.

## 2. The conversation is resolved out of the HOME, and the cwd does not enter into it

The first exec ran in `sandbox-a` and was told to remember a marker. That
directory was then **deleted**, which is what a dispatch's sandbox is by
the time the task comes round again, and the second exec ran in
`sandbox-b` out of the same HOME.

It resumed. The model request it sent carries the whole earlier
conversation with the new turn appended -- roles and text, with the 70KB
of system instruction and tool declarations around them elided:

```
user  : <USER_REQUEST> GRAIN-AGY-RESUME-PROBE-FIRST-TURN-ZANZIBAR … </USER_REQUEST>
model : ok
user  : <USER_REQUEST> GRAIN-AGY-RESUME-PROBE-SECOND-TURN-CARRY-ON … </USER_REQUEST>
user  : <SYSTEM_MESSAGE> … all your subagents and background processes …
```

The earlier turn *and the model's own reply to it* are both there, which
is the saving the whole idea is for.

The control, which is not optional: a third exec out of the same HOME, in
the same `sandbox-b`, passing no `--conversation`, gets a new conversation
id and a request body with none of the first turn in it. So the replay
above is `--conversation` doing something rather than a HOME-wide memory
or an agy that continues the most recent conversation by default.

One caveat worth knowing, visible in the resumed exec's system
instruction: the conversation carries its **old workspace** with it, so
the resumed run is told about two workspaces, one of which is the deleted
sandbox.

```
/tmp/…/sandbox-a -> /tmp/…/sandbox-a      <- gone
/tmp/…/sandbox-b -> /tmp/…/sandbox-b      <- this dispatch's --add-dir
```

`agent.SuspendedPreamble` already tells the model in as many words that
the sandbox is not the same one, which is the mitigation that was already
there; nothing here needed changing, but a dispatch that finds an agent
reaching for a path that does not exist has this to look at.

## 3. Nothing has to travel but `conversations/<id>.db`

Beside the conversation, agy's state directory
(`~/.gemini/antigravity-cli`) accumulates `conversation_summaries.db`,
`jetbox_summaries_proto.pb`, `cache/last_conversations.json`,
`annotations/<id>.pbtxt`, `implicit/*.pb`, `presence/<id>.lock` and a
`brain/<id>/` tree of transcripts. `pkg/agent/sessionstore` keeps all of
it (everything the CLI wrote), and what a resume actually needs out of it
was unknown.

Two shapes, the second deliberately harsher than anything production
does:

- the whole HOME with `conversation_summaries.db`,
  `jetbox_summaries_proto.pb` and `cache/last_conversations.json`
  deleted: **resumes and replays**, and agy rebuilds
  `conversation_summaries.db` for itself.
- a HOME this package has just written from scratch, at a path agy has
  never seen, carrying exactly one file of the old one --
  `conversations/<id>.db`, and not the `brain/<id>/` tree either:
  **resumes and replays**.

So the conversation is portable, both across working directories and
across HOME paths, and keeping the whole HOME is more than enough rather
than a gamble.

## 4. A conversation agy cannot find is not refused -- it is replaced

This is the one that came back against the guess in `resume.go`, and it
matters because a dispatch may hand agy a conversation grain's own
pruning has since removed.

```console
$ agy … --conversation 00000000-0000-0000-0000-000000000000
warning: conversation "00000000-0000-0000-0000-000000000000" not found   # stderr
{"event":"init","conversation_id":"f22478b8-6907-4f76-9e45-d40e0bf58b95",…}
…
exit=0
```

agy warns on stderr, opens a **new** conversation with a **new** id, runs
the turn and exits 0. (agy's own changelog has this as a deliberate fix
-- "the `--conversation` not-found warning" is listed among the startup
diagnostics that were being swallowed into the log file instead of
reaching the terminal.)

`resume.go` expected a refusal: an exec that fails with no `init` event,
which is what codex and claude do with a session they do not have, and
which is what `agent.SuspendedResumeRefusedNote` reports. That signal
therefore **never fires** on either measured build, and the only evidence
a carried resume did not take is that the conversation on the stream is
not the conversation that was asked for.

Nothing is lost when it happens -- the turn a resumed dispatch sends is
the whole prompt behind `agent.SuspendedPreamble`, so the run works the
task -- but a person reading the transcript was being told nothing at
all, while the task pane went on showing a resume token. `runAttempts`
now compares the two ids and says so (`suspendedResumeLostNote`).

## 5. An aside: agy updated itself in place

Not what this task was measuring, and worth writing down. The pinned
1.1.27 binary was first unpacked into a directory owned by the user the
probe ran as. After the first exec, `/opt/antigravity/antigravity` was a
different file: 217,284,864 bytes where the release artifact is
210,551,040, `sha256 84808e10…` -- byte for byte what
`antigravity.google/cli/install.sh` serves -- and `agy --version` said
`1.2.5`. Re-unpacked into a root-owned, non-writable directory, the same
binary stayed 1.1.27 (`sha256 93eb2118…`) across the whole probe suite.

So the Dockerfile's pin, whose whole argument is that "keeping the
release hashes beside the version makes a changed artifact fail the build
instead of silently changing the agent runtime", holds only while the
binary is not writable by the user agy runs as.
`Dockerfile.k8s-sandbox` runs as `agent` (uid 1000) against a root-owned
`/opt/antigravity`, so it is safe; the main `Dockerfile` declares no
`USER`. Whether a deployment's dispatches actually run as root there, and
whether agy's updater should be turned off outright, was not measured
here and is left as a question for whoever owns that image.

**Answered, in `docs/hand-runs/2026-09-17-agy-self-update.md`** (task
185): the single-node unit runs the daemon as the host's grain uid and
was safe, `deploy/kubernetes/grain.yaml` runs it as root and was not, and
the updater is now turned off outright --
`AGY_CLI_DISABLE_AUTO_UPDATE=true`, in the environment of every agy this
repository starts. The probes above are therefore measurements of the
pinned binary in a way they were not when they were taken: the first exec
of this suite used to leave a different agy behind it.

## What this changed

- `pkg/agent/antigravity/live_resume_stub_test.go` is §1 to §4 written
  down as three tests. They need an agy binary and no credential, and
  they skip (loudly, under `GRAIN_LIVE_AGENT_TEST`) where there is none.
  `.grain/workflows/live-agent.yml` runs them ahead of the credential
  gate, beside the retry probes, for the same reason those are there.
- `pkg/agent/antigravity/resume.go`'s header no longer says the resume is
  unmeasured; it says what was measured, on which builds, and what is
  still owed.
- `runAttempts` reports a carried conversation agy replaced
  (`suspendedResumeLostNote`), which §4 is the reason for.

## Still owed

- **A credentialed end-to-end resume.** Everything above is agy's own
  behaviour with the model's answers stubbed. On 1.1.27 and 1.2.5 agy
  replays the conversation out of its own on-disk database, so there is
  nothing on Google's side to survive -- but if that ever changes, these
  probes go on passing while a deployment stops resuming.
  `tests/e2e/live_resume_test.go` is that probe, and it still waits on
  the secret `.grain/workflows/live-agent.yml` has never been given.
- ~~**The self-update above**, if the main image's dispatches run as
  root.~~ Settled by task 185:
  `docs/hand-runs/2026-09-17-agy-self-update.md`.
