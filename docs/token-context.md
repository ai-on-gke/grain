# Agent context budgets

Grain should treat model context as a bounded runtime resource, not as an append-only projection of task history.

## Problem

A fresh dispatch currently renders every task comment into `commentThreadSection`. Earlier-attempt history is deliberately bounded, but the conversation is not. A task that accumulates comments therefore sends an ever-larger initial prompt, and repeated redispatches pay for the same old text again. Standing console sessions and CI-generated comments make that shape common rather than exceptional.

The desired invariant is: **a task may have unbounded durable history, but a dispatch has bounded continuation context.** The UI and store remain the complete record; the model receives only what it needs to continue.

## Conversation continuation

Split a task's conversation into two prompt components:

1. **Older summary.** Persist a compact summary of comments that have rolled out of the verbatim window. Advance it incrementally; do not ask a new run to summarize the whole historical thread again.
2. **Recent verbatim window.** Keep the newest comments exactly as written up to a byte/token budget. Budget bytes, not comment count: one pasted log can be larger than dozens of normal replies.

When comments move from the recent window into the summary, update the summary once and record the boundary it covers. A later dispatch reuses that summary plus only comments after the boundary. The summary must never contain secrets; the same rule that keeps secret values out of task comments applies here.

The rendered prompt should make the distinction explicit, e.g. `Earlier conversation (summary)` and `Recent conversation (verbatim)`, and say when older material was compacted.

A first implementation may use a conservative byte budget before model-specific token accounting exists. The important property is a hard upper bound on verbatim history.

## Structured state is not conversation

Do not depend on old comments to communicate state Grain already stores structurally. A dispatch should receive current state directly where useful, including:

- current Grain CI run/conclusion and commit;
- pending question/secret request;
- latest run outcome;
- merge-queue repair state.

Human-visible timeline events can remain. They are history, not the canonical machine-to-agent transport. A failure followed by a successful CI run must not keep an obsolete failure log alive in every future prompt merely because the old event remains in the conversation.

## CI output

`wait_for_checks` is a status operation. It should return run id, commit, overall conclusion, and job/step conclusions only. Log content belongs behind an explicit bounded `get_ci_log` call so polling cannot inject a noisy test suite into model context. This is implemented separately from conversation compaction so either change can land independently.

## Prompt accounting

Record prompt size for every `task_run`, with enough component accounting to identify regressions. Bytes are sufficient initially; estimated/model-reported tokens can be added where available.

At minimum account for:

- task title/body;
- Grain's base/runtime instructions;
- deployment/repo/task prompt extensions;
- memory and memory index;
- older conversation summary;
- recent verbatim conversation;
- previous-attempt history;
- checkout/setup/state-repo instructions;
- capability-specific sections;
- total prompt bytes.

Expose the total and breakdown on the run detail/prompt pane and in metrics. Also record model/CLI executions per Grain run and provider-capacity retries so a usage spike can be separated into `larger prompts` versus `more model executions`.

## Tests

The implementation should pin these properties:

- an arbitrarily long conversation cannot make the rendered verbatim section exceed its budget;
- newest comments survive intact and in order;
- an oversized single comment is bounded without invalid UTF-8;
- the prompt says older conversation was compacted rather than silently dropping it;
- summary advancement is monotonic and does not re-summarize already-covered comments;
- current structured CI state does not require replaying historical CI comments;
- per-component byte counts sum to the recorded total;
- a standing session can accumulate an arbitrarily long durable conversation while fresh-dispatch prompt size remains bounded.

## Rollout

Land the status/log CI split first. Then add conversation summary storage and the recent-window budget together: simply dropping old comments is a useful emergency brake but loses semantics, while a persisted summary gives long-lived sessions a stable continuation model. Prompt accounting should land with that change so the bound is observable from its first deployment.