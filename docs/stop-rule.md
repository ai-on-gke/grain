# The stop rule: runs that end their turn mid-promise

In every framework grain drives, an agent reply with no tool call ends the
run. Agents do not always act as if they knew that. Task 831's Director
session, on antigravity, repeatedly ended on "Checking ... back in a
minute" or "I'll keep going" (2026-10-25 and 2026-10-26). The run stopped
there, and the person had to write again to get the work they had been
promised.

The stop rule (`pkg/agent/stoprule.go`, `agent.DecideStop`) decides
whether a stop is allowed:

- **Interactive and console sessions** may end on plain text, which is how
  they answer. The exception is a final message that promises more work.
  `agent.PromisesMoreWork` reads only the last two sentences and looks for
  a short phrase list ("back in a minute", "I'll keep going", "checking
  now", ...), a sentence that starts with "let me" or "let's", a closing
  "-ing ... now/next" sentence, or a message that ends on a colon. It skips
  code fences and conditional offers ("if you want, I'll ..."). The nudge
  is: "You said you would continue. Do it now, or use `chat` to tell the
  person and wait for them."
- **Background tasks** may stop once they have made a closing move:
  `comment_on_issue`, `ask_question`, `nothing_needed`, `request_secret`, a
  `wait_for_task` that parks (`park` or `resume_at`), a pushed branch (a
  `run_command` whose `git push` did not fail), `open_pull_request`, or
  `add_review_comment`. Otherwise the nudge names those options.

A run is sent back **at most once per turn**, so the rule can never loop.
A wrong answer costs one turn. That is why the detector is conservative
but not timid. `pkg/agent/stoprule_test.go` table-tests it against final
messages of both kinds.

## Where it is applied

| Framework | How |
| :-- | :-- |
| claude | A `Stop` hook in a per-run `--settings` file (`pkg/agent/claude/stophook.go`): grain forks itself as `grain claude-stop-hook`. It reads Claude Code's payload, using `last_assistant_message` or the session transcript at `transcript_path`, and answers `{"decision":"block","reason":...}`. It allows every stop that has `stop_hook_active` set. Each block is appended to a record file grain stages beside the settings, and is read back onto the run's Result. Console sessions get the same hook as sessions, with no record. |
| antigravity, codex, custom | After the CLI exits, `orchestrator.continueStoppedRun` applies the same rule to the Result. If it would have blocked, it resumes the same CLI session once (`RunConfig.ResumeSession` plus `ContinuePrompt`, with the nudge alone and no suspended-dispatch preamble), the way the retry loops resume a session after a usage limit. The continuation's tool calls, transcript and final text are merged into the run. A run that leaves no resumable session (custom; a deployment with no session store) is left as it ended. |

Every block is a line in the run's transcript and a count in its tool
census, under the pseudo-tool `grain:stop-blocked`.

## agy's own mechanism

agy 1.1.26 documents a `Stop` hook in `hooks.json`. The contract is
quoted in `docs/agy-surface.md` ("Stop Contract"). Its input carries
`terminationReason`, `fullyIdle` and the common fields, including
`transcriptPath`, but not the final message. Replying
`{"decision":"continue","reason":...}` re-enters the loop with the reason
injected as a system message. Its release notes say Stop hooks only became
reachable once `hooks.json` hooks ran before the built-in termination
checks, and that it caps consecutive continuations.

grain does not install it yet. The final message would have to come from
agy's own transcript format, which grain has never parsed, and no live agy
here has ever exercised the hook (compare `TestLiveAgyLoadsGrainsHookConfig`
for the PreToolUse hook). agy runs therefore use the resume-once fallback.
Moving agy onto its own Stop hook is a follow-up. It needs a live
measurement of the payload and of `agy -p /hooks`.

## Trust boundary

None of the agent's output reaches anything but the decision of whether
one more turn is spent. The continuation runs under the run's own
RunConfig: the same sandbox, tools, grants and deadline. The only change
is ResumeSession, set to the session the framework itself named, and
ContinuePrompt, which is one of grain's own constants. The claude hook
holds no credential and does not contact the daemon. It reads its stdin
and the transcript file Claude Code names, and appends only to the record
file grain staged (`O_APPEND` without `O_CREATE`).
